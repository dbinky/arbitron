package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conflictedStaging builds a gather whose staging area holds an unresolved
// merge of task b over task a on c.txt (index stages 1/2/3, MERGE_HEAD),
// plus untracked, ignored, executable and empty-directory content.
func conflictedStaging(t *testing.T, binary bool) (*gatherFixture, engine.GatherHandle) {
	t.Helper()
	base, ours, theirs := "base\n", "from a\n", "from b\n"
	if binary {
		base, ours, theirs = "b\x00ase", "a\x00side", "b\x00side"
	}
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "c.txt", base, 0o644)
		writeFile(t, p, ".gitignore", "*.log\n", 0o644)
	})
	require.NoError(t, WithPortableGit(checkpoint.MaterializePortableGit)(f.a))
	refA := f.task(func(d string) { writeFile(t, d, "c.txt", ours, 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "c.txt", theirs, 0o644) })
	ctx := context.Background()
	h := f.begin()
	out, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	require.True(t, out.Clean)
	out, err = f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)
	require.False(t, out.Clean)
	writeFile(t, h.StagingDir, "notes/untracked.txt", "agent notes\n", 0o644)
	writeFile(t, h.StagingDir, "build.log", "ignored output\n", 0o644)
	writeFile(t, h.StagingDir, "tool.sh", "#!/bin/sh\necho hi\n", 0o755)
	require.NoError(t, os.MkdirAll(filepath.Join(h.StagingDir, "empty", "dir"), 0o755))
	return f, h
}

func resourceDst(f *gatherFixture) string {
	return filepath.Join(f.a.ResolutionResourceRoot(), uuid.NewString())
}

// standalone runs host git in dir with every alternate source disabled.
func standalone(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := execGit(full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_ALTERNATE_OBJECT_DIRECTORIES=")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestResolutionPortability_StandaloneConflictRoundTrip(t *testing.T) {
	f, h := conflictedStaging(t, false)
	ctx := context.Background()
	srcStages, err := standalone(t, h.StagingDir, "ls-files", "-s")
	require.NoError(t, err)
	srcMergeHead, err := os.ReadFile(filepath.Join(h.StagingDir, ".git", "MERGE_HEAD"))
	require.NoError(t, err)
	dst := resourceDst(f)

	require.NoError(t, f.a.ExportResolution(ctx, h.StagingDir, dst))

	backing, err := f.a.BackingDir(f.handle)
	require.NoError(t, err)
	require.NoError(t, os.Rename(backing, backing+".offline"), "make the server backing store unavailable")
	t.Cleanup(func() { _ = os.Rename(backing+".offline", backing) })

	_, err = os.Stat(filepath.Join(dst, ".git", "objects", "info", "alternates"))
	assert.True(t, os.IsNotExist(err), "the copy has no alternates")
	_, err = standalone(t, dst, "rev-parse", "--verify", "HEAD")
	require.NoError(t, err)
	_, err = standalone(t, dst, "cat-file", "-e", "MERGE_HEAD^{commit}")
	require.NoError(t, err)
	mergeHead, err := os.ReadFile(filepath.Join(dst, ".git", "MERGE_HEAD"))
	require.NoError(t, err)
	assert.Equal(t, string(srcMergeHead), string(mergeHead))
	unmerged, err := standalone(t, dst, "ls-files", "-u")
	require.NoError(t, err)
	for _, stage := range []string{" 1\tc.txt", " 2\tc.txt", " 3\tc.txt"} {
		assert.Contains(t, unmerged, stage, "every conflict stage survives")
	}
	stages, err := standalone(t, dst, "ls-files", "-s")
	require.NoError(t, err)
	assert.Equal(t, srcStages, stages, "the index is preserved exactly")
	_, err = standalone(t, dst, "merge-base", "HEAD", "MERGE_HEAD")
	require.NoError(t, err)
	for _, line := range strings.Split(stages, "\n") {
		sha := strings.Fields(line)[1]
		_, err := standalone(t, dst, "cat-file", "-e", sha)
		require.NoError(t, err, "index object %s is present", sha)
	}
	got := contentOf(t, dst)
	assert.Equal(t, "file:0644:agent notes\n", got["notes/untracked.txt"])
	assert.Equal(t, "file:0644:ignored output\n", got["build.log"], "ignored content is part of the resource")
	assert.Equal(t, "file:0755:#!/bin/sh\necho hi\n", got["tool.sh"])
	assert.Equal(t, "dir", got["empty/dir"])
	assert.Contains(t, got["c.txt"], "<<<<<<<", "conflict markers are preserved")
	_, err = os.Stat(filepath.Join(dst, ".git", "hooks"))
	assert.True(t, os.IsNotExist(err), "hooks are never copied")
}

func TestResolutionPortability_ResolvedIndexRoundTrip(t *testing.T) {
	f, h := conflictedStaging(t, false)
	ctx := context.Background()
	dst := resourceDst(f)
	require.NoError(t, f.a.ExportResolution(ctx, h.StagingDir, dst))

	// The remote runner works in its own root at the same relative path.
	remote := filepath.Join(t.TempDir(), "remote-root", ".runner-resources", "resolutions", filepath.Base(dst))
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o700))
	require.NoError(t, os.Rename(dst, remote))
	writeFile(t, remote, "c.txt", "from a and b\n", 0o644)
	author(t, remote, "add", "c.txt")
	writeFile(t, remote, "notes/untracked.txt", "edited remotely\n", 0o644)

	require.NoError(t, f.a.ImportResolution(ctx, remote, h.StagingDir))

	paths, err := f.a.ConflictPaths(ctx, h)
	require.NoError(t, err)
	assert.Empty(t, paths, "the remote resolution reached the staging index")
	require.NoError(t, f.a.CommitResolution(ctx, h, "resolve c.txt remotely"))
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, "file:0644:from a and b\n", contentOf(t, f.parent)["c.txt"], "the gather applied the committed remote result")
	alts, err := os.ReadFile(filepath.Join(h.StagingDir, ".git", "objects", "info", "alternates"))
	require.NoError(t, err)
	assert.Contains(t, string(alts), f.a.backing, "the server staging keeps its own alternates")
}

func TestResolutionPortability_RejectsForeignAlternateAndTraversal(t *testing.T) {
	f, h := conflictedStaging(t, false)
	ctx := context.Background()
	parentBefore := contentOf(t, f.parent)

	// A destination outside the resource root, or a traversal, is refused.
	for _, bad := range []string{filepath.Join(t.TempDir(), uuid.NewString()), filepath.Join(f.a.ResolutionResourceRoot(), "..", "escape"), filepath.Join(f.a.ResolutionResourceRoot(), "not-a-uuid")} {
		require.ErrorIs(t, f.a.ExportResolution(ctx, h.StagingDir, bad), ErrResolutionPortability, bad)
		_, err := os.Stat(bad)
		assert.True(t, os.IsNotExist(err), "nothing published at %s", bad)
	}
	// A source that is not this area's gather staging (the parent itself) is refused.
	require.ErrorIs(t, f.a.ExportResolution(ctx, f.parent, resourceDst(f)), ErrResolutionPortability)

	// A foreign alternate is refused and leaves the source byte-identical.
	alt := filepath.Join(h.StagingDir, ".git", "objects", "info", "alternates")
	orig, err := os.ReadFile(alt)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(alt, append(orig, []byte(t.TempDir()+"\n")...), 0o600))
	before := metadataOf(t, filepath.Join(h.StagingDir, ".git"))
	dst := resourceDst(f)
	require.ErrorIs(t, f.a.ExportResolution(ctx, h.StagingDir, dst), ErrResolutionPortability)
	assert.Equal(t, before, metadataOf(t, filepath.Join(h.StagingDir, ".git")), "source .git untouched")
	assert.Equal(t, parentBefore, contentOf(t, f.parent), "parent untouched")
	_, err = os.Stat(dst)
	assert.True(t, os.IsNotExist(err))

	// A symlink inside .git is refused.
	require.NoError(t, os.WriteFile(alt, orig, 0o600))
	require.NoError(t, os.Symlink("/etc", filepath.Join(h.StagingDir, ".git", "evil")))
	require.ErrorIs(t, f.a.ExportResolution(ctx, h.StagingDir, resourceDst(f)), ErrResolutionPortability)
}

func TestResolutionPortability_CopyDiskErrorPublishesNothing(t *testing.T) {
	f, h := conflictedStaging(t, false)
	ctx := context.Background()

	root := f.a.ResolutionResourceRoot()
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.Chmod(root, 0o500)) // write failure while copying
	dst := filepath.Join(root, uuid.NewString())
	require.Error(t, f.a.ExportResolution(ctx, h.StagingDir, dst))
	require.NoError(t, os.Chmod(root, 0o700))
	_, err := os.Stat(dst)
	assert.True(t, os.IsNotExist(err), "a failed copy publishes nothing")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "no partial directory is left behind")

	// A missing backing object (task b's conflicting blob) is an error, not a falsely portable copy.
	blob, err := standalone(t, h.StagingDir, "rev-parse", "MERGE_HEAD:c.txt")
	require.Error(t, err, "sanity: with alternates disabled the object is not local")
	res, err := f.a.git.Run(ctx, Command{Args: []string{"rev-parse", "MERGE_HEAD:c.txt"}, Dir: h.StagingDir})
	require.NoError(t, err)
	blob = strings.TrimSpace(res.Stdout)
	backing, err := f.a.BackingDir(f.handle)
	require.NoError(t, err)
	loose := filepath.Join(backing, "objects", blob[:2], blob[2:])
	require.FileExists(t, loose, "fixture objects are loose (gc.auto=0)")
	require.NoError(t, os.Remove(loose))
	dst = resourceDst(f)
	require.Error(t, f.a.ExportResolution(ctx, h.StagingDir, dst))
	_, err = os.Stat(dst)
	assert.True(t, os.IsNotExist(err), "a missing object never registers a usable candidate")
}

func TestResolutionPortability_BinaryConflictEmptyDirAndAlternateCycle(t *testing.T) {
	f, h := conflictedStaging(t, true)
	ctx := context.Background()
	src, err := standalone(t, h.StagingDir, "ls-files", "-s")
	require.NoError(t, err)
	dst := resourceDst(f)
	require.NoError(t, f.a.ExportResolution(ctx, h.StagingDir, dst))
	got, err := standalone(t, dst, "ls-files", "-s")
	require.NoError(t, err)
	assert.Equal(t, src, got, "binary index stages are preserved")
	assert.Equal(t, "dir", contentOf(t, dst)["empty/dir"])

	// Expanded bytes are bounded.
	require.NoError(t, WithResolutionMaxBytes(16)(f.a))
	big := resourceDst(f)
	require.ErrorIs(t, f.a.ExportResolution(ctx, h.StagingDir, big), ErrResolutionTooLarge)
	_, err = os.Stat(big)
	assert.True(t, os.IsNotExist(err))
	require.NoError(t, WithResolutionMaxBytes(0)(f.a)) // back to the default bound

	// An alternate cycle (backing -> itself) is rejected.
	backing, err := f.a.BackingDir(f.handle)
	require.NoError(t, err)
	loop := filepath.Join(backing, "objects", "info", "alternates")
	require.NoError(t, os.MkdirAll(filepath.Dir(loop), 0o700))
	require.NoError(t, os.WriteFile(loop, []byte(filepath.Join(backing, "objects")+"\n"), 0o600))
	t.Cleanup(func() { _ = os.Remove(loop) })
	require.ErrorIs(t, f.a.ExportResolution(ctx, h.StagingDir, resourceDst(f)), ErrResolutionPortability)
}

func TestResolutionPortability_RequiresInjectedMaterializer(t *testing.T) {
	f, h := conflictedStaging(t, false)
	f.a.portableGit = nil
	require.ErrorIs(t, f.a.ExportResolution(context.Background(), h.StagingDir, resourceDst(f)), ErrResolutionPortability,
		"without the P3 T2 materializer there is no second closure implementation to fall back to")
}

// execGit builds a host git command (tests only).
func execGit(args ...string) *exec.Cmd { return exec.Command("git", args...) }
