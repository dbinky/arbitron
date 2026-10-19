package git

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEAM (paired:11): a nested group's snapshot is taken of the enclosing
// task's fork (the launcher reads that task's reseeded `workspace`), and the
// nested gather must apply into that same fork. Pointed at the OUTER parent's
// directory instead, the inner gather would publish the task's changes into
// the root area directly, bypassing the outer group's eligibility (a losing
// first_success task, or a failed outer group, would still change the root).
// BeginGather binds the target to the directory the baseline was scanned from
// and refuses before taking a rollback snapshot or touching any file.
func TestGather_NestedGatherRefusesATargetOtherThanTheSnapshottedFork(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "root", 0o644) })
	ctx := t.Context()
	taskDir := mkdirIn(t, f.root, "instances/outer-task")
	require.NoError(t, f.a.ForkTask(ctx, f.handle, taskDir))
	innerHandle, err := f.a.SnapshotParent(ctx, uuid.New(), taskDir)
	require.NoError(t, err)
	innerTask := mkdirIn(t, f.root, "instances/inner-task")
	require.NoError(t, f.a.ForkTask(ctx, innerHandle, innerTask))
	writeFile(t, innerTask, "f", "inner", 0o644)
	_, err = f.a.FinalizeTask(ctx, innerHandle, uuid.New(), innerTask)
	require.NoError(t, err)

	gatherID := uuid.New()
	_, err = f.a.BeginGather(ctx, innerHandle, gatherID, f.parent)
	require.Error(t, err, "the inner gather may only target the fork its group snapshotted")
	assert.Contains(t, err.Error(), "differs from the group's baseline")
	assert.Equal(t, "file:0644:root", contentOf(t, f.parent)["f"], "the outer parent is untouched")
	backing, err := f.a.BackingDir(innerHandle)
	require.NoError(t, err)
	_, refErr := f.a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "-q", RollbackRefPrefix + gatherID.String()}, GitDir: backing})
	assert.Error(t, refErr, "no rollback snapshot is taken of the wrong directory")

	// The enclosing task's own fork is accepted (the production path).
	_, err = f.a.BeginGather(ctx, innerHandle, uuid.New(), taskDir)
	require.NoError(t, err)
}
