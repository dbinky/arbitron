package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseSnapshot_RemovesReadOnlyMetadataAndGatherAreas(t *testing.T) {
	skipOnWindows(t)
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	authorRepo(t, parent, map[string]string{"f": "base"})
	handle, err := a.SnapshotParent(t.Context(), uuid.New(), parent)
	require.NoError(t, err)
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	readonly := filepath.Join(backing, "arbitron/metadata-tree/.git/objects")
	require.NoError(t, os.Chmod(readonly, 0o500))
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o700) })
	staging := mkdirIn(t, a.backing, handle+"-gather-"+uuid.NewString())
	writeFile(t, staging, "readonly/file", "staged", 0o400)
	dir := filepath.Join(staging, "readonly")
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	other := mkdirIn(t, a.backing, uuid.NewString()+"-gather-"+uuid.NewString())
	require.NoError(t, a.ReleaseSnapshot(t.Context(), handle))
	assert.NoDirExists(t, backing)
	assert.NoDirExists(t, staging)
	assert.DirExists(t, other)
	assert.Equal(t, "base", author(t, parent, "show", "HEAD:f"))
}

func TestSnapshot_ReplacementAndFinalizedRefSurviveLiveAreaRemoval(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "f", "first", 0o600)
	id := uuid.New()
	handle, err := a.SnapshotParent(t.Context(), id, parent)
	require.NoError(t, err)
	writeFile(t, parent, "f", "replacement", 0o600)
	again, err := a.SnapshotParent(t.Context(), id, parent)
	require.NoError(t, err)
	assert.Equal(t, handle, again)
	task := filepath.Join(root, "instances/t")
	require.NoError(t, a.ForkTask(t.Context(), handle, task))
	raw, err := os.ReadFile(filepath.Join(task, "f"))
	require.NoError(t, err)
	assert.Equal(t, "replacement", string(raw))
	writeFile(t, task, "f", "finalized", 0o600)
	ref, err := a.FinalizeTask(t.Context(), handle, uuid.New(), task)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(task))
	require.NoError(t, os.RemoveAll(parent))
	assert.Equal(t, "finalized", blobAt(t, a, handle, ref, "f"))
}

func TestSnapshotFork_RepositoryWithNoOrdinaryFiles(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	repo := mkdirIn(t, parent, "empty-repo")
	author(t, repo, "init", "-q")
	author(t, repo, "commit", "--allow-empty", "-qm", "empty")
	handle, err := a.SnapshotParent(t.Context(), uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances/t")
	require.NoError(t, a.ForkTask(t.Context(), handle, task))
	assert.Equal(t, author(t, repo, "rev-parse", "HEAD"), author(t, filepath.Join(task, "empty-repo"), "rev-parse", "HEAD"))
}
