package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	gitarea "github.com/arbitron/arbitron/adapter/git"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Only the destructive boundary fails; inventory, persistence and disk sinks
// are real, so a wrong handle cannot accidentally satisfy these assertions.
type interruptedResultsRelease struct {
	*filesystem.GroupWorkspaceAdapter
	fail error
}

func (w *interruptedResultsRelease) ReleaseSnapshot(ctx context.Context, handle string) error {
	if w.fail != nil {
		return w.fail
	}
	return w.GroupWorkspaceAdapter.ReleaseSnapshot(ctx, handle)
}

func TestGroupResultsResources_SweepRecoversCrashAndRetriesRelease(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	groups, instances, run := terminalRun(t, db, "a", "b")
	root := t.TempDir()
	workspaces, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	snapshots, err := filesystem.NewGroupWorkspace(root)
	require.NoError(t, err)
	area, err := gitarea.NewWorkingArea(root)
	require.NoError(t, err)
	parent := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(parent, "seed.txt"), []byte("seed"), 0o600))
	handle, err := snapshots.SnapshotParent(ctx, run.ID, parent)
	require.NoError(t, err)
	// Crash during launch: every fork exists before CreateWithTasks; the roster
	// is still launching and tasks can still be queued, but no fork needs its
	// source snapshot after persistence.
	for _, task := range run.Tasks {
		dir, err := workspaces.EnsureInstanceDir(task.ChildInstanceID)
		require.NoError(t, err)
		require.NoError(t, snapshots.ForkTask(ctx, handle, dir))
	}
	_, err = db.Exec(`UPDATE group_runs SET workspace_snapshot=? WHERE id=?`, handle, run.ID.String())
	require.NoError(t, err)
	broken := &interruptedResultsRelease{GroupWorkspaceAdapter: snapshots, fail: errors.New("release unavailable")}
	pruner := engine.NewGroupResourcePruner(groups, instances, instances, workspaces, area, area)
	pruner.SetResultsResources(broken, groups)
	require.ErrorContains(t, pruner.Sweep(ctx), "release unavailable")
	require.DirExists(t, handle, "failed release remains discoverable")
	broken.fail = nil
	require.NoError(t, pruner.Sweep(ctx))
	require.NoDirExists(t, handle)
	for _, task := range run.Tasks {
		require.FileExists(t, filepath.Join(root, "instances", task.ChildInstanceID.String(), "seed.txt"), "queued tasks still need their workspaces")
	}
	require.NoError(t, pruner.Sweep(ctx), "recovery is idempotent")
}

func TestGroupResultsResources_SweepReleasesOnlyOldOrphanSnapshotsAndBuilds(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	groups, instances := NewGroupRunRepo(db), NewInstanceRepo(db)
	root := t.TempDir()
	workspaces, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	snapshots, err := filesystem.NewGroupWorkspace(root)
	require.NoError(t, err)
	area, err := gitarea.NewWorkingArea(root)
	require.NoError(t, err)
	parent := t.TempDir()
	old, err := snapshots.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	fresh, err := snapshots.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	tmp := filepath.Join(snapshots.SnapshotsDir(), ".tmp-"+uuid.NewString())
	require.NoError(t, os.Mkdir(tmp, 0o700))
	// A process killed mid-copy leaves this build directory, without any row.
	aged := time.Now().Add(-2 * engine.GroupOrphanSnapshotGrace)
	for _, path := range []string{old, tmp} {
		require.NoError(t, os.Chtimes(path, aged, aged))
	}
	pruner := engine.NewGroupResourcePruner(groups, instances, instances, workspaces, area, area)
	pruner.SetResultsResources(snapshots, groups)
	require.NoError(t, pruner.Sweep(ctx))
	require.NoDirExists(t, old)
	require.NoDirExists(t, tmp)
	require.DirExists(t, fresh, "an in-progress pre-persistence launch gets the orphan grace period")
}

// paired:15 — a crash between StartGroupTask calls leaves every fork on disk
// (forks precede CreateWithTasks) but only some children created. Once
// recovery has settled the run under the dead parent, a never-started task's
// fork has no instance row, no writer and nothing to inspect: the sweep must
// release it with the snapshot. A reserved child that does exist was
// cancelled by recovery and keeps its workspace for inspection.
func TestGroupResultsResources_SweepReleasesNeverStartedForksAfterAnInterruptedLaunch(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	groups, instances := NewGroupRunRepo(db), NewInstanceRepo(db)
	root := t.TempDir()
	workspaces, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	snapshots, err := filesystem.NewGroupWorkspace(root)
	require.NoError(t, err)
	area, err := gitarea.NewWorkingArea(root)
	require.NoError(t, err)

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	parent := &instance.Instance{ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot, ID: uuid.New(), OwnerID: owner, WorkflowName: "crash-parent", State: instance.StateRunning, Priority: instance.PriorityNormal}
	require.NoError(t, instances.Create(ctx, parent))
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "seed.txt"), []byte("seed"), 0o600))
	run := sampleGroupRun(parent.ID, "g", "a", "b")
	handle, err := snapshots.SnapshotParent(ctx, run.ID, src)
	require.NoError(t, err)
	run.WorkspaceSnapshot = handle
	for _, task := range run.Tasks {
		dir, err := workspaces.EnsureInstanceDir(task.ChildInstanceID)
		require.NoError(t, err)
		require.NoError(t, snapshots.ForkTask(ctx, handle, dir))
	}
	require.NoError(t, groups.CreateWithTasks(ctx, run))
	// Task a started and was recorded; the process died before task b started.
	node, key, rid, pid := "g", "a", run.ID, parent.ID
	require.NoError(t, instances.Create(ctx, &instance.Instance{
		ModelPins:        map[string]string{},
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               run.Tasks[0].ChildInstanceID, OwnerID: owner, WorkflowName: "crash-parent", State: instance.StateQueued,
		Priority: instance.PriorityNormal, ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &rid, GroupTaskKey: &key, GroupTaskKind: instance.GroupTaskKindBranch,
	}))
	require.NoError(t, groups.MarkTaskLaunched(ctx, run.ID, "a"))

	// Recovery: the crashed parent is failed, then the coordinator's
	// settleUnderDeadParent decides on the partial roster, cancels task a and
	// finalizes the run (the same repository writes, in the same order).
	require.NoError(t, instances.SetFailed(ctx, parent.ID, "g"))
	current, err := groups.Get(ctx, run.ID)
	require.NoError(t, err)
	decision := engine.DecideLaunchFailure(current, "parent run failed before the group decided", time.Now())
	won, err := groups.Decide(ctx, run.ID, *decision)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, instances.UpdateState(ctx, run.Tasks[0].ChildInstanceID, instance.StateCancelled))
	require.NoError(t, groups.MarkActionsApplied(ctx, run.ID))
	decided, err := groups.Get(ctx, run.ID)
	require.NoError(t, err)
	_, err = groups.SetFinalOutput(ctx, run.ID, "failure", engine.BuildGroupOutput(decided), instance.GroupRunDecided)
	require.NoError(t, err)
	require.NoError(t, groups.MarkContinuationDelivered(ctx, run.ID))

	pruner := engine.NewGroupResourcePruner(groups, instances, instances, workspaces, area, area)
	pruner.SetResultsResources(snapshots, groups)
	require.NoError(t, pruner.Sweep(ctx))

	require.NoDirExists(t, handle, "the snapshot is released")
	require.NoDirExists(t, filepath.Join(root, "instances", run.Tasks[1].ChildInstanceID.String()),
		"SINK: the never-started task's fork is released, not leaked forever")
	require.FileExists(t, filepath.Join(root, "instances", run.Tasks[0].ChildInstanceID.String(), "seed.txt"),
		"the cancelled task's workspace is retained for inspection")
	require.NoError(t, pruner.Sweep(ctx), "recovery is idempotent")
}
