package runner

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errSVUnused = errors.New("sv fake: unexpected call")

type svRegistry struct {
	mu      sync.Mutex
	runners map[RunnerID]Runner
	sources map[SourceID]Source
	slots   map[RunnerID]int
}

func (r *svRegistry) Snapshot(context.Context) (PoolSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap := PoolSnapshot{SlotsInUse: map[RunnerID]int{}, SourceInFlight: map[SourceID]int{}}
	for _, rn := range r.runners {
		snap.Runners = append(snap.Runners, rn)
	}
	sort.Slice(snap.Runners, func(i, j int) bool { return snap.Runners[i].ID < snap.Runners[j].ID })
	for _, s := range r.sources {
		snap.Sources = append(snap.Sources, s)
	}
	for k, v := range r.slots {
		snap.SlotsInUse[k] = v
	}
	return snap, nil
}
func (r *svRegistry) GetRunner(_ context.Context, id RunnerID) (Runner, []Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rn, ok := r.runners[id]
	if !ok {
		return Runner{}, nil, ErrRunnerNotFound
	}
	var srcs []Source
	for _, s := range r.sources {
		if s.RunnerID == id {
			srcs = append(srcs, s)
		}
	}
	return rn, srcs, nil
}
func (r *svRegistry) UpdateConfig(_ context.Context, id RunnerID, p RunnerConfigPatch) (Runner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rn, ok := r.runners[id]
	if !ok {
		return Runner{}, ErrRunnerNotFound
	}
	if p.StepLimit != nil {
		rn.StepLimit = *p.StepLimit
	}
	if p.Labels != nil {
		rn.Labels = p.Labels
	}
	r.runners[id] = rn
	return rn, nil
}
func (r *svRegistry) SetCordoned(_ context.Context, id RunnerID, c bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rn, ok := r.runners[id]
	if !ok {
		return ErrRunnerNotFound
	}
	rn.Cordoned = c
	r.runners[id] = rn
	return nil
}
func (r *svRegistry) GetSource(_ context.Context, id SourceID) (Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sources[id]
	if !ok {
		return Source{}, ErrSourceNotFound
	}
	return s, nil
}
func (r *svRegistry) RecordHealth(_ context.Context, id SourceID, h SourceHealth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sources[id]
	if !ok {
		return ErrSourceNotFound
	}
	s.Health = h
	r.sources[id] = s
	return nil
}
func (r *svRegistry) UpsertRunner(context.Context, Runner) error        { return errSVUnused }
func (r *svRegistry) SetDraining(context.Context, RunnerID, bool) error { return errSVUnused }
func (r *svRegistry) SetLiveness(context.Context, RunnerID, Liveness, time.Time) error {
	return errSVUnused
}
func (r *svRegistry) SetCompat(context.Context, RunnerID, Compatibility) error { return errSVUnused }
func (r *svRegistry) ReplaceSources(context.Context, RunnerID, []Source) error { return errSVUnused }
func (r *svRegistry) ListRunners(context.Context) ([]Runner, error)            { return nil, errSVUnused }

type svLedger struct{ jobs []Job }

func (l *svLedger) ListActive(context.Context) ([]Job, error) {
	var out []Job
	for _, j := range l.jobs {
		if j.State == JobQueued || j.State == JobOffered || j.State == JobRunning {
			out = append(out, j)
		}
	}
	return out, nil
}
func (l *svLedger) ListActiveByRunner(ctx context.Context, id RunnerID) ([]Job, error) {
	all, _ := l.ListActive(ctx)
	var out []Job
	for _, j := range all {
		if j.RunnerID == id {
			out = append(out, j)
		}
	}
	return out, nil
}
func (l *svLedger) EnsureIntent(context.Context, JobKey, string, ExecRequirements) (Job, error) {
	return Job{}, errSVUnused
}
func (l *svLedger) SetWait(context.Context, uuid.UUID, *WaitReason) error { return errSVUnused }
func (l *svLedger) Reserve(context.Context, uuid.UUID, Decision, int, time.Time) (Job, error) {
	return Job{}, errSVUnused
}
func (l *svLedger) MarkRunning(context.Context, Fence) error        { return errSVUnused }
func (l *svLedger) Complete(context.Context, Fence, string) error   { return errSVUnused }
func (l *svLedger) Fail(context.Context, Fence, OutcomeClass) error { return errSVUnused }
func (l *svLedger) Requeue(context.Context, Fence, OutcomeClass) (Job, error) {
	return Job{}, errSVUnused
}
func (l *svLedger) FenceCheck(context.Context, Fence) error             { return errSVUnused }
func (l *svLedger) ExtendLease(context.Context, Fence, time.Time) error { return errSVUnused }
func (l *svLedger) Get(context.Context, uuid.UUID) (Job, error)         { return Job{}, errSVUnused }
func (l *svLedger) FindByKey(context.Context, JobKey) (Job, bool, error) {
	return Job{}, false, errSVUnused
}
func (l *svLedger) ListByRun(context.Context, uuid.UUID) ([]Job, error)    { return nil, errSVUnused }
func (l *svLedger) Attempts(context.Context, uuid.UUID) ([]Attempt, error) { return nil, errSVUnused }

type svEventLog struct {
	mu        sync.Mutex
	evs       []PoolEvent // newest first
	lastLimit int
}

func (l *svEventLog) Append(_ context.Context, ev PoolEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append([]PoolEvent{ev}, l.evs...)
	return nil
}
func (l *svEventLog) List(_ context.Context, limit int) ([]PoolEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastLimit = limit
	if limit < len(l.evs) {
		return append([]PoolEvent(nil), l.evs[:limit]...), nil
	}
	return append([]PoolEvent(nil), l.evs...), nil
}
func (l *svEventLog) Prune(context.Context, int) error { return nil }
func (l *svEventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.evs {
		out = append(out, e.Kind)
	}
	return out
}

type svAudit struct {
	mu  sync.Mutex
	got []string
}

func (a *svAudit) Record(_ context.Context, action, target, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, action+" "+target)
}

type svVisibility map[uuid.UUID]bool

func (v svVisibility) CanView(_ context.Context, id uuid.UUID) bool { return v[id] }

type svBus struct{}

func (svBus) Publish(uuid.UUID, engine.Event)                   {}
func (svBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) { return nil, func() {} }
func (svBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) { return nil, func() {} }

type svProber struct{ seen []SourceID }

func (p *svProber) Probe(_ context.Context, s Source) (ProbeResult, error) {
	p.seen = append(p.seen, s.ID)
	return ProbeResult{Auth: AuthOK}, nil
}

type svNoConfirm struct{}

func (svNoConfirm) Confirm(context.Context, Source) error { return ErrConfirmationUnsupported }

type svNoAlerts struct{}

func (svNoAlerts) SourceLoggedOut(context.Context, Source) error { return nil }
func (svNoAlerts) SourceRecovered(context.Context, Source) error { return nil }

var (
	svAnth   = MakeSourceID(BuiltInRunnerID, CredAnthropicSubscription, "")
	svVisRun = uuid.New()
	svHidRun = uuid.New()
)

type svFixture struct {
	reg      *svRegistry
	ledger   *svLedger
	events   *svEventLog
	audit    *svAudit
	prober   *svProber
	notifier *PoolNotifier
	svc      *Service
}

func newSVFixture(t *testing.T) *svFixture {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	f := &svFixture{
		reg: &svRegistry{
			runners: map[RunnerID]Runner{BuiltInRunnerID: {ID: BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: 8}},
			sources: map[SourceID]Source{
				svAnth: {ID: svAnth, RunnerID: BuiltInRunnerID, Kind: CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Health: SourceHealth{Auth: AuthUnknown, Capacity: CapacityAvailable}},
			},
			slots: map[RunnerID]int{BuiltInRunnerID: 2},
		},
		ledger: &svLedger{jobs: []Job{
			{ID: uuid.New(), Key: JobKey{RunID: svVisRun}, NodeID: "visible-node", RunnerID: BuiltInRunnerID, SourceID: svAnth, State: JobRunning},
			{ID: uuid.New(), Key: JobKey{RunID: svHidRun}, NodeID: "secret-node", RunnerID: BuiltInRunnerID, SourceID: svAnth, State: JobRunning},
			{ID: uuid.New(), Key: JobKey{RunID: svVisRun}, State: JobQueued, Group: ModelGroup{Kind: CredAnthropicSubscription, Family: "anthropic"}, Wait: &WaitReason{Kind: WaitSlot}},
			{ID: uuid.New(), Key: JobKey{RunID: svVisRun}, State: JobQueued, Group: ModelGroup{Kind: CredAnthropicSubscription, Family: "anthropic"}, Wait: &WaitReason{Kind: WaitCapacity}},
			{ID: uuid.New(), Key: JobKey{RunID: svVisRun}, State: JobCompleted, RunnerID: BuiltInRunnerID},
		}},
		events:   &svEventLog{},
		audit:    &svAudit{},
		prober:   &svProber{},
		notifier: NewPoolNotifier(),
	}
	rec := NewEventRecorder(f.events, svBus{}, 1000, now)
	mon := NewHealthMonitor(MonitorDeps{
		Registry: f.reg, Probers: map[CredentialKind]SourceProber{CredAnthropicSubscription: f.prober},
		Confirm: svNoConfirm{}, Alerts: svNoAlerts{}, Events: rec, Notifier: f.notifier, Clock: now, Interval: time.Minute,
	})
	f.svc = NewService(ServiceDeps{
		Registry: f.reg, Ledger: f.ledger, Events: f.events, Recorder: rec, Audit: f.audit,
		Visibility: svVisibility{svVisRun: true}, Monitor: mon, Notifier: f.notifier, Clock: now,
	})
	return f
}

func svCtx(role identity.Role) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: role})
}

func TestService_ListAnyAuthenticatedUser(t *testing.T) {
	f := newSVFixture(t)
	views, err := f.svc.List(svCtx(identity.RoleUser))
	require.NoError(t, err)
	require.Len(t, views, 1)
	v := views[0]
	assert.Equal(t, BuiltInRunnerID, v.Runner.ID)
	assert.Equal(t, 2, v.SlotsUsed)
	assert.Equal(t, 1, v.Queued, "only slot waits served by this runner count as queued here")
	require.Len(t, v.Sources, 1)
	assert.Nil(t, v.Telemetry, "no telemetry is unknown, never zero")

	_, err = f.svc.List(context.Background())
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestService_GetHidesInvisibleRuns(t *testing.T) {
	f := newSVFixture(t)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.Len(t, d.Assignments, 2, "only offered/running jobs on this runner")
	var visible, hidden AssignmentView
	for _, a := range d.Assignments {
		if a.Hidden {
			hidden = a
		} else {
			visible = a
		}
	}
	require.NotNil(t, visible.RunID)
	assert.Equal(t, svVisRun, *visible.RunID)
	assert.Equal(t, "visible-node", visible.NodeID)
	assert.True(t, hidden.Hidden)
	assert.Nil(t, hidden.RunID)
	assert.Empty(t, hidden.NodeID, "node ids of hidden runs never leak")
	assert.Equal(t, svAnth, hidden.SourceID, "slot occupancy stays explainable")

	_, err = f.svc.Get(svCtx(identity.RoleUser), "laptop")
	assert.ErrorIs(t, err, ErrRunnerNotFound)
}

func TestService_EventsDropHiddenRunScoped(t *testing.T) {
	f := newSVFixture(t)
	ctx := context.Background()
	vis, hid := svVisRun, svHidRun
	require.NoError(t, f.events.Append(ctx, PoolEvent{ID: uuid.New(), Kind: EventSourceLoggedOut}))
	require.NoError(t, f.events.Append(ctx, PoolEvent{ID: uuid.New(), Kind: EventJobRerouted, RunID: &vis}))
	require.NoError(t, f.events.Append(ctx, PoolEvent{ID: uuid.New(), Kind: EventJobRerouted, RunID: &hid}))
	evs, err := f.svc.Events(svCtx(identity.RoleUser), 0)
	require.NoError(t, err)
	require.Len(t, evs, 2)
	for _, e := range evs {
		if e.RunID != nil {
			assert.Equal(t, svVisRun, *e.RunID)
		}
	}
	assert.Equal(t, 100, f.events.lastLimit, "default limit")
	_, err = f.svc.Events(svCtx(identity.RoleUser), 10_000)
	require.NoError(t, err)
	assert.Equal(t, 500, f.events.lastLimit, "limit is clamped")
	_, err = f.svc.Events(context.Background(), 10)
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestService_WritesAdminOnly(t *testing.T) {
	f := newSVFixture(t)
	user := svCtx(identity.RoleUser)
	twelve := 12
	_, err := f.svc.Update(user, BuiltInRunnerID, RunnerConfigPatch{StepLimit: &twelve})
	assert.ErrorIs(t, err, identity.ErrForbidden)
	assert.ErrorIs(t, f.svc.Cordon(user, BuiltInRunnerID), identity.ErrForbidden)
	assert.ErrorIs(t, f.svc.Uncordon(user, BuiltInRunnerID), identity.ErrForbidden)
	_, err = f.svc.ProbeSource(user, svAnth)
	assert.ErrorIs(t, err, identity.ErrForbidden)
	assert.ErrorIs(t, f.svc.Cordon(context.Background(), BuiltInRunnerID), identity.ErrUnauthenticated)

	assert.Equal(t, 8, f.reg.runners[BuiltInRunnerID].StepLimit)
	assert.False(t, f.reg.runners[BuiltInRunnerID].Cordoned)
	assert.Empty(t, f.audit.got)
	assert.Empty(t, f.prober.seen)
}

func TestService_UpdateAuditsEventsAndWakes(t *testing.T) {
	f := newSVFixture(t)
	wake, cancel := f.notifier.Subscribe()
	defer cancel()
	twelve := 12
	r, err := f.svc.Update(svCtx(identity.RoleAdmin), BuiltInRunnerID, RunnerConfigPatch{StepLimit: &twelve, Labels: map[string]string{"site": "hq"}})
	require.NoError(t, err)
	assert.Equal(t, 12, r.StepLimit)
	assert.Equal(t, 12, f.reg.runners[BuiltInRunnerID].StepLimit)
	assert.Equal(t, []string{"runner.update builtin"}, f.audit.got)
	assert.Contains(t, f.events.kinds(), EventRunnerUpdated)
	assert.Equal(t, CauseOperator, f.events.evs[0].Cause.Kind)
	assert.Equal(t, "root", f.events.evs[0].Cause.Actor)
	require.Eventually(t, func() bool {
		select {
		case <-wake:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond, "a raised step limit must wake slot waiters")
}

func TestService_UpdateRejectsInvalidStepLimit(t *testing.T) {
	f := newSVFixture(t)
	for _, n := range []int{0, 257} {
		n := n
		_, err := f.svc.Update(svCtx(identity.RoleAdmin), BuiltInRunnerID, RunnerConfigPatch{StepLimit: &n})
		assert.ErrorIs(t, err, ErrInvalidStepLimit)
	}
	assert.Empty(t, f.audit.got)
	two := 2
	_, err := f.svc.Update(svCtx(identity.RoleAdmin), "laptop", RunnerConfigPatch{StepLimit: &two})
	assert.ErrorIs(t, err, ErrRunnerNotFound)
	assert.Empty(t, f.audit.got, "failed writes are not audited")
}

func TestService_CordonUncordon(t *testing.T) {
	f := newSVFixture(t)
	admin := svCtx(identity.RoleAdmin)
	require.NoError(t, f.svc.Cordon(admin, BuiltInRunnerID))
	assert.True(t, f.reg.runners[BuiltInRunnerID].Cordoned)
	require.NoError(t, f.svc.Uncordon(admin, BuiltInRunnerID))
	assert.False(t, f.reg.runners[BuiltInRunnerID].Cordoned)
	assert.Equal(t, []string{"runner.cordon builtin", "runner.uncordon builtin"}, f.audit.got)
	assert.Contains(t, f.events.kinds(), EventRunnerCordoned)
	assert.Contains(t, f.events.kinds(), EventRunnerUncordoned)
	assert.ErrorIs(t, f.svc.Cordon(admin, "laptop"), ErrRunnerNotFound)
}

func TestService_ProbeSourceAudited(t *testing.T) {
	f := newSVFixture(t)
	h, err := f.svc.ProbeSource(svCtx(identity.RoleAdmin), svAnth)
	require.NoError(t, err)
	assert.Equal(t, AuthOK, h.Auth)
	assert.Equal(t, []SourceID{svAnth}, f.prober.seen, "the probe ran against the named source")
	assert.Equal(t, []string{"runner.source_probe " + string(svAnth)}, f.audit.got)
	_, err = f.svc.ProbeSource(svCtx(identity.RoleAdmin), "builtin/none/x")
	assert.ErrorIs(t, err, ErrSourceNotFound)
	assert.Len(t, f.audit.got, 1)
}

func TestNewService_PanicsOnMissingDeps(t *testing.T) {
	f := newSVFixture(t)
	ok := f.svc.d
	for name, mut := range map[string]func(*ServiceDeps){
		"registry": func(d *ServiceDeps) { d.Registry = nil }, "ledger": func(d *ServiceDeps) { d.Ledger = nil },
		"events": func(d *ServiceDeps) { d.Events = nil }, "recorder": func(d *ServiceDeps) { d.Recorder = nil },
		"audit": func(d *ServiceDeps) { d.Audit = nil }, "visibility": func(d *ServiceDeps) { d.Visibility = nil },
		"monitor": func(d *ServiceDeps) { d.Monitor = nil }, "notifier": func(d *ServiceDeps) { d.Notifier = nil },
	} {
		d := ok
		mut(&d)
		assert.Panics(t, func() { NewService(d) }, name)
	}
}
