package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEAM (paired:5): ForkTask and FinalizeTask share ONE baseline, the
// snapshot handle's BaselineRef. A fork that does not reproduce the baseline
// exactly, or a finalize that parents on anything else, would publish a delta
// carrying the whole starting area (or missing the task's edit), and the
// gather would apply it without error. Starting from a dirty, staged,
// untracked and ignored parent, a task that edits ONE file must publish a
// delta of exactly that file, with no mode or empty-directory drift, and the
// gather must change only that file in the parent.
func TestSnapshotForkFinalize_DeltaAgainstBaselineIsExactlyTheTaskEdit(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/parent")
	authorRepo(t, parent, map[string]string{"tracked.txt": "t1\n", "staged.txt": "s1\n", "edit.txt": "e1\n", ".gitignore": "*.log\n"})
	writeFile(t, parent, "tracked.txt", "t1 dirty\n", 0o644)
	writeFile(t, parent, "staged.txt", "s1 staged\n", 0o644)
	author(t, parent, "add", "staged.txt")
	writeFile(t, parent, "untracked/new.txt", "u\n", 0o600)
	writeFile(t, parent, "ignored.log", "i\n", 0o644)
	writeFile(t, parent, "bin/run.sh", "#!/bin/sh\n", 0o755)
	writeFile(t, parent, "readonly.txt", "ro\n", 0o444)
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "empty", "leaf"), 0o750))
	require.NoError(t, os.Symlink("tracked.txt", filepath.Join(parent, "link")))
	before := contentOf(t, parent)

	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	baseline := gitOut(t, a, handle, "rev-parse", BaselineRef)

	// Production prepares the task directory (EnsureInstanceDir) before forking.
	task := mkdirIn(t, root, "instances/task")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	require.Equal(t, before, contentOf(t, task), "the fork reproduces the baseline area")
	writeFile(t, task, "edit.txt", "e1\ntask edit\n", 0o644)

	taskID := uuid.New()
	ref, err := a.FinalizeTask(ctx, handle, taskID, task)
	require.NoError(t, err)

	assert.Equal(t, baseline, gitOut(t, a, handle, "rev-parse", ref+"^"), "the task commit parents on the shared baseline")
	assert.Equal(t, baseline, gitOut(t, a, handle, "rev-parse", BaselineRef), "finalize never moves the baseline")
	assert.Equal(t, "M\tedit.txt", gitOut(t, a, handle, "diff", "--name-status", "--no-renames", BaselineRef, ref),
		"the content delta is exactly the task's edit")

	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	baseManifest, err := readManifest(backing, baseline)
	require.NoError(t, err)
	taskManifest, err := readManifest(backing, gitOut(t, a, handle, "rev-parse", ref))
	require.NoError(t, err)
	assert.Equal(t, baseManifest.EmptyDirs, taskManifest.EmptyDirs, "no empty-directory drift between fork and baseline")
	assert.Equal(t, baseManifest.Permissions, taskManifest.Permissions, "no permission drift between fork and baseline")

	h, err := a.BeginGather(ctx, handle, uuid.New(), parent)
	require.NoError(t, err)
	_, err = a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, a.Apply(ctx, h))

	want := map[string]string{}
	for k, v := range before {
		want[k] = v
	}
	want["edit.txt"] = "file:0644:e1\ntask edit\n"
	assert.Equal(t, want, contentOf(t, parent), "the gather changes only the task's edit in the parent")
	assert.Equal(t, "s1 staged", author(t, parent, "show", ":staged.txt"), "the parent's index is untouched")
}
