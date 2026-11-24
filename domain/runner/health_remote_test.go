package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	assert.Equal(t, 1, remote.calls[s.ID], "periodic remote checks go to the owning runner at the normal cadence (P2 T13)")
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
	assert.Equal(t, 2, remote.calls[s.ID], "the next periodic remote check runs after one interval")
}

type hmBlockingProber struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	res     ProbeResult
}

func (p *hmBlockingProber) Probe(ctx context.Context, _ Source) (ProbeResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	p.started <- struct{}{}
	select {
	case <-p.release:
		return p.res, nil
	case <-ctx.Done():
		return ProbeResult{}, ctx.Err()
	}
}

func (p *hmBlockingProber) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

func TestHealthMonitor_PeriodicRemoteChecksUseRemoteProberAtCadence(t *testing.T) {
	ctx := context.Background()
	s := hmSource(MakeSourceID("remote-mac", CredAnthropicSubscription, ""), CredAnthropicSubscription)
	s.RunnerID = "remote-mac"
	f := newHMFixture(t, s)
	remote := &hmBlockingProber{started: make(chan struct{}, 4), release: make(chan struct{}), res: ProbeResult{Auth: AuthLoggedOut}}
	f.mon.SetRemoteProber(remote)

	require.NoError(t, f.mon.Tick(ctx))
	<-remote.started
	require.NoError(t, f.mon.Tick(ctx), "a slow remote check never blocks the monitor tick")
	f.clock.Advance(2 * time.Minute)
	require.NoError(t, f.mon.Tick(ctx))
	assert.Equal(t, 1, remote.count(), "one bounded in-flight remote check per source")
	close(remote.release)
	f.mon.wg.Wait()
	h := f.reg.health(s.ID)
	assert.Equal(t, AuthLoggedOut, h.Auth, "the periodic result flows through ObserveProbe")
	require.NotNil(t, h.LastProbeAt)
	assert.Zero(t, f.prober.calls[s.ID], "server credentials never probe a remote source")

	f.tick(t)
	assert.Equal(t, 2, remote.count(), "due again one interval after the last attempt")
	f.tick(t)
	assert.Equal(t, 2, remote.count(), "not due again within the interval")
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

func TestHealthMonitor_PeriodicRemoteFailureLeavesHealthUnchanged(t *testing.T) {
	s := hmSource(MakeSourceID("remote-mac", CredAnthropicSubscription, ""), CredAnthropicSubscription)
	s.RunnerID = "remote-mac"
	f := newHMFixture(t, s)
	before := f.reg.health(s.ID)
	f.mon.SetRemoteProber(&hmProber{err: map[SourceID]error{s.ID: ErrRemoteProbeTimeout}})
	f.tick(t)
	assert.Equal(t, before, f.reg.health(s.ID), "a failed check is not evidence: no auth change, no LastProbeAt")
	_, err := f.mon.ProbeNow(context.Background(), s.ID)
	assert.ErrorIs(t, err, ErrRemoteProbeTimeout)
	f.mon.SetRemoteProber(nil)
	_, err = f.mon.ProbeNow(context.Background(), s.ID)
	assert.ErrorIs(t, err, ErrRemoteProberNotWired)
	assert.ErrorIs(t, err, ErrRemoteProbeUnavailable)
}

func TestHealthMonitor_FailedConfirmationLeavesVisibleProbationReason(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"no profile":  {ErrConfirmationNoProfile, DetailConfirmNoProfile},
		"unsupported": {ErrConfirmationUnsupported, DetailConfirmUnsupported},
		"wrong model": {fmt.Errorf("%w: x", ErrConfirmationModelMismatch), DetailConfirmMismatch},
		"transport":   {&RemoteProbeError{Err: ErrRemoteProbeUnavailable}, DetailConfirmTransport},
		"lost runner": {WithOutcome(errors.New("lost"), SourceOutcome{Class: OutcomeRunnerLost}), DetailConfirmTransport},
		"timeout":     {context.DeadlineExceeded, DetailConfirmTimeout},
		"other":       {errors.New("boom"), DetailConfirmFailed},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
			past := f.clock.Now().Add(-time.Minute)
			require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past}))
			f.confirm.err[hmAnth] = tc.err
			f.tick(t)
			h := f.reg.health(hmAnth)
			assert.Equal(t, CapacityProbation, h.Capacity, "a failed confirmation never clears exhaustion")
			assert.Equal(t, tc.want, h.Detail)
			assert.NotContains(t, f.log.kinds(hmAnth), "source.recovered")
		})
	}
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	past := f.clock.Now().Add(-time.Minute)
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past}))
	f.confirm.err[hmAnth] = ErrConfirmationNoProfile
	f.tick(t)
	require.Equal(t, DetailConfirmNoProfile, f.reg.health(hmAnth).Detail)
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeNone, Model: "claude-opus-4-1"}))
	h := f.reg.health(hmAnth)
	assert.Equal(t, CapacityAvailable, h.Capacity, "a later real exact-model success confirms recovery")
	assert.Empty(t, h.Detail, "the probation reason is cleared on recovery")
}
