package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileExecFixture runs the REAL ParallelGroupExecutor + REAL FileGatherer over
// P3-T6's gatherFixture (a decided results_and_files run at node g1 whose
// tasks finished in reverse author order) and an instance repo holding the
// parent at g1. The SINK is fakeGatherArea.parentFiles(gatherParentDir).
type fileExecFixture struct {
	*gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	qm        *mockQueueManager
	x         *engine.ParallelGroupExecutor
	node      workflow.Node
}

func newFileExecFixture(t *testing.T, rule, outcome, winner string, keys ...string) *fileExecFixture {
	t.Helper()
	g := newGatherFixture(t, rule, outcome, winner, keys...)
	instances := newMockInstanceRepo()
	nodeID := "g1"
	parent := *g.parent
	parent.CurrentNodeID = &nodeID
	parent.Variables = map[string]any{"workspace": gatherParentDir}
	require.NoError(t, instances.Create(context.Background(), &parent))

	nodeExecs := newMockNodeExecutionRepo()
	qm := newMockQueueManager()
	canceller := &recordingCanceller{instances: instances}
	launcher := engine.NewGroupLauncher(g.groups, &fakeGroupTaskStarter{instances: instances}, nil, nil, nil,
		fixedIdentityResolver{}, canceller, g.bus)
	coord := engine.NewGroupCoordinator(g.groups, instances, &memWaker{instances: instances}, canceller,
		fixedIdentityResolver{}, g.bus, qm)
	x := engine.NewParallelGroupExecutor(instances, nodeExecs, g.groups, launcher, coord, &nodeClaims{instances: instances}, qm)
	x.SetGatherer(g.gatherer)

	behavior := workflow.GroupUnfinishedWaitForRemaining
	if rule == workflow.GroupRuleFirstSuccess {
		behavior = workflow.GroupUnfinishedFinishIndependently
	}
	tasks := make([]workflow.GroupTask, 0, len(keys))
	for _, k := range keys {
		tasks = append(tasks, workflow.GroupTask{Key: k, Entry: k + "-entry"})
	}
	cfg := workflow.GroupConfig{
		Form: workflow.GroupFormBranches, Tasks: tasks, CompletionRule: rule, UnfinishedBehavior: behavior,
		GatherMode: workflow.GroupGatherResultsAndFiles, ResolverProfile: "resolver",
	}
	return &fileExecFixture{gatherFixture: g, instances: instances, nodeExecs: nodeExecs, qm: qm, x: x, node: gxGroupNode("g1", false, cfg)}
}

func (f *fileExecFixture) execute(t *testing.T) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parent.ID)
	return f.x.Execute(ctx, f.node, variable.NewStore(), "wf")
}

func (f *fileExecFixture) setParentState(s instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parent.ID].State = s
}

func (f *fileExecFixture) parentState() instance.State {
	return f.instances.get(f.parent.ID).State
}

func TestParallelGroupExecutor_FileGather_AllSucceedAppliesAndRoutesSuccess(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")

	res, err := f.execute(t)
	require.NoError(t, err)
	require.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	output, ok := res.Output.(map[string]any)
	require.True(t, ok)
	gather, ok := output["gather"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "applied", gather["state"])
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))
	assert.Equal(t, []string{taskRef("a"), taskRef("b")}, f.area.mergeOrder(), "author order, not completion order")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.True(t, run.ContinuationDelivered)
}

func TestParallelGroupExecutor_FileGather_MergeFailureRollsBackAndRoutesFailureKeepingTheDecision(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort)
	output, ok := res.Output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "failure", output["outcome"], "the final group outcome follows the routed port")
	assert.Equal(t, "success", output["taskOutcome"], "the frozen task decision is never rewritten")
	gather := output["gather"].(map[string]any)
	assert.Equal(t, "rolled_back", gather["state"])
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "no partial work reaches the parent")
	run := f.groups.snapshot(f.runID)
	require.NotNil(t, run.Decision)
	assert.Equal(t, workflow.GroupPortSuccess, run.Decision.Outcome)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
}

func TestParallelGroupExecutor_FileGather_ConflictSuspendsWithoutHoldingASlot(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "an open resolution session suspends the parent")
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.GreaterOrEqual(t, f.qm.notifyCount(), 1, "the waiting parent releases its queue slot")
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)
	require.Len(t, f.starter.calls(), 1)

	f.setParentState(instance.StateRunning)
	res, err = f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "re-entry while the session is open keeps waiting")
	assert.Len(t, f.starter.calls(), 1, "no second session for the same attempt")
}

func TestParallelGroupExecutor_FileGather_ReEntryAfterResolutionResumesWithoutReapplying(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	res, err := f.execute(t)
	require.NoError(t, err)
	require.True(t, res.IsWait)

	f.area.resolveStaged("shared.txt", "merged")
	f.endOpenAttempts("")
	f.setParentState(instance.StateRunning)

	res, err = f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "merged", "a.txt": "from-a", "b.txt": "from-b"},
		f.area.parentFiles(gatherParentDir))
	assert.Equal(t, 1, f.area.timesApplied(taskRef("a")), "the merge done before the session is never re-applied")
	assert.Equal(t, engine.ResolutionOutcomeResolved, f.report().Attempts[0].Outcome)
}

func TestParallelGroupExecutor_FileGather_RollbackFailureSuspendsUntilRepaired(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")
	f.area.failRollback = errors.New("read-only file system")

	res, err := f.execute(t)
	require.NoError(t, err, "a failed rollback is not a node error: an error behaviour could otherwise continue past it")
	assert.True(t, res.IsWait)
	assert.Equal(t, instance.StateWaiting, f.parentState())
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "continuation stays blocked")
	require.NotNil(t, run.Gather)
	assert.Equal(t, "rollback_failed", run.Gather.RollbackStatus)

	f.area.failRollback = nil
	f.setParentState(instance.StateRunning)
	res, err = f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort)
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
	assert.Equal(t, "rolled_back", f.groups.snapshot(f.runID).Gather.RollbackStatus)
}

func TestParallelGroupExecutor_FileGather_CancelledParentIsNeverGathered(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.setParentState(instance.StateCancelled)

	_, err := f.execute(t)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "Engine.handleNodeError halts without overwriting the cancelled state")
	assert.Empty(t, f.area.begins(), "nothing is staged for a cancelled parent; the reconciler settles it")
	assert.Equal(t, instance.GroupRunDecided, f.groups.snapshot(f.runID).State)
}

func TestParallelGroupExecutor_FileGather_ReloadFailureIsANodeError(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.failOnce("Get", errors.New("database is locked"))

	_, err := f.execute(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload group run")
	assert.Empty(t, f.area.begins())
}

// blockingStarter holds StartResolution until released, so a test can observe
// the executor in the middle of a Gather call.
type blockingStarter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

var _ engine.GroupResolutionStarter = (*blockingStarter)(nil)

func (s *blockingStarter) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	if ref.GroupRunID == uuid.Nil {
		return errors.New("blocking starter: missing run")
	}
	return nil
}

func (s *blockingStarter) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	if ref.GroupRunID == uuid.Nil || reason != "completed" {
		return errors.New("blocking starter: invalid session closure")
	}
	return nil
}

func (s *blockingStarter) StartResolution(_ context.Context, run *instance.GroupRun, parent *instance.Instance, req engine.ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return &instance.ResolutionSessionRef{GroupRunID: run.ID, MergeIndex: req.MergeIndex, Attempt: req.Attempt, InstanceID: parent.ID, NodeID: run.NodeID}, nil
}

// PROVENANCE: the gate the reconciler probes is the one the executor holds for
// the whole Gather call.
func TestParallelGroupExecutor_FileGather_TryLockGatherExcludesTheReconcilerWhileGathering(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	blocker := &blockingStarter{entered: make(chan struct{}), release: make(chan struct{})}
	f.x.SetGatherer(engine.NewGroupGatherer(f.groups, f.area, blocker, f.bus))

	done := make(chan error, 1)
	go func() {
		_, err := f.execute(t)
		done <- err
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never reached the resolution session")
	}

	_, ok := f.x.TryLockGather(f.runID)
	assert.False(t, ok, "a run whose gather is being written cannot be taken by the reconciler")
	unlockOther, ok := f.x.TryLockGather(uuid.New())
	require.True(t, ok, "other runs are not blocked")
	unlockOther()

	close(blocker.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the executor did not finish after the session opened")
	}
	unlock, ok := f.x.TryLockGather(f.runID)
	require.True(t, ok, "the gate is released when Gather returns")
	unlock()
}

// paired:4 BREAK — the parent must never gather while the decision's
// unfinished-task actions are unapplied. Here the coordinator's re-entry
// Evaluate cannot record them, so the parent waits with nothing staged; once
// they are applied, the next entry gathers.
func TestParallelGroupExecutor_FileGather_NeverStartsBeforeUnfinishedActionsAreApplied(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleFirstSuccess, workflow.GroupPortSuccess, "a", "a", "b")
	f.groups.mu.Lock()
	f.groups.runs[f.runID].ActionsApplied = false
	f.groups.mu.Unlock()
	f.groups.failAlways("MarkActionsApplied", errors.New("database is locked"))

	res, err := f.execute(t)
	require.NoError(t, err)
	require.True(t, res.IsWait, "a decided run whose actions are unapplied keeps the parent waiting")
	assert.Empty(t, f.area.begins(), "nothing is staged before the unfinished actions are applied")
	assert.Equal(t, instance.GroupRunDecided, f.groups.snapshot(f.runID).State)
	assert.Equal(t, instance.StateWaiting, f.parentState())

	f.groups.failAlways("MarkActionsApplied", nil)
	f.setParentState(instance.StateRunning)
	res, err = f.execute(t)
	require.NoError(t, err)
	require.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	assert.Equal(t, []string{taskRef("a")}, f.area.mergeOrder(), "only the frozen winner is merged")
}
