# Parallel Workflow Gathering — Phase 2 Task 5: Group workspace snapshot/fork port and filesystem adapter

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Supply the workspace half of P2-B: an `engine.GroupWorkspace` port that takes ONE immutable copy of the parent's working area at the group's launch boundary and forks an independent writable copy per task before enqueue, plus `adapter/filesystem.GroupWorkspaceAdapter`, a plain recursive-copy implementation that preserves relative layout, permission bits and empty directories, recreates in-tree relative symlinks, copies `.git` directories as independent repositories, and refuses (with `ErrUnsupportedSnapshotContent` naming the path) everything that would let a task share state with the parent.
**Architecture:** Port + sentinel in `domain/engine/group_workspace.go`; the sentinel's identity lives in `domain/instance/group_snapshot.go` and the engine re-exports the SAME value, because `adapter/filesystem` cannot import `domain/engine` (`domain/engine`'s internal test `template_include_samples_test.go` imports `adapter/filesystem` — an import cycle). The adapter stores snapshots in `<root>/.group-snapshots/<groupRunID>` (0700), a sibling of `WorkspaceManagerAdapter`'s `<root>/instances/` so no instance-directory removal can delete one; snapshots are built in a temp dir and renamed into place (no half snapshot on failure); handles are the snapshot's absolute path and are strictly validated before any read or delete.
**Tech Stack:** Go 1.25.5 standard library (`os`, `io`, `io/fs`, `path/filepath`), `github.com/google/uuid`, testify; adapter tests are POSIX-only (`//go:build !windows`: permission bits, symlinks, named pipes) and use `t.TempDir`.
**Spec / Design:** spec "Each task starts with its own copy of the group's starting variables and its own isolated working area"; `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-2.md` §2 ("Take one immutable group-start copy of the parent's working area and fork independent task directories; preserve relative file layout even for results-only groups without `repoPath`. No hard links, writable symlinks, or Git metadata pointing back to the parent's mutable worktree") and Data Provenance rows "immutable workspace starting copy" (PLANNED DEPENDENCY P2-B — resolved here for the workspace half) and "Parent workspace path"; Phase 3 design §1 (the Git `WorkingArea` later embeds this same port).

## Required follow-on before enabling results execution

P2-T17 consumes this copy implementation, adds independent Git metadata materialization
for linked worktrees, and replaces the `.git` FILE rejection tests below. This task's
rejection is a staged boundary only; P2-T14 depends on P2-T17 and cannot ship results mode
with the bundled sample workspace permanently refused. General escaping content symlinks
and special devices remain unsupported. P3-T2 reuses P2-T17's helper for metadata copying.


**Interfaces**
- Consumes (existing code only):
  - `domain/engine/ports.go:26-40` `WorkspaceManager` (`EnsureInstanceDir`, `RemoveInstanceDir`) and its implementation `adapter/filesystem/workspace.go:20-83` (`NewWorkspaceManager`, layout `<basePath>/instances/<id>`, mode 0o700) — the tests build parents and task dirs through it so the layout under test is the production one.
  - `domain/instance` (imported by both `domain/engine` and the adapter).
- Produces (exact signatures):
  - `domain/engine/group_workspace.go`: `type GroupWorkspace interface { SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error); ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error; ReleaseSnapshot(ctx context.Context, snapshotHandle string) error }` and `var ErrUnsupportedSnapshotContent = instance.ErrUnsupportedSnapshotContent`.
  - `domain/instance/group_snapshot.go`: `var ErrUnsupportedSnapshotContent = errors.New("unsupported content in working area snapshot")` (the identity; `errors.Is` matches either name).
  - `adapter/filesystem/group_workspace.go`: `type GroupWorkspaceAdapter struct`; `func NewGroupWorkspace(root string) (*GroupWorkspaceAdapter, error)`; `func (g *GroupWorkspaceAdapter) SnapshotsDir() string`; the three port methods; `var ErrInvalidSnapshotHandle = errors.New("invalid group snapshot handle")`.
- Semantics later tasks rely on (pinned by the tests):
  - `SnapshotParent`: `groupRunID` non-nil; `parentDir` absolute, existing directory (missing ⇒ error wrapping `fs.ErrNotExist`), not overlapping the snapshot store; returns `<root>/.group-snapshots/<groupRunID>`; re-snapshotting the same id replaces the previous snapshot; `ctx` cancellation aborts with `context.Canceled` and leaves nothing behind; unsupported content ⇒ error wrapping `ErrUnsupportedSnapshotContent` whose message names the relative path and says what to do.
  - Unsupported: absolute symlinks; relative symlinks that resolve (or lexically point) outside the area, including escapes through another in-tree symlink; dangling symlinks whose target contains `..`; unresolvable symlinks (loops); a `.git` FILE anywhere ("git worktree metadata is supported only by the file-gathering backend"); devices/sockets/named pipes. Supported: regular files (by content, mode bits), directories (incl. empty and read-only), `.git` DIRECTORIES, in-area relative symlinks, dangling downward symlinks.
  - `ForkTask`: valid handle of an existing snapshot; `taskDir` absolute, outside the store, missing (created 0o700) or an EMPTY directory (its own mode is kept); non-empty or non-directory ⇒ error, content untouched; a failed fork removes what it wrote.
  - `ReleaseSnapshot`: `""` and already-released/unknown well-formed handles ⇒ `nil`; removes read-only trees; malformed or traversal handles ⇒ `ErrInvalidSnapshotHandle` and nothing is touched.
  - `ForkTask("")` ⇒ `ErrInvalidSnapshotHandle` (a parent with no working area has nothing to fork — the launcher must skip forking when `GroupRun.WorkspaceSnapshot == ""`).

**Wiring & data provenance**
- Composition root: P2-T14 adds, in `cmd/arbitron/main.go:initDomain` in its group-wiring block right after the `spawn_workflow` executor registration (main.go:940-949), `groupWS, err := filesystem.NewGroupWorkspace(cfg.Workspace.Path)` (the same root as `wsMgr, err := filesystem.NewWorkspaceManager(cfg.Workspace.Path)` at `main.go:814`, so `.group-snapshots/` is the sibling of `instances/`) and passes `groupWS` as the `groupWS GroupWorkspace` argument of `engine.NewGroupLauncher` (P2-T8). P2-T14's `cmd/arbitron/group_e2e_test.go` asserts a task sees the parent's starting file, which fails if the line is deleted. Phase 3 keeps `groupWS` for results-only groups and adds the Git-backed `WorkingArea` (P3-T2) for `results_and_files` groups only, via `groupLauncher.SetFileWorkingArea(workingArea)` (P3-T10, wired by P3-T11).
- Producers of the inputs:
  - `parentDir`: the parent's `{{workspace}}` variable, seeded by `domain/engine/workspace.go:Engine.prepareWorkspace` (lines 17-43: `e.workspaceManager.EnsureInstanceDir(instanceID)` → `vars.SetReserved(ReservedVarWorkspace, path)`), called from `domain/engine/engine.go:871` at the start of `Engine.run`; the launcher (P2-T8) reads it from `GroupLaunchRequest.Vars` (`Get("workspace")`). An empty value (no `WorkspaceManager` wired) ⇒ the launcher skips the snapshot and stores `WorkspaceSnapshot = ""`.
  - `groupRunID`: the run id the launcher generates before `CreateWithTasks` (P2-T3 persists the returned handle as `GroupRun.WorkspaceSnapshot`).
  - `taskDir`: `WorkspaceManager.EnsureInstanceDir(task.ChildInstanceID)` (`adapter/filesystem/workspace.go:59-75`) called by the launcher with the RESERVED child id before enqueue; when the task later runs, `Engine.prepareWorkspace` calls `EnsureInstanceDir` for the same id, which is idempotent (`os.MkdirAll`) and keeps the forked content.
- Release: `GroupLauncher` (P2-T8) is the only caller — it releases the snapshot on any pre-launch failure (its prepared-launch cleanup) and, for `results` groups, as soon as every task has been forked and launching has ended (`releaseResultsSnapshot`); a `results_and_files` group keeps it as the Phase 3 merge baseline (released by Phase 3 cleanup). Releasing right after forking is safe because every fork is made before any task is enqueued and nothing else reads a snapshot.
- Provenance probes: `TestGroupWorkspace_SnapshotSurvivesInstanceDirRemoval` fails if snapshots are ever placed inside a directory `WorkspaceManager.RemoveInstanceDir` deletes; `TestNewGroupWorkspace` fails if an empty root (an unwired config value) is accepted; `TestGroupWorkspace_SnapshotInputErrors` fails if a missing parent directory silently yields an empty snapshot.

### Task 5: Group workspace snapshot/fork
**Files:**
- Create: `domain/instance/group_snapshot.go`
- Create: `domain/engine/group_workspace.go`
- Create: `adapter/filesystem/group_workspace.go`
- Test: `domain/engine/group_workspace_test.go` (package `engine`)
- Test: `adapter/filesystem/group_workspace_test.go` (package `filesystem_test`, `//go:build !windows` — the same platform split as `adapter/process/procgroup_unix.go`; the precedent for an external test package asserting an engine port is `adapter/filesystem/file_reader_test.go:18`)

(No existing file is modified. Name collisions checked: `adapter/filesystem/workflow_repo.go:536` already has `copyFile` and `workflow_repo_test.go:58` a `copyTree` test helper, so the adapter's helpers are `copyWorkingTree` / `copyFileContent`.)

#### Cycle A — port and sentinel

- [x] Step 1: Write the failing sentinel test. Create `domain/engine/group_workspace_test.go`:

```go
package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

// The engine sentinel and the instance sentinel are ONE error value, so an
// adapter that wraps instance.ErrUnsupportedSnapshotContent (it cannot import
// this package) is recognized by engine code testing
// ErrUnsupportedSnapshotContent, and vice versa.
func TestErrUnsupportedSnapshotContentIsTheInstanceSentinel(t *testing.T) {
	assert.Equal(t, "unsupported content in working area snapshot", ErrUnsupportedSnapshotContent.Error())

	fromAdapter := fmt.Errorf("filesystem: snapshot /ws: %w: %q is a symlink", instance.ErrUnsupportedSnapshotContent, "leak")
	assert.True(t, errors.Is(fromAdapter, ErrUnsupportedSnapshotContent))

	fromEngine := fmt.Errorf("prepare group: %w", ErrUnsupportedSnapshotContent)
	assert.True(t, errors.Is(fromEngine, instance.ErrUnsupportedSnapshotContent))

	assert.False(t, errors.Is(errors.New("unsupported content in working area snapshot"), ErrUnsupportedSnapshotContent),
		"identity, not message text, is what matches")
}
```

- [x] Step 2: Run it, confirm it fails.

```bash
go test ./domain/engine/ -run TestErrUnsupportedSnapshotContentIsTheInstanceSentinel -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_workspace_test.go:17:66: undefined: ErrUnsupportedSnapshotContent
domain/engine/group_workspace_test.go:19:86: undefined: instance.ErrUnsupportedSnapshotContent
domain/engine/group_workspace_test.go:20:40: undefined: ErrUnsupportedSnapshotContent
domain/engine/group_workspace_test.go:22:48: undefined: ErrUnsupportedSnapshotContent
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Create `domain/instance/group_snapshot.go`:

```go
package instance

import "errors"

// ErrUnsupportedSnapshotContent is the identity behind
// engine.ErrUnsupportedSnapshotContent (domain/engine/group_workspace.go
// re-exports this exact value, so errors.Is matches either name).
//
// It is declared HERE, in the group-run domain, rather than only in
// domain/engine because the adapters that produce it cannot import
// domain/engine: domain/engine's own internal tests import
// adapter/filesystem (template_include_samples_test.go), so an
// adapter/filesystem -> domain/engine import would be an import cycle.
// domain/instance is the lowest package both sides already depend on, and it
// owns GroupRun.WorkspaceSnapshot, the handle a snapshot produces.
var ErrUnsupportedSnapshotContent = errors.New("unsupported content in working area snapshot")
```

and `domain/engine/group_workspace.go`:

```go
package engine

import (
	"context"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupWorkspace is the working-area half of a parallel_group launch: it takes
// ONE immutable copy of the parent's working area at the group's launch
// boundary and materializes an independent, writable copy of it for every
// task BEFORE that task is enqueued.
//
// Both group forms and both gather modes use it: a results-only group still
// gives every task the parent's files (relative layout and permissions
// preserved), so a task never starts in an empty directory and never writes
// into the parent's mutable area. Copies are by CONTENT — never hard links,
// writable symlinks out of the area, or Git metadata that points back into
// the parent's worktree — so no task can observe or alter another task's
// files, the parent's files, or the snapshot.
//
// Phase 2 implementation: adapter/filesystem.GroupWorkspaceAdapter (plain
// recursive copies). Phase 3 replaces it with the Git-backed WorkingArea
// (adapter/git), which embeds this interface and adds finalize/merge.
type GroupWorkspace interface {
	// SnapshotParent captures an immutable copy of parentDir (layout +
	// permissions) for groupRunID and returns an opaque handle for it. Content
	// the snapshot cannot represent safely fails with an error wrapping
	// ErrUnsupportedSnapshotContent that names the offending path.
	SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error)
	// ForkTask materializes an independent writable copy of the snapshot at
	// taskDir. taskDir may already exist but must then be empty.
	ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error
	// ReleaseSnapshot deletes the snapshot. An unknown (already released)
	// handle is not an error.
	ReleaseSnapshot(ctx context.Context, snapshotHandle string) error
}

// ErrUnsupportedSnapshotContent is wrapped by GroupWorkspace.SnapshotParent
// when the parent's working area contains something a task copy cannot
// reproduce without sharing state with the parent: an absolute or escaping
// symlink, a Git worktree/submodule pointer (a ".git" FILE), or a special
// file (device, socket, named pipe). It is a pre-launch failure: the group
// launcher (P2-T8) wraps it in ErrGroupPreLaunch so the node's ErrorBehavior
// applies and no task is ever started.
//
// It is the SAME error value as instance.ErrUnsupportedSnapshotContent (see
// domain/instance/group_snapshot.go for why the identity lives there):
// adapters wrap the instance name, domain code may test either.
var ErrUnsupportedSnapshotContent = instance.ErrUnsupportedSnapshotContent
```

- [x] Step 4: Run it, confirm it passes.

```bash
go test ./domain/engine/ -run TestErrUnsupportedSnapshotContentIsTheInstanceSentinel -count=1 -v
```
Expected:
```
--- PASS: TestErrUnsupportedSnapshotContentIsTheInstanceSentinel (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.3XXs
```

#### Cycle B — filesystem adapter

- [x] Step 5: Write the failing adapter tests. Create `adapter/filesystem/group_workspace_test.go`:

```go
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
	// An independent repository: a .git DIRECTORY is ordinary content.
	writeFile(t, filepath.Join(parent, "repo", ".git", "HEAD"), "ref: refs/heads/main\n", 0o644)
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
	assert.Equal(t, "ref: refs/heads/main\n", got["repo/.git/HEAD"].data, "a .git directory is copied as content")

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

func TestGroupWorkspace_GitWorktreePointerFileIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "instances", uuid.New().String())
	writeFile(t, filepath.Join(parent, "work", ".git"), "gitdir: /elsewhere/src/.git/worktrees/work\n", 0o644)
	writeFile(t, filepath.Join(parent, "work", "main.go"), "package main\n", 0o644)

	assertUnsupported(t, newGW(t, root), parent, "work/.git", "file-gathering backend")
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
```

- [x] Step 6: Run them, confirm they fail.

```bash
go test ./adapter/filesystem/ -run 'TestNewGroupWorkspace|TestGroupWorkspace' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/adapter/filesystem_test [github.com/arbitron/arbitron/adapter/filesystem.test]
adapter/filesystem/group_workspace_test.go:30:44: undefined: filesystem.GroupWorkspaceAdapter
adapter/filesystem/group_workspace_test.go:124:51: undefined: filesystem.GroupWorkspaceAdapter
adapter/filesystem/group_workspace_test.go:126:24: undefined: filesystem.NewGroupWorkspace
...
FAIL	github.com/arbitron/arbitron/adapter/filesystem [build failed]
```

- [x] Step 7: Implement the adapter. Create `adapter/filesystem/group_workspace.go`:

```go
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupWorkspaceAdapter implements engine.GroupWorkspace
// (domain/engine/group_workspace.go) with plain recursive directory copies.
// It wraps instance.ErrUnsupportedSnapshotContent — the same value as
// engine.ErrUnsupportedSnapshotContent — because this package cannot import
// domain/engine (domain/engine's internal tests import this package).
//
// LAYOUT. Snapshots live under <root>/.group-snapshots/<groupRunID>/, a
// SIBLING of the WorkspaceManagerAdapter's <root>/instances/ tree — never
// inside an instance directory — so RemoveInstanceDir on the parent (or on any
// task) can never delete a snapshot that later forks or a nested group still
// need. The store and every snapshot directory are mode 0o700, matching the
// instance-directory security invariant (workspace.go).
//
// HANDLE. The handle returned by SnapshotParent is the snapshot's absolute
// directory path. ForkTask and ReleaseSnapshot accept ONLY a clean absolute
// path whose parent is exactly the snapshot store and whose base name is a
// canonical UUID, so a corrupt or hostile handle (e.g. "<store>/../instances")
// can never make them read or delete anything else.
//
// IMMUTABILITY. A snapshot is never handed to a task: its path exists only in
// the group run record (GroupRun.WorkspaceSnapshot) and is passed only to
// ForkTask / ReleaseSnapshot. Every fork is a fresh byte-for-byte copy (no
// hard links, no reflinks), so writes in a fork, in the parent, or in another
// fork cannot reach it. The snapshot keeps the source's permission bits —
// rather than being made read-only — because those bits ARE the content the
// forks must reproduce; read-only bits would add no real boundary (anything
// running as the server's OS user could chmod them back) while forcing a
// separate mode manifest.
//
// SUPPORTED CONTENT. Regular files (content + permission bits), directories
// (including empty ones, with their permission bits), a ".git" DIRECTORY
// (copied as ordinary content: the fork gets an independent repository), and
// RELATIVE symlinks that resolve inside the working area (recreated verbatim,
// so they resolve inside the fork). Everything else fails with
// instance.ErrUnsupportedSnapshotContent naming the path:
//   - an absolute symlink, or a relative one that resolves outside the area
//     (the fork would share the target with the parent);
//   - a dangling symlink containing ".." (its eventual target cannot be
//     proven to stay inside), or one that cannot be resolved (a loop);
//   - a ".git" FILE — a linked git worktree or submodule pointer whose gitdir
//     lives in another repository, so the fork's commits would land in the
//     PARENT's repository;
//   - devices, sockets and named pipes.
//
// Only permission bits (0o777) are preserved; setuid/setgid/sticky bits and
// timestamps are not.
type GroupWorkspaceAdapter struct {
	root         string
	snapshotsDir string
}

// groupSnapshotsDirName is the snapshot store directory under the workspace
// root. The leading dot keeps it out of the way of the flat-store migration
// (which only moves folders containing a workflow.json) and of casual listings.
const groupSnapshotsDirName = ".group-snapshots"

// snapshotDirMode is the mode of the store and of every snapshot root.
const snapshotDirMode os.FileMode = 0o700

// ErrInvalidSnapshotHandle is returned by ForkTask / ReleaseSnapshot for a
// handle that is not a snapshot of this adapter.
var ErrInvalidSnapshotHandle = errors.New("invalid group snapshot handle")

// NewGroupWorkspace creates a GroupWorkspaceAdapter rooted at root (the same
// workspace root the WorkspaceManagerAdapter uses: cfg.Workspace.Path). root
// must be non-empty; it is resolved to an absolute path. Nothing is created
// on disk until the first snapshot.
func NewGroupWorkspace(root string) (*GroupWorkspaceAdapter, error) {
	if root == "" {
		return nil, fmt.Errorf("filesystem: group workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: resolve group workspace root: %w", err)
	}
	return &GroupWorkspaceAdapter{root: abs, snapshotsDir: filepath.Join(abs, groupSnapshotsDirName)}, nil
}

// SnapshotsDir returns the absolute snapshot store directory.
func (g *GroupWorkspaceAdapter) SnapshotsDir() string { return g.snapshotsDir }

// SnapshotParent implements engine.GroupWorkspace.
//
// The copy is built in <store>/.tmp-<groupRunID> and renamed into place only
// when complete, so a crash or a rejected entry never leaves a half-built
// snapshot at the handle path. Re-snapshotting the same groupRunID (a retried
// launch that never persisted its run) replaces the previous snapshot.
func (g *GroupWorkspaceAdapter) SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	if groupRunID == uuid.Nil {
		return "", fmt.Errorf("filesystem: snapshot parent: group run id is required")
	}
	if parentDir == "" || !filepath.IsAbs(parentDir) {
		return "", fmt.Errorf("filesystem: snapshot parent: parent directory %q must be an absolute path", parentDir)
	}
	parentDir = filepath.Clean(parentDir)
	info, err := os.Stat(parentDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("filesystem: snapshot parent: %s is not a directory", parentDir)
	}
	if err := g.ensureStore(); err != nil {
		return "", err
	}
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: resolve %s: %w", parentDir, err)
	}
	realStore, err := filepath.EvalSymlinks(g.snapshotsDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: resolve snapshot store: %w", err)
	}
	if within(realParent, realStore) || within(realStore, realParent) {
		return "", fmt.Errorf("filesystem: snapshot parent: %s overlaps the snapshot store %s", parentDir, g.snapshotsDir)
	}

	final := filepath.Join(g.snapshotsDir, groupRunID.String())
	tmp := filepath.Join(g.snapshotsDir, ".tmp-"+groupRunID.String())
	if err := removeTree(tmp); err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: clear stale build dir: %w", err)
	}
	if err := os.Mkdir(tmp, snapshotDirMode); err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	if err := os.Chmod(tmp, snapshotDirMode); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}

	check := &contentCheck{root: parentDir, realRoot: realParent}
	if err := copyWorkingTree(ctx, parentDir, tmp, check); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot %s: %w", parentDir, err)
	}
	if err := removeTree(final); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: replace previous snapshot: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	return final, nil
}

// ForkTask implements engine.GroupWorkspace. taskDir must be absolute and
// outside the snapshot store; if it exists it must be an EMPTY directory (a
// non-empty one would mix stale content into the task's starting copy). The
// fork root keeps taskDir's own mode — an instance directory's 0o700 — and
// everything below it mirrors the snapshot. A failed fork removes whatever it
// wrote.
func (g *GroupWorkspaceAdapter) ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error {
	snap, err := g.resolveHandle(snapshotHandle)
	if err != nil {
		return fmt.Errorf("filesystem: fork task: %w", err)
	}
	if info, err := os.Stat(snap); err != nil || !info.IsDir() {
		return fmt.Errorf("filesystem: fork task: group snapshot %s does not exist (released or never taken)", snapshotHandle)
	}
	if taskDir == "" || !filepath.IsAbs(taskDir) {
		return fmt.Errorf("filesystem: fork task: task directory %q must be an absolute path", taskDir)
	}
	taskDir = filepath.Clean(taskDir)
	if within(g.snapshotsDir, taskDir) {
		return fmt.Errorf("filesystem: fork task: task directory %s is inside the snapshot store", taskDir)
	}

	created := false
	info, err := os.Stat(taskDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(taskDir, snapshotDirMode); err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		if err := os.Chmod(taskDir, snapshotDirMode); err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		created = true
	case err != nil:
		return fmt.Errorf("filesystem: fork task: %w", err)
	case !info.IsDir():
		return fmt.Errorf("filesystem: fork task: %s is not a directory", taskDir)
	default:
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("filesystem: fork task: task directory %s is not empty (%d entries)", taskDir, len(entries))
		}
	}

	if err := copyWorkingTree(ctx, snap, taskDir, nil); err != nil {
		if created {
			_ = removeTree(taskDir)
		} else {
			_ = removeContents(taskDir)
		}
		return fmt.Errorf("filesystem: fork task into %s: %w", taskDir, err)
	}
	return nil
}

// ReleaseSnapshot implements engine.GroupWorkspace. The empty handle (a
// parent that had no workspace) and an already-released snapshot are no-ops.
func (g *GroupWorkspaceAdapter) ReleaseSnapshot(ctx context.Context, snapshotHandle string) error {
	if snapshotHandle == "" {
		return nil
	}
	snap, err := g.resolveHandle(snapshotHandle)
	if err != nil {
		return fmt.Errorf("filesystem: release snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("filesystem: release snapshot: %w", err)
	}
	if err := removeTree(snap); err != nil {
		return fmt.Errorf("filesystem: release snapshot %s: %w", snapshotHandle, err)
	}
	return nil
}

// ensureStore creates the snapshot store (0o700, umask-proof).
func (g *GroupWorkspaceAdapter) ensureStore() error {
	if err := os.MkdirAll(g.snapshotsDir, snapshotDirMode); err != nil {
		return fmt.Errorf("filesystem: create group snapshot store: %w", err)
	}
	if err := os.Chmod(g.snapshotsDir, snapshotDirMode); err != nil {
		return fmt.Errorf("filesystem: tighten group snapshot store perms: %w", err)
	}
	return nil
}

// resolveHandle validates that handle names a snapshot directory of THIS
// adapter (see the HANDLE note on the type) and returns it.
func (g *GroupWorkspaceAdapter) resolveHandle(handle string) (string, error) {
	if handle == "" {
		return "", fmt.Errorf("%w: empty handle (the parent had no working area)", ErrInvalidSnapshotHandle)
	}
	if !filepath.IsAbs(handle) || filepath.Clean(handle) != handle || filepath.Dir(handle) != g.snapshotsDir {
		return "", fmt.Errorf("%w: %q is not inside %s", ErrInvalidSnapshotHandle, handle, g.snapshotsDir)
	}
	base := filepath.Base(handle)
	id, err := uuid.Parse(base)
	if err != nil || id.String() != base {
		return "", fmt.Errorf("%w: %q does not name a group run snapshot", ErrInvalidSnapshotHandle, handle)
	}
	return handle, nil
}

// within reports whether p is root or lies below it. Both must be clean.
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// contentCheck validates source entries while a SNAPSHOT is taken. Forks copy
// an already-validated snapshot and run without it.
type contentCheck struct {
	root     string // lexical parent directory (clean, absolute)
	realRoot string // the same directory with symlinks resolved
}

func unsupported(rel, detail string) error {
	return fmt.Errorf("%w: %q %s", instance.ErrUnsupportedSnapshotContent, rel, detail)
}

// symlink accepts a relative link that stays inside the working area (see the
// SUPPORTED CONTENT note on GroupWorkspaceAdapter).
func (c *contentCheck) symlink(path, rel, target string) error {
	if filepath.IsAbs(target) {
		return unsupported(rel, fmt.Sprintf("is a symlink to the absolute path %q; a task copy would share it with the parent — make the link relative to a path inside the working area, or replace it with a copy", target))
	}
	if !within(c.root, filepath.Join(filepath.Dir(path), target)) {
		return unsupported(rel, fmt.Sprintf("is a symlink to %q, which points outside the working area; replace it with a copy of the target", target))
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		for _, part := range strings.Split(filepath.ToSlash(target), "/") {
			if part == ".." {
				return unsupported(rel, fmt.Sprintf("is a dangling symlink to %q; a dangling link containing \"..\" cannot be proven to stay inside the working area — remove it or point it at an existing path", target))
			}
		}
		return nil
	}
	if err != nil {
		return unsupported(rel, fmt.Sprintf("is a symlink that cannot be resolved (%v); remove it or fix the link loop", err))
	}
	if !within(c.realRoot, resolved) {
		return unsupported(rel, fmt.Sprintf("is a symlink to %q, which resolves outside the working area (to %s); replace it with a copy of the target", target, resolved))
	}
	return nil
}

// gitFile rejects a ".git" regular file: a linked worktree's or submodule's
// pointer to a gitdir in ANOTHER repository.
func gitFile(rel string) error {
	return unsupported(rel, "is a git worktree or submodule pointer (a \".git\" file whose repository lives elsewhere); "+
		"git worktree metadata is supported only by the file-gathering backend — for a results-only group, use a regular clone "+
		"(a \".git\" directory) inside the working area")
}

type dirMode struct {
	path string
	perm os.FileMode
}

// copyWorkingTree copies src's CONTENTS into the existing directory dst. Directories
// are created owner-writable and get their real permission bits only after
// everything below them is written (deepest first), so read-only source
// directories copy correctly. When check is non-nil every entry is validated
// first (snapshot); ctx is checked before every entry.
func copyWorkingTree(ctx context.Context, src, dst string, check *contentCheck) error {
	var dirs []dirMode
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.Mkdir(target, snapshotDirMode); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{path: target, perm: mode.Perm()})
			return nil
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if check != nil {
				if err := check.symlink(path, rel, link); err != nil {
					return err
				}
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			if check != nil && d.Name() == ".git" {
				return gitFile(rel)
			}
			return copyFileContent(path, target, mode.Perm())
		default:
			return unsupported(rel, fmt.Sprintf("is a special file (%s); only regular files, directories and relative symlinks can be copied into a task's working area", mode.Type()))
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].perm); err != nil {
			return err
		}
	}
	return nil
}

// copyFileContent copies one regular file by CONTENT (never a hard link) and applies
// perm explicitly so the umask cannot relax or tighten it.
func copyFileContent(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// makeOwnerWritable gives the owner rwx on every directory under root so a
// tree containing read-only directories can be deleted. Symlinks are never
// followed.
func makeOwnerWritable(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(path, snapshotDirMode)
		}
		return nil
	})
}

// removeTree deletes root and everything below it; a missing root is a no-op.
func removeTree(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := makeOwnerWritable(root); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

// removeContents deletes everything inside dir but keeps dir itself.
func removeContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := removeTree(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
```

Implementation notes the tests pin:
- Snapshot root, store and forked task roots are 0o700 (`TestGroupWorkspace_SnapshotAndForkPreserveLayoutAndModes`), matching `instanceDirMode` in `workspace.go:26`; everything below the root mirrors the source's permission bits.
- Directory modes are applied deepest-first AFTER their contents are written, so read-only (0o555) source directories copy; `removeTree` restores owner rwx before `os.RemoveAll` for the same reason. (Consequence worth knowing for P2-T8/P2-T13: a fork that contains a read-only directory cannot be deleted by `WorkspaceManagerAdapter.RemoveInstanceDir`'s bare `os.RemoveAll` — the same pre-existing limitation as any read-only directory an agent creates; `Engine.cleanupWorkspace` only logs it.)
- Symlink checks run against the SOURCE tree at snapshot time (lexical containment, then `filepath.EvalSymlinks` containment against the resolved parent); forks copy an already-validated snapshot verbatim.
- The `.git` FILE rule matches the sample `samples/feature-pipeline/scripts/init-workspace.sh`, which runs `git worktree add` inside `{{workspace}}`: this task initially refuses such a parent to avoid a shared mutable pointer; P2-T17 replaces that rejection with independent metadata before P2-T14 enables Phase 2 execution.

- [x] Step 8: Run the adapter tests, the package, and the race detector; gofmt and vet.

Verification: all commands below passed, including the Windows cross-build and the
broader domain/SQLite/filesystem/CLI tests. Both prescribed TDD cycles were observed
red then green. Self-review completed in order: (1) spec compliance — added failing
regressions and fixes for symlinked handles/store aliases and filesystem-root overlap;
(2) anti-green-theater — real WorkspaceManager directories feed nonempty parent trees,
and assertions check fork bytes/modes/links and unchanged parent/snapshot sinks;
added deterministic cancellation-after-copy-start rollback coverage; (3) code quality —
removed unused adapter state and made containment root-safe. All checks were rerun
after the fixes. Launcher consumption and composition wiring remain assigned to
P2-T8/P2-T14 as specified above; this task does not enable execution.

```bash
go test ./adapter/filesystem/ -run 'TestNewGroupWorkspace|TestGroupWorkspace' -count=1 -v
go test ./adapter/filesystem/ ./domain/engine/ ./domain/instance/ -count=1
go test ./adapter/filesystem/ -run GroupWorkspace -count=1 -race
gofmt -l adapter/filesystem domain/engine/group_workspace.go domain/engine/group_workspace_test.go domain/instance/group_snapshot.go
go vet ./adapter/filesystem/ ./domain/engine/ ./domain/instance/
```
Expected:
```
--- PASS: TestNewGroupWorkspace (0.00s)
--- PASS: TestGroupWorkspace_SnapshotAndForkPreserveLayoutAndModes (0.02s)
--- PASS: TestGroupWorkspace_ForkIntoMissingDirCreatesIt (0.01s)
--- PASS: TestGroupWorkspace_EmptyParentGivesEmptyFork (0.00s)
--- PASS: TestGroupWorkspace_ForksAreIndependent (0.02s)
--- PASS: TestGroupWorkspace_ParentChangesAfterSnapshotAreInvisibleToLaterForks (0.01s)
--- PASS: TestGroupWorkspace_HardLinksAreCopiedByContent (0.00s)
--- PASS: TestGroupWorkspace_SnapshotSurvivesInstanceDirRemoval (0.00s)
--- PASS: TestGroupWorkspace_GitWorktreePointerFileIsRejected (0.00s)
--- PASS: TestGroupWorkspace_AbsoluteSymlinkIsRejected (0.00s)
--- PASS: TestGroupWorkspace_EscapingRelativeSymlinkIsRejected (0.00s)
--- PASS: TestGroupWorkspace_SymlinkEscapingThroughAnotherSymlinkIsRejected (0.00s)
--- PASS: TestGroupWorkspace_DanglingSymlinks (0.00s)
--- PASS: TestGroupWorkspace_SymlinkLoopIsRejected (0.00s)
--- PASS: TestGroupWorkspace_SpecialFileIsRejected (0.00s)
--- PASS: TestGroupWorkspace_SnapshotInputErrors (0.00s)
--- PASS: TestGroupWorkspace_CancelledContextLeavesNoSnapshot (0.00s)
--- PASS: TestGroupWorkspace_ForkErrors (0.01s)
--- PASS: TestGroupWorkspace_ReleaseSnapshot (0.01s)
--- PASS: TestGroupWorkspace_TraversalHandlesAreRejected (0.01s)
--- PASS: TestGroupWorkspace_ResnapshotSameRunReplaces (0.01s)
PASS
ok  	github.com/arbitron/arbitron/adapter/filesystem	0.4XXs
```
then `ok` for each package of the second and third commands; `gofmt -l` and `go vet` print nothing. Finally confirm the module still builds for every distribution target's OS family (the adapter must stay portable even though its tests are POSIX-only):

```bash
GOOS=windows GOARCH=amd64 go build ./adapter/filesystem/ ./domain/engine/
go test ./domain/... ./adapter/sqlite/ ./adapter/filesystem/ ./cmd/... -count=1
```
Expected: no output from the cross-build; every package `ok` (a full `go test ./...` additionally shows the pre-existing, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles`).

- [x] Step 9: Commit.

```bash
git add domain/instance/group_snapshot.go domain/engine/group_workspace.go domain/engine/group_workspace_test.go adapter/filesystem/group_workspace.go adapter/filesystem/group_workspace_test.go
git commit -m "feat(engine,filesystem): group workspace snapshot/fork port and filesystem copy adapter" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
