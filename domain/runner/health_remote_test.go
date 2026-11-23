package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthMonitor_RemoteSourcesAreNotProbedLocally(t *testing.T) {
	ctx := context.Background()
	s := hmSource(MakeSourceID("remote-mac", CredAnthropicSubscription, ""), CredAnthropicSubscription)
	s.RunnerID = "remote-mac"
	f := newHMFixture(t, s)
	remote := &hmProber{res: map[SourceID]ProbeResult{s.ID: {Auth: AuthLoggedOut}}}
	f.mon.SetRemoteProber(remote)
	require.NoError(t, f.mon.ApplyOutcome(ctx, s.ID, SourceOutcome{Class: OutcomeTransient, RetryAfter: time.Second}))
	f.clock.Advance(2 * time.Minute)
	f.tick(t)
	assert.Zero(t, f.prober.calls[s.ID], "server credentials must never probe a remote source")
	assert.Zero(t, remote.calls[s.ID], "periodic remote health arrives as observations, not server-initiated probes")
	assert.False(t, f.reg.health(s.ID).Congested, "bookkeeping must still advance remote sources")
	_, probed := f.mon.lastProbe[s.ID]
	assert.False(t, probed, "skipping a probe must not invent a timestamp")
	h, err := f.mon.ObserveProbe(ctx, s.ID, ProbeResult{Auth: AuthLoggedOut, HealthRevision: f.reg.health(s.ID).Revision})
	require.NoError(t, err)
	assert.Equal(t, AuthLoggedOut, h.Auth)
	assert.Equal(t, AuthLoggedOut, f.reg.health(s.ID).Auth)
	assert.Contains(t, f.log.kinds(s.ID), "source.logged_out")
	reset := f.clock.Now().Add(time.Minute)
	require.NoError(t, f.mon.ApplyOutcome(ctx, s.ID, SourceOutcome{Class: OutcomeExhausted, ResetAt: &reset}))
	f.clock.Advance(2 * time.Minute)
	f.tick(t)
	assert.Equal(t, CapacityProbation, f.reg.health(s.ID).Capacity, "remote reset bookkeeping must still run")
	assert.Zero(t, remote.calls[s.ID])
}

func TestHealthMonitor_RemoteProbeNowRequiresFreshTransport(t *testing.T) {
	ctx := context.Background()
	s := hmSource(MakeSourceID("remote-mac", CredAnthropicSubscription, ""), CredAnthropicSubscription)
	s.RunnerID = "remote-mac"
	f := newHMFixture(t, s)
	_, err := f.mon.ProbeNow(ctx, s.ID)
	require.Error(t, err, "no transport cannot return cached healthy success")
	var unavailable *RemoteProbeError
	require.ErrorAs(t, err, &unavailable)
	remote := &hmProber{res: map[SourceID]ProbeResult{s.ID: {Auth: AuthLoggedOut}}, err: map[SourceID]error{}}
	f.mon.SetRemoteProber(remote)
	h, err := f.mon.ProbeNow(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, AuthLoggedOut, h.Auth)
	assert.Equal(t, AuthLoggedOut, f.reg.health(s.ID).Auth)
	require.Equal(t, 1, remote.calls[s.ID])
	for _, failure := range []error{errors.New("offline"), context.DeadlineExceeded} {
		remote.err[s.ID] = failure
		_, err = f.mon.ProbeNow(ctx, s.ID)
		require.ErrorAs(t, err, &unavailable)
		assert.ErrorIs(t, err, failure)
		assert.Equal(t, AuthLoggedOut, f.reg.health(s.ID).Auth, "transport failures must not overwrite health")
	}
	assert.Zero(t, f.prober.calls[s.ID])
	_, probed := f.mon.lastProbe[s.ID]
	assert.False(t, probed, "transport failure must not fabricate probe evidence")
}
