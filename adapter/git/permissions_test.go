package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionModes(t *testing.T, root string, paths []string) map[string]uint32 {
	t.Helper()
	out := map[string]uint32{}
	for _, rel := range paths {
		info, err := os.Lstat(filepath.Join(root, rel))
		require.NoError(t, err)
		out[rel] = uint32(info.Mode().Perm())
	}
	return out
}

func TestSnapshotFork_PreservesAllPermissionsInTwoIndependentForks(t *testing.T) {
	skipOnWindows(t)
	for _, layout := range []string{"plain", "nested-repository", "linked-worktree"} {
		t.Run(layout, func(t *testing.T) {
			a, root := newTestArea(t)
			parent := mkdirIn(t, root, "instances/p")
			prefix := ""
			if layout != "plain" {
				src := mkdirIn(t, parent, "src")
				authorRepo(t, src, map[string]string{"base": "base"})
				prefix = "src/"
				if layout == "linked-worktree" {
					author(t, src, "worktree", "add", "-q", "-b", "topic", filepath.Join(parent, "work"))
					prefix = "work/"
				}
			}
			writeFile(t, parent, prefix+"secret", "private", 0o600)
			writeFile(t, parent, prefix+"run", "#!/bin/sh\n", 0o751)
			writeFile(t, parent, prefix+"readonly/file", "read only", 0o640)
			require.NoError(t, os.Chmod(filepath.Join(parent, prefix+"readonly"), 0o550))
			require.NoError(t, os.Chmod(parent, 0o750))
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(parent, prefix+"readonly"), 0o750) })
			paths := []string{".", prefix + "secret", prefix + "run", prefix + "readonly", prefix + "readonly/file"}
			want := permissionModes(t, parent, paths)
			handle, err := a.SnapshotParent(t.Context(), uuid.New(), parent)
			require.NoError(t, err)
			tasks := []string{filepath.Join(root, "instances/a"), filepath.Join(root, "instances/b")}
			for _, task := range tasks {
				require.NoError(t, a.ForkTask(t.Context(), handle, task))
				task := task
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(task, prefix+"readonly"), 0o750) })
				assert.Equal(t, want, permissionModes(t, task, paths))
			}
			require.NoError(t, os.Chmod(filepath.Join(tasks[0], prefix+"secret"), 0o644))
			require.NoError(t, os.Chmod(filepath.Join(tasks[0], prefix+"readonly"), 0o750))
			assert.Equal(t, want, permissionModes(t, parent, paths))
			assert.Equal(t, want, permissionModes(t, tasks[1], paths))
			require.NoError(t, os.Chmod(tasks[0], 0o710))
			ref, err := a.FinalizeTask(t.Context(), handle, uuid.New(), tasks[0])
			require.NoError(t, err)
			backing, commit, err := a.baseline(t.Context(), handle)
			require.NoError(t, err)
			baseline, err := readManifest(backing, commit)
			require.NoError(t, err)
			for rel, mode := range want {
				assert.Equal(t, mode, baseline.Permissions[rel], rel)
			}
			commit, err = a.ResolveRef(t.Context(), handle, ref)
			require.NoError(t, err)
			finalized, err := readManifest(backing, commit)
			require.NoError(t, err)
			assert.Equal(t, uint32(0o644), finalized.Permissions[prefix+"secret"])
			assert.Equal(t, uint32(0o710), finalized.Permissions["."])
		})
	}
}

func TestForkTask_RejectsMissingMalformedAndIncompletePermissions(t *testing.T) {
	for _, raw := range []string{"", "{", `{"emptyDirs":[]}`, `{"permissions":{".":493}}`, `{"permissions":{".":493,"README.md":9999}}`} {
		t.Run(raw, func(t *testing.T) {
			a, root := newTestArea(t)
			handle, err := a.SnapshotParent(t.Context(), uuid.New(), plainParent(t, root))
			require.NoError(t, err)
			backing, commit, err := a.baseline(t.Context(), handle)
			require.NoError(t, err)
			p := manifestPath(backing, commit)
			if raw == "" {
				require.NoError(t, os.Remove(p))
			} else {
				require.NoError(t, os.WriteFile(p, []byte(raw), 0o600))
			}
			task := filepath.Join(root, "instances/t")
			require.Error(t, a.ForkTask(t.Context(), handle, task))
			entries, err := os.ReadDir(task)
			if !os.IsNotExist(err) {
				require.NoError(t, err)
				assert.Empty(t, entries, "failed fork leaves no partial content")
			}
		})
	}
}

func TestRestoreAreaPermissions_RejectsUnsafePathsAndChmodErrors(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "file", "outside", 0o600)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	writeFile(t, root, "regular", "file", 0o644)
	for _, rel := range []string{"../escape", outside, "link", "link/file", "missing", "regular/child", ".git/config"} {
		err := restoreAreaPermissions(t.Context(), root, map[string]uint32{".": 0o750, rel: 0o777})
		require.Error(t, err, rel)
	}
	assert.Equal(t, uint32(0o600), permissionModes(t, outside, []string{"file"})["file"])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, restoreAreaPermissions(ctx, root, map[string]uint32{".": 0o750}), context.Canceled)
}

func TestSnapshot_CaptureErrorsLeaveNoBacking(t *testing.T) {
	skipOnWindows(t)
	a, root := newTestArea(t)
	// WalkDir must surface an inaccessible directory, not silently drop it.
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "locked/secret", "private", 0o600)
	locked := filepath.Join(parent, "locked")
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	id := uuid.New()
	_, err := a.SnapshotParent(t.Context(), id, parent)
	require.Error(t, err)
	backing, err := a.BackingDir(id.String())
	require.NoError(t, err)
	assert.NoDirExists(t, backing)
}

func TestForkTask_InterruptedCheckoutCleansPartialContent(t *testing.T) {
	skipOnWindows(t)
	a, root := newTestArea(t)
	handle, err := a.SnapshotParent(t.Context(), uuid.New(), plainParent(t, root))
	require.NoError(t, err)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	gate := filepath.Join(t.TempDir(), "checkout-started")
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = checkout-index ]; then\n  printf partial > partial\n  touch '" + gate + "'\n  sleep 30\n  exit 1\n fi\ndone\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o755))
	a.git = NewRunner(RunnerConfig{GitPath: wrapper, StateDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := filepath.Join(root, "instances/t")
	result := make(chan error, 1)
	go func() { result <- a.ForkTask(ctx, handle, task) }()
	require.Eventually(t, func() bool { _, err := os.Stat(gate); return err == nil }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("fork did not stop")
	}
	entries, err := os.ReadDir(task)
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

func TestSnapshot_MetadataAndStagedStateAreImmutableAcrossForks(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	external := t.TempDir()
	authorRepo(t, external, map[string]string{"f": "base"})
	wt := filepath.Join(parent, "work")
	author(t, external, "worktree", "add", "-q", "-b", "topic", wt)
	writeFile(t, wt, "f", "staged", 0o644)
	author(t, wt, "add", "f")
	writeFile(t, wt, "f", "unstaged", 0o644)
	wantStatus := author(t, wt, "status", "--porcelain=v1")
	wantIndex := author(t, wt, "show", ":f")
	handle, err := a.SnapshotParent(t.Context(), uuid.New(), parent)
	require.NoError(t, err)
	author(t, wt, "add", "f")
	author(t, wt, "commit", "-qm", "parent moved")
	for _, name := range []string{"a", "b"} {
		task := filepath.Join(root, "instances", name)
		require.NoError(t, a.ForkTask(t.Context(), handle, task))
		tw := filepath.Join(task, "work")
		assert.Equal(t, wantStatus, author(t, tw, "status", "--porcelain=v1"))
		assert.Equal(t, wantIndex, author(t, tw, "show", ":f"))
		author(t, tw, "add", "f")
		author(t, tw, "commit", "-qm", "task moved")
	}
}

func TestSnapshot_WorkspaceRootMetadataDoesNotCaptureOtherBackingStores(t *testing.T) {
	a, root := newTestArea(t)
	authorRepo(t, root, map[string]string{"f": "base"})
	for i := 0; i < 2; i++ {
		handle, err := a.SnapshotParent(t.Context(), uuid.New(), root)
		require.NoError(t, err)
		backing, commit, err := a.baseline(t.Context(), handle)
		require.NoError(t, err)
		m, err := readManifest(backing, commit)
		require.NoError(t, err)
		require.Len(t, m.Metadata, 1)
		raw, err := json.Marshal(m.Permissions)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), ".group-backing")
		require.NoError(t, filepath.WalkDir(filepath.Join(backing, "arbitron/metadata-tree"), func(p string, d os.DirEntry, err error) error {
			require.NoError(t, err)
			rel, err := filepath.Rel(filepath.Join(backing, "arbitron/metadata-tree"), p)
			require.NoError(t, err)
			assert.False(t, strings.Contains(rel, ".group-backing"), rel)
			return nil
		}))
	}
}
