# Parallel Workflow Gathering — Phase 3 Task 11: Composition-root wiring of file gathering and end-to-end proof

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make `results_and_files` live in `cmd/arbitron/main.go:initDomain` — git working area, terminal finalizer, gatherer, resolution bridge, abandon/handler gate, gather reconciler (startup, sweep, event watch), resource pruner and `SetGroupSupport(GroupSupportResultsAndFiles)` — and prove every line through the real stack with real git in temp directories.
**Architecture:** Two edits to `initDomain` around P2-T14's `12c-group` block: (1) right after the config/DB boot, the workspace root is canonicalized (symlinks resolved) so the git area's staging paths, `{{workspace}}` and the claude-cli WorkDir pin all agree; (2) the git `WorkingArea` is built beside P2-T14's `groupWS` and handed to the launcher (`SetFileWorkingArea`) and finalizer, and a new `12d-group-files` block after the conversation handler (the bridge needs `interactiveStreamSel`, built at 12c) wires bridge → handler gate → gatherer → executor → pruner → reconciler, then raises the support level. Startup runs the gather reconcile after the group coordinator's pass; the existing group sweep context also drives a second `sweepGroupReconciler` goroutine and `Watch`. `cmd/arbitron/group_files_e2e_test.go` boots `initDomain` on a SYMLINKED workspace root with a fake `claude` binary (the resolver profile's stream) and asserts sinks: files in the parent's working area (read back by a downstream node into persisted variables), persisted gather reports and session rows, notifications, run state, and on-disk pruning.
**Tech Stack:** Go 1.25.5, SQLite, real `git` and real `sh` `tool_call` nodes, a `#!/bin/sh` stand-in for the claude CLI, testify.
**Spec / Design:** spec "Gathering file changes" (all bullets), "Existing behavior and cancellation", "Completed runs retain task outcomes, gathered results, and merge-resolution outcomes"; `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` "Integration and plan obligations" ("Implement P3-A–D and their P2 launcher/terminal/repository seams, then enable file mode"), §4 (bridge, abandon, cancellation), §5 (cleanup/recovery), Testing (happy/success/failure/error/edge lists).

## Additional real-stack automatic-resolution sink proof

Add `TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator`: the configured fake agent
binary must actually edit and stage the conflicted file in its received staging directory
(and inspect `MERGE_HEAD` / `MERGE_HEAD^`, proving both prompt refs exist). Boot through
`initDomain`, never call `EndSession`, and assert a downstream node reads the combined file
bytes into persisted parent variables, the report records a resolved attempt, and no human
conflict notification is stored. A resolver that merely prints a success claim while
leaving conflicts must still wait and notify. A failed apply after successful tasks must
reach the downstream failure sink with final `outcome=failure` and `taskOutcome=success`.

Replace race-dependent SKIPs in first-success/restart fixtures with test-controlled,
bounded process gates outside the instance workspaces: observe the loser waiting at its
gate, wait for the parent sink, then release the loser and assert its bytes never reach
the parent. Keep only explicit missing-host-tool/platform skips, never timing skips.


## Starting-copy and rollback permission sink

Extend the real-stack files fixtures with a 0600 file, an executable and a read-only
subdirectory. Task scripts report inherited modes into persisted outputs; after gathering,
a downstream script reports parent modes. On the forced failure path, assert pre-gather
bytes AND modes. These inputs come from the actual parent workspace, not an adapter fake;
P3-T2 captures them in manifests and P3-T3 restores/merges them.


**Interfaces**
- Consumes (composition-root constructors and setters; all by their defining task):
  - P3-T2 `gitarea.NewWorkingArea(root string, opts ...Option) (*gitarea.WorkingArea, error)` (import `gitarea "github.com/arbitron/arbitron/adapter/git"`); `*gitarea.WorkingArea` satisfies `engine.WorkingArea` (P3-T3), `engine.GatherPreflighter` and `engine.SnapshotInventory` (P3-T10).
  - P3-T5 `engine.NewGroupFileFinalizer(groups instance.GroupRunRepository, area WorkingArea, workspaces WorkspaceManager) *GroupFileFinalizer`; `(*engine.Engine).SetGroupTaskFinalizer(f GroupTaskFinalizer)`.
  - P3-T6 `engine.NewGroupGatherer(groups instance.GroupRunRepository, area WorkingArea, bridge GroupResolutionStarter, bus EventBus) *FileGatherer`.
  - P3-T8 `engine.NewGroupResolutionBridge(groups, instances, nodeExecs, resolver provider.ProfileResolver, selector InteractiveStreamSelector, files FileReader, notifier ResolutionNotifier, bus EventBus, waker instance.NodeAddressedWaker, queueMgr QueueManager) *GroupResolutionBridge`; `(*conversationHandler).SetGroupResolution(groups instance.GroupRunRepository, completer GroupResolutionCompleter)` reached by type assertion; `notification.TypeConflictResolution`; `engine.IsOpenGroupResolutionSession`.
  - P3-T9 `engine.ConversationHandler.AbandonSession(ctx context.Context, instanceID uuid.UUID) error`.
  - P3-T10 `(*engine.GroupLauncher).SetFileWorkingArea(area WorkingArea)`, `engine.NewGroupResourcePruner(groups, instances, children ChildLister, workspaces WorkspaceManager, area WorkingArea, inventory SnapshotInventory) *GroupResourcePruner`, `engine.NewGroupGatherReconciler(groups, instances, nodeExecs, aborter GatherAborter, closer GroupSessionCloser, completer GroupResolutionCompleter, waker instance.NodeAddressedWaker, queueMgr QueueManager, locker GatherLocker, bus EventBus, pruner *GroupResourcePruner) *GroupGatherReconciler`, `(*GroupGatherReconciler).Reconcile`, `.Watch(ctx) <-chan struct{}`, `(*engine.ParallelGroupExecutor).TryLockGather` (makes `groupExecutor` the `GatherLocker`).
  - P1-T9 `(*engine.Engine).SetGroupSupport`, `GroupSupportLevel`, `engine.GroupSupportResultsAndFiles`, `engine.ErrGroupExecutionUnsupported`.
  - P2-T14 (`cmd/arbitron/main.go` as P2-T14 leaves it): locals `groupRunRepo`, `groupWS`, `groupCoord`, `groupLauncher`, `groupExecutor`, `groupSweepCtx`; `var groupSweepInterval`; `type groupReconciler interface{ Reconcile(ctx context.Context) error }`; `func sweepGroupReconciler(ctx, groups groupReconciler, interval time.Duration, log *slog.Logger)`; `domainStack.groupRunRepo`, `domainStack.groupSweepCancel`. Test helpers (`cmd/arbitron/group_e2e_test.go`): `groupStackPaths{root, workspaceDir, configPath}`, `triggerGroupWorkflow`, `waitForTerminalState`, `parentVariables`, `groupOutputOf`, `groupTaskByKey`, `resultOf`, `groupRunOf`, `groupRosterTask`, `groupBranchesManifest`; and the P2-T14 version of `cmd/arbitron/group_trigger_e2e_test.go:TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering` (replaced here).
  - P2-T15 `cmd/arbitron/group_behaviors_e2e_test.go`: `bootGroupExecStack`, manifests `filesNestedManifest`, `leafChildManifest`, `filesParentManifest`, `filesChildManifest`, and the two Phase 2 refusal tests `TestGroupBehaviorsE2E_ResultsAndFilesRefusedAtTrigger`, `TestGroupBehaviorsE2E_ResultsAndFilesChildWorkflowFailsLaunch` (replaced here).
  - P1-T8 / P1-T9 helpers: `bootGroupAuthoringStack`, `filesModeGroup`, `newGroupMCPServer`, `callMCPTool`, `mcpText` (`group_authoring_e2e_test.go`), `instancesNamed` (`group_trigger_e2e_test.go`).
  - Existing `package main` helpers: `writeWorkflow` (`spawn_e2e_test.go:22`), `adminCtxForLocalUser` (`spawn_e2e_test.go:87`), `childrenOf` (`spawn_e2e_test.go:99`), `waitForState` / `branchVariable` (`spawn_wait_e2e_test.go:131/140`), `initDomain`, `domainStack` fields `eng`, `db`, `instanceRepo`, `nodeExecRepo`, `notifRepo`, `conversationHandler`.
- Produces:
  - `cmd/arbitron/main.go`: `func canonicalWorkspacePath(p string) (string, error)`; the wiring lines listed below.
  - Test helpers in `cmd/arbitron/group_files_e2e_test.go`: `requireHostGit`, `fakeResolverScript`, `writeFilesStack`, `bootFilesStack`, `canonicalRoot`, `backingDirOf`, `taskWorkspaceDir`, `pathExists`, `checkOutput`, `waitForRunState`, `waitForOpenResolution`, `stagingDirOf`, `gitIn`, `resolveConflictInStaging`, `loseSessionEndWake`, `groupSessionRows`, `hasNotification`, `gatherTaskReport`; manifests `filesAllManifest`, `filesConflictManifest`, `filesFirstManifest`, `filesFailedManifest`, `filesNestedGatherManifest`, `filesBadRepoManifest`.

**Wiring & data provenance**
- This IS the composition-root task for P3-T1…P3-T10. Lines added/changed in `cmd/arbitron/main.go:initDomain` and the e2e assertion that fails when each is deleted (every test boots the real stack):

| # | Line | Fails without it |
|---|---|---|
| 1 | `canonicalWS, err := canonicalWorkspacePath(cfg.Workspace.Path)` + `cfg.Workspace.Path = canonicalWS` | `TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved` (symlinked root: the staging dir is under the resolved root while claude-cli pins WorkDir lexically to `filepath.Abs(cfg.Workspace.Path)` (main.go:567, `adapter/claudecli/shared.go:71-88`), so the resolver stream is refused with `ErrWorkDirEscapesRoot`, the gather aborts and `waitForOpenResolution` times out); `TestCanonicalWorkspacePath_ResolvesSymlinksAndCreatesTheRoot` |
| 2 | `workingArea, err := gitarea.NewWorkingArea(cfg.Workspace.Path)` | compile error (lines 3, 4, 7–9 use it) |
| 3 | `groupLauncher.SetFileWorkingArea(workingArea)` | `TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder`: pre-launch "needs a file-gathering working area" ⇒ parent `failed`, expected `completed` |
| 4 | `eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr))` | same test: no `FinalizedRef` ⇒ the gather aborts ("has no finalized working-area ref") ⇒ `branch == "failure"`, expected `success` |
| 5 | `groupResolution := engine.NewGroupResolutionBridge(...)` | compile error (6, 7, 9 use it) |
| 6 | `SetGroupResolution(groupRunRepo, groupResolution)` on the concrete handler | `TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved`: `EndSession` returns "conversation: node group-1 is not interactive (type: parallel_group)"; `…ConflictAbandon…`: `AbandonSession` errors |
| 7 | `gatherer := engine.NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)` + `groupExecutor.SetGatherer(gatherer)` | `…AllSucceed…`: executor refuses with `ErrGroupExecutionUnsupported` ⇒ parent `failed` |
| 8 | `groupPruner := engine.NewGroupResourcePruner(...)` | compile error (9 uses it); its effect: `…AllSucceed…` asserts the backing dir and task worktrees disappear within 20s (sweep is 60s) |
| 9 | `gatherReconciler := engine.NewGroupGatherReconciler(...)` | compile error (10–12 use it) |
| 10 | `eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)` (replacing P2-T14's `GroupSupportResults`) | `TestGroupTriggerE2E_Phase3RunsBothGatherModes` (level assertion) and every `triggerGroupWorkflow` of a files group (`ErrGroupExecutionUnsupported`) |
| 11 | startup `gatherReconciler.Reconcile(context.Background())` | `TestGroupFilesE2E_RestartRestoresAnInterruptedResolution`: the parent stays `waiting` past 40s |
| 12 | `go sweepGroupReconciler(groupSweepCtx, gatherReconciler, groupSweepInterval, logger)` | `TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost` (interval 1s): never woken |
| 13 | `gatherReconciler.Watch(groupSweepCtx)` | `TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled`: the run is not `done` within 20s; `…AllSucceed…` pruning assertion |

- Exact producers of every constructor argument: `cfg.Workspace.Path` (`adapter/config` loader, canonicalized by line 1); `groupRunRepo` = `adaptsqlite.NewGroupRunRepo(db)` (P2-T14); `instanceRepo` (main.go:373, also `ChildLister` via P2-T12 `ListChildren`); `nodeExecRepo` (main.go:376); `wsMgr` (main.go:814); `profileResolver` (main.go:700); `interactiveStreamSel` (main.go:1028-1032); `fileReader` (main.go:466); `eng` (main.go:800, `*Engine` is the `ResolutionNotifier` via P3-T8 `EmitNotification`); `bus` (main.go:618); `queueRepo` (main.go:797, `NodeAddressedWaker` via P2-T4); `queueMgr` (main.go:830); `conversationHandler` (main.go:1044-1046); `groupExecutor` (P2-T14, the `GatherLocker`); `gatherer` is both the executor's `GroupGatherer` and the reconciler's `GatherAborter`; `groupResolution` is the gatherer's `GroupResolutionStarter`, the handler's `GroupResolutionCompleter`, and the reconciler's `GroupSessionCloser` + completer.
- Runtime producers the e2e exercises: task files — real `sh` `tool_call` nodes writing into `{{workspace}}` (`adapter/process/executor_toolcall.go`); the resolver stream — `claudecli.NewStreamAgentAdapter(agentCfg)` (main.go:994) running the fake binary configured through `llm.binarypath` (`cfg.LLM.BinaryPath`, main.go:595); the `resolver` agent profile — `profiles:` seeded by `SeedProfiles` (main.go:681); human turns — `ds.conversationHandler.EndSession` / `AbandonSession` (the same handler MCP/HTTP/WS call); cancellation — `ds.eng.Cancel` (`engine.go:1530`), whose `instance.cancelled` event feeds `Watch`.
- Disagreement noted: P3-T2's wiring note says the launcher receives `workingArea` *instead of* P2-T5's `filesystem.NewGroupWorkspace`. P3-T10 (which defines `SetFileWorkingArea`) keeps `groupWS` for results-only groups so they never require git; this task wires both.

### Task 11.1: Canonical workspace root
**Files:**
- Modify: `cmd/arbitron/main.go` — add `canonicalWorkspacePath` after `newOutboundHTTPClient` (ends at line 335); insert the canonicalization immediately before `// 6. Create filesystem-based repos.` (line 464).
- Test: `cmd/arbitron/workspace_root_test.go` (package `main`)

- [x] Step 1: Write the failing test. Create `cmd/arbitron/workspace_root_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalWorkspacePath_ResolvesSymlinksAndCreatesTheRoot(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(real, 0o700))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(real, link))
	want, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	got, err := canonicalWorkspacePath(link)
	require.NoError(t, err)
	assert.Equal(t, want, got, "a symlinked workspace root is resolved once, at boot")

	got, err = canonicalWorkspacePath(filepath.Join(link, "nested", "ws"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(want, "nested", "ws"), got)
	info, err := os.Stat(got)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "a missing root is created")
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	_, err = canonicalWorkspacePath("")
	assert.Error(t, err)
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./cmd/arbitron/ -run 'TestCanonicalWorkspacePath' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/cmd/arbitron [github.com/arbitron/arbitron/cmd/arbitron.test]
cmd/arbitron/workspace_root_test.go:...: undefined: canonicalWorkspacePath
FAIL	github.com/arbitron/arbitron/cmd/arbitron [build failed]
```

- [x] Step 3: Implement. In `cmd/arbitron/main.go`, immediately after the closing `}` of `newOutboundHTTPClient` (line 335), add:

```go
// canonicalWorkspacePath returns the absolute, symlink-free form of the
// configured workspace root, creating it (0o700) when missing. Every consumer
// of the root must see the SAME spelling: the git working area resolves
// symlinks for its staging areas (P3-T2/T3), while the claude-cli adapters pin
// each request's WorkDir lexically under the root (adapter/claudecli
// validateWorkDir). Resolving once at boot makes {{workspace}}, the staging
// areas and the pin agree on hosts where the root sits behind a symlink.
func canonicalWorkspacePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("workspace path is empty")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path %q: %w", p, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create workspace root %q: %w", abs, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root %q: %w", abs, err)
	}
	return resolved, nil
}
```

Immediately before the line `// 6. Create filesystem-based repos.` (line 464), insert:

```go
	// 5c. Canonicalize the workspace root (symlinks resolved) BEFORE any
	// filesystem adapter is built, so the workflow store, the file reader, the
	// per-instance workspace manager, the claude-cli WorkDir pin and the git
	// working area (12c-group) all use one spelling of the same directory.
	canonicalWS, err := canonicalWorkspacePath(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	cfg.Workspace.Path = canonicalWS
```

(`errors`, `os` and `path/filepath` are already imported by `main.go`, lines 8, 13 and 15.)

- [x] Step 4: Run, confirm it passes, and that the existing composition-root tests still boot.

```bash
go test ./cmd/arbitron/ -run 'TestCanonicalWorkspacePath|TestSpawnE2E|TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected:
```
--- PASS: TestCanonicalWorkspacePath_ResolvesSymlinksAndCreatesTheRoot (0.00s)
--- PASS: TestSpawnE2E_FireAndForgetCreatesLinkedChildThroughCompositionRoot (X.XXs)
--- PASS: TestSpawnE2E_ParentContinuesAndCompletes (X.XXs)
--- PASS: TestSpawnE2E_ChildlessRunIsARootWithNoChildren (X.XXs)
--- PASS: TestSpawnWaitE2E_CompletedChildRoutesParentThroughSuccess (X.XXs)
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```

- [x] Step 5: gofmt, vet, commit.

Canonical-root red/green: missing helper failed compilation; focused canonical/spawn gates pass. Self-review in order: (1) only the specified canonicalization and boot call added; (2) the test asserts actual symlink resolution, created directory and permissions, and existing roots boot through initDomain; the file-session sink below will additionally exercise provider pinning; (3) matches boot cleanup/error conventions. gofmt, vet and diff checks pass.

```bash
gofmt -l cmd/arbitron/main.go cmd/arbitron/workspace_root_test.go
go vet ./cmd/arbitron/
git add cmd/arbitron/main.go cmd/arbitron/workspace_root_test.go && git commit -m "fix(arbitron): canonicalize the workspace root at boot so staging paths and WorkDir pins agree" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `gofmt -l`/`go vet` print nothing.

### Task 11.2: Wire file gathering into initDomain and prove it end to end
**Files:**
- Modify: `cmd/arbitron/main.go` — import block (lines 21-50: add `gitarea` after `"github.com/arbitron/arbitron/adapter/filesystem"`, line 30); P2-T14's `12c-group` block (inserted after line 949: after its `groupWS` error check, after `groupLauncher := …`, after `eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor)`, and its `eng.SetGroupSupport(engine.GroupSupportResults)` statement); a new `12d-group-files` block after the `SetAuditRecorder` type-assertion block (ends line 1063) and before `// 12e. SEAL the executor set.` (line 1065); P2-T14's `14-group` startup block (after line 1113); P2-T14's `14-group-sweep` lines (after line 1136).
- Modify: `cmd/arbitron/group_trigger_e2e_test.go` (P1-T9, as replaced by P2-T14) — replace `TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering` with its Phase 3 version.
- Modify: `cmd/arbitron/group_behaviors_e2e_test.go` (P2-T15) — replace `TestGroupBehaviorsE2E_ResultsAndFilesRefusedAtTrigger` and `TestGroupBehaviorsE2E_ResultsAndFilesChildWorkflowFailsLaunch` with their Phase 3 versions.
- Create: `cmd/arbitron/group_files_e2e_test.go`

- [x] Step 1: Write the failing composition-root e2e tests. Create `cmd/arbitron/group_files_e2e_test.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gather reconciler is what main.go hands the group sweep (P2-T14).
var _ groupReconciler = (*engine.GroupGatherReconciler)(nil)

// ---------------------------------------------------------------------------
// Manifests. Every task writes real files with `sh` into its own {{workspace}};
// every sink assertion reads the PARENT's working area back through a
// downstream tool_call node whose JSON output lands in the parent's persisted
// variables (the parent's workspace is removed when it completes).
// ---------------------------------------------------------------------------

// filesAllManifest: all_succeed + results_and_files with a plain-directory
// repo hint (src). a adds a.txt and nested/deep.txt after 2s; b adds b.txt and
// deletes the parent's drop.txt at once, so b finishes FIRST while a is
// authored first. check reads the parent's area after the gather.
const filesAllManifest = `{
  "name": "grp-files-all",
  "description": "all_succeed results_and_files",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "mkdir -p \"$1/src\"; printf base > \"$1/base.txt\"; printf drop > \"$1/drop.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [
         {"key": "a", "entry": "a-run", "inputs": {"hold": "2"}},
         {"key": "b", "entry": "b-run", "inputs": {"hold": "0"}}
       ],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "repoPath": "src",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep \"$2\"; mkdir -p \"$1/nested\"; printf alpha > \"$1/a.txt\"; printf deep > \"$1/nested/deep.txt\"; echo '{}'", "sh", "{{workspace}}", "{{hold}}"]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep \"$2\"; printf beta > \"$1/b.txt\"; rm \"$1/drop.txt\"; echo '{}'", "sh", "{{workspace}}", "{{hold}}"]}},
    {"id": "check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/drop.txt\" ]; then d=false; else d=true; fi; printf '{\"a\":\"%s\",\"b\":\"%s\",\"deep\":\"%s\",\"base\":\"%s\",\"dropped\":%s}' \"$(cat \"$1/a.txt\")\" \"$(cat \"$1/b.txt\")\" \"$(cat \"$1/nested/deep.txt\")\" \"$(cat \"$1/base.txt\")\" \"$d\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "check", "output": "success"},
    {"source": "check", "target": "onSuccess"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// filesConflictManifest: a and b both rewrite the parent's shared.txt line, so
// b's merge (after a's, author order) conflicts; a also adds a.txt.
const filesConflictManifest = `{
  "name": "grp-files-conflict",
  "description": "two branches edit the same line",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf 'base\\n' > \"$1/shared.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf 'from-a\\n' > \"$1/shared.txt\"; printf alpha > \"$1/a.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "b-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf 'from-b\\n' > \"$1/shared.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "check-ok", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"shared\":\"%s\",\"a\":\"%s\"}' \"$(cat \"$1/shared.txt\")\" \"$(cat \"$1/a.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "check-fail", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/a.txt\" ]; then a=true; else a=false; fi; printf '{\"shared\":\"%s\",\"aPresent\":%s}' \"$(cat \"$1/shared.txt\")\" \"$a\"", "sh", "{{workspace}}"]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "check-ok", "output": "success"},
    {"source": "check-ok", "target": "onSuccess"},
    {"source": "group-1", "target": "check-fail", "output": "failure"},
    {"source": "check-fail", "target": "onFailure"}
  ]
}`

// filesFirstManifest: first_success + finish_independently. fast wins at
// once; slow keeps running, writes late.txt in ITS area after 3s and reports
// it. late-check reads the parent's area 8s after the gather.
const filesFirstManifest = `{
  "name": "grp-files-first",
  "description": "first_success finish_independently results_and_files",
  "inputs": [],
  "nodes": [
    {"id": "group-1", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "fast", "entry": "fast-run"}, {"key": "slow", "entry": "slow-run"}],
       "completionRule": "first_success",
       "unfinishedBehavior": "finish_independently",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "fast-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf win > \"$1/win.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "slow-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 3; printf late > \"$1/late.txt\"; echo '{\"wrote\":true}'", "sh", "{{workspace}}"]}},
    {"id": "late-check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "sleep 8; if [ -e \"$1/late.txt\" ]; then l=true; else l=false; fi; printf '{\"win\":\"%s\",\"late\":%s}' \"$(cat \"$1/win.txt\")\" \"$l\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "group-1", "target": "fast-run", "output": "task", "label": "fast"},
    {"source": "group-1", "target": "slow-run", "output": "task", "label": "slow"},
    {"source": "group-1", "target": "late-check", "output": "success"},
    {"source": "late-check", "target": "onSuccess"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// filesFailedManifest: all_succeed + wait_for_remaining; a succeeds with
// a.txt, b writes b.txt and then fails (halt).
const filesFailedManifest = `{
  "name": "grp-files-failed",
  "description": "failed results_and_files group",
  "inputs": [],
  "nodes": [
    {"id": "seed", "type": "tool_call", "entrypoint": true,
     "config": {"command": "sh", "args": ["-c", "printf base > \"$1/base.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "a", "entry": "a-run"}, {"key": "b", "entry": "b-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "wait_for_remaining",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "a-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf alpha > \"$1/a.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "b-run", "type": "tool_call", "errorBehavior": {"type": "halt"},
     "config": {"command": "sh", "args": ["-c", "printf beta > \"$1/b.txt\"; exit 3", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "check-fail", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "if [ -e \"$1/a.txt\" ]; then a=true; else a=false; fi; if [ -e \"$1/b.txt\" ]; then b=true; else b=false; fi; printf '{\"aPresent\":%s,\"bPresent\":%s,\"base\":\"%s\"}' \"$a\" \"$b\" \"$(cat \"$1/base.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "seed", "target": "group-1"},
    {"source": "group-1", "target": "a-run", "output": "task", "label": "a"},
    {"source": "group-1", "target": "b-run", "output": "task", "label": "b"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "check-fail", "output": "failure"},
    {"source": "check-fail", "target": "onFailure"}
  ]
}`

// filesNestedGatherManifest: the outer files group's only task t is itself a
// files group (x writes x.txt, y writes y.txt) whose continuations stay in t's
// body; t-mark reads t's OWN area after the inner gather.
const filesNestedGatherManifest = `{
  "name": "grp-files-nested-gather",
  "description": "nested results_and_files groups",
  "inputs": [],
  "nodes": [
    {"id": "outer", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "t", "entry": "inner"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "inner", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "x", "entry": "x-run"}, {"key": "y", "entry": "y-run"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "resolverProfile": "resolver"}}},
    {"id": "x-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf ex > \"$1/x.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "y-run", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf why > \"$1/y.txt\"; echo '{}'", "sh", "{{workspace}}"]}},
    {"id": "t-mark", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"x\":\"%s\",\"y\":\"%s\"}' \"$(cat \"$1/x.txt\")\" \"$(cat \"$1/y.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "t-bad", "type": "transform",
     "config": {"operations": [{"variable": "inner_branch", "operation": "set", "value": "failure"}]}},
    {"id": "check", "type": "tool_call",
     "config": {"command": "sh", "args": ["-c", "printf '{\"x\":\"%s\",\"y\":\"%s\"}' \"$(cat \"$1/x.txt\")\" \"$(cat \"$1/y.txt\")\"", "sh", "{{workspace}}"]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "outer", "target": "inner", "output": "task", "label": "t"},
    {"source": "inner", "target": "x-run", "output": "task", "label": "x"},
    {"source": "inner", "target": "y-run", "output": "task", "label": "y"},
    {"source": "inner", "target": "t-mark", "output": "success"},
    {"source": "inner", "target": "t-bad", "output": "failure"},
    {"source": "outer", "target": "check", "output": "success"},
    {"source": "check", "target": "onSuccess"},
    {"source": "outer", "target": "onFailure", "output": "failure"}
  ]
}`

// filesBadRepoManifest: the repo hint is a template whose runtime value climbs
// out of the parent's working area.
const filesBadRepoManifest = `{
  "name": "grp-files-badrepo",
  "description": "escaping repoPath",
  "inputs": [],
  "nodes": [
    {"id": "prep", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "repo_dir", "operation": "set", "value": "../outside"}]}},
    {"id": "group-1", "type": "parallel_group",
     "config": {"group": {
       "form": "branches",
       "tasks": [{"key": "only", "entry": "only-set"}],
       "completionRule": "all_succeed",
       "unfinishedBehavior": "cancel_unfinished",
       "gatherMode": "results_and_files",
       "repoPath": "{{repo_dir}}",
       "resolverProfile": "resolver"}}},
    {"id": "only-set", "type": "transform",
     "config": {"operations": [{"variable": "only_value", "operation": "set", "value": "x"}]}},
    {"id": "onSuccess", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "success"}]}},
    {"id": "onFailure", "type": "transform",
     "config": {"operations": [{"variable": "branch", "operation": "set", "value": "failure"}]}}
  ],
  "edges": [
    {"source": "prep", "target": "group-1"},
    {"source": "group-1", "target": "only-set", "output": "task", "label": "only"},
    {"source": "group-1", "target": "onSuccess", "output": "success"},
    {"source": "group-1", "target": "onFailure", "output": "failure"}
  ]
}`

// fakeResolverScript stands in for the claude CLI behind the "resolver" agent
// profile: it drains the prompt, changes nothing in the staging area, and
// reports that a human decision is needed (a stream-json transcript in the
// shape adapter/claudecli parses).
const fakeResolverScript = `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"fake-resolver"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"The conflict needs a human decision."}]}}'
printf '%s\n' '{"type":"result","is_error":false,"result":"The conflict needs a human decision.","session_id":"fake-resolver","usage":{"input_tokens":1,"output_tokens":1}}'
`

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func requireHostGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("file gathering needs a git binary on the host")
	}
}

// writeFilesStack writes the manifests and a local-mode config whose workspace
// root is a SYMLINK (to prove the canonical-root wiring), whose claude binary
// is fakeResolverScript, and which seeds an agent-kind "resolver" profile.
func writeFilesStack(t *testing.T, manifests map[string]string) groupStackPaths {
	t.Helper()
	requireHostGit(t)
	root := t.TempDir()
	realWS := filepath.Join(root, "workspaces-real")
	require.NoError(t, os.MkdirAll(realWS, 0o700))
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.Symlink(realWS, workspaceDir))
	for name, manifest := range manifests {
		writeWorkflow(t, workspaceDir, name, manifest)
	}
	fakeClaude := filepath.Join(root, "fake-claude")
	require.NoError(t, os.WriteFile(fakeClaude, []byte(fakeResolverScript), 0o700))

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 10\n  perusercap: 5\n" +
		"llm:\n  binarypath: \"" + fakeClaude + "\"\n" +
		"profiles:\n" +
		"  - alias: \"resolver\"\n    kind: \"agent\"\n    model: \"\"\n    allowedtools: [\"Read\", \"Edit\", \"Bash\"]\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	return groupStackPaths{root: root, workspaceDir: workspaceDir, configPath: configPath}
}

func bootFilesStack(t *testing.T, manifests map[string]string) (*domainStack, groupStackPaths) {
	t.Helper()
	paths := writeFilesStack(t, manifests)
	ds, err := initDomain(context.Background(), paths.configPath)
	require.NoError(t, err, "the composition root must boot with file gathering wired")
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds, paths
}

func canonicalRoot(t *testing.T, paths groupStackPaths) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(paths.workspaceDir)
	require.NoError(t, err)
	return p
}

func backingDirOf(t *testing.T, paths groupStackPaths, runID uuid.UUID) string {
	t.Helper()
	return filepath.Join(canonicalRoot(t, paths), ".group-backing", runID.String()+".git")
}

func taskWorkspaceDir(t *testing.T, paths groupStackPaths, id uuid.UUID) string {
	t.Helper()
	return filepath.Join(canonicalRoot(t, paths), "instances", id.String())
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// checkOutput is a downstream node's JSON output in the parent's PERSISTED
// variables — what the node read from the parent's working area.
func checkOutput(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string) map[string]any {
	t.Helper()
	out, ok := parentVariables(t, ds, parentID)[nodeID].(map[string]any)
	require.True(t, ok, "node %q must have run and recorded its reading of the working area", nodeID)
	return out
}

func waitForRunState(t *testing.T, ds *domainStack, parentID uuid.UUID, nodeID string, want instance.GroupRunState) *instance.GroupRun {
	t.Helper()
	var run *instance.GroupRun
	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, nodeID)
		if err != nil || r == nil {
			return false
		}
		run = r
		return r.State == want
	}, 40*time.Second, 150*time.Millisecond, "group run %s/%s never reached %s", parentID, nodeID, want)
	return run
}

// waitForOpenResolution waits until group-1 is resolving conflicts with a
// persisted active session and the parent is suspended.
func waitForOpenResolution(t *testing.T, ds *domainStack, parentID uuid.UUID) *instance.GroupRun {
	t.Helper()
	waitForRunState(t, ds, parentID, "group-1", instance.GroupRunResolvingConflicts)
	waitForState(t, ds, parentID, instance.StateWaiting)
	var run *instance.GroupRun
	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.GetByParentNode(context.Background(), parentID, "group-1")
		if err != nil || r == nil || r.Gather == nil || r.Gather.ActiveSession == nil {
			return false
		}
		run = r
		return true
	}, 20*time.Second, 100*time.Millisecond, "the resolution session must be recorded on the run")
	return run
}

func stagingDirOf(t *testing.T, run *instance.GroupRun) string {
	t.Helper()
	require.NotNil(t, run.Gather)
	dir, _ := run.Gather.Handle["stagingDir"].(string)
	require.NotEmpty(t, dir, "the persisted gather handle names the staging area")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// resolveConflictInStaging does what an operator (or the resolver agent)
// does in the staging area: edit the conflicted file and stage it.
func resolveConflictInStaging(t *testing.T, run *instance.GroupRun, content string) {
	t.Helper()
	dir := stagingDirOf(t, run)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shared.txt"), []byte(content), 0o644))
	gitIn(t, dir, "add", "--", "shared.txt")
}

// loseSessionEndWake records the session's end on the open attempt exactly as
// GroupResolutionBridge.OnResolutionSessionEnded does, but WITHOUT the wake —
// the state a process death between the two leaves behind.
func loseSessionEndWake(t *testing.T, ds *domainStack, parentID uuid.UUID) {
	t.Helper()
	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Gather)
	rep := run.Gather.Clone()
	now := time.Now().UTC()
	for i := range rep.Attempts {
		if rep.Attempts[i].EndedAt == nil {
			rep.Attempts[i].EndedAt = &now
		}
	}
	require.NoError(t, ds.groupRunRepo.SetGatherReport(context.Background(), run.ID, rep))
}

func groupSessionRows(t *testing.T, ds *domainStack, parentID uuid.UUID) []instance.NodeExecution {
	t.Helper()
	execs, err := ds.nodeExecRepo.ListByInstance(context.Background(), parentID)
	require.NoError(t, err)
	var out []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "group-1" && e.LLMConversation != nil && e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			out = append(out, e)
		}
	}
	return out
}

func hasNotification(notes []notification.Notification, id uuid.UUID, typ notification.Type) bool {
	for _, n := range notes {
		if n.InstanceID == id && n.Type == typ {
			return true
		}
	}
	return false
}

func gatherTaskReport(t *testing.T, run *instance.GroupRun, key string) instance.GatherTaskReport {
	t.Helper()
	require.NotNil(t, run.Gather)
	for _, tr := range run.Gather.Tasks {
		if tr.Key == key {
			return tr
		}
	}
	t.Fatalf("gather report of run %s has no task %q", run.ID, key)
	return instance.GatherTaskReport{}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// HAPPY PATH. Wiring lines 3, 4, 7, 8, 13 are load-bearing here.
func TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-all": filesAllManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-all")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID),
		"a failed parent here means the file area, the executor's gatherer or the launcher preflight is not wired")
	assert.Equal(t, "success", branchVariable(t, ds, parentID), "a missing finalizer routes failure (no finalized refs)")

	check := checkOutput(t, ds, parentID, "check")
	assert.Equal(t, "alpha", check["a"])
	assert.Equal(t, "beta", check["b"])
	assert.Equal(t, "deep", check["deep"], "new files outside the repo hint are gathered too")
	assert.Equal(t, "base", check["base"], "the parent's own files survive the gather")
	assert.Equal(t, true, check["dropped"], "a task's deletion is gathered")

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Gather, "the merge report is persisted")
	assert.Equal(t, "results_and_files", run.Gather.Mode)
	assert.Equal(t, "applied", run.Gather.State)
	require.Len(t, run.Gather.Tasks, 2)
	assert.Equal(t, "a", run.Gather.Tasks[0].Key, "the report lists tasks in author order")
	assert.True(t, run.Gather.Tasks[0].Merged)
	assert.True(t, run.Gather.Tasks[1].Merged)
	assert.Equal(t, "src", run.Config["resolvedRepoPath"], "the launcher resolved and recorded the repo hint")
	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.NotNil(t, a.TerminalSeq)
	require.NotNil(t, b.TerminalSeq)
	assert.Less(t, *b.TerminalSeq, *a.TerminalSeq, "b finished first, yet a was merged first")
	assert.NotEmpty(t, a.FinalizedRef, "each task's files were finalized before it completed")

	out := groupOutputOf(t, ds, parentID, "group-1")
	gather, ok := out["gather"].(map[string]any)
	require.True(t, ok, "the frozen group output downstream nodes read carries the merge report")
	assert.Equal(t, "applied", gather["state"])

	require.Eventually(t, func() bool {
		return !pathExists(backingDirOf(t, paths, run.ID)) &&
			!pathExists(taskWorkspaceDir(t, paths, a.ChildInstanceID)) &&
			!pathExists(taskWorkspaceDir(t, paths, b.ChildInstanceID))
	}, 20*time.Second, 100*time.Millisecond,
		"the backing snapshot and the retained task worktrees are pruned once every writer stopped (well before the 60s sweep)")
}

func TestGroupFilesE2E_FirstSuccessMergesOnlyTheWinnerAndALateLoserNeverReachesTheParent(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-first": filesFirstManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-first")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Decision)
	assert.Equal(t, "fast", run.Decision.WinnerKey)
	slowTask := groupRosterTask(t, run, "slow")
	assert.True(t, slowTask.ContinuingIndependently)
	assert.Empty(t, slowTask.FinalizedRef, "a task finishing after the decision is never finalized")
	waitForState(t, ds, slowTask.ChildInstanceID, instance.StateCompleted)
	slow, err := ds.instanceRepo.Get(ctx, slowTask.ChildInstanceID)
	require.NoError(t, err)
	wrote, _ := slow.Variables["slow-run"].(map[string]any)
	assert.Equal(t, true, wrote["wrote"], "the loser really wrote late.txt in its own working area")

	execs, err := ds.nodeExecRepo.ListByInstance(ctx, parentID)
	require.NoError(t, err)
	var checkExec *instance.NodeExecution
	for i := range execs {
		if execs[i].NodeID == "late-check" {
			checkExec = &execs[i]
		}
	}
	require.NotNil(t, checkExec)
	require.NotNil(t, slow.CompletedAt)
	require.True(t, slow.CompletedAt.Before(checkExec.StartedAt.Add(7*time.Second)), "test gates must establish the intended ordering; scheduler timing is never a skip")
	check := checkOutput(t, ds, parentID, "late-check")
	assert.Equal(t, "win", check["win"], "the winner's changes were gathered")
	assert.Equal(t, false, check["late"], "SINK: the independent loser's late write never reached the parent's working area")
	assert.True(t, gatherTaskReport(t, run, "fast").Merged)
	slowRep := gatherTaskReport(t, run, "slow")
	assert.False(t, slowRep.Merged)
	assert.Contains(t, slowRep.Reason, "not the winner")

	require.Eventually(t, func() bool { return !pathExists(backingDirOf(t, paths, run.ID)) },
		20*time.Second, 100*time.Millisecond, "the snapshot is released once the loser stopped writing")
}

func TestGroupFilesE2E_FailedGroupMergesNothingAndRetainsDebuggingContent(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-failed": filesFailedManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-failed")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))

	check := checkOutput(t, ds, parentID, "check-fail")
	assert.Equal(t, false, check["aPresent"], "a succeeded, but a failed group merges no partial work")
	assert.Equal(t, false, check["bPresent"])
	assert.Equal(t, "base", check["base"])

	run := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, run.Decision)
	assert.Equal(t, "failure", run.Decision.Outcome)
	require.NotNil(t, run.Gather)
	assert.Equal(t, "not_applicable", run.Gather.State)
	for _, tr := range run.Gather.Tasks {
		assert.False(t, tr.Merged)
		assert.Equal(t, "group failed: no tasks merged", tr.Reason)
	}

	a, b := groupRosterTask(t, run, "a"), groupRosterTask(t, run, "b")
	require.Eventually(t, func() bool { return !pathExists(backingDirOf(t, paths, run.ID)) },
		20*time.Second, 100*time.Millisecond, "the backing snapshot is still released")
	got, err := os.ReadFile(filepath.Join(taskWorkspaceDir(t, paths, a.ChildInstanceID), "a.txt"))
	require.NoError(t, err, "the completed task's workspace is retained for debugging")
	assert.Equal(t, "alpha", string(got))
	assert.True(t, pathExists(taskWorkspaceDir(t, paths, b.ChildInstanceID)), "the failed task's workspace is retained by the engine")
}

// Wiring lines 1 and 6 are load-bearing here.
func TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)

	require.Len(t, run.Gather.Attempts, 1)
	att := run.Gather.Attempts[0]
	assert.Equal(t, "b", att.TaskKey, "a merged cleanly first (author order); b conflicts")
	assert.Equal(t, []string{"shared.txt"}, att.ConflictPaths)
	assert.Nil(t, att.EndedAt)

	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1, "the session is persisted on the parent at the group node")
	assert.True(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation))
	assert.Equal(t, run.ID.String(), rows[0].LLMConversation["groupRunId"])

	notes, err := ds.notifRepo.List(ctx, notification.ListFilters{Limit: 200})
	require.NoError(t, err)
	assert.True(t, hasNotification(notes, parentID, notification.TypeConflictResolution), "an operator is told a human decision is needed")

	assert.Never(t, func() bool {
		inst, gerr := ds.instanceRepo.Get(ctx, parentID)
		return gerr != nil || inst.State != instance.StateWaiting
	}, 3*time.Second, 200*time.Millisecond, "downstream work never runs while the conflict is open")
	_, ran := parentVariables(t, ds, parentID)["check-ok"]
	assert.False(t, ran)

	resolveConflictInStaging(t, run, "resolved\n")
	_, err = ds.conversationHandler.EndSession(adminCtxForLocalUser(t, ds), parentID)
	require.NoError(t, err, "ending the session means 'finish and verify'")

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-ok")
	assert.Equal(t, "resolved", check["shared"], "SINK: the resolved content reached the parent's working area")
	assert.Equal(t, "alpha", check["a"], "a's clean merge is kept")
	final := groupRunOf(t, ds, parentID, "group-1")
	assert.Equal(t, "applied", final.Gather.State)
	assert.Equal(t, "resolved", final.Gather.Attempts[0].Outcome)
	assert.Contains(t, gatherTaskReport(t, final, "b").Reason, "after conflict resolution")
}

// Named by P3-T9. Wiring line 6 is load-bearing here.
func TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	waitForOpenResolution(t, ds, parentID)
	require.NoError(t, ds.conversationHandler.AbandonSession(adminCtxForLocalUser(t, ds), parentID))

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "failure", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-fail")
	assert.Equal(t, "base", check["shared"], "SINK: the parent's file is exactly as before the gather")
	assert.Equal(t, false, check["aPresent"], "not even a's clean merge was applied")

	final := groupRunOf(t, ds, parentID, "group-1")
	require.NotNil(t, final.Decision)
	assert.Equal(t, "success", final.Decision.Outcome, "the task decision is never rewritten")
	assert.Equal(t, "failure", final.ContinuationPort)
	assert.Equal(t, "rolled_back", final.Gather.State)
	assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
	assert.Equal(t, "abandoned", final.Gather.Attempts[0].Outcome)
	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1)
	assert.Equal(t, "abandoned", rows[0].LLMConversation["endReason"])
}

// Wiring line 13 (Watch) is load-bearing here: the sweep is 60s away.
func TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), parentID))

	require.Eventually(t, func() bool {
		r, err := ds.groupRunRepo.Get(ctx, run.ID)
		return err == nil && r.State == instance.GroupRunDone
	}, 20*time.Second, 100*time.Millisecond, "the cancellation aborts the gather at once, not at the next sweep")
	final := groupRunOf(t, ds, parentID, "group-1")
	assert.Equal(t, "failure", final.ContinuationPort)
	assert.Equal(t, "cancelled", final.Gather.Attempts[0].Outcome)
	assert.Nil(t, final.Gather.ActiveSession)
	assert.Equal(t, "rolled_back", final.Gather.RollbackStatus)
	rows := groupSessionRows(t, ds, parentID)
	require.Len(t, rows, 1)
	assert.Equal(t, "cancelled", rows[0].LLMConversation["endReason"])

	assert.Never(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, parentID)
		return err != nil || inst.State != instance.StateCancelled
	}, 3*time.Second, 200*time.Millisecond, "the parent stays cancelled")
	assert.Nil(t, branchVariable(t, ds, parentID), "neither continuation port ran")
}

func TestGroupFilesE2E_NestedGroupGathersIntoItsImmediateParent(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-files-nested-gather": filesNestedGatherManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-nested-gather")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check")
	assert.Equal(t, "ex", check["x"])
	assert.Equal(t, "why", check["y"])

	outer := groupRunOf(t, ds, parentID, "outer")
	require.NotNil(t, outer.Gather)
	assert.Equal(t, "applied", outer.Gather.State)
	tTask := groupRosterTask(t, outer, "t")
	inner := groupRunOf(t, ds, tTask.ChildInstanceID, "inner")
	assert.Equal(t, tTask.ChildInstanceID, inner.ParentInstanceID, "the nested group's parent is the enclosing task, not the root")
	require.NotNil(t, inner.Gather)
	assert.Equal(t, "applied", inner.Gather.State)
	mark, ok := parentVariables(t, ds, tTask.ChildInstanceID)["t-mark"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ex", mark["x"], "the nested gather wrote into the task's own working area first")
	assert.Equal(t, "why", mark["y"])

	require.Eventually(t, func() bool {
		return !pathExists(backingDirOf(t, paths, outer.ID)) && !pathExists(backingDirOf(t, paths, inner.ID))
	}, 20*time.Second, 100*time.Millisecond, "both levels release their snapshots")
}

func TestGroupFilesE2E_ResultsOnlyModeCopiesStartingFilesWithoutMerging(t *testing.T) {
	ds, paths := bootFilesStack(t, map[string]string{"grp-branches": groupBranchesManifest})

	parentID := triggerGroupWorkflow(t, ds, "grp-branches")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))
	check := checkOutput(t, ds, parentID, "check-parent")
	assert.Equal(t, false, check["leaked"], "no task file is merged in results mode")
	assert.Equal(t, true, check["seedKept"])

	out := groupOutputOf(t, ds, parentID, "group-1")
	aRun, ok := resultOf(t, groupTaskByKey(t, out, "a"))["a-run"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "parent-seed", aRun["seed"], "the task started from a copy of the parent's files")
	gather, ok := out["gather"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "results", gather["mode"])
	run := groupRunOf(t, ds, parentID, "group-1")
	assert.Nil(t, run.Gather)
	assert.False(t, pathExists(backingDirOf(t, paths, run.ID)), "results-only groups never create a git backing store")
}

// Wiring line 11 (startup gather reconcile) is load-bearing here.
func TestGroupFilesE2E_RestartRestoresAnInterruptedResolution(t *testing.T) {
	paths := writeFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()

	ds1, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	parentID := triggerGroupWorkflow(t, ds1, "grp-files-conflict")
	run := waitForOpenResolution(t, ds1, parentID)
	resolveConflictInStaging(t, run, "resolved\n")
	loseSessionEndWake(t, ds1, parentID)
	require.NoError(t, ds1.db.Close())

	ds2, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds2.Close(context.Background()) })

	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds2, parentID),
		"the startup gather reconcile wakes the parent whose wake was lost (the sweep is 60s away)")
	assert.Equal(t, "success", branchVariable(t, ds2, parentID))
	assert.Equal(t, "resolved", checkOutput(t, ds2, parentID, "check-ok")["shared"])
	rows := groupSessionRows(t, ds2, parentID)
	require.Len(t, rows, 1)
	assert.False(t, engine.IsOpenGroupResolutionSession(rows[0].LLMConversation), "the interrupted session's record is closed")
}

// Wiring line 12 (gather sweep goroutine) is load-bearing here.
func TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost(t *testing.T) {
	prev := groupSweepInterval
	groupSweepInterval = time.Second
	t.Cleanup(func() { groupSweepInterval = prev })

	ds, _ := bootFilesStack(t, map[string]string{"grp-files-conflict": filesConflictManifest})
	ctx := context.Background()
	parentID := triggerGroupWorkflow(t, ds, "grp-files-conflict")
	run := waitForOpenResolution(t, ds, parentID)
	resolveConflictInStaging(t, run, "resolved\n")
	loseSessionEndWake(t, ds, parentID)

	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, parentID)
		return err == nil && inst.State == instance.StateCompleted
	}, 20*time.Second, 150*time.Millisecond, "only the periodic gather reconcile can wake this parent")
	assert.Equal(t, "resolved", checkOutput(t, ds, parentID, "check-ok")["shared"])
}

func TestGroupFilesE2E_InvalidRepoPathFailsBeforeAnyTaskStarts(t *testing.T) {
	ds, _ := bootFilesStack(t, map[string]string{"grp-files-badrepo": filesBadRepoManifest})
	ctx := context.Background()

	parentID := triggerGroupWorkflow(t, ds, "grp-files-badrepo")
	require.Equal(t, instance.StateFailed, waitForTerminalState(t, ds, parentID))
	run, err := ds.groupRunRepo.GetByParentNode(ctx, parentID, "group-1")
	require.NoError(t, err)
	assert.Nil(t, run, "nothing is persisted for a group that could not be prepared")
	assert.Empty(t, childrenOf(t, ds, parentID), "no task was started")

	execs, err := ds.nodeExecRepo.ListByInstance(ctx, parentID)
	require.NoError(t, err)
	var msg string
	for _, e := range execs {
		if e.NodeID == "group-1" && e.Status == instance.NodeStatusFailed {
			msg = fmt.Sprint(e.Error["error"])
		}
	}
	assert.Contains(t, msg, "parallel_group could not be prepared")
	assert.Contains(t, msg, "repoPath", "the author is told which setting to fix")
}
```

- [x] Step 2: Update the trigger e2e. In `cmd/arbitron/group_trigger_e2e_test.go`, replace the whole `func TestGroupTriggerE2E_Phase2RunsResultsGroupsAndRefusesFileGathering(t *testing.T) { … }` together with the comment block directly above it (P2-T14's version) with (keep the file's imports and `instancesNamed` helper unchanged — every import stays used):

```go
// PHASE 3 EXPECTATION (P3-T11 update of P2-T14's Phase 2 version): the
// composition root wires eng.SetGroupSupport(engine.GroupSupportResultsAndFiles),
// so BOTH gather modes are accepted directly, over MCP, and as a spawned child,
// and a files group really gathers.
//
// PROVENANCE PROBE: remove eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)
// from initDomain and the level assertion plus every files-group subtest fail.
func TestGroupTriggerE2E_Phase3RunsBothGatherModes(t *testing.T) {
	requireHostGit(t)
	ds, _ := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.Equal(t, engine.GroupSupportResultsAndFiles, ds.eng.GroupSupportLevel(),
		"the Phase 3 composition root enables file gathering")

	results := filesModeGroup("results-group", "")
	results.Nodes[0].Config.Group.GatherMode = workflow.GroupGatherResults
	require.NoError(t, ds.workflowSvc.Create(ctx, "results-group", results))
	require.NoError(t, ds.workflowSvc.Create(ctx, "files-group", filesModeGroup("files-group", "default-agent")))

	t.Run("results-group runs", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "results-group", map[string]any{}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, id, instance.StateCompleted)
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, true, inst.Variables["done"], "the group's SUCCESS target ran")
	})

	t.Run("files-group runs and gathers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "files-group", map[string]any{}, "normal")
		require.NoError(t, err)
		assert.False(t, errors.Is(err, engine.ErrGroupExecutionUnsupported))
		require.NotEqual(t, uuid.Nil, id)
		waitForState(t, ds, id, instance.StateCompleted)
		inst, err := ds.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, true, inst.Variables["done"])
		_, failed := inst.Variables["failed"]
		assert.False(t, failed, "the failure port was not taken")
		run := groupRunOf(t, ds, id, "fanout")
		require.NotNil(t, run.Gather)
		assert.Equal(t, "applied", run.Gather.State, "an empty-diff gather is applied, not refused")
	})

	t.Run("instance_trigger over MCP runs files-group", func(t *testing.T) {
		srv := newGroupMCPServer(ds)
		res := callMCPTool(t, srv, ctx, "instance_trigger", map[string]any{"workflowName": "files-group"})
		require.False(t, res.IsError, mcpText(t, res))
		assert.Len(t, instancesNamed(t, ds, "files-group"), 2, "the direct run above plus this MCP run")
	})

	t.Run("spawned files-group runs as a fire-and-forget child", func(t *testing.T) {
		parent := &workflow.Workflow{
			Name: "spawns-files-group", Description: "spawns a files group workflow", Author: "e2e",
			Nodes: []workflow.Node{
				{ID: "p1", Type: workflow.NodeTypeTransform, Entrypoint: true, Config: workflow.NodeConfig{
					Operations: []workflow.Operation{{Variable: "started", Operation: "set", Value: true}}}},
				{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
					ChildWorkflow: "files-group", SpawnMode: "fire_and_forget"}},
			},
			Edges: []workflow.Edge{{Source: "p1", Target: "spawn-1"}},
		}
		require.NoError(t, ds.workflowSvc.Create(ctx, "spawns-files-group", parent))

		parentID, _, err := ds.eng.Trigger(ctx, "spawns-files-group", map[string]any{}, "normal")
		require.NoError(t, err)
		waitForState(t, ds, parentID, instance.StateCompleted)
		var kids []instance.InstanceSummary
		require.Eventually(t, func() bool {
			kids = childrenOf(t, ds, parentID)
			return len(kids) == 1
		}, 40*time.Second, 100*time.Millisecond, "the spawn path admits a files-mode group child")
		assert.Equal(t, "files-group", kids[0].WorkflowName)
		waitForState(t, ds, kids[0].ID, instance.StateCompleted)
	})

	t.Run("non-group workflow still triggers", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "group-child", map[string]any{"topic": "x"}, "normal")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			inst, gerr := ds.instanceRepo.Get(context.Background(), id)
			return gerr == nil && inst.State == instance.StateCompleted
		}, 20*time.Second, 100*time.Millisecond)
	})
}
```

- [x] Step 3: Update the Phase 2 refusal behaviours. In `cmd/arbitron/group_behaviors_e2e_test.go`, replace the section from the comment `// Phase 2 refusals` banner through the end of `func TestGroupBehaviorsE2E_ResultsAndFilesChildWorkflowFailsLaunch` (both functions and their comments; keep `stateVar` and everything else) with:

```go
// ---------------------------------------------------------------------------
// Phase 3 (P3-T11): the former Phase 2 refusals now run
// ---------------------------------------------------------------------------

// A results_and_files group nested inside a results-mode branch is accepted
// at the trigger and gathers its workflow task's files into its IMMEDIATE
// parent (the branch instance).
func TestGroupBehaviorsE2E_NestedFilesGroupInsideResultsBranchRuns(t *testing.T) {
	requireHostGit(t)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-nested": filesNestedManifest,
		"grp-leaf-child":   leafChildManifest,
	})
	require.Equal(t, engine.GroupSupportResultsAndFiles, ds.eng.GroupSupportLevel())

	parentID := triggerGroupWorkflow(t, ds, "grp-files-nested")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "outer")
	tTask := groupTaskByKey(t, out, "t")
	assert.Equal(t, "completed", tTask["status"])
	assert.Equal(t, "success", resultOf(t, tTask)["inner_branch"], "the nested files group took its success port inside the branch")
	branchID := uuid.MustParse(fmt.Sprint(tTask["childInstanceId"]))
	inner := groupRunOf(t, ds, branchID, "inner")
	require.NotNil(t, inner.Gather)
	assert.Equal(t, "applied", inner.Gather.State)
	require.NotEmpty(t, inner.Gather.Tasks)
	assert.True(t, strings.HasPrefix(inner.Gather.Tasks[0].Reason, "merged"), "reason %q", inner.Gather.Tasks[0].Reason)
}

// A results-mode parent whose workflow task is a results_and_files child
// workflow: the child's own files group runs and the parent's task completes.
func TestGroupBehaviorsE2E_FilesChildWorkflowRunsUnderResultsParent(t *testing.T) {
	requireHostGit(t)
	ds, _ := bootGroupExecStack(t, 5, map[string]string{
		"grp-files-parent": filesParentManifest,
		"grp-files-child":  filesChildManifest,
	})

	parentID := triggerGroupWorkflow(t, ds, "grp-files-parent")
	require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
	assert.Equal(t, "success", branchVariable(t, ds, parentID))

	out := groupOutputOf(t, ds, parentID, "g")
	reason := fmt.Sprint(out["reason"])
	assert.False(t, strings.Contains(reason, "launch failed"), "reason %q: the child workflow is no longer refused", reason)
	f := groupTaskByKey(t, out, "f")
	assert.Equal(t, "completed", f["status"])
	assert.Equal(t, "success", resultOf(t, f)["branch"], "the child's own files group succeeded")
	kids := childrenOf(t, ds, parentID)
	require.Len(t, kids, 1)
	assert.Equal(t, "grp-files-child", kids[0].WorkflowName)
}
```

(`fmt`, `strings`, `uuid` and `engine` stay used in that file by these two tests.)

- [x] Step 4: Run, confirm they fail.

All new file-mode cases fail at the unsupported-mode trigger as expected; results-only stays green. Added the required real automatic-repair, dishonest-success, inherited/gathered permissions and partial-apply rollback sink cases in `group_files_sinks_e2e_test.go`. First-success uses external bounded process gates; restart gates the actual resolver process and shuts down the old stack before reboot (rather than leaving DB-closed producers alive). MCP counts only roots and waits for their persisted completion, not branch rows.

```bash
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E|TestGroupTriggerE2E|TestGroupBehaviorsE2E_NestedFilesGroupInsideResultsBranchRuns|TestGroupBehaviorsE2E_FilesChildWorkflowRunsUnderResultsParent' -count=1 2>&1 | grep -E '^(--- |ok|FAIL)|not available|GroupSupport'
```
Expected (abridged):
```
--- FAIL: TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder (0.XXs)
        Error:          Received unexpected error:
                        ... parallel_group execution is not available on this server
        Messages:       a results-mode group workflow must be accepted by the trigger
...
--- FAIL: TestGroupTriggerE2E_Phase3RunsBothGatherModes (X.XXs)
        Messages:       the Phase 3 composition root enables file gathering
--- PASS: TestGroupFilesE2E_ResultsOnlyModeCopiesStartingFilesWithoutMerging (X.XXs)
FAIL	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
(Every files-group test fails at its trigger; the results-only test already passes because results mode never depended on Phase 3.)

- [x] Step 5: Implement the wiring in `cmd/arbitron/main.go`.

(a) In the import block, immediately after `"github.com/arbitron/arbitron/adapter/filesystem"` (line 30), add:

```go
	gitarea "github.com/arbitron/arbitron/adapter/git"
```

(b) In P2-T14's `12c-group` block, immediately after the `groupWS, err := filesystem.NewGroupWorkspace(cfg.Workspace.Path)` error check (its `return nil, fmt.Errorf("group workspace: %w", err)` / `}`), add:

```go
	// Phase 3: the git working area snapshots, forks, finalizes and gathers
	// results_and_files groups (P3-T2/T3). Results-only groups keep groupWS, so
	// they never depend on git. Same root as wsMgr: backing stores live under
	// <workspace>/.group-backing, beside <workspace>/instances.
	workingArea, err := gitarea.NewWorkingArea(cfg.Workspace.Path)
	if err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("group working area: %w", err)
	}
```

(c) Immediately after the `groupLauncher := engine.NewGroupLauncher(…)` statement (its closing `)`), add:

```go
	// File groups are preflighted (repoPath containment, git availability) and
	// snapshotted through the git area before any task exists (P3-T10).
	groupLauncher.SetFileWorkingArea(workingArea)
```

(d) Immediately after `eng.RegisterExecutor(workflow.NodeTypeParallelGroup, groupExecutor)`, add:

```go
	// A group task's files are finalized into an immutable ref BEFORE it is
	// marked completed and BEFORE its workspace could be removed (P3-T5).
	eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr))
```

(e) Replace P2-T14's two comment lines and statement

```go
	// Enable results-mode execution only now that every Phase 2 dependency is
	// wired; results_and_files stays refused until Phase 3.
	eng.SetGroupSupport(engine.GroupSupportResults)
```

with:

```go
	// The support level is raised at 12d-group-files, once the gatherer, the
	// resolution bridge and the gather reconciler are wired.
```

(f) Immediately after the `SetAuditRecorder` type-assertion block (the `}` that ends `if s, ok := conversationHandler.(interface { SetAuditRecorder(engine.AuditRecorder) }); ok { … }`, line 1063) and before `// 12e. SEAL the executor set.`, add:

```go
	// 12d-group-files. File gathering (Phase 3). Built here, after the
	// interactive stream selector (12c) and the conversation handler (12d), and
	// before FreezeExecutors.
	//
	// The resolution bridge opens conflict-resolution sessions (autonomous first
	// turn in the gather's staging area through the agent profile named by
	// resolverProfile), raises the conflict notification through the engine,
	// and completes/closes sessions; the conversation handler's group gate makes
	// MCP/HTTP/WS end and abandon reach it. The gatherer is the executor's file
	// gather AND the reconciler's aborter; the executor is the reconciler's
	// single-writer lock.
	groupResolution := engine.NewGroupResolutionBridge(
		groupRunRepo,
		instanceRepo,
		nodeExecRepo,
		profileResolver,
		interactiveStreamSel,
		fileReader,
		eng,       // ResolutionNotifier (EmitNotification)
		bus,
		queueRepo, // node-addressed waker
		queueMgr,
	)
	groupSessions, ok := conversationHandler.(interface {
		SetGroupResolution(instance.GroupRunRepository, engine.GroupResolutionCompleter)
	})
	if !ok {
		db.Close()
		logCloser.Close()
		return nil, errors.New("the conversation handler cannot host parallel-group conflict-resolution sessions")
	}
	groupSessions.SetGroupResolution(groupRunRepo, groupResolution)
	gatherer := engine.NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)
	groupExecutor.SetGatherer(gatherer)
	groupPruner := engine.NewGroupResourcePruner(
		groupRunRepo,
		instanceRepo, // task rows
		instanceRepo, // ChildLister (ListChildren) for descendant pins
		wsMgr,
		workingArea,
		workingArea, // SnapshotInventory
	)
	gatherReconciler := engine.NewGroupGatherReconciler(
		groupRunRepo,
		instanceRepo,
		nodeExecRepo,
		gatherer,        // GatherAborter
		groupResolution, // GroupSessionCloser
		groupResolution, // GroupResolutionCompleter
		queueRepo,
		queueMgr,
		groupExecutor, // GatherLocker
		bus,
		groupPruner,
	)
	// Every Phase 3 dependency is wired: admit results_and_files groups.
	eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)
```

(g) Immediately after P2-T14's `14-group` startup block (the `}` closing `if err := groupCoord.Reconcile(context.Background()); err != nil { … }`), add:

```go
	// 14-group-files. Recover file gathers. AFTER the coordinator's pass (which
	// decides groups whose crashed tasks were just failed) and BEFORE the queue
	// starts: no parent executor is running yet, so the reconciler is the only
	// writer. It rolls back gathers of failed/cancelled/completed parents,
	// re-wakes waiting parents whose wake was lost, finishes resolution attempts
	// whose session record is gone or closed, leaves open sessions as they are,
	// and sweeps working-area resources. Logged, not fatal; the sweep retries.
	logger.Info("reconciling parallel-group file gathers")
	if err := gatherReconciler.Reconcile(context.Background()); err != nil {
		logger.Error("parallel-group file-gather reconciliation incomplete", "error", err)
	}
```

(h) Immediately after P2-T14's `go sweepGroupReconciler(groupSweepCtx, groupCoord, groupSweepInterval, logger)`, add:

```go
	// The gather reconciler shares the group sweep's ticker interval and cancel
	// func (stopped in Close before the DB closes). Watch reacts at once to a
	// cancelled/failed/completed parent and prunes when a run's last writer
	// stops; it subscribes before returning.
	go sweepGroupReconciler(groupSweepCtx, gatherReconciler, groupSweepInterval, logger)
	gatherReconciler.Watch(groupSweepCtx)
```

Then confirm exactly one support-level call remains:
```bash
grep -n "SetGroupSupport" cmd/arbitron/main.go
```
Expected: one line, `eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)`.

- [x] Step 6: Run, confirm everything passes.

Build and all specified focused root gates pass. The permission sink exposed a real cleanup defect: `WorkspaceManager.RemoveInstanceDir` used plain `os.RemoveAll`, which cannot prune copied read-only directories. Added a focused red regression (also asserting external symlink target bytes/modes stay unchanged), then reused the filesystem adapter's existing no-follow `removeTree`; regression and real-root permission/pruning sinks now pass. Failed-gather retained copies are made writable only during test teardown, after stack shutdown.

```bash
go build ./cmd/arbitron/
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E|TestGroupTriggerE2E|TestGroupBehaviorsE2E|TestGroupE2E|TestCanonicalWorkspacePath|TestSweepGroupReconciler' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected:
```
--- PASS: TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder (X.XXs)
--- PASS: TestGroupFilesE2E_FirstSuccessMergesOnlyTheWinnerAndALateLoserNeverReachesTheParent (1X.XXs)
--- PASS: TestGroupFilesE2E_FailedGroupMergesNothingAndRetainsDebuggingContent (X.XXs)
--- PASS: TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved (X.XXs)
--- PASS: TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles (X.XXs)
--- PASS: TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled (X.XXs)
--- PASS: TestGroupFilesE2E_NestedGroupGathersIntoItsImmediateParent (X.XXs)
--- PASS: TestGroupFilesE2E_ResultsOnlyModeCopiesStartingFilesWithoutMerging (X.XXs)
--- PASS: TestGroupFilesE2E_RestartRestoresAnInterruptedResolution (X.XXs)
--- PASS: TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost (X.XXs)
--- PASS: TestGroupFilesE2E_InvalidRepoPathFailsBeforeAnyTaskStarts (X.XXs)
--- PASS: TestGroupTriggerE2E_Phase3RunsBothGatherModes (X.XXs)
--- PASS: TestGroupBehaviorsE2E_NestedFilesGroupInsideResultsBranchRuns (X.XXs)
--- PASS: TestGroupBehaviorsE2E_FilesChildWorkflowRunsUnderResultsParent (X.XXs)
--- PASS: TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder (X.XXs)
...
--- PASS: TestCanonicalWorkspacePath_ResolvesSymlinksAndCreatesTheRoot (0.00s)
ok  	github.com/arbitron/arbitron/cmd/arbitron	XX.XXXs
```
First-success and restart fixtures use the required process gates, so scheduler timing cannot turn a regression green via SKIP. Files tests may skip only when the host lacks git.

- [x] Step 7: Provenance check — prove each wiring line is load-bearing (do NOT commit these edits; revert each before the next).

```bash
# 1. replace `cfg.Workspace.Path = canonicalWS` with `_ = canonicalWS`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved' -count=1 -v
# Expected: --- FAIL ... never reached resolving_conflicts / never reached state waiting   (the resolver WorkDir escapes the lexical pin, StartResolution fails, the gather aborts and the parent runs on through check-fail)
# 2. restore; comment out `groupLauncher.SetFileWorkingArea(workingArea)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder' -count=1 -v
# Expected: --- FAIL ... expected: "completed" actual: "failed"
# 3. restore; comment out `eng.SetGroupTaskFinalizer(...)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder' -count=1 -v
# Expected: --- FAIL ... expected: "success" actual: "failure"   (a missing finalizer routes failure (no finalized refs))
# 4. restore; comment out `groupSessions.SetGroupResolution(groupRunRepo, groupResolution)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved|TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles' -count=1 -v
# Expected: --- FAIL ... ending the session means 'finish and verify' ... conversation: node group-1 is not interactive (type: parallel_group)
# 5. restore; comment out `groupExecutor.SetGatherer(gatherer)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder' -count=1 -v
# Expected: --- FAIL ... expected: "completed" actual: "failed"
# 6. restore; change `eng.SetGroupSupport(engine.GroupSupportResultsAndFiles)` to `eng.SetGroupSupport(engine.GroupSupportResults)`:
go test ./cmd/arbitron/ -run 'TestGroupTriggerE2E_Phase3RunsBothGatherModes' -count=1 -v
# Expected: --- FAIL ... the Phase 3 composition root enables file gathering
# 7. restore; comment out the 14-group-files startup Reconcile block:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_RestartRestoresAnInterruptedResolution' -count=1 -v
# Expected: --- FAIL ... instance <id> never reached a terminal state   (after ~40s)
# 8. restore; comment out `go sweepGroupReconciler(groupSweepCtx, gatherReconciler, groupSweepInterval, logger)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost' -count=1 -v
# Expected: --- FAIL ... only the periodic gather reconcile can wake this parent
# 9. restore; comment out `gatherReconciler.Watch(groupSweepCtx)`:
go test ./cmd/arbitron/ -run 'TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled|TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder' -count=1 -v
# Expected: --- FAIL ... the cancellation aborts the gather at once, not at the next sweep
#           --- FAIL ... the backing snapshot and the retained task worktrees are pruned once every writer stopped
# restore.
git diff --stat cmd/arbitron/main.go   # must show only the intended Step 5 changes
```

- [x] Step 8: Full regression, gofmt, vet, commit.

Verification complete: `go build` succeeds; all specified focused composition gates pass normally and with `-race`; the full `cmd/arbitron` suite passes; the entire filesystem adapter passes with `-race`. `go test ./... -count=1` passes every package except the unrelated `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure. Reproduced that exact failure from an unchanged `git archive 8b897a6` checkout. `gofmt -l`, `go vet ./cmd/arbitron/ ./adapter/filesystem/` and `git diff --check` are clean. All temporary provenance mutations are restored. Other tasks' historical completion gates are left untouched for their own verification passes.

Self-review, in the required order:
1. Spec compliance: reviewed this task's wiring table and added revision obligations. All constructors receive the specified real repositories/producers, the common working area, profile stream and lock; file support is raised only after construction. Kept results-only filesystem copies. The only necessary adjacent fix is read-only workspace removal, proven red/green; no feature flags/options added.
2. Anti-green-theater: all nine specified removal probes failed at the expected sinks (canonical pin, launcher, finalizer, handler gate, gatherer, support level, startup recovery, periodic recovery, event watch/pruning). Restored each before the next. Strengthened first-success: the parent reads winner bytes, then waits at an external process gate while the loser writes; a second downstream read after the loser completes still sees no late file. Automatic repair inspects both real prompt refs, stages combined bytes and continues without EndSession/notification; a lying resolver stays waiting and notifies. Partial real Git checkout failure restores bytes and permission modes and freezes outcome=failure/taskOutcome=success.
3. Code quality: reused the adjacent filesystem no-follow removal helper and main's boot error/cleanup conventions. Formatted and reran every focused root gate (including all group behavioral/regression cases) with the race detector after restoring all probes: green.

```bash
go test ./cmd/arbitron/ -count=1
go test ./... -count=1
gofmt -l cmd/arbitron
go vet ./cmd/arbitron/
git add cmd/arbitron/main.go cmd/arbitron/group_files_e2e_test.go cmd/arbitron/group_trigger_e2e_test.go cmd/arbitron/group_behaviors_e2e_test.go && git commit -m "feat(arbitron): wire parallel-group file gathering into the composition root" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok  	github.com/arbitron/arbitron/cmd/arbitron`; every package `ok` except the PRE-EXISTING, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` (fails identically on the base commit, as P2-T14 notes); `gofmt -l` and `go vet` print nothing.
