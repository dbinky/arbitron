package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real impl must satisfy the port — a rename of either side breaks the build
// here. (Mirrors the fake's compile-time assertion in mcp_session_test.go.)
var _ mcpSession = (*mcpStdioSession)(nil)

// --- helpers ---------------------------------------------------------------

// notif builds an mcp.JSONRPCNotification whose params are the given codex/event
// params object, decoded through mcp-go's NotificationParams.UnmarshalJSON exactly
// as the transport hands them to OnNotification — so the MCP-reserved `_meta` key
// is routed to Params.Meta and the rest (e.g. `msg`) to AdditionalFields, matching
// the wire. Pass the full {_meta?, msg} envelope.
func notif(t *testing.T, method string, params map[string]any) mcp.JSONRPCNotification {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	var np mcp.NotificationParams
	require.NoError(t, json.Unmarshal(raw, &np))
	return mcp.JSONRPCNotification{
		JSONRPC:      mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{Method: method, Params: np},
	}
}

// codexEvt wraps a msg body (and optional _meta threadId) in the verified
// codex/event params envelope.
func codexEvt(msg map[string]any) map[string]any {
	return map[string]any{"msg": msg}
}

// codexEvtWithThread wraps a msg body plus a _meta.threadId.
func codexEvtWithThread(threadID string, msg map[string]any) map[string]any {
	return map[string]any{"_meta": map[string]any{"threadId": threadID}, "msg": msg}
}

// --- mapNotification: HAPPY ------------------------------------------------

// HAPPY — an agent_message event maps to a turnText carrying the assistant text
// from msg.message.
func TestMapNotification_Happy_AgentMessageToken(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type":    codexMsgAgentMessage,
		"message": "hello world",
	}))

	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnText, ev.Kind)
	assert.Equal(t, "hello world", ev.Text)
}

// HAPPY — the terminal task_complete event maps to a turnDone carrying the thread
// id (from _meta.threadId) + the tokens accumulated from a prior token_count.
func TestMapNotification_Happy_TaskCompleteDoneWithTokens(t *testing.T) {
	m := &eventMapper{}

	// token_count arrives before task_complete (codex sends them as separate events).
	tc := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type": codexMsgTokenCount,
		"info": map[string]any{"total_token_usage": map[string]any{"input_tokens": 11, "output_tokens": 4}},
	}))
	_, ok := m.mapNotification(tc)
	assert.False(t, ok, "a token_count is usage-only and surfaces no event")

	done := notif(t, codexEventNotificationMethod, codexEvtWithThread("conv-xyz", map[string]any{
		"type":               codexMsgTaskComplete,
		"last_agent_message": "all done",
	}))
	ev, ok := m.mapNotification(done)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, "conv-xyz", ev.SessionID)
	assert.Equal(t, 11, ev.TokensIn)
	assert.Equal(t, 4, ev.TokensOut)
}

// --- mapNotification: SUCCESS ----------------------------------------------

// SUCCESS — task_complete without a preceding token_count still maps to a
// turnDone carrying the thread id (tokens default to zero, never an error).
func TestMapNotification_Success_TaskCompleteNoTokens(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvtWithThread("conv-resumed", map[string]any{
		"type": codexMsgTaskComplete,
	}))

	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, "conv-resumed", ev.SessionID)
	assert.Equal(t, 0, ev.TokensIn)
	assert.Equal(t, 0, ev.TokensOut)
}

// --- mapNotification: FAILURE ----------------------------------------------

// FAILURE — an error event maps to a turnFailed carrying the cause, never a clean
// turnDone.
func TestMapNotification_Failure_ErrorEvent(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type":    codexMsgError,
		"message": "model refused",
	}))

	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnFailed, ev.Kind)
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "model refused")
}

// FAILURE — a stream_error event with a nested error object still fails with its
// message.
func TestMapNotification_Failure_StreamErrorNestedMessage(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type":  codexMsgStreamError,
		"error": map[string]any{"message": "stream aborted"},
	}))

	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnFailed, ev.Kind)
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "stream aborted")
}

// --- mapNotification: ERROR ------------------------------------------------

// ERROR — a notification with a method we do not consume is ignored (ok=false),
// so unrelated MCP chatter never produces a spurious event.
func TestMapNotification_Error_UnknownMethodIgnored(t *testing.T) {
	n := notif(t, "notifications/message", codexEvt(map[string]any{"type": codexMsgAgentMessage}))
	_, ok := (&eventMapper{}).mapNotification(n)
	assert.False(t, ok, "an unrelated notification method must be ignored")
}

// ERROR — a codex event whose msg.type is not one we surface (e.g. session_configured,
// item_started, a future event) is ignored rather than mis-mapped (forward-compat).
func TestMapNotification_Error_UnknownEventTypeIgnored(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{"type": "session_configured"}))
	_, ok := (&eventMapper{}).mapNotification(n)
	assert.False(t, ok, "an unknown codex event type must be ignored, not mis-mapped")
}

// ERROR — a codex-event notification whose payload cannot be decoded fails CLOSED
// as a terminal failure (ok=true, kind=turnFailed) rather than being silently
// dropped. We exercise this by injecting a non-marshalable value into
// AdditionalFields (bypassing the JSON-round-tripping notif() helper).
func TestMapNotification_Error_MalformedPayloadFailsClosed(t *testing.T) {
	n := mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: codexEventNotificationMethod,
			Params: mcp.NotificationParams{
				AdditionalFields: map[string]any{
					"msg": make(chan int), // channels are not JSON-serialisable
				},
			},
		},
	}

	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok, "a malformed payload must NOT be silently dropped (fail closed)")
	assert.Equal(t, turnFailed, ev.Kind, "a malformed payload must produce a terminal failure, not a silent drop")
	require.Error(t, ev.Err)
	assert.Contains(t, ev.Err.Error(), "codex mcp-server")
}

// --- mapNotification: EDGE -------------------------------------------------

// EDGE — an error event with no error object and no message still produces a
// turnFailed with a non-empty cause (turnError's "turn failed" fallback).
func TestMapNotification_Edge_ErrorNoMessageFallback(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type": codexMsgError,
		// no "error" object, no "message" field
	}))
	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnFailed, ev.Kind)
	require.Error(t, ev.Err)
	assert.NotEmpty(t, ev.Err.Error(), "an error event with no message must never produce an empty cause")
}

// EDGE — an agent_message with empty text is ignored: an empty increment must not
// be surfaced as a token.
func TestMapNotification_Edge_EmptyTextIgnored(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type":    codexMsgAgentMessage,
		"message": "",
	}))
	_, ok := (&eventMapper{}).mapNotification(n)
	assert.False(t, ok, "an empty assistant-text increment must not be surfaced")
}

// EDGE — a task_complete carrying only msg.thread_id (no _meta.threadId) still
// surfaces a SessionID so session resume is not lost (the threadID fallback).
func TestMapNotification_Edge_TaskCompleteThreadIDFallback(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type":      codexMsgTaskComplete,
		"thread_id": "thread-abc",
	}))
	ev, ok := (&eventMapper{}).mapNotification(n)
	require.True(t, ok)
	assert.Equal(t, turnDone, ev.Kind)
	assert.Equal(t, "thread-abc", ev.SessionID, "msg.thread_id must be used when _meta.threadId is absent")
}

// EDGE — a token_count event surfaces NO turn event (it only updates the running
// usage that a later task_complete reports).
func TestMapNotification_Edge_TokenCountSurfacesNoEvent(t *testing.T) {
	n := notif(t, codexEventNotificationMethod, codexEvt(map[string]any{
		"type": codexMsgTokenCount,
		"info": map[string]any{"total_token_usage": map[string]any{"input_tokens": 7, "output_tokens": 2}},
	}))
	_, ok := (&eventMapper{}).mapNotification(n)
	assert.False(t, ok, "a token_count is usage-only and must surface no event")
}

// --- startToolRequest: SECURITY / argument assertions ----------------------

// HAPPY — a fresh conversation: the start tool request targets the `codex` tool
// and carries the model, prompt (via the protocol body, NEVER argv), cwd, and the
// workspace-write + no-approval posture, with no resume id.
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
	assert.Equal(t, codexSandboxWorkspaceWrite, args[codexArgSandbox])
	assert.Equal(t, codexApprovalPolicyNever, args[codexArgApprovalPolicy])
	_, hasResume := args[codexArgConversationID]
	assert.False(t, hasResume, "a fresh conversation must not carry a resume id")
}

// SUCCESS — resume: the call targets the `codex-reply` tool with the prior
// conversation id + prompt; the model/sandbox carry from the original session and
// are NOT re-sent.
func TestStartToolRequest_Success_ResumeViaCodexReply(t *testing.T) {
	req := startToolRequest(turnConfig{
		Model:    "gpt-5",
		Prompt:   "continue",
		WorkDir:  "/work/dir",
		ResumeID: "conv-abc",
	})

	assert.Equal(t, codexReplyToolName, req.Params.Name, "a resume must target the codex-reply tool")
	args, ok := req.Params.Arguments.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "conv-abc", args[codexArgConversationID])
	assert.Equal(t, "continue", args[codexArgPrompt])
	_, hasModel := args[codexArgModel]
	assert.False(t, hasModel, "codex-reply does not re-send the model — it carries from the session")
	_, hasSandbox := args[codexArgSandbox]
	assert.False(t, hasSandbox, "codex-reply does not re-send the sandbox posture")
}

// EDGE — effort is ignored on the mcp-server path (codex 0.139 has no verified
// top-level reasoning-effort arg): a non-empty Effort emits no extra key and the
// fresh-conversation posture is unchanged.
func TestStartToolRequest_Edge_EffortIgnored(t *testing.T) {
	req := startToolRequest(turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w", Effort: "high"})
	args := req.Params.Arguments.(map[string]any)
	assert.Equal(t, codexStartToolName, req.Params.Name)
	// No effort key in any form.
	_, hasEffort := args["model_reasoning_effort"]
	assert.False(t, hasEffort, "effort is not emitted on the mcp-server path")
	assert.Len(t, args, 5, "fresh-conversation args are exactly model/prompt/cwd/sandbox/approval-policy")
}

// --- mcpStdioSession.Start: ERROR (fail-closed before any subprocess) ------

// ERROR — an incompatible-binary probe fails closed: Start returns the probe
// error and NO stream, before any conversation is launched.
func TestMCPStdioSession_Error_IncompatibleProbeFailsClosed(t *testing.T) {
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

// ERROR — probe passes (compatible binary) but the actual mcp-server launch fails
// because the binary does not exist at exec time. Start must return (nil, err) —
// fail closed — and not ErrWorkDirRequired / ErrIncompatibleCodex.
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

// PAIRED (FA#20) — the version probe for the interactive mcp-server path uses
// codexEnv() (the no-key subscription env), sharing the invariant with the exec
// path: OPENAI_API_KEY in the parent env must NOT reach the probe subprocess.
func TestMCPStdioSession_Probe_SubscriptionEnv_NoAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-not-be-forwarded")

	mr := &multiRunner{scripts: []runScript{compatibleProbe()}}
	c := newCore(Config{BinaryPath: "/nonexistent/codex", Runner: mr})
	s := newMCPStdioSession(&c)

	_, _ = s.Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})

	require.Len(t, mr.calls, 1, "exactly one runner call — the version probe")
	assert.Contains(t, mr.calls[0].Args, "--version", "call must be the version probe")

	_, hasKey := envValue(mr.calls[0].Env, "OPENAI_API_KEY")
	assert.False(t, hasKey,
		"mcp-server path probe must use codexEnv() — OPENAI_API_KEY must not be forwarded to the version probe subprocess")
}

// --- verifyInitializePosture: fail-closed handshake self-check -------------
//
// The workspace-write sandbox containment rests on the peer being a real codex
// mcp-server that honours the `sandbox` arg. The post-Initialize self-check
// confirms serverInfo.name == codexServerName and fails CLOSED otherwise. (The
// original check asserted a SERVER-advertised elicitation capability, which is
// structurally always absent — elicitation is a CLIENT capability in MCP — so it
// could never pass against any real codex build.)

// SUCCESS — a handshake whose serverInfo identifies codex mcp-server passes.
func TestVerifyInitializePosture_Success_CodexPeer(t *testing.T) {
	res := &mcp.InitializeResult{
		ServerInfo: mcp.Implementation{Name: codexServerName, Version: "0.139.0"},
	}
	assert.NoError(t, verifyInitializePosture(res),
		"a handshake identifying codex mcp-server confirms the workspace-write sandbox arg is honoured")
}

// SECURITY / FAIL-CLOSED — a handshake from a non-codex (or unnamed) peer must
// fail closed: an arbitrary MCP server might ignore our `sandbox` arg and run a
// turn unconfined.
func TestVerifyInitializePosture_Security_NonCodexPeer_FailsClosed(t *testing.T) {
	res := &mcp.InitializeResult{
		ServerInfo: mcp.Implementation{Name: "some-other-mcp-server"},
	}
	err := verifyInitializePosture(res)
	require.Error(t, err, "a non-codex peer must fail closed")
	assert.ErrorIs(t, err, ErrUnverifiedPosture)
}

// SECURITY / FAIL-CLOSED — a nil InitializeResult (a transport that returned no
// negotiated handshake) must also fail closed, never be treated as confirmed.
func TestVerifyInitializePosture_Security_NilResult_FailsClosed(t *testing.T) {
	err := verifyInitializePosture(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnverifiedPosture)
}

// --- sendTurnEvent: backpressure (no silent drop) --------------------------

// BACKPRESSURE — when the buffer is full the send must BLOCK (slow the producer)
// until the consumer drains, then deliver the frame — NOT silently discard it.
func TestSendTurnEvent_Backpressure_BlocksThenDelivers(t *testing.T) {
	events := make(chan turnEvent, 1)
	events <- turnEvent{Kind: turnText, Text: "first"} // buffer now full

	go func() {
		time.Sleep(10 * time.Millisecond)
		<-events
	}()

	ok := sendTurnEvent(context.Background(), events, turnEvent{Kind: turnText, Text: "second"}, slog.Default())
	assert.True(t, ok, "a full buffer must block until drained, not drop the frame")

	got := <-events
	assert.Equal(t, "second", got.Text, "the back-pressured frame must be delivered, not lost")
}

// DROP + WARN — if the consumer is gone (ctx cancelled) and the buffer is full,
// the send must NOT block forever: it gives up and emits a slog.Warn so the true
// drop is observable. It must return false (frame not delivered).
func TestSendTurnEvent_ConsumerGone_WarnsAndGivesUp(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	events := make(chan turnEvent, 1)
	events <- turnEvent{Kind: turnText, Text: "stuck"} // full, and nobody draining

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the consumer has torn down

	ok := sendTurnEvent(ctx, events, turnEvent{Kind: turnText, Text: "lost"}, logger)
	assert.False(t, ok, "a send to a gone consumer must give up, not block forever")
	assert.Contains(t, buf.String(), "level=WARN", "a true drop must be logged at WARN")
	assert.Contains(t, buf.String(), "drop", "the warn must identify the dropped frame")
}

// HAPPY — with free buffer space the send delivers immediately and returns true.
func TestSendTurnEvent_Happy_DeliversImmediately(t *testing.T) {
	events := make(chan turnEvent, 4)
	ok := sendTurnEvent(context.Background(), events, turnEvent{Kind: turnText, Text: "hi"}, slog.Default())
	assert.True(t, ok)
	assert.Equal(t, "hi", (<-events).Text)
}

// --- eventStream: the in-memory turnStream the handler feeds ----------------

// HAPPY — the event-channel stream yields buffered events in order, then the
// terminal errStreamDone once the channel closes.
func TestEventStream_Happy_DrainsThenDone(t *testing.T) {
	ch := make(chan turnEvent, 3)
	ch <- turnEvent{Kind: turnText, Text: "a"}
	ch <- turnEvent{Kind: turnText, Text: "b"}
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

// --- protocol constants: value pinning (VERIFIED against codex-cli 0.139) ---
//
// The mapper tests use constants in both the input payload AND the expected-value
// assertions, so if a constant drifts the mappers silently use the wrong value
// and the mapper tests still pass. These TYPE tests pin the exact verified
// strings so any change is a deliberate, test-driven decision.

// TYPE — tool names + the notification method (verified via tools/list + live turns).
func TestProtocolConstants_ToolsAndMethodPinned(t *testing.T) {
	assert.Equal(t, "codex", codexStartToolName, "start-conversation tool name")
	assert.Equal(t, "codex-reply", codexReplyToolName, "resume-conversation tool name")
	assert.Equal(t, "codex/event", codexEventNotificationMethod, "streaming-event notification method")
	assert.Equal(t, "codex-mcp-server", codexServerName, "serverInfo.name of a genuine codex mcp-server")
}

// SECURITY — the start-tool arg keys + posture values. A wrong key silently drops
// the workspace-write sandbox constraint (codex would run in its default posture).
func TestProtocolConstants_PostureArgsPinned(t *testing.T) {
	assert.Equal(t, "sandbox", codexArgSandbox, "sandbox arg key (was wrongly sandbox_mode)")
	assert.Equal(t, "approval-policy", codexArgApprovalPolicy, "approval-policy arg key (hyphen, was wrongly approval_policy)")
	assert.Equal(t, "workspace-write", codexSandboxWorkspaceWrite, "worktree-confined write posture")
	assert.Equal(t, "never", codexApprovalPolicyNever, "no per-mutation approval loop")
	assert.Equal(t, "conversationId", codexArgConversationID, "codex-reply resume arg key")
}

// TYPE — the codex/event msg.type discriminators that drive event folding.
func TestProtocolConstants_EventTypesPinned(t *testing.T) {
	assert.Equal(t, "agent_message", codexMsgAgentMessage, "final assistant text event")
	assert.Equal(t, "task_complete", codexMsgTaskComplete, "terminal turn-success event (was wrongly turn.completed)")
	assert.Equal(t, "token_count", codexMsgTokenCount, "usage event (was wrongly a usage field)")
	assert.Equal(t, "error", codexMsgError, "terminal error event")
	assert.Equal(t, "stream_error", codexMsgStreamError, "terminal stream-error event")
}
