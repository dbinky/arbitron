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

// SUCCESS — ResumeID + Effort set on the turnConfig are recorded by Start so
// argument-threading assertions can read them back.
func TestFakeMCPSession_Success_RecordsResumeAndEffort(t *testing.T) {
	f := &fakeMCPSession{
		events: []turnEvent{{Kind: turnDone, SessionID: "c2"}},
	}

	_, err := f.Start(context.Background(), turnConfig{
		Model:    "gpt-5",
		Prompt:   "continue",
		WorkDir:  "/work/dir",
		ResumeID: "conv-abc",
		Effort:   "high",
	})
	require.NoError(t, err)

	// Resume id + effort threaded through to the transport.
	assert.Equal(t, "conv-abc", f.gotCfg.ResumeID)
	assert.Equal(t, "high", f.gotCfg.Effort)
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

// EDGE — boundary conditions:
//   - an empty script reports terminal completion immediately (no block);
//   - blockOnCtx + a cancelled ctx makes Next return the context error.
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

// TYPE — the turnEventKind constants are distinct.
func TestTurnEventKinds_AreDistinct(t *testing.T) {
	kinds := map[turnEventKind]struct{}{turnText: {}, turnDone: {}, turnFailed: {}}
	assert.Len(t, kinds, 3, "turnText/turnDone/turnFailed must be distinct kinds")
}
