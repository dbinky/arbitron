# Parallel Workflow Gathering — Phase 3 Task 3: Git working area — gather, merge, resolve, apply, rollback

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Complete `engine.WorkingArea` in `adapter/git`: prepare an exact pre-gather rollback snapshot and a staging area, three-way merge finalized task refs one at a time, expose and verify conflicts, record resolutions, apply the staged result to the parent's area, restore the parent exactly on rollback, and release refs — every step idempotent so a crash at any boundary resumes without applying a delta twice.
**Architecture:** `BeginGather` captures the parent's CURRENT content (P3-T2 capture: dirty/untracked/ignored/empty dirs, `.git` excluded) as `refs/arbitron/rollback/<gatherID>` — a commit whose parent is the baseline — and creates the staging area `<root>/.group-backing/<groupRunID>-gather-<gatherID>`: an ordinary git repository (`git init --template=`, objects shared from the backing store through `objects/info/alternates`) with branch `gather` at the rollback commit, so a resolver agent can use normal git there. `MergeTask` runs `git merge --no-ff` of a task commit; every task commit has the baseline as its parent, so each merge is a true three-way merge of "parent now" and "task" against "group start". Unmerged index entries are the conflict authority (plus conflict markers left in a previously conflicted file). `Apply`/`Rollback` share `syncArea`: refuse to replace a directory holding author metadata, `checkout-index -a -f` the target tree into the parent, delete files the target lacks, prune directories that became empty, recreate recorded empty dirs — never touching any `.git` entry.
**Tech Stack:** Go 1.25.5 standard library, `github.com/google/uuid`, P3-T1 `Runner`, P3-T2 content/manifest helpers, testify; real git repositories in tests.
**Spec / Design:** spec "Gathering file changes" (all-succeed combines all tasks' changes; conflicts pause gathering; downstream proceeds only after resolution); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §1 bullets 3–5 ("Merge one eligible task delta relative to the common baseline into a staging gather tree. Report clean/conflicted and unmerged paths. Commit/record resolved trees, then apply the finished result to the parent's area. Preserve an exact pre-gather rollback snapshot (including dirty/untracked state), not just a branch HEAD"; "resume after a crash … without applying a delta twice"), Error handling ("Merge/session errors abort and restore pre-gather state"), Testing edge cases (empty diff, untracked/uncommitted edits, deletion, rename/binary/mode changes, files outside repo hint, restart at each merge/apply boundary).

**Decisions settled here (pinned by tests)**
- *Staging starts at the rollback commit, not the baseline.* Anything the parent area gained after launch is preserved by Apply instead of being overwritten; merges still use the baseline as merge base. When the parent is unchanged since launch the rollback tree equals the baseline tree.
- *Conflict authority:* `ConflictPaths` = unmerged index entries (`git ls-files -u`) ∪ previously conflicted paths (recorded in `<staging>/.git/arbitron-conflicts.json` by `MergeTask`) whose text still holds both a `<<<<<<<` and a `>>>>>>>` line while the merge is in progress. A resolver must edit AND `git add` each path (the P3-T7 prompt says so); a file edited but not added stays conflicted; a file added with markers inside stays conflicted. Binary conflicts are resolved by choosing a side and adding it.
- *CommitResolution* refuses (wrapping `engine.ErrStillConflicted`, nothing changed) while `ConflictPaths` is non-empty; otherwise `git add -A --force` (new and ignored files the resolver wrote are kept) and `git commit --allow-empty --no-verify` (a merge commit when `MERGE_HEAD` exists). With no merge in progress and a clean staging tree it is a no-op returning nil (P3-T6 relies on this when replaying).
- *Idempotency:* `BeginGather` never re-takes an existing rollback ref and reuses an existing staging repository; `MergeTask` of a ref already merged (ancestor of `HEAD`) reports `Clean` without a new commit; `MergeTask` of the ref whose merge is in progress reports that merge's conflicts; a different ref while a merge is unresolved ⇒ `ErrMergeInProgress`; `Apply`/`Rollback` compute their deletions from the current disk state, so re-running after a partial run converges.
- *Author metadata safety:* `Apply`/`Rollback` never read or write inside any `.git` entry; if the target would put a file where a directory containing a `.git` entry (or an ancestor of it) is, they refuse with `ErrApplyTouchesMetadata` before writing anything.
- *Release:* `ReleaseRef` only accepts `refs/arbitron/*`; releasing a rollback ref also removes that gather's staging area (the gather is over once its rollback pin is released). P3-T10 decides when.

## Required permission-manifest integration and strict-TDD cycle

P3-T2 produces `manifest.Permissions`, `scanArea` permission capture and
`restoreAreaPermissions`. BeginGather stores current-parent permissions on its rollback
commit. Staging starts with those permissions; merging an eligible task applies its
permission DELTA relative to the common baseline, never its unchanged baseline modes.
For non-executable permission bits, process changed modes in author order (later eligible
changes win as the automatic metadata resolution rule); first-success considers only the
winner. Content/executable conflicts retain the existing Git verification/session path.
CommitResolution captures the actual staging permissions after agent/operator changes.
Apply uses the resulting permission manifest; Rollback uses the exact pre-gather manifest.
Restore modes after byte synchronization, with directory/root modes last. Resume/crash
replay uses persisted manifests rather than a new read of task workspaces.

Add/run red real-Git tests before these changes: unchanged task modes cannot overwrite a
parent's current modes; eligible chmod changes reach the parent, losing/failed tasks'
chmods do not; two eligible changed modes resolve in author order; an injected apply error
restores original file/directory/root modes along with bytes; interrupted apply then
restart still restores them. Add chmod/read-manifest failures, read-only directories,
empty diffs, symlinks and nested immediate-parent gathering. P3-T11 asserts the downstream
mode/file-read sink through `initDomain`, then runs the existing error/edge/recovery suite.


**Interfaces**
- Consumes:
  - P3-T2 `domain/engine/working_area.go`: `WorkingArea`, `GatherHandle`, `MergeOutcome`, `ErrStillConflicted`.
  - P3-T2 `adapter/git`: `(*WorkingArea).BackingDir`, `baseline`, `revParse`, `refExists`, `updateRef`, `skipInside`, `commitContent`, `checkoutCommit`, `scanArea`, `readManifest`, `writeManifest`, `manifest`, `createEmptyDirs`, `slashAncestors`, `firstLine`, `resolveDir`, `locks`, `backingAttributes`, `TaskRefPrefix`, `RollbackRefPrefix`; test helpers in `adapter/git/testhelpers_test.go` (`newTestArea`, `mkdirIn`, `writeFile`, `contentOf`, `author`, `authorRepo`).
  - P3-T1 `adapter/git/runner.go`: `Command`, `IsExitCode`, `NewRunner`, `RunnerConfig`, `ErrGitUnavailable`.
- Produces (package `git`):
  - `var _ engine.WorkingArea = (*WorkingArea)(nil)` — the adapter now satisfies the full contract port.
  - `func (a *WorkingArea) BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (engine.GatherHandle, error)` — handle fields: `SnapshotHandle`, `GatherID`, `StagingDir = <root>/.group-backing/<handle>-gather-<gatherID>`, `RollbackRef = "refs/arbitron/rollback/<gatherID>"`, `ParentDir` (symlink-resolved).
  - `func (a *WorkingArea) MergeTask(ctx context.Context, h engine.GatherHandle, taskRef string) (engine.MergeOutcome, error)` — `taskRef` must start with `refs/arbitron/tasks/`; `ConflictPaths` is non-nil (`[]string{}` when clean), sorted.
  - `func (a *WorkingArea) ConflictPaths(ctx context.Context, h engine.GatherHandle) ([]string, error)`
  - `func (a *WorkingArea) CommitResolution(ctx context.Context, h engine.GatherHandle, message string) error`
  - `func (a *WorkingArea) Apply(ctx context.Context, h engine.GatherHandle) error`
  - `func (a *WorkingArea) Rollback(ctx context.Context, h engine.GatherHandle) error`
  - `func (a *WorkingArea) ReleaseRef(ctx context.Context, snapshotHandle, ref string) error`
  - `func (a *WorkingArea) StagingDir(snapshotHandle string, gatherID uuid.UUID) (string, error)`
  - `var ErrMergeInProgress = errors.New("a merge is already in progress in the gather staging area")`
  - `var ErrApplyTouchesMetadata = errors.New("staged result would overwrite author repository metadata")`

**Wiring & data provenance**
- Composition root: **P3-T11** — the single `workingArea := gitarea.NewWorkingArea(cfg.Workspace.Path)` value (P3-T2) is handed to `engine.NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)` (P3-T6) and `engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr)` (P3-T5); the resolution bridge (P3-T8) takes no area — its agent works in `GatherHandle.StagingDir`, read back from the persisted `GatherReport.Handle`; `cmd/arbitron/group_files_e2e_test.go` asserts merged files in the parent's `{{workspace}}` and fails if the gatherer is not wired to this adapter.
- Producers of every input:
  - `snapshotHandle` — `instance.GroupRun.WorkspaceSnapshot` (P2-T1/T3), written by P2-T8 `GroupLauncher.Launch` from P3-T2 `SnapshotParent`.
  - `gatherID` — generated by P3-T6 `FileGatherer` and persisted inside `instance.GatherReport.Handle` (P3-T4) so a restart reuses it.
  - `parentDir` — the parent's `workspace` variable (`domain/engine/workspace.go:17-43` `Engine.prepareWorkspace` → `WorkspaceManager.EnsureInstanceDir`), read by P3-T6.
  - `taskRef` — `instance.GroupTask.FinalizedRef` (P3-T4 storage), written by P3-T5 `GroupFileFinalizer` from P3-T2 `FinalizeTask`.
  - `h` — the handle `BeginGather` returned, round-tripped through `GatherReport.Handle` (P3-T6 `GatherHandleFromReport`).
  - staging-area edits — the resolver agent (P3-T8) working in `h.StagingDir`.
- Provenance probes: `TestGather_HandleAndRefValidation` fails if a forged handle (staging dir or rollback ref not derived from this area) is accepted; `TestGather_MissingGitFailsWithoutTouchingParent` fails if Apply/Rollback do not go through the area's runner; every gather test asserts the SINK — `contentOf(parentDir)` — not intermediate state.

### Task 3: Merge, resolve, apply, rollback, release
**Files:**
- Create: `adapter/git/working_area_merge.go`
- Test:   `adapter/git/working_area_merge_test.go` (uses the P3-T2 helpers in `adapter/git/testhelpers_test.go`)

No existing file is modified.

- [x] Step 1: Write the failing tests. Create `adapter/git/working_area_merge_test.go`:

```go
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
```

- [x] Step 2: Run them; confirm they fail to compile.

Confirmed undefined BeginGather/MergeTask/Apply with the planned real-Git tests and added permission-delta, read-only/symlink, missing-manifest, interrupted/error apply/restart rollback and nested-immediate-parent sink tests, before implementation.

```bash
go test ./adapter/git/ -count=1 -run 'TestGather|TestReleaseRef'
```
Expected:
```
# github.com/arbitron/arbitron/adapter/git [github.com/arbitron/arbitron/adapter/git.test]
adapter/git/working_area_merge_test.go:53:16: f.a.BeginGather undefined (type *WorkingArea has no field or method BeginGather)
adapter/git/working_area_merge_test.go:94:19: f.a.MergeTask undefined (type *WorkingArea has no field or method MergeTask)
adapter/git/working_area_merge_test.go:99:25: f.a.Apply undefined (type *WorkingArea has no field or method Apply)
...
FAIL	github.com/arbitron/arbitron/adapter/git [build failed]
```

- [x] Step 3: Implement. Create `adapter/git/working_area_merge.go`:

Implemented the adapter and required permission-manifest integration. A durable merge journal and manifest-before-HEAD publication close replay boundaries; Git supplies three-way content/executable decisions while ordinary permission deltas resolve in merge order. Additional real-Git sink tests cover parent drift, deleted task workspaces, precise executable bits, nested empty-directory deletion, read-only directories, missing/incomplete manifests, permission-restoration failure, metadata-only ancestors, symlink safety, file/directory transitions, workspace-root backing exclusion, rebound handles, and restarts at merge/commit/apply boundaries. No existing production file was modified.

```go
package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ErrMergeInProgress reports a staging area whose previous merge has not been
// resolved and committed yet.
var ErrMergeInProgress = errors.New("a merge is already in progress in the gather staging area")

// ErrApplyTouchesMetadata reports a staged result that would replace a
// directory holding author repository metadata with a file.
var ErrApplyTouchesMetadata = errors.New("staged result would overwrite author repository metadata")

const (
	stagingBranch = "refs/heads/gather"
	conflictsFile = "arbitron-conflicts.json"
)

var _ engine.WorkingArea = (*WorkingArea)(nil)

// StagingDir returns the staging work tree of one gather of a snapshot.
func (a *WorkingArea) StagingDir(snapshotHandle string, gatherID uuid.UUID) (string, error) {
	if _, err := a.BackingDir(snapshotHandle); err != nil {
		return "", err
	}
	if gatherID == uuid.Nil {
		return "", errors.New("git working area: gather id is required")
	}
	return filepath.Join(a.backing, snapshotHandle+"-gather-"+gatherID.String()), nil
}

// BeginGather snapshots parentDir's exact current content (dirty, untracked,
// ignored, empty dirs; author .git excluded) as refs/arbitron/rollback/<id>,
// a commit whose parent is the baseline, and prepares the staging area: an
// ordinary git repository at the staging dir, sharing the backing store's
// objects through alternates, with branch "gather" at the rollback commit.
//
// Starting staging at the rollback commit (not the baseline) means anything
// the parent area gained since the group launched is kept; every task commit
// still shares the baseline as merge base, so each merge is a true 3-way
// merge of "parent now" and "task" against "group start".
//
// Idempotent per gatherID: an existing rollback ref is never re-taken (a
// restart after a partial Apply must still roll back to pre-gather content),
// and an existing staging repository is reused as is (merge progress and
// in-flight resolutions survive).
func (a *WorkingArea) BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (engine.GatherHandle, error) {
	staging, err := a.StagingDir(snapshotHandle, gatherID)
	if err != nil {
		return engine.GatherHandle{}, err
	}
	unlock := a.locks.Lock(snapshotHandle)
	defer unlock()
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	parent, err := resolveDir(parentDir)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	h := engine.GatherHandle{
		SnapshotHandle: snapshotHandle,
		GatherID:       gatherID,
		StagingDir:     staging,
		RollbackRef:    RollbackRefPrefix + gatherID.String(),
		ParentDir:      parent,
	}

	exists, err := a.refExists(ctx, backing, h.RollbackRef)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	if !exists {
		content, err := scanArea(parent, a.skipInside(parent))
		if err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback snapshot: %w", err)
		}
		commit, err := a.commitContent(ctx, backing, parent, content, baseline, "arbitron: pre-gather "+gatherID.String())
		if err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback snapshot: %w", err)
		}
		if err := writeManifest(backing, commit, manifest{Root: parent, EmptyDirs: content.EmptyDirs, Permissions: content.Permissions}); err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback manifest: %w", err)
		}
		if err := a.updateRef(ctx, backing, h.RollbackRef, commit); err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
		}
	}
	rollback, err := a.revParse(ctx, backing, h.RollbackRef)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	if err := a.ensureStaging(ctx, backing, staging, rollback); err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: staging: %w", err)
	}
	return h, nil
}

// ensureStaging creates (or keeps) the staging repository.
func (a *WorkingArea) ensureStaging(ctx context.Context, backing, staging, rollback string) error {
	gitDir := filepath.Join(staging, ".git")
	if _, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "-q", stagingBranch}, GitDir: gitDir}); err == nil {
		return nil
	}
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"init", "-q", "--template=", staging}}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"),
		[]byte(filepath.Join(backing, "objects")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "attributes"), []byte(backingAttributes), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"update-ref", stagingBranch, rollback},
		{"symbolic-ref", "HEAD", stagingBranch},
		{"reset", "-q", "--hard", stagingBranch},
	} {
		if _, err := a.git.Run(ctx, Command{Args: args, Dir: staging}); err != nil {
			return err
		}
	}
	return nil
}

// MergeTask merges one finalized task ref into the staging branch with
// "git merge --no-ff". Clean merges commit immediately. On conflict the
// unmerged index entries and conflict markers are left in the staging work
// tree and the conflicted paths are returned (and recorded so later marker
// checks know which files to inspect).
func (a *WorkingArea) MergeTask(ctx context.Context, h engine.GatherHandle, taskRef string) (engine.MergeOutcome, error) {
	backing, staging, err := a.gatherDirs(h)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if !strings.HasPrefix(taskRef, TaskRefPrefix) {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %q is not a finalized task ref", taskRef)
	}
	commit, err := a.revParse(ctx, backing, taskRef)
	if err != nil {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %w", err)
	}
	gitDir := filepath.Join(staging, ".git")

	if mergeHead, ok := readMergeHead(gitDir); ok {
		if mergeHead != commit {
			return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task %s: %w", taskRef, ErrMergeInProgress)
		}
		paths, err := a.ConflictPaths(ctx, h)
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		return engine.MergeOutcome{Clean: len(paths) == 0, ConflictPaths: paths}, nil
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"merge-base", "--is-ancestor", commit, "HEAD"}, Dir: staging}); err == nil {
		return engine.MergeOutcome{Clean: true, ConflictPaths: []string{}}, nil
	} else if !IsExitCode(err, 1) {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %w", err)
	}

	_, mergeErr := a.git.Run(ctx, Command{
		Args: []string{"merge", "--no-ff", "--no-edit", "--no-verify", "-q", "-m", "arbitron: gather " + taskRef, commit},
		Dir:  staging,
	})
	if mergeErr == nil {
		return engine.MergeOutcome{Clean: true, ConflictPaths: []string{}}, nil
	}
	if !IsExitCode(mergeErr, 1) {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task %s: %w", taskRef, mergeErr)
	}
	unmerged, err := a.unmergedPaths(ctx, staging)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if len(unmerged) == 0 {
		_, _ = a.git.Run(ctx, Command{Args: []string{"merge", "--abort"}, Dir: staging})
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task %s: %w", taskRef, mergeErr)
	}
	if err := writeJSON(filepath.Join(gitDir, conflictsFile), unmerged); err != nil {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: record conflicts: %w", err)
	}
	return engine.MergeOutcome{Clean: false, ConflictPaths: unmerged}, nil
}

// ConflictPaths returns the paths still conflicted in the staging area:
// unmerged index entries (the authority), plus any path of the current merge's
// recorded conflicts whose file still contains conflict markers even though
// it was marked resolved.
func (a *WorkingArea) ConflictPaths(ctx context.Context, h engine.GatherHandle) ([]string, error) {
	_, staging, err := a.gatherDirs(h)
	if err != nil {
		return nil, err
	}
	paths, err := a.unmergedPaths(ctx, staging)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	if _, inMerge := readMergeHead(filepath.Join(staging, ".git")); inMerge {
		var recorded []string
		if err := readJSON(filepath.Join(staging, ".git", conflictsFile), &recorded); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, p := range recorded {
			if !set[p] && hasConflictMarkers(filepath.Join(staging, filepath.FromSlash(p))) {
				set[p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// CommitResolution stages the whole staging work tree ("git add -A --force",
// so new or ignored files the resolver wrote are kept) and commits it,
// completing the in-progress merge if there is one. It refuses, changing
// nothing, while ConflictPaths is non-empty. With no merge in progress and an
// unchanged staging tree it is a no-op returning nil (safe to replay).
func (a *WorkingArea) CommitResolution(ctx context.Context, h engine.GatherHandle, message string) error {
	_, staging, err := a.gatherDirs(h)
	if err != nil {
		return err
	}
	if _, inMerge := readMergeHead(filepath.Join(staging, ".git")); !inMerge {
		res, err := a.git.Run(ctx, Command{Args: []string{"status", "--porcelain", "--untracked-files=all", "--ignored=matching"}, Dir: staging})
		if err != nil {
			return fmt.Errorf("git working area: commit resolution: %w", err)
		}
		if strings.TrimSpace(res.Stdout) == "" {
			return nil
		}
	}
	paths, err := a.ConflictPaths(ctx, h)
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		return fmt.Errorf("git working area: commit resolution: %w: %s", engine.ErrStillConflicted, strings.Join(paths, ", "))
	}
	if strings.TrimSpace(message) == "" {
		message = "arbitron: resolve gather conflicts"
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"add", "-A", "--force"}, Dir: staging}); err != nil {
		return fmt.Errorf("git working area: commit resolution: %w", err)
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"commit", "-q", "--no-verify", "--allow-empty", "-m", message}, Dir: staging}); err != nil {
		return fmt.Errorf("git working area: commit resolution: %w", err)
	}
	_ = os.Remove(filepath.Join(staging, ".git", conflictsFile))
	return nil
}

// Apply makes h.ParentDir equal the staging branch's committed result. It
// refuses while a merge is unresolved.
func (a *WorkingArea) Apply(ctx context.Context, h engine.GatherHandle) error {
	_, staging, err := a.gatherDirs(h)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(staging, ".git")
	if _, inMerge := readMergeHead(gitDir); inMerge {
		return fmt.Errorf("git working area: apply: %w", ErrMergeInProgress)
	}
	target, err := a.revParse(ctx, gitDir, stagingBranch)
	if err != nil {
		return fmt.Errorf("git working area: apply: %w", err)
	}
	if err := a.syncArea(ctx, gitDir, target, h.ParentDir, nil); err != nil {
		return fmt.Errorf("git working area: apply: %w", err)
	}
	return nil
}

// Rollback restores h.ParentDir to the content recorded by BeginGather,
// including empty directories. It does not need the staging area.
func (a *WorkingArea) Rollback(ctx context.Context, h engine.GatherHandle) error {
	backing, err := a.BackingDir(h.SnapshotHandle)
	if err != nil {
		return err
	}
	if h.RollbackRef != RollbackRefPrefix+h.GatherID.String() {
		return fmt.Errorf("git working area: rollback: handle rollback ref %q does not belong to gather %s", h.RollbackRef, h.GatherID)
	}
	target, err := a.revParse(ctx, backing, h.RollbackRef)
	if err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	m, err := readManifest(backing, target)
	if err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	if err := a.syncArea(ctx, backing, target, h.ParentDir, m.EmptyDirs); err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	return nil
}

// ReleaseRef deletes one refs/arbitron/* ref. Releasing a rollback ref also
// removes that gather's staging area. Unknown handle or ref: no error.
func (a *WorkingArea) ReleaseRef(ctx context.Context, snapshotHandle, ref string) error {
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(ref, "refs/arbitron/") {
		return fmt.Errorf("git working area: release ref: %q is not an arbitron ref", ref)
	}
	if _, err := os.Stat(backing); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	exists, err := a.refExists(ctx, backing, ref)
	if err != nil {
		return fmt.Errorf("git working area: release ref: %w", err)
	}
	if exists {
		if _, err := a.git.Run(ctx, Command{Args: []string{"update-ref", "-d", ref}, GitDir: backing}); err != nil {
			return fmt.Errorf("git working area: release ref: %w", err)
		}
	}
	if strings.HasPrefix(ref, RollbackRefPrefix) {
		if id, err := uuid.Parse(strings.TrimPrefix(ref, RollbackRefPrefix)); err == nil {
			if staging, err := a.StagingDir(snapshotHandle, id); err == nil {
				if err := os.RemoveAll(staging); err != nil {
					return fmt.Errorf("git working area: release staging: %w", err)
				}
			}
		}
	}
	return nil
}

// syncArea makes dir hold exactly commit's files (plus keepEmpty dirs),
// leaving every ".git" entry alone. Algorithm, safe to re-run after a crash:
//  1. refuse if the commit puts a file where a directory holding author
//     metadata (or one of its ancestors) is;
//  2. write every file of the commit (checkout-index -a -f: creates, rewrites,
//     fixes modes, replaces blocking files/symlinks/directories);
//  3. delete every file/symlink present now but absent from the commit;
//  4. remove directories that step 3 emptied and the commit does not need;
//  5. recreate keepEmpty directories.
func (a *WorkingArea) syncArea(ctx context.Context, gitDir, commit, dir string, keepEmpty []string) error {
	root, err := resolveDir(dir)
	if err != nil {
		return err
	}
	targetFiles, targetDirs, err := a.treeListing(ctx, gitDir, commit)
	if err != nil {
		return err
	}
	current, err := scanArea(root, a.skipInside(root))
	if err != nil {
		return err
	}
	for _, ge := range current.GitEntries {
		for _, anc := range slashAncestors(ge.Rel) {
			if targetFiles[anc] {
				return fmt.Errorf("%w: %q would replace the directory holding %q", ErrApplyTouchesMetadata, anc, ge.Rel)
			}
		}
	}
	if err := a.checkoutCommit(ctx, gitDir, filepath.Join(gitDir, "arbitron", "tmp"), commit, root); err != nil {
		return err
	}
	var removedFrom []string
	for i := len(current.Files) - 1; i >= 0; i-- {
		f := current.Files[i]
		if targetFiles[f.Rel] {
			continue
		}
		if err := os.Remove(f.Abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", f.Rel, err)
		}
		removedFrom = append(removedFrom, slashAncestors(f.Rel)...)
	}
	keep := map[string]bool{}
	for _, k := range keepEmpty {
		keep[k] = true
	}
	sort.Slice(removedFrom, func(i, j int) bool { return len(removedFrom[i]) > len(removedFrom[j]) })
	for _, d := range removedFrom {
		if targetDirs[d] || keep[d] {
			continue
		}
		// os.Remove only removes EMPTY directories: anything still holding a
		// file, a .git entry or a kept directory stays.
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(d)))
	}
	return createEmptyDirs(root, keepEmpty)
}

// treeListing returns the commit's file paths and every directory they need.
func (a *WorkingArea) treeListing(ctx context.Context, gitDir, commit string) (map[string]bool, map[string]bool, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"ls-tree", "-r", "-z", "--full-tree", "--name-only", commit}, GitDir: gitDir})
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", commit, err)
	}
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, p := range strings.Split(res.Stdout, "\x00") {
		if p == "" {
			continue
		}
		files[p] = true
		for _, d := range slashAncestors(p) {
			dirs[d] = true
		}
	}
	return files, dirs, nil
}

func (a *WorkingArea) unmergedPaths(ctx context.Context, staging string) ([]string, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"ls-files", "-u", "-z"}, Dir: staging})
	if err != nil {
		return nil, fmt.Errorf("git working area: list conflicts: %w", err)
	}
	set := map[string]bool{}
	for _, rec := range strings.Split(res.Stdout, "\x00") {
		if _, p, ok := strings.Cut(rec, "\t"); ok && p != "" {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// gatherDirs validates a handle against this area and returns its backing
// and staging directories.
func (a *WorkingArea) gatherDirs(h engine.GatherHandle) (string, string, error) {
	backing, err := a.BackingDir(h.SnapshotHandle)
	if err != nil {
		return "", "", err
	}
	staging, err := a.StagingDir(h.SnapshotHandle, h.GatherID)
	if err != nil {
		return "", "", err
	}
	if h.StagingDir != staging {
		return "", "", fmt.Errorf("git working area: gather handle staging dir %q does not belong to this area", h.StagingDir)
	}
	return backing, staging, nil
}

func readMergeHead(gitDir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "MERGE_HEAD"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(firstLine(string(raw))), true
}

// hasConflictMarkers reports whether a text file still holds both an opening
// and a closing conflict marker line. Binary and missing files report false:
// for them the unmerged index entry is the only signal.
func hasConflictMarkers(p string) bool {
	raw, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	head := raw
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	var open, closed bool
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), len(raw)+1)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "<<<<<<<") {
			open = true
		}
		if open && strings.HasPrefix(line, ">>>>>>>") {
			closed = true
		}
	}
	return open && closed
}

func writeJSON(p string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(p, raw, 0o600)
}

func readJSON(p string, v any) error {
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
```

- [x] Step 4: Run the whole package (P3-T1/T2 tests must stay green), the race detector, gofmt and vet.

Completion pass after P3-T11 (`62faa02`): the finalizer, gatherer and production wiring now exist. `initDomain` constructs one Git WorkingArea and feeds it to the launcher, finalizer, gatherer and recovery/pruner. Real tool-call writes produce finalized refs, and downstream tool-call reads assert persisted parent bytes/modes. No new implementation was needed; the task's earlier red/green steps remain complete.

Verification: `go test ./adapter/git/ -count=1 -v` passes the complete package (68.624s). The first full adapter race run timed out in the existing P3-T2 `TestForkTask_InterruptedCheckoutCleansPartialContent` cancellation wait; the complete retry passes (62.465s), as does the final post-review run (66.840s). `go test ./cmd/arbitron/ -run '^TestGroupFilesE2E_' -race -count=1` passes all production-root file tests before and after review (45.698s / 44.133s), including permissions, conflict resolution, apply-error rollback, abandon, nested gathering, restart and cleanup. Formatting, vet and diff checks are clean.

Completion self-review, in the required order:
1. Spec compliance: compared the implementation with this task's merge/conflict/apply/rollback/release contracts, permission-manifest requirements and Phase 3 design. Eligibility and composition remain in their assigned tasks. No missing behavior or unrequested feature was found; no production change was necessary.
2. Anti-green-theater: inspected real-root construction and data flow plus the parent-variable file/mode sinks. Temporarily bypassed only Apply's byte/permission synchronization; the production-root all-succeed test failed on missing alpha/beta/deep content and the unapplied deletion. Restored Apply exactly and reran every root file test with the race detector, green. The test therefore cannot pass with inert application. No mutation remains.
3. Code quality: reviewed the adapter alongside neighboring content/permission helpers, immutable manifest publication and recovery paths. No cleanup was needed. `gofmt -l adapter/git/`, `go vet ./adapter/git/`, `git diff --check` and a production-file diff check print nothing. Reran the whole adapter race suite and root file tests after review, green.

> RESOLVED (P3-T5/P3-T6/P3-T11; historical blocker follows): The adapter verification is green, but the mission's wired-and-fed completion gate cannot pass until the planned P3-T5 finalizer, P3-T6 gatherer and P3-T11 composition wiring land. `cmd/arbitron/main.go:990-1002` still constructs `filesystem.NewGroupWorkspace` and enables only `GroupSupportResults`; no production Git WorkingArea, file finalizer, or file gatherer exists there. Adapter tests use real SnapshotParent/ForkTask/FinalizeTask producers and assert parent bytes/modes, but are not a substitute for P3-T11's initDomain downstream sink test. Leave this final completion gate unchecked and skip re-failing it on later passes while those dependencies remain absent; proceed to independent P3-T4 steps next.

Local verification: the complete adapter package, including P3-T1/T2 tests, passes normally and with `-race`. Final post-review runs: `go test ./adapter/git/ -count=1 -v` (88.937s), `go test ./adapter/git/ -count=1 -race` (82.541s); `go vet ./adapter/git/`, `gofmt -l adapter/git/`, and `git diff --check` print nothing.

Self-review, in the required order:
1. Spec compliance: compared the adapter to the task/design contracts and neighboring content/permission helpers. Added red→green regressions for nested empty-directory deletion, incomplete manifests before mutation, precise executable bits, workspace-root exclusion, metadata-only ancestor modes, handle binding, and resolution replay after HEAD publication. Kept eligibility policy and runtime composition in their explicitly planned tasks; did not enable file mode early or add options.
2. Anti-green-theater: real Git baseline/fork/finalize refs feed every merge; assertions read parent bytes and exact file/directory/root modes. Tests remove task directories before restart, proving that immutable refs/manifests feed replay. Temporarily removed production permission restoration: mode sink assertions failed and the restoration-error test incorrectly returned success. Restored production code. These tests do not establish runtime eligibility or root wiring; that remaining gap is the blocker above, not a claimed integration pass.
3. Code quality: reviewed neighboring WorkingArea/content/Runner conventions, kept author metadata and symlink targets outside mutation, used atomic side-record replacement and typed map/slice comparisons, corrected synchronization comments, and retained only the requested adapter/recovery behavior. No mutation remains.

```bash
go test ./adapter/git/ -count=1 -v 2>&1 | grep -E '^(--- (PASS|FAIL): (TestGather|TestReleaseRef)|ok|FAIL)'
go test ./adapter/git/ -count=1 -race
gofmt -l adapter/git/
go vet ./adapter/git/
```
Expected (`gofmt -l` and `go vet` print nothing):
```
--- PASS: TestGather_TwoCleanMergesInOrderPlainDirectory (0.XXs)
--- PASS: TestGather_DeletionRenameBinaryModeAndSymlinkChanges (0.XXs)
--- PASS: TestGather_RenameInOneTaskEditInAnotherMergesBoth (0.XXs)
--- PASS: TestGather_ConflictResolveThenNextMerge (0.XXs)
--- PASS: TestGather_StagedFileWithMarkersIsStillConflicted (0.XXs)
--- PASS: TestGather_EditedButNotMarkedResolvedIsStillConflicted (0.XXs)
--- PASS: TestGather_BinaryConflictResolvedByChoosingASide (0.XXs)
--- PASS: TestGather_EmptyDiffTaskLeavesParentIdentical (0.XXs)
--- PASS: TestGather_MergeTaskIsIdempotentForAMergedRef (0.XXs)
--- PASS: TestGather_ParentDriftSinceSnapshotIsKept (0.XXs)
--- PASS: TestGather_AuthorRepositoryMetadataUntouchedAndDirtyStateRestored (0.XXs)
--- PASS: TestGather_FilesOutsideRepoHintAndNestedRepository (0.XXs)
--- PASS: TestGather_ApplyRefusesToReplaceMetadataDirectory (0.XXs)
--- PASS: TestGather_ApplyIsIdempotentAfterPartialApply (0.XXs)
--- PASS: TestGather_BeginGatherIsIdempotentAndNeverRetakesRollback (0.XXs)
--- PASS: TestGather_RollbackAfterStagingRemovedAndIdempotent (0.XXs)
--- PASS: TestGather_MissingGitFailsWithoutTouchingParent (0.XXs)
--- PASS: TestGather_HandleAndRefValidation (0.XXs)
--- PASS: TestReleaseRef_DeletesRefsAndRollbackStaging (0.XXs)
--- PASS: TestGather_CommitResolutionWithoutMergeIsANoOp (0.XXs)
ok  	github.com/arbitron/arbitron/adapter/git	2X.XXXs
```

- [x] Step 5: Commit.

The initial commit contained the unblocked adapter implementation, real-Git regression tests, verification and precise runtime-wiring blocker. This completion pass closes Step 4 after its production composition/sink dependencies landed and commits the verification and ordered self-review evidence only.

```bash
git add adapter/git/working_area_merge.go adapter/git/working_area_merge_test.go
git commit -m "feat(git): gather staging, three-way task merges, conflict verification, apply and exact rollback" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
