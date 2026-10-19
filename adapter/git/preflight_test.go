package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflightGather_NoRepoPathNeedsOnlyGitAndTheParentArea(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")

	require.NoError(t, a.PreflightGather(ctx, parent, ""))
	err := a.PreflightGather(ctx, filepath.Join(root, "instances", "missing"), "")
	require.Error(t, err, "a parent area that does not exist cannot be gathered into")
}

func TestPreflightGather_MissingGitIsReportedBeforeAnyTaskStarts(t *testing.T) {
	root := t.TempDir()
	a, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{SearchPath: t.TempDir(), StateDir: t.TempDir()})))
	require.NoError(t, err)
	parent := mkdirIn(t, root, "instances/p")

	err = a.PreflightGather(context.Background(), parent, "")
	require.ErrorIs(t, err, ErrGitUnavailable)
	assert.Contains(t, err.Error(), "results_and_files", "the message names the gather mode that needs git")
}

func TestPreflightGather_DeclaredRepositoryOrPlainDirectoryInsideTheParentIsAccepted(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	mkdirIn(t, parent, "src")
	authorRepo(t, filepath.Join(parent, "src"), map[string]string{"main.go": "package main\n"})
	mkdirIn(t, parent, "docs")

	require.NoError(t, a.PreflightGather(ctx, parent, "src"), "an author repository is the intended hint")
	require.NoError(t, a.PreflightGather(ctx, parent, "docs"), "a plain directory hint is not rejected")
	require.NoError(t, a.PreflightGather(ctx, parent, "."), "the whole area is a valid hint")
}

func TestPreflightGather_RejectsHintsThatCannotBeUsed(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "notes.txt", "x", 0o644)
	outside := mkdirIn(t, root, "outside")
	require.NoError(t, os.Symlink(outside, filepath.Join(parent, "escape")))

	for name, rel := range map[string]string{
		"missing":     "nope",
		"file":        "notes.txt",
		"symlink out": "escape",
		"absolute":    "/etc",
		"climb":       "../outside",
	} {
		t.Run(name, func(t *testing.T) {
			err := a.PreflightGather(ctx, parent, rel)
			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
		})
	}
}

func TestPreflightGather_LockedDeclaredRepositoryIsActionable(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	mkdirIn(t, parent, "src")
	authorRepo(t, filepath.Join(parent, "src"), map[string]string{"a.txt": "a"})
	writeFile(t, filepath.Join(parent, "src", ".git"), "index.lock", "", 0o644)

	err := a.PreflightGather(context.Background(), parent, "src")
	require.ErrorIs(t, err, ErrRepositoryLocked)
	assert.Contains(t, err.Error(), "index.lock", "the operator is told which file blocks the gather")
}

func TestListSnapshots_ListsOnlyBackingRepositories(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "f.txt", "x", 0o644)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	// Things that live under .group-backing but are not snapshots.
	mkdirIn(t, root, ".group-backing/not-a-uuid.git")
	mkdirIn(t, root, ".group-backing/"+uuid.New().String()+"-gather-"+uuid.New().String())
	writeFile(t, root, ".group-backing/"+uuid.New().String()+".git", "a file, not a repository", 0o644)

	infos, err := a.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, handle, infos[0].Handle)
	assert.WithinDuration(t, time.Now(), infos[0].ModTime, time.Minute)

	has, err := a.HasSnapshot(ctx, handle)
	require.NoError(t, err)
	assert.True(t, has)

	require.NoError(t, a.ReleaseSnapshot(ctx, handle))
	infos, err = a.ListSnapshots(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos, "a released snapshot is no longer listed")
	has, err = a.HasSnapshot(ctx, handle)
	require.NoError(t, err)
	assert.False(t, has)

	_, err = a.HasSnapshot(ctx, "../etc")
	assert.Error(t, err, "a handle is used in a path and must be a group-run id")
}
