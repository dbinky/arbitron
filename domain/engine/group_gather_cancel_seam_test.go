package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paired:9 — Area 2 file gather ↔ Area 6 parent cancellation. The parent's
// terminal state is the abort signal: a gather that stops with an error leaves
// the parent suspended at the node for the reconciler, and a cancelled parent
// is the reconciler's cue to roll back. These join the REAL Engine run loop,
// ParallelGroupExecutor, FileGatherer, GroupGatherReconciler and SQLite
// instance state; the sink is the parent's state and its working area.

var errGatherOutputStorage = errors.New("gather output storage unavailable")

type gatherCancelSeam struct {
	*gatherFixture
	area     *gateableArea
	repo     *adaptsqlite.InstanceRepo
	race     *parkRaceRepo
	eng      *engine.Engine
	x        *engine.ParallelGroupExecutor
	rec      *engine.GroupGatherReconciler
	sink     *mockExecutor
	parentID uuid.UUID
	owner    uuid.UUID
}

// newGatherCancelSeam persists a decided two-task files run under a parent
// queued at g1 in SQLite and assembles the engine, executor, gatherer and
// reconciler the way cmd/arbitron/main.go does (the executor is the
// reconciler's gather locker, the gatherer its aborter). raceAtSuspend lands a
// full Engine.Cancel immediately before the parent's waiting write.
func newGatherCancelSeam(t *testing.T, raceAtSuspend bool) *gatherCancelSeam {
	t.Helper()
	ctx := context.Background()
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.WorkspaceSnapshot = r.ID.String() })
	// ReplaceGatherReport calls for two clean tasks: #1 merging, #2 handle,
	// #3 after a, #4 after b, #5 applied. Failing #5 strands the applied area.
	f.groups.failCall("ReplaceGatherReport", 5, errGatherOutputStorage)
	area := &gateableArea{fakeGatherArea: f.area}
	gatherer := engine.NewGroupGatherer(f.groups, area, f.starter, f.bus)

	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	repo := adaptsqlite.NewInstanceRepo(db)
	nodes := adaptsqlite.NewNodeExecutionRepo(db)
	users := adaptsqlite.NewUserRepo(db)
	queueRepo := adaptsqlite.NewQueueRepo(db)
	owner, err := users.GetDefaultUser(ctx)
	require.NoError(t, err)

	node := "g1"
	parent := *f.parent
	parent.OwnerID = owner
	parent.State = instance.StateQueued
	parent.Priority = instance.PriorityNormal
	parent.CurrentNodeID = &node
	parent.WorkflowSnapshot = groupWorkflowSnapshot(t)
	parent.WorkflowSnapshot["nodes"] = append(parent.WorkflowSnapshot["nodes"].([]any),
		map[string]any{"id": "after", "type": "script"}, map[string]any{"id": "fail", "type": "script"})
	parent.WorkflowSnapshot["edges"] = []any{
		map[string]any{"source": "g1", "target": "after", "output": "success"},
		map[string]any{"source": "g1", "target": "fail", "output": "failure"},
	}
	parent.ModelPins, parent.ModelPinsVersion = map[string]string{}, instance.ModelPinsVersionSnapshot
	require.NoError(t, repo.Create(ctx, &parent))

	s := &gatherCancelSeam{gatherFixture: f, area: area, repo: repo, parentID: parent.ID, owner: owner}
	qm := newMockQueueManager()
	s.race = &parkRaceRepo{InstanceRepo: repo, raced: make(chan struct{})}
	s.race.parent = func() uuid.UUID {
		if raceAtSuspend {
			return parent.ID
		}
		return uuid.Nil
	}
	s.race.race = func() { require.NoError(t, s.eng.Cancel(ctxAs(owner), parent.ID)) }
	s.eng = wireModelPins(engine.NewEngine(nil, s.race, nodes, adaptsqlite.NewVariableSnapshotRepo(db), users,
		map[workflow.NodeType]engine.NodeExecutor{}, f.bus, qm, queueRepo, ""))
	s.eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)
	s.eng.SetSpawnLinks(repo)
	t.Cleanup(s.eng.Shutdown)

	ids := fixedIdentityResolver{}
	launcher := engine.NewGroupLauncher(f.groups, s.eng, s.eng, nil, nil, ids, s.eng, f.bus)
	coord := engine.NewGroupCoordinator(f.groups, s.race, queueRepo, s.eng, ids, f.bus, qm)
	s.x = engine.NewParallelGroupExecutor(s.race, nodes, f.groups, launcher, coord, s.race, qm)
	s.x.SetGatherer(gatherer)
	s.eng.RegisterExecutor(workflow.NodeTypeParallelGroup, s.x)
	s.sink = newMockExecutor()
	s.eng.RegisterExecutor(workflow.NodeTypeScript, s.sink)
	s.eng.FreezeExecutors()

	inventory := newFakeInventory(f.area)
	inventory.add(f.runID.String(), time.Now())
	pruner := engine.NewGroupResourcePruner(f.groups, repo, &fakeChildLister{}, newDirWorkspaces(t), area, inventory)
	s.rec = engine.NewGroupGatherReconciler(f.groups, repo, nodes, gatherer, &recordingCloser{}, &recordingCompleter{},
		queueRepo, qm, s.x, &fanoutBus{}, pruner)
	return s
}

func (s *gatherCancelSeam) parentState(t *testing.T) instance.State {
	t.Helper()
	p, err := s.repo.Get(context.Background(), s.parentID)
	require.NoError(t, err)
	return p.State
}

func (s *gatherCancelSeam) assertStaysCancelled(t *testing.T, msg string) {
	t.Helper()
	assert.Never(t, func() bool {
		p, err := s.repo.Get(context.Background(), s.parentID)
		return err != nil || p.State != instance.StateCancelled
	}, 300*time.Millisecond, 10*time.Millisecond, msg)
}

// assertSweepRollsBackUnderTheCancelledParent runs the periodic sweep entry
// and checks the sink: area restored, run settled on failure, parent still
// cancelled and routed through neither port.
func (s *gatherCancelSeam) assertSweepRollsBackUnderTheCancelledParent(t *testing.T) {
	t.Helper()
	require.NoError(t, s.rec.Reconcile(context.Background()))
	assert.Equal(t, instance.StateCancelled, s.parentState(t), "the cancelled parent is never woken through the gather route")
	assert.Equal(t, map[string]string{"base.txt": "base"}, s.area.parentFiles(gatherParentDir),
		"recovery under the cancelled parent restores the pre-gather area")
	run := s.groups.snapshot(s.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.True(t, run.ContinuationDelivered, "settled: the run leaves the unsettled sweep")
	for _, call := range s.sink.getCalls() {
		assert.NotContains(t, []string{"after", "fail"}, call.NodeID, "a cancelled parent routes neither port")
	}
}

var appliedParent = map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}

// A → B: the gather fails after Apply changed the parent's area, and Engine.Cancel
// commits `cancelled` while the engine is suspending the parent for
// reconciliation (after handleNodeError's context check, before the cancel func
// fires). The suspension must not revive the cancelled parent as waiting:
// recovery would then wake it through the gather route and keep the applied
// changes. B → A: the sweep sees the cancellation and rolls the area back.
func TestSeamGatherCancel_CancelWhileSuspendingAPendingGatherNeverRevivesTheParent(t *testing.T) {
	s := newGatherCancelSeam(t, true)
	require.NoError(t, s.eng.StartInstance(context.Background(), s.parentID))
	select {
	case <-s.race.raced:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine never suspended the parent after the gather error")
	}
	require.Equal(t, appliedParent, s.area.parentFiles(gatherParentDir), "precondition: the gather stopped after Apply")
	s.assertStaysCancelled(t, "suspending a pending gather must never overwrite cancelled")
	s.assertSweepRollsBackUnderTheCancelledParent(t)
}

// Cancellation while the executor holds the gather gate inside Apply: the
// reconciler's immediate recovery (what Watch runs on instance.cancelled)
// must stand aside rather than roll back under the writer; the executor's
// stranded pass leaves the parent cancelled, and the next sweep rolls back.
func TestSeamGatherCancel_CancelDuringAnExecutorHeldGatherIsRolledBackByTheNextSweep(t *testing.T) {
	s := newGatherCancelSeam(t, false)
	ctx := context.Background()
	s.area.parkNextApply()
	require.NoError(t, s.eng.StartInstance(ctx, s.parentID))
	select {
	case <-s.area.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never reached Apply")
	}
	require.NoError(t, s.eng.Cancel(ctxAs(s.owner), s.parentID))
	require.Equal(t, instance.StateCancelled, s.parentState(t))

	require.NoError(t, s.rec.RecoverRun(ctx, s.runID))
	assert.Zero(t, s.area.rollbackCount(), "recovery never rolls back while the executor holds the gate")
	assert.Equal(t, instance.GroupRunGathering, s.groups.snapshot(s.runID).State)

	close(s.area.release)
	require.Eventually(t, func() bool {
		unlock, ok := s.x.TryLockGather(s.runID)
		if ok {
			unlock()
		}
		return ok && s.area.applyCount() == 1
	}, 5*time.Second, 10*time.Millisecond, "the executor released the gate after its pass")
	require.Equal(t, appliedParent, s.area.parentFiles(gatherParentDir), "precondition: the pass stopped after Apply")
	s.assertStaysCancelled(t, "the stranded pass leaves the parent cancelled")
	s.assertSweepRollsBackUnderTheCancelledParent(t)
}
