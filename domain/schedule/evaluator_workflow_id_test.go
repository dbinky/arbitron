package schedule_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeResolver is an in-memory schedule.WorkflowResolver keyed by id->name. A
// rename reassigns the name an id maps to; a miss returns workflow.ErrNotFound
// (the registry sentinel) to simulate a deleted/unregistered workflow.
type fakeResolver struct {
	mu       sync.Mutex
	idToName map[uuid.UUID]string
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{idToName: map[uuid.UUID]string{}}
}

func (r *fakeResolver) set(id uuid.UUID, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idToName[id] = name
}

func (r *fakeResolver) delete(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.idToName, id)
}

func (r *fakeResolver) ResolveName(_ context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.idToName[id]
	if !ok {
		return "", workflow.ErrNotFound
	}
	return name, nil
}

// --- success: a schedule survives a rename — it fires the workflow's CURRENT name. ---

func TestEvaluator_FiresByID_AfterRename(t *testing.T) {
	creator := uuid.New()
	idA := uuid.New()
	s := dueSchedule(creator, "")
	s.WorkflowID = idA
	s.WorkflowName = "alpha" // the label captured at create time
	repo := newEvalRepo(s)

	res := newFakeResolver()
	res.set(idA, "beta") // simulate a rename: idA now resolves to "beta"

	tr := &fakeTrigger{}
	al := &fakeAlerter{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := schedule.NewEvaluator(repo, tr, &fakeActive{}, &fakeIdents{}, al,
		schedule.WithClock(clk), schedule.WithResolver(res))

	require.NoError(t, ev.Tick(context.Background()))

	assert.Equal(t, 1, tr.callCount(), "the schedule must fire")
	assert.Equal(t, "beta", tr.lastWF,
		"fire must resolve the stable id to the CURRENT name (renamed definition), not the stale label")
	_, fired := repo.lastFiredFor(s.ID)
	assert.True(t, fired, "fire position recorded on a clean fire")
}

// --- failure: a schedule whose workflow was deleted logs+skips; the loop continues. ---

func TestEvaluator_DeletedWorkflow_SkipsAndAdvances_LoopContinues(t *testing.T) {
	creator := uuid.New()
	idGone := uuid.New()
	deleted := dueSchedule(creator, "")
	deleted.WorkflowID = idGone
	deleted.WorkflowName = "ghost"

	// A second, healthy schedule that must still fire in the same tick (the loop
	// must not crash on the deleted one).
	idLive := uuid.New()
	healthy := dueSchedule(creator, "")
	healthy.WorkflowID = idLive
	healthy.WorkflowName = "alive"

	repo := newEvalRepo(deleted, healthy)

	res := newFakeResolver()
	// idGone is NOT registered (deleted). idLive resolves to "alive".
	res.set(idLive, "alive")

	// The trigger fails with workflow.ErrNotFound for the unresolvable name so the
	// deleted-workflow branch (alert + advance, leave enabled) is taken.
	tr := &deletedAwareTrigger{deletedName: "ghost"}
	al := &fakeAlerter{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := schedule.NewEvaluator(repo, tr, &fakeActive{}, &fakeIdents{}, al,
		schedule.WithClock(clk), schedule.WithResolver(res))

	// Tick must not return an error: one bad schedule never crashes the loop.
	require.NoError(t, ev.Tick(context.Background()))

	// The healthy schedule still fired.
	assert.Equal(t, 1, tr.firedFor("alive"), "the healthy schedule must still fire despite the deleted neighbour")
	// The deleted schedule raised a miss alert and advanced its position.
	assert.GreaterOrEqual(t, al.count(notification.TypeScheduleMissed), 1,
		"a deleted-workflow occurrence raises a schedule_missed alert")
	_, advanced := repo.lastFiredFor(deleted.ID)
	assert.True(t, advanced, "the deleted schedule advances its position so it does not hot-loop")
}

// deletedAwareTrigger returns workflow.ErrNotFound when asked to trigger
// deletedName, and succeeds otherwise; it counts fires per workflow name.
type deletedAwareTrigger struct {
	mu          sync.Mutex
	deletedName string
	fired       map[string]int
}

func (t *deletedAwareTrigger) TriggerWithDepartment(_ context.Context, workflowName string, _ map[string]any, _, _ string) (uuid.UUID, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired == nil {
		t.fired = map[string]int{}
	}
	if workflowName == t.deletedName {
		return uuid.Nil, false, workflow.ErrNotFound
	}
	t.fired[workflowName]++
	return uuid.New(), true, nil
}

func (t *deletedAwareTrigger) firedFor(name string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fired[name]
}

// --- edge: a legacy Nil-id schedule fires by its WorkflowName label. ---

func TestEvaluator_LegacyNilID_FiresByName(t *testing.T) {
	creator := uuid.New()
	s := dueSchedule(creator, "")
	s.WorkflowID = uuid.Nil // legacy: no id reference
	s.WorkflowName = "legacy-report"
	repo := newEvalRepo(s)

	res := newFakeResolver() // resolver wired, but the schedule has no id to resolve
	tr := &fakeTrigger{}
	al := &fakeAlerter{}
	clk := &fakeClock{t: time.Date(2026, 6, 8, 9, 30, 0, 0, time.UTC)}
	ev := schedule.NewEvaluator(repo, tr, &fakeActive{}, &fakeIdents{}, al,
		schedule.WithClock(clk), schedule.WithResolver(res))

	require.NoError(t, ev.Tick(context.Background()))

	assert.Equal(t, 1, tr.callCount(), "a legacy Nil-id schedule still fires")
	assert.Equal(t, "legacy-report", tr.lastWF,
		"a legacy Nil-id schedule fires by its retained WorkflowName label")
}
