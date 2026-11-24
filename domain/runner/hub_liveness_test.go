// domain/runner/hub_liveness_test.go
package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
)

func liveness(t *testing.T, p *hubtest.Pool, id runner.RunnerID) runner.Liveness {
	t.Helper()
	r, _, err := p.Registry.GetRunner(context.Background(), id)
	require.NoError(t, err)
	return r.Liveness
}

func TestHubTick_ShortGapChangesNothing(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	p.Clock.Advance(29 * time.Second)
	require.NoError(t, h.Tick(context.Background()))
	assert.Equal(t, runner.LivenessOnline, liveness(t, p, "mac-a"))
	assert.Equal(t, []string{"runner.connected"}, p.EventKinds(t))
	_, _, closed := conn.Closed()
	assert.False(t, closed)
}

func TestHubTick_SuspectThenLost(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))

	p.Clock.Advance(30 * time.Second)
	require.NoError(t, h.Tick(ctx))
	assert.Equal(t, runner.LivenessSuspect, liveness(t, p, "mac-a"))
	assert.False(t, place(t, p, anthropicChat(runner.AffinityNone)).Placed, "suspect runners get no new work")
	assertStillRunning(t, ch)

	p.Clock.Advance(89 * time.Second)
	require.NoError(t, h.Tick(ctx))
	assert.Equal(t, runner.LivenessSuspect, liveness(t, p, "mac-a"), "119 s is not yet lost")

	p.Clock.Advance(1 * time.Second)
	require.NoError(t, h.Tick(ctx))
	assert.Equal(t, runner.LivenessLost, liveness(t, p, "mac-a"))
	code, _, closed := conn.Closed()
	assert.True(t, closed)
	assert.Equal(t, runner.CloseLost, code)
	r := waitRun(t, ch)
	assert.ErrorIs(t, r.err, runner.ErrRunnerLost)
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(r.err).Class)
	assert.Equal(t, []string{"runner.connected", "runner.suspect", "runner.lost"}, p.EventKinds(t))
	lost := p.EventsOf(t, "runner.lost")[0]
	assert.Equal(t, "liveness", lost.Cause.Kind)
	assert.Equal(t, runner.OutcomeRunnerLost, lost.Cause.Outcome)
}

func TestHubTick_SocketDropIsNotLoss(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))

	h.Disconnect(s)
	p.Clock.Advance(60 * time.Second)
	require.NoError(t, h.Tick(ctx))
	assert.Equal(t, runner.LivenessSuspect, liveness(t, p, "mac-a"), "a dropped socket only ages contact")
	assertStillRunning(t, ch)

	conn2 := hubtest.NewFakeConn()
	_, err := h.Admit(ctx, key, conn2, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessOnline, liveness(t, p, "mac-a"))
	assert.Equal(t, []string{"runner.connected", "runner.suspect", "runner.reconnected"}, p.EventKinds(t))
	assertStillRunning(t, ch)
}

func TestHubTick_HeartbeatAfterSuspectReconnects(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	p.Clock.Advance(45 * time.Second)
	require.NoError(t, h.Tick(ctx))
	require.Equal(t, runner.LivenessSuspect, liveness(t, p, "mac-a"))
	require.NoError(t, h.Handle(ctx, s, heartbeat()))
	assert.Equal(t, runner.LivenessOnline, liveness(t, p, "mac-a"))
	assert.Equal(t, []string{"runner.connected", "runner.suspect", "runner.reconnected"}, p.EventKinds(t))
}

func TestHubTick_UnacceptedOfferExpires(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	runID := p.SeedRun(t)
	unaccepted := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	accepted := p.ReserveJob(t, runID, "mac-a", anthropicSrcA, runner.OpChat)
	ch1 := goRun(ctx, h, unaccepted, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	ch2 := goRun(ctx, h, accepted, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, unaccepted.Fence.JobID)
	conn.Await(t, runner.MsgDispatch, accepted.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, accepted)))

	p.Clock.Advance(29 * time.Second)
	require.NoError(t, h.Handle(ctx, s, heartbeat()))
	require.NoError(t, h.Tick(ctx))
	assertStillRunning(t, ch1)

	p.Clock.Advance(1 * time.Second)
	require.NoError(t, h.Tick(ctx))
	r := waitRun(t, ch1)
	assert.ErrorIs(t, r.err, runner.ErrOfferExpired)
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(r.err).Class, "the dispatcher re-places it with a new generation")
	conn.Await(t, runner.MsgCancel, unaccepted.Fence.JobID)
	assertStillRunning(t, ch2)

	require.NoError(t, h.Handle(ctx, s, resultMsg(accepted, okChat("fine"))))
	require.NoError(t, waitRun(t, ch2).err)
}

func TestHubTick_LostFailsActiveJobsWithoutWaiters(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	orphan := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	p.Clock.Advance(120 * time.Second)
	require.NoError(t, h.Tick(ctx))
	got, err := p.Ledger.Get(ctx, orphan.Fence.JobID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobState("failed"), got.State, "a slot held by no in-process waiter is released")
}

func TestHubRevokeRunner_DisconnectsLosesAndRefuses(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	ctx := context.Background()
	s, conn, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	a := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)
	ch := goRun(ctx, h, a, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	conn.Await(t, runner.MsgDispatch, a.Fence.JobID)
	require.NoError(t, h.Handle(ctx, s, jobMsg(runner.MsgAccept, a)))
	b := p.ReserveJob(t, p.SeedRun(t), "mac-a", anthropicSrcA, runner.OpChat)

	require.NoError(t, h.RevokeRunner(ctx, "mac-a"))
	code, _, closed := conn.Closed()
	assert.True(t, closed)
	assert.Equal(t, runner.CloseRevoked, code)
	r := waitRun(t, ch)
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(r.err).Class, "interrupted steps are re-placed")
	assert.Equal(t, runner.LivenessLost, liveness(t, p, "mac-a"))
	assert.Equal(t, []string{"runner.connected", "runner.key_revoked", "runner.lost"}, p.EventKinds(t))

	assert.ErrorIs(t, h.Handle(ctx, s, heartbeat()), runner.ErrRunnerRevoked)
	_, err := h.Admit(ctx, key, hubtest.NewFakeConn(), hubtest.DarwinHello())
	assert.ErrorIs(t, err, runner.ErrRunnerRevoked)
	_, err = h.Run(ctx, b, hubtest.ChatJob("claude-opus-5-5", runner.OpChat))
	assert.True(t, errors.Is(err, runner.ErrRunnerRevoked))
	assert.Equal(t, runner.OutcomeRunnerLost, runner.ClassifyError(err).Class)
}

func TestHubStart_DrivesLivenessOnItsOwn(t *testing.T) {
	p := hubtest.NewPool(t)
	cfg := hubtest.HubConfig()
	cfg.Heartbeat = 5 * time.Millisecond
	h := p.NewHub(cfg)
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	p.Clock.Advance(121 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)
	require.Eventually(t, func() bool { return liveness(t, p, "mac-a") == runner.LivenessLost }, 2*time.Second, 5*time.Millisecond)
	_, _, closed := conn.Closed()
	assert.True(t, closed)
}
