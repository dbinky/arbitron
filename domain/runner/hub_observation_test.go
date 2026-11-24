package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func obsBeat(src runner.SourceReport, obs *runner.ProbeResult, seq uint64, ageMs int64) runner.Inbound {
	src.Observation, src.ObservationSeq, src.SampleAgeMs = obs, seq, ageMs
	return heartbeat(src)
}
func storedHealth(t *testing.T, p *hubtest.Pool, id runner.SourceID) runner.SourceHealth {
	t.Helper()
	s, err := p.Registry.GetSource(context.Background(), id)
	require.NoError(t, err)
	return s.Health
}
func TestRemoteProbe_RepeatedHeartbeatDoesNotRefreshLastProbe(t *testing.T) {
	p, h, _, _ := probeStack(t)
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	t0 := p.Clock.Now()
	first := storedHealth(t, p, anthropicSrcA)
	require.NotNil(t, first.LastProbeAt, "the hello observation is a real sample")
	assert.True(t, first.LastProbeAt.Equal(t0))
	assert.Equal(t, runner.AuthOK, first.Auth)
	p.Clock.Advance(5 * time.Second)
	ok := &runner.ProbeResult{Auth: runner.AuthOK}
	for _, in := range []runner.Inbound{
		obsBeat(hubtest.AnthropicSource(), ok, 1, 0),
		obsBeat(hubtest.AnthropicSource(), ok, 0, 0),
		obsBeat(hubtest.AnthropicSource(), ok, 7, 16_000),
		obsBeat(hubtest.AnthropicSource(), ok, 8, -1),
		obsBeat(hubtest.AnthropicSource(), nil, 0, 0),
	} {
		require.NoError(t, h.Handle(ctx, s, in))
		assert.True(t, storedHealth(t, p, anthropicSrcA).LastProbeAt.Equal(t0), "no new sample, no new probe time")
	}
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), ok, 9, 1000)))
	hl := storedHealth(t, p, anthropicSrcA)
	assert.True(t, hl.LastProbeAt.Equal(p.Clock.Now()), "a new sample is recorded at server receipt, persisted in SQLite")
	assert.Equal(t, first.Revision, hl.Revision, "a bare LastProbeAt refresh is not a material transition")
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), &runner.ProbeResult{Auth: runner.AuthLoggedOut}, 8, 0)))
	assert.Equal(t, runner.AuthOK, storedHealth(t, p, anthropicSrcA).Auth, "an older sequence cannot overwrite a newer one")
}
func TestRemoteProbe_AuthOnlyDoesNotClearExhaustion(t *testing.T) {
	p, h, mon, _ := probeStack(t)
	ctx := context.Background()
	s, conn, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	reset := p.Clock.Now().Add(3 * time.Hour)
	require.NoError(t, mon.ApplyOutcome(ctx, anthropicSrcA, runner.SourceOutcome{Class: runner.OutcomeLoggedOut}))
	require.NoError(t, mon.ApplyOutcome(ctx, anthropicSrcA, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-5-5", ResetAt: &reset}))
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), &runner.ProbeResult{Auth: runner.AuthOK}, 2, 0)))
	hl := storedHealth(t, p, anthropicSrcA)
	assert.Equal(t, runner.AuthOK, hl.Auth, "auth-only success clears logout")
	assert.Equal(t, runner.CapacityExhausted, hl.Capacity, "but preserves exhaustion")
	require.NotNil(t, hl.ResetAt)
	assert.True(t, hl.ResetAt.Equal(reset))
	allowed := true
	recovered := &runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{ModelScope: "claude-opus-5-5", OrdinaryUsageAllowed: &allowed}}
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), recovered, 3, 0)))
	assert.Equal(t, runner.CapacityExhausted, storedHealth(t, p, anthropicSrcA).Capacity, "a heartbeat hint cannot recover a classified episode")
	assert.Len(t, p.EventsOf(t, "source.recovered"), 1, "only the auth recovery event exists")
	ch := goProbe(ctx, mon, anthropicSrcA)
	out := conn.Await(t, runner.MsgProbe, uuid.Nil)
	require.NoError(t, h.Handle(ctx, s, probeReply(out, hubtest.AnthropicSource(), recovered, 4, 0)))
	require.NoError(t, awaitProbe(t, ch).err)
	assert.Equal(t, runner.CapacityAvailable, storedHealth(t, p, anthropicSrcA).Capacity, "a correlated fresh check can")
}
func TestRemoteProbe_CombinedLogoutAndQuotaArePreserved(t *testing.T) {
	p, h, _, alerts := probeStack(t)
	ctx := context.Background()
	s, _, _ := admit(t, p, h, "mac-a", hubtest.DarwinHello(hubtest.AnthropicSource()))
	held, reset := false, p.Clock.Now().Add(2*time.Hour)
	combined := &runner.ProbeResult{Auth: runner.AuthLoggedOut, Capacity: &runner.CapacityObservation{
		State: runner.CapacityExhausted, ModelScope: "claude-opus-5-5", OrdinaryUsageAllowed: &held, ResetAt: &reset}}
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), combined, 2, 0)))
	require.NoError(t, h.Handle(ctx, s, obsBeat(hubtest.AnthropicSource(), combined, 3, 0)))
	hl := storedHealth(t, p, anthropicSrcA)
	assert.Equal(t, runner.AuthLoggedOut, hl.Auth)
	assert.Equal(t, runner.CapacityExhausted, hl.Capacity)
	require.NotNil(t, hl.ResetAt)
	assert.True(t, hl.ResetAt.Equal(reset))
	assert.Equal(t, "claude-opus-5-5", hl.ConfirmationModel)
	assert.Equal(t, 1, alerts.LoggedOut(), "one notification for the combined transition")
	assert.Len(t, p.EventsOf(t, "source.logged_out"), 1)
}
