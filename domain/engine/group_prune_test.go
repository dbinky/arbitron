package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInventory lists the snapshots it was given, minus the ones the area has
// released since — so HasSnapshot turns false exactly when ReleaseSnapshot ran.
type fakeInventory struct {
	mu      sync.Mutex
	area    *fakeGatherArea
	handles map[string]time.Time
	err     error
}

var _ engine.SnapshotInventory = (*fakeInventory)(nil)

func newFakeInventory(area *fakeGatherArea) *fakeInventory {
	return &fakeInventory{area: area, handles: map[string]time.Time{}}
}

func (i *fakeInventory) add(handle string, mod time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.handles[handle] = mod
}

func (i *fakeInventory) released(handle string) bool {
	i.area.mu.Lock()
	defer i.area.mu.Unlock()
	for _, h := range i.area.releasedSnapshots {
		if h == handle {
			return true
		}
	}
	return false
}

func (i *fakeInventory) ListSnapshots(context.Context) ([]engine.SnapshotInfo, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return nil, i.err
	}
	var out []engine.SnapshotInfo
	for h, mod := range i.handles {
		if !i.released(h) {
			out = append(out, engine.SnapshotInfo{Handle: h, ModTime: mod})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Handle < out[b].Handle })
	return out, nil
}

func (i *fakeInventory) HasSnapshot(_ context.Context, handle string) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.handles[handle]
	return ok && !i.released(handle), nil
}

// fakeChildLister answers ListChildren per parent id.
type fakeChildLister struct {
	mu       sync.Mutex
	children map[uuid.UUID][]instance.ChildRef
	err      error
}

var _ engine.ChildLister = (*fakeChildLister)(nil)

func (c *fakeChildLister) set(parent uuid.UUID, kids ...instance.ChildRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.children == nil {
		c.children = map[uuid.UUID][]instance.ChildRef{}
	}
	c.children[parent] = kids
}

func (c *fakeChildLister) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return append([]instance.ChildRef{}, c.children[parentID]...), nil
}

type pruneFixture struct {
	groups    *memGroupRuns
	instances *mockInstanceRepo
	children  *fakeChildLister
	ws        *dirWorkspaces
	area      *fakeGatherArea
	inventory *fakeInventory
	pruner    *engine.GroupResourcePruner
}

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	f := &pruneFixture{
		groups: newMemGroupRuns(), instances: newMockInstanceRepo(), children: &fakeChildLister{},
		ws: newDirWorkspaces(t), area: newFakeGatherArea(),
	}
	f.inventory = newFakeInventory(f.area)
	f.pruner = engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, f.area, f.inventory)
	return f
}

// seedRun persists a results_and_files run in runState whose gather report is
// in gatherState, with one launched task per child state; every child row
// exists, and every child has a real workspace holding "<key>.txt".
func (f *pruneFixture) seedRun(t *testing.T, runState instance.GroupRunState, gatherState string, states ...instance.State) *instance.GroupRun {
	t.Helper()
	ctx := context.Background()
	id, gatherID := uuid.New(), uuid.New()
	run := &instance.GroupRun{
		ID: id, ParentInstanceID: uuid.New(), NodeID: "g1", Attempt: 1,
		Form: workflow.GroupFormBranches, CompletionRule: workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResultsAndFiles,
		WorkspaceSnapshot: id.String(), State: runState, ActionsApplied: true,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: gatherState,
			Handle: map[string]any{
				"snapshotHandle": id.String(), "gatherId": gatherID.String(), "stagingDir": "/staging/" + gatherID.String(),
				"rollbackRef": "refs/arbitron/rollback/" + gatherID.String(), "parentDir": "/ws/parent",
			},
			Tasks: []instance.GatherTaskReport{}, Attempts: []instance.ResolutionAttempt{},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	for i, st := range states {
		child := uuid.New()
		key := string(rune('a' + i))
		run.Tasks = append(run.Tasks, instance.GroupTask{
			GroupRunID: id, Position: i, Key: key, Kind: instance.GroupTaskKindBranch, Origin: key + "1",
			ChildInstanceID: child, LaunchState: instance.GroupTaskLaunched, LiveStatus: instance.GroupTaskStatusFromState(st),
		})
		require.NoError(t, f.instances.Create(ctx, &instance.Instance{ID: child, State: st}))
		dir, err := f.ws.EnsureInstanceDir(child)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, key+".txt"), []byte(key), 0o644))
	}
	f.groups.put(run)
	f.inventory.add(run.WorkspaceSnapshot, time.Now())
	return f.groups.snapshot(id)
}

func (f *pruneFixture) setState(id uuid.UUID, st instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[id].State = st
}

func (f *pruneFixture) released() []string {
	f.area.mu.Lock()
	defer f.area.mu.Unlock()
	return append([]string(nil), f.area.releasedSnapshots...)
}

func (f *pruneFixture) releasedRefs() []string {
	f.area.mu.Lock()
	defer f.area.mu.Unlock()
	return append([]string(nil), f.area.releasedRefs...)
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func rollbackPin(run *instance.GroupRun) string {
	return run.WorkspaceSnapshot + "|" + run.Gather.Handle["rollbackRef"].(string)
}

func TestGroupResourcePruner_AppliedGatherReleasesSnapshotAndCompletedTaskWorkspaces(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted, instance.StateCompleted)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.Equal(t, []string{run.WorkspaceSnapshot}, f.released(), "the snapshot handle comes from the run")
	assert.Contains(t, f.releasedRefs(), rollbackPin(run), "the gather's rollback pin (and staging area) goes first")
	for _, task := range run.Tasks {
		assert.False(t, dirExists(f.ws.path(task.ChildInstanceID)), "SINK: completed task %s workspace removed", task.Key)
	}
}

func TestGroupResourcePruner_WaitsForAnIndependentTaskToStop(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted, instance.StateRunning)
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a task still writing pins the run's resources")
	assert.Empty(t, f.released())
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
	assert.Contains(t, f.releasedRefs(), rollbackPin(run), "the finished gather's own pin is released already")

	f.setState(run.Tasks[1].ChildInstanceID, instance.StateCompleted)
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.False(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

func TestGroupResourcePruner_WaitsForActiveDescendantsThroughTerminalIntermediates(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	task := run.Tasks[0].ChildInstanceID
	mid, leaf := uuid.New(), uuid.New()
	f.children.set(task, instance.ChildRef{ID: mid, State: instance.StateCompleted})
	f.children.set(mid, instance.ChildRef{ID: leaf, State: instance.StateRunning})
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a running grandchild below a completed child still pins the task workspace")
	assert.True(t, dirExists(f.ws.path(task)))

	f.children.set(mid, instance.ChildRef{ID: leaf, State: instance.StateCompleted})
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_WaitsForANestedGroupUnderATask(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	nested := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: run.Tasks[0].ChildInstanceID, NodeID: "inner", Attempt: 1,
		GatherMode: workflow.GroupGatherResultsAndFiles, State: instance.GroupRunGathering,
	}
	f.groups.put(nested)
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a dependent gather into the task's area pins it")

	f.groups.update(nested.ID, func(r *instance.GroupRun) { r.State = instance.GroupRunDone })
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_NeverReleasesARunThatIsNotDone(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunGathering, "merging", instance.StateCompleted)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
	assert.Empty(t, f.releasedRefs(), "gather and recovery still need every ref")
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

// Failed runs retain debugging content, consistent with
// Engine.warnWorkspaceRetained: nothing was applied, so the tasks' files stay.
func TestGroupResourcePruner_UnappliedGatherRetainsCompletedTaskWorkspaces(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "rolled_back", instance.StateCompleted, instance.StateFailed)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released, "the backing snapshot itself is still released")
	for _, task := range run.Tasks {
		got, rerr := os.ReadFile(filepath.Join(f.ws.path(task.ChildInstanceID), task.Key+".txt"))
		require.NoError(t, rerr, "task %s workspace is retained for debugging", task.Key)
		assert.Equal(t, task.Key, string(got))
	}
}

// addNeverStarted appends a task whose fork exists (forks precede the roster)
// but whose child was never created: a crash between StartGroupTask calls.
func (f *pruneFixture) addNeverStarted(t *testing.T, run *instance.GroupRun, key string) uuid.UUID {
	t.Helper()
	child := uuid.New()
	dir, err := f.ws.EnsureInstanceDir(child)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, key+".txt"), []byte(key), 0o644))
	run.Tasks = append(run.Tasks, instance.GroupTask{
		GroupRunID: run.ID, Position: len(run.Tasks), Key: key, Kind: instance.GroupTaskKindBranch, Origin: key + "1",
		ChildInstanceID: child, LaunchState: instance.GroupTaskPending, LiveStatus: instance.GroupTaskStatusNotStarted,
	})
	f.groups.put(run)
	return child
}

// paired:15 — after recovery settles an interrupted launch, a never-started
// task's fork has no child and no writer; it is released with the run, while
// the cancelled task's workspace is kept for inspection.
func TestGroupResourcePruner_InterruptedLaunchReleasesNeverStartedForks(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "rolled_back", instance.StateCancelled)
	never := f.addNeverStarted(t, run, "z")

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.False(t, dirExists(f.ws.path(never)), "SINK: the never-started fork is released")
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)), "the cancelled task is retained for debugging")
}

// A run that is not done may still be launching in-process: the next task's
// fork is about to be used, so nothing is released.
func TestGroupResourcePruner_NeverStartedForkOfAnUnsettledRunIsKept(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunLaunching, "", instance.StateRunning)
	never := f.addNeverStarted(t, run, "z")

	_, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, dirExists(f.ws.path(never)))
}

func TestGroupResourcePruner_ReleaseErrorIsReturnedAndRetried(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.area.failReleaseSnapshot = errors.New("device busy")
	ctx := context.Background()

	_, err := f.pruner.PruneRun(ctx, run.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device busy")

	f.area.failReleaseSnapshot = nil
	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_AlreadyReleasedIsANoOp(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "rolled_back", instance.StateCompleted)
	f.inventory.mu.Lock()
	delete(f.inventory.handles, run.WorkspaceSnapshot)
	f.inventory.mu.Unlock()

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.Empty(t, f.released(), "an absent snapshot is the durable 'already pruned' marker")
	assert.Empty(t, f.releasedRefs())
}

func TestGroupResourcePruner_ResultsModeAndUnknownRunsAreNotItsConcern(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.groups.update(run.ID, func(r *instance.GroupRun) { r.GatherMode = workflow.GroupGatherResults })
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released)
	released, err = f.pruner.PruneRun(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
}

func TestGroupResourcePruner_LookupErrorDefers(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.children.err = errors.New("database is locked")

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.Error(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

func TestGroupResourcePruner_SweepPrunesRunsAndReleasesOnlyOldOrphans(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	oldOrphan, freshOrphan := uuid.New().String(), uuid.New().String()
	f.inventory.add(oldOrphan, time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))
	f.inventory.add(freshOrphan, time.Now())
	f.inventory.add("not-a-run-id", time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))

	require.NoError(t, f.pruner.Sweep(context.Background()))
	got := f.released()
	assert.ElementsMatch(t, []string{run.WorkspaceSnapshot, oldOrphan}, got,
		"a fresh orphan may be a launch in progress; a non-id entry is never touched")
}

func TestGroupResourcePruner_SweepListingErrorIsReturned(t *testing.T) {
	f := newPruneFixture(t)
	f.inventory.err = errors.New("permission denied")
	assert.ErrorContains(t, f.pruner.Sweep(context.Background()), "permission denied")
}

func TestNewGroupResourcePruner_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newPruneFixture(t)
	assert.Panics(t, func() { engine.NewGroupResourcePruner(nil, f.instances, f.children, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, nil, f.children, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, nil, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, nil, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, nil, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, f.area, nil) })
}
