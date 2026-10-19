# Parallel Workflow Gathering — Phase 3 Task 2: Git working area — snapshot, fork, finalize, release (P3-A)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Define the domain `WorkingArea` port and implement its snapshot/fork/finalize/release half in `adapter/git`, so a group can start every task from an immutable copy of the parent's FULL working area (plain directories, files outside any repository, dirty/untracked/ignored content, author repositories and linked worktrees) and record each successful task's files as an immutable ref — without ever writing to, or pointing back at, an author repository.
**Architecture:** One private bare backing repository per group snapshot at `<workspace root>/.group-backing/<groupRunID>.git` (handle = the group-run id). Content is recorded with git plumbing from a Go directory walk (`hash-object -w --no-filters --stdin-paths` → `update-index -z --index-info` into a per-call temp index → `write-tree` → `commit-tree`), never with `git add`, and every `.git` entry at any depth is excluded; a per-commit manifest beside the repository keeps what git cannot store (empty directories) and an immutable metadata-tree handle reproduced through P2-T17's filesystem helper. Fork = `read-tree` + `checkout-index -a -f` into the task dir, then independent author metadata: `.git` directories are copied, in-area worktree pointers are re-rooted into the task copy, and pointers to repositories OUTSIDE the area become standalone repositories (common dir copied without `worktrees/`, worktree HEAD and index copied). Finalize = the same capture of the task dir as `refs/arbitron/tasks/<taskInstanceID>`, a commit whose single parent is the baseline.
**Tech Stack:** Go 1.25.5 standard library (`io/fs`, `path/filepath`, `encoding/json`, `sync`), `github.com/google/uuid`, P3-T1 `Runner`, testify; real temporary git repositories and plain directories in tests.
**Spec / Design:** spec "Gathering file changes", "Each task starts with … its own isolated working area"; `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §1 bullets 1–2 and §5, Data Provenance row "Full-area baselines, forks, immutable task refs including uncommitted edits" (**P3-A**, resolved here), Error handling ("missing Git binary, unsupported snapshot content or locked index produces actionable errors before launching tasks where possible. No declared repo is valid"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §2 ("No hard links, writable symlinks, or Git metadata pointing back to the parent's mutable worktree").

**Backing strategy decisions (P3-A, settled here and pinned by the tests)**
1. *Where:* `<root>/.group-backing/<groupRunID>.git` (bare, created with `git init --bare --template=` so no sample hooks/description exist), sibling of `<root>/instances/` — never inside an author directory. `root` is the workspace root (`cfg.Workspace.Path`), symlink-resolved. A parent area that IS the root never captures `.group-backing` (`skipInside`).
2. *What is content:* every regular file (mode `100644`, or `100755` when any exec bit is set), every symlink (`120000`, target stored as the blob), at any depth, whether tracked, modified, staged, untracked or ignored by any `.gitignore`. Excluded: any entry named `.git` (case-insensitively — git itself refuses such path components) at any depth, directory or file. Named pipes, sockets and devices fail with `engine.ErrUnsupportedSnapshotContent` naming the path. Empty directories are recorded in the manifest and recreated on fork.
3. *Why plumbing, not `git add -A --force` + `info/exclude`:* `git add` turns any nested author repository into a gitlink (mode 160000) and records NONE of its working-tree files, which would silently drop the most common layout (`{{workspace}}/src` cloned by `samples/feature-pipeline/scripts/init-workspace.sh`). The walk records them as ordinary files. `--no-filters` plus `info/attributes` (`* -text -eol -ident -filter -working-tree-encoding`) keep bytes exact despite any author `.gitattributes`.
4. *Author metadata reproduction:* capture a single immutable tree at
   `<backing>/arbitron/metadata-tree` through P2-T17's `CopyGroupGitMetadata`. The manifest
   stores `metadata[{path:".",kind:"metadata-tree",copy:"arbitron/metadata-tree"}]`.
   The helper copies `.git` directories independently, re-roots in-area pointers, and
   materializes external gitdirs as standalone copies while preserving each worktree's
   HEAD and index/staged state. Forks reuse that same helper from the immutable tree to
   each task directory; they never consult the original parent. Locked indexes wrap
   the shared `ErrGroupRepositoryLocked` / `git.ErrRepositoryLocked` sentinel.
5. *Finalize:* captures the task dir exactly like the snapshot (task `.git` metadata excluded — commits a task made inside its repository copy are captured as working-tree content, never as history), `commit-tree -p <baseline>`, `update-ref refs/arbitron/tasks/<id>`. An unchanged task yields a commit whose tree equals the baseline tree (empty diff is valid). Re-finalizing replaces the ref. Each call uses its own temp index under `<backing>/arbitron/tmp`, so concurrent task finalizations are safe.
6. *Release:* `ReleaseSnapshot` deletes the backing repository and every `<handle>-gather-*` staging area; empty/unknown handle ⇒ nil; a non-UUID handle ⇒ error (it would be used in a path). WHEN to release is decided by P3-T10's cleanup, never by this adapter.
7. *Permissions and empty directories:* the manifest records full ordinary file/directory permission bits, because Git only stores executability. Restore them after checkout on fork/apply/rollback. Empty directories created BY TASKS are not gathered as file changes; parent empty directories are kept and restored.

## Shared metadata-copy prerequisite

P2-T17 produces `adapter/filesystem/group_git_metadata.go:CopyGroupGitMetadata` and
requires independent copies of linked-worktree metadata even for results-only groups.
Reuse that helper in `metadata.go`; do not maintain a second pointer/commondir/alternates
normalizer here. This task owns the content manifest/plumbing, storage of the immutable
metadata source and reproduction calls. Preserve worktree index/staged state, including
external linked worktrees, instead of rebuilding it from HEAD and losing staged changes.
Existing real-repository tests must compare both content and index/ref isolation.


## Metadata helper error vocabulary

The shared helper must expose `filesystem.ErrGroupRepositoryLocked` and wrap it when a
source index lock is found. This task defines `git.ErrRepositoryLocked` as an alias to
that sentinel, preserving P3-T10's `errors.Is` preflight contract. P2-T17 owns the sentinel
producer; the helper must inspect linked-worktree and common metadata lock locations.
The old standalone normalizer is removed from the implementation block; only pure
pointer/lock helpers still referenced by preflight/tests remain.


## Required permissions producer and strict-TDD cycle

The design requires starting copies and rollback to preserve permissions. Add
`Permissions map[string]uint32` (`json:"permissions"`) to the private `manifest` and
`areaContent` types, populated by `scanArea` from `Lstat().Mode().Perm()` for regular
files/directories, including the root under key `"."`; exclude symlinks and `.git`
metadata. Persist it with baseline, finalized-task and rollback commits. Produce
`restoreAreaPermissions(ctx context.Context, root string, permissions map[string]uint32)
error` in `adapter/git/content.go`: validate contained paths, never follow symlinks,
chmod files then deepest directories and root LAST so read-only modes cannot prevent
copying. ForkTask calls it after content/metadata/empty-dir reproduction. Missing/malformed
permission manifests are actionable errors for these new group snapshots, never ignored.

Before implementing, write/run red real-adapter tests: a 0600 file, an executable, a
read-only directory and a nondefault root mode arrive identically in TWO forks, including
plain directories and nested Git/worktree layouts. Mutating modes in one fork changes
neither parent nor sibling. Include capture/chmod errors and interrupted fork cleanup.
The composition test in P3-T11 reads actual task modes through a script and persists
that result downstream. It fails when permission capture or restore is bypassed.
P3-T3 consumes these same manifests to preserve exact rollback modes and apply gathered
permission changes; executable-bit conflicts still follow Git's merge behavior.


**Interfaces**
- Consumes:
  - P2-T5 `domain/engine/group_workspace.go`: `type GroupWorkspace interface { SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error); ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error; ReleaseSnapshot(ctx context.Context, snapshotHandle string) error }`, `var ErrUnsupportedSnapshotContent`.
  - P3-T1 `adapter/git/runner.go`: `Runner`, `NewRunner`, `RunnerConfig`, `Command`, `Result`, `IsExitCode`, `ErrGitUnavailable`.
- Produces:
  - `domain/engine/working_area.go` (the Phase 3 port, exactly the contract; full declarations in Step 4):
    - `type WorkingArea interface` = `GroupWorkspace` + `FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error)`, `BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (GatherHandle, error)`, `MergeTask(ctx context.Context, h GatherHandle, taskRef string) (MergeOutcome, error)`, `ConflictPaths(ctx context.Context, h GatherHandle) ([]string, error)`, `CommitResolution(ctx context.Context, h GatherHandle, message string) error`, `Apply(ctx context.Context, h GatherHandle) error`, `Rollback(ctx context.Context, h GatherHandle) error`, `ReleaseRef(ctx context.Context, snapshotHandle, ref string) error`
    - `type GatherHandle struct { SnapshotHandle string; GatherID uuid.UUID; StagingDir string; RollbackRef string; ParentDir string }` with json tags `snapshotHandle`, `gatherId`, `stagingDir`, `rollbackRef`, `parentDir`
    - `type MergeOutcome struct { Clean bool; ConflictPaths []string }` with json tags `clean`, `conflictPaths`
    - refinement (additive): `var ErrStillConflicted = errors.New("working area still has unresolved merge conflicts")` — returned (wrapped) by `CommitResolution` (P3-T3) so the domain gatherer can detect it without importing the adapter.
  - `adapter/git` (package `git`):
    - `type WorkingArea struct{ … }` — `var _ engine.GroupWorkspace = (*WorkingArea)(nil)` here; P3-T3 adds `var _ engine.WorkingArea = (*WorkingArea)(nil)`.
    - `type Option func(*WorkingArea) error`; `func WithRunner(r *Runner) Option`
    - `func NewWorkingArea(root string, opts ...Option) (*WorkingArea, error)` — `gitarea.NewWorkingArea(cfg.Workspace.Path)` is the composition-root call (P3-T6 already cites it with one argument).
    - `func (a *WorkingArea) SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error)` — handle = `groupRunID.String()`; replaces a previous snapshot of the same run.
    - `func (a *WorkingArea) ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error` — taskDir created if missing; must be empty.
    - `func (a *WorkingArea) FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error)` — returns `"refs/arbitron/tasks/<taskInstanceID>"`.
    - `func (a *WorkingArea) ReleaseSnapshot(ctx context.Context, snapshotHandle string) error`
    - `func (a *WorkingArea) BackingDir(handle string) (string, error)`; `func (a *WorkingArea) ResolveRef(ctx context.Context, snapshotHandle, ref string) (string, error)`
    - `const BaselineRef = "refs/arbitron/baseline"`, `const TaskRefPrefix = "refs/arbitron/tasks/"`, `const RollbackRefPrefix = "refs/arbitron/rollback/"`
    - `var ErrRepositoryLocked = filesystem.ErrGroupRepositoryLocked`

**Wiring & data provenance**
- Composition root: **P3-T11** (`cmd/arbitron/main.go:initDomain`, beside `wsMgr, err := filesystem.NewWorkspaceManager(cfg.Workspace.Path)` at main.go:814): `workingArea, err := gitarea.NewWorkingArea(cfg.Workspace.Path)`, then `groupLauncher.SetFileWorkingArea(workingArea)` (P3-T10) gives files-mode groups this Git-backed area while results-only groups keep P2-T5's `filesystem.NewGroupWorkspace` (so results mode never needs git), and the same value feeds `engine.NewGroupFileFinalizer` (P3-T5) and `engine.NewGroupGatherer` (P3-T6). Its `group_files_e2e_test.go` asserts files land in the parent's workspace, which fails if the line is deleted.
- Producers of every input:
  - `root` — `cfg.Workspace.Path` (`adapter/config` loader; the same value `filesystem.NewWorkspaceManager` resolves, `adapter/filesystem/workspace.go:30-39`).
  - `groupRunID` — the id P2-T8 `GroupLauncher.Launch` reserves for the run before `CreateWithTasks`; the returned handle is persisted as `instance.GroupRun.WorkspaceSnapshot` (P2-T1/T3).
  - `parentDir` / `taskDir` — `WorkspaceManager.EnsureInstanceDir(id)` (`domain/engine/ports.go:27-40`, implemented at `adapter/filesystem/workspace.go:59-75`: `<root>/instances/<id>`), seeded as `{{workspace}}` by `domain/engine/workspace.go:17-43` `Engine.prepareWorkspace`; the parent's live bytes are written by existing executors (`executor_agent.go:AgentCallExecutor.Execute`, `adapter/process/executor_script.go:ScriptExecutor.Execute`, …).
  - `taskInstanceID` — the roster's reserved `GroupTask.ChildInstanceID` (P2-T8), passed by `GroupFileFinalizer.FinalizeTerminal` (P3-T5) from the finishing `Instance.ID`.
- Provenance probes: `TestNewWorkingArea_DefaultRunnerSnapshotsAndForks` fails if the one-argument composition-root form does not produce a working area; `TestSnapshotFork_PlainDirectoryRoundTrips` asserts the SINK (the task directory's bytes/modes/links/empty dirs) equals the parent; `TestSnapshot_MissingGitIsReportedBeforeAnyTaskExists` fails if the runner (P3-T1) is not what the area uses.

### Task 2: WorkingArea port + git snapshot/fork/finalize/release
**Files:**
- Create: `domain/engine/working_area.go` (port; no test of its own — it is pure declarations, exercised through the adapter and P3-T5/T6 fakes)
- Create: `adapter/git/content.go` (directory scan, blob hashing, tree/commit building, checkout, manifests, path helpers)
- Create: `adapter/git/metadata.go` (author `.git` capture/reproduction, `ErrRepositoryLocked`, tree copy)
- Create: `adapter/git/working_area.go` (`WorkingArea`, constructor/options, SnapshotParent/ForkTask/FinalizeTask/ReleaseSnapshot, keyed lock)
- Test:   `adapter/git/testhelpers_test.go` (shared by this task and P3-T3)
- Test:   `adapter/git/working_area_test.go`

No existing file is modified.

- [x] Step 1: Write the shared test helpers. Create `adapter/git/testhelpers_test.go` (it uses `requireGit` from P3-T1's `runner_test.go`):

```go
package git

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestArea returns a WorkingArea rooted at a fresh workspace root together
// with that root. Skips only when the host has no git binary.
func newTestArea(t *testing.T) (*WorkingArea, string) {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	a, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{StateDir: t.TempDir()})))
	require.NoError(t, err)
	return a, a.root
}

// mkdirIn creates <root>/<rel> and returns it (symlink-resolved like the
// adapter's own paths).
func mkdirIn(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(p, 0o755))
	resolved, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return resolved
}

// writeFile writes content at dir/rel (creating parents) with mode perm.
func writeFile(t *testing.T, dir, rel, content string, perm os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), perm))
	require.NoError(t, os.Chmod(p, perm))
}

// contentOf renders a directory as path -> description, EXCLUDING every .git
// entry: "file:<0644|0755>:<bytes>", "link:<target>", "dir" (empty dirs only).
// Two directories with equal contentOf hold identical task content.
func contentOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if strings.EqualFold(d.Name(), ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		require.NoError(t, ierr)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			require.NoError(t, lerr)
			out[rel] = "link:" + target
		case d.IsDir():
			entries, rerr := os.ReadDir(p)
			require.NoError(t, rerr)
			if len(entries) == 0 {
				out[rel] = "dir"
			}
		default:
			raw, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			mode := "0644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "0755"
			}
			out[rel] = "file:" + mode + ":" + string(raw)
		}
		return nil
	}))
	return out
}

// author runs the HOST git as a workflow author would (own identity, no
// global config so a developer's signing setup cannot break the test).
func author(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=Author", "-c", "user.email=author@example.com",
		"-c", "commit.gpgSign=false", "-c", "init.defaultBranch=main", "-c", "core.hooksPath=" + os.DevNull}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	require.NoError(t, cmd.Run(), "git %v: %s", args, errb.String())
	return strings.TrimSpace(out.String())
}

// authorRepo initialises a repository at dir with one commit of files.
func authorRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	author(t, dir, "init", "-q")
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeFile(t, dir, k, files[k], 0o644)
	}
	author(t, dir, "add", "-A")
	author(t, dir, "commit", "-q", "-m", "initial")
}

// lsTree lists a commit's paths (recursively) in the area's backing store.
func lsTree(t *testing.T, a *WorkingArea, handle, rev string) []string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: []string{"ls-tree", "-r", "-z", "--name-only", rev}, GitDir: backing})
	require.NoError(t, err)
	var paths []string
	for _, p := range strings.Split(res.Stdout, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// gitOut runs a git command against the area's backing store.
func gitOut(t *testing.T, a *WorkingArea, handle string, args ...string) string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: args, GitDir: backing})
	require.NoError(t, err)
	return strings.TrimSpace(res.Stdout)
}

// blobAt returns the exact bytes of rev:path in the area's backing store.
func blobAt(t *testing.T, a *WorkingArea, handle, rev, path string) string {
	t.Helper()
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	res, err := a.git.Run(t.Context(), Command{Args: []string{"cat-file", "blob", rev + ":" + path}, GitDir: backing})
	require.NoError(t, err)
	return res.Stdout
}
```

- [x] Step 2: Write the failing tests.

  Added the specified real-adapter tests plus `adapter/git/permissions_test.go` for exact ordinary modes in two forks across plain/nested/worktree layouts, immutable staged metadata, invalid/missing manifests, capture/chmod errors and interrupted-fork cleanup. Create `adapter/git/working_area_test.go`:

```go
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

func TestFinalizeTask_RefinalizeReplacesRef(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := plainParent(t, root)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	task := filepath.Join(root, "instances", "t")
	require.NoError(t, a.ForkTask(ctx, handle, task))
	id := uuid.New()
	_, err = a.FinalizeTask(ctx, handle, id, task)
	require.NoError(t, err)
	writeFile(t, task, "README.md", "second\n", 0o644)
	ref, err := a.FinalizeTask(ctx, handle, id, task)
	require.NoError(t, err)
	assert.Equal(t, "second\n", blobAt(t, a, handle, ref, "README.md"))
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
```

- [x] Step 3: Run them; confirm they fail to compile.

  `go test ./adapter/git/ -count=1` failed with undefined WorkingArea, readManifest, manifestPath and restoreAreaPermissions, before implementation.

```bash
go test ./adapter/git/ -count=1
```
Expected:
```
# github.com/arbitron/arbitron/adapter/git [github.com/arbitron/arbitron/adapter/git.test]
adapter/git/testhelpers_test.go:18:34: undefined: WorkingArea
adapter/git/testhelpers_test.go:22:12: undefined: NewWorkingArea
adapter/git/testhelpers_test.go:22:33: undefined: WithRunner
...
FAIL	github.com/arbitron/arbitron/adapter/git [build failed]
```

- [x] Step 4: Create the domain port. Create `domain/engine/working_area.go`:

```go
package engine

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// WorkingArea extends the Phase 2 GroupWorkspace (snapshot/fork/release) with
// the mergeable operations file gathering needs. Every "ref" is an immutable
// name inside the snapshot's private backing store, never a moving branch of an
// author repository. Implemented by adapter/git.WorkingArea.
type WorkingArea interface {
	GroupWorkspace
	// FinalizeTask records taskDir's full content (committed or not, untracked
	// and ignored files included, author .git metadata excluded) as an
	// immutable ref whose parent is the snapshot baseline, and returns it
	// ("refs/arbitron/tasks/<taskInstanceID>"). An unchanged taskDir is valid
	// and yields a ref whose tree equals the baseline. Calling it again for the
	// same task replaces the ref with the current content.
	FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error)
	// BeginGather records an exact rollback snapshot of parentDir (dirty,
	// untracked and ignored content included) and prepares a staging area
	// holding that content. Re-calling it for the same gatherID returns the
	// SAME handle and never re-snapshots, so a restart after a partial Apply
	// still rolls back to the true pre-gather content.
	BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (GatherHandle, error)
	// MergeTask three-way merges one finalized task ref (relative to the
	// common baseline) into the staging area. Conflicts are reported, not
	// errors; conflict markers are left in the staging files for resolution.
	// Re-calling it for a ref already merged reports Clean; re-calling it for
	// the ref whose merge is in progress reports that merge's conflicts.
	MergeTask(ctx context.Context, h GatherHandle, taskRef string) (MergeOutcome, error)
	// ConflictPaths returns the staging paths that are still conflicted:
	// unmerged index entries, plus previously conflicted paths whose content
	// still carries conflict markers. Sorted, unique; empty when resolved.
	ConflictPaths(ctx context.Context, h GatherHandle) ([]string, error)
	// CommitResolution records the resolved staging content as the next staged
	// result. Returns an error wrapping ErrStillConflicted (and changes
	// nothing) while ConflictPaths is non-empty.
	CommitResolution(ctx context.Context, h GatherHandle, message string) error
	// Apply makes parentDir equal the staged result: written, overwritten and
	// deleted files, modes and symlinks. Author .git metadata is never touched.
	// Idempotent.
	Apply(ctx context.Context, h GatherHandle) error
	// Rollback restores the exact pre-gather parentDir content recorded by
	// BeginGather (author .git metadata untouched). Idempotent.
	Rollback(ctx context.Context, h GatherHandle) error
	// ReleaseRef deletes one ref from the snapshot's backing store. Releasing a
	// gather's rollback ref also removes that gather's staging area. An
	// unknown handle or ref is not an error.
	ReleaseRef(ctx context.Context, snapshotHandle, ref string) error
}

// GatherHandle identifies one gather operation. It is persisted verbatim in
// instance.GatherReport.Handle (as a JSON object) so a restarted gatherer can
// resume against the same staging area and rollback snapshot.
type GatherHandle struct {
	SnapshotHandle string    `json:"snapshotHandle"`
	GatherID       uuid.UUID `json:"gatherId"`
	StagingDir     string    `json:"stagingDir"`
	RollbackRef    string    `json:"rollbackRef"`
	ParentDir      string    `json:"parentDir"`
}

// MergeOutcome is the result of one MergeTask.
type MergeOutcome struct {
	Clean         bool     `json:"clean"`
	ConflictPaths []string `json:"conflictPaths"`
}

// ErrStillConflicted reports that a staging area still has unresolved
// conflicts (CommitResolution / Apply refuse to proceed).
var ErrStillConflicted = errors.New("working area still has unresolved merge conflicts")
```

- [x] Step 5: Implement content capture.

  Implemented full permission capture, strict manifest validation and symlink-safe restoration (files, deepest directories, root last), including atomic unique manifest writes. Create `adapter/git/content.go`:

```go
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// Git file modes used in trees built by this package.
const (
	modeFile    = "100644"
	modeExec    = "100755"
	modeSymlink = "120000"
)

// fileEntry is one regular file or symlink of a scanned area. Rel is
// slash-separated and relative to the area root.
type fileEntry struct {
	Rel  string
	Abs  string
	Mode string
	Link string // symlink target (Mode == modeSymlink)
}

// gitEntry is one author repository metadata entry (".git" directory or
// ".git" file) found at any depth. It is NEVER task content.
type gitEntry struct {
	Rel   string
	IsDir bool
}

// areaContent is the content of a working area as git sees it: files and
// symlinks, directories that have no children at all (git cannot store them,
// so they are kept in the manifest), and author metadata locations.
type areaContent struct {
	Permissions map[string]uint32
	Files      []fileEntry
	EmptyDirs  []string
	GitEntries []gitEntry
}

// isGitMetadataName reports whether a directory entry is repository metadata.
// git itself refuses ".git" path components case-insensitively, so a
// differently-cased ".GIT" can never be content either.
func isGitMetadataName(name string) bool {
	return strings.EqualFold(name, ".git")
}

// scanArea walks root (which must already be symlink-resolved) without
// following symlinks. skip, when non-empty, is an absolute path excluded with
// everything beneath it (the backing store when it sits inside root).
func scanArea(root, skip string) (*areaContent, error) {
	c := &areaContent{Permissions: map[string]uint32{}}
	hasChild := map[string]bool{}
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			info, err := d.Info()
			if err != nil { return err }
			c.Permissions["."] = uint32(info.Mode().Perm())
			return nil
		}
		if skip != "" && p == skip {
			hasChild[filepath.Dir(p)] = true
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		hasChild[filepath.Dir(p)] = true
		if isGitMetadataName(d.Name()) {
			c.GitEntries = append(c.GitEntries, gitEntry{Rel: rel, IsDir: d.IsDir()})
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch t := d.Type(); {
		case t.IsDir():
			info, err := d.Info()
			if err != nil { return err }
			c.Permissions[rel] = uint32(info.Mode().Perm())
			dirs = append(dirs, p)
		case t.IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			c.Permissions[rel] = uint32(info.Mode().Perm())
			mode := modeFile
			if info.Mode().Perm()&0o111 != 0 {
				mode = modeExec
			}
			c.Files = append(c.Files, fileEntry{Rel: rel, Abs: p, Mode: mode})
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			c.Files = append(c.Files, fileEntry{Rel: rel, Abs: p, Mode: modeSymlink, Link: target})
		default:
			return fmt.Errorf("%w: %q is a %s (only regular files, directories and symlinks can be gathered)",
				engine.ErrUnsupportedSnapshotContent, rel, describeMode(t))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if !hasChild[d] {
			rel, _ := filepath.Rel(root, d)
			c.EmptyDirs = append(c.EmptyDirs, filepath.ToSlash(rel))
		}
	}
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Rel < c.Files[j].Rel })
	sort.Strings(c.EmptyDirs)
	sort.Slice(c.GitEntries, func(i, j int) bool { return c.GitEntries[i].Rel < c.GitEntries[j].Rel })
	return c, nil
}

func describeMode(m fs.FileMode) string {
	switch {
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "special file"
	}
}

// stdinPathSafe reports whether hash-object --stdin-paths can read rel
// verbatim: it splits on LF, strips a trailing CR and C-unquotes a leading '"'.
func stdinPathSafe(rel string) bool {
	return !strings.ContainsAny(rel, "\n\r") && !strings.HasPrefix(rel, `"`)
}

// hashFiles writes every entry's blob into gitDir and returns the object ids
// in entry order, using ONE hash-object process. Symlink targets and files
// whose names cannot travel over --stdin-paths are staged as temporary files
// first. --no-filters stores bytes exactly as they are on disk.
func (a *WorkingArea) hashFiles(ctx context.Context, gitDir, root, tmpRoot string, files []fileEntry) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	tmp, err := os.MkdirTemp(tmpRoot, "blobs-")
	if err != nil {
		return nil, fmt.Errorf("stage blobs: %w", err)
	}
	defer os.RemoveAll(tmp)

	var list bytes.Buffer
	for i, f := range files {
		p := f.Rel
		switch {
		case f.Mode == modeSymlink:
			p = filepath.Join(tmp, fmt.Sprintf("%d", i))
			if err := os.WriteFile(p, []byte(f.Link), 0o600); err != nil {
				return nil, fmt.Errorf("stage symlink %q: %w", f.Rel, err)
			}
		case !stdinPathSafe(f.Rel):
			p = filepath.Join(tmp, fmt.Sprintf("%d", i))
			if err := copyFileContent(f.Abs, p); err != nil {
				return nil, fmt.Errorf("stage file %q: %w", f.Rel, err)
			}
		}
		list.WriteString(p)
		list.WriteByte('\n')
	}
	res, err := a.git.Run(ctx, Command{
		Args:   []string{"hash-object", "-w", "--no-filters", "--stdin-paths"},
		Dir:    root,
		GitDir: gitDir,
		Stdin:  list.Bytes(),
	})
	if err != nil {
		return nil, fmt.Errorf("hash working-area files: %w", err)
	}
	shas := strings.Fields(res.Stdout)
	if len(shas) != len(files) {
		return nil, fmt.Errorf("hash working-area files: got %d object ids for %d files", len(shas), len(files))
	}
	return shas, nil
}

func copyFileContent(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// commitContent records c (scanned from root) as a commit in gitDir whose
// only parent is parent ("" = root commit) and returns the commit id. The
// index is private to this call, so concurrent finalizations never collide.
func (a *WorkingArea) commitContent(ctx context.Context, gitDir, root string, c *areaContent, parent, message string) (string, error) {
	tmpRoot := filepath.Join(gitDir, "arbitron", "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return "", fmt.Errorf("prepare backing tmp: %w", err)
	}
	shas, err := a.hashFiles(ctx, gitDir, root, tmpRoot, c.Files)
	if err != nil {
		return "", err
	}
	idxDir, err := os.MkdirTemp(tmpRoot, "index-")
	if err != nil {
		return "", fmt.Errorf("prepare index: %w", err)
	}
	defer os.RemoveAll(idxDir)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(idxDir, "index")}

	var info bytes.Buffer
	for i, f := range c.Files {
		fmt.Fprintf(&info, "%s %s\t%s\x00", f.Mode, shas[i], f.Rel)
	}
	if _, err := a.git.Run(ctx, Command{
		Args: []string{"update-index", "-z", "--index-info"}, GitDir: gitDir, Env: indexEnv, Stdin: info.Bytes(),
	}); err != nil {
		return "", fmt.Errorf("build tree index: %w", err)
	}
	res, err := a.git.Run(ctx, Command{Args: []string{"write-tree"}, GitDir: gitDir, Env: indexEnv})
	if err != nil {
		return "", fmt.Errorf("write tree: %w", err)
	}
	tree := strings.TrimSpace(res.Stdout)
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	res, err = a.git.Run(ctx, Command{Args: args, GitDir: gitDir})
	if err != nil {
		return "", fmt.Errorf("commit tree: %w", err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// checkoutCommit writes every file of commit into dir (overwriting, creating
// directories, replacing a file/symlink that blocks a directory and vice
// versa). It never deletes files the commit lacks — callers that need an
// exact mirror use syncArea.
func (a *WorkingArea) checkoutCommit(ctx context.Context, gitDir, tmpRoot, commit, dir string) error {
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return fmt.Errorf("prepare tmp: %w", err)
	}
	idxDir, err := os.MkdirTemp(tmpRoot, "index-")
	if err != nil {
		return fmt.Errorf("prepare index: %w", err)
	}
	defer os.RemoveAll(idxDir)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(idxDir, "index")}
	if _, err := a.git.Run(ctx, Command{Args: []string{"read-tree", commit}, GitDir: gitDir, Env: indexEnv}); err != nil {
		return fmt.Errorf("read tree %s: %w", commit, err)
	}
	if _, err := a.git.Run(ctx, Command{
		Args: []string{"checkout-index", "-a", "-f"}, GitDir: gitDir, WorkTree: dir, Dir: dir, Env: indexEnv,
	}); err != nil {
		return fmt.Errorf("check out %s: %w", commit, err)
	}
	return nil
}

// manifest is the side record kept for every commit this package creates:
// what git cannot represent (empty directories) and where author metadata
// lived (baseline snapshots only).
type manifest struct {
	Permissions map[string]uint32 `json:"permissions"`
	Root      string          `json:"root,omitempty"` // area path the commit was scanned from
	EmptyDirs []string        `json:"emptyDirs"`
	Metadata  []metadataEntry `json:"metadata,omitempty"`
}

// metadataEntry describes one author ".git" entry of a baseline snapshot and
// how ForkTask reproduces it independently in every task copy.
type metadataEntry struct {
	Path string `json:"path"` // slash-separated location of the ".git" entry
	// Kind is "metadata-tree", an immutable area-relative metadata layout
	// materialized by the shared filesystem helper. No pointer targets the parent.
	Kind          string `json:"kind"`
	Copy          string `json:"copy,omitempty"`          // dir/independent: copy location relative to the backing dir
	Content       string `json:"content,omitempty"`       // pointer: original file content
	PointerTarget string `json:"pointerTarget,omitempty"` // pointer with an absolute gitdir: target relative to Root
}

func manifestPath(gitDir, commit string) string {
	return filepath.Join(gitDir, "arbitron", "manifests", commit+".json")
}

func writeManifest(gitDir, commit string, m manifest) error {
	if m.EmptyDirs == nil {
		m.EmptyDirs = []string{}
	}
	p := manifestPath(gitDir, commit)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// readManifest returns the commit's manifest; a missing manifest (a commit
// created by a merge) is an empty one.
func readManifest(gitDir, commit string) (manifest, error) {
	raw, err := os.ReadFile(manifestPath(gitDir, commit))
	if errors.Is(err, os.ErrNotExist) {
		return manifest{}, nil
	}
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("corrupt manifest for %s: %w", commit, err)
	}
	return m, nil
}

// createEmptyDirs recreates the manifest's empty directories under dir.
func createEmptyDirs(dir string, rels []string) error {
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
			return fmt.Errorf("recreate empty directory %q: %w", rel, err)
		}
	}
	return nil
}

// within reports whether p is root or lies beneath it (both cleaned, absolute).
func within(root, p string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// slashAncestors returns every proper ancestor directory of a slash path,
// nearest first ("a/b/c" -> "a/b", "a").
func slashAncestors(rel string) []string {
	var out []string
	for d := path.Dir(rel); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		out = append(out, d)
	}
	return out
}
```

- [x] Step 6: Implement author-metadata capture and reproduction.

  Reused the P2-T17 helper without duplicating its normalizer. Its implementation deliberately flattens linked worktrees into standalone repositories. A temporary area-relative pointer skeleton supplies the helper's required directory layout and avoids scanning backing stores when the parent equals the workspace root; forks read only the resulting immutable metadata tree. Create `adapter/git/metadata.go`:

```go
package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

// ErrRepositoryLocked reports an author repository whose index is locked by
// an in-flight git operation (index.lock present); snapshotting it would
// capture inconsistent metadata.
var ErrRepositoryLocked = filesystem.ErrGroupRepositoryLocked

// captureMetadata copies every author ".git" entry of area into the backing
// store so the snapshot stays immutable even if the parent's repositories
// change later, and returns the manifest entries ForkTask replays.
func (a *WorkingArea) captureMetadata(ctx context.Context, area, backing string, entries []gitEntry) ([]metadataEntry, error) {
	if len(entries) == 0 { return []metadataEntry{}, nil }
	copyRel := "arbitron/metadata-tree"
	copyRoot := filepath.Join(backing, filepath.FromSlash(copyRel))
	if err := os.MkdirAll(copyRoot, 0o700); err != nil { return nil, err }
	if err := filesystem.CopyGroupGitMetadata(ctx, area, copyRoot); err != nil { return nil, err }
	return []metadataEntry{{Path: ".", Kind: "metadata-tree", Copy: copyRel}}, nil
}

// restoreMetadata repeats the same required metadata-isolation boundary for
// each task. The source is the immutable captured tree, never the live parent.
func restoreMetadata(ctx context.Context, backing, taskDir string, m manifest) error {
	for _, entry := range m.Metadata {
		if entry.Kind != "metadata-tree" || entry.Copy != "arbitron/metadata-tree" {
			return fmt.Errorf("unsupported group metadata manifest entry %q", entry.Kind)
		}
		if err := filesystem.CopyGroupGitMetadata(ctx, filepath.Join(backing, filepath.FromSlash(entry.Copy)), taskDir); err != nil { return err }
	}
	return nil
}

// rewriteWorktreeLinks re-roots the absolute "gitdir" back-pointers of a
// copied repository's linked worktrees that lived inside the snapshotted
// area, so the task copy's worktrees belong to the task copy's repository.
func rewriteWorktreeLinks(gitDir, oldRoot, newRoot string) error {
	entries, err := os.ReadDir(filepath.Join(gitDir, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(gitDir, "worktrees", e.Name(), "gitdir")
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		target := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(target) || oldRoot == "" || !within(oldRoot, filepath.Clean(target)) {
			continue
		}
		rel, _ := filepath.Rel(oldRoot, filepath.Clean(target))
		if err := os.WriteFile(p, []byte(filepath.Join(newRoot, rel)+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// parseGitdirPointer reads "gitdir: <path>" (relative paths resolve against
// base) and returns the cleaned, symlink-resolved target.
func parseGitdirPointer(content, base string) (string, bool) {
	line := firstLine(content)
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	p := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return p, true
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

func refuseLocked(gitDir, rel string) error {
	if _, err := os.Lstat(filepath.Join(gitDir, "index.lock")); err == nil {
		return fmt.Errorf("%w: %q (finish or abort the operation, or remove the stale lock, then retry)", ErrRepositoryLocked, rel)
	}
	return nil
}

// copyTree copies src to dst recursively, preserving file modes and
// symlinks. Sockets and pipes (fsmonitor IPC endpoints) are skipped: they are
// runtime artefacts, not repository state.
func copyTree(src, dst string) error { return copyTreeSkipping(src, dst, nil) }

func copyTreeSkipping(src, dst string, skipTop map[string]bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel != "." && skipTop[strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch t := d.Type(); {
		case t.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm()|0o700)
		case t&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case t.IsRegular():
			if err := copyFileContent(p, target); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		default:
			return nil
		}
	})
}
```

- [x] Step 7: Implement the working area.

  Added snapshot/fork/finalize/release and default-runner construction. Failed/interrupted forks remove partial content and restore the original empty directory mode. Create `adapter/git/working_area.go`:

```go
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

const (
	// backingDirName is the directory under the workspace root that holds every
	// group snapshot's private backing repository and gather staging areas. It
	// is a sibling of the per-instance directories, never inside an author dir.
	backingDirName = ".group-backing"

	// BaselineRef names the group-start snapshot commit in a backing store.
	BaselineRef = "refs/arbitron/baseline"
	// TaskRefPrefix + <taskInstanceID> names a finalized task commit.
	TaskRefPrefix = "refs/arbitron/tasks/"
	// RollbackRefPrefix + <gatherID> names a gather's pre-gather snapshot.
	RollbackRefPrefix = "refs/arbitron/rollback/"

	// backingAttributes overrides every in-tree .gitattributes so content is
	// stored and written back byte-for-byte: no end-of-line conversion, no
	// $Id$ expansion, no clean/smudge filters, no re-encoding. Merge drivers
	// are left to the content's own attributes (union / binary are honoured;
	// unknown custom drivers fall back to git's text merge).
	backingAttributes = "* -text -eol -ident -filter -working-tree-encoding\n"
)

// WorkingArea is the git-backed engine.WorkingArea.
//
// BACKING STRATEGY (P3-A). Every group snapshot owns a private bare
// repository at <root>/.group-backing/<groupRunID>.git. Content is recorded
// with git plumbing (hash-object --no-filters, update-index --index-info,
// write-tree, commit-tree) from a Go walk of the directory, never with
// "git add" — "git add" would turn a nested author repository into a gitlink
// and lose its working-tree files. The walk captures everything the area
// holds — tracked, modified, untracked and ignored files, executable bits and
// symlinks — and excludes every ".git" entry at any depth: author repository
// metadata is not task content. Metadata is reproduced separately per task
// (metadata.go), and empty directories (which git cannot store) live in a
// per-commit manifest beside the repository.
//
// All git processes run through Runner, so no author or host configuration,
// hook, filter or credential helper ever runs. The author's repositories are
// read (copied) but never written.
type WorkingArea struct {
	root    string // absolute, symlink-resolved workspace root
	backing string // root/.group-backing
	git     *Runner
	locks   keyedMutex
}

var _ engine.GroupWorkspace = (*WorkingArea)(nil)

// Option configures NewWorkingArea.
type Option func(*WorkingArea) error

// WithRunner makes every git process of the area go through r (tests, an
// explicit git binary, a custom timeout/output cap).
func WithRunner(r *Runner) Option {
	return func(a *WorkingArea) error {
		if r == nil {
			return errors.New("git working area: runner is required")
		}
		a.git = r
		return nil
	}
}

// NewWorkingArea returns a WorkingArea whose backing stores live under
// root/.group-backing. root is the workspace root (cfg.Workspace.Path); it is
// created if missing. Without WithRunner every git process runs through a
// default Runner (git looked up on the subprocess PATH at each use) whose
// private HOME lives at root/.group-backing/.runner.
func NewWorkingArea(root string, opts ...Option) (*WorkingArea, error) {
	if root == "" {
		return nil, errors.New("git working area: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("git working area: resolve root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, backingDirName), 0o700); err != nil {
		return nil, fmt.Errorf("git working area: create backing root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("git working area: resolve root: %w", err)
	}
	a := &WorkingArea{root: resolved, backing: filepath.Join(resolved, backingDirName)}
	for _, opt := range opts {
		if err := opt(a); err != nil {
			return nil, err
		}
	}
	if a.git == nil {
		a.git = NewRunner(RunnerConfig{StateDir: filepath.Join(a.backing, ".runner")})
	}
	return a, nil
}

// BackingDir returns the private backing repository path for a snapshot
// handle. The handle must be a group-run UUID (it is used in a path).
func (a *WorkingArea) BackingDir(handle string) (string, error) {
	id, err := uuid.Parse(handle)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("git working area: invalid snapshot handle %q", handle)
	}
	return filepath.Join(a.backing, id.String()+".git"), nil
}

// SnapshotParent records parentDir as the group's baseline and returns the
// handle (the group-run id). A previous snapshot for the same group run is
// replaced. Fails BEFORE any task exists when git is missing, an author
// repository is locked, or the area holds unsupported content.
func (a *WorkingArea) SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	handle := groupRunID.String()
	backing, err := a.BackingDir(handle)
	if err != nil {
		return "", err
	}
	unlock := a.locks.Lock(handle)
	defer unlock()

	parent, err := resolveDir(parentDir)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	if err := os.RemoveAll(backing); err != nil {
		return "", fmt.Errorf("git working area: reset backing store: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(backing)
		}
	}()
	if err := a.initBacking(ctx, backing); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	content, err := scanArea(parent, a.skipInside(parent))
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	meta, err := a.captureMetadata(ctx, parent, backing, content.GitEntries)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	commit, err := a.commitContent(ctx, backing, parent, content, "", "arbitron: group baseline "+handle)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	if err := writeManifest(backing, commit, manifest{Root: parent, EmptyDirs: content.EmptyDirs, Metadata: meta, Permissions: content.Permissions}); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: write manifest: %w", err)
	}
	if err := a.updateRef(ctx, backing, BaselineRef, commit); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	ok = true
	return handle, nil
}

// ForkTask materializes an independent writable copy of the baseline at
// taskDir (created if missing; it must be empty): every file with its mode,
// every symlink, every empty directory, and an independent reproduction of
// each author repository's metadata.
func (a *WorkingArea) ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error {
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	task, err := resolveDir(taskDir)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	entries, err := os.ReadDir(task)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("git working area: fork task: task directory %q is not empty", task)
	}
	if err := a.checkoutCommit(ctx, backing, filepath.Join(backing, "arbitron", "tmp"), baseline, task); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	m, err := readManifest(backing, baseline)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := createEmptyDirs(task, m.EmptyDirs); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := restoreMetadata(ctx, backing, task, m); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := restoreAreaPermissions(ctx, task, m.Permissions); err != nil { return err }
	return nil
}

// FinalizeTask records taskDir's full content as refs/arbitron/tasks/<id>, a
// commit whose single parent is the baseline. Edits are captured whether or
// not the task committed them in its own repository copy; the task's
// repository metadata is never recorded.
func (a *WorkingArea) FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error) {
	if taskInstanceID == uuid.Nil {
		return "", errors.New("git working area: finalize task: task instance id is required")
	}
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	task, err := resolveDir(taskDir)
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	content, err := scanArea(task, a.skipInside(task))
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	commit, err := a.commitContent(ctx, backing, task, content, baseline, "arbitron: task "+taskInstanceID.String())
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	if err := writeManifest(backing, commit, manifest{Root: task, EmptyDirs: content.EmptyDirs, Permissions: content.Permissions}); err != nil {
		return "", fmt.Errorf("git working area: finalize task: write manifest: %w", err)
	}
	ref := TaskRefPrefix + taskInstanceID.String()
	if err := a.updateRef(ctx, backing, ref, commit); err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	return ref, nil
}

// ReleaseSnapshot deletes the backing store and every staging area of the
// snapshot. An empty or unknown handle is not an error.
func (a *WorkingArea) ReleaseSnapshot(ctx context.Context, snapshotHandle string) error {
	if snapshotHandle == "" {
		return nil
	}
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return err
	}
	unlock := a.locks.Lock(snapshotHandle)
	defer unlock()
	staging, _ := filepath.Glob(filepath.Join(a.backing, snapshotHandle+"-gather-*"))
	for _, s := range staging {
		if err := os.RemoveAll(s); err != nil {
			return fmt.Errorf("git working area: release snapshot: %w", err)
		}
	}
	if err := os.RemoveAll(backing); err != nil {
		return fmt.Errorf("git working area: release snapshot: %w", err)
	}
	return nil
}

// ResolveRef returns the commit a ref of the snapshot names.
func (a *WorkingArea) ResolveRef(ctx context.Context, snapshotHandle, ref string) (string, error) {
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return "", err
	}
	return a.revParse(ctx, backing, ref)
}

func (a *WorkingArea) initBacking(ctx context.Context, backing string) error {
	if _, err := a.git.Run(ctx, Command{Args: []string{"init", "--bare", "-q", "--template=", backing}}); err != nil {
		return fmt.Errorf("create backing store: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(backing, "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backing, "info", "attributes"), []byte(backingAttributes), 0o600); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(backing, "arbitron", "tmp"), 0o700)
}

// baseline resolves the backing store and baseline commit of a handle.
func (a *WorkingArea) baseline(ctx context.Context, handle string) (string, string, error) {
	backing, err := a.BackingDir(handle)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(backing); err != nil {
		return "", "", fmt.Errorf("unknown snapshot %q: %w", handle, err)
	}
	commit, err := a.revParse(ctx, backing, BaselineRef)
	if err != nil {
		return "", "", fmt.Errorf("snapshot %q has no baseline: %w", handle, err)
	}
	return backing, commit, nil
}

func (a *WorkingArea) revParse(ctx context.Context, gitDir, rev string) (string, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "-q", rev + "^{commit}"}, GitDir: gitDir})
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (a *WorkingArea) refExists(ctx context.Context, gitDir, ref string) (bool, error) {
	_, err := a.git.Run(ctx, Command{Args: []string{"show-ref", "--verify", "-q", ref}, GitDir: gitDir})
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

func (a *WorkingArea) updateRef(ctx context.Context, gitDir, ref, commit string) error {
	if _, err := a.git.Run(ctx, Command{Args: []string{"update-ref", ref, commit}, GitDir: gitDir}); err != nil {
		return fmt.Errorf("record %s: %w", ref, err)
	}
	return nil
}

// skipInside returns the backing root when it lies inside dir (a parent area
// that IS the workspace root), so a snapshot never captures its own store.
func (a *WorkingArea) skipInside(dir string) string {
	if within(dir, a.backing) {
		return a.backing
	}
	return ""
}

func resolveDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("directory %q: %w", dir, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}
	return resolved, nil
}

// keyedMutex serializes operations per snapshot handle within this process.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
```

- [x] Step 8: Run the package, the race detector, the engine package (the port must compile against P2-T5), gofmt and vet.

  Self-review stage 1 (spec compliance): compared the implementation with the declared port, full-area/plumbing/metadata contracts and P3-T11's explicit production wiring. Kept this pass to the adapter/port slice; no new mode/flag or premature enabling of file mode. Added lifecycle sink tests for replacement snapshots, finalized refs surviving live-area deletion, metadata-only repositories and release of all gather staging directories. The read-only-release regression failed with permission denied, then passed after private-tree cleanup learned to make directories removable without following symlinks. All adapter tests pass with `-race`; engine builds, gofmt and vet are clean. Production construction/data feeding is explicitly owned by P3-T10/T11, not omitted from the plan.

  Self-review stage 2 (anti-green-theater): tests consume actual parent bytes/modes and staged Git indexes and assert the task-directory/ref sinks, including two independent forks and live-source removal. Temporarily removed only ForkTask's permission-restoration call: the exact-mode sink test failed in all three layouts (0600/0751/0550/0640/root modes changed to Git/default modes). Restored the call; no mutation remains. The planned P3-T11 tests own real Engine/composition-root proof after the remaining WorkingArea operations exist.

  Self-review stage 3 (code quality): read neighboring filesystem snapshot/metadata and Git-runner implementations. Removed the plan block's unused standalone metadata-copy/rewriting functions and obsolete manifest fields; kept shared primitives required by P3-T3. Clarified checkout/manifest comments, normalized formatting and contextualized fork errors. After all review fixes, all 50 top-level adapter tests pass, including `-race`; `go test ./adapter/filesystem/ -count=1`, `go build ./domain/engine/`, gofmt, vet and `git diff --check` also pass.

```bash
go test ./adapter/git/ -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
go test ./adapter/git/ -count=1 -race
go build ./domain/engine/
gofmt -l adapter/git/ domain/engine/working_area.go
go vet ./adapter/git/ ./domain/engine/
```
Expected: every `TestRunner_*` (P3-T1) and every test below passes; `gofmt -l` and `go vet` print nothing.
```
--- PASS: TestNewWorkingArea_RequiresRootAndNonNilRunner (0.00s)
--- PASS: TestNewWorkingArea_DefaultRunnerSnapshotsAndForks (0.1Xs)
--- PASS: TestNewWorkingArea_CreatesBackingRootUnderWorkspaceRoot (0.00s)
--- PASS: TestBackingDir_RejectsNonUUIDHandles (0.00s)
--- PASS: TestSnapshotFork_PlainDirectoryRoundTrips (0.1Xs)
--- PASS: TestSnapshot_IsImmutableAndForksAreIndependent (0.2Xs)
--- PASS: TestSnapshot_ByteExactDespiteAuthorAttributes (0.1Xs)
--- PASS: TestSnapshot_WorkspaceRootAsParentNeverCapturesBackingStore (0.1Xs)
--- PASS: TestSnapshot_AuthorRepositoryMetadataExcludedAndReproduced (0.3Xs)
--- PASS: TestSnapshot_InAreaLinkedWorktreeIsRepointedToTheTaskCopy (0.3Xs)
--- PASS: TestSnapshot_ExternalWorktreePointerBecomesIndependentRepository (0.3Xs)
--- PASS: TestSnapshot_LockedAuthorIndexIsActionableAndLeavesNothing (0.0Xs)
--- PASS: TestSnapshot_UnsupportedSpecialFile (0.0Xs)
--- PASS: TestSnapshot_MissingGitIsReportedBeforeAnyTaskExists (0.00s)
--- PASS: TestSnapshot_MissingParentDirIsAnError (0.0Xs)
--- PASS: TestForkTask_RejectsNonEmptyDirAndUnknownSnapshot (0.1Xs)
--- PASS: TestFinalizeTask_CapturesUncommittedWorkAndParentsOnBaseline (0.3Xs)
--- PASS: TestFinalizeTask_EmptyDiffIsValid (0.1Xs)
--- PASS: TestFinalizeTask_RefinalizeReplacesRef (0.2Xs)
--- PASS: TestFinalizeTask_ErrorCases (0.1Xs)
--- PASS: TestFinalizeTask_ConcurrentTasksGetDistinctRefs (0.5Xs)
--- PASS: TestReleaseSnapshot_RemovesBackingAndToleratesUnknown (0.1Xs)
ok  	github.com/arbitron/arbitron/adapter/git	X.XXXs
```

- [x] Step 9: Commit.

  Committed implementation, focused tests and this completed task record with the specified subject.

```bash
git add domain/engine/working_area.go adapter/git/content.go adapter/git/metadata.go adapter/git/working_area.go adapter/git/testhelpers_test.go adapter/git/working_area_test.go
git commit -m "feat(git): private-backing working area snapshot/fork/finalize/release (P3-A)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
