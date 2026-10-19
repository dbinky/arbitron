package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errUnusedInFinalizeFake = errors.New("not used by the finalizer tests")

// callLog records cross-fake call order.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, s)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// finalizeGroupRepo is an in-memory instance.GroupRunRepository keyed by run
// id; only the methods the finalizer uses are live, the rest fail loudly.
type finalizeGroupRepo struct {
	mu        sync.Mutex
	runs      map[uuid.UUID]*instance.GroupRun
	getErr    error
	setRefErr error
	log       *callLog
}

func newFinalizeGroupRepo(log *callLog, runs ...*instance.GroupRun) *finalizeGroupRepo {
	r := &finalizeGroupRepo{runs: map[uuid.UUID]*instance.GroupRun{}, log: log}
	for _, run := range runs {
		r.runs[run.ID] = run
	}
	return r
}

func (r *finalizeGroupRepo) Get(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	run, ok := r.runs[id]
	if !ok {
		return nil, fmt.Errorf("group run %s: %w", id, instance.ErrGroupRunNotFound)
	}
	cp := *run
	cp.Tasks = append([]instance.GroupTask(nil), run.Tasks...)
	return &cp, nil
}

func (r *finalizeGroupRepo) SetTaskFinalizedRef(_ context.Context, runID uuid.UUID, key, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("SetTaskFinalizedRef:" + key + "=" + ref)
	if r.setRefErr != nil {
		return r.setRefErr
	}
	if key == "" || ref == "" {
		return errors.New("task key and finalized ref are required")
	}
	run, ok := r.runs[runID]
	if !ok {
		return instance.ErrGroupRunNotFound
	}
	for i := range run.Tasks {
		if run.Tasks[i].Key == key {
			run.Tasks[i].FinalizedRef = ref
			return nil
		}
	}
	return instance.ErrGroupRunNotFound
}

func (r *finalizeGroupRepo) finalizedRef(runID uuid.UUID, key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.runs[runID].Tasks {
		if t.Key == key {
			return t.FinalizedRef
		}
	}
	return ""
}

func (r *finalizeGroupRepo) CreateWithTasks(context.Context, *instance.GroupRun) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) GetByParentNode(context.Context, uuid.UUID, string) (*instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) GetByChild(context.Context, uuid.UUID) (*instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) ListByParent(context.Context, uuid.UUID) ([]instance.GroupRun, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) ListUnsettled(context.Context) ([]uuid.UUID, error) {
	return nil, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskLaunched(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskLaunchFailed(context.Context, uuid.UUID, string, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) TransitionState(context.Context, uuid.UUID, instance.GroupRunState, instance.GroupRunState) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) RecordTaskTerminal(context.Context, uuid.UUID, string, instance.GroupTaskStatus, map[string]any, map[string]any, time.Time) (int64, bool, error) {
	return 0, false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) UpdateTaskLiveStatus(context.Context, uuid.UUID, string, instance.GroupTaskStatus) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) Decide(context.Context, uuid.UUID, instance.GroupDecision) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskCancelRequested(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkTaskContinuingIndependently(context.Context, uuid.UUID, string) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkActionsApplied(context.Context, uuid.UUID) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) SetFinalOutput(context.Context, uuid.UUID, string, map[string]any, instance.GroupRunState) (bool, error) {
	return false, errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) MarkContinuationDelivered(context.Context, uuid.UUID) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) SetGatherReport(context.Context, uuid.UUID, instance.GatherReport) error {
	return errUnusedInFinalizeFake
}
func (r *finalizeGroupRepo) ReplaceGatherReport(context.Context, uuid.UUID, *instance.GatherReport, instance.GatherReport) error {
	return errUnusedInFinalizeFake
}

// finalizeCall is one FinalizeTask invocation.
type finalizeCall struct {
	Handle string
	TaskID uuid.UUID
	Dir    string
}

// fakeWorkingArea is an engine.WorkingArea whose FinalizeTask records its
// load-bearing arguments and returns refs/arbitron/tasks/<taskID>.
type fakeWorkingArea struct {
	mu          sync.Mutex
	calls       []finalizeCall
	finalizeErr error
	onFinalize  func(ctx context.Context) error
	log         *callLog
}

func (a *fakeWorkingArea) FinalizeTask(ctx context.Context, handle string, taskID uuid.UUID, dir string) (string, error) {
	a.mu.Lock()
	a.calls = append(a.calls, finalizeCall{Handle: handle, TaskID: taskID, Dir: dir})
	hook, ferr := a.onFinalize, a.finalizeErr
	a.mu.Unlock()
	a.log.add("FinalizeTask")
	if hook != nil {
		if err := hook(ctx); err != nil {
			return "", err
		}
	}
	if ferr != nil {
		return "", ferr
	}
	return "refs/arbitron/tasks/" + taskID.String(), nil
}

func (a *fakeWorkingArea) finalizeCalls() []finalizeCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]finalizeCall(nil), a.calls...)
}

func (a *fakeWorkingArea) SnapshotParent(context.Context, uuid.UUID, string) (string, error) {
	return "", errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ForkTask(context.Context, string, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ReleaseSnapshot(context.Context, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) BeginGather(context.Context, string, uuid.UUID, string) (engine.GatherHandle, error) {
	return engine.GatherHandle{}, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) MergeTask(context.Context, engine.GatherHandle, string) (engine.MergeOutcome, error) {
	return engine.MergeOutcome{}, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ConflictPaths(context.Context, engine.GatherHandle) ([]string, error) {
	return nil, errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) CommitResolution(context.Context, engine.GatherHandle, string) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) Apply(context.Context, engine.GatherHandle) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) Rollback(context.Context, engine.GatherHandle) error {
	return errUnusedInFinalizeFake
}
func (a *fakeWorkingArea) ReleaseRef(context.Context, string, string) error {
	return errUnusedInFinalizeFake
}

func TestNewGroupFileFinalizer_RequiresEveryDependency(t *testing.T) {
	log := &callLog{}
	groups, area, ws := newFinalizeGroupRepo(log), &fakeWorkingArea{log: log}, &fakeWorkspaceManager{}
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(nil, area, ws) })
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(groups, nil, ws) })
	assert.Panics(t, func() { engine.NewGroupFileFinalizer(groups, area, nil) })
	assert.NotPanics(t, func() { engine.NewGroupFileFinalizer(groups, area, ws) })
}

func TestGroupFileFinalizer_EligibilityAndInputProvenance(t *testing.T) {
	for _, tc := range []struct {
		state    instance.GroupRunState
		blocking bool
		eligible bool
	}{
		{instance.GroupRunLaunching, true, true},
		{instance.GroupRunRunning, true, true},
		{instance.GroupRunRunning, false, false},
		{instance.GroupRunDecided, true, false},
		{instance.GroupRunGathering, true, false},
		{instance.GroupRunResolvingConflicts, true, false},
		{instance.GroupRunDone, true, false},
	} {
		t.Run(fmt.Sprintf("%s/blocking=%t", tc.state, tc.blocking), func(t *testing.T) {
			log := &callLog{}
			runID, taskID := uuid.New(), uuid.New()
			run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles,
				State: tc.state, WorkspaceSnapshot: "snapshot-from-producer",
				Tasks: []instance.GroupTask{
					{Key: "decoy", ChildInstanceID: uuid.New(), Blocking: true},
					{Key: "actual", ChildInstanceID: taskID, Blocking: tc.blocking},
				}}
			other := &instance.GroupRun{ID: uuid.New(), Tasks: []instance.GroupTask{{Key: "actual"}}}
			groups := newFinalizeGroupRepo(log, other, run)
			area := &fakeWorkingArea{log: log}
			ws := &fakeWorkspaceManager{}
			fin := engine.NewGroupFileFinalizer(groups, area, ws)
			misleadingKey := "decoy"
			task := &instance.Instance{ID: taskID, GroupRunID: &runID, GroupTaskKey: &misleadingKey}
			require.NoError(t, fin.FinalizeTerminal(context.Background(), task, true))
			if tc.eligible {
				assert.Equal(t, []finalizeCall{{Handle: run.WorkspaceSnapshot, TaskID: taskID,
					Dir: "/tmp/arbitron-test-ws/" + taskID.String()}}, area.finalizeCalls())
				assert.Equal(t, "refs/arbitron/tasks/"+taskID.String(), groups.finalizedRef(runID, "actual"))
			} else {
				assert.Empty(t, area.finalizeCalls())
				assert.Empty(t, groups.finalizedRef(runID, "actual"))
				assert.Empty(t, ws.ensuredIDs())
			}
			assert.Empty(t, groups.finalizedRef(runID, "decoy"))
			assert.Empty(t, groups.finalizedRef(other.ID, "actual"))
		})
	}
}

func TestGroupFileFinalizer_WorkspaceErrorDoesNotPublishARef(t *testing.T) {
	log := &callLog{}
	runID, taskID := uuid.New(), uuid.New()
	run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles,
		State: instance.GroupRunRunning, WorkspaceSnapshot: "snapshot",
		Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: taskID, Blocking: true}}}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	fin := engine.NewGroupFileFinalizer(groups, area, &fakeWorkspaceManager{ensureErr: errors.New("read-only filesystem")})
	err := fin.FinalizeTerminal(context.Background(), &instance.Instance{ID: taskID, GroupRunID: &runID}, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization)
	assert.Contains(t, err.Error(), "read-only filesystem")
	assert.Empty(t, area.finalizeCalls())
	assert.Empty(t, groups.finalizedRef(runID, "a"))
}

// A missing workspace and an intentionally emptied workspace have different
// meanings: only an existing directory is valid input for file finalization.
func TestGroupFileFinalizer_RequiresExistingWorkspace(t *testing.T) {
	for _, state := range []string{"vanished", "empty", "replaced-by-file"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			log := &callLog{}
			runID, taskID := uuid.New(), uuid.New()
			run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles,
				State: instance.GroupRunRunning, WorkspaceSnapshot: runID.String(),
				Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: taskID, Blocking: true}}}
			groups := newFinalizeGroupRepo(log, run)
			area := &fakeWorkingArea{log: log}
			ws, err := filesystem.NewWorkspaceManager(t.TempDir())
			require.NoError(t, err)
			dir, err := ws.EnsureInstanceDir(taskID)
			require.NoError(t, err)
			if state != "empty" {
				require.NoError(t, os.Remove(dir))
			}
			if state == "replaced-by-file" {
				require.NoError(t, os.WriteFile(dir, []byte("replacement"), 0o600))
			}

			fin := engine.NewGroupFileFinalizer(groups, area, ws)
			err = fin.FinalizeTerminal(ctx, &instance.Instance{ID: taskID, GroupRunID: &runID}, true)
			if state == "empty" {
				require.NoError(t, err, "an empty directory can represent intentional deletions")
				assert.NotEmpty(t, groups.finalizedRef(runID, "a"))
			} else {
				require.ErrorIs(t, err, engine.ErrGroupFileFinalization)
				assert.Empty(t, area.finalizeCalls())
				assert.Empty(t, groups.finalizedRef(runID, "a"))
			}
			if state == "vanished" {
				assert.NoDirExists(t, dir, "reading must never recreate the directory")
			} else if state == "replaced-by-file" {
				got, err := os.ReadFile(dir)
				require.NoError(t, err)
				assert.Equal(t, "replacement", string(got))
			} else {
				matches, err := filepath.Glob(filepath.Join(dir, "*"))
				require.NoError(t, err)
				assert.Empty(t, matches)
			}
		})
	}
}

func TestGroupFileFinalizer_UnitCases(t *testing.T) {
	ctx := context.Background()
	log := &callLog{}
	runID, taskID := uuid.New(), uuid.New()
	run := &instance.GroupRun{ID: runID, GatherMode: workflow.GroupGatherResultsAndFiles, State: instance.GroupRunRunning,
		WorkspaceSnapshot: runID.String(), Tasks: []instance.GroupTask{{Key: "a", ChildInstanceID: taskID, Blocking: true}}}
	groups := newFinalizeGroupRepo(log, run)
	area := &fakeWorkingArea{log: log}
	fin := engine.NewGroupFileFinalizer(groups, area, &fakeWorkspaceManager{})
	key := "a"
	task := &instance.Instance{ID: taskID, GroupRunID: &runID, GroupTaskKey: &key}

	require.NoError(t, fin.FinalizeTerminal(ctx, task, false), "failed work is never finalized")
	require.NoError(t, fin.FinalizeTerminal(ctx, nil, true))
	require.NoError(t, fin.FinalizeTerminal(ctx, &instance.Instance{ID: uuid.New()}, true), "non-group instance")
	assert.Empty(t, area.finalizeCalls())

	stranger := &instance.Instance{ID: uuid.New(), GroupRunID: &runID, GroupTaskKey: &key}
	err := fin.FinalizeTerminal(ctx, stranger, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization, "an instance missing from the roster")
	assert.Contains(t, err.Error(), "not on the roster")

	groups.getErr = errors.New("db gone")
	require.ErrorIs(t, fin.FinalizeTerminal(ctx, task, true), engine.ErrGroupFileFinalization)
	assert.True(t, fin.RetainWorkspace(ctx, task), "an unreadable run is retained")
	groups.getErr = nil

	assert.True(t, fin.RetainWorkspace(ctx, task))
	assert.False(t, fin.RetainWorkspace(ctx, &instance.Instance{ID: uuid.New()}))
	assert.False(t, fin.RetainWorkspace(ctx, nil))
	groups.runs[runID].State = instance.GroupRunDone
	assert.True(t, fin.RetainWorkspace(ctx, task), "a done run's task workspace is the pruner's to remove")
	groups.runs[runID].State = instance.GroupRunGathering
	groups.runs[runID].GatherMode = workflow.GroupGatherResults
	assert.False(t, fin.RetainWorkspace(ctx, task))

	area.finalizeErr = errors.New("git exploded")
	groups.runs[runID].GatherMode = workflow.GroupGatherResultsAndFiles
	groups.runs[runID].State = instance.GroupRunLaunching
	err = fin.FinalizeTerminal(ctx, task, true)
	require.ErrorIs(t, err, engine.ErrGroupFileFinalization)
	assert.Contains(t, err.Error(), "git exploded")
}
