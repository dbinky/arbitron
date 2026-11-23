// domain/runner/hub_run_test.go
package runner_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

type runResult struct {
	out runner.StepOutput
	err error
}

func goRun(ctx context.Context, h *runner.Hub, a runner.Assignment, job runner.StepJob) <-chan runResult {
	ch := make(chan runResult, 1)
	go func() {
		out, err := h.Run(ctx, a, job)
		ch <- runResult{out: out, err: err}
	}()
	return ch
}

func waitRun(t *testing.T, ch <-chan runResult) runResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
		return runResult{}
	}
}

func assertStillRunning(t *testing.T, ch <-chan runResult) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("Run returned early: out=%+v err=%v", r.out, r.err)
	case <-time.After(50 * time.Millisecond):
	}
}

func jobMsg(kind string, a runner.Assignment) runner.Inbound {
	return runner.Inbound{Kind: kind, JobID: a.Fence.JobID, Generation: a.Fence.Generation, Accept: kind == runner.MsgAccept}
}

func resultMsg(a runner.Assignment, r runner.RemoteResult) runner.Inbound {
	in := jobMsg(runner.MsgResult, a)
	in.Result = &r
	return in
}

func outputMsg(a runner.Assignment, seq uint64, tok string) runner.Inbound {
	in := jobMsg(runner.MsgOutput, a)
	in.Seq = seq
	in.Output = &runner.OutputFrame{Seq: seq, Token: tok}
	return in
}

func okChat(text string) runner.RemoteResult {
	return runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: text, Model: "claude-opus-5-5", TokensIn: 7, TokensOut: 2, SessionID: "sess-1"}}
}

var anthropicSrcA = runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, "")

func TestHubRun_ChatRoundTripNeverShipsSecrets(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)

	var revealed atomic.Int32
	job := hubtest.ChatJob("claude-opus-5-5", runner.OpChat)
	job.Binding.BaseURL = "https://secret-gateway.example"
	job.Binding.RevealKey = func(context.Context) (string, error) { revealed.Add(1); return "lr_supersecret", nil }
	job.Binding.Pinned = true // a pin the runner must prove it executed
	job.Chat.WorkDir = "/srv/arbitron/workspaces/run-1"
	job.Chat.OutputSchema = map[string]any{"type": "object"}
	job.Chat.ToolScope = []string{"Read"}
	ch := goRun(ctx, h, a, job)

	d := conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	assert.Equal(t, a.Fence.Generation, d.Generation)
	require.NotNil(t, d.Dispatch)
	require.NotNil(t, d.Dispatch.Chat)
	assert.Equal(t, runner.OpChat, d.Dispatch.Op)
	assert.Equal(t, "claude-opus-5-5", d.Dispatch.Chat.Model)
	assert.Equal(t, "say hi", d.Dispatch.Chat.Prompt)
	assert.Equal(t, runner.CredAnthropicSubscription, d.Dispatch.Chat.CredentialKind)
	assert.Equal(t, []string{"Read"}, d.Dispatch.Chat.Tools)
	assert.False(t, d.Dispatch.Chat.Stream)
	assert.True(t, d.Dispatch.Chat.RequireExecutedModel, "a pinned binding requires executed-model evidence from the runner")
	assert.JSONEq(t, `{"type":"object"}`, string(d.Dispatch.Chat.OutputSchema))
	assert.True(t, d.Dispatch.LeaseUntil.Equal(p.Clock.Now().Add(120*time.Second)))
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	for _, secret := range []string{"lr_supersecret", "secret-gateway", "/srv/arbitron/workspaces/run-1"} {
		assert.NotContains(t, string(raw), secret)
	}
	assert.Zero(t, revealed.Load(), "the hub never reveals a server credential")

	p.Clock.Advance(10 * time.Second)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	got, err := p.Ledger.Get(ctx, a.Fence.JobID)
	require.NoError(t, err)
	require.NotNil(t, got.LeaseUntil)
	assert.True(t, got.LeaseUntil.Equal(p.Clock.Now().Add(120*time.Second)), "accept extends the lease")
	assertStillRunning(t, ch)

	res := okChat("hi")
	res.Chat.Conversation = json.RawMessage(`[{"role":"assistant","content":"hi"}]`)
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, res)))
	r := waitRun(t, ch)
	require.NoError(t, r.err)
	require.NotNil(t, r.out.Chat)
	assert.Equal(t, "hi", r.out.Chat.Output)
	assert.Equal(t, "claude-opus-5-5", r.out.Chat.Model)
	assert.Equal(t, 7, r.out.Chat.TokensIn)
	assert.Equal(t, 2, r.out.Chat.TokensOut)
	assert.Equal(t, "sess-1", r.out.Chat.SessionID)
	assert.NotNil(t, r.out.Chat.Conversation)
	ack := conn.Await(t, runner.MsgAck, a.Fence.JobID)
	require.NotNil(t, ack.Ack)
	assert.True(t, ack.Ack.Result)
}

func TestHubRun_StreamDeliversOutputInOrderWithoutGapsOrDuplicates(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpStream)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	d := conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	assert.True(t, d.Dispatch.Chat.Stream)
	assertStillRunning(t, ch)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	r := waitRun(t, ch)
	require.NoError(t, r.err)
	require.NotNil(t, r.out.Stream, "stream jobs return the token channel on accept")

	collected := make(chan []engine.ChatChunk, 1)
	go func() {
		var all []engine.ChatChunk
		for c := range r.out.Stream {
			all = append(all, c)
		}
		collected <- all
	}()
	for _, f := range []struct {
		seq uint64
		tok string
	}{{1, "a"}, {2, "b"}, {2, "b"}, {4, "d"}, {3, "c"}, {4, "d"}} {
		require.NoError(t, h.Handle(ctx, s, outputMsg(a, f.seq, f.tok)))
	}
	var acks []uint64
	for i := 0; i < 6; i++ {
		acks = append(acks, conn.Await(t, runner.MsgAck, a.Fence.JobID).Ack.Seq)
	}
	assert.Equal(t, []uint64{1, 2, 2, 2, 3, 4}, acks, "duplicates and gaps re-ack the last contiguous frame")

	fin := okChat("abcd")
	fin.Chat.TokensOut = 4
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, fin)))
	var chunks []engine.ChatChunk
	select {
	case chunks = <-collected:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not closed after result")
	}
	require.NotEmpty(t, chunks)
	var tokens []string
	for _, c := range chunks[:len(chunks)-1] {
		tokens = append(tokens, c.Token)
	}
	assert.Equal(t, []string{"a", "b", "c", "d"}, tokens)
	last := chunks[len(chunks)-1]
	assert.True(t, last.Done)
	assert.NoError(t, last.Error)
	assert.Equal(t, 4, last.TokensOut)
	assert.Equal(t, "sess-1", last.SessionID)
}

func TestHubRun_FailedResultsCarryClassifiedOutcomes(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	runID := p.SeedRun(t)
	reset := p.Clock.Now().Add(2 * time.Hour)
	cases := []struct {
		name string
		sent runner.SourceOutcome
		want runner.SourceOutcome
	}{
		{"exhausted keeps reset", runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}, runner.SourceOutcome{Class: runner.OutcomeExhausted, ResetAt: &reset}},
		{"logged out", runner.SourceOutcome{Class: runner.OutcomeLoggedOut}, runner.SourceOutcome{Class: runner.OutcomeLoggedOut}},
		{"transient keeps retry-after", runner.SourceOutcome{Class: runner.OutcomeTransient, RetryAfter: 3 * time.Second}, runner.SourceOutcome{Class: runner.OutcomeTransient, RetryAfter: 3 * time.Second}},
		{"runner cannot claim runner_lost", runner.SourceOutcome{Class: runner.OutcomeRunnerLost}, runner.SourceOutcome{Class: runner.OutcomeOther}},
		{"missing class is other", runner.SourceOutcome{}, runner.SourceOutcome{Class: runner.OutcomeOther}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
			ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
			conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
			require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
			require.NoError(t, h.Handle(ctx, s, resultMsg(a, runner.RemoteResult{OK: false, Outcome: tc.sent, ErrorText: "usage limit reached"})))
			r := waitRun(t, ch)
			require.Error(t, r.err)
			assert.ErrorIs(t, r.err, runner.ErrRemoteStepFailed)
			assert.Contains(t, r.err.Error(), "usage limit reached")
			assert.Equal(t, tc.want, runner.ClassifyError(r.err))
		})
	}
}

func TestHubRun_RejectFailsTheStepAsOther(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	rej := jobMsg(runner.MsgReject, a)
	rej.RejectReason = "no source for anthropic"
	require.NoError(t, h.Handle(ctx, s, rej))
	r := waitRun(t, ch)
	assert.ErrorIs(t, r.err, runner.ErrJobRejected)
	assert.Contains(t, r.err.Error(), "no source for anthropic")
	assert.Equal(t, runner.OutcomeOther, runner.ClassifyError(r.err).Class)
}

func TestHubRun_UnsupportedWorkNeverLeavesTheServer(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	runID := p.SeedRun(t)

	agent := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpAgent)
	_, err := h.Run(ctx, agent, runner.StepJob{Op: runner.OpAgent, Agent: &engine.AgentMission{}})
	assert.ErrorIs(t, err, runner.ErrRemoteOpUnsupported)
	assert.Equal(t, runner.OutcomeOther, runner.ClassifyError(err).Class)

	proc := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpProcess)
	_, err = h.Run(ctx, proc, runner.StepJob{Op: runner.OpProcess, Process: &engine.ProcessSpec{}})
	assert.ErrorIs(t, err, runner.ErrRemoteOpUnsupported)

	server := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	server.Job.Group.Kind = runner.CredServerConnection
	_, err = h.Run(ctx, server, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	assert.ErrorIs(t, err, runner.ErrRemoteCredential)

	builtin := server
	builtin.Job.Group.Kind = runner.CredAnthropicSubscription
	builtin.Fence.RunnerID = runner.BuiltInRunnerID
	_, err = h.Run(ctx, builtin, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	assert.Error(t, err)

	assert.Equal(t, []string{runner.MsgAdmitted}, conn.Kinds(), "nothing was dispatched")
}

func TestHubRun_StaleOrUnknownGenerationsAreFenced(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)

	stale := a
	stale.Fence.Generation = a.Fence.Generation + 1
	require.NoError(t, h.Handle(ctx, s, resultMsg(stale, okChat("wrong generation"))))
	f := conn.Await(t, runner.MsgFenced, a.Fence.JobID)
	assert.Equal(t, a.Fence.Generation+1, f.Generation)
	assertStillRunning(t, ch)

	ghost := uuid.New()
	require.NoError(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgAccept, JobID: ghost, Generation: 1, Accept: true}))
	conn.Await(t, runner.MsgFenced, ghost)
	assert.ErrorIs(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgAccept}), runner.ErrMalformedMessage)

	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, okChat("right"))))
	r := waitRun(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, "right", r.out.Chat.Output)
}

func TestHubRun_CancelledStepCancelsRemoteJob(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ctx, cancel := context.WithCancel(context.Background())
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	cancel()
	r := waitRun(t, ch)
	assert.ErrorIs(t, r.err, context.Canceled)
	conn.Await(t, runner.MsgCancel, a.Fence.JobID)
}

func TestHubRun_HeartbeatExtendsLeasesAndFencesUnknownJobs(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))

	p.Clock.Advance(60 * time.Second)
	ghost := uuid.New()
	require.NoError(t, h.Handle(ctx, s, runner.Inbound{Kind: runner.MsgHeartbeat, Heartbeat: &runner.HeartbeatInfo{Jobs: []runner.HeldJobInfo{
		{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldRunning},
		{JobID: ghost, Generation: 1, State: runner.HeldRunning},
	}}}))
	got, err := p.Ledger.Get(ctx, a.Fence.JobID)
	require.NoError(t, err)
	assert.True(t, got.LeaseUntil.Equal(p.Clock.Now().Add(120*time.Second)), "heartbeat extends leases of held jobs")
	conn.Await(t, runner.MsgFenced, ghost)

	require.NoError(t, h.Handle(ctx, s, resultMsg(a, okChat("done"))))
	require.NoError(t, waitRun(t, ch).err)
}

func TestHubRun_ReplayedResultAfterCompletionIsAcknowledged(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	require.NoError(t, h.Handle(ctx, s, resultMsg(a, okChat("once"))))
	require.NoError(t, waitRun(t, ch).err)
	conn.Await(t, runner.MsgAck, a.Fence.JobID)
	require.NoError(t, p.Ledger.Complete(ctx, a.Fence, a.Fence.JobID.String()), "what the dispatcher does on success")

	require.NoError(t, h.Handle(ctx, s, resultMsg(a, okChat("once"))))
	ack := conn.Await(t, runner.MsgAck, a.Fence.JobID)
	assert.True(t, ack.Ack.Result, "a replayed result for a completed job is acknowledged so the runner can drop it")
	assert.Zero(t, conn.Count(runner.MsgFenced, a.Fence.JobID))
}
