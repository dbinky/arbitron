package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGather_EmptyDirectoryDeltasAndPreciseExecutableBits(t *testing.T) {
	skipOnWindows(t)
	f := newGatherFixture(t, func(p string) { mkdirIn(t, p, "removed-empty/nested"); writeFile(t, p, "tool", "base", 0o751) })
	ref := f.task(func(d string) {
		require.NoError(t, os.RemoveAll(filepath.Join(d, "removed-empty")))
		mkdirIn(t, d, "new-empty")
		chmodIn(t, d, "new-empty", 0o710)
		writeFile(t, d, "tool", "task", 0o751)
		writeFile(t, d, "new-tool", "new executable", 0o751)
	})
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.NoDirExists(t, filepath.Join(f.parent, "removed-empty"))
	assert.Equal(t, map[string]uint32{"tool": 0o751, "new-tool": 0o751, "new-empty": 0o710}, permissionModes(t, f.parent, []string{"tool", "new-tool", "new-empty"}))
	require.NoError(t, f.a.Rollback(t.Context(), h))
	assert.DirExists(t, filepath.Join(f.parent, "removed-empty"))
}

func TestGather_DeletingRepositoryContentKeepsMetadataAndAncestorModes(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		repo := mkdirIn(t, p, "nested/repo")
		authorRepo(t, repo, map[string]string{"f": "base"})
		chmodIn(t, p, "nested", 0o750)
	})
	meta := metadataOf(t, filepath.Join(f.parent, "nested/repo/.git"))
	ref := f.task(func(d string) { require.NoError(t, os.RemoveAll(filepath.Join(d, "nested"))) })
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.NoFileExists(t, filepath.Join(f.parent, "nested/repo/f"))
	assert.Equal(t, meta, metadataOf(t, filepath.Join(f.parent, "nested/repo/.git")))
	assert.Equal(t, uint32(0o750), permissionModes(t, f.parent, []string{"nested"})["nested"])
	require.NoError(t, f.a.Rollback(t.Context(), h))
	assert.Equal(t, "file:0644:base", contentOf(t, f.parent)["nested/repo/f"])
}

func TestGather_WorkspaceRootNeverTouchesBackingStore(t *testing.T) {
	a, root := newTestArea(t)
	writeFile(t, root, "f", "base", 0o640)
	handle, err := a.SnapshotParent(t.Context(), uuid.New(), root)
	require.NoError(t, err)
	// The task is outside root to avoid treating a live task as parent content.
	task := t.TempDir()
	require.NoError(t, a.ForkTask(t.Context(), handle, task))
	writeFile(t, task, "f", "task", 0o600)
	ref, err := a.FinalizeTask(t.Context(), handle, uuid.New(), task)
	require.NoError(t, err)
	h, err := a.BeginGather(t.Context(), handle, uuid.New(), root)
	require.NoError(t, err)
	_, err = a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	require.NoError(t, a.Apply(t.Context(), h))
	raw, err := os.ReadFile(filepath.Join(root, "f"))
	require.NoError(t, err)
	assert.Equal(t, "task", string(raw))
	require.NoError(t, a.Rollback(t.Context(), h))
	_, err = a.ResolveRef(t.Context(), handle, ref)
	require.NoError(t, err)
	assert.Equal(t, uint32(0o640), permissionModes(t, root, []string{"f"})["f"])
}

func TestGather_RejectsReboundParentAndStagingBeforeWrites(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o640) })
	h := f.begin()
	other := t.TempDir()
	writeFile(t, other, "f", "innocent", 0o600)
	forged := h
	forged.ParentDir = other
	require.Error(t, f.a.Apply(t.Context(), forged))
	require.Error(t, f.a.Rollback(t.Context(), forged))
	_, err := f.a.BeginGather(t.Context(), f.handle, h.GatherID, other)
	require.Error(t, err)
	assert.Equal(t, "file:0644:innocent", contentOf(t, other)["f"])
	forged = h
	forged.StagingDir = other
	require.Error(t, f.a.Rollback(t.Context(), forged))
}

// Inject failures at real Git process boundaries, including HEAD publication.
// Retrying must finish the SAME merge, with the original intended permissions.
func TestGather_RestartAtMergePublicationBoundaries(t *testing.T) {
	skipOnWindows(t)
	for _, boundary := range []string{"merge", "write-tree", "update-ref", "merge-quit", "resolution-replay"} {
		t.Run(boundary, func(t *testing.T) {
			f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o640) })
			ref := f.task(func(d string) { writeFile(t, d, "f", "task", 0o600); chmodIn(t, d, ".", 0o750) })
			h := f.begin()
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			wrapper := filepath.Join(t.TempDir(), "git")
			gate := filepath.Join(t.TempDir(), "failed")
			match := "[ \"$arg\" = '" + boundary + "' ]"
			if boundary == "merge-quit" || boundary == "resolution-replay" {
				match = "[ \"$arg\" = --quit ]"
			}
			// For merge and HEAD publication, fail AFTER performing the command.
			action := "exit 2"
			if boundary == "merge" || boundary == "update-ref" {
				action = "'" + realGit + "' \"$@\"; exit 2"
			}
			script := "#!/bin/sh\nfor arg do\n if " + match + " && [ ! -e '" + gate + "' ]; then\n touch '" + gate + "'\n " + action + "\n fi\ndone\nexec '" + realGit + "' \"$@\"\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o755))
			f.a.git = NewRunner(RunnerConfig{GitPath: wrapper, StateDir: t.TempDir()})
			_, err = f.a.MergeTask(t.Context(), h, ref)
			require.Error(t, err)
			restarted, err := NewWorkingArea(f.root)
			require.NoError(t, err)
			publishedHead := author(t, h.StagingDir, "rev-parse", "HEAD")
			if boundary == "resolution-replay" {
				require.NoError(t, restarted.CommitResolution(t.Context(), h, "replay after publication"))
				assert.Equal(t, publishedHead, author(t, h.StagingDir, "rev-parse", "HEAD"), "published merge must not be committed twice")
			}
			out, err := restarted.MergeTask(t.Context(), h, ref)
			require.NoError(t, err)
			require.True(t, out.Clean)
			// Recovery can call CommitResolution rather than MergeTask on replay.
			require.NoError(t, restarted.CommitResolution(t.Context(), h, "replay"))
			head := author(t, h.StagingDir, "rev-parse", "HEAD")
			_, err = restarted.MergeTask(t.Context(), h, ref)
			require.NoError(t, err)
			assert.Equal(t, head, author(t, h.StagingDir, "rev-parse", "HEAD"))
			require.NoError(t, restarted.Apply(t.Context(), h))
			assert.Equal(t, "file:0644:task", contentOf(t, f.parent)["f"])
			assert.Equal(t, map[string]uint32{".": 0o750, "f": 0o600}, permissionModes(t, f.parent, []string{".", "f"}))
			require.NoError(t, restarted.Rollback(t.Context(), h))
			assert.Equal(t, uint32(0o640), permissionModes(t, f.parent, []string{"f"})["f"])
		})
	}
}
