package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingResource is an engine.WorkspaceResource that records every call
// with its load-bearing arguments (run, dir, reason) and runs per-call hooks,
// so a test can stand in for "restore the committed checkpoint into dir".
type recordingResource struct {
	mu         sync.Mutex
	calls      []string
	claim      engine.ResourceFenceRef
	acquireErr error
	commitErr  error
	onAcquire  func(dir string) error
	onCommit   func(dir string) error
	order      *callLog // optional cross-fake ordering (group_finalize_test.go)
}

var _ engine.WorkspaceResource = (*recordingResource)(nil)

func (r *recordingResource) Acquire(_ context.Context, runID uuid.UUID, dir, reason string) (engine.ResourceFenceRef, error) {
	r.mu.Lock()
	r.calls = append(r.calls, fmt.Sprintf("acquire %s %s %s", runID, dir, reason))
	hook, err := r.onAcquire, r.acquireErr
	r.mu.Unlock()
	if r.order != nil {
		r.order.add("Acquire")
	}
	if err != nil {
		return engine.ResourceFenceRef{}, err
	}
	if hook != nil {
		if err := hook(dir); err != nil {
			return engine.ResourceFenceRef{}, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claim = engine.ResourceFenceRef{Key: "run:" + runID.String(), Owner: "builtin", Generation: 1, Token: uuid.New()}
	return r.claim, nil
}

func (r *recordingResource) Commit(_ context.Context, claim engine.ResourceFenceRef, runID uuid.UUID, dir string) error {
	r.mu.Lock()
	if claim != r.claim || claim.Key != "run:"+runID.String() || claim.Validate() != nil {
		r.mu.Unlock()
		return errors.New("commit did not receive the acquired claim")
	}
	r.calls = append(r.calls, fmt.Sprintf("commit %s %s", runID, dir))
	hook, err := r.onCommit, r.commitErr
	r.mu.Unlock()
	if r.order != nil {
		r.order.add("Commit")
	}
	if err != nil {
		return err
	}
	if hook != nil {
		return hook(dir)
	}
	return nil
}

func (r *recordingResource) entries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// RR-11: a remote step left the parent's files in the committed checkpoint,
// not in the server directory. Every branch must fork from the restored
// content, so Acquire runs before the snapshot.
func TestGroupLauncher_AcquiresTheParentWorkingAreaBeforeTheSnapshot(t *testing.T) {
	f := newLaunchFixture(t)
	res := &recordingResource{onAcquire: func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "from-remote.txt"), []byte("pushed by mac-1"), 0o644)
	}}
	f.launcher.SetWorkspaceResource(res)

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.NoError(t, err)

	assert.Equal(t, []string{"acquire " + f.parent.ID.String() + " " + f.parentWS + " group snapshot", "release run:" + f.parent.ID.String()}, res.entries())
	reqs := f.starter.requests()
	require.Len(t, reqs, 2)
	for _, r := range reqs {
		b, err := os.ReadFile(filepath.Join(f.workspaces.path(r.TaskInstanceID), "from-remote.txt"))
		require.NoError(t, err, "SINK: every branch forks from the restored parent content")
		assert.Equal(t, "pushed by mac-1", string(b))
	}
}

func TestGroupLauncher_AcquireFailureIsPreLaunch(t *testing.T) {
	f := newLaunchFixture(t)
	f.launcher.SetWorkspaceResource(&recordingResource{
		acquireErr: fmt.Errorf("%w: job j1 (work) on \"mac-1\"", engine.ErrWorkspaceBusy),
	})

	_, err := f.launcher.Launch(context.Background(), f.request(t, glBranchConfig()))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	require.ErrorIs(t, err, engine.ErrWorkspaceBusy)
	assert.Contains(t, err.Error(), "acquire the parent's working area")
	assert.Empty(t, f.groupWS.snapshotHandles(), "no snapshot of a working area the server does not hold")
	f.assertNothingPersisted(t)
}

func TestGroupLauncher_NoParentWorkingAreaAcquiresNothing(t *testing.T) {
	f := newLaunchFixture(t)
	res := &recordingResource{}
	l := engine.NewGroupLauncher(f.groups, f.starter, f.contracts, nil, nil, f.identities, f.canceller, f.bus)
	l.SetWorkspaceResource(res)
	cfg := glBranchConfig()
	delete(cfg.Tasks[0].Inputs, "work_dir")

	_, err := l.Launch(context.Background(), f.requestWith(t, cfg, glStore(t, "")))
	require.NoError(t, err)
	assert.Empty(t, res.entries(), "a run without a working area has nothing to acquire")
}

func (r *recordingResource) Release(_ context.Context, claim engine.ResourceFenceRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if claim != r.claim || claim.Validate() != nil {
		panic("release did not receive the acquired claim")
	}
	r.calls = append(r.calls, "release "+claim.Key)
	return nil
}

func finalizeResourceFixture(t *testing.T, state instance.GroupRunState) (*callLog, uuid.UUID, uuid.UUID, *finalizeGroupRepo, *fakeWorkingArea, *engine.GroupFileFinalizer) {
	t.Helper()
	log := &callLog{}
	runID, taskID := uuid.New(), uuid.New()
	run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles,
		State: state, WorkspaceSnapshot: "snapshot",
		Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: taskID, Blocking: true}}}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	return log, runID, taskID, groups, area, engine.NewGroupFileFinalizer(groups, area, &fakeWorkspaceManager{})
}

// A task that ran on a remote runner left its files in its checkpoint; the
// immutable ref must be taken from the restored directory.
func TestGroupFileFinalizer_AcquiresTheTaskWorkingAreaBeforeFinalizing(t *testing.T) {
	log, runID, taskID, groups, _, fin := finalizeResourceFixture(t, instance.GroupRunRunning)
	res := &recordingResource{order: log}
	fin.SetWorkspaceResource(res)

	require.NoError(t, fin.FinalizeTerminal(context.Background(), &instance.Instance{ID: taskID, GroupRunID: &runID}, true))

	dir := "/tmp/arbitron-test-ws/" + taskID.String()
	assert.Equal(t, []string{"acquire " + taskID.String() + " " + dir + " group finalize", "release run:" + taskID.String()}, res.entries())
	assert.Equal(t, []string{"Acquire", "FinalizeTask", "SetTaskFinalizedRef:a=refs/arbitron/tasks/" + taskID.String()}, log.snapshot(),
		"the area is acquired before the ref is cut")
	assert.Equal(t, "refs/arbitron/tasks/"+taskID.String(), groups.finalizedRef(runID, "a"))
}

func TestGroupFileFinalizer_AcquireFailurePublishesNoRef(t *testing.T) {
	log, runID, taskID, groups, area, fin := finalizeResourceFixture(t, instance.GroupRunRunning)
	fin.SetWorkspaceResource(&recordingResource{order: log, acquireErr: errors.New("checkpoint store offline")})

	err := fin.FinalizeTerminal(context.Background(), &instance.Instance{ID: taskID, GroupRunID: &runID}, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization)
	assert.Contains(t, err.Error(), `task "a": acquire its working area: checkpoint store offline`)
	assert.Empty(t, area.finalizeCalls())
	assert.Empty(t, groups.finalizedRef(runID, "a"))
}

func TestGroupFileFinalizer_IneligibleTaskAcquiresNothing(t *testing.T) {
	_, runID, taskID, _, _, fin := finalizeResourceFixture(t, instance.GroupRunDecided)
	res := &recordingResource{}
	fin.SetWorkspaceResource(res)

	require.NoError(t, fin.FinalizeTerminal(context.Background(), &instance.Instance{ID: taskID, GroupRunID: &runID}, true))
	assert.Empty(t, res.entries(), "a task that can no longer be merged never moves its home")
}

func TestFileGatherer_AcquiresBeforeMergingAndCommitsTheAppliedArea(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	beginsAtAcquire := -1
	var committed map[string]string
	res := &recordingResource{
		onAcquire: func(string) error { beginsAtAcquire = len(f.area.begins()); return nil },
		onCommit:  func(dir string) error { committed = f.area.parentFiles(dir); return nil },
	}
	f.gatherer.SetWorkspaceResource(res)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Zero(t, beginsAtAcquire, "the parent area is acquired before the gather begins")
	assert.Equal(t, []string{
		"acquire " + f.parent.ID.String() + " " + gatherParentDir + " group gather",
		"commit " + f.parent.ID.String() + " " + gatherParentDir,
		"release run:" + f.parent.ID.String(),
	}, res.entries())
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, committed,
		"SINK: the checkpoint is taken from the applied parent area")
	assert.Equal(t, instance.GatherStateApplied, f.report().State)
}

func TestFileGatherer_AcquireFailureFailsTheGatherWithoutTouchingTheArea(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	res := &recordingResource{acquireErr: engine.ErrWorkspaceBusy}
	f.gatherer.SetWorkspaceResource(res)

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins())
	assert.Zero(t, f.area.applyCount())
	assert.Len(t, res.entries(), 1, "nothing is committed after a refused acquire")
	assert.Contains(t, f.report().Error, "acquire the parent working area: engine: a step of the run is using its working area")
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_CommitFailureRollsTheApplyBack(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.gatherer.SetWorkspaceResource(&recordingResource{commitErr: errors.New("checkpoint disk full")})

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.applyCount())
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: the parent area returns to the content the last checkpoint holds")
	assert.Contains(t, f.report().Error, "commit the gathered working area: checkpoint disk full")
	assert.Equal(t, instance.GatherStateRolledBack, f.report().State)
	assert.Equal(t, instance.RollbackStatusRolledBack, f.report().RollbackStatus)
}

// A resolved conflict resumes in a later pass, which takes its own parent claim.
func TestFileGatherer_ResolvedConflictPassReacquiresAndCommits(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	var committed map[string]string
	res := &recordingResource{
		onAcquire: func(dir string) error {
			assert.Equal(t, gatherParentDir, dir)
			assert.Zero(t, f.area.applyCount(), "the parent claim precedes Apply, including on resume")
			return nil
		},
		onCommit: func(dir string) error { committed = f.area.parentFiles(dir); return nil },
	}
	f.gatherer.SetWorkspaceResource(res)
	_, err := f.gather()
	require.NoError(t, err)
	acq := "acquire " + f.parent.ID.String() + " " + gatherParentDir + " group gather"
	rel := "release run:" + f.parent.ID.String()
	assert.Equal(t, []string{acq, rel}, res.entries(), "a suspended pass releases its claim")

	f.area.resolveStaged("shared.txt", "merged-a-and-b")
	f.endOpenAttempts("")
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, []string{acq, rel, acq, "commit " + f.parent.ID.String() + " " + gatherParentDir, rel}, res.entries())
	assert.Equal(t, "merged-a-and-b", committed["shared.txt"], "SINK: the resolved, applied area is what is committed")
	assert.Equal(t, instance.GatherStateApplied, f.report().State)
}

// AbortGather rolls back under its own claim.
func TestFileGatherer_AbortGatherRollsBackUnderItsOwnClaim(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	res := &recordingResource{onAcquire: func(dir string) error {
		assert.Equal(t, gatherParentDir, dir)
		assert.Zero(t, f.area.rollbackCount(), "the abort claim precedes Rollback")
		return nil
	}}
	f.gatherer.SetWorkspaceResource(res)
	_, err := f.gather()
	require.NoError(t, err)

	step, err := f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.NoError(t, err)
	require.True(t, step.Done)
	acq := "acquire " + f.parent.ID.String() + " " + gatherParentDir + " group gather"
	rel := "release run:" + f.parent.ID.String()
	assert.Equal(t, []string{acq, rel, acq, rel}, res.entries())
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, instance.GatherStateRolledBack, f.report().State)
}

// Refusing an abort claim must block continuation, not fake a rollback.
func TestFileGatherer_AbortGatherRefusedClaimIsARollbackFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	res := &recordingResource{}
	f.gatherer.SetWorkspaceResource(res)
	_, err := f.gather()
	require.NoError(t, err)
	res.acquireErr = engine.ErrWorkspaceBusy

	_, err = f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)
	assert.Zero(t, f.area.rollbackCount())
	assert.Equal(t, instance.RollbackStatusFailed, f.report().RollbackStatus)
	assert.NotEqual(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)

	res.acquireErr = nil
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
}
