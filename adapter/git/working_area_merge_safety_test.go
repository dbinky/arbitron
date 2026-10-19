package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGather_ResolvedSymlinkNeverReadsItsTargetForMarkers(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o644) })
	a := f.task(func(d string) { writeFile(t, d, "f", "a", 0o644) })
	b := f.task(func(d string) { writeFile(t, d, "f", "b", 0o644) })
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, a)
	require.NoError(t, err)
	out, err := f.a.MergeTask(t.Context(), h, b)
	require.NoError(t, err)
	require.False(t, out.Clean)
	outside := t.TempDir()
	writeFile(t, outside, "markers", "<<<<<<< outside\n>>>>>>> outside\n", 0o600)
	target := filepath.Join(outside, "markers")
	require.NoError(t, os.Remove(filepath.Join(h.StagingDir, "f")))
	require.NoError(t, os.Symlink(target, filepath.Join(h.StagingDir, "f")))
	author(t, h.StagingDir, "add", "f")
	paths, err := f.a.ConflictPaths(t.Context(), h)
	require.NoError(t, err)
	assert.Empty(t, paths)
	require.NoError(t, f.a.CommitResolution(t.Context(), h, "resolve as symlink"))
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.Equal(t, "link:"+target, contentOf(t, f.parent)["f"])
	assert.Equal(t, uint32(0o600), permissionModes(t, outside, []string{"markers"})["markers"])
}

func TestGather_FileDirectoryTransitionsDoNotFollowOldSymlink(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "old-dir/f", "base", 0o644)
		writeFile(t, p, "old-file", "base", 0o644)
	})
	ref := f.task(func(d string) {
		require.NoError(t, os.RemoveAll(filepath.Join(d, "old-dir")))
		writeFile(t, d, "old-dir", "now file", 0o600)
		require.NoError(t, os.Remove(filepath.Join(d, "old-file")))
		writeFile(t, d, "old-file/f", "now dir", 0o640)
	})
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	outside := t.TempDir()
	writeFile(t, outside, "f", "untouched", 0o600)
	require.NoError(t, os.RemoveAll(filepath.Join(f.parent, "old-dir")))
	require.NoError(t, os.Symlink(outside, filepath.Join(f.parent, "old-dir")))
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.Equal(t, "file:0644:now file", contentOf(t, f.parent)["old-dir"])
	assert.Equal(t, "file:0644:now dir", contentOf(t, f.parent)["old-file/f"])
	assert.Equal(t, "file:0644:untouched", contentOf(t, outside)["f"])
	require.NoError(t, f.a.Rollback(t.Context(), h))
	assert.Equal(t, "file:0644:base", contentOf(t, f.parent)["old-dir/f"])
}

// The filesystem adapter's symlink containment policy applies to the git
// working area in both untrusted directions: the parent's baseline (it would
// be forked into every task) and a task's output (it would be merged into the
// parent).
func TestWorkingArea_RefusesSymlinksThatEscapeTheArea(t *testing.T) {
	skipOnWindows(t)
	outside := t.TempDir()
	links := map[string]func(dir string) string{
		"absolute":        func(string) string { return outside },
		"relative escape": func(string) string { return "../../outside" },
		"dangling climb":  func(string) string { return "../missing/x" },
	}
	for name, target := range links {
		t.Run("baseline/"+name, func(t *testing.T) {
			a, root := newTestArea(t)
			parent := mkdirIn(t, root, "instances/parent")
			writeFile(t, parent, "f", "base", 0o644)
			require.NoError(t, os.Symlink(target(parent), filepath.Join(parent, "link")))
			_, err := a.SnapshotParent(t.Context(), uuid.New(), parent)
			require.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
			assert.Contains(t, err.Error(), `"link"`)
		})
		t.Run("task/"+name, func(t *testing.T) {
			f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o644) })
			dir := filepath.Join(f.root, "instances", "task-"+uuid.NewString())
			require.NoError(t, f.a.ForkTask(t.Context(), f.handle, dir))
			require.NoError(t, os.Symlink(target(dir), filepath.Join(dir, "notes")))
			_, err := f.a.FinalizeTask(t.Context(), f.handle, uuid.New(), dir)
			require.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
			assert.Contains(t, err.Error(), `"notes"`)
		})
	}
	t.Run("contained relative link is kept", func(t *testing.T) {
		f := newGatherFixture(t, func(p string) {
			writeFile(t, p, "f", "base", 0o644)
			require.NoError(t, os.Symlink("f", filepath.Join(p, "link")))
		})
		dir := filepath.Join(f.root, "instances", "task-ok")
		require.NoError(t, f.a.ForkTask(t.Context(), f.handle, dir))
		_, err := f.a.FinalizeTask(t.Context(), f.handle, uuid.New(), dir)
		require.NoError(t, err)
	})
}
