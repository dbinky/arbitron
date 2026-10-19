package git

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gatherFixture is a snapshotted parent plus helpers to fork, edit and
// finalize task copies.
type gatherFixture struct {
	t      *testing.T
	a      *WorkingArea
	root   string
	parent string
	handle string
}

func newGatherFixture(t *testing.T, build func(parent string)) *gatherFixture {
	t.Helper()
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/parent")
	build(parent)
	handle, err := a.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	return &gatherFixture{t: t, a: a, root: root, parent: parent, handle: handle}
}

// task forks a task copy, lets edit change it, finalizes it and returns the ref.
func (f *gatherFixture) task(edit func(dir string)) string {
	f.t.Helper()
	id := uuid.New()
	dir := filepath.Join(f.root, "instances", id.String())
	require.NoError(f.t, f.a.ForkTask(context.Background(), f.handle, dir))
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(f.t, err)
	edit(resolved)
	ref, err := f.a.FinalizeTask(context.Background(), f.handle, id, resolved)
	require.NoError(f.t, err)
	return ref
}

func (f *gatherFixture) begin() engine.GatherHandle {
	f.t.Helper()
	h, err := f.a.BeginGather(context.Background(), f.handle, uuid.New(), f.parent)
	require.NoError(f.t, err)
	return h
}

// metadataOf renders every byte of a .git directory except the index file,
// whose stat cache any "git status" (including the test's own) may refresh;
// index ENTRIES are compared separately with "git ls-files -s".
func metadataOf(t *testing.T, gitDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(gitDir, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() || (filepath.Dir(p) == gitDir && d.Name() == "index") {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		require.NoError(t, rerr)
		rel, _ := filepath.Rel(gitDir, p)
		out[rel] = string(raw)
		return nil
	}))
	return out
}

func TestGather_TwoCleanMergesInOrderPlainDirectory(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "shared.txt", "line1\nline2\nline3\nline4\nline5\n", 0o644)
		writeFile(t, p, "keep.txt", "keep\n", 0o644)
	})
	refA := f.task(func(d string) {
		writeFile(t, d, "shared.txt", "line1 by a\nline2\nline3\nline4\nline5\n", 0o644)
		writeFile(t, d, "a-only.txt", "a\n", 0o644)
	})
	refB := f.task(func(d string) {
		writeFile(t, d, "shared.txt", "line1\nline2\nline3\nline4\nline5 by b\n", 0o644)
		writeFile(t, d, "b-only.txt", "b\n", 0o644)
	})
	ctx := context.Background()
	h := f.begin()
	for _, ref := range []string{refA, refB} {
		out, err := f.a.MergeTask(ctx, h, ref)
		require.NoError(t, err)
		assert.True(t, out.Clean)
		assert.Empty(t, out.ConflictPaths)
	}
	require.NoError(t, f.a.Apply(ctx, h))
	got := contentOf(t, f.parent)
	assert.Equal(t, "file:0644:line1 by a\nline2\nline3\nline4\nline5 by b\n", got["shared.txt"])
	assert.Equal(t, "file:0644:a\n", got["a-only.txt"])
	assert.Equal(t, "file:0644:b\n", got["b-only.txt"])
	assert.Equal(t, "file:0644:keep\n", got["keep.txt"])
}

func TestGather_DeletionRenameBinaryModeAndSymlinkChanges(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "del.txt", "delete me\n", 0o644)
		writeFile(t, p, "old/name.txt", "rename me\nwith content\nthat is long enough\n", 0o644)
		writeFile(t, p, "img.bin", "\x00\x01old", 0o644)
		writeFile(t, p, "tool.sh", "#!/bin/sh\n", 0o644)
		require.NoError(t, os.Symlink("del.txt", filepath.Join(p, "link")))
	})
	ref := f.task(func(d string) {
		require.NoError(t, os.Remove(filepath.Join(d, "del.txt")))
		require.NoError(t, os.MkdirAll(filepath.Join(d, "new"), 0o755))
		require.NoError(t, os.Rename(filepath.Join(d, "old", "name.txt"), filepath.Join(d, "new", "name.txt")))
		require.NoError(t, os.Remove(filepath.Join(d, "old")))
		writeFile(t, d, "img.bin", "\x00\x02new\xff", 0o644)
		require.NoError(t, os.Chmod(filepath.Join(d, "tool.sh"), 0o755))
		require.NoError(t, os.Remove(filepath.Join(d, "link")))
		require.NoError(t, os.Symlink("img.bin", filepath.Join(d, "link")))
	})
	ctx := context.Background()
	h := f.begin()
	out, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.True(t, out.Clean)
	require.NoError(t, f.a.Apply(ctx, h))

	got := contentOf(t, f.parent)
	assert.NotContains(t, got, "del.txt")
	assert.NotContains(t, got, "old/name.txt")
	assert.NoDirExists(t, filepath.Join(f.parent, "old"), "a directory emptied by the gather is removed")
	assert.Equal(t, "file:0644:rename me\nwith content\nthat is long enough\n", got["new/name.txt"])
	assert.Equal(t, "file:0644:\x00\x02new\xff", got["img.bin"])
	assert.Equal(t, "file:0755:#!/bin/sh\n", got["tool.sh"])
	assert.Equal(t, "link:img.bin", got["link"])
}

func TestGather_RenameInOneTaskEditInAnotherMergesBoth(t *testing.T) {
	body := "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\n"
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", body, 0o644) })
	refRename := f.task(func(d string) {
		require.NoError(t, os.Rename(filepath.Join(d, "a.txt"), filepath.Join(d, "b.txt")))
	})
	refEdit := f.task(func(d string) {
		writeFile(t, d, "a.txt", strings.Replace(body, "gamma", "GAMMA", 1), 0o644)
	})
	ctx := context.Background()
	h := f.begin()
	for _, ref := range []string{refRename, refEdit} {
		out, err := f.a.MergeTask(ctx, h, ref)
		require.NoError(t, err)
		require.True(t, out.Clean, "%v", out.ConflictPaths)
	}
	require.NoError(t, f.a.Apply(ctx, h))
	got := contentOf(t, f.parent)
	assert.NotContains(t, got, "a.txt")
	assert.Equal(t, "file:0644:"+strings.Replace(body, "gamma", "GAMMA", 1), got["b.txt"])
}

func TestGather_ConflictResolveThenNextMerge(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "c.txt", "base\n", 0o644) })
	refA := f.task(func(d string) { writeFile(t, d, "c.txt", "from a\n", 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "c.txt", "from b\n", 0o644) })
	refC := f.task(func(d string) { writeFile(t, d, "c-extra.txt", "c\n", 0o644) })
	ctx := context.Background()
	h := f.begin()

	out, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	require.True(t, out.Clean)
	out, err = f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)
	assert.False(t, out.Clean)
	assert.Equal(t, []string{"c.txt"}, out.ConflictPaths)

	staged, err := os.ReadFile(filepath.Join(h.StagingDir, "c.txt"))
	require.NoError(t, err)
	assert.Contains(t, string(staged), "<<<<<<<")
	assert.Contains(t, string(staged), ">>>>>>>")

	paths, err := f.a.ConflictPaths(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, []string{"c.txt"}, paths)
	require.ErrorIs(t, f.a.CommitResolution(ctx, h, "too early"), engine.ErrStillConflicted)
	require.ErrorIs(t, f.a.Apply(ctx, h), ErrMergeInProgress)
	_, err = f.a.MergeTask(ctx, h, refC)
	require.ErrorIs(t, err, ErrMergeInProgress, "a different task cannot merge while one is unresolved")

	again, err := f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err, "re-merging the in-progress ref (restart) reports the same conflict")
	assert.Equal(t, []string{"c.txt"}, again.ConflictPaths)

	// The resolver edits the file and marks it resolved, as the prompt asks.
	writeFile(t, h.StagingDir, "c.txt", "from a and b\n", 0o644)
	author(t, h.StagingDir, "add", "c.txt")
	paths, err = f.a.ConflictPaths(ctx, h)
	require.NoError(t, err)
	assert.Empty(t, paths)
	require.NoError(t, f.a.CommitResolution(ctx, h, "resolve c.txt"))

	out, err = f.a.MergeTask(ctx, h, refC)
	require.NoError(t, err)
	assert.True(t, out.Clean)
	require.NoError(t, f.a.Apply(ctx, h))
	got := contentOf(t, f.parent)
	assert.Equal(t, "file:0644:from a and b\n", got["c.txt"])
	assert.Equal(t, "file:0644:c\n", got["c-extra.txt"])
}

func TestGather_StagedFileWithMarkersIsStillConflicted(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "c.txt", "base\n", 0o644) })
	refA := f.task(func(d string) { writeFile(t, d, "c.txt", "a\n", 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "c.txt", "b\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	_, err = f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)

	author(t, h.StagingDir, "add", "c.txt") // marked resolved, markers still inside
	paths, err := f.a.ConflictPaths(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, []string{"c.txt"}, paths)
	require.ErrorIs(t, f.a.CommitResolution(ctx, h, "x"), engine.ErrStillConflicted)
}

// An empty index after abort/reset is not evidence that the task was merged.
// Removing the pending-merge identity check must make these real-Git cases fail.
func TestGather_InterruptedMergeCannotBeVerifiedAsResolved(t *testing.T) {
	for _, command := range [][]string{{"merge", "--abort"}, {"reset", "--hard", "HEAD"}, {"merge", "--quit"}} {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			f := newGatherFixture(t, func(p string) { writeFile(t, p, "c.txt", "base\n", 0o644) })
			refA := f.task(func(d string) { writeFile(t, d, "c.txt", "a\n", 0o644) })
			refB := f.task(func(d string) { writeFile(t, d, "c.txt", "b\n", 0o644) })
			h := f.begin()
			_, err := f.a.MergeTask(t.Context(), h, refA)
			require.NoError(t, err)
			out, err := f.a.MergeTask(t.Context(), h, refB)
			require.NoError(t, err)
			require.False(t, out.Clean)
			author(t, h.StagingDir, command...)
			// --quit leaves the index alone; stage marker-free text as a
			// resolver might, without recreating the missing merge identity.
			writeFile(t, h.StagingDir, "c.txt", "a\n", 0o644)
			author(t, h.StagingDir, "add", "c.txt")
			require.Empty(t, author(t, h.StagingDir, "ls-files", "-u"))
			head := author(t, h.StagingDir, "rev-parse", "HEAD")
			_, err = f.a.ConflictPaths(t.Context(), h)
			assert.Error(t, err, "an aborted pending merge must fail verification")
			assert.Error(t, f.a.CommitResolution(t.Context(), h, "false success"))
			assert.Equal(t, head, author(t, h.StagingDir, "rev-parse", "HEAD"), "rejection must not publish a commit")
			assert.Error(t, f.a.Apply(t.Context(), h), "the incomplete gather cannot reach the parent")
			require.NoError(t, f.a.Rollback(t.Context(), h), "rollback remains available")
			assert.Equal(t, "file:0644:base\n", contentOf(t, f.parent)["c.txt"])
		})
	}
}

func TestGather_EditedButNotMarkedResolvedIsStillConflicted(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "c.txt", "base\n", 0o644) })
	refA := f.task(func(d string) { writeFile(t, d, "c.txt", "a\n", 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "c.txt", "b\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	_, err = f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)
	writeFile(t, h.StagingDir, "c.txt", "clean text but never git-added\n", 0o644)
	paths, err := f.a.ConflictPaths(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, []string{"c.txt"}, paths, "unmerged index entries are the authority")
}

func TestGather_BinaryConflictResolvedByChoosingASide(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "img.bin", "\x00base", 0o644) })
	refA := f.task(func(d string) { writeFile(t, d, "img.bin", "\x00aaaa", 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "img.bin", "\x00bbbb", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	out, err := f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)
	assert.Equal(t, []string{"img.bin"}, out.ConflictPaths)
	author(t, h.StagingDir, "checkout", "--theirs", "--", "img.bin")
	author(t, h.StagingDir, "add", "img.bin")
	require.NoError(t, f.a.CommitResolution(ctx, h, "take b"))
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, "file:0644:\x00bbbb", contentOf(t, f.parent)["img.bin"])
}

func TestGather_EmptyDiffTaskLeavesParentIdentical(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "x.txt", "x\n", 0o644)
		require.NoError(t, os.MkdirAll(filepath.Join(p, "empty"), 0o755))
	})
	before := contentOf(t, f.parent)
	ref := f.task(func(string) {})
	ctx := context.Background()
	h := f.begin()
	out, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	assert.True(t, out.Clean)
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, before, contentOf(t, f.parent))
}

func TestGather_MergeTaskIsIdempotentForAMergedRef(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "x.txt", "x\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "x.txt", "y\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	head := strings.TrimSpace(author(t, h.StagingDir, "rev-parse", "HEAD"))
	out, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	assert.True(t, out.Clean)
	assert.Equal(t, head, author(t, h.StagingDir, "rev-parse", "HEAD"), "no second merge commit")
}

func TestGather_ParentDriftSinceSnapshotIsKept(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "parent.txt", "p\n", 0o644)
		writeFile(t, p, "task.txt", "t\n", 0o644)
	})
	ref := f.task(func(d string) { writeFile(t, d, "task.txt", "t by task\n", 0o644) })
	writeFile(t, f.parent, "parent.txt", "p changed after launch\n", 0o644)
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h))
	got := contentOf(t, f.parent)
	assert.Equal(t, "file:0644:p changed after launch\n", got["parent.txt"])
	assert.Equal(t, "file:0644:t by task\n", got["task.txt"])
}

func TestGather_AuthorRepositoryMetadataUntouchedAndDirtyStateRestored(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		authorRepo(t, filepath.Join(p), map[string]string{"repo.txt": "r1\n", ".gitignore": "*.log\n"})
		writeFile(t, p, "repo.txt", "r1 dirty\n", 0o644)
		writeFile(t, p, "untracked.txt", "u\n", 0o644)
		writeFile(t, p, "ignored.log", "i\n", 0o644)
		require.NoError(t, os.MkdirAll(filepath.Join(p, "empty"), 0o755))
	})
	before := contentOf(t, f.parent)
	status := author(t, f.parent, "status", "--porcelain=v1", "--ignored")
	meta := metadataOf(t, filepath.Join(f.parent, ".git"))
	entries := author(t, f.parent, "ls-files", "-s")

	ref := f.task(func(d string) {
		writeFile(t, d, "repo.txt", "r1 dirty\nplus task\n", 0o644)
		require.NoError(t, os.Remove(filepath.Join(d, "untracked.txt")))
		writeFile(t, d, "added.txt", "added\n", 0o644)
		author(t, d, "add", "-A")
		author(t, d, "commit", "-q", "-m", "task commits inside its own copy")
	})
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h))

	got := contentOf(t, f.parent)
	assert.Equal(t, "file:0644:r1 dirty\nplus task\n", got["repo.txt"])
	assert.Equal(t, "file:0644:added\n", got["added.txt"])
	assert.NotContains(t, got, "untracked.txt")
	assert.Equal(t, meta, metadataOf(t, filepath.Join(f.parent, ".git")),
		"the task's commit is content only: parent HEAD, refs, config and objects are byte-identical")
	assert.Equal(t, entries, author(t, f.parent, "ls-files", "-s"), "parent index entries unchanged")

	require.NoError(t, f.a.Rollback(ctx, h))
	assert.Equal(t, before, contentOf(t, f.parent), "rollback restores dirty, untracked, ignored and empty-dir state")
	assert.Equal(t, status, author(t, f.parent, "status", "--porcelain=v1", "--ignored"))
	assert.Equal(t, meta, metadataOf(t, filepath.Join(f.parent, ".git")))
}

func TestGather_FilesOutsideRepoHintAndNestedRepository(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		require.NoError(t, os.MkdirAll(filepath.Join(p, "repo"), 0o755))
		authorRepo(t, filepath.Join(p, "repo"), map[string]string{"in.txt": "in\n"})
		writeFile(t, p, "notes/outside.txt", "outside\n", 0o644)
	})
	nestedMeta := metadataOf(t, filepath.Join(f.parent, "repo", ".git"))
	ref := f.task(func(d string) {
		writeFile(t, d, "repo/in.txt", "in edited\n", 0o644)
		writeFile(t, d, "notes/outside.txt", "outside edited\n", 0o644)
		writeFile(t, d, "top.txt", "new top\n", 0o644)
	})
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h))
	got := contentOf(t, f.parent)
	assert.Equal(t, "file:0644:in edited\n", got["repo/in.txt"])
	assert.Equal(t, "file:0644:outside edited\n", got["notes/outside.txt"])
	assert.Equal(t, "file:0644:new top\n", got["top.txt"])
	assert.Equal(t, nestedMeta, metadataOf(t, filepath.Join(f.parent, "repo", ".git")))
}

func TestGather_ApplyRefusesToReplaceMetadataDirectory(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		require.NoError(t, os.MkdirAll(filepath.Join(p, "repo"), 0o755))
		authorRepo(t, filepath.Join(p, "repo"), map[string]string{"in.txt": "in\n"})
	})
	ref := f.task(func(d string) {
		require.NoError(t, os.RemoveAll(filepath.Join(d, "repo")))
		writeFile(t, d, "repo", "now a file\n", 0o644)
	})
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.ErrorIs(t, f.a.Apply(ctx, h), ErrApplyTouchesMetadata)
	assert.DirExists(t, filepath.Join(f.parent, "repo", ".git"))
}

func TestGather_ApplyIsIdempotentAfterPartialApply(t *testing.T) {
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "a.txt", "a\n", 0o644)
		writeFile(t, p, "b.txt", "b\n", 0o644)
	})
	ref := f.task(func(d string) {
		writeFile(t, d, "a.txt", "a2\n", 0o644)
		require.NoError(t, os.Remove(filepath.Join(d, "b.txt")))
		writeFile(t, d, "c/c.txt", "c\n", 0o644)
	})
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h))
	want := contentOf(t, f.parent)

	// Simulate a crash mid-way through an earlier Apply: half old, half new.
	writeFile(t, f.parent, "b.txt", "b\n", 0o644)
	writeFile(t, f.parent, "a.txt", "a\n", 0o644)
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, want, contentOf(t, f.parent))
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, want, contentOf(t, f.parent))
}

func TestGather_BeginGatherIsIdempotentAndNeverRetakesRollback(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "original\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "a.txt", "task\n", 0o644) })
	ctx := context.Background()
	gatherID := uuid.New()
	h1, err := f.a.BeginGather(ctx, f.handle, gatherID, f.parent)
	require.NoError(t, err)
	_, err = f.a.MergeTask(ctx, h1, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h1))

	h2, err := f.a.BeginGather(ctx, f.handle, gatherID, f.parent) // restart after apply
	require.NoError(t, err)
	assert.Equal(t, h1, h2)
	out, err := f.a.MergeTask(ctx, h2, ref)
	require.NoError(t, err)
	assert.True(t, out.Clean, "merge progress survived the restart")
	require.NoError(t, f.a.Rollback(ctx, h2))
	assert.Equal(t, "file:0644:original\n", contentOf(t, f.parent)["a.txt"])
}

func TestGather_RollbackAfterStagingRemovedAndIdempotent(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "original\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "z.txt", "z\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(ctx, h))
	require.NoError(t, os.RemoveAll(h.StagingDir))
	require.NoError(t, f.a.Rollback(ctx, h))
	require.NoError(t, f.a.Rollback(ctx, h))
	assert.Equal(t, map[string]string{"a.txt": "file:0644:original\n"}, contentOf(t, f.parent))
}

func TestGather_MissingGitFailsWithoutTouchingParent(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "a\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "a.txt", "b\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	f.a.git = NewRunner(RunnerConfig{SearchPath: t.TempDir(), StateDir: t.TempDir()})
	require.ErrorIs(t, f.a.Apply(ctx, h), ErrGitUnavailable)
	require.ErrorIs(t, f.a.Rollback(ctx, h), ErrGitUnavailable)
	assert.Equal(t, "file:0644:a\n", contentOf(t, f.parent)["a.txt"])
}

func TestGather_HandleAndRefValidation(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "a\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, "refs/heads/main")
	require.Error(t, err, "only finalized task refs are mergeable")
	_, err = f.a.MergeTask(ctx, h, TaskRefPrefix+uuid.New().String())
	require.Error(t, err, "unknown task ref")
	forged := h
	forged.StagingDir = t.TempDir()
	_, err = f.a.ConflictPaths(ctx, forged)
	require.Error(t, err)
	forged = h
	forged.RollbackRef = RollbackRefPrefix + uuid.New().String()
	require.Error(t, f.a.Rollback(ctx, forged))
	_, err = f.a.BeginGather(ctx, f.handle, uuid.Nil, f.parent)
	require.Error(t, err)
	_, err = f.a.BeginGather(ctx, uuid.New().String(), uuid.New(), f.parent)
	require.Error(t, err, "unknown snapshot")
}

func TestReleaseRef_DeletesRefsAndRollbackStaging(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "a\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "a.txt", "b\n", 0o644) })
	ctx := context.Background()
	h := f.begin()

	require.NoError(t, f.a.ReleaseRef(ctx, f.handle, ref))
	_, err := f.a.ResolveRef(ctx, f.handle, ref)
	require.Error(t, err, "released task ref is gone")
	require.NoError(t, f.a.ReleaseRef(ctx, f.handle, ref), "releasing twice is a no-op")

	require.DirExists(t, h.StagingDir)
	require.NoError(t, f.a.ReleaseRef(ctx, f.handle, h.RollbackRef))
	assert.NoDirExists(t, h.StagingDir, "releasing the rollback pin ends the gather's staging area")
	require.Error(t, f.a.ReleaseRef(ctx, f.handle, "refs/heads/main"))
	require.NoError(t, f.a.ReleaseRef(ctx, uuid.New().String(), TaskRefPrefix+uuid.New().String()), "unknown handle")

	_, err = f.a.ResolveRef(ctx, f.handle, BaselineRef)
	require.NoError(t, err, "the baseline stays until ReleaseSnapshot")
}

func TestGather_CommitResolutionWithoutMergeIsANoOp(t *testing.T) {
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "a.txt", "a\n", 0o644) })
	ref := f.task(func(d string) { writeFile(t, d, "a.txt", "b\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, ref)
	require.NoError(t, err)
	head := author(t, h.StagingDir, "rev-parse", "HEAD")
	require.NoError(t, f.a.CommitResolution(ctx, h, "replayed after restart"))
	assert.Equal(t, head, author(t, h.StagingDir, "rev-parse", "HEAD"), "no commit when nothing changed")

	// A resolver that edits the staging tree outside a merge still has its
	// edits recorded (and applied).
	writeFile(t, h.StagingDir, "extra.txt", "x\n", 0o644)
	require.NoError(t, f.a.CommitResolution(ctx, h, "record extra"))
	assert.NotEqual(t, head, author(t, h.StagingDir, "rev-parse", "HEAD"))
	require.NoError(t, f.a.Apply(ctx, h))
	assert.Equal(t, "file:0644:x\n", contentOf(t, f.parent)["extra.txt"])
}
