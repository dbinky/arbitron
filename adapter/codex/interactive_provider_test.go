package codex

import (
	"context"
	"errors"
	"testing"
	"time"

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

// newInteractiveTestProvider builds an InteractiveProvider whose turnSession is
// the supplied fake, with a binding model and no key reveal. It mirrors how the
// exec stream tests inject a fake runner.
func newInteractiveTestProvider(t *testing.T, fake *fakeTurnSession, binding provider.ResolvedBinding) *InteractiveProvider {
	t.Helper()
	return newInteractiveProviderWithSession(
		InteractiveConfig{Binding: binding},
		func() turnSession { return fake },
	)
}

// tokenEv / doneEv / failedEv build scripted turnEvents for the fake stream.
func tokenEv(text string) turnEvent { return turnEvent{Kind: turnText, Text: text} }
func doneEv(sid string, in, out int) turnEvent {
	return turnEvent{Kind: turnDone, SessionID: sid, TokensIn: in, TokensOut: out}
}
func failedEv(err error) turnEvent { return turnEvent{Kind: turnFailed, Err: err} }

// --- HAPPY PATH ------------------------------------------------------------

// HAPPY — a scripted [token, token, done{c1}] yields the tokens in order then a
// single terminal Done carrying the conversation id + token counts; the
// turnConfig carries the WorkDir + model.
func TestInteractive_StreamChat_HappyPath(t *testing.T) {
	revealed := false
	fake := &fakeTurnSession{events: []turnEvent{
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

	// turnConfig carried the WorkDir + binding model.
	assert.True(t, fake.startCalled)
	assert.Equal(t, "/tmp/ws", fake.gotCfg.WorkDir)
	assert.Equal(t, "say hi", fake.gotCfg.Prompt)
	assert.Equal(t, "gpt-5-codex", fake.gotCfg.Model)

	// SECURITY: the no-key subscription path never reveals a key.
	assert.False(t, revealed, "codex interactive must never reveal an API key")
}

// HAPPY — Chat folds the same turn to completion into a ChatResponse with the
// resolved model, conversation id, output text, and token counts.
func TestInteractive_Chat_HappyPath(t *testing.T) {
	revealed := false
	fake := &fakeTurnSession{events: []turnEvent{
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
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "continue", WorkDir: "/tmp/ws", SessionID: "conv-1",
	})
	require.NoError(t, err)
	collectChunks(t, ch)

	assert.Equal(t, "conv-1", fake.gotCfg.ResumeID, "req.SessionID must flow to turnConfig.ResumeID")
}

// PAIRED (FA#15) — full session-id round-trip through the transport seam: the
// req.SessionID flows in as turnConfig.ResumeID (resume), and the terminal
// doneEv.SessionID flows back out as ChatChunk.SessionID (the new id for the
// next resume). Tests both directions of the seam in one scenario.
func TestInteractive_StreamChat_SessionIDRoundTrip(t *testing.T) {
	// Terminal done carries a DIFFERENT id from the resume id (codex can rotate
	// the conversation id after certain events — the provider must pass through
	// whatever the transport returns, not reflect the input id back).
	fake := &fakeTurnSession{events: []turnEvent{doneEv("new-conv-id", 5, 3)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "next turn", WorkDir: "/tmp/ws", SessionID: "old-conv-id",
	})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	// Forward: req.SessionID → turnConfig.ResumeID.
	assert.Equal(t, "old-conv-id", fake.gotCfg.ResumeID, "req.SessionID must flow to turnConfig.ResumeID")
	// Back-out: doneEv.SessionID → ChatChunk.SessionID on the terminal Done chunk.
	last := chunks[len(chunks)-1]
	assert.True(t, last.Done, "session-id round-trip test must reach a terminal Done")
	assert.Equal(t, "new-conv-id", last.SessionID, "terminal ChatChunk must carry the new session id from the done event, not the resume id")
}

// SUCCESS — a non-empty req.SessionID flows to the Chat fold's turnConfig.ResumeID
// so the same codex conversation resumes when Chat is used instead of StreamChat.
// The startTurn prelude is shared, but a refactor that bypassed it for Chat could
// silently break resume — this test pins the plumbing symmetrically.
func TestInteractive_Chat_ResumeID(t *testing.T) {
	fake := &fakeTurnSession{events: []turnEvent{
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

// SUCCESS / LIFECYCLE — spawn-per-turn: each StreamChat call must create a fresh
// turnSession via the factory. If the provider cached the session (e.g. sync.Once),
// the second turn would share an exhausted stream from the first. The factory call
// count must equal the number of turns.
func TestInteractive_SpawnPerTurn_FreshSessionEachStreamChat(t *testing.T) {
	var callCount int
	factory := func() turnSession {
		callCount++
		return &fakeTurnSession{events: []turnEvent{doneEv("conv-x", 1, 1)}}
	}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		factory,
	)
	req := engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"}

	ch1, err := p.StreamChat(context.Background(), req)
	require.NoError(t, err)
	collectChunks(t, ch1)

	ch2, err := p.StreamChat(context.Background(), req)
	require.NoError(t, err)
	collectChunks(t, ch2)

	assert.Equal(t, 2, callCount, "spawn-per-turn: two StreamChat calls must create two independent sessions, not reuse one")
}

// SUCCESS — a non-empty req.Model overrides the binding's model.
func TestInteractive_StreamChat_RequestModelOverridesBinding(t *testing.T) {
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("binding-model", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "request-model",
	})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "request-model", fake.gotCfg.Model)
}

// SUCCESS / COMPOSITION-ROOT — a provider constructed with an empty Binding.Model
// (the composition-root pattern: no static binding, model injected per-request by
// the engine from the profile binding) uses req.Model as the turn model. This is
// the EXACT wiring in initDomain: NewInteractiveProvider(InteractiveConfig{...})
// with no Binding, so the engine-provided req.Model is the sole model source.
func TestInteractive_StreamChat_EmptyBindingModel_UsesRequestModel(t *testing.T) {
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	// Empty Binding (no Model) — mirrors the composition root's NewInteractiveProvider call.
	p := newInteractiveProviderWithSession(
		InteractiveConfig{},
		func() turnSession { return fake },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5.5", // model from engine/profile
	})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "gpt-5.5", fake.gotCfg.Model, "req.Model must be used when Binding.Model is empty")
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

	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: binding, CapabilityRules: rules},
		func() turnSession { return fake },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.Equal(t, "high", fake.gotCfg.Effort, "the floored effort value must ride the turnConfig")
}

// SUCCESS — with no capability ruleset (Empty), the turnConfig carries no effort
// (default emits nothing), preserving back-compat.
func TestInteractive_StreamChat_NoEffortByDefault(t *testing.T) {
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
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
	fake := &fakeTurnSession{events: []turnEvent{
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
	fake := &fakeTurnSession{events: []turnEvent{
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
	transportErr := errors.New("codex exec: stream broke")
	sess := &errStreamSession{pre: []turnEvent{failedEv(turnErr)}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() turnSession { return sess },
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
	fake := &fakeTurnSession{events: []turnEvent{failedEv(turnErr)}}
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
		func() turnSession { return sess },
	)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Error(t, err, "a stream that ends without a turnDone must return an error")
	assert.Contains(t, err.Error(), "turn ended without completion")
	assert.Nil(t, resp, "no partial ChatResponse must escape when turnDone was never received")
}

// --- ERROR -----------------------------------------------------------------

// ERROR — a Start error makes StreamChat return (nil, err): no half-open stream.
func TestInteractive_StreamChat_StartError(t *testing.T) {
	wantErr := errors.New("codex exec: start failed")
	fake := &fakeTurnSession{startErr: wantErr}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, ch, "a Start error must yield no stream")
	assert.ErrorIs(t, err, wantErr)
}

// ERROR — a Start error makes Chat return (nil, err).
func TestInteractive_Chat_StartError(t *testing.T) {
	wantErr := errors.New("codex exec: start failed")
	fake := &fakeTurnSession{startErr: wantErr}
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
	transportErr := errors.New("codex exec: stream broke")
	sess := &errStreamSession{pre: []turnEvent{tokenEv("partial")}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() turnSession { return sess },
	)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp, "a transport break must not return a partial ChatResponse")
	assert.Error(t, err)
	assert.ErrorIs(t, err, transportErr, "Chat must wrap the transport error so callers can identify it")
}

// errStreamSession is a local test turnSession whose stream yields some events and
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
	transportErr := errors.New("codex exec: stream broke")
	sess := &errStreamSession{pre: []turnEvent{tokenEv("partial")}, nextErr: transportErr}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)},
		func() turnSession { return sess },
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

// --- EDGE CASE -------------------------------------------------------------

// EDGE — empty WorkDir fails closed with ErrWorkDirRequired and NO stream; the
// session is never even started.
// SECURITY — the no-key invariant holds on this error path too: RevealKey must
// not be called even before the guard, because startTurn touches p.binding only
// for Model (field read, no RevealKey).
func TestInteractive_StreamChat_EmptyWorkDir(t *testing.T) {
	revealed := false
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
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
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", &revealed))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: ""})
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.False(t, revealed, "the no-key invariant must hold on error paths — RevealKey must never be called")
}

// EDGE — a cancelled context mid-stream unblocks the sender and reaps the child;
// no clean Done is emitted.
func TestInteractive_StreamChat_ContextCancel(t *testing.T) {
	fake := &fakeTurnSession{blockOnCtx: true}
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
	fake := &fakeTurnSession{blockOnCtx: true}
	p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := p.Chat(ctx, engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- TIMEOUT (hang vector regression) --------------------------------------

// HANG / REGRESSION — a turn whose transport never emits a terminal frame must be
// killed by the per-turn deadline, not park forever. The fake blocks Next on the
// turn ctx; with a tiny Timeout the derived deadline must fire, the stream must
// yield NO clean Done, and StreamChat must terminate (the channel closes) rather
// than hang. Before the fix Timeout was dead config and this would block forever.
func TestInteractive_StreamChat_PerTurnTimeout_KillsWedgedTurn(t *testing.T) {
	fake := &fakeTurnSession{blockOnCtx: true} // Next blocks until the turn ctx is done
	p := newInteractiveProviderWithSession(
		InteractiveConfig{
			Binding: codexBinding("gpt-5-codex", nil),
			Timeout: 20 * time.Millisecond, // the per-turn deadline under test
		},
		func() turnSession { return fake },
	)

	// A background ctx with NO deadline of its own: the ONLY bound is p.Timeout, so
	// this test fails (hangs) if the timeout is not wired into startTurn.
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)

	chunks := collectChunks(t, ch) // returns only when the channel closes
	for _, c := range chunks {
		assert.False(t, c.Done, "a turn killed by the per-turn deadline must never emit a clean Done")
	}
}

// HANG / REGRESSION — the Chat fold honours the per-turn deadline too: a wedged
// turn returns a context.DeadlineExceeded error (no partial result), not a hang.
func TestInteractive_Chat_PerTurnTimeout_KillsWedgedTurn(t *testing.T) {
	fake := &fakeTurnSession{blockOnCtx: true}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{
			Binding: codexBinding("gpt-5-codex", nil),
			Timeout: 20 * time.Millisecond,
		},
		func() turnSession { return fake },
	)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	assert.Nil(t, resp, "a deadline-killed turn must not return a partial result")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "a wedged turn must be killed by the per-turn deadline")
}

// SUCCESS — a non-positive Timeout (the default) imposes no deadline: a normal
// turn with a terminal frame completes cleanly, proving the wrap degrades to the
// parent ctx (procenv.WithTimeout no-op) rather than spuriously cancelling.
func TestInteractive_StreamChat_ZeroTimeout_NoSpuriousDeadline(t *testing.T) {
	fake := &fakeTurnSession{events: []turnEvent{tokenEv("hi"), doneEv("conv-1", 1, 1)}}
	p := newInteractiveProviderWithSession(
		InteractiveConfig{Binding: codexBinding("gpt-5-codex", nil)}, // Timeout zero
		func() turnSession { return fake },
	)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)
	last := chunks[len(chunks)-1]
	assert.True(t, last.Done, "a zero Timeout must not impose a spurious deadline on a normal turn")
}

// --- CONSTRUCTOR -----------------------------------------------------------

// CONSTRUCTOR — the public NewInteractiveProvider builds the shared core via
// newCore (same binary resolution as the exec adapters), and a nil session factory
// defaults to the real stdio session.
func TestNewInteractiveProvider_Defaults(t *testing.T) {
	p := NewInteractiveProvider(InteractiveConfig{
		Binding:    codexBinding("gpt-5-codex", nil),
		BinaryPath: "codex",
	})
	require.NotNil(t, p)
	assert.Equal(t, "codex", p.binPath, "core must resolve the binary like the exec adapters")
	require.NotNil(t, p.newSession, "a nil session factory must default to the real stdio session")
}

// A pinned request (RequireExecutedModel) must reach the transport as
// turnConfig.VerifyModel, or the rollout proof never runs.
func TestInteractive_StreamChat_PinnedRequestForwardsVerifyModel(t *testing.T) {
	for _, pinned := range []bool{true, false} {
		fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
		p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))
		ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
			Prompt: "p", WorkDir: "/tmp/ws", Model: "gpt-5-codex", RequireExecutedModel: pinned,
		})
		require.NoError(t, err)
		collectChunks(t, ch)
		assert.Equal(t, pinned, fake.gotCfg.VerifyModel)
	}
}

// A tool-free request (SuppressTools, the usability probe) must reach the
// transport as a read-only turn; an ordinary request keeps the workspace-write
// posture.
func TestInteractive_StreamChat_SuppressToolsForwardsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeModelsCache(t, home, "gpt-5-codex")
	for _, suppress := range []bool{true, false} {
		fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
		p := newInteractiveTestProvider(t, fake, codexBinding("gpt-5-codex", nil))
		ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
			Prompt: "p", WorkDir: "/tmp/ws", Model: "gpt-5-codex", SuppressTools: suppress,
		})
		require.NoError(t, err)
		collectChunks(t, ch)
		assert.Equal(t, suppress, fake.gotCfg.ReadOnly)
	}
}

// D14: a pinned binding verifies the executed model on every interactive turn
// whatever the request says, and the request may not name another model.
func TestInteractive_PinnedBindingDemandsVerificationAndItsOwnModel(t *testing.T) {
	pinned := codexBinding("gpt-5-codex", nil)
	pinned.Pinned = true

	t.Run("request flag absent still verifies", func(t *testing.T) {
		for _, model := range []string{"gpt-5-codex", ""} {
			fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
			p := newInteractiveTestProvider(t, fake, pinned)
			ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "p", WorkDir: "/tmp/ws", Model: model})
			require.NoError(t, err)
			collectChunks(t, ch)
			assert.True(t, fake.gotCfg.VerifyModel, "model %q", model)
			assert.Equal(t, "gpt-5-codex", fake.gotCfg.Model)
		}
	})
	t.Run("chat verifies too", func(t *testing.T) {
		fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
		p := newInteractiveTestProvider(t, fake, pinned)
		_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "p", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
		require.NoError(t, err)
		assert.True(t, fake.gotCfg.VerifyModel)
	})
	t.Run("another request model never starts a turn", func(t *testing.T) {
		fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
		p := newInteractiveTestProvider(t, fake, pinned)
		ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "p", WorkDir: "/tmp/ws", Model: "gpt-5-mini"})
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Nil(t, ch)
		assert.False(t, fake.startCalled)
	})
}

func TestInteractive_ToolScopeAndContextReachTurnConfig(t *testing.T) {
	b := codexBinding("gpt-5-codex", nil)
	b.Backend, b.ContextWindow = "codex", provider.Context1M
	fake := &fakeTurnSession{events: []turnEvent{doneEv("conv-1", 0, 0)}}
	p := newInteractiveProviderWithSession(InteractiveConfig{Binding: b}, func() turnSession { return fake })
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read", "Bash"}})
	require.NoError(t, err)
	collectChunks(t, ch)
	assert.True(t, fake.gotCfg.Tools.Restricted)
	assert.True(t, fake.gotCfg.Tools.ReadOnly)
	assert.Equal(t, []string{"-c", "model_context_window=1000000"}, fake.gotCfg.ContextArgs)

	_, err = p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", ToolScope: []string{"Read"}})
	require.ErrorIs(t, err, ErrToolScopeUnsupported, "refused before any turn starts")
}
