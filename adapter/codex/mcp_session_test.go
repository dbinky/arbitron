package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fake must satisfy the port — a downstream rename of either side is caught
// here at compile time.
var _ mcpSession = (*fakeMCPSession)(nil)

// The stream the fake returns must also satisfy the port — catches a missing
// method if turnStream ever grows beyond Next.
var _ turnStream = (*fakeTurnStream)(nil)

// drainStream replays a turnStream to completion, collecting events until the
// terminal errStreamDone sentinel. A non-terminal error is returned for the
// caller to assert (failure / context-cancel paths). It mirrors the fold
// discipline Phase 2 will use over a real stream.
func drainStream(t *testing.T, ctx context.Context, s turnStream) ([]turnEvent, error) {
	t.Helper()
	var out []turnEvent
	for {
		ev, err := s.Next(ctx)
		if errors.Is(err, errStreamDone) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}

// HAPPY — a script [token "Hi", token " there", done{SessionID:"c1"}] replays in
// order via Next, and gotCfg captures the prompt + workdir handed to Start.
func TestFakeMCPSession_HappyPath_ReplaysEventsInOrder(t *testing.T) {
	f := &fakeMCPSession{events: []turnEvent{
		{Kind: turnText, Text: "Hi"},
		{Kind: turnText, Text: " there"},
		{Kind: turnDone, SessionID: "c1", TokensIn: 7, TokensOut: 3},
	}}

	stream, err := f.Start(context.Background(), turnConfig{
		Model:   "gpt-5",
		Prompt:  "interview me",
		WorkDir: "/work/dir",
	})
	require.NoError(t, err)
	require.NotNil(t, stream)

	events, err := drainStream(t, context.Background(), stream)
	require.NoError(t, err)

	require.Len(t, events, 3)
	assert.Equal(t, turnText, events[0].Kind)
	assert.Equal(t, "Hi", events[0].Text)
	assert.Equal(t, turnText, events[1].Kind)
	assert.Equal(t, " there", events[1].Text)
	assert.Equal(t, turnDone, events[2].Kind)
	assert.Equal(t, "c1", events[2].SessionID)
	assert.Equal(t, 7, events[2].TokensIn)
	assert.Equal(t, 3, events[2].TokensOut)

	// Start recorded the config for argument assertions.
	assert.True(t, f.startCalled)
	assert.Equal(t, "interview me", f.gotCfg.Prompt)
	assert.Equal(t, "/work/dir", f.gotCfg.WorkDir)
	assert.Equal(t, "gpt-5", f.gotCfg.Model)
}

// SUCCESS — ResumeID set on the turnConfig is recorded, and a one-request
// approval script invokes the caller's Approver and records its decision.
func TestFakeMCPSession_Success_RecordsResumeAndApproval(t *testing.T) {
	var gotReq approvalRequest
	approver := func(req approvalRequest) approvalDecision {
		gotReq = req
		return approvalDecision{Allow: true, Reason: "info command is safe"}
	}

	f := &fakeMCPSession{
		approvals: []approvalRequest{{
			Command: []string{"git", "status"},
			Reason:  "inspect working tree",
		}},
		events: []turnEvent{{Kind: turnDone, SessionID: "c2"}},
	}

	_, err := f.Start(context.Background(), turnConfig{
		Model:    "gpt-5",
		Prompt:   "continue",
		WorkDir:  "/work/dir",
		ResumeID: "conv-abc",
		Approver: approver,
		Effort:   "high",
	})
	require.NoError(t, err)

	// Resume id + effort threaded through to the transport.
	assert.Equal(t, "conv-abc", f.gotCfg.ResumeID)
	assert.Equal(t, "high", f.gotCfg.Effort)

	// The approval request reached the policy, and its decision was recorded.
	assert.Equal(t, []string{"git", "status"}, gotReq.Command)
	assert.Equal(t, "inspect working tree", gotReq.Reason)
	require.Len(t, f.decisions, 1)
	assert.True(t, f.decisions[0].Allow)
	assert.Equal(t, "info command is safe", f.decisions[0].Reason)
}

// SUCCESS — multiple approval requests with different verdicts (allow, allow, deny)
// are ALL delivered to the approver in order and each decision is recorded
// separately. This also proves a denial does not terminate the event stream —
// the stream still reaches a clean Done, modelling the FA#14 mandatory scenario.
func TestFakeMCPSession_Success_MultipleApprovalsAllDeliveredInOrder(t *testing.T) {
	requests := []approvalRequest{
		{Command: []string{"git", "log", "--oneline"}, Reason: "inspect history"},
		{WritePaths: []string{"docs/specs/interview.md"}, Reason: "write deliverable"},
		{WritePaths: []string{"internal/core.go"}, Reason: "apply source patch"},
	}

	var gotReqs []approvalRequest
	approver := func(req approvalRequest) approvalDecision {
		gotReqs = append(gotReqs, req)
		if len(req.WritePaths) > 0 && req.WritePaths[0] == "internal/core.go" {
			return approvalDecision{Allow: false, Reason: "source write denied"}
		}
		return approvalDecision{Allow: true, Reason: "ok"}
	}

	f := &fakeMCPSession{
		approvals: requests,
		events:    []turnEvent{{Kind: turnDone, SessionID: "c3"}},
	}

	stream, err := f.Start(context.Background(), turnConfig{
		Prompt:   "describe the design",
		WorkDir:  "/project",
		Approver: approver,
	})
	require.NoError(t, err)

	// All three requests delivered in order.
	require.Len(t, gotReqs, 3)
	assert.Equal(t, requests[0], gotReqs[0])
	assert.Equal(t, requests[1], gotReqs[1])
	assert.Equal(t, requests[2], gotReqs[2])

	// Decisions: allow, allow, deny — recorded separately.
	require.Len(t, f.decisions, 3)
	assert.True(t, f.decisions[0].Allow)
	assert.True(t, f.decisions[1].Allow)
	assert.False(t, f.decisions[2].Allow)
	assert.Equal(t, "source write denied", f.decisions[2].Reason)

	// Denial does not terminate the stream — it still reaches Done.
	events, drainErr := drainStream(t, context.Background(), stream)
	require.NoError(t, drainErr)
	require.Len(t, events, 1)
	assert.Equal(t, turnDone, events[0].Kind)
	assert.Equal(t, "c3", events[0].SessionID)
}

// FAILURE — a turnFailed terminal event surfaces its error from Next (a failed
// turn is never a clean done).
func TestFakeMCPSession_Failure_TurnFailedSurfacesError(t *testing.T) {
	turnErr := errors.New("turn.failed: model refused")
	f := &fakeMCPSession{events: []turnEvent{
		{Kind: turnText, Text: "partial"},
		{Kind: turnFailed, Err: turnErr},
	}}

	stream, err := f.Start(context.Background(), turnConfig{Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)

	// First event is the partial token.
	ev, err := stream.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, turnText, ev.Kind)
	assert.Equal(t, "partial", ev.Text)

	// Second event is the terminal failure carrying the cause.
	ev, err = stream.Next(context.Background())
	require.NoError(t, err)
	assert.Equal(t, turnFailed, ev.Kind)
	assert.ErrorIs(t, ev.Err, turnErr)

	// After the terminal failure, Next reports errStreamDone — never a clean Done.
	// A failed turn must never silently become a success on a subsequent poll.
	_, err = stream.Next(context.Background())
	assert.ErrorIs(t, err, errStreamDone, "Next after turnFailed must return errStreamDone, not a turnDone")
}

// ERROR — startErr makes Start fail closed: (nil, err) with no stream.
func TestFakeMCPSession_Error_StartErrYieldsNoStream(t *testing.T) {
	startErr := errors.New("codex mcp-server: launch failed")
	f := &fakeMCPSession{startErr: startErr}

	stream, err := f.Start(context.Background(), turnConfig{Prompt: "p", WorkDir: "/w"})
	require.Error(t, err)
	assert.ErrorIs(t, err, startErr)
	assert.Nil(t, stream)

	// Start was still recorded as attempted (config captured for assertions).
	assert.True(t, f.startCalled)
	assert.Equal(t, "p", f.gotCfg.Prompt)
}

// EDGE — three boundary conditions:
//   - an empty script reports terminal completion immediately (no block);
//   - blockOnCtx + a cancelled ctx makes Next return the context error;
//   - an approval request before any token is still delivered to the Approver.
func TestFakeMCPSession_Edge_EmptyScriptTerminatesImmediately(t *testing.T) {
	f := &fakeMCPSession{} // no events

	stream, err := f.Start(context.Background(), turnConfig{Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)

	ev, err := stream.Next(context.Background())
	assert.ErrorIs(t, err, errStreamDone)
	assert.Equal(t, turnEvent{}, ev)
}

func TestFakeMCPSession_Edge_BlockOnCtxReturnsContextError(t *testing.T) {
	f := &fakeMCPSession{blockOnCtx: true, events: []turnEvent{{Kind: turnDone}}}

	stream, err := f.Start(context.Background(), turnConfig{Prompt: "p", WorkDir: "/w"})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled — Next must return the context error, not an event.

	ev, err := stream.Next(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, turnEvent{}, ev)
}

func TestFakeMCPSession_Edge_ApprovalBeforeAnyToken(t *testing.T) {
	var called bool
	approver := func(req approvalRequest) approvalDecision {
		called = true
		return approvalDecision{Allow: false, Reason: "writes to source are denied"}
	}

	f := &fakeMCPSession{
		approvals: []approvalRequest{{
			WritePaths: []string{"internal/secret.go"},
			Reason:     "apply patch",
		}},
		events: []turnEvent{{Kind: turnText, Text: "hello"}, {Kind: turnDone}},
	}

	_, err := f.Start(context.Background(), turnConfig{Prompt: "p", WorkDir: "/w", Approver: approver})
	require.NoError(t, err)

	// The approval reached the policy at Start time — before any token was drained.
	assert.True(t, called, "approval must be delivered to the Approver before any token")
	require.Len(t, f.decisions, 1)
	assert.False(t, f.decisions[0].Allow)
	assert.Equal(t, []string{"internal/secret.go"}, f.approvals[0].WritePaths)
}

// EDGE — nil Approver with scripted approvals records a default-deny decision;
// fail-closed means a missing policy never silently permits a mutation.
func TestFakeMCPSession_Edge_NilApproverDefaultDeny(t *testing.T) {
	f := &fakeMCPSession{
		approvals: []approvalRequest{{
			WritePaths: []string{"internal/secret.go"},
			Reason:     "apply patch",
		}},
		events: []turnEvent{{Kind: turnDone, SessionID: "c1"}},
	}

	_, err := f.Start(context.Background(), turnConfig{
		Prompt:   "p",
		WorkDir:  "/w",
		Approver: nil,
	})
	require.NoError(t, err)

	require.Len(t, f.decisions, 1)
	assert.False(t, f.decisions[0].Allow, "nil Approver must produce a default-deny decision (fail-closed)")
	assert.Empty(t, f.decisions[0].Reason)
}

// TYPE — the turnEventKind constants are distinct and the approval value types
// carry the structured codex payload (exec argv vs. patch write-paths).
func TestTurnEventKinds_AreDistinct(t *testing.T) {
	kinds := map[turnEventKind]struct{}{turnText: {}, turnDone: {}, turnFailed: {}}
	assert.Len(t, kinds, 3, "turnText/turnDone/turnFailed must be distinct kinds")
}
