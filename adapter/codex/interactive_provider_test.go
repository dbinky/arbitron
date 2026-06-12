package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// The interactive provider must satisfy the engine port — a rename of either
// side breaks the build here.
func TestInteractiveProviderImplementsPort(t *testing.T) {
	var _ engine.ChatProvider = (*InteractiveProvider)(nil)
}

// newInteractiveTestProvider builds an InteractiveProvider whose mcpSession is
// the supplied fake, with a binding model and no key reveal. It mirrors how the
// exec stream tests inject a fake runner.
func newInteractiveTestProvider(t *testing.T, fake *fakeMCPSession, binding provider.ResolvedBinding) *InteractiveProvider {
	t.Helper()
	return newInteractiveProviderWithSession(
		InteractiveConfig{Binding: binding},
		func() mcpSession { return fake },
	)
}

// tokenEv / doneEv / failedEv build scripted turnEvents for the fake stream.
func tokenEv(text string) turnEvent { return turnEvent{Kind: turnToken, Text: text} }
func doneEv(sid string, in, out int) turnEvent {
	return turnEvent{Kind: turnDone, SessionID: sid, TokensIn: in, TokensOut: out}
}
func failedEv(err error) turnEvent { return turnEvent{Kind: turnFailed, Err: err} }

// --- HAPPY PATH ------------------------------------------------------------

// HAPPY — a scripted [token, token, done{c1}] yields the tokens in order then a
// single terminal Done carrying the conversation id + token counts; the
// turnConfig carries the WorkDir + model and an installed Approver.
func TestInteractive_StreamChat_HappyPath(t *testing.T) {
	revealed := false
	fake := &fakeMCPSession{events: []turnEvent{
		tokenEv("hello "),
		tokenEv("world"),
		doneEv("conv-1", 11, 5),
	}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", &revealed))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "say hi", WorkDir: "/tmp/ws",
	})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	// Tokens in order, then exactly one terminal Done.
	require.Len(t, chunks, 3)
	assert.Equal(t, "hello ", chunks[0].Token)
	assert.Equal(t, "world", chunks[1].Token)
	assert.False(t, chunks[0].Done)
	assert.False(t, chunks[1].Done)

	last := chunks[2]
	assert.True(t, last.Done)
	assert.Equal(t, "conv-1", last.SessionID)
	assert.Equal(t, 11, last.TokensIn)
	assert.Equal(t, 5, last.TokensOut)
	assert.NoError(t, last.Error)

	// turnConfig carried the WorkDir + binding model and an installed Approver.
	assert.True(t, fake.startCalled)
	assert.Equal(t, "/tmp/ws", fake.gotCfg.WorkDir)
	assert.Equal(t, "say hi", fake.gotCfg.Prompt)
	assert.Equal(t, "gpt-5-codex", fake.gotCfg.Model)
	assert.NotNil(t, fake.gotCfg.Approver, "the provider must install its approval policy")

	// SECURITY: the no-key subscription path never reveals a key.
	assert.False(t, revealed, "codex interactive must never reveal an API key")
}

// HAPPY — Chat folds the same turn to completion into a ChatResponse with the
// resolved model, conversation id, output text, and token counts.
func TestInteractive_Chat_HappyPath(t *testing.T) {
	revealed := false
	fake := &fakeMCPSession{events: []turnEvent{
		tokenEv("hello "),
		tokenEv("world"),
		doneEv("conv-1", 7, 3),
	}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", &revealed))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "hello world", resp.Output)
	assert.Equal(t, "conv-1", resp.SessionID)
	assert.Equal(t, "gpt-5-codex", resp.Model)
	assert.Equal(t, 7, resp.TokensIn)
	assert.Equal(t, 3, resp.TokensOut)

	// SECURITY: the no-key subscription path must never reveal a key from either fold.
	assert.False(t, revealed, "codex interactive (Chat fold) must never reveal an API key")
}

// --- SUCCESS ---------------------------------------------------------------

// SUCCESS — a non-empty req.SessionID flows to the turn's ResumeID so the same
// codex conversation continues across human turns.
func TestInteractive_StreamChat_ResumeID(t *testing.T) {
	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "continue", WorkDir: "/tmp/ws", SessionID: "conv-1",
	})
	require.NoError(t, err)
	collectChunks(t, ch)

	assert.Equal(t, "conv-1", fake.gotCfg.ResumeID, "req.SessionID must flow to turnConfig.ResumeID")
}

// SUCCESS — a non-empty req.SessionID flows to the Chat fold's turnConfig.ResumeID
// so the same codex conversation resumes when Chat is used instead of StreamChat.
// The startTurn prelude is shared, but a refactor that bypassed it for Chat could
// silently break resume — this test pins the plumbing symmetrically.
func TestInteractive_Chat_ResumeID(t *testing.T) {
	fake := &fakeMCPSession{events: []turnEvent{
		tokenEv("ok"),
		doneEv("conv-1", 5, 3),
	}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{
		Prompt: "continue", WorkDir: "/tmp/ws", SessionID: "conv-1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "conv-1", fake.gotCfg.ResumeID, "req.SessionID must flow to turnConfig.ResumeID in the Chat fold")
	assert.Equal(t, "conv-1", resp.SessionID, "terminal SessionID must round-trip back out through Chat")
}

// SUCCESS — a non-empty req.Model overrides the binding's model.
func TestInteractive_StreamChat_RequestModelOverridesBinding(t *testing.T) {
	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("binding-model", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "request-model",
	})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "request-model", fake.gotCfg.Model)
}

// SUCCESS — an informational-command approval request is ALLOWED.
func TestInteractive_Approval_InformationalCommand_Allowed(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{Command: []string{"git", "status"}, Reason: "check state"}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.True(t, fake.decisions[0].Allow, "a read-only/informational command must be allowed")
}

// SUCCESS — a write whose every path is under docs/ is ALLOWED.
func TestInteractive_Approval_DocsWrite_Allowed(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{WritePaths: []string{"docs/specs/feature.md"}, Reason: "deliverable"}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.True(t, fake.decisions[0].Allow, "a docs/-scoped write must be allowed")
}

// SUCCESS — the deliverable git commit is ALLOWED.
func TestInteractive_Approval_Commit_Allowed(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{Command: []string{"git", "commit", "-m", "docs: deliverable"}, Reason: "commit"}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.True(t, fake.decisions[0].Allow, "the deliverable commit must be allowed")
}

// SUCCESS — a configured capability ruleset floors+emits the effort value onto
// the turnConfig (the same effective value the exec path emits). Default/empty
// effort emits nothing.
func TestInteractive_StreamChat_EffortEmitted(t *testing.T) {
	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"codex": {
			Backend:      "codex",
			EffortLevels: []provider.Effort{provider.EffortLow, provider.EffortMedium, provider.EffortHigh},
		},
	})
	binding := codexBinding("gpt-5-codex", nil)
	binding.Backend = "codex"
	binding.Effort = provider.EffortHigh

	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: binding, CapabilityRules: rules},
		func() mcpSession { return fake },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "high", fake.gotCfg.Effort, "the floored effort value must ride the turnConfig")
}

// SUCCESS — with no capability ruleset (Empty), the turnConfig carries no effort
// (default emits nothing), preserving back-compat.
func TestInteractive_StreamChat_NoEffortByDefault(t *testing.T) {
	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "", fake.gotCfg.Effort, "an Empty ruleset must emit no effort")
}

// --- FAILURE ---------------------------------------------------------------

// FAILURE — a turnFailed frame closes the stream with a terminal Error chunk and
// NO Done; the deliverable is never reported as written. The chunk's error must
// wrap the underlying turn error so callers can use errors.Is to identify the
// specific failure cause (rate-limit, auth, model error, etc.).
func TestInteractive_StreamChat_TurnFailed_ErrorNoDone(t *testing.T) {
	turnErr := errors.New("model overloaded")
	fake := &fakeMCPSession{events: []turnEvent{
		tokenEv("partial"),
		failedEv(turnErr),
	}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	assert.Error(t, last.Error, "a failed turn must surface a terminal error chunk")
	assert.ErrorIs(t, last.Error, turnErr, "the error chunk must wrap the underlying turn error (%%w, not %%v)")
	for _, c := range chunks {
		assert.False(t, c.Done, "a failed turn must never emit a clean Done")
	}
}

// FAILURE / CROSS-CUTTING — a turnDone arriving after a turnFailed must never
// produce a clean Done chunk (fail-closed drain: the `failed` flag gates all
// events that arrive after the failure). A misbehaving transport could send
// both a turnFailed and a later turnDone; removing the `if failed { continue }`
// guard would silently upgrade the failure to a success.
func TestInteractive_StreamChat_TurnDoneAfterTurnFailed_NoDoneChunk(t *testing.T) {
	turnErr := errors.New("rate limited")
	fake := &fakeMCPSession{events: []turnEvent{
		failedEv(turnErr),
		doneEv("conv-1", 10, 5), // spurious turnDone after failure — must be suppressed
	}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks, "at least the error chunk must arrive")
	last := chunks[len(chunks)-1]
	assert.ErrorIs(t, last.Error, turnErr, "the failure error must be surfaced")
	for _, c := range chunks {
		assert.False(t, c.Done, "a spurious turnDone after turnFailed must NOT produce a Done chunk")
	}
}

// FAILURE / CROSS-CUTTING — a transport error arriving AFTER a turnFailed must
// produce exactly ONE error chunk, not two. The `failed` flag gates the transport-
// error send path (`if !failed { sendChunk(...) }`) so the consumer never sees a
// second error event from the same turn. Without the `!failed` guard, a double-
// error would escape: the consumer would receive both the turn-failure chunk and a
// transport-error chunk, violating the single-terminal-event contract and
// confusing any caller that inspects the last chunk.
func TestInteractive_StreamChat_TransportErrorAfterTurnFailed_SingleErrorOnly(t *testing.T) {
	turnErr := errors.New("rate limited")
	transportErr := errors.New("codex mcp-server: stdio broke")
	sess := &errStreamSession{pre: []turnEvent{failedEv(turnErr)}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() mcpSession { return sess },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	var errChunks []engine.ChatChunk
	for _, c := range chunks {
		if c.Error != nil {
			errChunks = append(errChunks, c)
		}
		assert.False(t, c.Done, "a failure sequence must never produce a Done chunk")
	}
	require.Len(t, errChunks, 1, "exactly one error chunk — transport error after turnFailed must be suppressed")
	assert.ErrorIs(t, errChunks[0].Error, turnErr, "the surfaced error must be the turn failure, not the later transport error")
}

// FAILURE — Chat surfaces a failed turn as a hard error (no silent empty result).
// The returned error must wrap the underlying turn error so callers can use
// errors.Is for retry/classification logic.
func TestInteractive_Chat_TurnFailed_HardError(t *testing.T) {
	turnErr := errors.New("boom")
	fake := &fakeMCPSession{events: []turnEvent{failedEv(turnErr)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Error(t, err)
	assert.ErrorIs(t, err, turnErr, "Chat must wrap the underlying turn error (%%w, not %%v)")
	assert.Nil(t, resp, "a failed turn must not return a partial ChatResponse")
}

// FAILURE — Chat fails closed when the stream ends with errStreamDone but no
// turnDone event was ever received (e.g. an immediately-closed or aborted turn).
// Without the sawDone guard, Chat would silently return an empty ChatResponse
// instead of an error — a vacuous-success that masks a broken session.
func TestInteractive_Chat_NoTerminalDone_HardError(t *testing.T) {
	sess := &errStreamSession{nextErr: errStreamDone}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() mcpSession { return sess },
	)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Error(t, err, "a stream that ends without a turnDone must return an error")
	assert.Contains(t, err.Error(), "turn ended without completion")
	assert.Nil(t, resp, "no partial ChatResponse must escape when turnDone was never received")
}

// --- ERROR -----------------------------------------------------------------

// ERROR — a Start error makes StreamChat return (nil, err): no half-open stream.
func TestInteractive_StreamChat_StartError(t *testing.T) {
	wantErr := errors.New("codex mcp-server: start failed")
	fake := &fakeMCPSession{startErr: wantErr}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, ch, "a Start error must yield no stream")
	assert.ErrorIs(t, err, wantErr)
}

// ERROR — a Start error makes Chat return (nil, err).
func TestInteractive_Chat_StartError(t *testing.T) {
	wantErr := errors.New("codex mcp-server: start failed")
	fake := &fakeMCPSession{startErr: wantErr}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, wantErr)
}

// ERROR — a non-sentinel transport error mid-stream makes Chat return (nil, err).
// Mirrors TestInteractive_StreamChat_TransportError_ErrorNoDone for the Chat fold.
// The Chat fold's transport-error branch (`return nil, fmt.Errorf("...: %w", nerr)`)
// must wrap the error so callers can identify the cause; an errored Chat must never
// return a partial result.
func TestInteractive_Chat_TransportError_HardError(t *testing.T) {
	transportErr := errors.New("codex mcp-server: stdio broke")
	sess := &errStreamSession{pre: []turnEvent{tokenEv("partial")}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() mcpSession { return sess },
	)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp, "a transport break must not return a partial ChatResponse")
	assert.Error(t, err)
	assert.ErrorIs(t, err, transportErr, "Chat must wrap the transport error so callers can identify it")
}

// errStreamSession is a local test mcpSession whose stream yields some events and
// then a NON-sentinel transport error from Next (a broken stdio mid-turn), which
// the shared scripted fake cannot express. It exercises the provider's
// fail-closed fold of an arbitrary Next error.
type errStreamSession struct {
	pre     []turnEvent
	nextErr error
	gotCfg  turnConfig
}

func (s *errStreamSession) Start(_ context.Context, cfg turnConfig) (turnStream, error) {
	s.gotCfg = cfg
	return &errTurnStream{pre: s.pre, nextErr: s.nextErr}, nil
}

type errTurnStream struct {
	pre     []turnEvent
	idx     int
	nextErr error
}

func (s *errTurnStream) Next(_ context.Context) (turnEvent, error) {
	if s.idx < len(s.pre) {
		ev := s.pre[s.idx]
		s.idx++
		return ev, nil
	}
	return turnEvent{}, s.nextErr
}

// ERROR — a non-sentinel transport error mid-stream becomes a terminal Error
// chunk (no Done), and the drainer still completes.
func TestInteractive_StreamChat_TransportError_ErrorNoDone(t *testing.T) {
	transportErr := errors.New("codex mcp-server: stdio broke")
	sess := &errStreamSession{pre: []turnEvent{tokenEv("partial")}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() mcpSession { return sess },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	assert.ErrorIs(t, last.Error, transportErr)
	for _, c := range chunks {
		assert.False(t, c.Done, "a transport break must never emit a clean Done")
	}
}

// --- EDGE CASE (mandatory guardrail) ---------------------------------------

// EDGE / MANDATORY — codex tries to patch a SOURCE path outside docs/ during the
// interview. The policy DENIES it, the decision is traced (recorded via the
// fake's decision log), the stream continues to its clean Done (no spurious
// suppression), and nothing is surfaced to the human (no Error chunk, no Done
// suppression — denials are an operator-only signal).
func TestInteractive_Approval_SourceWriteDuringInterview_Denied(t *testing.T) {
	fake := &fakeMCPSession{
		events: []turnEvent{
			tokenEv("let me update Settings"),
			doneEv("conv-1", 0, 0),
		},
		approvals: []approvalRequest{
			{WritePaths: []string{"domain/x/Settings.tsx"}, Reason: "wire the toggle"},
		},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	// The guardrail fired: the source write was denied.
	require.Len(t, fake.decisions, 1)
	assert.False(t, fake.decisions[0].Allow, "a write to a source path outside docs/ must be denied")
	assert.NotEmpty(t, fake.decisions[0].Reason, "the denial must carry a reason for the trace")

	// The interview continued: the token flowed and the turn reached a clean Done.
	require.Len(t, chunks, 2)
	assert.Equal(t, "let me update Settings", chunks[0].Token)
	assert.True(t, chunks[1].Done, "a denied mutation must NOT suppress the turn's Done")

	// No human-facing surfacing: no chunk carries an Error for the denial.
	for _, c := range chunks {
		assert.NoError(t, c.Error, "a guardrail denial must never surface as a human-facing error")
	}
}

// EDGE — a multi-file patch with one docs/ path and one source path is denied
// wholesale (one outside path denies the whole patch).
func TestInteractive_Approval_MixedPatch_Denied(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{WritePaths: []string{"docs/specs/ok.md", "internal/secret.go"}}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.False(t, fake.decisions[0].Allow, "a mixed patch with any non-docs path must be denied wholesale")
}

// EDGE — a ..-escape path resolving outside WorkDir/docs is denied (fail closed).
func TestInteractive_Approval_DotDotEscape_Denied(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{WritePaths: []string{"docs/../../etc/passwd"}}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.False(t, fake.decisions[0].Allow, "a ..-escape path must be denied")
}

// EDGE — an unknown approval kind (neither command nor write paths) is denied by
// default (default-deny / fail closed).
func TestInteractive_Approval_UnknownKind_Denied(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{Reason: "some brand-new approval kind"}},
	}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.False(t, fake.decisions[0].Allow, "an unknown approval kind must be denied")
}

// EDGE — empty WorkDir fails closed with ErrWorkDirRequired and NO stream; the
// session is never even started.
// SECURITY — the no-key invariant holds on this error path too: RevealKey must
// not be called even before the guard, because startTurn touches p.binding only
// for Model (field read, no RevealKey).
func TestInteractive_StreamChat_EmptyWorkDir(t *testing.T) {
	revealed := false
	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", &revealed))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: ""})
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.False(t, fake.startCalled, "an empty WorkDir must not start a session")
	assert.False(t, revealed, "the no-key invariant must hold on error paths — RevealKey must never be called")
}

// EDGE — empty WorkDir fails closed on Chat too.
// SECURITY — RevealKey must not be called even on this early error path.
func TestInteractive_Chat_EmptyWorkDir(t *testing.T) {
	revealed := false
	fake := &fakeMCPSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", &revealed))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: ""})
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.False(t, revealed, "the no-key invariant must hold on error paths — RevealKey must never be called")
}

// EDGE — a cancelled context mid-stream unblocks the sender and reaps the child;
// no clean Done is emitted.
func TestInteractive_StreamChat_ContextCancel(t *testing.T) {
	fake := &fakeMCPSession{blockOnCtx: true}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.StreamChat(ctx, engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	cancel()

	chunks := collectChunks(t, ch)
	for _, c := range chunks {
		assert.False(t, c.Done, "a cancelled stream must not emit a clean Done")
	}
}

// EDGE — a cancelled context makes Chat return a context error, not a partial
// result.
func TestInteractive_Chat_ContextCancel(t *testing.T) {
	fake := &fakeMCPSession{blockOnCtx: true}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := p.Chat(ctx, engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- CONSTRUCTOR -----------------------------------------------------------

// CONSTRUCTOR — the public NewInteractiveProvider defaults WritableRoot to docs
// when empty and builds the shared core via newCore (same binary resolution as
// the exec adapters). A nil session factory is non-nil (defaults to the real
// stdio session).
func TestNewInteractiveProvider_Defaults(t *testing.T) {
	p := NewInteractiveProvider(InteractiveConfig{
		Binding:    codexBinding("gpt-5-codex", nil),
		BinaryPath: "codex",
	})
	require.NotNil(t, p)
	assert.Equal(t, defaultWritableRoot, p.writableRoot, "empty WritableRoot must default to docs")
	assert.Equal(t, "codex", p.binPath, "core must resolve the binary like the exec adapters")
	require.NotNil(t, p.newSession, "a nil session factory must default to the real stdio session")
}

// CONSTRUCTOR — an explicit WritableRoot is honoured.
func TestNewInteractiveProvider_ExplicitWritableRoot(t *testing.T) {
	p := NewInteractiveProvider(InteractiveConfig{
		Binding:      codexBinding("gpt-5-codex", nil),
		BinaryPath:   "codex",
		WritableRoot: "deliverables",
	})
	assert.Equal(t, "deliverables", p.writableRoot)
}

// CONSTRUCTOR — the policy installed by the provider uses the provider's
// writableRoot. A write under the configured root is allowed; a write under the
// default docs/ (which is NOT the configured root) is denied.
func TestInteractive_Policy_UsesConfiguredWritableRoot(t *testing.T) {
	fake := &fakeMCPSession{
		events:    []turnEvent{doneEv("conv-1", 0, 0)},
		approvals: []approvalRequest{{WritePaths: []string{"deliverables/out.md"}}},
	}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil), WritableRoot: "deliverables"},
		func() mcpSession { return fake },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.Len(t, fake.decisions, 1)
	assert.True(t, fake.decisions[0].Allow, "a write under the configured writable root must be allowed")
}

// --- SECURITY: approvalSummary log-safety --------------------------------

// SECURITY — approvalSummary produces the "summary" field in the slog trace
// emitted by tracingApprover. It must NEVER include req.Reason (codex's
// human-readable explanation for the request, which may contain prompt-derived
// content — user intent, partial transcript, sensitive context). Only structural
// facts (argv or paths) are safe to log. This test verifies all three branches.
func TestApprovalSummary_NeverLogsRequestReason(t *testing.T) {
	secret := "the user asked me to implement the login button"

	// command branch — only the argv, never req.Reason
	got := approvalSummary(approvalRequest{
		Command: []string{"git", "status"},
		Reason:  secret,
	})
	assert.Contains(t, got, "git status", "summary must identify the command argv")
	assert.NotContains(t, got, secret, "summary must never include req.Reason for a command request")

	// write branch — only the paths, never req.Reason
	got = approvalSummary(approvalRequest{
		WritePaths: []string{"docs/spec.md"},
		Reason:     secret,
	})
	assert.Contains(t, got, "docs/spec.md", "summary must identify the write paths")
	assert.NotContains(t, got, secret, "summary must never include req.Reason for a write request")

	// unrecognized branch — fallback string only, never req.Reason
	got = approvalSummary(approvalRequest{Reason: secret})
	assert.NotContains(t, got, secret, "summary must never include req.Reason for an unrecognized request")
}
