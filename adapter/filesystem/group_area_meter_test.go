package filesystem

import (
	"context"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupAreaMeter_CountsEveryRegularFileIncludingGitMetadata(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), make([]byte, 100), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "objects", "pack"), make([]byte, 1000), 0o600))
	outside := filepath.Join(t.TempDir(), "big")
	require.NoError(t, os.WriteFile(outside, make([]byte, 1<<20), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link")))

	n, err := GroupAreaMeter{}.AreaBytes(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, int64(1100), n, "symlinks are not followed")

	_, err = GroupAreaMeter{}.AreaBytes(context.Background(), filepath.Join(dir, "missing"))
	assert.Error(t, err)
}

// dirBytes sums regular-file bytes under dir (what a copy actually wrote).
func dirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	}))
	return n
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// The launch byte budget must refuse a group whose forks would copy more than
// the budget. A worktree-backed area (the normal Arbitron instance workspace)
// holds only a tiny `.git` pointer file; CopyGroupGitMetadata follows it and
// copies the external gitdir, the shared common dir and alternate objects into
// every fork. The verify gate on #76 measured 63 bytes for an area whose fork
// copied 3,225,645. The meter must count at least what the copier writes.

// HAPPY — a git worktree's external metadata is counted.
func TestGroupAreaMeter_CountsExternalWorktreeMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	main := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.MkdirAll(main, 0o700))
	gitRun(t, main, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(main, "big.bin"), randomBytes(t, 256<<10), 0o600))
	gitRun(t, main, "add", ".")
	gitRun(t, main, "commit", "-q", "-m", "seed")
	area := filepath.Join(t.TempDir(), "work")
	gitRun(t, main, "worktree", "add", "-q", area)

	measured, err := GroupAreaMeter{}.AreaBytes(context.Background(), area)
	require.NoError(t, err)

	fork := t.TempDir()
	require.NoError(t, CopyGroupGitMetadata(context.Background(), area, fork))
	copied := dirBytes(t, fork)
	require.Greater(t, copied, int64(256<<10)/2, "fixture must copy real external objects")
	assert.GreaterOrEqual(t, measured, copied, "meter must count every byte the fork copier writes")
}

// SUCCESS — a plain repository (.git directory inside the area) still counts once.
func TestGroupAreaMeter_InAreaRepositoryCountedOnce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	area := t.TempDir()
	gitRun(t, area, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(area, "f.txt"), []byte("hi"), 0o600))
	gitRun(t, area, "add", ".")
	gitRun(t, area, "commit", "-q", "-m", "seed")
	measured, err := GroupAreaMeter{}.AreaBytes(context.Background(), area)
	require.NoError(t, err)
	assert.Equal(t, dirBytes(t, area), measured, "an in-area .git is part of the area walk, not added twice")
}

// EDGE — objects borrowed through alternates are counted (the copier materializes them).
func TestGroupAreaMeter_CountsAlternateObjects(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	origin := filepath.Join(t.TempDir(), "origin")
	require.NoError(t, os.MkdirAll(origin, 0o700))
	gitRun(t, origin, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(origin, "big.bin"), randomBytes(t, 128<<10), 0o600))
	gitRun(t, origin, "add", ".")
	gitRun(t, origin, "commit", "-q", "-m", "seed")
	area := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(area), "clone", "-q", "--shared", origin, area)

	measured, err := GroupAreaMeter{}.AreaBytes(context.Background(), area)
	require.NoError(t, err)
	fork := t.TempDir()
	require.NoError(t, CopyGroupGitMetadata(context.Background(), area, fork))
	assert.GreaterOrEqual(t, measured, dirBytes(t, fork))
}

// ERROR — a malformed .git pointer is an error, never an under-count.
func TestGroupAreaMeter_MalformedPointerFails(t *testing.T) {
	area := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(area, ".git"), []byte("not a pointer"), 0o600))
	_, err := GroupAreaMeter{}.AreaBytes(context.Background(), area)
	assert.Error(t, err)
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}
