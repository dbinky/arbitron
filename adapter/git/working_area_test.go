package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainParent builds a parent area with NO repository: nested dirs, an
// executable, a symlink, an empty dir, binary bytes and awkward names.
func plainParent(t *testing.T, root string) string {
	t.Helper()
	parent := mkdirIn(t, root, "instances/parent")
	writeFile(t, parent, "README.md", "hello\n", 0o644)
	writeFile(t, parent, "src/main.go", "package main\n", 0o644)
	writeFile(t, parent, "bin/run.sh", "#!/bin/sh\necho run\n", 0o755)
	writeFile(t, parent, "data/blob.bin", "\x00\x01\x02\xff binary", 0o644)
	writeFile(t, parent, "odd/\"quoted name", "q\n", 0o644)
	writeFile(t, parent, "odd/new\nline", "n\n", 0o644)
	writeFile(t, parent, "odd/trailing\r", "r\n", 0o644)
	require.NoError(t, os.Symlink("../README.md", filepath.Join(parent, "src", "readme-link")))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "logs", "empty"), 0o755))
	return parent
}

func TestNewWorkingArea_RequiresRootAndNonNilRunner(t *testing.T) {
	_, err := NewWorkingArea("")
	require.Error(t, err)
	_, err = NewWorkingArea(t.TempDir(), WithRunner(nil))
	require.Error(t, err)
}

// Provenance: the composition root builds the area with the root alone
// (gitarea.NewWorkingArea(cfg.Workspace.Path)); the default runner must work.
func TestNewWorkingArea_DefaultRunnerSnapshotsAndForks(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	a, err := NewWorkingArea(root)
	require.NoError(t, err)
	parent := mkdirIn(t, a.root, "instances/p")
	writeFile(t, parent, "f.txt", "f\n", 0o644)
	handle, err := a.SnapshotParent(context.Background(), uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(a.root, "instances", "t")
	require.NoError(t, a.ForkTask(context.Background(), handle, task))
	assert.Equal(t, contentOf(t, parent), contentOf(t, task))
}

func TestNewWorkingArea_CreatesBackingRootUnderWorkspaceRoot(t *testing.T) {
	a, root := newTestArea(t)
	info, err := os.Stat(filepath.Join(root, ".group-backing"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	id := uuid.New()
	dir, err := a.BackingDir(id.String())
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".group-backing", id.String()+".git"), dir)
}

func TestBackingDir_RejectsNonUUIDHandles(t *testing.T) {
	a, _ := newTestArea(t)
	for _, h := range []string{"", "../escape", uuid.Nil.String(), "not-a-uuid"} {
		_, err := a.BackingDir(h)
		assert.Error(t, err, h)
	}
}

func TestSnapshotFork_PlainDirectoryRoundTrips(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	want := contentOf(t, parent)

	runID := uuid.New()
	handle, err := a.SnapshotParent(ctx, runID, parent)
	require.NoError(t, err)
	assert.Equal(t, runID.String(), handle, "the handle is the group-run id")

	task := filepath.Join(root, "instances", "task-a")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	assert.Equal(t, want, contentOf(t, task))
	assert.Equal(t, "file:0755:#!/bin/sh\necho run\n", contentOf(t, task)["bin/run.sh"])
	assert.Equal(t, "link:../README.md", contentOf(t, task)["src/readme-link"])
	assert.Equal(t, "dir", contentOf(t, task)["logs/empty"])
}

func TestSnapshot_IsImmutableAndForksAreIndependent(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	want := contentOf(t, parent)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	taskA := filepath.Join(root, "instances", "a")
	taskB := filepath.Join(root, "instances", "b")
	require.NoError(t, a.ForkTask(ctx, handle, taskA))
	writeFile(t, taskA, "README.md", "changed by a\n", 0o644)
	writeFile(t, parent, "README.md", "changed by parent after snapshot\n", 0o644)
	require.NoError(t, a.ForkTask(ctx, handle, taskB))

	assert.Equal(t, want, contentOf(t, taskB), "a later fork still sees the group-start content")
	assert.Equal(t, "file:0644:changed by parent after snapshot\n", contentOf(t, parent)["README.md"])
}

func TestSnapshot_ByteExactDespiteAuthorAttributes(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, ".gitattributes", "* text eol=crlf\n*.dat filter=lfs ident\n", 0o644)
	writeFile(t, parent, "lf.txt", "a\nb\n", 0o644)
	writeFile(t, parent, "crlf.txt", "a\r\nb\r\n", 0o644)
	writeFile(t, parent, "id.dat", "$Id$\n", 0o644)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	assert.Equal(t, contentOf(t, parent), contentOf(t, task))
}

func TestSnapshot_WorkspaceRootAsParentNeverCapturesBackingStore(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	writeFile(t, root, "top.txt", "t\n", 0o644)
	handle, err := a.SnapshotParent(ctx, uuid.New(), root)
	require.NoError(t, err)
	for _, p := range lsTree(t, a, handle, BaselineRef) {
		assert.False(t, strings.HasPrefix(p, ".group-backing"), p)
	}
}

func TestSnapshot_AuthorRepositoryMetadataExcludedAndReproduced(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	authorRepo(t, parent, map[string]string{"tracked.txt": "v1\n", ".gitignore": "build/\n", "gone.txt": "x\n"})
	writeFile(t, parent, "tracked.txt", "v2 dirty\n", 0o644)
	writeFile(t, parent, "staged.txt", "staged\n", 0o644)
	author(t, parent, "add", "staged.txt")
	writeFile(t, parent, "untracked.txt", "u\n", 0o644)
	writeFile(t, parent, "build/out.o", "ignored\n", 0o644)
	require.NoError(t, os.Remove(filepath.Join(parent, "gone.txt")))
	parentStatus := author(t, parent, "status", "--porcelain=v1", "--ignored")
	parentHead := author(t, parent, "rev-parse", "HEAD")

	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	for _, p := range lsTree(t, a, handle, BaselineRef) {
		assert.NotContains(t, "/"+p+"/", "/.git/", "metadata must never be content: %s", p)
	}

	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	assert.Equal(t, contentOf(t, parent), contentOf(t, task))
	assert.Equal(t, parentStatus, author(t, task, "status", "--porcelain=v1", "--ignored"),
		"the task copy reports exactly the parent's dirty/staged/untracked/ignored state")
	assert.Equal(t, parentHead, author(t, task, "rev-parse", "HEAD"))

	// The copy is independent: committing in it never moves the parent repo.
	author(t, task, "add", "-A")
	author(t, task, "commit", "-q", "-m", "task commit")
	assert.Equal(t, parentHead, author(t, parent, "rev-parse", "HEAD"))
	assert.Equal(t, parentStatus, author(t, parent, "status", "--porcelain=v1", "--ignored"))
}

// The feature-pipeline sample layout: {{workspace}}/src is a clone and
// {{workspace}}/work is a linked worktree of it, both inside the area.
func TestSnapshot_InAreaLinkedWorktreeIsRepointedToTheTaskCopy(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	src := filepath.Join(parent, "src")
	require.NoError(t, os.MkdirAll(src, 0o755))
	authorRepo(t, src, map[string]string{"app.txt": "base\n"})
	author(t, src, "worktree", "add", "-q", "-b", "feature", filepath.Join(parent, "work"))
	writeFile(t, parent, "work/app.txt", "worktree edit\n", 0o644)
	workStatus := author(t, filepath.Join(parent, "work"), "status", "--porcelain=v1")
	srcHead := author(t, src, "rev-parse", "HEAD")

	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	taskResolved, err := filepath.EvalSymlinks(task)
	require.NoError(t, err)

	assert.Equal(t, contentOf(t, parent), contentOf(t, task))
	taskWork := filepath.Join(taskResolved, "work")
	assert.Equal(t, workStatus, author(t, taskWork, "status", "--porcelain=v1"))
	assert.Equal(t, "feature", author(t, taskWork, "rev-parse", "--abbrev-ref", "HEAD"))
	common := author(t, taskWork, "rev-parse", "--path-format=absolute", "--git-common-dir")
	assert.True(t, strings.HasPrefix(common, taskResolved), "worktree must belong to the task copy, got %s", common)

	author(t, taskWork, "commit", "-q", "-am", "task commit on feature")
	assert.Equal(t, srcHead, author(t, src, "rev-parse", "feature"), "parent repository refs untouched")
}

func TestSnapshot_ExternalWorktreePointerBecomesIndependentRepository(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	external := t.TempDir()
	authorRepo(t, external, map[string]string{"lib.txt": "lib\n"})
	parent := mkdirIn(t, root, "instances/p")
	wt := filepath.Join(parent, "wt")
	author(t, external, "worktree", "add", "-q", "-b", "topic", wt)
	writeFile(t, wt, "lib.txt", "edited in worktree\n", 0o644)
	writeFile(t, wt, "new.txt", "untracked\n", 0o644)
	wtStatus := author(t, wt, "status", "--porcelain=v1")
	externalHead := author(t, external, "rev-parse", "topic")

	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	taskWT := filepath.Join(task, "wt")

	info, err := os.Lstat(filepath.Join(taskWT, ".git"))
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "an external worktree pointer becomes an independent repository directory")
	_, err = os.Stat(filepath.Join(taskWT, ".git", "commondir"))
	assert.True(t, os.IsNotExist(err), "no commondir pointer back to the author's repository")
	assert.Equal(t, wtStatus, author(t, taskWT, "status", "--porcelain=v1"))
	assert.Equal(t, "topic", author(t, taskWT, "rev-parse", "--abbrev-ref", "HEAD"))

	author(t, taskWT, "add", "-A")
	author(t, taskWT, "commit", "-q", "-m", "task commit")
	assert.Equal(t, externalHead, author(t, external, "rev-parse", "topic"), "external repository untouched")
}

func TestSnapshot_LockedAuthorIndexIsActionableAndLeavesNothing(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	authorRepo(t, parent, map[string]string{"f": "f\n"})
	writeFile(t, parent, ".git/index.lock", "", 0o644)
	runID := uuid.New()
	_, err := a.SnapshotParent(context.Background(), runID, parent)
	require.ErrorIs(t, err, ErrRepositoryLocked)
	backing, _ := a.BackingDir(runID.String())
	_, statErr := os.Stat(backing)
	assert.True(t, os.IsNotExist(statErr), "a failed snapshot leaves no backing store")
}

func TestSnapshot_UnsupportedSpecialFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes are unix-only")
	}
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	require.NoError(t, syscall.Mkfifo(filepath.Join(parent, "pipe"), 0o644))
	_, err := a.SnapshotParent(context.Background(), uuid.New(), parent)
	require.ErrorIs(t, err, engine.ErrUnsupportedSnapshotContent)
	assert.Contains(t, err.Error(), "pipe")
}

func TestSnapshot_MissingGitIsReportedBeforeAnyTaskExists(t *testing.T) {
	root := t.TempDir()
	a, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{SearchPath: t.TempDir(), StateDir: t.TempDir()})))
	require.NoError(t, err)
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "f", "f", 0o644)
	_, err = a.SnapshotParent(context.Background(), uuid.New(), parent)
	require.ErrorIs(t, err, ErrGitUnavailable)
}

func TestSnapshot_MissingParentDirIsAnError(t *testing.T) {
	a, root := newTestArea(t)
	_, err := a.SnapshotParent(context.Background(), uuid.New(), filepath.Join(root, "nope"))
	require.Error(t, err)
}

func TestForkTask_RejectsNonEmptyDirAndUnknownSnapshot(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	busy := mkdirIn(t, root, "instances/busy")
	writeFile(t, busy, "x", "x", 0o644)
	require.ErrorContains(t, a.ForkTask(ctx, handle, busy), "not empty")
	require.Error(t, a.ForkTask(ctx, uuid.New().String(), filepath.Join(root, "instances", "z")))
	require.Error(t, a.ForkTask(ctx, "not-a-handle", filepath.Join(root, "instances", "z2")))
}

func TestForkTask_RejectsDestinationsInsideBackingStore(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/parent")
	writeFile(t, parent, "payload.txt", "baseline\n", 0o644)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	baseline, err := a.ResolveRef(ctx, handle, BaselineRef)
	require.NoError(t, err)
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	existing := mkdirIn(t, a.backing, "empty-task")
	alias := filepath.Join(root, "backing-alias")
	require.NoError(t, os.Symlink(a.backing, alias))
	before := contentOf(t, a.backing)

	for _, tc := range []struct{ name, dir string }{
		{"backing root", a.backing},
		{"new child", filepath.Join(a.backing, "new-task")},
		{"existing child", existing},
		{"snapshot child", filepath.Join(backing, "new-task")},
		{"symlink alias", filepath.Join(alias, "aliased-task")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, a.ForkTask(ctx, handle, tc.dir), "inside the backing store")
			assert.Equal(t, before, contentOf(t, a.backing), "refusal must not create a destination or change backing data")
		})
	}
	// Refusals leave the immutable baseline usable by ordinary task forks.
	after, err := a.ResolveRef(ctx, handle, BaselineRef)
	require.NoError(t, err)
	assert.Equal(t, baseline, after)
	task := filepath.Join(root, ".group-backing-other", "task")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	assert.Equal(t, "file:0644:baseline\n", contentOf(t, task)["payload.txt"])
}

func TestFinalizeTask_CapturesUncommittedWorkAndParentsOnBaseline(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	authorRepo(t, parent, map[string]string{"keep.txt": "k\n", "del.txt": "d\n", ".gitignore": "*.log\n"})
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))

	writeFile(t, task, "keep.txt", "k edited, not committed\n", 0o755)
	require.NoError(t, os.Remove(filepath.Join(task, "del.txt")))
	writeFile(t, task, "new/untracked.txt", "new\n", 0o644)
	writeFile(t, task, "debug.log", "ignored but content\n", 0o644)
	writeFile(t, task, "committed.txt", "c\n", 0o644)
	author(t, task, "add", "committed.txt")
	author(t, task, "commit", "-q", "-m", "task's own commit")

	taskID := uuid.New()
	ref, err := a.FinalizeTask(ctx, handle, taskID, task)
	require.NoError(t, err)
	assert.Equal(t, "refs/arbitron/tasks/"+taskID.String(), ref)

	assert.Equal(t, gitOut(t, a, handle, "rev-parse", BaselineRef), gitOut(t, a, handle, "rev-parse", ref+"^"),
		"a task commit's only parent is the baseline")
	paths := lsTree(t, a, handle, ref)
	assert.ElementsMatch(t, []string{".gitignore", "committed.txt", "debug.log", "keep.txt", "new/untracked.txt"}, paths)
	assert.Equal(t, "k edited, not committed\n", blobAt(t, a, handle, ref, "keep.txt"))
	assert.Contains(t, gitOut(t, a, handle, "ls-tree", ref, "keep.txt"), "100755")
}

func TestFinalizeTask_EmptyDiffIsValid(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	ref, err := a.FinalizeTask(ctx, handle, uuid.New(), task)
	require.NoError(t, err)
	assert.Equal(t, gitOut(t, a, handle, "rev-parse", BaselineRef+"^{tree}"), gitOut(t, a, handle, "rev-parse", ref+"^{tree}"))
}

func TestFinalizeTask_RefinalizePreservesPublishedContent(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	id := uuid.New()
	first, err := a.FinalizeTask(ctx, handle, id, task)
	require.NoError(t, err)
	commit := gitOut(t, a, handle, "rev-parse", first)
	writeFile(t, task, "README.md", "second\n", 0o644)
	// A fresh adapter models a retry after publication but before SQL persistence.
	restarted, err := NewWorkingArea(root, WithRunner(a.git))
	require.NoError(t, err)
	ref, err := restarted.FinalizeTask(ctx, handle, id, task)
	require.NoError(t, err)
	assert.Equal(t, first, ref)
	assert.Equal(t, commit, gitOut(t, a, handle, "rev-parse", ref))
	assert.Equal(t, "hello\n", blobAt(t, a, handle, ref, "README.md"))
	require.NoError(t, os.RemoveAll(task))
	ref, err = restarted.FinalizeTask(ctx, handle, id, task)
	require.NoError(t, err, "published work survives workspace cleanup")
	assert.Equal(t, first, ref)
}

func TestFinalizeTask_ConcurrentPublicationKeepsOneCommit(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	id := uuid.New()
	const n = 4
	areas := make([]*WorkingArea, n)
	dirs := make([]string, n)
	for i := range areas {
		areas[i], err = NewWorkingArea(root, WithRunner(a.git))
		require.NoError(t, err)
		dirs[i] = filepath.Join(root, "instances", uuid.NewString())
		require.NoError(t, a.ForkTask(ctx, handle, dirs[i]))
		writeFile(t, dirs[i], "README.md", dirs[i], 0o644)
	}
	start := make(chan struct{})
	errs := make([]error, n)
	commits := make([]string, n)
	var wg sync.WaitGroup
	for i := range areas {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ref, err := areas[i].FinalizeTask(ctx, handle, id, dirs[i])
			if err == nil {
				commits[i], err = areas[i].ResolveRef(ctx, handle, ref)
			}
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()
	ref := TaskRefPrefix + id.String()
	winner := gitOut(t, a, handle, "rev-parse", ref)
	successes := 0
	for i := range areas {
		if errs[i] == nil {
			successes++
			assert.Equal(t, winner, commits[i], "every successful publisher observes the same frozen commit")
		}
		// A loser may see Git's ref lock before publication completes. Retrying
		// must reuse the winner rather than replace it with the loser's bytes.
		again, err := areas[i].FinalizeTask(ctx, handle, id, dirs[i])
		require.NoError(t, err)
		assert.Equal(t, winner, gitOut(t, a, handle, "rev-parse", again))
	}
	require.Positive(t, successes)
}

func TestFinalizeTask_ErrorCases(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	_, err = a.FinalizeTask(ctx, handle, uuid.Nil, parent)
	require.Error(t, err)
	_, err = a.FinalizeTask(ctx, handle, uuid.New(), filepath.Join(root, "missing"))
	require.Error(t, err)
	_, err = a.FinalizeTask(ctx, uuid.New().String(), uuid.New(), parent)
	require.Error(t, err)
}

func TestFinalizeTask_ConcurrentTasksGetDistinctRefs(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	const n = 4
	ids := make([]uuid.UUID, n)
	dirs := make([]string, n)
	for i := range ids {
		ids[i] = uuid.New()
		dirs[i] = filepath.Join(root, "instances", ids[i].String())
		require.NoError(t, a.ForkTask(ctx, handle, dirs[i]))
		writeFile(t, dirs[i], "README.md", "task "+ids[i].String()+"\n", 0o644)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = a.FinalizeTask(ctx, handle, ids[i], dirs[i])
		}(i)
	}
	wg.Wait()
	for i := range ids {
		require.NoError(t, errs[i])
		assert.Equal(t, "task "+ids[i].String()+"\n",
			blobAt(t, a, handle, TaskRefPrefix+ids[i].String(), "README.md"))
	}
}

func TestReleaseSnapshot_RemovesBackingAndToleratesUnknown(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	backing, _ := a.BackingDir(handle)
	require.DirExists(t, backing)
	require.NoError(t, a.ReleaseSnapshot(ctx, handle))
	assert.NoDirExists(t, backing)
	require.NoError(t, a.ReleaseSnapshot(ctx, handle), "second release is a no-op")
	require.NoError(t, a.ReleaseSnapshot(ctx, ""), "empty handle (parent had no workspace) is a no-op")
	require.NoError(t, a.ReleaseSnapshot(ctx, uuid.New().String()))
	require.Error(t, a.ReleaseSnapshot(ctx, "../../etc"))
	assert.FileExists(t, filepath.Join(parent, "README.md"), "release never touches the parent")
}
