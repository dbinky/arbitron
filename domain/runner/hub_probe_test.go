package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
)

type probeCall struct {
	h   runner.SourceHealth
	err error
}

func goProbe(ctx context.Context, mon *runner.HealthMonitor, id runner.SourceID) <-chan probeCall {
	ch := make(chan probeCall, 1)
	go func() { h, err := mon.ProbeNow(ctx, id); ch <- probeCall{h, err} }()
	return ch
}

func awaitProbe(t *testing.T, ch <-chan probeCall) probeCall {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("ProbeNow did not return")
		return probeCall{}
	}
}

func probeReply(out runner.Outbound, rep runner.SourceReport, obs *runner.ProbeResult, seq uint64, ageMs int64) runner.Inbound {
	rep.Observation, rep.ObservationSeq, rep.SampleAgeMs = obs, seq, ageMs
	return runner.Inbound{Kind: runner.MsgProbeResult, ProbeResult: &runner.ProbeReply{
		RequestID: out.Probe.RequestID, HealthRevision: out.Probe.HealthRevision, Source: rep}}
}

func openAISource() runner.SourceReport {
	return runner.SourceReport{Kind: runner.CredOpenAISubscription, Families: []modelidentity.Family{"openai"}}
}

// probeStack: real SQLite pool, real HealthMonitor, hub with a short probe
// timeout, and the RemoteSourceProber installed exactly as the root does.
func probeStack(t *testing.T) (*hubtest.Pool, *runner.Hub, *runner.HealthMonitor, *hubtest.RecordingAlerts) {
	t.Helper()
	p := hubtest.NewPool(t)
	alerts := &hubtest.RecordingAlerts{}
	mon := p.NewMonitor(alerts)
	cfg := hubtest.HubConfig()
	cfg.ProbeTimeout = 150 * time.Millisecond
	h := p.NewHubWithHealth(cfg, mon)
	mon.SetRemoteProber(runner.NewRemoteSourceProber(h))
	return p, h, mon, alerts
}

func TestHubProbeSource_CorrelatedFreshReplyReachesTheRegistry(t *testing.T) {
	p, h, mon, alerts := probeStack(t)
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	before, err := p.Registry.GetSource(ctx, anthropicSrcA)
	require.NoError(t, err)

	ch := goProbe(ctx, mon, anthropicSrcA)
	out := conn.Await(t, runner.MsgProbe, uuid.Nil)
	require.NotNil(t, out.Probe)
	assert.NotEqual(t, uuid.Nil, out.Probe.RequestID)
	assert.Equal(t, runner.CredAnthropicSubscription, out.Probe.SourceKind)
	assert.Equal(t, before.Health.Revision, out.Probe.HealthRevision)
	assert.Equal(t, 1, h.PendingProbes(), "registered before send")
	p.Clock.Advance(time.Second)
	require.NoError(t, h.Handle(ctx, s, probeReply(out, hubtest.AnthropicSource(), &runner.ProbeResult{Auth: runner.AuthLoggedOut}, 9, 10)))
	r := awaitProbe(t, ch)
	require.NoError(t, r.err)
	assert.Equal(t, runner.AuthLoggedOut, r.h.Auth)
	stored, err := p.Registry.GetSource(ctx, anthropicSrcA)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthLoggedOut, stored.Health.Auth)
	require.NotNil(t, stored.Health.LastProbeAt)
	assert.True(t, stored.Health.LastProbeAt.Equal(p.Clock.Now()), "server receipt time, not the runner wall clock")
	assert.Equal(t, 1, alerts.LoggedOut())
	assert.Zero(t, h.PendingProbes())
}

func TestRemoteProbe_OfflineTimeoutCancelAndWrongReplyNeverReturnCachedSuccess(t *testing.T) {
	p, h, mon, _ := probeStack(t)
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	stored := func() runner.SourceHealth {
		src, err := p.Registry.GetSource(ctx, anthropicSrcA)
		require.NoError(t, err)
		return src.Health
	}
	base := stored()
	var remoteErr *runner.RemoteProbeError

	// Timeout: no reply.
	r := awaitProbe(t, goProbe(ctx, mon, anthropicSrcA))
	require.ErrorAs(t, r.err, &remoteErr)
	assert.ErrorIs(t, r.err, runner.ErrRemoteProbeTimeout)
	assert.Equal(t, base, stored())
	assert.Zero(t, h.PendingProbes(), "no waiter survives a timeout")
	conn.Await(t, runner.MsgProbe, uuid.Nil) // consume the timed-out request frame

	// Caller cancellation wins over the probe timeout.
	cctx, cancel := context.WithCancel(ctx)
	ch := goProbe(cctx, mon, anthropicSrcA)
	conn.Await(t, runner.MsgProbe, uuid.Nil)
	cancel()
	assert.ErrorIs(t, awaitProbe(t, ch).err, context.Canceled)
	assert.Zero(t, h.PendingProbes())

	// Wrong replies never fulfil the request; the check then times out.
	ch = goProbe(ctx, mon, anthropicSrcA)
	out := conn.Await(t, runner.MsgProbe, uuid.Nil)
	healthy := &runner.ProbeResult{Auth: runner.AuthOK}
	wrongID := probeReply(out, hubtest.AnthropicSource(), healthy, 20, 0)
	wrongID.ProbeResult.RequestID = uuid.New()
	wrongCred := probeReply(out, hubtest.LunaRouteSource("lunaroute", "zhipuai"), healthy, 21, 0)
	wrongRev := probeReply(out, hubtest.AnthropicSource(), healthy, 22, 0)
	wrongRev.ProbeResult.HealthRevision++
	p.Clock.Advance(time.Second)
	stale := probeReply(out, hubtest.AnthropicSource(), healthy, 23, 5000) // sampled before the request
	for _, in := range []runner.Inbound{wrongID, wrongCred, wrongRev, stale} {
		require.NoError(t, h.Handle(ctx, s, in))
	}
	r = awaitProbe(t, ch)
	assert.ErrorIs(t, r.err, runner.ErrRemoteProbeTimeout)
	assert.Equal(t, base.Auth, stored().Auth)

	// An unsolicited reply with nothing pending changes nothing.
	require.NoError(t, h.Handle(ctx, s, probeReply(out, hubtest.AnthropicSource(), healthy, 24, 0)))

	// A typed unavailable reply (no observation) fails promptly.
	ch = goProbe(ctx, mon, anthropicSrcA)
	out = conn.Await(t, runner.MsgProbe, uuid.Nil)
	unavailable := hubtest.AnthropicSource()
	unavailable.UnavailableReason = "harness_not_found"
	require.NoError(t, h.Handle(ctx, s, probeReply(out, unavailable, nil, 0, 0)))
	assert.ErrorIs(t, awaitProbe(t, ch).err, runner.ErrRemoteProbeUnavailable)

	// Offline: no session.
	h.Disconnect(s)
	r = awaitProbe(t, goProbe(ctx, mon, anthropicSrcA))
	assert.ErrorIs(t, r.err, runner.ErrRemoteProbeUnavailable)
	assert.False(t, errors.Is(r.err, runner.ErrRemoteProberNotWired), "the transport is wired; only the runner is offline")
	assert.Equal(t, base.Auth, stored().Auth)
	assert.Zero(t, h.PendingProbes())
}

func TestRemoteProbe_LateOldSessionReplyCannotOverwriteOutcome(t *testing.T) {
	p, h, mon, _ := probeStack(t)
	ctx := context.Background()
	s1, conn1, key := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	ch := goProbe(ctx, mon, anthropicSrcA)
	out := conn1.Await(t, runner.MsgProbe, uuid.Nil)
	_, err := h.Admit(ctx, key, hubtest.NewFakeConn(), hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	assert.ErrorIs(t, awaitProbe(t, ch).err, runner.ErrRemoteProbeUnavailable, "session replacement cancels its pending checks")
	assert.Zero(t, h.PendingProbes())

	reset := p.Clock.Now().Add(3 * time.Hour)
	require.NoError(t, mon.ApplyOutcome(ctx, anthropicSrcA, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-5-5", ResetAt: &reset}))
	allowed := true
	late := probeReply(out, hubtest.AnthropicSource(), &runner.ProbeResult{Auth: runner.AuthOK,
		Capacity: &runner.CapacityObservation{ModelScope: "claude-opus-5-5", OrdinaryUsageAllowed: &allowed}}, 99, 0)
	assert.ErrorIs(t, h.Handle(ctx, s1, late), runner.ErrSessionClosed)
	assert.ErrorIs(t, h.Handle(ctx, s1, heartbeat(late.ProbeResult.Source)), runner.ErrSessionClosed)
	src, err := p.Registry.GetSource(ctx, anthropicSrcA)
	require.NoError(t, err)
	assert.Equal(t, runner.CapacityExhausted, src.Health.Capacity)
	require.NotNil(t, src.Health.ResetAt)
	assert.True(t, src.Health.ResetAt.Equal(reset))
	assert.Empty(t, p.EventsOf(t, "source.recovered"))
}

func TestRemoteProbe_InFlightOldRevisionCannotClearNewExhaustion(t *testing.T) {
	p, h, mon, _ := probeStack(t)
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(openAISource()))
	sid := runner.MakeSourceID("mac-a", runner.CredOpenAISubscription, "")
	allowed := true
	recovered := &runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{ModelScope: "gpt-5.2-codex", OrdinaryUsageAllowed: &allowed}}

	ch := goProbe(ctx, mon, sid)
	out := conn.Await(t, runner.MsgProbe, uuid.Nil)
	require.NoError(t, mon.ApplyOutcome(ctx, sid, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "gpt-5.2-codex"}))
	require.NoError(t, h.Handle(ctx, s, probeReply(out, openAISource(), recovered, 5, 0)))
	r := awaitProbe(t, ch)
	require.NoError(t, r.err)
	src, err := p.Registry.GetSource(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, runner.CapacityExhausted, src.Health.Capacity, "a check started before the classified outcome cannot recover it")
	require.NotNil(t, src.Health.LastProbeAt, "the check itself is still recorded")

	ch = goProbe(ctx, mon, sid) // a new check, issued after the outcome
	out = conn.Await(t, runner.MsgProbe, uuid.Nil)
	assert.Equal(t, src.Health.Revision, out.Probe.HealthRevision)
	require.NoError(t, h.Handle(ctx, s, probeReply(out, openAISource(), recovered, 6, 0)))
	require.NoError(t, awaitProbe(t, ch).err)
	src, err = p.Registry.GetSource(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, runner.CapacityAvailable, src.Health.Capacity, "a fresh matching-scope allowed=true check recovers")
}

func TestRemoteHealth_ShutdownJoinsPendingChecks(t *testing.T) {
	p := hubtest.NewPool(t)
	mon := p.NewMonitor(&hubtest.RecordingAlerts{})
	cfg := hubtest.HubConfig()
	cfg.ProbeTimeout = time.Hour // only shutdown can end it
	h := p.NewHubWithHealth(cfg, mon)
	mon.SetRemoteProber(runner.NewRemoteSourceProber(h))
	hubCtx, stopHub := context.WithCancel(context.Background())
	h.Start(hubCtx)
	monCtx, stopMon := context.WithCancel(context.Background())
	_, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))

	ch := goProbe(context.Background(), mon, anthropicSrcA)
	conn.Await(t, runner.MsgProbe, uuid.Nil)
	p.Clock.Advance(2 * time.Minute)
	require.NoError(t, mon.Tick(monCtx)) // a periodic check is now in flight too
	conn.Await(t, runner.MsgProbe, uuid.Nil)
	require.Equal(t, 2, h.PendingProbes())

	stopMon()
	stopHub()
	done := make(chan struct{})
	go func() { h.Wait(); mon.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join pending checks")
	}
	assert.ErrorIs(t, awaitProbe(t, ch).err, runner.ErrRemoteProbeUnavailable)
	assert.Zero(t, h.PendingProbes())
	r := awaitProbe(t, goProbe(context.Background(), mon, anthropicSrcA))
	assert.ErrorIs(t, r.err, runner.ErrRemoteProbeUnavailable, "a stopped hub accepts no new checks")
}

func TestRemoteProbe_OverflowingSampleAgeCannotBecomeFresh(t *testing.T) {
	p, h, mon, _ := probeStack(t)
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	ch := goProbe(ctx, mon, anthropicSrcA)
	out := conn.Await(t, runner.MsgProbe, uuid.Nil)
	// Conversion to nanoseconds would wrap this age to zero.
	require.NoError(t, h.Handle(ctx, s, probeReply(out, hubtest.AnthropicSource(), &runner.ProbeResult{Auth: runner.AuthLoggedOut}, 1, 1<<61)))
	assert.ErrorIs(t, awaitProbe(t, ch).err, runner.ErrRemoteProbeTimeout)
	src, err := p.Registry.GetSource(ctx, anthropicSrcA)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthOK, src.Health.Auth)
	assert.Zero(t, h.PendingProbes())
}

func TestNewRemoteSourceProber_RejectsNilAndBuiltIn(t *testing.T) {
	assert.Panics(t, func() { runner.NewRemoteSourceProber(nil) })
	p := hubtest.NewPool(t)
	pr := runner.NewRemoteSourceProber(p.NewHub(hubtest.HubConfig()))
	_, err := pr.Probe(context.Background(), runner.Source{RunnerID: runner.BuiltInRunnerID})
	assert.ErrorIs(t, err, runner.ErrRemoteProbeUnavailable, "built-in sources are probed locally, never over the transport")
}
