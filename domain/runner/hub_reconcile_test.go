// domain/runner/hub_reconcile_test.go
package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
)

func TestHubReconnect_ResumesHeldStreamWithoutGapsOrDuplicates(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpStream)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	r := waitRun(t, ch)
	require.NoError(t, r.err)
	collected := make(chan []string, 1)
	go func() {
		var toks []string
		for c := range r.out.Stream {
			if !c.Done {
				toks = append(toks, c.Token)
			}
		}
		collected <- toks
	}()
	require.NoError(t, h.Handle(ctx, s, outputMsg(a, 1, "a")))
	require.NoError(t, h.Handle(ctx, s, outputMsg(a, 2, "b")))

	h.Disconnect(s) // socket drops while the runner keeps working (frames 3, 4 spooled)
	p.Clock.Advance(20 * time.Second)
	conn2 := hubtest.NewFakeConn()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{{JobID: a.Fence.JobID, Generation: a.Fence.Generation, State: runner.HeldRunning, LastOutputSeq: 4}}
	s2, err := h.Admit(ctx, key, conn2, hello)
	require.NoError(t, err)
	assert.Equal(t, runner.MsgAdmitted, conn2.Sent()[0].Kind)
	resume := conn2.Await(t, runner.MsgAck, a.Fence.JobID)
	assert.Equal(t, uint64(2), resume.Ack.Seq, "the hub tells the runner where to resume")
	assert.False(t, resume.Ack.Result)
	got, err := p.Ledger.Get(ctx, a.Fence.JobID)
	require.NoError(t, err)
	assert.True(t, got.LeaseUntil.Equal(p.Clock.Now().Add(120*time.Second)), "reconnect extends the held job's lease")
	assert.Zero(t, conn2.Count(runner.MsgDispatch, a.Fence.JobID), "an accepted job is not re-offered")

	require.NoError(t, h.Handle(ctx, s2, outputMsg(a, 2, "b")))
	require.NoError(t, h.Handle(ctx, s2, outputMsg(a, 3, "c")))
	require.NoError(t, h.Handle(ctx, s2, outputMsg(a, 4, "d")))
	require.NoError(t, h.Handle(ctx, s2, resultMsg(a, okChat("abcd"))))
	select {
	case toks := <-collected:
		assert.Equal(t, []string{"a", "b", "c", "d"}, toks)
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not finish")
	}
	assert.Equal(t, []string{"runner.connected", "runner.reconnected"}, p.EventKinds(t))
}

func TestHubReconnect_FencesJobsTheServerNoLongerOwns(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	done := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, done, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, done.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, done)))
	require.NoError(t, h.Handle(ctx, s, resultMsg(done, okChat("ok"))))
	require.NoError(t, waitRun(t, ch).err)
	require.NoError(t, p.Ledger.Complete(ctx, done.Fence, done.Fence.JobID.String()))
	h.Disconnect(s)

	ghost := uuid.New()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.Held = []runner.HeldJobInfo{
		{JobID: done.Fence.JobID, Generation: done.Fence.Generation, State: runner.HeldTerminal},
		{JobID: ghost, Generation: 1, State: runner.HeldRunning, LastOutputSeq: 3},
		{JobID: done.Fence.JobID, Generation: done.Fence.Generation + 1, State: runner.HeldRunning},
	}
	conn2 := hubtest.NewFakeConn()
	_, err := h.Admit(ctx, key, conn2, hello)
	require.NoError(t, err)
	ack := conn2.Await(t, runner.MsgAck, done.Fence.JobID)
	assert.True(t, ack.Ack.Result, "a held terminal result already consumed is acknowledged")
	conn2.Await(t, runner.MsgFenced, ghost)
	stale := conn2.Await(t, runner.MsgFenced, done.Fence.JobID)
	assert.Equal(t, done.Fence.Generation+1, stale.Generation)
}

func TestHubReconnect_ResendsUnacceptedOffers(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	runID := p.SeedRun(t)
	sentBeforeDrop := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	ch1 := goRun(ctx, h, sentBeforeDrop, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, sentBeforeDrop.Fence.JobID)
	h.Disconnect(s)
	whileDown := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	ch2 := goRun(ctx, h, whileDown, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	assertStillRunning(t, ch2)
	assert.Zero(t, conn.Count(runner.MsgDispatch, whileDown.Fence.JobID), "no session: nothing sent yet")

	conn2 := hubtest.NewFakeConn()
	s2, err := h.Admit(ctx, key, conn2, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	for _, a := range []runner.Assignment{sentBeforeDrop, whileDown} {
		d := conn2.Await(t, runner.MsgDispatch, a.Fence.JobID)
		assert.Equal(t, a.Fence.Generation, d.Generation)
		require.NoError(t, h.Handle(ctx, s2, jobMsg(runner.MsgAccept, a)))
		require.NoError(t, h.Handle(ctx, s2, resultMsg(a, okChat("ok"))))
	}
	assert.Equal(t, runner.MsgAdmitted, conn2.Sent()[0].Kind, "admitted precedes resent offers")
	require.NoError(t, waitRun(t, ch1).err)
	require.NoError(t, waitRun(t, ch2).err)
}

func TestHubContract_InMemoryRunnersAndChaos(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	keyA := p.SeedRunner(t, "mac-a", 2)
	keyB := p.SeedRunner(t, "mac-b", 2)
	ra := hubtest.ConnectInMemoryRunner(t, h, keyA, hubtest.DarwinHello(hubtest.AnthropicSource()))
	rb := hubtest.ConnectInMemoryRunner(t, h, keyB, hubtest.DarwinHello(hubtest.AnthropicSource()))
	ra.SetHold(true)
	rb.SetAcceptIf(func(int64) bool { return false }) // chaos: mac-b never sees its offers
	runID := p.SeedRun(t)
	srcB := runner.MakeSourceID("mac-b", runner.CredAnthropicSubscription, "")

	a1 := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	a2 := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpStream)
	b1 := p.ReserveJob(t, runID, "mac-b", srcB, runner.OpChat)
	ch1 := goRun(ctx, h, a1, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	ch2 := goRun(ctx, h, a2, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	chB := goRun(ctx, h, b1, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	ra.AwaitInflight(t, 2)
	assertStillRunning(t, ch1)

	r2 := waitRun(t, ch2) // stream returns on accept, before release
	require.NoError(t, r2.err)
	ra.ReleaseAll()
	r1 := waitRun(t, ch1)
	require.NoError(t, r1.err)
	assert.Equal(t, "remote:mac-a:claude-opus-5-5", r1.out.Chat.Output)
	var toks []string
	for c := range r2.out.Stream {
		if !c.Done {
			toks = append(toks, c.Token)
		}
	}
	assert.Equal(t, []string{"remote:", "mac-a", ":claude-opus-5-5"}, toks)
	assert.Equal(t, 2, ra.MaxInflight())
	// what the dispatcher does on success: completing releases mac-a's two slots
	require.NoError(t, p.Ledger.Complete(ctx, a1.Fence, a1.Fence.JobID.String()))
	require.NoError(t, p.Ledger.Complete(ctx, a2.Fence, a2.Fence.JobID.String()))

	assertStillRunning(t, chB)
	p.Clock.Advance(30 * time.Second)
	require.NoError(t, h.Handle(ctx, ra.Session(), heartbeat()), "keep mac-a fresh")
	require.NoError(t, h.Handle(ctx, rb.Session(), heartbeat()))
	require.NoError(t, h.Tick(ctx))
	rB := waitRun(t, chB)
	assert.ErrorIs(t, rB.err, runner.ErrOfferExpired)
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(rB.err).Class)
	rb.Conn().Await(t, runner.MsgCancel, b1.Fence.JobID)

	// chaos: a held job is cancelled by its step; the runner kills it and never reports a result.
	a3 := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	cctx, cancel := context.WithCancel(ctx)
	ch3 := goRun(cctx, h, a3, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	ra.AwaitInflight(t, 1)
	cancel()
	assert.ErrorIs(t, waitRun(t, ch3).err, context.Canceled)
	ra.AwaitInflight(t, 0)
	assert.NotContains(t, ra.Completed(), a3.Fence)
	assert.Contains(t, ra.Completed(), a1.Fence)
}
