//go:build !windows

// POSIX-only: these tests pin permission bits, symlinks and named pipes,
// none of which Windows represents the same way. The adapter itself is
// portable Go.

package filesystem_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time check: the adapter satisfies the engine port the group
// launcher (P2-T8) consumes.
var _ engine.GroupWorkspace = (*filesystem.GroupWorkspaceAdapter)(nil)

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.NoError(t, os.Chmod(path, perm))
}

// entry is one node of a tree listing: its type+permission bits and its
// content (file) or target (symlink).
type entry struct {
	mode fs.FileMode
	data string
}

// listTree returns every entry below root keyed by slash-separated relative
// path. The root itself is excluded: a fork root keeps the task dir's mode.
func listTree(t *testing.T, root string) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		require.NoError(t, err)
		e := entry{mode: info.Mode().Type() | info.Mode().Perm()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			require.NoError(t, err)
			e.data = target
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			e.data = string(b)
		}
		out[filepath.ToSlash(rel)] = e
		return nil
	}))
	return out
}

// writableTempRoot returns a t.TempDir whose cleanup first restores owner
// write permission on every directory: snapshots and forks faithfully keep
// read-only directories, which os.RemoveAll (used by t.TempDir) cannot
// descend into.
func writableTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	return root
}

// buildParent creates a realistic parent working area under
// <root>/instances/<id> (the WorkspaceManagerAdapter layout) and returns
// (root, parentDir).
func buildParent(t *testing.T) (string, string) {
	t.Helper()
	root := writableTempRoot(t)
	wm, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	parent, err := wm.EnsureInstanceDir(uuid.New())
	require.NoError(t, err)

	writeFile(t, filepath.Join(parent, "README.md"), "hello", 0o644)
	writeFile(t, filepath.Join(parent, "bin", "run.sh"), "#!/bin/sh\necho hi\n", 0o755)
	writeFile(t, filepath.Join(parent, "secret", "key"), "k", 0o600)
	writeFile(t, filepath.Join(parent, "nested", "deep", "x.txt"), "x", 0o640)
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "empty"), 0o750))
	require.NoError(t, os.Chmod(filepath.Join(parent, "empty"), 0o750))
	writeFile(t, filepath.Join(parent, "ro", "frozen.txt"), "frozen", 0o444)
	require.NoError(t, os.Chmod(filepath.Join(parent, "ro"), 0o555))
	// Minimal empty repository metadata; real Git layouts are covered in
	// group_git_metadata_test.go without changing these byte/mode fixtures.
	writeFile(t, filepath.Join(parent, "repo", ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "repo", ".git", "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "repo", ".git", "refs"), 0o755))
	writeFile(t, filepath.Join(parent, "repo", "main.go"), "package main\n", 0o644)
	// Relative symlinks that stay inside the area.
	require.NoError(t, os.Symlink("README.md", filepath.Join(parent, "link-to-readme")))
	require.NoError(t, os.Symlink("../README.md", filepath.Join(parent, "nested", "up-link")))
	require.NoError(t, os.Symlink("nested/deep", filepath.Join(parent, "dir-link")))
	return root, parent
}

func newGW(t *testing.T, root string) *filesystem.GroupWorkspaceAdapter {
	t.Helper()
	gw, err := filesystem.NewGroupWorkspace(root)
	require.NoError(t, err)
	return gw
}

func snapshotEntries(t *testing.T, gw *filesystem.GroupWorkspaceAdapter) []string {
	t.Helper()
	entries, err := os.ReadDir(gw.SnapshotsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// ---------- construction ----------

func TestNewGroupWorkspace(t *testing.T) {
	_, err := filesystem.NewGroupWorkspace("")
	require.Error(t, err, "an empty root is a wiring bug")

	root := t.TempDir()
	gw := newGW(t, root)
	assert.Equal(t, filepath.Join(root, ".group-snapshots"), gw.SnapshotsDir())
	_, err = os.Stat(gw.SnapshotsDir())
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing is created until the first snapshot")

	wd, err := os.Getwd()
	require.NoError(t, err)
	rel := newGW(t, "relative-root")
	assert.Equal(t, filepath.Join(wd, "relative-root", ".group-snapshots"), rel.SnapshotsDir(), "the root is made absolute")
}

// ---------- happy path ----------

func TestGroupWorkspace_SnapshotAndForkPreserveLayoutAndModes(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	runID := uuid.New()

	handle, err := gw.SnapshotParent(ctx, runID, parent)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".group-snapshots", runID.String()), handle)

	want := listTree(t, parent)
	assert.Equal(t, want, listTree(t, handle), "the snapshot mirrors layout, content, modes and symlinks")

	for _, dir := range []string{gw.SnapshotsDir(), handle} {
		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "%s must be owner-only", dir)
	}

	wm, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	taskDir, err := wm.EnsureInstanceDir(uuid.New())
	require.NoError(t, err)
	require.NoError(t, gw.ForkTask(ctx, handle, taskDir))

	got := listTree(t, taskDir)
	assert.Equal(t, want, got, "a fork reproduces the parent's starting files exactly")
	assert.Equal(t, os.FileMode(0o755), got["bin/run.sh"].mode.Perm(), "executable bits survive")
	assert.Equal(t, os.FileMode(0o555)|fs.ModeDir, got["ro"].mode, "read-only directories keep their mode")
	assert.Equal(t, os.FileMode(0o750)|fs.ModeDir, got["empty"].mode, "empty directories are copied")
	assert.Equal(t, "ref: refs/heads/main\n", got["repo/.git/HEAD"].data, "independent Git metadata is reproduced")

	info, err := os.Stat(taskDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "the fork root keeps the instance dir's 0o700")

	// Relative links resolve inside the FORK, not back into the parent.
	b, err := os.ReadFile(filepath.Join(taskDir, "dir-link", "x.txt"))
	require.NoError(t, err)
	assert.Equal(t, "x", string(b))
	resolved, err := filepath.EvalSymlinks(filepath.Join(taskDir, "link-to-readme"))
	require.NoError(t, err)
	realTask, err := filepath.EvalSymlinks(taskDir)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(resolved, realTask+string(os.PathSeparator)), "symlink resolves to %s, outside the fork", resolved)
}

func TestGroupWorkspace_ForkIntoMissingDirCreatesIt(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	taskDir := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, gw.ForkTask(ctx, handle, taskDir))
	info, err := os.Stat(taskDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	assert.Equal(t, listTree(t, parent), listTree(t, taskDir))
}

func TestGroupWorkspace_EmptyParentGivesEmptyFork(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	gw := newGW(t, root)
	ctx := context.Background()

	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	taskDir := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, gw.ForkTask(ctx, handle, taskDir))
	assert.Empty(t, listTree(t, taskDir))
}

// ---------- isolation ----------

func TestGroupWorkspace_ForksAreIndependent(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	before := listTree(t, parent)

	forkA := filepath.Join(root, "instances", "task-a")
	forkB := filepath.Join(root, "instances", "task-b")
	require.NoError(t, gw.ForkTask(ctx, handle, forkA))
	require.NoError(t, gw.ForkTask(ctx, handle, forkB))

	// Task A rewrites, adds and deletes — including inside its .git directory.
	require.NoError(t, os.WriteFile(filepath.Join(forkA, "README.md"), []byte("task A was here"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(forkA, "nested", "new.txt"), []byte("new"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(forkA, "secret", "key")))
	require.NoError(t, os.WriteFile(filepath.Join(forkA, "repo", ".git", "HEAD"), []byte("ref: refs/heads/task-a\n"), 0o644))

	assert.Equal(t, before, listTree(t, forkB), "fork B never sees fork A's writes")
	assert.Equal(t, before, listTree(t, parent), "the parent never sees a task's writes")
	assert.Equal(t, before, listTree(t, handle), "the snapshot never sees a task's writes")

	// No file is shared by inode between parent, snapshot and forks.
	for _, rel := range []string{"README.md", "bin/run.sh", "repo/main.go"} {
		pi, err := os.Stat(filepath.Join(parent, rel))
		require.NoError(t, err)
		si, err := os.Stat(filepath.Join(handle, rel))
		require.NoError(t, err)
		bi, err := os.Stat(filepath.Join(forkB, rel))
		require.NoError(t, err)
		assert.False(t, os.SameFile(pi, si), "%s: snapshot must not hard-link the parent", rel)
		assert.False(t, os.SameFile(si, bi), "%s: fork must not hard-link the snapshot", rel)
		assert.False(t, os.SameFile(pi, bi), "%s: fork must not hard-link the parent", rel)
	}
}

func TestGroupWorkspace_ParentChangesAfterSnapshotAreInvisibleToLaterForks(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	atSnapshot := listTree(t, parent)

	require.NoError(t, os.WriteFile(filepath.Join(parent, "README.md"), []byte("parent moved on"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "later.txt"), []byte("later"), 0o644))

	late := filepath.Join(root, "instances", "late-task")
	require.NoError(t, gw.ForkTask(ctx, handle, late))
	assert.Equal(t, atSnapshot, listTree(t, late), "every task starts from the SAME group-start boundary")
}

// A hard-linked pair in the parent becomes two independent files.
func TestGroupWorkspace_HardLinksAreCopiedByContent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	writeFile(t, filepath.Join(parent, "one"), "same", 0o644)
	require.NoError(t, os.Link(filepath.Join(parent, "one"), filepath.Join(parent, "two")))
	gw := newGW(t, root)
	ctx := context.Background()

	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	fork := filepath.Join(root, "instances", "task")
	require.NoError(t, gw.ForkTask(ctx, handle, fork))

	require.NoError(t, os.WriteFile(filepath.Join(fork, "one"), []byte("changed"), 0o644))
	b, err := os.ReadFile(filepath.Join(fork, "two"))
	require.NoError(t, err)
	assert.Equal(t, "same", string(b))
	b, err = os.ReadFile(filepath.Join(parent, "two"))
	require.NoError(t, err)
	assert.Equal(t, "same", string(b))
}

// The snapshot store must survive the WorkspaceManager removing the parent's
// (or any task's) instance directory: a nested or late fork still needs it.
func TestGroupWorkspace_SnapshotSurvivesInstanceDirRemoval(t *testing.T) {
	root := t.TempDir()
	wm, err := filesystem.NewWorkspaceManager(root)
	require.NoError(t, err)
	parentID := uuid.New()
	parent, err := wm.EnsureInstanceDir(parentID)
	require.NoError(t, err)
	writeFile(t, filepath.Join(parent, "a.txt"), "a", 0o644)
	gw := newGW(t, root)
	ctx := context.Background()

	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	require.NoError(t, wm.RemoveInstanceDir(parentID))

	taskID := uuid.New()
	taskDir, err := wm.EnsureInstanceDir(taskID)
	require.NoError(t, err)
	require.NoError(t, gw.ForkTask(ctx, handle, taskDir))
	b, err := os.ReadFile(filepath.Join(taskDir, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a", string(b))
}

// ---------- unsupported content ----------

func assertUnsupported(t *testing.T, gw *filesystem.GroupWorkspaceAdapter, parent, wantPath string, wantText ...string) {
	t.Helper()
	runID := uuid.New()
	handle, err := gw.SnapshotParent(context.Background(), runID, parent)
	require.Error(t, err)
	assert.Empty(t, handle)
	assert.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
	assert.ErrorIs(t, err, instance.ErrUnsupportedSnapshotContent, "the engine and instance sentinels are one value")
	assert.Contains(t, err.Error(), wantPath, "the message must name the offending path")
	for _, s := range wantText {
		assert.Contains(t, err.Error(), s)
	}
	for _, name := range snapshotEntries(t, gw) {
		assert.NotContains(t, name, runID.String(), "a rejected snapshot leaves nothing behind (found %s)", name)
	}
}

func TestGroupWorkspace_BrokenGitWorktreePointerIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	writeFile(t, filepath.Join(parent, "work", ".git"), "gitdir: /elsewhere/src/.git/worktrees/work\n", 0o644)
	writeFile(t, filepath.Join(parent, "work", "main.go"), "package main\n", 0o644)

	assertUnsupported(t, newGW(t, root), parent, "work/.git", "resolve metadata")
}

func TestGroupWorkspace_AbsoluteSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	writeFile(t, filepath.Join(parent, "a.txt"), "a", 0o644)
	outside := filepath.Join(t.TempDir(), "secret")
	writeFile(t, outside, "s", 0o600)
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "cfg"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(parent, "cfg", "leak")))

	assertUnsupported(t, newGW(t, root), parent, "cfg/leak", "absolute path")
}

func TestGroupWorkspace_EscapingRelativeSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	require.NoError(t, os.Symlink("../../outside", filepath.Join(parent, "escape")))

	assertUnsupported(t, newGW(t, root), parent, "escape", "outside the working area")
}

// A link that is lexically inside but escapes through a SHALLOWER in-tree
// symlink ("self" -> ".", "x" -> "self/..") must be caught by resolution, not
// by string checks alone.
func TestGroupWorkspace_SymlinkEscapingThroughAnotherSymlinkIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	require.NoError(t, os.Symlink(".", filepath.Join(parent, "self")))
	require.NoError(t, os.Symlink("self/..", filepath.Join(parent, "sneaky")))

	assertUnsupported(t, newGW(t, root), parent, "sneaky", "resolves outside")
}

func TestGroupWorkspace_DanglingSymlinks(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	require.NoError(t, os.Symlink("build/output.bin", filepath.Join(parent, "latest")))
	gw := newGW(t, root)
	ctx := context.Background()

	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err, "a dangling downward link stays inside wherever it ends up")
	fork := filepath.Join(root, "instances", "task")
	require.NoError(t, gw.ForkTask(ctx, handle, fork))
	target, err := os.Readlink(filepath.Join(fork, "latest"))
	require.NoError(t, err)
	assert.Equal(t, "build/output.bin", target)

	other := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(filepath.Join(other, "sub"), 0o700))
	require.NoError(t, os.Symlink("../missing", filepath.Join(other, "sub", "up")))
	assertUnsupported(t, gw, other, "sub/up", "dangling")
}

func TestGroupWorkspace_SymlinkLoopIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	require.NoError(t, os.Symlink("b", filepath.Join(parent, "a")))
	require.NoError(t, os.Symlink("a", filepath.Join(parent, "b")))

	assertUnsupported(t, newGW(t, root), parent, "", "cannot be resolved")
}

func TestGroupWorkspace_SpecialFileIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	require.NoError(t, os.MkdirAll(parent, 0o700))
	if err := syscall.Mkfifo(filepath.Join(parent, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	assertUnsupported(t, newGW(t, root), parent, "pipe", "special file")
}

// ---------- error paths ----------

func TestGroupWorkspace_SnapshotInputErrors(t *testing.T) {
	root := t.TempDir()
	gw := newGW(t, root)
	ctx := context.Background()
	file := filepath.Join(root, "plain-file")
	writeFile(t, file, "x", 0o644)

	_, err := gw.SnapshotParent(ctx, uuid.Nil, root)
	assert.Error(t, err, "a nil group run id cannot name a snapshot")
	_, err = gw.SnapshotParent(ctx, uuid.New(), filepath.Join(root, "missing"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a missing parent directory is an error")
	_, err = gw.SnapshotParent(ctx, uuid.New(), file)
	assert.Error(t, err, "a file is not a working area")
	_, err = gw.SnapshotParent(ctx, uuid.New(), "relative/dir")
	assert.Error(t, err, "a relative parent path is refused")
	_, err = gw.SnapshotParent(ctx, uuid.New(), "")
	assert.Error(t, err)

	// A parent that CONTAINS the snapshot store would copy itself forever.
	_, err = gw.SnapshotParent(ctx, uuid.New(), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps the snapshot store")

	// The filesystem root also contains the store; prefix checks must not
	// accidentally look for a double path separator in this case.
	_, err = gw.SnapshotParent(copyingContext(), uuid.New(), string(os.PathSeparator))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps the snapshot store")
}

func TestGroupWorkspace_CancelledContextLeavesNoSnapshot(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, snapshotEntries(t, gw), "no partial snapshot or build dir remains")
}

func TestGroupWorkspace_ForkErrors(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	nonEmpty := filepath.Join(root, "instances", "busy")
	writeFile(t, filepath.Join(nonEmpty, "keep.txt"), "mine", 0o644)
	err = gw.ForkTask(ctx, handle, nonEmpty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not empty")
	b, rerr := os.ReadFile(filepath.Join(nonEmpty, "keep.txt"))
	require.NoError(t, rerr)
	assert.Equal(t, "mine", string(b), "a refused fork never touches existing content")

	asFile := filepath.Join(root, "instances", "a-file")
	writeFile(t, asFile, "x", 0o644)
	assert.Error(t, gw.ForkTask(ctx, handle, asFile))
	assert.Error(t, gw.ForkTask(ctx, handle, "relative/task"))
	assert.Error(t, gw.ForkTask(ctx, handle, filepath.Join(gw.SnapshotsDir(), "inside")),
		"a fork may never write into the snapshot store")

	released := uuid.New()
	missing := filepath.Join(gw.SnapshotsDir(), released.String())
	err = gw.ForkTask(ctx, missing, filepath.Join(root, "instances", "t1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")

	assert.ErrorIs(t, gw.ForkTask(ctx, "", filepath.Join(root, "instances", "t2")), filesystem.ErrInvalidSnapshotHandle)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	partial := filepath.Join(root, "instances", "t3")
	require.ErrorIs(t, gw.ForkTask(cctx, handle, partial), context.Canceled)
	_, serr := os.Stat(partial)
	assert.ErrorIs(t, serr, fs.ErrNotExist, "a failed fork into a new dir removes it")
}

// ---------- release ----------

func TestGroupWorkspace_ReleaseSnapshot(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	before := listTree(t, parent)

	require.NoError(t, gw.ReleaseSnapshot(ctx, handle), "a snapshot with read-only dirs is still removable")
	_, err = os.Stat(handle)
	assert.ErrorIs(t, err, fs.ErrNotExist)
	require.NoError(t, gw.ReleaseSnapshot(ctx, handle), "releasing twice is a no-op")
	require.NoError(t, gw.ReleaseSnapshot(ctx, filepath.Join(gw.SnapshotsDir(), uuid.New().String())),
		"an unknown handle is not an error")
	require.NoError(t, gw.ReleaseSnapshot(ctx, ""), "a parent with no workspace has nothing to release")

	assert.Equal(t, before, listTree(t, parent), "release never touches the parent")

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	other, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	assert.ErrorIs(t, gw.ReleaseSnapshot(cctx, other), context.Canceled)
	_, err = os.Stat(other)
	assert.NoError(t, err, "a cancelled release deletes nothing")
}

// Handles are validated: nothing outside <root>/.group-snapshots/<uuid> can
// be forked from or deleted.
func TestGroupWorkspace_TraversalHandlesAreRejected(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	_, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	store := gw.SnapshotsDir()
	bad := []string{
		filepath.Join(store, "..", "instances"),
		store + "/../instances",
		store,
		parent,
		filepath.Join(store, "not-a-uuid"),
		filepath.Join(store, strings.ToUpper(uuid.New().String())),
		filepath.Join(store, uuid.New().String(), "nested"),
		"relative/" + uuid.New().String(),
		fmt.Sprintf("%s/%s/", store, uuid.New().String()),
	}
	for _, h := range bad {
		assert.ErrorIs(t, gw.ReleaseSnapshot(ctx, h), filesystem.ErrInvalidSnapshotHandle, "release %q", h)
		assert.ErrorIs(t, gw.ForkTask(ctx, h, filepath.Join(root, "instances", uuid.New().String())),
			filesystem.ErrInvalidSnapshotHandle, "fork %q", h)
	}
	_, err = os.Stat(filepath.Join(parent, "README.md"))
	assert.NoError(t, err, "a traversal handle must never delete the parent")
}

// A well-formed handle must not redirect reads or deletes to another area.
func TestGroupWorkspace_SymlinkHandlesAreRejected(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	_, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	before := listTree(t, parent)
	handle := filepath.Join(gw.SnapshotsDir(), uuid.New().String())
	require.NoError(t, os.Symlink(parent, handle))
	taskDir := filepath.Join(root, "instances", "task")
	assert.ErrorIs(t, gw.ForkTask(ctx, handle, taskDir), filesystem.ErrInvalidSnapshotHandle)
	assert.ErrorIs(t, gw.ReleaseSnapshot(ctx, handle), filesystem.ErrInvalidSnapshotHandle)
	_, err = os.Lstat(taskDir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "invalid handles must not produce a fork")
	assert.Equal(t, before, listTree(t, parent), "invalid handles must not touch their target")
	_, err = os.Readlink(handle)
	assert.NoError(t, err, "validation must precede deletion")
}

func TestGroupWorkspace_SymlinkStoreIsRejected(t *testing.T) {
	root := t.TempDir()
	gw := newGW(t, root)
	outside := t.TempDir()
	id := uuid.New()
	writeFile(t, filepath.Join(outside, id.String(), "keep.txt"), "keep", 0o644)
	require.NoError(t, os.Chmod(outside, 0o755))
	require.NoError(t, os.Symlink(outside, gw.SnapshotsDir()))
	before := listTree(t, outside)
	handle := filepath.Join(gw.SnapshotsDir(), id.String())
	ctx := context.Background()
	assert.ErrorIs(t, gw.ForkTask(ctx, handle, filepath.Join(root, "task")), filesystem.ErrInvalidSnapshotHandle)
	assert.ErrorIs(t, gw.ReleaseSnapshot(ctx, handle), filesystem.ErrInvalidSnapshotHandle)

	parent := filepath.Join(root, "instances", "parent")
	writeFile(t, filepath.Join(parent, "a.txt"), "a", 0o644)
	_, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	assert.Error(t, err, "snapshot creation must not follow a substituted store")
	assert.Equal(t, before, listTree(t, outside))
	info, err := os.Stat(outside)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "store validation must precede chmod")
}

func TestGroupWorkspace_ForkThroughStoreAliasIsRejected(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	before := listTree(t, gw.SnapshotsDir())
	alias := filepath.Join(root, "store-alias")
	require.NoError(t, os.Symlink(gw.SnapshotsDir(), alias))

	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			dest := filepath.Join(gw.SnapshotsDir(), "task")
			if exists {
				require.NoError(t, os.Mkdir(dest, 0o750))
			}
			err := gw.ForkTask(ctx, handle, filepath.Join(alias, "task"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "snapshot store")
			if exists {
				assert.Empty(t, listTree(t, dest), "a refused fork must leave the existing dir empty")
				require.NoError(t, os.Remove(dest))
			} else {
				_, err := os.Lstat(dest)
				assert.ErrorIs(t, err, fs.ErrNotExist)
			}
			assert.Equal(t, before, listTree(t, gw.SnapshotsDir()))
		})
	}
}

// cancelDuringCopy cancels on the first entry after the root and a directory
// have been visited, making rollback deterministic without timing assumptions.
type cancelDuringCopy struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelDuringCopy) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func copyingContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelDuringCopy{Context: ctx, cancel: cancel}
}

func TestGroupWorkspace_CancelledCopyRollsBack(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", "parent")
	writeFile(t, filepath.Join(parent, "nested", "file.txt"), "starting data", 0o644)
	gw := newGW(t, root)
	ctx := context.Background()

	_, err := gw.SnapshotParent(copyingContext(), uuid.New(), parent)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, snapshotEntries(t, gw), "a partially built snapshot must be removed")
	handle, err := gw.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprint(exists), func(t *testing.T) {
			taskDir := filepath.Join(root, "instances", "task")
			if exists {
				require.NoError(t, os.Mkdir(taskDir, 0o750))
			}
			require.ErrorIs(t, gw.ForkTask(copyingContext(), handle, taskDir), context.Canceled)
			if exists {
				assert.Empty(t, listTree(t, taskDir), "rollback preserves an existing empty directory")
				info, err := os.Stat(taskDir)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o750), info.Mode().Perm())
			} else {
				_, err := os.Stat(taskDir)
				assert.ErrorIs(t, err, fs.ErrNotExist)
			}
		})
	}
}

// Re-snapshotting the same group run (a retried launch that never persisted
// its run) replaces the earlier snapshot instead of failing or merging.
func TestGroupWorkspace_ResnapshotSameRunReplaces(t *testing.T) {
	root, parent := buildParent(t)
	gw := newGW(t, root)
	ctx := context.Background()
	runID := uuid.New()

	first, err := gw.SnapshotParent(ctx, runID, parent)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(parent, "secret", "key")))
	second, err := gw.SnapshotParent(ctx, runID, parent)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, listTree(t, parent), listTree(t, second))
	assert.Equal(t, []string{runID.String()}, snapshotEntries(t, gw), "no stale build dir remains")
}

func TestGroupWorkspace_SnapshotInventoryIncludesInterruptedBuilds(t *testing.T) {
	ctx := context.Background()
	ws, err := filesystem.NewGroupWorkspace(t.TempDir())
	require.NoError(t, err)
	inventory, ok := any(ws).(engine.SnapshotInventory)
	require.True(t, ok, "results snapshots must be discoverable after a crash")
	infos, err := inventory.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Empty(t, infos)
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "keep.txt"), "keep", 0o600)
	id, interrupted := uuid.New(), uuid.New()
	handle, err := ws.SnapshotParent(ctx, id, parent)
	require.NoError(t, err)
	tmp := filepath.Join(ws.SnapshotsDir(), ".tmp-"+interrupted.String())
	require.NoError(t, os.Mkdir(tmp, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(ws.SnapshotsDir(), "unrelated"), 0o700))
	require.NoError(t, os.Symlink(parent, filepath.Join(ws.SnapshotsDir(), uuid.NewString())))
	infos, err = inventory.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2, "only snapshots and interrupted snapshot builds are inventoried")
	handles := []string{infos[0].Handle, infos[1].Handle}
	assert.ElementsMatch(t, []string{handle, filepath.Join(ws.SnapshotsDir(), interrupted.String())}, handles)
	for _, info := range infos {
		present, err := inventory.HasSnapshot(ctx, info.Handle)
		require.NoError(t, err)
		require.True(t, present)
		require.NoError(t, ws.ReleaseSnapshot(ctx, info.Handle))
	}
	require.NoDirExists(t, handle)
	require.NoDirExists(t, tmp)
	require.FileExists(t, filepath.Join(parent, "keep.txt"), "inventory must not follow symlinks")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = inventory.ListSnapshots(cancelled)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, os.Rename(ws.SnapshotsDir(), ws.SnapshotsDir()+"-real"))
	require.NoError(t, os.Symlink(parent, ws.SnapshotsDir()))
	_, err = inventory.ListSnapshots(ctx)
	require.ErrorIs(t, err, filesystem.ErrInvalidSnapshotHandle)
}
