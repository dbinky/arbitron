package schedule_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type evalRepo struct {
	mu        sync.Mutex
	schedules map[uuid.UUID]*schedule.Schedule
	lastFired map[uuid.UUID]time.Time
}

func newEvalRepo(ss ...*schedule.Schedule) *evalRepo {
	r := &evalRepo{schedules: map[uuid.UUID]*schedule.Schedule{}, lastFired: map[uuid.UUID]time.Time{}}
	for _, s := range ss {
		r.schedules[s.ID] = s
	}
	return r
}

func (r *evalRepo) Create(ctx context.Context, s *schedule.Schedule) error {
	r.schedules[s.ID] = s
	return nil
}
func (r *evalRepo) Get(ctx context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	s, ok := r.schedules[id]
	if !ok {
		return nil, schedule.ErrNotFound
	}
	return s, nil
}
func (r *evalRepo) List(ctx context.Context, f schedule.ListFilters) ([]schedule.Schedule, error) {
	var out []schedule.Schedule
	for _, s := range r.schedules {
		out = append(out, *s)
	}
	return out, nil
}
func (r *evalRepo) ListEnabled(ctx context.Context) ([]schedule.Schedule, error) {
	var out []schedule.Schedule
	for _, s := range r.schedules {
		if s.Enabled {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *evalRepo) Update(ctx context.Context, s *schedule.Schedule) error {
	r.schedules[s.ID] = s
	return nil
}
func (r *evalRepo) Delete(ctx context.Context, id uuid.UUID) error {
	delete(r.schedules, id)
	return nil
}
func (r *evalRepo) SetLastFired(ctx context.Context, id uuid.UUID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFired[id] = at
	if s, ok := r.schedules[id]; ok {
		t := at
		s.LastFiredAt = &t
	}
	return nil
}
func (r *evalRepo) lastFiredFor(id uuid.UUID) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.lastFired[id]
	return t, ok
}

type fakeTrigger struct {
	mu        sync.Mutex
	calls     int
	lastWF    string
	lastVars  map[string]any
	lastDept  string
	lastPrio  string
	lastIdent identity.Identity
	identOK   bool
	lastCtx   context.Context // the trigger context captured for origin assertions
	err       error
}

func (f *fakeTrigger) TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority, department string) (uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastWF = workflowName
	f.lastVars = variables
	f.lastDept = department
	f.lastPrio = priority
	f.lastCtx = ctx
	f.lastIdent, f.identOK = identity.FromContext(ctx)
	if f.err != nil {
		return uuid.Nil, false, f.err
	}
	return uuid.New(), true, nil
}
func (f *fakeTrigger) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *fakeTrigger) capturedCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCtx
}

type fakeActive struct {
	active bool
	err    error
}

func (f *fakeActive) HasActiveRun(ctx context.Context, workflowName string, ownerID uuid.UUID) (bool, error) {
	return f.active, f.err
}

type fakeIdents struct {
	id  identity.Identity
	err error
}

func (f *fakeIdents) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	if f.err != nil {
		return identity.Identity{}, f.err
	}
	id := f.id
	id.UserID = userID
	return id, nil
}

type fakeAlerter struct {
	mu     sync.Mutex
	alerts []notification.Type
}

func (a *fakeAlerter) Alert(ctx context.Context, s schedule.Schedule, t notification.Type) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, t)
	return nil
}
func (a *fakeAlerter) count(t notification.Type) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, x := range a.alerts {
		if x == t {
			n++
		}
	}
	return n
}
func (a *fakeAlerter) total() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.alerts) }

// dueSchedule builds an enabled schedule whose next-fire (from CreatedAt) is at
// or before the given clock, given a daily-at-09:00 UTC cron.
func dueSchedule(creator uuid.UUID, dept string) *schedule.Schedule {
	created := time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC) // 06:00, before 09:00 fire
	return &schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		Timezone:     "UTC",
		CreatorID:    creator,
		Department:   dept,
		Enabled:      true,
		Variables:    map[string]any{"k": "v"},
		CreatedAt:    created,
	}
}

func newEval(t *testing.T, repo schedule.ScheduleRepository, tr schedule.Trigger, act schedule.ActiveRunChecker, idn schedule.IdentityResolver, al schedule.Alerter, clk *fakeClock) *schedule.Evaluator {
	return schedule.NewEvaluator(repo, tr, act, idn, al,
		schedule.WithClock(clk),
		schedule.WithInterval(10*time.Millisecond),
	)
}

// --- Happy-path ---

func TestEvaluator_DueSchedule_FiresAsCreator(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "eng")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	idn := &fakeIdents{id: identity.Identity{Departments: []string{"eng"}}}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, idn, al, clk)

	require.NoError(t, ev.Tick(context.Background()))

	assert.Equal(t, 1, tr.callCount())
	assert.Equal(t, "report", tr.lastWF)
	assert.Equal(t, "normal", tr.lastPrio)
	assert.Equal(t, "eng", tr.lastDept)
	assert.Equal(t, map[string]any{"k": "v"}, tr.lastVars)
	require.True(t, tr.identOK, "trigger must run with creator identity in context")
	assert.Equal(t, creator, tr.lastIdent.UserID)
	assert.Equal(t, 0, al.total())
}

// FireOneOrigin: after a due cron fire, the captured trigger context must carry
// an Origin with Source=schedule and the originating schedule's id.
func TestEvaluator_FireOneOrigin_StampsScheduleOrigin(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "eng")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	idn := &fakeIdents{id: identity.Identity{Departments: []string{"eng"}}}
	ev := newEval(t, repo, tr, &fakeActive{}, idn, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	require.Equal(t, 1, tr.callCount())

	o, ok := instance.OriginFromContext(tr.capturedCtx())
	require.True(t, ok, "cron fire must carry an origin on the trigger context")
	assert.Equal(t, instance.OriginSchedule, o.Source)
	require.NotNil(t, o.ScheduleID)
	assert.Equal(t, s.ID, *o.ScheduleID)
}

func TestEvaluator_AfterFire_SetsLastFired(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	lf, ok := repo.lastFiredFor(s.ID)
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC), lf.UTC())

	// Second tick at same clock must NOT fire again (position advanced).
	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 1, tr.callCount())
}

func TestEvaluator_VariablesPassedThrough(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	s.Variables = map[string]any{"a": float64(1), "b": "two"}
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, map[string]any{"a": float64(1), "b": "two"}, tr.lastVars)
}

// --- Input-validation / policy ---

func TestEvaluator_NotDue_DoesNotFire(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 8, 0, 0, 0, time.UTC)} // before 09:00
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 0, tr.callCount())
	assert.Equal(t, 0, al.total())
}

func TestEvaluator_DisabledNotInListEnabled(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	s.Enabled = false
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 0, tr.callCount())
}

// --- Error-handling ---

func TestEvaluator_Collision_SkipsAndAlerts(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{active: true}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 0, tr.callCount(), "collision must not create an instance")
	assert.Equal(t, 1, al.count(notification.TypeScheduleSkipped))
	_, advanced := repo.lastFiredFor(s.ID)
	assert.True(t, advanced, "position must advance so it does not re-alert every tick")

	// Second tick at same clock must not re-alert.
	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 1, al.count(notification.TypeScheduleSkipped))
}

func TestEvaluator_TriggerError_LogsNoCrash(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{err: errors.New("boom")}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	// A subsequent tick still works (loop survives).
	require.NoError(t, ev.Tick(context.Background()))
}

func TestEvaluator_DeletedWorkflow_Alerts(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{err: workflow.ErrNotFound}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.GreaterOrEqual(t, al.count(notification.TypeScheduleMissed), 1)
	// Keeps running.
	require.NoError(t, ev.Tick(context.Background()))
}

func TestEvaluator_CreatorUnresolvable_SkipsGracefully(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	idn := &fakeIdents{err: errors.New("user gone")}
	ev := newEval(t, repo, tr, &fakeActive{}, idn, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 0, tr.callCount())
}

// --- Edge-cases / boundary (no-backfill recovery) ---

func TestEvaluator_Recover_MissedOccurrences_NoBackfill(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	// Last fired well in the past; clock now days later (several missed 09:00s).
	past := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	s.LastFiredAt = &past
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Recover(context.Background()))
	assert.Equal(t, 0, tr.callCount(), "no backfill on recovery")
	assert.Equal(t, 1, al.count(notification.TypeScheduleMissed))
	_, advanced := repo.lastFiredFor(s.ID)
	assert.True(t, advanced, "recovery must advance the position")

	// Next future occurrence (next day 09:00) must then fire normally.
	clk.mu.Lock()
	clk.t = time.Date(2026, 6, 9, 9, 30, 0, 0, time.UTC)
	clk.mu.Unlock()
	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 1, tr.callCount())
}

func TestEvaluator_Recover_FutureNextFire_NoAlert(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	recent := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	s.LastFiredAt = &recent
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	// Clock right after the last fire; next fire (tomorrow 09:00) is in the future.
	clk := &fakeClock{t: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Recover(context.Background()))
	assert.Equal(t, 0, al.total())
	_, advanced := repo.lastFiredFor(s.ID)
	assert.False(t, advanced, "future next-fire must not advance position")
}

// error — a schedule with a corrupt cron expression (could happen if the DB
// row was written outside the management surface) must be skipped without
// crashing the loop; the other schedules in the same tick must still fire.
func TestEvaluator_CorruptCron_SkipsWithoutCrash(t *testing.T) {
	creator := uuid.New()
	bad := &schedule.Schedule{
		ID: uuid.New(), WorkflowName: "bad", CronExpr: "not-a-cron",
		Timezone: "UTC", CreatorID: creator, Enabled: true,
		CreatedAt: time.Date(2026, 6, 8, 6, 0, 0, 0, time.UTC),
	}
	good := dueSchedule(creator, "")
	repo := newEvalRepo(bad, good)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()), "Tick must not return an error for a corrupt cron")
	assert.Equal(t, 1, tr.callCount(), "the valid schedule must still fire")
}

// error — an error from HasActiveRun (e.g. DB failure) logs and skips the
// occurrence without crashing the loop.
func TestEvaluator_ActiveRunCheckError_SkipsWithoutCrash(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{err: errors.New("db down")}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 0, tr.callCount(), "active-run error must skip the occurrence, not fire")
}

func TestEvaluator_ExactlyDue_Boundary(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	// Clock exactly at the 09:00 fire instant.
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 1, tr.callCount(), "next == now counts as due")
}

// error — a corrupt cron row must be skipped in Recover too (same guard as Tick).
func TestEvaluator_Recover_CorruptCron_SkipsWithoutCrash(t *testing.T) {
	creator := uuid.New()
	bad := &schedule.Schedule{
		ID: uuid.New(), WorkflowName: "bad", CronExpr: "not-a-cron",
		Timezone: "UTC", CreatorID: creator, Enabled: true,
		CreatedAt: time.Date(2026, 6, 1, 6, 0, 0, 0, time.UTC),
	}
	repo := newEvalRepo(bad)
	clk := &fakeClock{t: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, nil, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Recover(context.Background()), "Recover must not error for a corrupt cron")
	assert.Equal(t, 0, al.total(), "a corrupt row must not raise an alert")
}

// edge — exactly one occurrence missed (LastFiredAt right before the scheduled
// time): Recover advances position and raises one schedule_missed alert.
func TestEvaluator_Recover_ExactlyOneMissed(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	// Last fired right before the 09:00 tick, so exactly one occurrence was missed.
	justBefore := time.Date(2026, 6, 8, 8, 59, 0, 0, time.UTC)
	s.LastFiredAt = &justBefore
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	// Clock shortly after 09:00 — one tick was missed.
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	al := &fakeAlerter{}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, al, clk)

	require.NoError(t, ev.Recover(context.Background()))
	assert.Equal(t, 0, tr.callCount(), "no backfill")
	assert.Equal(t, 1, al.count(notification.TypeScheduleMissed), "one alert for the one missed occurrence")

	// Next future fire (tomorrow 09:00) must now run normally.
	clk.mu.Lock()
	clk.t = time.Date(2026, 6, 9, 9, 30, 0, 0, time.UTC)
	clk.mu.Unlock()
	require.NoError(t, ev.Tick(context.Background()))
	assert.Equal(t, 1, tr.callCount())
}

// --- Integration ---

func TestEvaluator_StartStop_GracefulShutdown(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev.Start(ctx)

	require.Eventually(t, func() bool { return tr.callCount() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"the real ticker must fire Tick against the due schedule")

	done := make(chan struct{})
	go func() { ev.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return promptly (goroutine leak)")
	}
}

// Stop must be idempotent: a second Stop (or a Stop after the loop already
// exited via ctx cancellation) must not panic on a close-of-closed channel.
func TestEvaluator_Stop_Idempotent(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	tr := &fakeTrigger{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := newEval(t, repo, tr, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ev.Start(ctx)

	require.Eventually(t, func() bool { return tr.callCount() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"the real ticker must fire Tick against the due schedule")

	done := make(chan struct{})
	go func() {
		ev.Stop()
		ev.Stop() // second Stop must be a no-op, not a panic.
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated Stop did not return promptly")
	}
}

// Stop after the loop exited via ctx cancellation must also be safe.
func TestEvaluator_Stop_AfterContextCancel(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	repo := newEvalRepo(s)
	ev := newEval(t, repo, &fakeTrigger{}, &fakeActive{}, &fakeIdents{}, &fakeAlerter{},
		&fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)})

	ctx, cancel := context.WithCancel(context.Background())
	ev.Start(ctx)
	cancel() // loop exits via ctx.Done().

	done := make(chan struct{})
	go func() { ev.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop after ctx-cancel did not return promptly")
	}
}

// Recovery must never stamp LastFiredAt with `now` (a non-occurrence instant):
// the advanced position must be an actual cron occurrence at or before now, so
// the next NextFire computes from a real schedule slot.
func TestEvaluator_Recover_StampsRealOccurrenceNotNow(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "") // "0 9 * * *" daily at 09:00 UTC, created 06:00.
	repo := newEvalRepo(s)
	now := time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC) // two missed occurrences.
	clk := &fakeClock{t: now}
	ev := newEval(t, repo, &fakeTrigger{}, &fakeActive{}, &fakeIdents{}, &fakeAlerter{}, clk)

	require.NoError(t, ev.Recover(context.Background()))

	last, ok := repo.lastFiredFor(s.ID)
	require.True(t, ok, "recovery must advance the position")
	assert.NotEqual(t, now, last, "must not stamp `now` (a non-occurrence)")
	// The latest occurrence at/before 2026-06-10 09:30 is 2026-06-10 09:00.
	assert.Equal(t, time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC), last)
}
