package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parkRaceRepo lands a full Engine.Cancel of the parent immediately before the
// executor's write that parks it waiting. The write itself runs on a detached
// context: it was already in flight when Cancel committed `cancelled` and before
// Cancel reached the parent's run-context cancel func.
type parkRaceRepo struct {
	*adaptsqlite.InstanceRepo
	parent func() uuid.UUID
	race   func()
	once   sync.Once
	raced  chan struct{}
}

func (r *parkRaceRepo) fire(id uuid.UUID) {
	if id == r.parent() {
		r.once.Do(func() { r.race(); close(r.raced) })
	}
}

func (r *parkRaceRepo) UpdateState(ctx context.Context, id uuid.UUID, state instance.State) error {
	if state == instance.StateWaiting {
		r.fire(id)
		ctx = context.WithoutCancel(ctx)
	}
	return r.InstanceRepo.UpdateState(ctx, id, state)
}

func (r *parkRaceRepo) SuspendRunning(ctx context.Context, id uuid.UUID) (bool, error) {
	r.fire(id)
	return r.InstanceRepo.SuspendRunning(context.WithoutCancel(ctx), id)
}

// Area 6 ↔ Area 4 (paired:1), reverse direction: the launcher finished, and the
// parent is cancelled just before the executor parks it. Parking must never
// overwrite `cancelled` with `waiting`; otherwise the coordinator decides the
// cancelled tasks as a failure and wakes the parent through its failure port.
func TestParallelGroupExecutor_CancelBeforeParkingNeverRevivesTheParent(t *testing.T) {
	ctx := context.Background()
	wfRepo := newMockWorkflowRepo()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewInstanceRepo(db)
	nodeExecs := adaptsqlite.NewNodeExecutionRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	bus := newMockEventBus()
	exec := newMockExecutor()
	qm := newMockQueueManager()
	eng := engine.NewEngine(wfRepo, repo, nodeExecs, adaptsqlite.NewVariableSnapshotRepo(db), users,
		map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec}, bus, qm, adaptsqlite.NewQueueRepo(db), "")
	qm.mu.Lock()
	qm.enqueueFn = func(ctx context.Context, id uuid.UUID, _ uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	qm.mu.Unlock()
	eng.SetGroupSupport(engine.GroupSupportResults)
	eng.SetSpawnLinks(repo)
	t.Cleanup(eng.Shutdown)

	owner, err := users.GetDefaultUser(ctx)
	require.NoError(t, err)
	var parentMu sync.Mutex
	var parentID uuid.UUID
	race := &parkRaceRepo{InstanceRepo: repo, raced: make(chan struct{})}
	race.parent = func() uuid.UUID { parentMu.Lock(); defer parentMu.Unlock(); return parentID }
	race.race = func() { require.NoError(t, eng.Cancel(ctxAs(owner), race.parent())) }

	groups := adaptsqlite.NewGroupRunRepo(db)
	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(groups, eng, eng, nil, nil, ids, eng, bus)
	coord := engine.NewGroupCoordinator(groups, repo, adaptsqlite.NewQueueRepo(db), eng, ids, bus, qm)
	eng.RegisterExecutor(workflow.NodeTypeParallelGroup,
		engine.NewParallelGroupExecutor(race, nodeExecs, groups, launcher, coord, race, qm))
	eng.FreezeExecutors()

	wf := gxWorkflow()
	wfRepo.add(&wf)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	block := func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		<-release
		return &engine.NodeResult{Output: map[string]any{}}, nil
	}
	exec.onNode("a1", block)
	exec.onNode("b1", block)

	// Hold the parent's lock across Trigger so the race hook cannot read a
	// zero id; the parent reaches the group only after Trigger returns.
	parentMu.Lock()
	id, _, err := eng.Trigger(ctxAs(owner), wf.Name, map[string]any{"repo": "acme/widgets"}, "normal")
	parentID = id
	parentMu.Unlock()
	require.NoError(t, err)

	select {
	case <-race.raced:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never parked the parent")
	}
	run, err := groups.GetByParentNode(ctx, id, "g")
	require.NoError(t, err)
	require.NotNil(t, run)
	for _, task := range run.Tasks {
		gxWaitForInstance(t, repo, task.ChildInstanceID, instance.StateCancelled)
	}

	// The sweep decides the cancelled tasks; it must find no live parent to wake.
	require.NoError(t, coord.Reconcile(ctx))
	assert.Never(t, func() bool {
		p, err := repo.Get(ctx, id)
		return err != nil || p.State != instance.StateCancelled
	}, 300*time.Millisecond, 10*time.Millisecond, "the cancelled parent must stay cancelled")
	for _, call := range exec.getCalls() {
		assert.NotEqual(t, "fail", call.NodeID, "a cancelled parent never routes its failure port")
		assert.NotEqual(t, "after", call.NodeID)
	}
}
