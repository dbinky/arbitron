package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real impl must satisfy the port — a rename of either side breaks the build
// here. (Mirrors the fake's compile-time assertion in mcp_session_test.go.)
var _ mcpSession = (*mcpStdioSession)(nil)

// --- helpers ---------------------------------------------------------------

// notif builds an mcp.JSONRPCNotification whose params carry the given codex
// event fields (re-encoded through JSON exactly as the wire would deliver them),
// so the mapper is exercised against a realistic frame, not a hand-built struct.
func notif(t *testing.T, method string, payload map[string]any) mcp.JSONRPCNotification {
	t.Helper()
	n := mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: method,
			Params: mcp.NotificationParams{AdditionalFields: payload},
		},
	}
	// Round-trip through JSON so AdditionalFields decode the way the transport
	// hands them to OnNotification (numbers become float64, etc.).
	raw, err := json.Marshal(n)
	require.NoError(t, err)
	var back mcp.JSONRPCNotification
	require.NoError(t, json.Unmarshal(raw, &back))
	return back
}

// elicitParams builds an mcp.ElicitationParams whose codex approval payload rides
// the AdditionalFields of _meta (round-tripped through JSON like the wire).
func elicitParams(t *testing.T, message string, meta map[string]any) mcp.ElicitationParams {
	t.Helper()
	p := mcp.ElicitationParams{Message: message, Meta: &mcp.Meta{AdditionalFields: meta}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	var back mcp.ElicitationParams
	require.NoError(t, json.Unmarshal(raw, &back))
	return back
}

// --- mapNotification: HAPPY ------------------------------------------------

// HAPPY — an agent_message item notification maps to a turnToken carrying the
// assistant text.
func TestMapNotification_Happy_AgentMessageToken(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type": codexItemAgentMessage,
		"item": map[string]any{"type": codexItemAgentMessage, "text": "hello world"},
	})

	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnToken, ev.Kind)
	assert.Equal(t, "hello world", ev.Text)
}

// HAPPY — the terminal turn-completed notification maps to a turnDone carrying
// the conversation id + token counts.
func TestMapNotification_Happy_TurnCompletedDone(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type":            codexTurnCompleted,
		"conversation_id": "conv-xyz",
		"usage":           map[string]any{"input_tokens": 11, "output_tokens": 4},
	})

	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, "conv-xyz", ev.SessionID)
	assert.Equal(t, 11, ev.TokensIn)
	assert.Equal(t, 4, ev.TokensOut)
}

// --- mapNotification: SUCCESS ----------------------------------------------

// SUCCESS — a session-configured/started notification carries only the
// conversation id (no tokens, not terminal) and folds into a turnDone-less
// SessionID carrier. We surface it as a turnToken-free SessionID-only token so
// the id is captured even if the terminal frame omits it.
func TestMapNotification_Success_ConversationIDCaptured(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type":            codexTurnCompleted,
		"conversation_id": "conv-resumed",
	})

	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, "conv-resumed", ev.SessionID)
	assert.Equal(t, 0, ev.TokensIn)
	assert.Equal(t, 0, ev.TokensOut)
}

// --- mapNotification: FAILURE ----------------------------------------------

// FAILURE — a turn.failed notification maps to a turnFailed carrying the cause,
// never a clean turnDone.
func TestMapNotification_Failure_TurnFailed(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type":  codexTurnFailed,
		"error": map[string]any{"message": "model refused"},
	})

	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnFailed, ev.Kind)
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "model refused")
}

// FAILURE — a top-level `error` event (no nested error object) still fails with
// its message.
func TestMapNotification_Failure_TopLevelErrorMessage(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type":    codexErrorEvent,
		"message": "stream aborted",
	})

	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnFailed, ev.Kind)
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "stream aborted")
}

// --- mapNotification: ERROR ------------------------------------------------

// ERROR — a notification with a method we do not consume is ignored (ok=false),
// so unrelated MCP chatter never produces a spurious event.
func TestMapNotification_Error_UnknownMethodIgnored(t *testing.T) {
	n := notif(t, "notifications/message", map[string]any{"type": codexItemAgentMessage})
	_, ok := mapNotification(n)
	assert.False(t, ok, "an unrelated notification method must be ignored")
}

// ERROR — a codex event notification whose payload is not a recognised type is
// ignored rather than mis-mapped (forward-compat with future codex events).
func TestMapNotification_Error_UnknownEventTypeIgnored(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{"type": "task.queued"})
	_, ok := mapNotification(n)
	assert.False(t, ok, "an unknown codex event type must be ignored, not mis-mapped")
}

// ERROR — a codex-event notification whose payload cannot be decoded fails
// CLOSED as a terminal failure (ok=true, kind=turnFailed) rather than being
// silently dropped. A silently-dropped frame would let the stream end in a
// clean Done even though a malformed event arrived — a partial-success lie.
// We exercise this by injecting a non-marshalable value into AdditionalFields
// (bypassing the JSON-round-tripping notif() helper intentionally).
func TestMapNotification_Error_MalformedPayloadFailsClosed(t *testing.T) {
	n := mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: codexEventNotificationMethod,
			Params: mcp.NotificationParams{
				AdditionalFields: map[string]any{
					"type": make(chan int), // channels are not JSON-serialisable
				},
			},
		},
	}

	ev, ok := mapNotification(n)
	require.True(t, ok, "a malformed payload must NOT be silently dropped (fail closed)")
	assert.Equal(t, turnFailed, ev.Kind, "a malformed payload must produce a terminal failure, not a silent drop")
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "codex mcp-server")
}

// --- mapNotification: EDGE -------------------------------------------------

// EDGE — an agent_message with empty text is ignored: an empty increment must
// not be surfaced as a token.
func TestMapNotification_Edge_EmptyTextIgnored(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type": codexItemAgentMessage,
		"item": map[string]any{"type": codexItemAgentMessage, "text": ""},
	})
	_, ok := mapNotification(n)
	assert.False(t, ok, "an empty assistant-text increment must not be surfaced")
}

// EDGE — a terminal turn-completed notification missing token counts still maps
// to a turnDone (best-effort tokens default to zero, never an error).
func TestMapNotification_Edge_TerminalMissingTokens(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, map[string]any{
		"type":            codexTurnCompleted,
		"conversation_id": "conv-1",
	})
	ev, ok := mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, 0, ev.TokensIn)
	assert.Equal(t, 0, ev.TokensOut)
}

// --- mapApprovalRequest: HAPPY/SUCCESS -------------------------------------

// HAPPY — a command-exec approval payload maps to an approvalRequest carrying
// the argv + reason (WritePaths nil).
func TestMapApprovalRequest_Happy_CommandExec(t *testing.T) {
	p := elicitParams(t, "Allow running git status?", map[string]any{
		codexApprovalTypeKey:    codexApprovalTypeExec,
		codexApprovalCommandKey: []any{"git", "status"},
		codexApprovalReasonKey:  "inspect working tree",
	})

	req, err := mapApprovalRequest(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"git", "status"}, req.Command)
	assert.Nil(t, req.WritePaths)
	assert.Equal(t, "inspect working tree", req.Reason)
}

// SUCCESS — a patch/write approval payload maps to an approvalRequest carrying
// the target write paths + reason (Command nil).
func TestMapApprovalRequest_Success_PatchWrite(t *testing.T) {
	p := elicitParams(t, "Apply patch to docs/notes.md?", map[string]any{
		codexApprovalTypeKey:   codexApprovalTypePatch,
		codexApprovalPathsKey:  []any{"docs/notes.md", "docs/extra.md"},
		codexApprovalReasonKey: "write interview notes",
	})

	req, err := mapApprovalRequest(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"docs/notes.md", "docs/extra.md"}, req.WritePaths)
	assert.Nil(t, req.Command)
	assert.Equal(t, "write interview notes", req.Reason)
}

// --- mapApprovalRequest: FAILURE/ERROR -------------------------------------

// ERROR — an approval payload with neither a command nor write paths is a
// malformed request; the mapper fails closed so the transport can deny.
func TestMapApprovalRequest_Error_NoActionFailsClosed(t *testing.T) {
	p := elicitParams(t, "approve?", map[string]any{
		codexApprovalReasonKey: "unspecified",
	})
	_, err := mapApprovalRequest(p)
	require.Error(t, err)
}

// EDGE — when codex omits the reason, the human-readable Message is used as the
// reason so the trace is never blank.
func TestMapApprovalRequest_Edge_FallsBackToMessageReason(t *testing.T) {
	p := elicitParams(t, "Allow running ls?", map[string]any{
		codexApprovalTypeKey:    codexApprovalTypeExec,
		codexApprovalCommandKey: []any{"ls"},
	})
	req, err := mapApprovalRequest(p)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, req.Command)
	assert.Equal(t, "Allow running ls?", req.Reason)
}

// --- decisionToElicitationResult: HAPPY/FAILURE/EDGE -----------------------

// HAPPY — an allow decision becomes an "accept" elicitation result whose content
// carries the approval verdict + reason for the trace.
func TestDecisionToElicitationResult_Happy_Allow(t *testing.T) {
	res := decisionToElicitationResult(approvalDecision{Allow: true, Reason: "info command is safe"})
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionAccept, res.Action)

	content, ok := res.Content.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, content[codexApprovalDecisionKey])
	assert.Equal(t, "info command is safe", content[codexApprovalReasonKey])
}

// FAILURE — a deny decision becomes a "decline" elicitation result (codex must
// not proceed with the mutation), still carrying the recorded reason.
func TestDecisionToElicitationResult_Failure_Deny(t *testing.T) {
	res := decisionToElicitationResult(approvalDecision{Allow: false, Reason: "writes to source are denied"})
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionDecline, res.Action)

	content, ok := res.Content.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, content[codexApprovalDecisionKey])
	assert.Equal(t, "writes to source are denied", content[codexApprovalReasonKey])
}

// EDGE — a deny with an empty reason still declines and produces a non-nil
// result (never a dropped/garbled response that would wedge codex).
func TestDecisionToElicitationResult_Edge_EmptyReasonDeny(t *testing.T) {
	res := decisionToElicitationResult(approvalDecision{Allow: false})
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionDecline, res.Action)
}

// --- startToolRequest: SECURITY / argument assertions ----------------------

// HAPPY — a fresh conversation: the start tool request carries the model, prompt
// (via the protocol body, NEVER argv), and the read-only sandbox + approval
// posture, with no resume id.
func TestStartToolRequest_Happy_FreshConversation(t *testing.T) {
	req := startToolRequest(turnConfig{
		Model:   "gpt-5",
		Prompt:  "interview me",
		WorkDir: "/work/dir",
	})

	assert.Equal(t, codexStartToolName, req.Params.Name)
	args, ok := req.Params.Arguments.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gpt-5", args[codexArgModel])
	assert.Equal(t, "interview me", args[codexArgPrompt])
	assert.Equal(t, "/work/dir", args[codexArgCwd])
	assert.Equal(t, codexSandboxReadOnly, args[codexArgSandboxMode])
	assert.Equal(t, codexApprovalPolicyOnRequest, args[codexArgApprovalPolicy])
	_, hasResume := args[codexArgResumeID]
	assert.False(t, hasResume, "a fresh conversation must not carry a resume id")
	_, hasEffort := args[codexArgEffort]
	assert.False(t, hasEffort, "an empty effort must emit nothing")
}

// SUCCESS — resume: the prior conversation id rides the start call, the prompt is
// still in the protocol body, and a non-empty effort is threaded through.
func TestStartToolRequest_Success_ResumeAndEffort(t *testing.T) {
	req := startToolRequest(turnConfig{
		Model:    "gpt-5",
		Prompt:   "continue",
		WorkDir:  "/work/dir",
		ResumeID: "conv-abc",
		Effort:   "high",
	})

	args, ok := req.Params.Arguments.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "conv-abc", args[codexArgResumeID])
	assert.Equal(t, "high", args[codexArgEffort])
	assert.Equal(t, "continue", args[codexArgPrompt])
}

// EDGE — an empty effort string emits no effort key at all (preserving the
// existing "default/empty effort passes nothing" behaviour).
func TestStartToolRequest_Edge_EmptyEffortOmitted(t *testing.T) {
	req := startToolRequest(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", Effort: ""})
	args := req.Params.Arguments.(map[string]any)
	_, has := args[codexArgEffort]
	assert.False(t, has)
}

// --- mcpStdioSession.Start: ERROR (fail-closed before any subprocess) ------

// ERROR — an incompatible-binary probe fails closed: Start returns the probe
// error and NO stream, before any conversation is launched. The probe runs
// through the shared core seam (a fake runner), so no live codex is needed.
func TestMCPStdioSession_Error_IncompatibleProbeFailsClosed(t *testing.T) {
	// A runner whose `--version` reply is below minCodexVersion → ErrIncompatibleCodex.
	c := newCore(Config{
		BinaryPath: "/nonexistent/codex",
		Runner:     &fakeRunner{lines: []string{"codex-cli 0.1.0"}},
	})
	s := newMCPStdioSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{
		Model:   "gpt-5",
		Prompt:  "p",
		WorkDir: "/w",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
	assert.Nil(t, stream, "an incompatible binary must yield no stream")
}

// ERROR — an empty WorkDir fails closed before the probe even runs (the same
// sandbox-root invariant the exec path guards), with no stream.
func TestMCPStdioSession_Error_EmptyWorkDirFailsClosed(t *testing.T) {
	runner := &fakeRunner{}
	c := newCore(Config{BinaryPath: "/nonexistent/codex", Runner: runner})
	s := newMCPStdioSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Nil(t, stream)
	assert.False(t, runner.runCalled, "WorkDir guard must fire before the probe — probe must never run")
}

// ERROR — probe passes (compatible binary) but the actual mcp-server launch
// fails because the binary does not exist at exec time. Start must return
// (nil, err) — fail closed — and the error must not be ErrWorkDirRequired or
// ErrIncompatibleCodex (those come from earlier guards).
func TestMCPStdioSession_Error_LaunchFailsAfterCompatibleProbe(t *testing.T) {
	c := newCore(Config{
		BinaryPath: "/nonexistent/codex",
		Runner:     &multiRunner{scripts: []runScript{compatibleProbe()}},
	})
	s := newMCPStdioSession(&c)

	stream, err := s.Start(context.Background(), turnConfig{
		Model:   "gpt-5",
		Prompt:  "p",
		WorkDir: "/w",
	})
	require.Error(t, err)
	assert.Nil(t, stream, "a launch failure after a compatible probe must yield no stream")
	assert.NotErrorIs(t, err, ErrWorkDirRequired)
	assert.NotErrorIs(t, err, ErrIncompatibleCodex)
	assert.Contains(t, err.Error(), "codex mcp-server", "error must identify the launch site")
}

// --- elicitationApprover: the handler glue (no subprocess) -----------------

// HAPPY — the elicitation approver translates a codex exec-approval request into
// an approvalRequest, calls the injected Approver, and returns the allow verdict
// as an accept result — with the transport making NO judgement of its own.
func TestElicitationApprover_Happy_PlumbsToApprover(t *testing.T) {
	var got approvalRequest
	approver := func(req approvalRequest) approvalDecision {
		got = req
		return approvalDecision{Allow: true, Reason: "ok"}
	}
	h := &elicitationApprover{approver: approver}

	p := elicitParams(t, "Allow git status?", map[string]any{
		codexApprovalTypeKey:    codexApprovalTypeExec,
		codexApprovalCommandKey: []any{"git", "status"},
		codexApprovalReasonKey:  "inspect",
	})
	res, err := h.Elicit(context.Background(), mcp.ElicitationRequest{Params: p})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionAccept, res.Action)
	assert.Equal(t, []string{"git", "status"}, got.Command)
}

// ERROR — a malformed approval payload (no action) fails closed: the approver is
// NEVER consulted and the handler declines, so codex cannot proceed.
func TestElicitationApprover_Error_MalformedDeclinesWithoutApprover(t *testing.T) {
	called := false
	approver := func(req approvalRequest) approvalDecision {
		called = true
		return approvalDecision{Allow: true}
	}
	h := &elicitationApprover{approver: approver}

	p := elicitParams(t, "approve?", map[string]any{codexApprovalReasonKey: "huh"})
	res, err := h.Elicit(context.Background(), mcp.ElicitationRequest{Params: p})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionDecline, res.Action)
	assert.False(t, called, "a malformed approval request must fail closed without consulting the policy")
}

// EDGE — a nil Approver (mis-wired provider) fails closed: the handler declines
// rather than panicking or silently allowing.
func TestElicitationApprover_Edge_NilApproverDeclines(t *testing.T) {
	h := &elicitationApprover{approver: nil}
	p := elicitParams(t, "Allow ls?", map[string]any{
		codexApprovalTypeKey:    codexApprovalTypeExec,
		codexApprovalCommandKey: []any{"ls"},
	})
	res, err := h.Elicit(context.Background(), mcp.ElicitationRequest{Params: p})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, mcp.ElicitationResponseActionDecline, res.Action)
}

// --- streamFromEvents: the in-memory turnStream the handler feeds -----------

// HAPPY — the event-channel stream yields buffered events in order, then the
// terminal errStreamDone once the channel closes.
func TestEventStream_Happy_DrainsThenDone(t *testing.T) {
	ch := make(chan turnEvent, 3)
	ch <- turnEvent{Kind: turnToken, Text: "a"}
	ch <- turnEvent{Kind: turnToken, Text: "b"}
	ch <- turnEvent{Kind: turnDone, SessionID: "c1"}
	close(ch)
	s := newEventStream(ch, &terminalError{})

	ev, err := s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "a", ev.Text)
	ev, err = s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "b", ev.Text)
	ev, err = s.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, turnDone, ev.Kind)

	_, err = s.Next(context.Background())
	assert.ErrorIs(t, err, errStreamDone)
}

// EDGE — a cancelled context makes Next return the context error rather than
// blocking forever on an event that never arrives (no leak).
func TestEventStream_Edge_ContextCancelled(t *testing.T) {
	ch := make(chan turnEvent) // never fed
	s := newEventStream(ch, &terminalError{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Next(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

// ERROR — a stream constructed with a terminal closeErr surfaces it on drain
// (e.g. a broken transport), not a clean errStreamDone.
func TestEventStream_Error_CloseErrSurfaces(t *testing.T) {
	transportErr := errors.New("codex mcp-server: stdio closed")
	ch := make(chan turnEvent)
	close(ch)
	term := &terminalError{}
	term.set(transportErr)
	s := newEventStream(ch, term)

	_, err := s.Next(context.Background())
	assert.ErrorIs(t, err, transportErr)
}
