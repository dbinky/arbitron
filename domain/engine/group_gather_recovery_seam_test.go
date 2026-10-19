package engine_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paired:8 — Area 7 group recovery ↔ Area 2 file gather. Unlike
// group_gather_reconciler_test.go (fakeGatherLocker, assertions stop at the
// wake), these join the REAL ParallelGroupExecutor's gatherGate, the REAL
// FileGatherer and the REAL GroupGatherReconciler over one run, the way
// cmd/arbitron/main.go wires them (the executor is the reconciler's locker
// and the gatherer is its aborter). The sink is the parent's area.

type gatherRecoverySeam struct {
	*fileExecFixture
	area *gateableArea
	rec  *engine.GroupGatherReconciler
}

// gateableArea parks the next Apply until released, so a test can act while
// the executor is inside Gather holding the gate.
type gateableArea struct {
	*fakeGatherArea
	mu      sync.Mutex
	park    bool
	entered chan struct{}
	release chan struct{}
}

func (a *gateableArea) parkNextApply() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.park, a.entered, a.release = true, make(chan struct{}), make(chan struct{})
}

func (a *gateableArea) Apply(ctx context.Context, h engine.GatherHandle) error {
	a.mu.Lock()
	park, entered, release := a.park, a.entered, a.release
	a.park = false
	a.mu.Unlock()
	if park {
		close(entered)
		<-release
	}
	return a.fakeGatherArea.Apply(ctx, h)
}

func newGatherRecoverySeam(t *testing.T, keys ...string) *gatherRecoverySeam {
	t.Helper()
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", keys...)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.WorkspaceSnapshot = r.ID.String() })
	for _, task := range f.groups.snapshot(f.runID).Tasks {
		require.NoError(t, f.instances.Create(context.Background(), &instance.Instance{ID: task.ChildInstanceID, OwnerID: f.parent.OwnerID, State: instance.StateCompleted}))
	}
	area := &gateableArea{fakeGatherArea: f.gatherFixture.area}
	gatherer := engine.NewGroupGatherer(f.groups, area, f.starter, f.bus)
	f.x.SetGatherer(gatherer)
	inventory := newFakeInventory(f.gatherFixture.area)
	inventory.add(f.runID.String(), time.Now())
	pruner := engine.NewGroupResourcePruner(f.groups, f.instances, &fakeChildLister{}, newDirWorkspaces(t), area, inventory)
	rec := engine.NewGroupGatherReconciler(f.groups, f.instances, f.nodeExecs, gatherer, &recordingCloser{}, &recordingCompleter{},
		&fakeNodeWaker{instances: f.instances}, f.qm, f.x, &fanoutBus{}, pruner)
	return &gatherRecoverySeam{fileExecFixture: f, area: area, rec: rec}
}

// crashAfterApply runs the parent's executor into the window between Apply
// writing the parent's area and the applied report (then SetFinalOutput)
// becoming durable. ReplaceGatherReport calls for two clean tasks: #1
// merging, #2 handle, #3 after a, #4 after b, #5 applied. The engine
// suspends a pendingFileGatherError at the node, so the parent is left
// waiting there.
func (s *gatherRecoverySeam) crashAfterApply(t *testing.T) {
	t.Helper()
	s.groups.failCall("ReplaceGatherReport", 5, errCrashAfterApply)
	_, err := s.execute(t)
	require.ErrorIs(t, err, errCrashAfterApply)
	require.Equal(t, instance.GroupRunGathering, s.groups.snapshot(s.runID).State)
	require.Equal(t, 1, s.area.applyCount())
	require.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"},
		s.area.parentFiles(gatherParentDir), "precondition: the crash happened after Apply")
	s.setParentState(instance.StateWaiting)
}

// B → A: startup recovery wakes the parent; the requeued parent's
// continueFileGather finishes the gather from the persisted progress, never
// re-merging a delta, and a later recovery pass leaves the done run alone.
func TestSeamGatherRecovery_CrashAfterApply_RecoveryWakesAndTheExecutorFinishesOnce(t *testing.T) {
	s := newGatherRecoverySeam(t, "a", "b")
	s.crashAfterApply(t)
	ctx := context.Background()

	require.NoError(t, s.rec.Reconcile(ctx))
	require.Equal(t, instance.StateQueued, s.parentState(), "recovery hands the gathering run back to its parent")
	assert.Zero(t, s.area.rollbackCount(), "a waiting parent's gather is resumed, never rolled back")

	s.setParentState(instance.StateRunning) // dequeued
	res, err := s.execute(t)
	require.NoError(t, err)
	require.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)

	run := s.groups.snapshot(s.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.True(t, run.ContinuationDelivered)
	assert.Equal(t, "applied", run.Gather.State)
	assert.Equal(t, []string{taskRef("a"), taskRef("b")}, s.area.mergeOrder(),
		"the resume starts from the persisted merge index: no task is merged again")
	assert.Equal(t, 1, s.area.timesApplied(taskRef("a")), "a's delta entered the staged tree exactly once")
	assert.Equal(t, 1, s.area.timesApplied(taskRef("b")), "b's delta entered the staged tree exactly once")
	assert.Len(t, s.area.begins(), 1, "the resume reuses the persisted staging handle")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"},
		s.area.parentFiles(gatherParentDir), "re-Apply converges on the staged result; nothing is doubled or lost")

	// A later sweep, even under a parent that has since failed, must not undo it.
	s.setParentState(instance.StateFailed)
	require.NoError(t, s.rec.Reconcile(ctx))
	assert.Zero(t, s.area.rollbackCount(), "a done run is never rolled back")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, s.area.parentFiles(gatherParentDir))
}

// A → B: while the requeued parent's executor is inside Gather (holding the
// gate), recovery that judges the parent dead must not roll the area back
// under it; once the executor has finished, recovery never reverses it.
func TestSeamGatherRecovery_RecoveryNeverRollsBackWhileTheExecutorFinishes(t *testing.T) {
	s := newGatherRecoverySeam(t, "a", "b")
	s.crashAfterApply(t)
	ctx := context.Background()
	require.NoError(t, s.rec.RecoverRun(ctx, s.runID))
	s.setParentState(instance.StateRunning)

	s.area.parkNextApply()
	done := make(chan error, 1)
	go func() {
		_, err := s.execute(t)
		done <- err
	}()
	select {
	case <-s.area.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the resumed executor never reached Apply")
	}

	// The parent is reported failed while its executor is mid-Apply: the only
	// rollback writer is the reconciler, and the gate must keep it out.
	s.setParentState(instance.StateFailed)
	require.NoError(t, s.rec.RecoverRun(ctx, s.runID))
	require.NoError(t, s.rec.Reconcile(ctx))
	assert.Zero(t, s.area.rollbackCount(), "no rollback while the executor holds the gather gate")
	assert.Equal(t, instance.GroupRunGathering, s.groups.snapshot(s.runID).State, "the executor's gather is not aborted under it")

	close(s.area.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the executor did not finish")
	}
	assert.Equal(t, instance.GroupRunDone, s.groups.snapshot(s.runID).State)

	require.NoError(t, s.rec.RecoverRun(ctx, s.runID))
	assert.Zero(t, s.area.rollbackCount(), "the finished gather is never reversed by a later pass")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, s.area.parentFiles(gatherParentDir))
}

// Crash recovery's own path: a parent RUNNING at the crash is failed by
// RecoverCrashedInstances, so recovery restores the exact pre-gather area
// and freezes failure; a retried parent then replays that failure instead of
// re-applying the staged tree.
func TestSeamGatherRecovery_CrashFailedParentIsRestoredAndARetryReplaysFailure(t *testing.T) {
	s := newGatherRecoverySeam(t, "a", "b")
	s.crashAfterApply(t)
	s.setParentState(instance.StateFailed)
	ctx := context.Background()

	require.NoError(t, s.rec.Reconcile(ctx))
	run := s.groups.snapshot(s.runID)
	require.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.Equal(t, map[string]string{"base.txt": "base"}, s.area.parentFiles(gatherParentDir), "the pre-gather area is restored")

	s.setParentState(instance.StateRunning) // Engine.Retry
	res, err := s.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort, "the retried parent consumes the frozen failure")
	assert.Equal(t, 1, s.area.applyCount(), "the staged tree is never applied again after the rollback")
	assert.Equal(t, map[string]string{"base.txt": "base"}, s.area.parentFiles(gatherParentDir))
}

// A completed resolution survives a crash: the resolution is committed in
// staging but the advanced merge index is not durable. Recovery wakes the
// parent (the attempt is ended) and the re-entered gather replays the commit
// as a no-op, advances once, and applies the operator's resolution.
func TestSeamGatherRecovery_CrashAfterResolutionCommitKeepsTheResolution(t *testing.T) {
	s := newGatherRecoverySeam(t, "a", "b")
	s.conflictOnB()
	res, err := s.execute(t)
	require.NoError(t, err)
	require.True(t, res.IsWait, "b's conflict opens a resolution session")
	s.setParentState(instance.StateWaiting)

	s.area.resolveStaged("shared.txt", "resolved")
	s.endOpenAttempts("")
	// Re-entry persists: #1 attempt resolved, CommitResolution, #2 merge index.
	s.groups.failCall("ReplaceGatherReport", 2, errCrashAfterApply)
	s.setParentState(instance.StateRunning)
	_, err = s.execute(t)
	require.ErrorIs(t, err, errCrashAfterApply)
	s.area.mu.Lock()
	commits := s.area.commits
	s.area.mu.Unlock()
	require.Equal(t, 1, commits, "precondition: the crash happened after the resolution was committed")
	require.Equal(t, 1, s.report().MergeIndex, "precondition: the advanced merge index was not durable")
	s.setParentState(instance.StateWaiting)

	require.NoError(t, s.rec.Reconcile(context.Background()))
	require.Equal(t, instance.StateQueued, s.parentState(), "recovery wakes the parent of an ended attempt")
	s.setParentState(instance.StateRunning)
	res, err = s.execute(t)
	require.NoError(t, err)
	require.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)

	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b", "shared.txt": "resolved"},
		s.area.parentFiles(gatherParentDir), "the operator's resolution reaches the parent")
	assert.Equal(t, []string{taskRef("a"), taskRef("b")}, s.area.mergeOrder(), "b is never merged again")
	assert.Zero(t, s.area.rollbackCount())
	rep := s.report()
	require.Len(t, rep.Attempts, 1)
	assert.Equal(t, engine.ResolutionOutcomeResolved, rep.Attempts[0].Outcome)
	assert.Equal(t, 2, rep.MergeIndex)
}
