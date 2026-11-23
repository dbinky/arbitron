package runner

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- fakes ----

type hmClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *hmClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *hmClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// hmRegistry is an in-memory Registry. Only the methods the monitor uses carry
// behaviour; the rest return errHMUnused so an unexpected call fails loudly.
type hmRegistry struct {
	mu       sync.Mutex
	sources  map[SourceID]Source
	inFlight map[SourceID]int
	records  int
}

var errHMUnused = errors.New("hmRegistry: unexpected call")

func newHMRegistry(srcs ...Source) *hmRegistry {
	r := &hmRegistry{sources: map[SourceID]Source{}, inFlight: map[SourceID]int{}}
	for _, s := range srcs {
		r.sources[s.ID] = s
	}
	return r
}
func (r *hmRegistry) Snapshot(context.Context) (PoolSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := PoolSnapshot{SourceInFlight: map[SourceID]int{}}
	for _, s := range r.sources {
		snap.Sources = append(snap.Sources, s)
	}
	sort.Slice(snap.Sources, func(i, j int) bool { return snap.Sources[i].ID < snap.Sources[j].ID })
	for k, v := range r.inFlight {
		snap.SourceInFlight[k] = v
	}
	return snap, nil
}
func (r *hmRegistry) GetSource(_ context.Context, id SourceID) (Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sources[id]
	if !ok {
		return Source{}, ErrSourceNotFound
	}
	return s, nil
}
func (r *hmRegistry) RecordHealth(_ context.Context, id SourceID, h SourceHealth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sources[id]
	if !ok {
		return ErrSourceNotFound
	}
	s.Health = h
	r.sources[id] = s
	r.records++
	return nil
}
func (r *hmRegistry) health(id SourceID) SourceHealth {
	s, _ := r.GetSource(context.Background(), id)
	return s.Health
}
func (r *hmRegistry) GetRunner(context.Context, RunnerID) (Runner, []Source, error) {
	return Runner{}, nil, errHMUnused
}
func (r *hmRegistry) UpsertRunner(context.Context, Runner) error { return errHMUnused }
func (r *hmRegistry) UpdateConfig(context.Context, RunnerID, RunnerConfigPatch) (Runner, error) {
	return Runner{}, errHMUnused
}
func (r *hmRegistry) SetCordoned(context.Context, RunnerID, bool) error { return errHMUnused }
func (r *hmRegistry) SetDraining(context.Context, RunnerID, bool) error { return errHMUnused }
func (r *hmRegistry) SetLiveness(context.Context, RunnerID, Liveness, time.Time) error {
	return errHMUnused
}
func (r *hmRegistry) SetCompat(context.Context, RunnerID, Compatibility) error { return errHMUnused }
func (r *hmRegistry) ReplaceSources(context.Context, RunnerID, []Source) error { return errHMUnused }
func (r *hmRegistry) ListRunners(context.Context) ([]Runner, error)            { return nil, errHMUnused }

type hmEventLog struct {
	mu  sync.Mutex
	evs []PoolEvent
}

func (l *hmEventLog) Append(_ context.Context, ev PoolEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, ev)
	return nil
}
func (l *hmEventLog) List(context.Context, int) ([]PoolEvent, error) { return nil, nil }
func (l *hmEventLog) Prune(context.Context, int) error               { return nil }
func (l *hmEventLog) kinds(src SourceID) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.evs {
		if e.SourceID == src {
			out = append(out, e.Kind)
		}
	}
	return out
}

type hmBus struct{}

func (hmBus) Publish(uuid.UUID, engine.Event)                   {}
func (hmBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) { return nil, func() {} }
func (hmBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) { return nil, func() {} }

// hmProber answers per Source ID (load-bearing argument) and counts calls.
type hmProber struct {
	mu    sync.Mutex
	res   map[SourceID]ProbeResult
	err   map[SourceID]error
	calls map[SourceID]int
}

func (p *hmProber) Probe(_ context.Context, s Source) (ProbeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[SourceID]int{}
	}
	p.calls[s.ID]++
	return p.res[s.ID], p.err[s.ID]
}

type hmConfirmer struct {
	mu    sync.Mutex
	err   map[SourceID]error
	calls []SourceID
}

func (c *hmConfirmer) Confirm(_ context.Context, s Source) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s.ID)
	return c.err[s.ID]
}
func (c *hmConfirmer) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.calls) }

type hmAlerts struct {
	mu        sync.Mutex
	loggedOut []SourceID
	recovered []SourceID
}

func (a *hmAlerts) SourceLoggedOut(_ context.Context, s Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loggedOut = append(a.loggedOut, s.ID)
	return nil
}
func (a *hmAlerts) SourceRecovered(_ context.Context, s Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered = append(a.recovered, s.ID)
	return nil
}

type hmFixture struct {
	clock   *hmClock
	reg     *hmRegistry
	log     *hmEventLog
	prober  *hmProber
	confirm *hmConfirmer
	alerts  *hmAlerts
	mon     *HealthMonitor
}

var (
	hmAnth   = MakeSourceID(BuiltInRunnerID, CredAnthropicSubscription, "")
	hmOpenAI = MakeSourceID(BuiltInRunnerID, CredOpenAISubscription, "")
	hmLuna   = MakeSourceID(BuiltInRunnerID, CredLunaRoute, "lr")
)

func hmSource(id SourceID, kind CredentialKind) Source {
	return Source{ID: id, RunnerID: BuiltInRunnerID, Kind: kind, Health: SourceHealth{Auth: "ok", Capacity: "available"}}
}

func newHMFixture(t *testing.T, srcs ...Source) *hmFixture {
	t.Helper()
	f := &hmFixture{
		clock:   &hmClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)},
		reg:     newHMRegistry(srcs...),
		log:     &hmEventLog{},
		prober:  &hmProber{res: map[SourceID]ProbeResult{}, err: map[SourceID]error{}},
		confirm: &hmConfirmer{err: map[SourceID]error{}},
		alerts:  &hmAlerts{},
	}
	f.mon = NewHealthMonitor(MonitorDeps{
		Registry: f.reg,
		Probers:  map[CredentialKind]SourceProber{CredAnthropicSubscription: f.prober, CredOpenAISubscription: f.prober, CredLunaRoute: f.prober},
		Confirm:  f.confirm, Alerts: f.alerts,
		Events:   NewEventRecorder(f.log, hmBus{}, 1000, f.clock.Now),
		Notifier: NewPoolNotifier(), Clock: f.clock.Now,
		Interval: time.Minute, Jitter: 10 * time.Second, Rand: func() float64 { return 0.5 },
	})
	return f
}

func (f *hmFixture) tick(t *testing.T) {
	t.Helper()
	require.NoError(t, f.mon.Tick(context.Background()))
	f.mon.wg.Wait() // confirmations run asynchronously; settle them
}

func hmTime(t time.Time) *time.Time { return &t }

// ---- tests ----

// Success: exhausted until T → exactly one probation, only after T + jitter.
func TestHealthMonitor_ExhaustedUntilT_SingleProbationAfterJitter(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	reset := f.clock.Now().Add(time.Hour)
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeExhausted, ResetAt: &reset}))
	h := f.reg.health(hmAnth)
	assert.Equal(t, CapacityState("exhausted"), h.Capacity)
	require.NotNil(t, h.ResetAt)
	assert.InDelta(t, 0.0, *h.Headroom, 1e-9)

	f.clock.Advance(time.Hour + 4*time.Second) // jitter offset = 0.5 * 10s = 5s
	f.tick(t)
	assert.Equal(t, CapacityState("exhausted"), f.reg.health(hmAnth).Capacity, "reset alone never proves availability")

	f.clock.Advance(time.Second)
	f.tick(t)
	assert.Equal(t, CapacityState("probation"), f.reg.health(hmAnth).Capacity)

	f.reg.mu.Lock()
	f.reg.inFlight[hmAnth] = 1 // a real step holds the single probation slot
	f.reg.mu.Unlock()
	f.clock.Advance(2 * time.Minute)
	f.tick(t)
	assert.Equal(t, CapacityState("probation"), f.reg.health(hmAnth).Capacity)
	assert.Equal(t, 0, f.confirm.count(), "no confirmation while a step is in flight")
}

// Edge: a reset already in the past goes straight to probation.
func TestHealthMonitor_PastResetStraightToProbation(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	past := f.clock.Now().Add(-time.Minute)
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeExhausted, ResetAt: &past}))
	assert.Equal(t, CapacityState("probation"), f.reg.health(hmAnth).Capacity)
}

// Success: probation → available after one successful step.
func TestHealthMonitor_ProbationAvailableAfterOneSuccess(t *testing.T) {
	src := hmSource(hmAnth, CredAnthropicSubscription)
	src.Health.Capacity = "probation"
	f := newHMFixture(t, src)
	hint := 0.4
	job := StepJob{Binding: provider.ResolvedBinding{Model: "claude-sonnet-4-5"}}
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeNone, Model: job.Binding.Model, HeadroomHint: &hint}))
	h := f.reg.health(hmAnth)
	assert.Equal(t, CapacityState("available"), h.Capacity)
	assert.InDelta(t, 0.4, *h.Headroom, 1e-9)
	assert.Contains(t, f.log.kinds(hmAnth), "source.recovered")
}

// Probation with zero in-flight gets ONE confirmation per interval; success → available.
func TestHealthMonitor_ProbationConfirmationBounded(t *testing.T) {
	src := hmSource(hmAnth, CredAnthropicSubscription)
	src.Health.Capacity = "probation"
	f := newHMFixture(t, src)
	f.confirm.err[hmAnth] = errors.New("still waiting") // unclassified: probation stays
	f.tick(t)
	assert.Equal(t, 1, f.confirm.count())
	f.clock.Advance(30 * time.Second)
	f.tick(t)
	assert.Equal(t, 1, f.confirm.count(), "at most once per ProbeInterval")
	delete(f.confirm.err, hmAnth)
	f.clock.Advance(31 * time.Second)
	f.tick(t)
	assert.Equal(t, 2, f.confirm.count())
	assert.Equal(t, CapacityState("available"), f.reg.health(hmAnth).Capacity)
}

// Unknown reset: after one interval → probation, then a bounded confirmation; a
// classified exhausted confirmation puts it back to exhausted.
func TestHealthMonitor_UnknownResetBoundedConfirmation(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeExhausted}))
	f.clock.Advance(30 * time.Second)
	f.tick(t)
	assert.Equal(t, CapacityState("exhausted"), f.reg.health(hmAnth).Capacity)
	assert.Equal(t, 0, f.confirm.count())

	f.clock.Advance(31 * time.Second)
	f.tick(t)
	assert.Equal(t, CapacityState("probation"), f.reg.health(hmAnth).Capacity)

	f.confirm.err[hmAnth] = WithOutcome(errors.New("quota"), SourceOutcome{Class: OutcomeExhausted})
	f.tick(t)
	assert.Equal(t, 1, f.confirm.count())
	assert.Equal(t, CapacityState("exhausted"), f.reg.health(hmAnth).Capacity)
}

// Codex ordinaryUsageAllowed: true confirms, false keeps (even past reset), null unknown.
func TestHealthMonitor_CodexOrdinaryUsageAllowed(t *testing.T) {
	yes, no := true, false
	for name, tc := range map[string]struct {
		oua  *bool
		want CapacityState
	}{"true confirms": {&yes, "available"}, "false keeps": {&no, "exhausted"}, "null unknown": {nil, "exhausted"}} {
		t.Run(name, func(t *testing.T) {
			f := newHMFixture(t, hmSource(hmOpenAI, CredOpenAISubscription))
			past := f.clock.Now().Add(-2 * time.Hour)
			future := f.clock.Now().Add(time.Hour)
			require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmOpenAI, SourceOutcome{Class: OutcomeExhausted, ResetAt: &future}))
			used := 99.0
			f.prober.res[hmOpenAI] = ProbeResult{Auth: "ok", Capacity: &CapacityObservation{OrdinaryUsageAllowed: tc.oua, ResetAt: &past, Windows: []LimitWindow{{LimitID: "codex", DurationMins: 10080, UsedPercent: &used}}}}
			f.tick(t)
			assert.Equal(t, tc.want, f.reg.health(hmOpenAI).Capacity)
			assert.Equal(t, 1, f.prober.calls[hmOpenAI])
			assert.Equal(t, 0, f.confirm.count(), "codex confirmation is read-only")
		})
	}
}

// Failure: logged out raises ONE notification and ONE source.logged_out event.
func TestHealthMonitor_LoggedOutOneNotificationOneEvent(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription), hmSource(hmOpenAI, CredOpenAISubscription))
	for i := 0; i < 3; i++ {
		require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeLoggedOut}))
	}
	assert.Equal(t, []SourceID{hmAnth}, f.alerts.loggedOut)
	n := 0
	for _, k := range f.log.kinds(hmAnth) {
		if k == "source.logged_out" {
			n++
		}
	}
	assert.Equal(t, 1, n)
	assert.Equal(t, AuthState("ok"), f.reg.health(hmOpenAI).Auth, "two sources never share health")
}

// Success: a successful auth probe clears logged_out but NOT exhaustion.
func TestHealthMonitor_AuthSuccessClearsLoggedOutNotExhaustion(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	future := f.clock.Now().Add(3 * time.Hour)
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeExhausted, ResetAt: &future}))
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmAnth, SourceOutcome{Class: OutcomeLoggedOut}))
	f.prober.res[hmAnth] = ProbeResult{Auth: "ok"}
	f.tick(t)
	h := f.reg.health(hmAnth)
	assert.Equal(t, AuthState("ok"), h.Auth)
	assert.Equal(t, CapacityState("exhausted"), h.Capacity)
	assert.Equal(t, []SourceID{hmAnth}, f.alerts.recovered)
	assert.Contains(t, f.log.kinds(hmAnth), "source.recovered")
}

// Read-only probes: once per interval per source, keyed by the Source given,
// never via the confirmer; a probe error changes nothing but LastProbeAt.
func TestHealthMonitor_ReadOnlyProbesIntervalGated(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription), hmSource(hmLuna, CredLunaRoute))
	f.prober.res[hmAnth] = ProbeResult{Auth: "logged_out"}
	f.prober.err[hmLuna] = errors.New("probe transport failed")
	f.tick(t)
	f.clock.Advance(10 * time.Second)
	f.tick(t)
	assert.Equal(t, 1, f.prober.calls[hmAnth])
	assert.Equal(t, 1, f.prober.calls[hmLuna])
	assert.Equal(t, AuthState("logged_out"), f.reg.health(hmAnth).Auth)
	assert.Equal(t, AuthState("ok"), f.reg.health(hmLuna).Auth)
	assert.NotNil(t, f.reg.health(hmLuna).LastProbeAt)
	assert.Equal(t, 0, f.confirm.count())
	f.clock.Advance(time.Minute)
	f.tick(t)
	assert.Equal(t, 2, f.prober.calls[hmAnth])
}

// Edge: LunaRoute busy / non-subscription "exhausted" is congestion, never exhaustion.
func TestHealthMonitor_NonSubscriptionNeverExhausted(t *testing.T) {
	f := newHMFixture(t, hmSource(hmLuna, CredLunaRoute))
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmLuna, SourceOutcome{Class: OutcomeExhausted}))
	h := f.reg.health(hmLuna)
	assert.Equal(t, CapacityState("available"), h.Capacity)
	assert.True(t, h.Congested)
	require.NoError(t, f.mon.ApplyOutcome(context.Background(), hmLuna, SourceOutcome{Class: OutcomeTransient, RetryAfter: 5 * time.Second}))
	f.clock.Advance(6 * time.Second)
	f.tick(t)
	assert.False(t, f.reg.health(hmLuna).Congested, "congestion expires")
}

func TestHealthMonitor_ProbeNowForcesProbe(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	f.prober.res[hmAnth] = ProbeResult{Auth: "ok"}
	f.tick(t)
	h, err := f.mon.ProbeNow(context.Background(), hmAnth)
	require.NoError(t, err)
	assert.Equal(t, AuthState("ok"), h.Auth)
	assert.Equal(t, 2, f.prober.calls[hmAnth], "on-demand probe ignores the interval")
	_, err = f.mon.ProbeNow(context.Background(), "builtin/none/x")
	assert.ErrorIs(t, err, ErrSourceNotFound)
}

func TestHealthMonitor_ApplyOutcomeUnknownSource(t *testing.T) {
	f := newHMFixture(t)
	assert.ErrorIs(t, f.mon.ApplyOutcome(context.Background(), "nope", SourceOutcome{Class: OutcomeLoggedOut}), ErrSourceNotFound)
}

func TestNewHealthMonitor_PanicsOnMissingDeps(t *testing.T) {
	f := newHMFixture(t)
	ok := f.mon.d
	for name, mutate := range map[string]func(*MonitorDeps){
		"registry": func(d *MonitorDeps) { d.Registry = nil }, "events": func(d *MonitorDeps) { d.Events = nil },
		"notifier": func(d *MonitorDeps) { d.Notifier = nil }, "alerts": func(d *MonitorDeps) { d.Alerts = nil },
		"confirm": func(d *MonitorDeps) { d.Confirm = nil }, "clock": func(d *MonitorDeps) { d.Clock = nil },
		"interval": func(d *MonitorDeps) { d.Interval = 0 },
	} {
		d := ok
		mutate(&d)
		assert.Panics(t, func() { NewHealthMonitor(d) }, name)
	}
}

var _ HealthTransitioner = (*HealthMonitor)(nil)
