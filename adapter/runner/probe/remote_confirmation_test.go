package probe

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rcAlerts struct {
	mu                   sync.Mutex
	loggedOut, recovered int
}

func (a *rcAlerts) SourceLoggedOut(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loggedOut++
	return nil
}
func (a *rcAlerts) SourceRecovered(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered++
	return nil
}

type rcFixture struct {
	pool *hubtest.Pool
	mon  *runner.HealthMonitor
	d    *cfDispatcher
	src  runner.SourceID
}

// newRCFixture: a remote runner mac-a with one Claude subscription source in
// real SQLite, the real HealthMonitor, the real DispatchConfirmer and a
// recording dispatcher (the dispatcher/slot path is covered by Cycle 16 roots).
func newRCFixture(t *testing.T, profiles ProfileLister) *rcFixture {
	t.Helper()
	p := hubtest.NewPool(t)
	p.SeedRunner(t, "mac-a", 1)
	src := runner.Source{ID: runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), RunnerID: "mac-a",
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}}
	require.NoError(t, p.Registry.ReplaceSources(context.Background(), "mac-a", []runner.Source{src}))
	d := &cfDispatcher{}
	c := NewDispatchConfirmer(profiles, t.TempDir(), time.Second)
	c.Attach(d)
	mon := runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Confirm: c, Alerts: &rcAlerts{},
		Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Minute, Rand: func() float64 { return 0 }})
	return &rcFixture{pool: p, mon: mon, d: d, src: src.ID}
}

// exhaustUnknownResetThenConfirm drives the real episode: exhausted with
// unknown reset -> probation after one interval -> one bounded confirmation.
func (f *rcFixture) exhaustUnknownResetThenConfirm(t *testing.T, o runner.SourceOutcome) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.mon.ApplyOutcome(ctx, f.src, o))
	f.pool.Clock.Advance(2 * time.Minute)
	require.NoError(t, f.mon.Tick(ctx)) // exhausted -> probation
	require.NoError(t, f.mon.Tick(ctx)) // probation -> confirmation
	f.mon.Wait()
}

func (f *rcFixture) health(t *testing.T) runner.SourceHealth {
	t.Helper()
	s, err := f.pool.Registry.GetSource(context.Background(), f.src)
	require.NoError(t, err)
	return s.Health
}

var rcOpus = runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1"}

func TestRemoteConfirmation_WrongModelCannotRecover(t *testing.T) {
	f := newRCFixture(t, cfProfiles{ps: []provider.Profile{{Alias: "opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"}}})
	f.d.out = &runner.ConfirmationResult{RequestedModel: "claude-opus-4-1", ExecutedModel: "claude-sonnet-4-5", SessionID: "s"}
	f.exhaustUnknownResetThenConfirm(t, rcOpus)
	require.Len(t, f.d.reqs, 1, "exactly one bounded confirmation ran")
	assert.Equal(t, f.src, f.d.reqs[0].RequiredSource)
	assert.Equal(t, runner.RunnerID("mac-a"), f.d.reqs[0].PinnedRunner)
	h := f.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity)
	assert.Equal(t, "claude-opus-4-1", h.ConfirmationModel)
	assert.Equal(t, runner.DetailConfirmMismatch, h.Detail)
	assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
}

func TestRemoteConfirmation_NoMatchingProfileRetainsProbation(t *testing.T) {
	f := newRCFixture(t, cfProfiles{ps: []provider.Profile{
		{Alias: "sonnet", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"},
		{Alias: "opus-azure", Kind: provider.ProfileKindChat, ConnectionRef: "azure", Model: "claude-opus-4-1"},
		{Alias: "opus-codex", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "claude-opus-4-1"},
	}})
	f.exhaustUnknownResetThenConfirm(t, rcOpus)
	assert.Empty(t, f.d.reqs, "no same-family, connection or other-backend substitute is dispatched")
	h := f.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity)
	assert.Nil(t, h.ResetAt)
	assert.Equal(t, runner.DetailConfirmNoProfile, h.Detail)
	assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), f.src, runner.SourceOutcome{Class: runner.OutcomeNone, Model: "claude-opus-4-1"}))
	assert.Equal(t, runner.CapacityAvailable, f.health(t).Capacity, "a later real exact-model success may still confirm recovery")
}

func TestRemoteConfirmation_QuotaOrExportFailureCannotMarkAvailable(t *testing.T) {
	profiles := cfProfiles{ps: []provider.Profile{{Alias: "opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"}}}

	quota := newRCFixture(t, profiles)
	reset := quota.pool.Clock.Now().Add(5 * time.Hour)
	quota.d.err = runner.WithOutcome(assert.AnError, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &reset})
	quota.exhaustUnknownResetThenConfirm(t, rcOpus)
	h := quota.health(t)
	assert.Equal(t, runner.CapacityExhausted, h.Capacity, "the confirmation's own quota failure starts a new episode")
	require.NotNil(t, h.ResetAt)
	assert.True(t, h.ResetAt.Equal(reset))

	lost := newRCFixture(t, profiles)
	lost.d.err = runner.WithOutcome(assert.AnError, runner.SourceOutcome{Class: runner.OutcomeRunnerLost})
	lost.exhaustUnknownResetThenConfirm(t, rcOpus)
	h = lost.health(t)
	assert.Equal(t, runner.CapacityProbation, h.Capacity, "transport loss cannot recover the source")
	assert.Equal(t, runner.DetailConfirmTransport, h.Detail)
	for _, f := range []*rcFixture{quota, lost} {
		assert.Empty(t, f.pool.EventsOf(t, "source.recovered"))
	}
	// Export/deferred-completion failure (P3 T5 ProbeCommitter) is added in Cycle 17.
}
