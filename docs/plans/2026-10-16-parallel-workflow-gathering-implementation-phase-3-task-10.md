# Parallel Workflow Gathering — Phase 3 Task 10: File-mode executor/launcher integration, gather recovery and resource pruning

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make `results_and_files` groups safe end to end around the gatherer: resolve and contain the optional `repoPath` at launch and preflight git before any task starts, serialize every writer of a run's file gather, keep a parent whose rollback failed suspended (never continued), roll back or resume interrupted gathers and restore resolution sessions after a restart or a parent cancellation, and release refs/worktrees only once every writer has stopped and every dependent gather/descendant has released its pins.
**Architecture:** Six units in `domain/engine` plus one adapter file. `ResolveGroupRepoPath` (pure) and a `GatherPreflighter` port feed a files-mode branch of `GroupLauncher.Launch` that snapshots/forks through a dedicated file `WorkingArea` (`SetFileWorkingArea`) while results-only groups keep P2-T5's filesystem `GroupWorkspace`. `ParallelGroupExecutor.resolve`'s files branch moves into `continueFileGather`, which holds a per-run in-process gather gate (`TryLockGather` for the reconciler), reloads the run under it, never gathers for a terminal parent, and suspends on `ErrGroupGatherRollbackFailed`. `GroupGatherReconciler` (startup pass, sweep tick, and an event-bus `Watch`) aborts gathers under cancelled/failed/completed parents through `FileGatherer.AbortGather`, closes their sessions, and restores waiting parents (wake on a lost wake, finish an attempt whose session record is gone or closed, leave an open session untouched); `GroupResourcePruner` releases a done run's rollback pin, removes completed task workspaces (or retains them, warned like `Engine.warnWorkspaceRetained`, when nothing was applied) and finally releases the backing snapshot, which is also its durable "pruned" marker (listed by the git adapter's `SnapshotInventory`).
**Tech Stack:** Go 1.25.5 standard library (`context`, `errors`, `path`, `path/filepath`, `sync`, `log/slog`, `os` in the adapter only), `github.com/google/uuid`, testify; real temporary git repositories for the adapter, domain fakes for engine units.
**Spec / Design:** spec "Gathering file changes", "Existing behavior and cancellation" ("Cancelling a parent stops its unfinished parallel work"), "Completed runs retain task outcomes, gathered results, and merge-resolution outcomes"; `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` "Design decisions" (repoPath is a templated, optional hint within the immediate parent's workspace), §2 (launcher integration), §3 (`IsWait` while resolving, re-entry verification, failure without partial work), §4 (parent cancellation aborts/rolls back and leaves the parent cancelled), §5 "Cleanup and recovery" (all bullets), Error handling (invalid declared repo path / missing git before launch; rollback failure blocks continuation; parent cancellation never routes through success).

## Recovery of notification and automatic-session boundaries

The P3-T8 bridge implements `NotifyResolutionRequired(ctx, ref)` and records
`LLMConversation.notificationSent`. During restoration of a current OPEN session, retry
that method if the marker is absent through the mandatory
`closer GroupSessionCloser` method (production `*GroupResolutionBridge`). Extend the `recordingCloser`
fake to implement it keyed by full session ref and assert the notified identity. A CLOSED
automatic session with an unfinished attempt uses the existing completer path, wakes the
parent and verifies files without starting a second first turn. Add red tests for both
restart windows and notification lookup errors before implementation, then the existing
reconcile/error/cancel/edge suite to green. P3-T11's startup test drives these through
`initDomain`; notification delivery is an explicit production effect, never a nil no-op.


**Interfaces**
- Consumes:
  - Existing `domain/engine`: `ports.go:27-40` `WorkspaceManager` (`EnsureInstanceDir`, `RemoveInstanceDir`, `InstanceDirStats`), `ports.go:214-217` `QueueManager.NotifyCapacityFreed`, `ports.go:279-285` `EventBus` (`Publish`, `SubscribeAll() (<-chan ScopedEvent, func())`), `ports.go:260-275` `Event`/`ScopedEvent`, `ports.go:246-249` `EventTypeInstanceCompleted|Failed|Cancelled`, `conventions.go:15` `ReservedVarWorkspace`, `conventions.go:30-41` `WorkDirFromVars`, `executor_timer.go:21-26` `ContextWithInstanceID`, `workspace.go:68-88` `warnWorkspaceRetained` (log shape mirrored). Existing `domain/instance`: `instance.go:47-49` `State.IsTerminal`, `spawn_link.go:76` `MaxSpawnDepth`. Existing `domain/variable`: `variable.go:38` `NewStore`, `:100` `SetInput`, `:111` `SetReserved`.
  - P1-T1 `domain/workflow`: `GroupConfig{Form, Tasks, CompletionRule, UnfinishedBehavior, GatherMode, RepoPath, ResolverProfile}`, `GroupTask{Key, Entry}`, `GroupGatherResults`, `GroupGatherResultsAndFiles`, `GroupRuleAllSucceed`, `GroupRuleFirstSuccess`, `GroupUnfinished*`, `GroupFormBranches`, `GroupPortSuccess`, `GroupPortFailure`, `GroupScope{NodeID, Config, Bodies}`.
  - P2-T1 / P3-T4 `domain/instance`: `GroupRun` (incl. `Gather *GatherReport`, `WorkspaceSnapshot`, `ContinuationDelivered`, `Decision`), `GroupTask{Key, ChildInstanceID, LaunchState, FinalizedRef}`, `GroupRunState` constants, `GroupTaskLaunched`, `ChildRef{ID, State}` (P2-T12), `GroupRunRepository` (`Get`, `GetByChild`, `ListByParent`, `ListUnsettled`, `MarkContinuationDelivered`, `SetGatherReport`), `ErrGroupRunNotFound`, `NodeAddressedWaker.SetQueuedIfWaitingAtNode` (P2-T4), `GatherReport{State, Handle, MergeIndex, Attempts, ActiveSession, RollbackStatus, Error}`, `ResolutionAttempt{EndedAt, Outcome}`, `ResolutionSessionRef`.
  - P2-T6 `domain/variable`: `func ResolveTyped(v any, store *VariableStore) (any, error)`, `ErrUnresolvedReference`.
  - P2-T8 `domain/engine/group_launcher.go`: `GroupLauncher` (struct lines as written by P2-T8: fields `groups … now`), `launchPrep{l, snapshot, dirs}` + `cleanup`, `Launch`, `preLaunchError`, `checkLaunchRequest`, `captureStartVariables`, `prepareTask`, `newGroupRun`, `existingRun`, `launchAll`, `ErrGroupPreLaunch`, `GroupLaunchRequest`; test fakes in `group_fakes_test.go`: `memGroupRuns` / `newMemGroupRuns` (+ `failOnce`, `get`), `fakeGroupTaskStarter`, `recordingCanceller`, `dirWorkspaces` / `newDirWorkspaces` (`path`, `ensuredIDs`), `copyGroupWorkspace` / `newCopyGroupWorkspace` (`snapshotHandles`, `releasedHandles`, `forkErr`).
  - P2-T10: `NewGroupCoordinator`; test fake `memWaker` (`group_coordinator_test.go`).
  - P2-T11 `domain/engine/executor_group.go`: `ParallelGroupExecutor` (struct fields `instances … gatherer`), `NewParallelGroupExecutor`, `SetGatherer`, `Execute`, `resolve`, `deliver`, `gatherableGroupState`, `GroupGatherer`, `GatherStep`; test helpers `nodeClaims`, `gxGroupNode` (`executor_group_test.go`).
  - P2-T13/P2-T8: `EventTypeGroupCompleted` (published on the PARENT id with `Data["groupRunId"]`).
  - P3-T2 `domain/engine/working_area.go`: `WorkingArea`, `GatherHandle`, `MergeOutcome`; `adapter/git`: `WorkingArea` (fields `root`, `backing`, `git *Runner`), `NewWorkingArea`, `WithRunner`, `BackingDir`, `SnapshotParent`, `ReleaseSnapshot`, `ErrRepositoryLocked`, `resolveDir`; test helpers `newTestArea`, `mkdirIn`, `writeFile`, `authorRepo` (`adapter/git/testhelpers_test.go`).
  - P3-T1 `adapter/git/runner.go`: `(*Runner).Resolve() (string, error)`, `NewRunner`, `RunnerConfig{SearchPath, StateDir}`, `ErrGitUnavailable`.
  - P3-T3: `(*gitarea.WorkingArea).ReleaseRef` semantics (releasing a rollback ref also removes that gather's staging area).
  - P3-T5: `GroupFileFinalizer.RetainWorkspace` keeps files-mode task workspaces until the run is `done` (this task prunes them).
  - P3-T6 `domain/engine/group_gatherer.go`: `FileGatherer`, `NewGroupGatherer`, `(*FileGatherer).AbortGather(ctx, run, outcome, reason string) (*GatherStep, error)`, `ErrGroupGatherRollbackFailed`, `GatherHandleFromReport`, `ResolutionOutcomeCancelled`, `ResolutionOutcomeError`, package helpers `latestAttemptIndex`, `copyGatherReport`, constant `gatherStateApplied`; test fakes (`group_gather_fakes_test.go` / `group_gatherer_test.go`): `fakeGatherArea` (`parents`, `refs`, `conflicts`, `failMerge`, `failRollback`, `failReleaseSnapshot`, `releasedSnapshots`, `releasedRefs`, `parentFiles`, `begins`, `rollbackCount`, `timesApplied`, `mergeOrder`, `resolveStaged`), `fakeResolutionStarter` (`calls`), `memGroupRuns` helpers `put`, `snapshot`, `update`, `failCall`, fixture `gatherFixture` / `newGatherFixture`, `gatherParentDir`, `taskRef`, `(*gatherFixture).gather`, `.report`, `.endOpenAttempts`, `.conflictOnB`, `gatherSection`, `taskReportByKey`.
  - P3-T8 `domain/engine/group_resolution.go`: `GroupSessionCloser.CloseSession(ctx, ref instance.ResolutionSessionRef, reason string) error`, `GroupResolutionCompleter.OnResolutionSessionEnded(ctx, instanceID uuid.UUID, nodeID string, abandoned bool) error`, `IsOpenGroupResolutionSession`, `findGroupSessionExec(ctx, repo, ref) (*instance.NodeExecution, error)` (package-private), `ConversationKey{Kind,GroupRunID,MergeIndex,Attempt,TaskKey,EndedAt,EndReason}`, `ConversationKindGroupResolution`; test fakes `recordingCompleter` (`all()`), `fakeNodeWaker` (`wakeCalls()`).
  - Existing engine test fakes (`domain/engine/engine_test.go`): `newMockInstanceRepo` / `mockInstanceRepo` (`instances`, `mu`, `get`), `newMockNodeExecutionRepo`, `newMockQueueManager` (`notifyCount`), `newMockEventBus`; `fixedIdentityResolver` (`orphan_reconciler_test.go:15-28`).
- Produces:
  - `domain/engine/group_files_ports.go`: `var ErrGroupRepoPathInvalid = errors.New("parallel_group repoPath is invalid")`; `const GroupConfigKeyResolvedRepoPath = "resolvedRepoPath"`; `type GatherPreflighter interface { PreflightGather(ctx context.Context, parentDir, repoRel string) error }`; `type SnapshotInfo struct { Handle string; ModTime time.Time }`; `type SnapshotInventory interface { ListSnapshots(ctx context.Context) ([]SnapshotInfo, error); HasSnapshot(ctx context.Context, handle string) (bool, error) }`; `type ChildLister interface { ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) }`; `type GatherAborter interface { AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error) }`; `type GatherLocker interface { TryLockGather(runID uuid.UUID) (unlock func(), ok bool) }`.
  - `domain/engine/group_repo_path.go`: `func ResolveGroupRepoPath(raw string, vars *variable.VariableStore, parentWorkspace string) (string, error)` — returns a cleaned slash-separated path relative to the parent's working area (`""` = no hint, `"."` = the whole area).
  - `adapter/git/preflight.go`: `func (a *WorkingArea) PreflightGather(ctx context.Context, parentDir, repoRel string) error`; `func (a *WorkingArea) ListSnapshots(ctx context.Context) ([]engine.SnapshotInfo, error)`; `func (a *WorkingArea) HasSnapshot(ctx context.Context, handle string) (bool, error)`; `var _ engine.GatherPreflighter`, `var _ engine.SnapshotInventory`.
  - `domain/engine/group_launch_files.go`: `func (l *GroupLauncher) SetFileWorkingArea(area WorkingArea)`; `GroupLauncher` gains field `fileArea WorkingArea`; `launchPrep` gains `ws GroupWorkspace`; files-mode `Launch` persists `Config[GroupConfigKeyResolvedRepoPath]`.
  - `domain/engine/executor_group_files.go`: `func (x *ParallelGroupExecutor) TryLockGather(runID uuid.UUID) (unlock func(), ok bool)`; package-private `continueFileGather`, `gatherGate`; `ParallelGroupExecutor` gains field `gate gatherGate`.
  - `domain/engine/group_prune.go`: `const GroupOrphanSnapshotGrace = time.Hour`; `type GroupResourcePruner struct{…}`; `func NewGroupResourcePruner(groups instance.GroupRunRepository, instances instance.InstanceRepository, children ChildLister, workspaces WorkspaceManager, area WorkingArea, inventory SnapshotInventory) *GroupResourcePruner`; `func (p *GroupResourcePruner) PruneRun(ctx context.Context, runID uuid.UUID) (bool, error)`; `func (p *GroupResourcePruner) Sweep(ctx context.Context) error`.
  - `domain/engine/group_gather_reconciler.go`: `type GroupGatherReconciler struct{…}`; `func NewGroupGatherReconciler(groups instance.GroupRunRepository, instances instance.InstanceRepository, nodeExecs instance.NodeExecutionRepository, aborter GatherAborter, closer GroupSessionCloser, completer GroupResolutionCompleter, waker instance.NodeAddressedWaker, queueMgr QueueManager, locker GatherLocker, bus EventBus, pruner *GroupResourcePruner) *GroupGatherReconciler`; `func (r *GroupGatherReconciler) Reconcile(ctx context.Context) error` (structurally satisfies P2-T14's `cmd/arbitron/main.go:groupReconciler`); `func (r *GroupGatherReconciler) RecoverRun(ctx context.Context, runID uuid.UUID) error`; `func (r *GroupGatherReconciler) Watch(ctx context.Context) <-chan struct{}`.
  - Behaviour later tasks rely on: results-only groups never touch the file area (no git dependency); a files-mode pre-launch problem (no file area, no parent `{{workspace}}`, bad/escaping/unresolved `repoPath`, git missing, locked declared repository) is an `ErrGroupPreLaunch` error before anything is persisted; a parent whose gather rollback failed stays `waiting` at the group node and is re-woken by the reconciler to retry; a cancelled parent's gather is rolled back and settled on `failure` within one bus event, the parent stays `cancelled`; a done run's backing directory disappears once its tasks, their descendants and nested runs are terminal/done.

**Wiring & data provenance**
- Composition root: **P3-T11** (`cmd/arbitron/main.go:initDomain`): `groupLauncher.SetFileWorkingArea(workingArea)` (next to P2-T14's `groupLauncher := engine.NewGroupLauncher(...)`), `groupExecutor.SetGatherer(gatherer)` (the executor's `continueFileGather` is then live), `groupPruner := engine.NewGroupResourcePruner(groupRunRepo, instanceRepo, instanceRepo, wsMgr, workingArea, workingArea)`, `gatherReconciler := engine.NewGroupGatherReconciler(groupRunRepo, instanceRepo, nodeExecRepo, gatherer, groupResolution, groupResolution, queueRepo, queueMgr, groupExecutor, bus, groupPruner)`, startup `gatherReconciler.Reconcile`, `go sweepGroupReconciler(groupSweepCtx, gatherReconciler, groupSweepInterval, logger)` and `gatherReconciler.Watch(groupSweepCtx)`. Each line's failing e2e is listed in P3-T11.
- Producers of every input:
  - `raw` repoPath — `workflow.GroupConfig.RepoPath` from `ParseGroupConfig(node.Config)` (P1-T1) inside `ParallelGroupExecutor.firstEntry` (P2-T11), statically checked by P1-T3 (`GROUP_INVALID_REPO_PATH`); `vars` — `GroupLaunchRequest.Vars` = the parent's live store built by `domain/engine/engine.go:868` `variable.FromSnapshotWithNodeIDs` + `workspace.go:32` `prepareWorkspace`; `parentWorkspace` — `WorkDirFromVars(req.Vars)` (`conventions.go:30-41`), i.e. `adapter/filesystem/workspace.go:59-75` `EnsureInstanceDir`.
  - `fileArea` / `area` / `inventory` — `gitarea.NewWorkingArea(cfg.Workspace.Path)` (P3-T2/T3, wired by P3-T11); the preflight runner — P3-T1 `Runner.Resolve` inside it.
  - `run` for `continueFileGather` — re-read under the gate from `adapter/sqlite/group_run_repo.go:GroupRunRepo.Get` (P2-T3, hydrated by P3-T4); `parent` — `adapter/sqlite/instance_repo.go:InstanceRepo.Get`; `gatherer` — P3-T6 `NewGroupGatherer` (P3-T11).
  - `groups` / `instances` / `nodeExecs` — `adaptsqlite.NewGroupRunRepo(db)` (P2-T14), `adaptsqlite.NewInstanceRepo(db)` (main.go:373), `adaptsqlite.NewNodeExecutionRepo(db)` (main.go:376); `children` — `*adaptsqlite.InstanceRepo.ListChildren` (P2-T12); `workspaces` — `wsMgr` (main.go:814).
  - `aborter` — `*engine.FileGatherer` (P3-T6); `closer` / `completer` — `*engine.GroupResolutionBridge` (P3-T8); `waker` — `*adaptsqlite.QueueRepo` (`queueRepo`, main.go:797, P2-T4 `SetQueuedIfWaitingAtNode`); `queueMgr` — main.go:830; `locker` — the registered `groupExecutor` (P2-T14); `bus` — `eventbus.New()` (main.go:618), whose `SubscribeAll` delivers `instance.cancelled` (`engine.go:1611-1616` `publishCancelledEvent`), `instance.failed` (`engine.go:1068-1072`), `instance.completed` (`engine.go:978-981`) and `group.completed` (P3-T6 `finish`, P2-T10 `finalize`).
  - Session records — P3-T8 `GroupResolutionBridge.StartResolution` (`NodeExecution` on the parent at the group node), read through `findGroupSessionExec`.
- Provenance probes: `TestGroupLauncher_FilesMode_PreflightsThenSnapshotsThroughTheFileArea` (fails if the resolved repoPath or the parent workspace do not reach the preflight, or the snapshot is taken by the results workspace); `TestGroupLauncher_FilesMode_WithoutAFileAreaRefuses` (fails if an unwired file area silently degrades to the results workspace); `TestParallelGroupExecutor_FileGather_TryLockGatherExcludesTheReconcilerWhileGathering` (fails if the executor does not hold the gate the reconciler probes); `TestGroupResourcePruner_AppliedGatherReleasesSnapshotAndCompletedTaskWorkspaces` asserts the SINK (directories gone on disk, `ReleaseSnapshot` called with `run.WorkspaceSnapshot`); `TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles` asserts the SINK (`fakeGatherArea.parentFiles` restored, run `done` on `failure`, parent still `cancelled`); `TestListSnapshots_ListsOnlyBackingRepositories` / `TestPreflightGather_*` run real git.

**Decisions settled here**
- *Results mode keeps the filesystem snapshot.* Only `results_and_files` launches go through the file `WorkingArea`, so hosts without git keep running results-only groups (P2 behaviour unchanged). This refines P3-T2's wiring note ("the launcher receives `workingArea` instead of P2-T5's `filesystem.NewGroupWorkspace`"): the launcher receives it IN ADDITION, via `SetFileWorkingArea`.
- *repoPath* is resolved against the parent's store at launch; an absolute result is accepted only when inside the parent's `{{workspace}}` and is stored relative. Any `..` segment, NUL, rooted backslash path, blank result, non-string template result or unresolved reference is `ErrGroupRepoPathInvalid`. The adapter then checks physically (after symlink resolution) that it exists, is a directory inside the parent area, and is not a git repository with `index.lock`. A plain directory is accepted: the hint never decides what is captured or gathered.
- *One writer.* The executor holds a per-run gate for the whole `Gather` call; the reconciler only `TryLockGather`s (a busy gate means the parent's own goroutine is writing; the next pass re-checks). The gate is in-process; after a restart no executor runs before the startup pass (P2-T14 ordering).
- *Rollback failure suspends.* `ErrGroupGatherRollbackFailed` is not turned into a node error (an author's `skip`/`route` error behaviour would otherwise continue past a half-restored area): the parent stays `waiting` at the group node and the reconciler wakes it each pass to retry; operators see `GatherReport.RollbackStatus = "rollback_failed"`.
- *Recovery by parent state.* `cancelled` → close the active session (`"cancelled"`), `AbortGather(…, cancelled)`; `failed` (incl. crash-recovered) → close (`"error"`), `AbortGather(…, error)`; `completed` (the parent left the node through an error behaviour and its workspace is gone) → abort WITHOUT the gather handle so nothing is written to a vanished area; then `MarkContinuationDelivered`. `waiting` at the node → `decided`(actions applied)/`gathering` ⇒ wake; `resolving_conflicts` ⇒ ended attempt: close the record (`"ended"`) and wake; no active session recorded ⇒ wake (the gatherer opens it); session record missing or closed while the attempt is open ⇒ `OnResolutionSessionEnded(abandoned = endReason=="abandoned")`; open record ⇒ restored as is. Any other parent state: untouched (the parent's executor owns the gather). A later operator `Retry` of a failed parent re-enters the done run and routes its frozen `failure`.
- *Pruning.* A done files-mode run first releases its gather's rollback ref (which removes the staging area). Its snapshot is released only when every launched task row is terminal, has no non-terminal descendant (walked through terminal intermediates, depth ≤ `MaxSpawnDepth`) and every group run whose parent is that task is `done`. Completed task workspaces are removed when the gather `applied`; otherwise they are retained and logged exactly like `Engine.warnWorkspaceRetained` (reason `group_gather_not_applied`); failed/cancelled task workspaces are never touched (the engine retained them). `ReleaseSnapshot` is last, so a backing directory's absence is the durable "already pruned" marker; `Sweep` walks the adapter's inventory and also releases orphaned snapshots (no run row) older than `GroupOrphanSnapshotGrace`.

### Task 10.1: Ports and runtime repoPath resolution
**Files:**
- Create: `domain/engine/group_files_ports.go` (declarations only; exercised by every later sub-task)
- Create: `domain/engine/group_repo_path.go`
- Test: `domain/engine/group_repo_path_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/group_repo_path_test.go`:

```go
package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const repoPathParentWS = "/ws/instances/parent"

// repoVars builds the parent's live store: {{workspace}} seeded the way
// Engine.prepareWorkspace does it, plus ordinary inputs.
func repoVars(t *testing.T, workspace string, inputs map[string]any) *variable.VariableStore {
	t.Helper()
	s := variable.NewStore()
	if workspace != "" {
		s.SetReserved("workspace", workspace)
	}
	for k, v := range inputs {
		require.NoError(t, s.SetInput(k, v))
	}
	return s
}

func TestResolveGroupRepoPath_EmptyIsNoHint(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestResolveGroupRepoPath_LiteralRelativePathIsCleaned(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("src/./app/", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src/app", got)
}

func TestResolveGroupRepoPath_TemplateResolvesAgainstTheParentsStore(t *testing.T) {
	got, err := engine.ResolveGroupRepoPath("{{repo_dir}}/app", repoVars(t, repoPathParentWS, map[string]any{"repo_dir": "src"}), repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src/app", got, "the template is resolved from the PARENT's variables at launch")
}

func TestResolveGroupRepoPath_WorkspaceTemplateBecomesRelative(t *testing.T) {
	vars := repoVars(t, repoPathParentWS, nil)
	got, err := engine.ResolveGroupRepoPath("{{workspace}}/src", vars, repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, "src", got)

	got, err = engine.ResolveGroupRepoPath("{{workspace}}", vars, repoPathParentWS)
	require.NoError(t, err)
	assert.Equal(t, ".", got, "the whole working area is a valid hint")
}

func TestResolveGroupRepoPath_RejectsPathsOutsideTheImmediateParent(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		inputs map[string]any
	}{
		{"blank", "   ", nil},
		{"absolute outside", "/etc", nil},
		{"literal climb", "../sibling", nil},
		{"climb inside", "src/../../sibling", nil},
		{"templated climb", "{{up}}/x", map[string]any{"up": ".."}},
		{"workspace climb", "{{workspace}}/../other", nil},
		{"rooted backslash", `\share\repo`, nil},
		{"blank after resolution", "{{empty}}", map[string]any{"empty": "  "}},
		{"nul byte", "src\x00x", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.ResolveGroupRepoPath(tc.raw, repoVars(t, repoPathParentWS, tc.inputs), repoPathParentWS)
			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
		})
	}
}

func TestResolveGroupRepoPath_UnresolvedReferenceIsActionable(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{missing}}/x", repoVars(t, repoPathParentWS, nil), repoPathParentWS)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	assert.Contains(t, err.Error(), "{{missing}}/x", "the message names the author's template")
}

func TestResolveGroupRepoPath_NonStringTemplateIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{count}}", repoVars(t, repoPathParentWS, map[string]any{"count": 3}), repoPathParentWS)
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.Contains(t, err.Error(), "not a path")
}

func TestResolveGroupRepoPath_AbsoluteWithoutAParentAreaIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("/ws/instances/parent/src", repoVars(t, "", nil), "")
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
}

func TestResolveGroupRepoPath_TemplateWithoutAStoreIsRejected(t *testing.T) {
	_, err := engine.ResolveGroupRepoPath("{{repo_dir}}", nil, repoPathParentWS)
	require.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestResolveGroupRepoPath' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_repo_path_test.go:...: undefined: engine.ResolveGroupRepoPath
domain/engine/group_repo_path_test.go:...: undefined: engine.ErrGroupRepoPathInvalid
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Create the ports. Create `domain/engine/group_files_ports.go`:

```go
package engine

import (
	"context"
	"errors"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ErrGroupRepoPathInvalid marks a parallel_group repoPath that cannot be used
// as a repository hint inside the immediate parent's working area: it does not
// resolve, escapes the area, is not a directory, or is otherwise malformed. It
// is always reported before any task starts (wrapped in ErrGroupPreLaunch).
var ErrGroupRepoPathInvalid = errors.New("parallel_group repoPath is invalid")

// GroupConfigKeyResolvedRepoPath is the GroupRun.Config key under which the
// launcher records the runtime-resolved repoPath (slash-separated, relative to
// the immediate parent's working area). Absent when no repoPath is declared.
const GroupConfigKeyResolvedRepoPath = "resolvedRepoPath"

// GatherPreflighter is implemented by file working areas that can verify, before
// any task starts, that file gathering can work for a parent area: the host
// tools are available and the optional repository hint (relative to parentDir,
// "" = none) exists inside it and is usable.
type GatherPreflighter interface {
	PreflightGather(ctx context.Context, parentDir, repoRel string) error
}

// SnapshotInfo is one group-start snapshot held by a file working area.
type SnapshotInfo struct {
	Handle  string
	ModTime time.Time
}

// SnapshotInventory lists the snapshots a file working area still holds. A
// snapshot that is no longer listed has been released (the pruner's durable
// "already pruned" marker).
type SnapshotInventory interface {
	ListSnapshots(ctx context.Context) ([]SnapshotInfo, error)
	HasSnapshot(ctx context.Context, handle string) (bool, error)
}

// ChildLister lists every child of a run (terminal ones included), one level.
// *adapter/sqlite.InstanceRepo satisfies it (P2-T12 ListChildren).
type ChildLister interface {
	ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error)
}

// GatherAborter rolls a file gather back and settles its run on the failure
// route from outside the parent's run. *FileGatherer (P3-T6) satisfies it.
type GatherAborter interface {
	AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error)
}

var _ GatherAborter = (*FileGatherer)(nil)

// GatherLocker lets the reconciler find out whether this process is writing a
// run's file gather right now. ok=false means a writer holds it; ok=true hands
// the caller exclusive use until it calls unlock.
type GatherLocker interface {
	TryLockGather(runID uuid.UUID) (unlock func(), ok bool)
}
```

- [x] Step 4: Implement. Create `domain/engine/group_repo_path.go`:

```go
package engine

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
)

// ResolveGroupRepoPath resolves a parallel_group repoPath at launch.
//
// raw is the author's value (P1-T1 GroupConfig.RepoPath), possibly templated;
// templates resolve against the PARENT's live store (vars). The result is a
// cleaned, slash-separated path relative to parentWorkspace: "" when no hint is
// declared, "." for the whole area. An absolute result is accepted only when it
// lies inside parentWorkspace (e.g. "{{workspace}}/src"). Anything that cannot
// stay inside the immediate parent's working area is ErrGroupRepoPathInvalid;
// an unresolved reference additionally wraps variable.ErrUnresolvedReference.
// This is the lexical half; the file working area's GatherPreflighter checks
// the resolved path physically (symlinks, existence, directory).
func ResolveGroupRepoPath(raw string, vars *variable.VariableStore, parentWorkspace string) (string, error) {
	if raw == "" {
		return "", nil
	}
	resolved := raw
	if strings.Contains(raw, "{{") {
		if vars == nil {
			return "", fmt.Errorf("%w: repoPath %q is templated but the parent's variables are unavailable", ErrGroupRepoPathInvalid, raw)
		}
		v, err := variable.ResolveTyped(raw, vars)
		if err != nil {
			return "", fmt.Errorf("%w: repoPath %q: %w", ErrGroupRepoPathInvalid, raw, err)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%w: repoPath %q resolved to a %T, not a path", ErrGroupRepoPathInvalid, raw, v)
		}
		resolved = s
	}
	resolved = strings.TrimSpace(resolved)
	switch {
	case resolved == "":
		return "", fmt.Errorf("%w: repoPath %q is blank once resolved", ErrGroupRepoPathInvalid, raw)
	case strings.ContainsRune(resolved, 0):
		return "", fmt.Errorf("%w: repoPath %q contains a NUL byte", ErrGroupRepoPathInvalid, raw)
	case strings.HasPrefix(resolved, `\`):
		return "", fmt.Errorf("%w: repoPath %q must be relative to the parent's working area", ErrGroupRepoPathInvalid, raw)
	}
	if filepath.IsAbs(resolved) {
		root := strings.TrimSpace(parentWorkspace)
		if root == "" {
			return "", fmt.Errorf("%w: repoPath %q resolved to the absolute path %q, and the parent run has no working area to contain it",
				ErrGroupRepoPathInvalid, raw, resolved)
		}
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(resolved))
		if err != nil || relClimbsOut(rel) {
			return "", fmt.Errorf("%w: repoPath %q resolved to %q, outside the parent's working area %q",
				ErrGroupRepoPathInvalid, raw, resolved, root)
		}
		return filepath.ToSlash(rel), nil
	}
	slashed := filepath.ToSlash(resolved)
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: repoPath %q (resolved %q) climbs out of the parent's working area",
				ErrGroupRepoPathInvalid, raw, resolved)
		}
	}
	return path.Clean(slashed), nil
}

// relClimbsOut reports whether a filepath.Rel result leaves its base.
func relClimbsOut(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
```

- [x] Step 5: Run, confirm it passes.

```bash
go test ./domain/engine/ -run 'TestResolveGroupRepoPath' -count=1 -v 2>&1 | tail -15
```
Expected:
```
--- PASS: TestResolveGroupRepoPath_EmptyIsNoHint (0.00s)
--- PASS: TestResolveGroupRepoPath_LiteralRelativePathIsCleaned (0.00s)
--- PASS: TestResolveGroupRepoPath_TemplateResolvesAgainstTheParentsStore (0.00s)
--- PASS: TestResolveGroupRepoPath_WorkspaceTemplateBecomesRelative (0.00s)
--- PASS: TestResolveGroupRepoPath_RejectsPathsOutsideTheImmediateParent (0.00s)
--- PASS: TestResolveGroupRepoPath_UnresolvedReferenceIsActionable (0.00s)
--- PASS: TestResolveGroupRepoPath_NonStringTemplateIsRejected (0.00s)
--- PASS: TestResolveGroupRepoPath_AbsoluteWithoutAParentAreaIsRejected (0.00s)
--- PASS: TestResolveGroupRepoPath_TemplateWithoutAStoreIsRejected (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 6: gofmt, vet, commit.

10.1: Confirmed missing-symbol red, then focused green, formatting/vet clean. Ordered review: (1) enforced the stated ban on **any** `..` segment, including absolute paths, with an additional red/green regression; (2) template tests require real nonempty parent-store inputs, with launcher/root sink proofs assigned to 10.3/P3-T11; (3) checked neighboring path/variable conventions, reran focused tests and vet. Production-root completion remains gated below; this commit does not enable partial file mode.

```bash
gofmt -l domain/engine/group_files_ports.go domain/engine/group_repo_path.go domain/engine/group_repo_path_test.go
go vet ./domain/engine/
git add domain/engine/group_files_ports.go domain/engine/group_repo_path.go domain/engine/group_repo_path_test.go && git commit -m "feat(engine): resolve and contain parallel_group repoPath at launch; file-gather recovery ports" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `gofmt -l` and `go vet` print nothing; one commit created.

### Task 10.2: Git working area preflight and snapshot inventory
**Files:**
- Create: `adapter/git/preflight.go`
- Test: `adapter/git/preflight_test.go` (package `git`; reuses P3-T2's `newTestArea`, `mkdirIn`, `writeFile`, `authorRepo` from `adapter/git/testhelpers_test.go` and P3-T1's `requireGit`)

- [x] Step 1: Write the failing tests. Create `adapter/git/preflight_test.go`:

```go
package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreflightGather_NoRepoPathNeedsOnlyGitAndTheParentArea(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")

	require.NoError(t, a.PreflightGather(ctx, parent, ""))
	err := a.PreflightGather(ctx, filepath.Join(root, "instances", "missing"), "")
	require.Error(t, err, "a parent area that does not exist cannot be gathered into")
}

func TestPreflightGather_MissingGitIsReportedBeforeAnyTaskStarts(t *testing.T) {
	root := t.TempDir()
	a, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{SearchPath: t.TempDir(), StateDir: t.TempDir()})))
	require.NoError(t, err)
	parent := mkdirIn(t, root, "instances/p")

	err = a.PreflightGather(context.Background(), parent, "")
	require.ErrorIs(t, err, ErrGitUnavailable)
	assert.Contains(t, err.Error(), "results_and_files", "the message names the gather mode that needs git")
}

func TestPreflightGather_DeclaredRepositoryOrPlainDirectoryInsideTheParentIsAccepted(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	mkdirIn(t, parent, "src")
	authorRepo(t, filepath.Join(parent, "src"), map[string]string{"main.go": "package main\n"})
	mkdirIn(t, parent, "docs")

	require.NoError(t, a.PreflightGather(ctx, parent, "src"), "an author repository is the intended hint")
	require.NoError(t, a.PreflightGather(ctx, parent, "docs"), "a plain directory hint is not rejected")
	require.NoError(t, a.PreflightGather(ctx, parent, "."), "the whole area is a valid hint")
}

func TestPreflightGather_RejectsHintsThatCannotBeUsed(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "notes.txt", "x", 0o644)
	outside := mkdirIn(t, root, "outside")
	require.NoError(t, os.Symlink(outside, filepath.Join(parent, "escape")))

	for name, rel := range map[string]string{
		"missing":     "nope",
		"file":        "notes.txt",
		"symlink out": "escape",
		"absolute":    "/etc",
		"climb":       "../outside",
	} {
		t.Run(name, func(t *testing.T) {
			err := a.PreflightGather(ctx, parent, rel)
			require.Error(t, err)
			assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
		})
	}
}

func TestPreflightGather_LockedDeclaredRepositoryIsActionable(t *testing.T) {
	a, root := newTestArea(t)
	parent := mkdirIn(t, root, "instances/p")
	mkdirIn(t, parent, "src")
	authorRepo(t, filepath.Join(parent, "src"), map[string]string{"a.txt": "a"})
	writeFile(t, filepath.Join(parent, "src", ".git"), "index.lock", "", 0o644)

	err := a.PreflightGather(context.Background(), parent, "src")
	require.ErrorIs(t, err, ErrRepositoryLocked)
	assert.Contains(t, err.Error(), "index.lock", "the operator is told which file blocks the gather")
}

func TestListSnapshots_ListsOnlyBackingRepositories(t *testing.T) {
	a, root := newTestArea(t)
	ctx := context.Background()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "f.txt", "x", 0o644)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)

	// Things that live under .group-backing but are not snapshots.
	mkdirIn(t, root, ".group-backing/not-a-uuid.git")
	mkdirIn(t, root, ".group-backing/"+uuid.New().String()+"-gather-"+uuid.New().String())
	writeFile(t, root, ".group-backing/"+uuid.New().String()+".git", "a file, not a repository", 0o644)

	infos, err := a.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, handle, infos[0].Handle)
	assert.WithinDuration(t, time.Now(), infos[0].ModTime, time.Minute)

	has, err := a.HasSnapshot(ctx, handle)
	require.NoError(t, err)
	assert.True(t, has)

	require.NoError(t, a.ReleaseSnapshot(ctx, handle))
	infos, err = a.ListSnapshots(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos, "a released snapshot is no longer listed")
	has, err = a.HasSnapshot(ctx, handle)
	require.NoError(t, err)
	assert.False(t, has)

	_, err = a.HasSnapshot(ctx, "../etc")
	assert.Error(t, err, "a handle is used in a path and must be a group-run id")
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./adapter/git/ -run 'TestPreflightGather|TestListSnapshots' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/adapter/git [github.com/arbitron/arbitron/adapter/git.test]
adapter/git/preflight_test.go:...: a.PreflightGather undefined (type *WorkingArea has no field or method PreflightGather)
adapter/git/preflight_test.go:...: a.ListSnapshots undefined (type *WorkingArea has no field or method ListSnapshots)
adapter/git/preflight_test.go:...: a.HasSnapshot undefined (type *WorkingArea has no field or method HasSnapshot)
FAIL	github.com/arbitron/arbitron/adapter/git [build failed]
```

- [x] Step 3: Implement. Create `adapter/git/preflight.go`:

```go
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var (
	_ engine.GatherPreflighter = (*WorkingArea)(nil)
	_ engine.SnapshotInventory = (*WorkingArea)(nil)
)

// PreflightGather verifies, before any task of a results_and_files group
// starts, that the gather can work: git is available to this server (checked
// at use, never cached), parentDir is an existing directory, and the optional
// repository hint repoRel (slash-separated, relative to parentDir; "" = none)
// resolves — after symlinks — to a directory INSIDE the parent's area that is
// not a git repository locked by another git process. A plain directory hint
// is accepted: the hint never limits what is captured or gathered.
func (a *WorkingArea) PreflightGather(ctx context.Context, parentDir, repoRel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.git.Resolve(); err != nil {
		return fmt.Errorf("git working area: gatherMode results_and_files gathers files with git: %w", err)
	}
	parent, err := resolveDir(parentDir)
	if err != nil {
		return fmt.Errorf("git working area: the parent's working area cannot be gathered into: %w", err)
	}
	if repoRel == "" {
		return nil
	}
	if filepath.IsAbs(repoRel) || strings.HasPrefix(repoRel, `\`) {
		return fmt.Errorf("%w: %q must be relative to the parent's working area", engine.ErrGroupRepoPathInvalid, repoRel)
	}
	for _, seg := range strings.Split(filepath.ToSlash(repoRel), "/") {
		if seg == ".." {
			return fmt.Errorf("%w: %q climbs out of the parent's working area", engine.ErrGroupRepoPathInvalid, repoRel)
		}
	}
	target, err := filepath.EvalSymlinks(filepath.Join(parent, filepath.FromSlash(repoRel)))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %q does not exist in the parent's working area %s", engine.ErrGroupRepoPathInvalid, repoRel, parent)
	}
	if err != nil {
		return fmt.Errorf("%w: %q: %v", engine.ErrGroupRepoPathInvalid, repoRel, err)
	}
	rel, err := filepath.Rel(parent, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q resolves to %s, outside the parent's working area %s", engine.ErrGroupRepoPathInvalid, repoRel, target, parent)
	}
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", engine.ErrGroupRepoPathInvalid, repoRel, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", engine.ErrGroupRepoPathInvalid, repoRel)
	}
	lock := filepath.Join(target, ".git", "index.lock")
	if _, err := os.Stat(lock); err == nil {
		return fmt.Errorf("git working area: repoPath %q: %w; finish or abort the git operation running there, or remove the stale %s",
			repoRel, ErrRepositoryLocked, lock)
	}
	return nil
}

// ListSnapshots lists every group-start snapshot this area still holds: the
// "<groupRunID>.git" backing repositories under root/.group-backing. Staging
// areas, the runner's private state and anything that is not a UUID-named
// directory are ignored. Sorted by handle.
func (a *WorkingArea) ListSnapshots(ctx context.Context) ([]engine.SnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.backing)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git working area: list snapshots: %w", err)
	}
	var out []engine.SnapshotInfo
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasSuffix(name, ".git") {
			continue
		}
		id, err := uuid.Parse(strings.TrimSuffix(name, ".git"))
		if err != nil || id == uuid.Nil {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue // released while listing
		}
		if err != nil {
			return nil, fmt.Errorf("git working area: list snapshots: %w", err)
		}
		out = append(out, engine.SnapshotInfo{Handle: id.String(), ModTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// HasSnapshot reports whether handle's backing repository still exists. A
// handle that is not a group-run id is an error (it would be used in a path).
func (a *WorkingArea) HasSnapshot(ctx context.Context, handle string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := a.BackingDir(handle)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("git working area: inspect snapshot %s: %w", handle, err)
	}
	return info.IsDir(), nil
}
```

- [x] Step 4: Run, confirm it passes.

```bash
go test ./adapter/git/ -run 'TestPreflightGather|TestListSnapshots' -count=1 -v 2>&1 | tail -12
```
Expected:
```
--- PASS: TestPreflightGather_NoRepoPathNeedsOnlyGitAndTheParentArea (0.0Xs)
--- PASS: TestPreflightGather_MissingGitIsReportedBeforeAnyTaskStarts (0.00s)
--- PASS: TestPreflightGather_DeclaredRepositoryOrPlainDirectoryInsideTheParentIsAccepted (0.0Xs)
--- PASS: TestPreflightGather_RejectsHintsThatCannotBeUsed (0.0Xs)
--- PASS: TestPreflightGather_LockedDeclaredRepositoryIsActionable (0.0Xs)
--- PASS: TestListSnapshots_ListsOnlyBackingRepositories (0.0Xs)
PASS
ok  	github.com/arbitron/arbitron/adapter/git	0.XXXs
```
(On a host without git, the tests built on `newTestArea` SKIP through P3-T1's `requireGit`; `TestPreflightGather_MissingGitIsReportedBeforeAnyTaskStarts` always runs.)

- [x] Step 5: Whole package, gofmt, vet, commit.

10.2: Missing-method red → focused green → whole Git package green; formatting/vet clean. Ordered review: (1) matches preflight/inventory slice without enabling file mode; (2) real Git snapshot producer and physical directories/symlink/lock inputs drive inventory/removal and preflight sinks, not canned handles; (3) checked adjacent WorkingArea containment/handle conventions. Reran focused tests after review, green. P3-T11 still owns production construction.

```bash
go test ./adapter/git/ -count=1
gofmt -l adapter/git/preflight.go adapter/git/preflight_test.go
go vet ./adapter/git/
git add adapter/git/preflight.go adapter/git/preflight_test.go && git commit -m "feat(git): file-gather preflight (git availability, repoPath containment, locked repo) and snapshot inventory" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok  	github.com/arbitron/arbitron/adapter/git`; `gofmt -l` and `go vet` print nothing.

### Task 10.3: Launcher files-mode preflight
**Files:**
- Create: `domain/engine/group_launch_files.go`
- Modify: `domain/engine/group_launcher.go` (created by P2-T8): the `GroupLauncher` struct (P2-T8 Step 10, the `type GroupLauncher struct {` block), the `launchPrep` struct + `cleanup` (the `type launchPrep struct {` block through the end of `func (p *launchPrep) cleanup`), and `func (l *GroupLauncher) Launch` (whole function). Locate each by its quoted anchor; nothing else in the file changes.
- Test: `domain/engine/group_launch_files_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/group_launch_files_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errLaunchAreaUnused = errors.New("not used by the launcher")

// launchFileArea is a file WorkingArea whose snapshot/fork really copy
// directories (P2-T8's copyGroupWorkspace), so the tests assert the SINK: the
// bytes in each task directory. The gather half is never reached by Launch.
type launchFileArea struct {
	*copyGroupWorkspace
}

var _ engine.WorkingArea = launchFileArea{}

func (launchFileArea) FinalizeTask(context.Context, string, uuid.UUID, string) (string, error) {
	return "", errLaunchAreaUnused
}
func (launchFileArea) BeginGather(context.Context, string, uuid.UUID, string) (engine.GatherHandle, error) {
	return engine.GatherHandle{}, errLaunchAreaUnused
}
func (launchFileArea) MergeTask(context.Context, engine.GatherHandle, string) (engine.MergeOutcome, error) {
	return engine.MergeOutcome{}, errLaunchAreaUnused
}
func (launchFileArea) ConflictPaths(context.Context, engine.GatherHandle) ([]string, error) {
	return nil, errLaunchAreaUnused
}
func (launchFileArea) CommitResolution(context.Context, engine.GatherHandle, string) error {
	return errLaunchAreaUnused
}
func (launchFileArea) Apply(context.Context, engine.GatherHandle) error    { return errLaunchAreaUnused }
func (launchFileArea) Rollback(context.Context, engine.GatherHandle) error { return errLaunchAreaUnused }
func (launchFileArea) ReleaseRef(context.Context, string, string) error   { return errLaunchAreaUnused }

type preflightCall struct {
	parentDir string
	repoRel   string
}

// preflightFileArea additionally records every preflight with the arguments it
// was handed (the load-bearing parent directory and resolved hint).
type preflightFileArea struct {
	launchFileArea
	mu    sync.Mutex
	calls []preflightCall
	err   error
}

var _ engine.GatherPreflighter = (*preflightFileArea)(nil)

func (a *preflightFileArea) PreflightGather(_ context.Context, parentDir, repoRel string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, preflightCall{parentDir: parentDir, repoRel: repoRel})
	return a.err
}

func (a *preflightFileArea) preflights() []preflightCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]preflightCall(nil), a.calls...)
}

type fileLaunchFixture struct {
	groups    *memGroupRuns
	instances *mockInstanceRepo
	starter   *fakeGroupTaskStarter
	ws        *dirWorkspaces
	resultsWS *copyGroupWorkspace
	area      *preflightFileArea
	launcher  *engine.GroupLauncher
	parent    *instance.Instance
	parentWS  string
}

// newFileLaunchFixture: a running parent whose working area holds seed.txt and
// a src/ directory; results groups snapshot through resultsWS, file groups
// through area.
func newFileLaunchFixture(t *testing.T) *fileLaunchFixture {
	t.Helper()
	instances := newMockInstanceRepo()
	groups := newMemGroupRuns()
	starter := &fakeGroupTaskStarter{instances: instances}
	ws := newDirWorkspaces(t)
	resultsWS := newCopyGroupWorkspace(t)
	area := &preflightFileArea{launchFileArea: launchFileArea{newCopyGroupWorkspace(t)}}
	launcher := engine.NewGroupLauncher(groups, starter, nil, ws, resultsWS, fixedIdentityResolver{},
		&recordingCanceller{instances: instances}, newMockEventBus())
	launcher.SetFileWorkingArea(area)

	parent := &instance.Instance{ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal}
	require.NoError(t, instances.Create(context.Background(), parent))
	parentWS, err := ws.EnsureInstanceDir(parent.ID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentWS, "seed.txt"), []byte("seed"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(parentWS, "src"), 0o755))

	return &fileLaunchFixture{groups: groups, instances: instances, starter: starter, ws: ws, resultsWS: resultsWS,
		area: area, launcher: launcher, parent: parent, parentWS: parentWS}
}

func fileLaunchConfig(gatherMode, repoPath string) workflow.GroupConfig {
	return workflow.GroupConfig{
		Form:               workflow.GroupFormBranches,
		Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         gatherMode,
		RepoPath:           repoPath,
		ResolverProfile:    "resolver",
	}
}

func (f *fileLaunchFixture) vars(t *testing.T, withWorkspace bool, inputs map[string]any) *variable.VariableStore {
	t.Helper()
	ws := ""
	if withWorkspace {
		ws = f.parentWS
	}
	return repoVars(t, ws, inputs)
}

func (f *fileLaunchFixture) request(cfg workflow.GroupConfig, vars *variable.VariableStore) engine.GroupLaunchRequest {
	c := cfg
	return engine.GroupLaunchRequest{
		Parent:    f.parent,
		Node:      workflow.Node{ID: "g", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: &c}},
		Config:    cfg,
		Ownership: &workflow.GroupScope{NodeID: "g", Config: cfg, Bodies: map[string][]string{"a": {"a1"}, "b": {"b1"}}},
		Vars:      vars,
	}
}

func (f *fileLaunchFixture) assertNothingStarted(t *testing.T) {
	t.Helper()
	run, err := f.groups.GetByParentNode(context.Background(), f.parent.ID, "g")
	require.NoError(t, err)
	assert.Nil(t, run, "nothing is persisted before the preflight passes")
	assert.Empty(t, f.starter.requests(), "no task starts")
	assert.Equal(t, []uuid.UUID{f.parent.ID}, f.ws.ensuredIDs(), "no task directory is prepared")
}

// PROVENANCE: the resolved hint and the parent's {{workspace}} reach the
// preflight, and the baseline is taken by the FILE area (not the results one).
func TestGroupLauncher_FilesMode_PreflightsThenSnapshotsThroughTheFileArea(t *testing.T) {
	f := newFileLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{repo_dir}}"), f.vars(t, true, map[string]any{"repo_dir": "src"})))
	require.NoError(t, err)
	require.NotNil(t, run)

	assert.Equal(t, []preflightCall{{parentDir: f.parentWS, repoRel: "src"}}, f.area.preflights())
	require.Len(t, f.area.snapshotHandles(), 1)
	assert.Equal(t, f.area.snapshotHandles()[0], run.WorkspaceSnapshot)
	assert.Empty(t, f.resultsWS.snapshotHandles(), "a file group never snapshots through the results workspace")
	assert.Empty(t, f.area.releasedHandles(), "the files baseline is kept for the gather")

	stored, err := f.groups.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "src", stored.Config[engine.GroupConfigKeyResolvedRepoPath])
	for _, task := range stored.Tasks {
		got, rerr := os.ReadFile(filepath.Join(f.ws.path(task.ChildInstanceID), "seed.txt"))
		require.NoError(t, rerr, "task %s starts from the parent's files", task.Key)
		assert.Equal(t, "seed", string(got))
	}
}

func TestGroupLauncher_ResultsMode_KeepsTheResultsWorkspace(t *testing.T) {
	f := newFileLaunchFixture(t)
	run, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResults, ""), f.vars(t, true, nil)))
	require.NoError(t, err)
	require.NotNil(t, run)

	assert.Len(t, f.resultsWS.snapshotHandles(), 1)
	assert.Len(t, f.resultsWS.releasedHandles(), 1, "results snapshots are released once launching ends")
	assert.Empty(t, f.area.snapshotHandles(), "results-only groups never need the file area (or git)")
	assert.Empty(t, f.area.preflights())
	_, present := run.Config[engine.GroupConfigKeyResolvedRepoPath]
	assert.False(t, present)
}

func TestGroupLauncher_FilesMode_PreflightFailureIsPreLaunch(t *testing.T) {
	f := newFileLaunchFixture(t)
	errNoGit := errors.New("git executable is not available on this host")
	f.area.err = errNoGit

	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, errNoGit)
	assert.Empty(t, f.area.snapshotHandles(), "the preflight runs before the snapshot")
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_InvalidRepoPathIsRejectedBeforeThePreflight(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{up}}/x"), f.vars(t, true, map[string]any{"up": ".."})))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, engine.ErrGroupRepoPathInvalid)
	assert.Empty(t, f.area.preflights())
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_UnresolvedRepoPathTemplateIsPreLaunch(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(),
		f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, "{{nope}}"), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.ErrorIs(t, err, variable.ErrUnresolvedReference)
	f.assertNothingStarted(t)
}

// PROVENANCE: without the composition-root setter a file group is refused —
// it is never silently snapshotted through the results workspace.
func TestGroupLauncher_FilesMode_WithoutAFileAreaRefuses(t *testing.T) {
	f := newFileLaunchFixture(t)
	bare := engine.NewGroupLauncher(f.groups, f.starter, nil, f.ws, f.resultsWS, fixedIdentityResolver{},
		&recordingCanceller{instances: f.instances}, newMockEventBus())

	_, err := bare.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "file-gathering working area")
	assert.Empty(t, f.resultsWS.snapshotHandles())
	f.assertNothingStarted(t)
}

func TestGroupLauncher_FilesMode_WithoutAParentWorkspaceRefuses(t *testing.T) {
	f := newFileLaunchFixture(t)
	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, false, nil)))
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	assert.Contains(t, err.Error(), "{{workspace}}")
	assert.Empty(t, f.area.preflights())
}

func TestGroupLauncher_FilesMode_ForkFailureReleasesTheFileSnapshot(t *testing.T) {
	f := newFileLaunchFixture(t)
	f.area.forkErr = errors.New("disk full")

	_, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.ErrorIs(t, err, engine.ErrGroupPreLaunch)
	require.Len(t, f.area.snapshotHandles(), 1)
	assert.Equal(t, f.area.snapshotHandles(), f.area.releasedHandles(), "the unused baseline is released by the area that took it")
	assert.Empty(t, f.resultsWS.releasedHandles())
}

func TestGroupLauncher_FilesMode_AreaWithoutPreflightStillLaunches(t *testing.T) {
	f := newFileLaunchFixture(t)
	plain := launchFileArea{newCopyGroupWorkspace(t)}
	f.launcher.SetFileWorkingArea(plain)

	run, err := f.launcher.Launch(context.Background(), f.request(fileLaunchConfig(workflow.GroupGatherResultsAndFiles, ""), f.vars(t, true, nil)))
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Len(t, plain.snapshotHandles(), 1)
	_, present := run.Config[engine.GroupConfigKeyResolvedRepoPath]
	assert.False(t, present, "no repoPath declared, nothing recorded")
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestGroupLauncher_FilesMode|TestGroupLauncher_ResultsMode_KeepsTheResultsWorkspace' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_launch_files_test.go:...: launcher.SetFileWorkingArea undefined (type *engine.GroupLauncher has no field or method SetFileWorkingArea)
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement the files-mode branch. Create `domain/engine/group_launch_files.go`:

```go
package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// SetFileWorkingArea wires the working area that snapshots, forks, finalizes
// and gathers results_and_files groups (the git area, P3-T2/T3). Results-only
// groups keep the launcher's groupWS, so they never depend on git. Composition
// time only (before FreezeExecutors); a nil area refuses every file group.
func (l *GroupLauncher) SetFileWorkingArea(area WorkingArea) {
	l.fileArea = area
}

// launchWorkspace picks the snapshot/fork capability for this launch and, for
// file gathering, runs every check that must happen BEFORE a task exists: a
// file area is wired, the parent has a working area, the optional repoPath
// resolves inside it, and the area's own preflight (git availability, the
// declared repository) passes. It returns the resolved hint ("" = none).
func (l *GroupLauncher) launchWorkspace(ctx context.Context, req GroupLaunchRequest, parentWS string) (GroupWorkspace, string, error) {
	if req.Config.GatherMode != workflow.GroupGatherResultsAndFiles {
		return l.groupWS, "", nil
	}
	if l.fileArea == nil {
		return nil, "", fmt.Errorf("gatherMode %q needs a file-gathering working area, and none is wired on this server; use gatherMode %q",
			workflow.GroupGatherResultsAndFiles, workflow.GroupGatherResults)
	}
	if strings.TrimSpace(parentWS) == "" {
		return nil, "", fmt.Errorf("gatherMode %q gathers files into the parent run's working area ({{workspace}}), and this run has none",
			workflow.GroupGatherResultsAndFiles)
	}
	repoRel, err := ResolveGroupRepoPath(req.Config.RepoPath, req.Vars, parentWS)
	if err != nil {
		return nil, "", err
	}
	if p, ok := l.fileArea.(GatherPreflighter); ok {
		if err := p.PreflightGather(ctx, parentWS, repoRel); err != nil {
			return nil, "", fmt.Errorf("file-gathering preflight: %w", err)
		}
	}
	return l.fileArea, repoRel, nil
}
```

- [x] Step 4: Edit `domain/engine/group_launcher.go` (P2-T8). Replace the whole `type GroupLauncher struct { … }` block with:

```go
// GroupLauncher launches a parallel group exactly once.
type GroupLauncher struct {
	groups     instance.GroupRunRepository
	starter    GroupTaskStarter
	contracts  ChildWorkflowReader
	workspaces WorkspaceManager
	groupWS    GroupWorkspace
	// fileArea snapshots/forks results_and_files groups (SetFileWorkingArea,
	// P3-T10); results-only groups use groupWS.
	fileArea   WorkingArea
	identities IdentityResolver
	canceller  RunCanceller
	bus        EventBus
	now        func() time.Time
}
```

Replace the `type launchPrep struct { … }` block and the whole `func (p *launchPrep) cleanup(ctx context.Context) { … }` with:

```go
// launchPrep tracks pre-launch side effects so a refusal leaves nothing behind.
// ws is the capability that took the snapshot, so the snapshot is released by
// the same area (results workspace or file working area).
type launchPrep struct {
	l        *GroupLauncher
	ws       GroupWorkspace
	snapshot string
	dirs     []uuid.UUID
}

func (p *launchPrep) cleanup(ctx context.Context) {
	for _, id := range p.dirs {
		if err := p.l.workspaces.RemoveInstanceDir(id); err != nil {
			slog.Warn("parallel_group: removing a prepared task area failed", "taskInstanceID", id, "error", err)
		}
	}
	if p.snapshot != "" && p.ws != nil {
		if err := p.ws.ReleaseSnapshot(ctx, p.snapshot); err != nil {
			slog.Warn("parallel_group: releasing an unused snapshot failed", "snapshot", p.snapshot, "error", err)
		}
	}
}
```

Replace the whole `func (l *GroupLauncher) Launch(ctx context.Context, req GroupLaunchRequest) (*instance.GroupRun, error) { … }` (keep its doc comment above it) with:

```go
func (l *GroupLauncher) Launch(ctx context.Context, req GroupLaunchRequest) (*instance.GroupRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkLaunchRequest(req); err != nil {
		return nil, preLaunchError(err)
	}
	parent := req.Parent
	nodeID := req.Node.ID

	// Reconstruct the PARENT OWNER's identity, as SpawnWorkflowExecutor does. No
	// fallback: a system identity would change what the tasks may see and reach.
	ownerIdentity, err := l.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return nil, preLaunchError(fmt.Errorf("resolve the parent owner's identity: %w", err))
	}
	ownerCtx := identity.WithIdentity(ctx, ownerIdentity)

	start := captureStartVariables(req.Vars)
	parentWS, _ := WorkDirFromVars(req.Vars)
	// File gathering snapshots through the file working area, after its
	// preflight (repoPath containment, git availability); results-only groups
	// keep groupWS (P3-T10).
	ws, repoRel, err := l.launchWorkspace(ctx, req, parentWS)
	if err != nil {
		return nil, preLaunchError(err)
	}
	if parentWS != "" && (l.workspaces == nil || ws == nil) {
		return nil, preLaunchError(errors.New("the parent run has a working area but no workspace snapshot/fork " +
			"capability is wired; refusing to start tasks that would share the parent's files"))
	}

	runID := uuid.New()
	prep := &launchPrep{l: l, ws: ws}
	if parentWS != "" {
		handle, err := ws.SnapshotParent(ctx, runID, parentWS)
		if err != nil {
			return nil, preLaunchError(fmt.Errorf("snapshot the parent's working area: %w", err))
		}
		prep.snapshot = handle
	}

	tasks := make([]launchTask, 0, len(req.Config.Tasks))
	for i, spec := range req.Config.Tasks {
		lt, err := l.prepareTask(ownerCtx, req, runID, i, spec, start, parentWS, prep)
		if err != nil {
			prep.cleanup(ctx)
			return nil, preLaunchError(err)
		}
		tasks = append(tasks, lt)
	}
	if prep.snapshot != "" {
		for _, lt := range tasks {
			if err := ws.ForkTask(ctx, prep.snapshot, lt.dir); err != nil {
				prep.cleanup(ctx)
				return nil, preLaunchError(fmt.Errorf("task %q: fork the working area: %w", lt.row.Key, err))
			}
		}
	}

	run, err := newGroupRun(runID, req, start, prep.snapshot, tasks, l.now())
	if err != nil {
		prep.cleanup(ctx)
		return nil, preLaunchError(err)
	}
	if repoRel != "" {
		run.Config[GroupConfigKeyResolvedRepoPath] = repoRel
	}
	if err := l.groups.CreateWithTasks(ctx, run); err != nil {
		prep.cleanup(ctx)
		if errors.Is(err, instance.ErrGroupRunExists) {
			return l.existingRun(ctx, parent.ID, nodeID, err)
		}
		return nil, preLaunchError(fmt.Errorf("persist the group run and its roster: %w", err))
	}
	return l.launchAll(ctx, ownerCtx, run, tasks, string(parent.Priority)), nil
}
```

(`newGroupRun` always returns a non-nil `Config` map — it unmarshals the JSON of `req.Config` — so the assignment is safe; `releaseResultsSnapshot` is unchanged and still only releases results-mode snapshots through `groupWS`.)

- [x] Step 5: Run the new tests and every P2-T8 launcher test.

```bash
go test ./domain/engine/ -run 'TestGroupLauncher|TestNewGroupLauncher|TestComposeTaskVariables' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected: every `--- PASS`, including the nine new tests (`TestGroupLauncher_FilesMode_PreflightsThenSnapshotsThroughTheFileArea`, `…_ResultsMode_KeepsTheResultsWorkspace`, `…_FilesMode_PreflightFailureIsPreLaunch`, `…_InvalidRepoPathIsRejectedBeforeThePreflight`, `…_UnresolvedRepoPathTemplateIsPreLaunch`, `…_WithoutAFileAreaRefuses`, `…_WithoutAParentWorkspaceRefuses`, `…_ForkFailureReleasesTheFileSnapshot`, `…_AreaWithoutPreflightStillLaunches`) and the unchanged P2-T8 tests; last line `ok  	github.com/arbitron/arbitron/domain/engine`.

- [x] Step 6: Whole package, gofmt, vet, commit.

10.3: Missing-setter red → focused green. Updated two older first-entry file-mode fixtures to supply the now-required dedicated area and parent workspace; whole engine and launcher race tests pass, formatting/vet clean. Ordered review: (1) only the specified launch/preflight capability selection and resolved config storage; (2) nonempty seed bytes reach each forked task directory and persisted config carries the parent-resolved hint, absent wiring explicitly refuses; (3) retained neighboring launch cleanup/error patterns. Rechecked tests after review. Production root remains P3-T11's responsibility.

```bash
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_launch_files.go domain/engine/group_launcher.go domain/engine/group_launch_files_test.go
go vet ./domain/engine/
git add domain/engine/group_launch_files.go domain/engine/group_launcher.go domain/engine/group_launch_files_test.go && git commit -m "feat(engine): files-mode launch preflight through a dedicated file working area" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok  	github.com/arbitron/arbitron/domain/engine`; `gofmt -l`/`go vet` print nothing.

### Task 10.4: Executor file mode — single-writer gate, rollback-failure suspension, cancelled-parent guard
**Files:**
- Create: `domain/engine/executor_group_files.go`
- Modify: `domain/engine/executor_group.go` (created by P2-T11): the `type ParallelGroupExecutor struct { … }` block (add one field) and the whole `func (x *ParallelGroupExecutor) resolve(…)` (its gather half moves to `continueFileGather`; the done and not-files branches are unchanged). Nothing else in the file changes, so P2-T11's `TestParallelGroupExecutor_FilesModeReEntryUsesTheGatherer` keeps passing (its seeded run is re-read under the gate and its fake gatherer still decides).
- Test: `domain/engine/executor_group_files_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/executor_group_files_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileExecFixture runs the REAL ParallelGroupExecutor + REAL FileGatherer over
// P3-T6's gatherFixture (a decided results_and_files run at node g1 whose
// tasks finished in reverse author order) and an instance repo holding the
// parent at g1. The SINK is fakeGatherArea.parentFiles(gatherParentDir).
type fileExecFixture struct {
	*gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	qm        *mockQueueManager
	x         *engine.ParallelGroupExecutor
	node      workflow.Node
}

func newFileExecFixture(t *testing.T, rule, outcome, winner string, keys ...string) *fileExecFixture {
	t.Helper()
	g := newGatherFixture(t, rule, outcome, winner, keys...)
	instances := newMockInstanceRepo()
	nodeID := "g1"
	parent := *g.parent
	parent.CurrentNodeID = &nodeID
	parent.Variables = map[string]any{"workspace": gatherParentDir}
	require.NoError(t, instances.Create(context.Background(), &parent))

	nodeExecs := newMockNodeExecutionRepo()
	qm := newMockQueueManager()
	canceller := &recordingCanceller{instances: instances}
	launcher := engine.NewGroupLauncher(g.groups, &fakeGroupTaskStarter{instances: instances}, nil, nil, nil,
		fixedIdentityResolver{}, canceller, g.bus)
	coord := engine.NewGroupCoordinator(g.groups, instances, &memWaker{instances: instances}, canceller,
		fixedIdentityResolver{}, g.bus, qm)
	x := engine.NewParallelGroupExecutor(instances, nodeExecs, g.groups, launcher, coord, &nodeClaims{instances: instances}, qm)
	x.SetGatherer(g.gatherer)

	behavior := workflow.GroupUnfinishedWaitForRemaining
	if rule == workflow.GroupRuleFirstSuccess {
		behavior = workflow.GroupUnfinishedFinishIndependently
	}
	tasks := make([]workflow.GroupTask, 0, len(keys))
	for _, k := range keys {
		tasks = append(tasks, workflow.GroupTask{Key: k, Entry: k + "-entry"})
	}
	cfg := workflow.GroupConfig{
		Form: workflow.GroupFormBranches, Tasks: tasks, CompletionRule: rule, UnfinishedBehavior: behavior,
		GatherMode: workflow.GroupGatherResultsAndFiles, ResolverProfile: "resolver",
	}
	return &fileExecFixture{gatherFixture: g, instances: instances, nodeExecs: nodeExecs, qm: qm, x: x, node: gxGroupNode("g1", false, cfg)}
}

func (f *fileExecFixture) execute(t *testing.T) (*engine.NodeResult, error) {
	t.Helper()
	ctx := engine.ContextWithInstanceID(context.Background(), f.parent.ID)
	return f.x.Execute(ctx, f.node, variable.NewStore(), "wf")
}

func (f *fileExecFixture) setParentState(s instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parent.ID].State = s
}

func (f *fileExecFixture) parentState() instance.State {
	return f.instances.get(f.parent.ID).State
}

func TestParallelGroupExecutor_FileGather_AllSucceedAppliesAndRoutesSuccess(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")

	res, err := f.execute(t)
	require.NoError(t, err)
	require.False(t, res.IsWait)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	gather, ok := res.Output["gather"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "applied", gather["state"])
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))
	assert.Equal(t, []string{taskRef("a"), taskRef("b")}, f.area.mergeOrder(), "author order, not completion order")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.True(t, run.ContinuationDelivered)
}

func TestParallelGroupExecutor_FileGather_MergeFailureRollsBackAndRoutesFailureKeepingTheDecision(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort)
	assert.Equal(t, "success", res.Output["outcome"], "the frozen task decision is never rewritten")
	gather := res.Output["gather"].(map[string]any)
	assert.Equal(t, "rolled_back", gather["state"])
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "no partial work reaches the parent")
	run := f.groups.snapshot(f.runID)
	require.NotNil(t, run.Decision)
	assert.Equal(t, workflow.GroupPortSuccess, run.Decision.Outcome)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
}

func TestParallelGroupExecutor_FileGather_ConflictSuspendsWithoutHoldingASlot(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()

	res, err := f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "an open resolution session suspends the parent")
	assert.Equal(t, instance.StateWaiting, f.parentState())
	assert.GreaterOrEqual(t, f.qm.notifyCount(), 1, "the waiting parent releases its queue slot")
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)
	require.Len(t, f.starter.calls(), 1)

	f.setParentState(instance.StateRunning)
	res, err = f.execute(t)
	require.NoError(t, err)
	assert.True(t, res.IsWait, "re-entry while the session is open keeps waiting")
	assert.Len(t, f.starter.calls(), 1, "no second session for the same attempt")
}

func TestParallelGroupExecutor_FileGather_ReEntryAfterResolutionResumesWithoutReapplying(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	res, err := f.execute(t)
	require.NoError(t, err)
	require.True(t, res.IsWait)

	f.area.resolveStaged("shared.txt", "merged")
	f.endOpenAttempts("")
	f.setParentState(instance.StateRunning)

	res, err = f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortSuccess, res.OutputPort)
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "merged", "a.txt": "from-a", "b.txt": "from-b"},
		f.area.parentFiles(gatherParentDir))
	assert.Equal(t, 1, f.area.timesApplied(taskRef("a")), "the merge done before the session is never re-applied")
	assert.Equal(t, engine.ResolutionOutcomeResolved, f.report().Attempts[0].Outcome)
}

func TestParallelGroupExecutor_FileGather_RollbackFailureSuspendsUntilRepaired(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")
	f.area.failRollback = errors.New("read-only file system")

	res, err := f.execute(t)
	require.NoError(t, err, "a failed rollback is not a node error: an error behaviour could otherwise continue past it")
	assert.True(t, res.IsWait)
	assert.Equal(t, instance.StateWaiting, f.parentState())
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "continuation stays blocked")
	require.NotNil(t, run.Gather)
	assert.Equal(t, "rollback_failed", run.Gather.RollbackStatus)

	f.area.failRollback = nil
	f.setParentState(instance.StateRunning)
	res, err = f.execute(t)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, res.OutputPort)
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
	assert.Equal(t, "rolled_back", f.groups.snapshot(f.runID).Gather.RollbackStatus)
}

func TestParallelGroupExecutor_FileGather_CancelledParentIsNeverGathered(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.setParentState(instance.StateCancelled)

	_, err := f.execute(t)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "Engine.handleNodeError halts without overwriting the cancelled state")
	assert.Empty(t, f.area.begins(), "nothing is staged for a cancelled parent; the reconciler settles it")
	assert.Equal(t, instance.GroupRunDecided, f.groups.snapshot(f.runID).State)
}

func TestParallelGroupExecutor_FileGather_ReloadFailureIsANodeError(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.failOnce("Get", errors.New("database is locked"))

	_, err := f.execute(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload group run")
	assert.Empty(t, f.area.begins())
}

// blockingStarter holds StartResolution until released, so a test can observe
// the executor in the middle of a Gather call.
type blockingStarter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

var _ engine.GroupResolutionStarter = (*blockingStarter)(nil)

func (s *blockingStarter) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	if ref.GroupRunID == uuid.Nil { return errors.New("blocking starter: missing run") }
	return nil
}

func (s *blockingStarter) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	if ref.GroupRunID == uuid.Nil || reason != "completed" {
		return errors.New("blocking starter: invalid session closure")
	}
	return nil
}

func (s *blockingStarter) StartResolution(_ context.Context, run *instance.GroupRun, parent *instance.Instance, req engine.ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return &instance.ResolutionSessionRef{GroupRunID: run.ID, MergeIndex: req.MergeIndex, Attempt: req.Attempt, InstanceID: parent.ID, NodeID: run.NodeID}, nil
}

// PROVENANCE: the gate the reconciler probes is the one the executor holds for
// the whole Gather call.
func TestParallelGroupExecutor_FileGather_TryLockGatherExcludesTheReconcilerWhileGathering(t *testing.T) {
	f := newFileExecFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	blocker := &blockingStarter{entered: make(chan struct{}), release: make(chan struct{})}
	f.x.SetGatherer(engine.NewGroupGatherer(f.groups, f.area, blocker, f.bus))

	done := make(chan error, 1)
	go func() {
		_, err := f.execute(t)
		done <- err
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never reached the resolution session")
	}

	_, ok := f.x.TryLockGather(f.runID)
	assert.False(t, ok, "a run whose gather is being written cannot be taken by the reconciler")
	unlockOther, ok := f.x.TryLockGather(uuid.New())
	require.True(t, ok, "other runs are not blocked")
	unlockOther()

	close(blocker.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the executor did not finish after the session opened")
	}
	unlock, ok := f.x.TryLockGather(f.runID)
	require.True(t, ok, "the gate is released when Gather returns")
	unlock()
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestParallelGroupExecutor_FileGather' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/executor_group_files_test.go:...: f.x.TryLockGather undefined (type *engine.ParallelGroupExecutor has no field or method TryLockGather)
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/executor_group_files.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// gatherGate serializes this process's writers of one run's file gather: the
// parent's executor (blocking lock, for the whole Gather call) and the
// gather reconciler (tryLock: a busy gate means the parent is writing right
// now and the reconciler leaves it alone). Entries are reference-counted, so
// the map only holds runs somebody is using. The zero value is ready to use.
type gatherGate struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*gatherGateEntry
}

type gatherGateEntry struct {
	mu   sync.Mutex
	refs int
}

func (g *gatherGate) acquire(id uuid.UUID) *gatherGateEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runs == nil {
		g.runs = make(map[uuid.UUID]*gatherGateEntry)
	}
	e := g.runs[id]
	if e == nil {
		e = &gatherGateEntry{}
		g.runs[id] = e
	}
	e.refs++
	return e
}

func (g *gatherGate) release(id uuid.UUID, e *gatherGateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e.refs--
	if e.refs == 0 && g.runs[id] == e {
		delete(g.runs, id)
	}
}

func (g *gatherGate) lock(id uuid.UUID) func() {
	e := g.acquire(id)
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		g.release(id, e)
	}
}

func (g *gatherGate) tryLock(id uuid.UUID) (func(), bool) {
	e := g.acquire(id)
	if !e.mu.TryLock() {
		g.release(id, e)
		return nil, false
	}
	return func() {
		e.mu.Unlock()
		g.release(id, e)
	}, true
}

var _ GatherLocker = (*ParallelGroupExecutor)(nil)

// TryLockGather implements GatherLocker for the group gather reconciler: ok is
// false while this executor is inside a Gather call for runID.
func (x *ParallelGroupExecutor) TryLockGather(runID uuid.UUID) (func(), bool) {
	return x.gate.tryLock(runID)
}

// continueFileGather advances a results_and_files run that is ready to gather,
// in the parent's own goroutine, as the single writer of the parent's working
// area. The run is re-read under the gate (the reconciler may have settled it
// meanwhile). A terminal (cancelled) parent is never gathered for: the error
// wraps context.Canceled so Engine.handleNodeError halts without touching the
// parent's state, and the reconciler rolls the gather back. A failed rollback
// keeps the parent suspended (nil, nil => IsWait) instead of failing the node,
// so no error behaviour can continue past a half-restored area; the
// reconciler wakes it to retry. A finished step is routed exactly once.
func (x *ParallelGroupExecutor) continueFileGather(ctx context.Context, node workflow.Node, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	unlock := x.gate.lock(run.ID)
	defer unlock()

	fresh, err := x.groups.Get(ctx, run.ID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: reload group run %s before gathering: %w", node.ID, run.ID, err)
	}
	if fresh.State == instance.GroupRunDone {
		return x.deliver(ctx, node, fresh)
	}
	if !gatherableGroupState(fresh.State) {
		return nil, nil
	}
	parent, err := x.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("parallel_group %q: load parent instance: %w", node.ID, err)
	}
	if parent.State.IsTerminal() {
		return nil, fmt.Errorf("parallel_group %q: parent %s is %s, so its file gather is left to the group gather reconciler: %w",
			node.ID, instanceID, parent.State, context.Canceled)
	}
	step, err := x.gatherer.Gather(ctx, fresh, parent)
	if err != nil {
		if errors.Is(err, ErrGroupGatherRollbackFailed) {
			slog.Error("parallel_group: the parent's working area could not be restored; the parent stays suspended until a retried rollback succeeds",
				"nodeID", node.ID, "groupRunID", fresh.ID, "parentID", instanceID, "error", err)
			return nil, nil
		}
		return nil, fmt.Errorf("parallel_group %q: gather: %w", node.ID, err)
	}
	if step == nil || !step.Done {
		return nil, nil
	}
	if step.Port != workflow.GroupPortSuccess && step.Port != workflow.GroupPortFailure {
		return nil, fmt.Errorf("parallel_group %q: the gatherer finished with port %q (want %q or %q)",
			node.ID, step.Port, workflow.GroupPortSuccess, workflow.GroupPortFailure)
	}
	if err := x.groups.MarkContinuationDelivered(ctx, fresh.ID); err != nil {
		slog.Warn("parallel_group: recording delivery failed; a replay re-delivers the same output",
			"nodeID", node.ID, "groupRunID", fresh.ID, "error", err)
	}
	return &NodeResult{OutputPort: step.Port, Output: variable.DeepCopyMap(step.Output)}, nil
}
```

- [x] Step 4: Edit `domain/engine/executor_group.go` (P2-T11). Replace the `type ParallelGroupExecutor struct { … }` block with:

```go
type ParallelGroupExecutor struct {
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	groups    instance.GroupRunRepository
	launcher  *GroupLauncher
	coord     *GroupCoordinator
	claims    instance.SpawnWaitRepository
	queueMgr  QueueManager
	gatherer  GroupGatherer
	// gate serializes the writers of each run's file gather (P3-T10).
	gate gatherGate
}
```

Replace the whole `func (x *ParallelGroupExecutor) resolve(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) { … }` (keep its doc comment) with:

```go
func (x *ParallelGroupExecutor) resolve(ctx context.Context, node workflow.Node, filesMode bool, run *instance.GroupRun, instanceID uuid.UUID) (*NodeResult, error) {
	if run.State == instance.GroupRunDone {
		return x.deliver(ctx, node, run)
	}
	if !filesMode || !gatherableGroupState(run.State) {
		return nil, nil
	}
	if x.gatherer == nil {
		return nil, fmt.Errorf("parallel_group %q: group run %s is ready to gather files: %w", node.ID, run.ID, ErrGroupExecutionUnsupported)
	}
	return x.continueFileGather(ctx, node, run, instanceID)
}
```

(`slog` and `variable` stay imported by `executor_group.go`: `deliver`, `reenter`, `recoverLostWake` and `reread` still use them.)

- [x] Step 5: Run the new tests and every P2-T11 executor test.

```bash
go test ./domain/engine/ -run 'TestParallelGroupExecutor|TestNewParallelGroupExecutor' -race -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected: every `--- PASS`, including `TestParallelGroupExecutor_FileGather_AllSucceedAppliesAndRoutesSuccess`, `…_MergeFailureRollsBackAndRoutesFailureKeepingTheDecision`, `…_ConflictSuspendsWithoutHoldingASlot`, `…_ReEntryAfterResolutionResumesWithoutReapplying`, `…_RollbackFailureSuspendsUntilRepaired`, `…_CancelledParentIsNeverGathered`, `…_ReloadFailureIsANodeError`, `…_TryLockGatherExcludesTheReconcilerWhileGathering` and P2-T11's `TestParallelGroupExecutor_FilesModeReEntryUsesTheGatherer`; last line `ok  	github.com/arbitron/arbitron/domain/engine`.

- [x] Step 6: Whole package, gofmt, vet, commit.

10.4: Corrected the supplied test's `NodeResult.Output` type assertion before confirming the missing-gate red. Kept P3-T6's amended contract: final `outcome=failure`, immutable `taskOutcome=success`; the old sample assertion conflated them. Focused executor race tests, whole engine, formatting/vet pass. Ordered review: (1) exactly the gate/terminal guard/rollback suspension slice; (2) real executor + gatherer assert parent bytes, routed output and persisted delivery, and observe the same lock the reconciler uses; (3) checked existing wait/error behavior and gate refcount cleanup, reran focused race tests green. P3-T11 still owns gatherer construction at the root.

```bash
go test ./domain/engine/ -count=1
gofmt -l domain/engine/executor_group_files.go domain/engine/executor_group.go domain/engine/executor_group_files_test.go
go vet ./domain/engine/
git add domain/engine/executor_group_files.go domain/engine/executor_group.go domain/engine/executor_group_files_test.go && git commit -m "feat(engine): single-writer file gather in the parallel_group executor; suspend on failed rollback" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok`; `gofmt -l`/`go vet` print nothing.

### Task 10.5: GroupResourcePruner — release refs and worktrees only after writers stop and pins release
**Files:**
- Create: `domain/engine/group_prune.go`
- Test: `domain/engine/group_prune_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/group_prune_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInventory lists the snapshots it was given, minus the ones the area has
// released since — so HasSnapshot turns false exactly when ReleaseSnapshot ran.
type fakeInventory struct {
	mu      sync.Mutex
	area    *fakeGatherArea
	handles map[string]time.Time
	err     error
}

var _ engine.SnapshotInventory = (*fakeInventory)(nil)

func newFakeInventory(area *fakeGatherArea) *fakeInventory {
	return &fakeInventory{area: area, handles: map[string]time.Time{}}
}

func (i *fakeInventory) add(handle string, mod time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.handles[handle] = mod
}

func (i *fakeInventory) released(handle string) bool {
	i.area.mu.Lock()
	defer i.area.mu.Unlock()
	for _, h := range i.area.releasedSnapshots {
		if h == handle {
			return true
		}
	}
	return false
}

func (i *fakeInventory) ListSnapshots(context.Context) ([]engine.SnapshotInfo, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.err != nil {
		return nil, i.err
	}
	var out []engine.SnapshotInfo
	for h, mod := range i.handles {
		if !i.released(h) {
			out = append(out, engine.SnapshotInfo{Handle: h, ModTime: mod})
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Handle < out[b].Handle })
	return out, nil
}

func (i *fakeInventory) HasSnapshot(_ context.Context, handle string) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, ok := i.handles[handle]
	return ok && !i.released(handle), nil
}

// fakeChildLister answers ListChildren per parent id.
type fakeChildLister struct {
	mu       sync.Mutex
	children map[uuid.UUID][]instance.ChildRef
	err      error
}

var _ engine.ChildLister = (*fakeChildLister)(nil)

func (c *fakeChildLister) set(parent uuid.UUID, kids ...instance.ChildRef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.children == nil {
		c.children = map[uuid.UUID][]instance.ChildRef{}
	}
	c.children[parent] = kids
}

func (c *fakeChildLister) ListChildren(_ context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	return append([]instance.ChildRef{}, c.children[parentID]...), nil
}

type pruneFixture struct {
	groups    *memGroupRuns
	instances *mockInstanceRepo
	children  *fakeChildLister
	ws        *dirWorkspaces
	area      *fakeGatherArea
	inventory *fakeInventory
	pruner    *engine.GroupResourcePruner
}

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	f := &pruneFixture{
		groups: newMemGroupRuns(), instances: newMockInstanceRepo(), children: &fakeChildLister{},
		ws: newDirWorkspaces(t), area: newFakeGatherArea(),
	}
	f.inventory = newFakeInventory(f.area)
	f.pruner = engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, f.area, f.inventory)
	return f
}

// seedRun persists a results_and_files run in runState whose gather report is
// in gatherState, with one launched task per child state; every child row
// exists, and every child has a real workspace holding "<key>.txt".
func (f *pruneFixture) seedRun(t *testing.T, runState instance.GroupRunState, gatherState string, states ...instance.State) *instance.GroupRun {
	t.Helper()
	ctx := context.Background()
	id, gatherID := uuid.New(), uuid.New()
	run := &instance.GroupRun{
		ID: id, ParentInstanceID: uuid.New(), NodeID: "g1", Attempt: 1,
		Form: workflow.GroupFormBranches, CompletionRule: workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResultsAndFiles,
		WorkspaceSnapshot: id.String(), State: runState, ActionsApplied: true,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: gatherState,
			Handle: map[string]any{
				"snapshotHandle": id.String(), "gatherId": gatherID.String(), "stagingDir": "/staging/" + gatherID.String(),
				"rollbackRef": "refs/arbitron/rollback/" + gatherID.String(), "parentDir": "/ws/parent",
			},
			Tasks: []instance.GatherTaskReport{}, Attempts: []instance.ResolutionAttempt{},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	for i, st := range states {
		child := uuid.New()
		key := string(rune('a' + i))
		run.Tasks = append(run.Tasks, instance.GroupTask{
			GroupRunID: id, Position: i, Key: key, Kind: instance.GroupTaskKindBranch, Origin: key + "1",
			ChildInstanceID: child, LaunchState: instance.GroupTaskLaunched, LiveStatus: instance.GroupTaskStatusFromState(st),
		})
		require.NoError(t, f.instances.Create(ctx, &instance.Instance{ID: child, State: st}))
		dir, err := f.ws.EnsureInstanceDir(child)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, key+".txt"), []byte(key), 0o644))
	}
	f.groups.put(run)
	f.inventory.add(run.WorkspaceSnapshot, time.Now())
	return f.groups.snapshot(id)
}

func (f *pruneFixture) setState(id uuid.UUID, st instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[id].State = st
}

func (f *pruneFixture) released() []string {
	f.area.mu.Lock()
	defer f.area.mu.Unlock()
	return append([]string(nil), f.area.releasedSnapshots...)
}

func (f *pruneFixture) releasedRefs() []string {
	f.area.mu.Lock()
	defer f.area.mu.Unlock()
	return append([]string(nil), f.area.releasedRefs...)
}

func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func rollbackPin(run *instance.GroupRun) string {
	return run.WorkspaceSnapshot + "|" + run.Gather.Handle["rollbackRef"].(string)
}

func TestGroupResourcePruner_AppliedGatherReleasesSnapshotAndCompletedTaskWorkspaces(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted, instance.StateCompleted)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.Equal(t, []string{run.WorkspaceSnapshot}, f.released(), "the snapshot handle comes from the run")
	assert.Contains(t, f.releasedRefs(), rollbackPin(run), "the gather's rollback pin (and staging area) goes first")
	for _, task := range run.Tasks {
		assert.False(t, dirExists(f.ws.path(task.ChildInstanceID)), "SINK: completed task %s workspace removed", task.Key)
	}
}

func TestGroupResourcePruner_WaitsForAnIndependentTaskToStop(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted, instance.StateRunning)
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a task still writing pins the run's resources")
	assert.Empty(t, f.released())
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
	assert.Contains(t, f.releasedRefs(), rollbackPin(run), "the finished gather's own pin is released already")

	f.setState(run.Tasks[1].ChildInstanceID, instance.StateCompleted)
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.False(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

func TestGroupResourcePruner_WaitsForActiveDescendantsThroughTerminalIntermediates(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	task := run.Tasks[0].ChildInstanceID
	mid, leaf := uuid.New(), uuid.New()
	f.children.set(task, instance.ChildRef{ID: mid, State: instance.StateCompleted})
	f.children.set(mid, instance.ChildRef{ID: leaf, State: instance.StateRunning})
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a running grandchild below a completed child still pins the task workspace")
	assert.True(t, dirExists(f.ws.path(task)))

	f.children.set(mid, instance.ChildRef{ID: leaf, State: instance.StateCompleted})
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_WaitsForANestedGroupUnderATask(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	nested := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: run.Tasks[0].ChildInstanceID, NodeID: "inner", Attempt: 1,
		GatherMode: workflow.GroupGatherResultsAndFiles, State: instance.GroupRunGathering,
	}
	f.groups.put(nested)
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released, "a dependent gather into the task's area pins it")

	f.groups.update(nested.ID, func(r *instance.GroupRun) { r.State = instance.GroupRunDone })
	released, err = f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_NeverReleasesARunThatIsNotDone(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunGathering, "merging", instance.StateCompleted)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
	assert.Empty(t, f.releasedRefs(), "gather and recovery still need every ref")
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

// Failed runs retain debugging content, consistent with
// Engine.warnWorkspaceRetained: nothing was applied, so the tasks' files stay.
func TestGroupResourcePruner_UnappliedGatherRetainsCompletedTaskWorkspaces(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "rolled_back", instance.StateCompleted, instance.StateFailed)

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released, "the backing snapshot itself is still released")
	for _, task := range run.Tasks {
		got, rerr := os.ReadFile(filepath.Join(f.ws.path(task.ChildInstanceID), task.Key+".txt"))
		require.NoError(t, rerr, "task %s workspace is retained for debugging", task.Key)
		assert.Equal(t, task.Key, string(got))
	}
}

func TestGroupResourcePruner_ReleaseErrorIsReturnedAndRetried(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.area.failReleaseSnapshot = errors.New("device busy")
	ctx := context.Background()

	_, err := f.pruner.PruneRun(ctx, run.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device busy")

	f.area.failReleaseSnapshot = nil
	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.True(t, released)
}

func TestGroupResourcePruner_AlreadyReleasedIsANoOp(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "rolled_back", instance.StateCompleted)
	f.inventory.mu.Lock()
	delete(f.inventory.handles, run.WorkspaceSnapshot)
	f.inventory.mu.Unlock()

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.NoError(t, err)
	assert.True(t, released)
	assert.Empty(t, f.released(), "an absent snapshot is the durable 'already pruned' marker")
	assert.Empty(t, f.releasedRefs())
}

func TestGroupResourcePruner_ResultsModeAndUnknownRunsAreNotItsConcern(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.groups.update(run.ID, func(r *instance.GroupRun) { r.GatherMode = workflow.GroupGatherResults })
	ctx := context.Background()

	released, err := f.pruner.PruneRun(ctx, run.ID)
	require.NoError(t, err)
	assert.False(t, released)
	released, err = f.pruner.PruneRun(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
}

func TestGroupResourcePruner_LookupErrorDefers(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	f.children.err = errors.New("database is locked")

	released, err := f.pruner.PruneRun(context.Background(), run.ID)
	require.Error(t, err)
	assert.False(t, released)
	assert.Empty(t, f.released())
	assert.True(t, dirExists(f.ws.path(run.Tasks[0].ChildInstanceID)))
}

func TestGroupResourcePruner_SweepPrunesRunsAndReleasesOnlyOldOrphans(t *testing.T) {
	f := newPruneFixture(t)
	run := f.seedRun(t, instance.GroupRunDone, "applied", instance.StateCompleted)
	oldOrphan, freshOrphan := uuid.New().String(), uuid.New().String()
	f.inventory.add(oldOrphan, time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))
	f.inventory.add(freshOrphan, time.Now())
	f.inventory.add("not-a-run-id", time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))

	require.NoError(t, f.pruner.Sweep(context.Background()))
	got := f.released()
	assert.ElementsMatch(t, []string{run.WorkspaceSnapshot, oldOrphan}, got,
		"a fresh orphan may be a launch in progress; a non-id entry is never touched")
}

func TestGroupResourcePruner_SweepListingErrorIsReturned(t *testing.T) {
	f := newPruneFixture(t)
	f.inventory.err = errors.New("permission denied")
	assert.ErrorContains(t, f.pruner.Sweep(context.Background()), "permission denied")
}

func TestNewGroupResourcePruner_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newPruneFixture(t)
	assert.Panics(t, func() { engine.NewGroupResourcePruner(nil, f.instances, f.children, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, nil, f.children, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, nil, f.ws, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, nil, f.area, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, nil, f.inventory) })
	assert.Panics(t, func() { engine.NewGroupResourcePruner(f.groups, f.instances, f.children, f.ws, f.area, nil) })
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestGroupResourcePruner|TestNewGroupResourcePruner' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_prune_test.go:...: undefined: engine.GroupResourcePruner
domain/engine/group_prune_test.go:...: undefined: engine.NewGroupResourcePruner
domain/engine/group_prune_test.go:...: undefined: engine.GroupOrphanSnapshotGrace
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/group_prune.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupOrphanSnapshotGrace is how old a snapshot without any group-run row must
// be before the sweep releases it. A younger one may belong to a launch that
// has snapshotted but not yet persisted its roster.
const GroupOrphanSnapshotGrace = time.Hour

// GroupResourcePruner releases a results_and_files run's working-area
// resources once nothing can need them any more:
//
//   - the run is done (gather and recovery are over): its gather's rollback
//     pin — and with it the staging area — is released right away;
//   - every launched task row is terminal (no writer, including tasks that
//     continue independently after a first_success decision), none has a
//     non-terminal descendant, and every group run nested under a task is
//     done: then completed task workspaces are removed (or, when nothing was
//     applied, retained and logged like Engine.warnWorkspaceRetained) and the
//     backing snapshot is released LAST — its absence is the durable
//     "already pruned" marker.
//
// Failed/cancelled task workspaces are never touched: the engine retained them
// for debugging. Results-only runs are not its concern (P2-T8 releases them).
type GroupResourcePruner struct {
	groups     instance.GroupRunRepository
	instances  instance.InstanceRepository
	children   ChildLister
	workspaces WorkspaceManager
	area       WorkingArea
	inventory  SnapshotInventory
	grace      time.Duration
	now        func() time.Time
}

// NewGroupResourcePruner constructs the pruner. Every dependency is required.
func NewGroupResourcePruner(groups instance.GroupRunRepository, instances instance.InstanceRepository, children ChildLister,
	workspaces WorkspaceManager, area WorkingArea, inventory SnapshotInventory) *GroupResourcePruner {
	switch {
	case groups == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil InstanceRepository")
	case children == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil ChildLister")
	case workspaces == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil WorkspaceManager")
	case area == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil WorkingArea")
	case inventory == nil:
		panic("engine: NewGroupResourcePruner requires a non-nil SnapshotInventory")
	}
	return &GroupResourcePruner{
		groups: groups, instances: instances, children: children, workspaces: workspaces,
		area: area, inventory: inventory, grace: GroupOrphanSnapshotGrace, now: time.Now,
	}
}

// PruneRun releases what runID no longer needs and reports whether its
// snapshot is released (now or earlier). false with a nil error means a pin is
// still held; an error leaves everything for the next pass (idempotent).
func (p *GroupResourcePruner) PruneRun(ctx context.Context, runID uuid.UUID) (bool, error) {
	run, err := p.groups.Get(ctx, runID)
	if errors.Is(err, instance.ErrGroupRunNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("group pruner: load run %s: %w", runID, err)
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles || strings.TrimSpace(run.WorkspaceSnapshot) == "" {
		return false, nil
	}
	if run.State != instance.GroupRunDone {
		return false, nil
	}
	present, err := p.inventory.HasSnapshot(ctx, run.WorkspaceSnapshot)
	if err != nil {
		return false, fmt.Errorf("group pruner: run %s: inspect snapshot: %w", run.ID, err)
	}
	if !present {
		return true, nil
	}
	if h, ok := GatherHandleFromReport(run.Gather); ok && h.RollbackRef != "" {
		if err := p.area.ReleaseRef(ctx, run.WorkspaceSnapshot, h.RollbackRef); err != nil {
			return false, fmt.Errorf("group pruner: run %s: release the gather's rollback pin %s: %w", run.ID, h.RollbackRef, err)
		}
	}
	pinned, completed, err := p.pins(ctx, run)
	if err != nil || pinned {
		return false, err
	}
	applied := run.Gather != nil && run.Gather.State == gatherStateApplied
	for _, id := range completed {
		if !applied {
			warnGroupTaskWorkspaceRetained(p.workspaces, id, "group_gather_not_applied")
			continue
		}
		if err := p.workspaces.RemoveInstanceDir(id); err != nil {
			return false, fmt.Errorf("group pruner: run %s: remove task workspace %s: %w", run.ID, id, err)
		}
	}
	if err := p.area.ReleaseSnapshot(ctx, run.WorkspaceSnapshot); err != nil {
		return false, fmt.Errorf("group pruner: run %s: release snapshot %s: %w", run.ID, run.WorkspaceSnapshot, err)
	}
	slog.Info("parallel_group: released the group's working-area resources",
		"groupRunID", run.ID, "snapshot", run.WorkspaceSnapshot, "gatherApplied", applied)
	return true, nil
}

// pins reports whether any writer or dependent still holds the run's
// resources, and otherwise lists the completed task instances.
func (p *GroupResourcePruner) pins(ctx context.Context, run *instance.GroupRun) (bool, []uuid.UUID, error) {
	var completed []uuid.UUID
	for _, t := range run.Tasks {
		child, err := p.instances.Get(ctx, t.ChildInstanceID)
		if err != nil {
			if t.LaunchState != instance.GroupTaskLaunched {
				continue // never started: nothing writes there
			}
			return false, nil, fmt.Errorf("group pruner: run %s task %q: load %s: %w", run.ID, t.Key, t.ChildInstanceID, err)
		}
		if !child.State.IsTerminal() {
			return true, nil, nil
		}
		active, err := anyActiveDescendant(ctx, p.children, child.ID)
		if err != nil {
			return false, nil, fmt.Errorf("group pruner: run %s task %q: list descendants: %w", run.ID, t.Key, err)
		}
		if active {
			return true, nil, nil
		}
		nested, err := p.groups.ListByParent(ctx, child.ID)
		if err != nil {
			return false, nil, fmt.Errorf("group pruner: run %s task %q: list nested groups: %w", run.ID, t.Key, err)
		}
		for _, n := range nested {
			if n.State != instance.GroupRunDone {
				return true, nil, nil
			}
		}
		if child.State == instance.StateCompleted {
			completed = append(completed, child.ID)
		}
	}
	return false, completed, nil
}

// Sweep prunes every run that still holds a snapshot and releases orphaned
// snapshots (no run row) older than the grace period. Per-entry errors are
// joined; the listing error is returned alone.
func (p *GroupResourcePruner) Sweep(ctx context.Context) error {
	infos, err := p.inventory.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("group pruner: list snapshots: %w", err)
	}
	var errs []error
	for _, info := range infos {
		id, perr := uuid.Parse(info.Handle)
		if perr != nil || id == uuid.Nil {
			continue
		}
		_, err := p.groups.Get(ctx, id)
		if errors.Is(err, instance.ErrGroupRunNotFound) {
			if p.now().Sub(info.ModTime) >= p.grace {
				if rerr := p.area.ReleaseSnapshot(ctx, info.Handle); rerr != nil {
					errs = append(errs, fmt.Errorf("group pruner: release orphaned snapshot %s: %w", info.Handle, rerr))
				}
			}
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("group pruner: load run %s: %w", id, err))
			continue
		}
		if _, err := p.PruneRun(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// anyActiveDescendant walks the spawn-link tree below rootID, through terminal
// intermediates, looking for a non-terminal run.
func anyActiveDescendant(ctx context.Context, children ChildLister, rootID uuid.UUID) (bool, error) {
	seen := map[uuid.UUID]bool{rootID: true}
	frontier := []uuid.UUID{rootID}
	for depth := 0; len(frontier) > 0 && depth <= instance.MaxSpawnDepth; depth++ {
		var next []uuid.UUID
		for _, id := range frontier {
			kids, err := children.ListChildren(ctx, id)
			if err != nil {
				return false, err
			}
			for _, c := range kids {
				if !c.State.IsTerminal() {
					return true, nil
				}
				if !seen[c.ID] {
					seen[c.ID] = true
					next = append(next, c.ID)
				}
			}
		}
		frontier = next
	}
	return false, nil
}

// warnGroupTaskWorkspaceRetained mirrors Engine.warnWorkspaceRetained
// (workspace.go:68-88) for a group task workspace kept past its run.
func warnGroupTaskWorkspaceRetained(ws WorkspaceManager, id uuid.UUID, reason string) {
	bytes, files, err := ws.InstanceDirStats(id)
	if err != nil {
		slog.Warn("workspace retained but stat failed", "instanceID", id, "reason", reason, "error", err)
		return
	}
	if files == 0 && bytes == 0 {
		return
	}
	slog.Warn("workspace retained on disk", "instanceID", id, "reason", reason, "bytes", bytes, "files", files)
}
```

- [x] Step 4: Run, confirm it passes.

```bash
go test ./domain/engine/ -run 'TestGroupResourcePruner|TestNewGroupResourcePruner' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected:
```
--- PASS: TestGroupResourcePruner_AppliedGatherReleasesSnapshotAndCompletedTaskWorkspaces (0.00s)
--- PASS: TestGroupResourcePruner_WaitsForAnIndependentTaskToStop (0.00s)
--- PASS: TestGroupResourcePruner_WaitsForActiveDescendantsThroughTerminalIntermediates (0.00s)
--- PASS: TestGroupResourcePruner_WaitsForANestedGroupUnderATask (0.00s)
--- PASS: TestGroupResourcePruner_NeverReleasesARunThatIsNotDone (0.00s)
--- PASS: TestGroupResourcePruner_UnappliedGatherRetainsCompletedTaskWorkspaces (0.00s)
--- PASS: TestGroupResourcePruner_ReleaseErrorIsReturnedAndRetried (0.00s)
--- PASS: TestGroupResourcePruner_AlreadyReleasedIsANoOp (0.00s)
--- PASS: TestGroupResourcePruner_ResultsModeAndUnknownRunsAreNotItsConcern (0.00s)
--- PASS: TestGroupResourcePruner_LookupErrorDefers (0.00s)
--- PASS: TestGroupResourcePruner_SweepPrunesRunsAndReleasesOnlyOldOrphans (0.00s)
--- PASS: TestGroupResourcePruner_SweepListingErrorIsReturned (0.00s)
--- PASS: TestNewGroupResourcePruner_PanicsOnAMissingCollaborator (0.00s)
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 5: Whole package, gofmt, vet, commit.

10.5: Missing-pruner red → focused green; whole engine and vet pass. Ordered review: (1) preserved done-only release, descendant/nested-group pins, failed/unapplied retention and last-step snapshot release; (2) tests assert actual workspace bytes/removal, exact run-derived rollback/snapshot identities and inventory disappearance, including nonempty active descendants; (3) checked neighboring workspace retention logs and spawn-depth conventions. Reran focused race tests after review, green. Real inventory/producer composition remains P3-T11.

```bash
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_prune.go domain/engine/group_prune_test.go
go vet ./domain/engine/
git add domain/engine/group_prune.go domain/engine/group_prune_test.go && git commit -m "feat(engine): prune group refs and task worktrees only after writers stop and pins release" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok`; `gofmt -l`/`go vet` print nothing.

### Task 10.6: GroupGatherReconciler — startup/sweep recovery, parent cancellation, session restoration, event watch
**Files:**
- Create: `domain/engine/group_gather_reconciler.go`
- Test: `domain/engine/group_gather_reconciler_test.go` (package `engine_test`)

- [x] Step 1: Write the failing tests. Create `domain/engine/group_gather_reconciler_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errCrashAfterApply = errors.New("process died after applying")

// fanoutBus is an EventBus whose SubscribeAll really delivers (the shared
// mockEventBus's channel never does), so Watch is exercised end to end.
type fanoutBus struct {
	mu   sync.Mutex
	subs []chan engine.ScopedEvent
}

var _ engine.EventBus = (*fanoutBus)(nil)

func (b *fanoutBus) Publish(id uuid.UUID, ev engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- engine.ScopedEvent{InstanceID: id, Event: ev}:
		default:
		}
	}
}

func (b *fanoutBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	return ch, func() {}
}

func (b *fanoutBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent, 64)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, s := range b.subs {
				if s == ch {
					b.subs = append(b.subs[:i], b.subs[i+1:]...)
					break
				}
			}
			close(ch)
		})
	}
}

type closeCall struct {
	ref    instance.ResolutionSessionRef
	reason string
}

// recordingCloser is a GroupSessionCloser recording which session (by its
// full identity) was closed and why.
type recordingCloser struct {
	mu    sync.Mutex
	calls []closeCall
	notified []instance.ResolutionSessionRef
	err   error
}

var _ engine.GroupSessionCloser = (*recordingCloser)(nil)

func (c *recordingCloser) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, closeCall{ref: ref, reason: reason})
	return c.err
}

func (c *recordingCloser) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" { return errors.New("missing notification identity") }
	c.notified = append(c.notified, ref)
	return c.err
}

func (c *recordingCloser) all() []closeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]closeCall(nil), c.calls...)
}

// fakeGatherLocker models the executor's gate: busy runs refuse TryLockGather.
type fakeGatherLocker struct {
	mu   sync.Mutex
	busy map[uuid.UUID]bool
}

var _ engine.GatherLocker = (*fakeGatherLocker)(nil)

func (l *fakeGatherLocker) TryLockGather(id uuid.UUID) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[uuid.UUID]bool{}
	}
	if l.busy[id] {
		return nil, false
	}
	l.busy[id] = true
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.busy, id)
	}, true
}

func (l *fakeGatherLocker) setBusy(id uuid.UUID, busy bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy == nil {
		l.busy = map[uuid.UUID]bool{}
	}
	l.busy[id] = busy
}

// reconcileFixture: P3-T6's gatherFixture (all_succeed success, tasks in
// author order) whose snapshot handle is the run id, an instance repo holding
// the parent at g1 plus every task row (completed), and a REAL FileGatherer as
// the aborter and a REAL pruner over P3-T6's fakeGatherArea.
type reconcileFixture struct {
	*gatherFixture
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	closer    *recordingCloser
	completer *recordingCompleter
	waker     *fakeNodeWaker
	qm        *mockQueueManager
	locker    *fakeGatherLocker
	events    *fanoutBus
	inventory *fakeInventory
	rec       *engine.GroupGatherReconciler
}

func newReconcileFixture(t *testing.T, keys ...string) *reconcileFixture {
	t.Helper()
	g := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", keys...)
	g.groups.update(g.runID, func(r *instance.GroupRun) { r.WorkspaceSnapshot = r.ID.String() })
	ctx := context.Background()
	instances := newMockInstanceRepo()
	nodeID := "g1"
	parent := *g.parent
	parent.CurrentNodeID = &nodeID
	require.NoError(t, instances.Create(ctx, &parent))
	for _, task := range g.groups.snapshot(g.runID).Tasks {
		require.NoError(t, instances.Create(ctx, &instance.Instance{ID: task.ChildInstanceID, OwnerID: g.parent.OwnerID, State: instance.StateCompleted}))
	}
	f := &reconcileFixture{
		gatherFixture: g, instances: instances, nodeExecs: newMockNodeExecutionRepo(),
		closer: &recordingCloser{}, completer: &recordingCompleter{}, waker: &fakeNodeWaker{instances: instances},
		qm: newMockQueueManager(), locker: &fakeGatherLocker{}, events: &fanoutBus{},
	}
	f.inventory = newFakeInventory(g.area)
	f.inventory.add(g.runID.String(), time.Now())
	pruner := engine.NewGroupResourcePruner(g.groups, instances, &fakeChildLister{}, newDirWorkspaces(t), g.area, f.inventory)
	f.rec = engine.NewGroupGatherReconciler(g.groups, instances, f.nodeExecs, g.gatherer, f.closer, f.completer,
		f.waker, f.qm, f.locker, f.events, pruner)
	return f
}

func (f *reconcileFixture) setParent(state instance.State) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parent.ID].State = state
}

func (f *reconcileFixture) parentState() instance.State {
	return f.instances.get(f.parent.ID).State
}

// openSession runs the gather into a conflict on b (a merged into staging, a
// session opened) and suspends the parent the way the executor does.
func (f *reconcileFixture) openSession(t *testing.T) instance.ResolutionSessionRef {
	t.Helper()
	f.conflictOnB()
	step, err := f.gather()
	require.NoError(t, err)
	require.False(t, step.Done)
	f.setParent(instance.StateWaiting)
	rep := f.report()
	require.NotNil(t, rep.ActiveSession)
	return *rep.ActiveSession
}

// crashAfterApply runs a clean gather whose process "dies" right after Apply
// wrote the parent's area: the run stays gathering, the area is changed.
func (f *reconcileFixture) crashAfterApply(t *testing.T) {
	t.Helper()
	f.groups.failCall("SetGatherReport", 5, errCrashAfterApply)
	_, err := f.gather()
	require.ErrorIs(t, err, errCrashAfterApply)
	require.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State)
	require.Contains(t, f.area.parentFiles(gatherParentDir), "a.txt", "precondition: the crash happened after Apply")
}

func (f *reconcileFixture) addSessionRecord(t *testing.T, ref instance.ResolutionSessionRef, open bool, endReason string) {
	t.Helper()
	conv := map[string]any{
		engine.ConversationKeyKind:       engine.ConversationKindGroupResolution,
		engine.ConversationKeyGroupRunID: ref.GroupRunID.String(),
		engine.ConversationKeyMergeIndex: ref.MergeIndex,
		engine.ConversationKeyAttempt:    ref.Attempt,
		engine.ConversationKeyTaskKey:    "b",
		engine.ConversationKeyMessages:   []map[string]any{},
	}
	if !open {
		conv[engine.ConversationKeyEndedAt] = time.Now().UTC().Format(time.RFC3339Nano)
		conv[engine.ConversationKeyEndReason] = endReason
	}
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: ref.InstanceID, NodeID: ref.NodeID, Attempt: 2,
		Status: instance.NodeStatusSucceeded, LLMConversation: conv, StartedAt: time.Now(),
	}))
}

// Named by P3-T9 (TestGroupAbandon_Chain_CancelledParentMidSessionStaysCancelled).
func TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.setParent(instance.StateCancelled)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))

	assert.Equal(t, []closeCall{{ref: active, reason: "cancelled"}}, f.closer.all(), "the open session is closed")
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: no task change reached the parent")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.True(t, run.ContinuationDelivered, "a cancelled parent never consumes the continuation")
	require.NotNil(t, run.Gather)
	assert.Equal(t, engine.ResolutionOutcomeCancelled, run.Gather.Attempts[0].Outcome)
	assert.Nil(t, run.Gather.ActiveSession)
	assert.Contains(t, run.Gather.Error, "cancelled")
	assert.Equal(t, instance.StateCancelled, f.parentState(), "the parent stays cancelled")
	assert.Empty(t, f.waker.wakeCalls(), "a cancelled parent is never woken")
	assert.Empty(t, f.completer.all())
}

func TestGroupGatherReconciler_DecidedRunUnderCancelledParentSettlesWithoutStaging(t *testing.T) {
	f := newReconcileFixture(t, "a")
	f.setParent(instance.StateCancelled)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.area.begins(), "nothing is staged for a parent that will never continue")
	assert.Empty(t, f.closer.all())
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
}

// A crash mid-gather leaves the parent `running`; RecoverCrashedInstances marks
// it failed; the startup pass restores its exact pre-gather area.
func TestGroupGatherReconciler_FailedParentMidGather_RestoresThePreGatherArea(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateFailed)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "SINK: partial work is rolled back")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.Equal(t, "rolled_back", run.Gather.State)
	assert.Contains(t, run.Gather.Error, "failed")
	assert.Equal(t, instance.StateFailed, f.parentState())
}

func TestGroupGatherReconciler_CompletedParentSettlesWithoutTouchingItsVanishedArea(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateCompleted)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Zero(t, f.area.rollbackCount(), "a completed parent's workspace was removed; nothing is written to it")
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, "failed", run.Gather.State)
	assert.Contains(t, run.Gather.Error, "nothing was restored")
	assert.True(t, run.ContinuationDelivered)
}

func TestGroupGatherReconciler_RollbackFailureStaysVisibleUntilRepaired(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateFailed)
	f.area.failRollback = errors.New("read-only file system")
	ctx := context.Background()

	err := f.rec.RecoverRun(ctx, f.runID)
	require.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "continuation stays blocked")
	assert.Equal(t, "rollback_failed", run.Gather.RollbackStatus)

	f.area.failRollback = nil
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestGroupGatherReconciler_WaitingParentWithAnEndedAttemptIsWoken(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.endOpenAttempts("")

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.StateQueued, f.parentState(), "the lost wake is recovered")
	assert.Equal(t, 1, f.qm.notifyCount())
	assert.Equal(t, []closeCall{{ref: active, reason: "ended"}}, f.closer.all(), "the record of the ended session is closed")
}

func TestGroupGatherReconciler_WaitingParentWithAnOpenSessionIsRestoredUntouched(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	active := f.openSession(t)
	f.addSessionRecord(t, active, true, "")

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.waker.wakeCalls())
	assert.Empty(t, f.completer.all())
	assert.Empty(t, f.closer.all())
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)
	assert.Equal(t, instance.StateWaiting, f.parentState(), "the operator keeps resolving in the persisted session")
}

func TestGroupGatherReconciler_MissingSessionRecordEndsTheAttemptThroughTheCompleter(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []completerCall{{instanceID: f.parent.ID, nodeID: "g1", abandoned: false}}, f.completer.all(),
		"the gatherer then verifies the staging area and reopens a fresh attempt if needed")
}

func TestGroupGatherReconciler_ClosedRecordWithAnOpenAttemptIsCompleted(t *testing.T) {
	for _, tc := range []struct {
		reason    string
		abandoned bool
	}{{"user_ended", false}, {"abandoned", true}} {
		t.Run(tc.reason, func(t *testing.T) {
			f := newReconcileFixture(t, "a", "b")
			active := f.openSession(t)
			f.addSessionRecord(t, active, false, tc.reason)

			require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
			assert.Equal(t, []completerCall{{instanceID: f.parent.ID, nodeID: "g1", abandoned: tc.abandoned}}, f.completer.all())
		})
	}
}

func TestGroupGatherReconciler_SessionNeverOpenedWakesTheParent(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession = nil })

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls(),
		"the re-entered gatherer opens the recorded attempt's session")
}

func TestGroupGatherReconciler_WaitingParentOfAGatheringRunIsWokenToRetry(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.crashAfterApply(t)
	f.setParent(instance.StateWaiting)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Equal(t, []wakeCall{{id: f.parent.ID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State, "the parent's executor resumes it; nothing is aborted")
	assert.Zero(t, f.area.rollbackCount())
}

func TestGroupGatherReconciler_RunningParentIsLeftToItsExecutor(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateRunning)

	require.NoError(t, f.rec.RecoverRun(context.Background(), f.runID))
	assert.Empty(t, f.waker.wakeCalls())
	assert.Empty(t, f.closer.all())
	assert.Zero(t, f.area.rollbackCount())
}

func TestGroupGatherReconciler_ActiveWriterIsSkippedUntilItReleases(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateCancelled)
	f.locker.setBusy(f.runID, true)
	ctx := context.Background()

	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State, "the writer's gather is never aborted under it")
	assert.Zero(t, f.area.rollbackCount())

	f.locker.setBusy(f.runID, false)
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
}

func TestGroupGatherReconciler_ResultsModeAndDoneRunsAreIgnored(t *testing.T) {
	f := newReconcileFixture(t, "a")
	f.setParent(instance.StateCancelled)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.GatherMode = workflow.GroupGatherResults })
	ctx := context.Background()

	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Equal(t, instance.GroupRunDecided, f.groups.snapshot(f.runID).State)

	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.GatherMode = workflow.GroupGatherResultsAndFiles
		r.State = instance.GroupRunDone
	})
	require.NoError(t, f.rec.RecoverRun(ctx, f.runID))
	assert.Empty(t, f.area.begins())
}

func TestGroupGatherReconciler_ReconcileRecoversUnsettledRunsThenSweepsResources(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	f.setParent(instance.StateCancelled)
	orphan := uuid.New().String()
	f.inventory.add(orphan, time.Now().Add(-2*engine.GroupOrphanSnapshotGrace))

	require.NoError(t, f.rec.Reconcile(context.Background()))
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
	f.area.mu.Lock()
	released := append([]string(nil), f.area.releasedSnapshots...)
	f.area.mu.Unlock()
	assert.ElementsMatch(t, []string{f.runID.String(), orphan}, released,
		"the settled run's resources and an old orphan are released in the same pass")
}

func TestGroupGatherReconciler_WatchAbortsAsSoonAsTheParentIsCancelled(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	f.openSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := f.rec.Watch(ctx)

	f.setParent(instance.StateCancelled)
	f.events.Publish(f.parent.ID, engine.Event{Type: engine.EventTypeInstanceCancelled, Timestamp: time.Now()})

	require.Eventually(t, func() bool {
		return f.groups.snapshot(f.runID).State == instance.GroupRunDone
	}, 5*time.Second, 10*time.Millisecond, "the cancellation event settles the gather without waiting for a sweep")
	assert.Equal(t, instance.StateCancelled, f.parentState())

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not stop when its context was cancelled")
	}
}

func TestGroupGatherReconciler_WatchPrunesWhenTheLastWriterStops(t *testing.T) {
	f := newReconcileFixture(t, "a", "b")
	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	loser := f.groups.snapshot(f.runID).Tasks[1].ChildInstanceID
	f.instances.mu.Lock()
	f.instances.instances[loser].State = instance.StateRunning
	f.instances.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.rec.Watch(ctx)
	f.events.Publish(f.parent.ID, engine.Event{Type: engine.EventTypeGroupCompleted, Data: map[string]any{"groupRunId": f.runID.String(), "nodeId": "g1"}})
	time.Sleep(100 * time.Millisecond)
	has, err := f.inventory.HasSnapshot(ctx, f.runID.String())
	require.NoError(t, err)
	assert.True(t, has, "a running task still pins the snapshot")

	f.instances.mu.Lock()
	f.instances.instances[loser].State = instance.StateCompleted
	f.instances.mu.Unlock()
	f.events.Publish(loser, engine.Event{Type: engine.EventTypeInstanceCompleted})
	require.Eventually(t, func() bool {
		has, _ := f.inventory.HasSnapshot(ctx, f.runID.String())
		return !has
	}, 5*time.Second, 10*time.Millisecond, "the last task's completion releases the run's resources")
}

func TestNewGroupGatherReconciler_PanicsOnAMissingCollaborator(t *testing.T) {
	f := newReconcileFixture(t, "a")
	pruner := engine.NewGroupResourcePruner(f.groups, f.instances, &fakeChildLister{}, newDirWorkspaces(t), f.area, f.inventory)
	build := func(mut func(*[11]any)) func() {
		return func() {
			args := [11]any{f.groups, f.instances, f.nodeExecs, f.gatherer, f.closer, f.completer, f.waker, f.qm, f.locker, f.events, pruner}
			mut(&args)
			groups, _ := args[0].(instance.GroupRunRepository)
			instances, _ := args[1].(instance.InstanceRepository)
			nodeExecs, _ := args[2].(instance.NodeExecutionRepository)
			aborter, _ := args[3].(engine.GatherAborter)
			closer, _ := args[4].(engine.GroupSessionCloser)
			completer, _ := args[5].(engine.GroupResolutionCompleter)
			waker, _ := args[6].(instance.NodeAddressedWaker)
			qm, _ := args[7].(engine.QueueManager)
			locker, _ := args[8].(engine.GatherLocker)
			bus, _ := args[9].(engine.EventBus)
			p, _ := args[10].(*engine.GroupResourcePruner)
			engine.NewGroupGatherReconciler(groups, instances, nodeExecs, aborter, closer, completer, waker, qm, locker, bus, p)
		}
	}
	for i := 0; i < 11; i++ {
		idx := i
		assert.Panics(t, build(func(a *[11]any) { a[idx] = nil }), "argument %d is required", idx)
	}
	assert.NotPanics(t, build(func(*[11]any) {}))
}
```

- [x] Step 2: Run, confirm it fails.

```bash
go test ./domain/engine/ -run 'TestGroupGatherReconciler|TestNewGroupGatherReconciler' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_gather_reconciler_test.go:...: undefined: engine.GroupGatherReconciler
domain/engine/group_gather_reconciler_test.go:...: undefined: engine.NewGroupGatherReconciler
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 3: Implement. Create `domain/engine/group_gather_reconciler.go`:

```go
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Abort reasons recorded in GatherReport.Error and session end reasons.
const (
	gatherAbortParentCancelled = "parent instance was cancelled while the group was gathering files"
	gatherAbortParentFailed    = "parent instance failed while the group was gathering files"
	gatherAbortParentCompleted = "parent instance finished without delivering the group's file gather; its working area is gone, so nothing was restored"

	resolutionCloseCancelled = "cancelled"
	resolutionCloseError     = "error"
	resolutionCloseEnded     = "ended"
	resolutionEndAbandoned   = "abandoned"
)

// GroupGatherReconciler recovers results_and_files gathers that no parent
// executor is driving: at startup (after RecoverCrashedInstances and the group
// coordinator's pass), on the group sweep tick, and — through Watch — as soon
// as a parent is cancelled, fails or finishes. It never writes a parent's
// working area while that parent's executor holds the run's gather gate.
type GroupGatherReconciler struct {
	groups    instance.GroupRunRepository
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	aborter   GatherAborter
	closer    GroupSessionCloser
	completer GroupResolutionCompleter
	waker     instance.NodeAddressedWaker
	queueMgr  QueueManager
	locker    GatherLocker
	bus       EventBus
	pruner    *GroupResourcePruner
	now       func() time.Time
}

// NewGroupGatherReconciler constructs the reconciler. Every collaborator is
// required: a missing one is a composition-root bug that would leave
// cancelled parents' gathers unsettled or resources unreleased.
func NewGroupGatherReconciler(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	aborter GatherAborter,
	closer GroupSessionCloser,
	completer GroupResolutionCompleter,
	waker instance.NodeAddressedWaker,
	queueMgr QueueManager,
	locker GatherLocker,
	bus EventBus,
	pruner *GroupResourcePruner,
) *GroupGatherReconciler {
	switch {
	case groups == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil InstanceRepository")
	case nodeExecs == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil NodeExecutionRepository")
	case aborter == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GatherAborter")
	case closer == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupSessionCloser")
	case completer == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupResolutionCompleter")
	case waker == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil NodeAddressedWaker")
	case queueMgr == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil QueueManager")
	case locker == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GatherLocker")
	case bus == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil EventBus")
	case pruner == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupResourcePruner")
	}
	return &GroupGatherReconciler{
		groups: groups, instances: instances, nodeExecs: nodeExecs, aborter: aborter, closer: closer,
		completer: completer, waker: waker, queueMgr: queueMgr, locker: locker, bus: bus, pruner: pruner, now: time.Now,
	}
}

// Reconcile recovers every unsettled file gather, then sweeps working-area
// resources. Per-run problems are logged (the next pass retries); only the
// listing error is returned. Satisfies cmd/arbitron's groupReconciler.
func (r *GroupGatherReconciler) Reconcile(ctx context.Context) error {
	ids, err := r.groups.ListUnsettled(ctx)
	if err != nil {
		return fmt.Errorf("group gather reconciler: list unsettled group runs: %w", err)
	}
	for _, id := range ids {
		if err := r.RecoverRun(ctx, id); err != nil {
			slog.Warn("group gather reconciler: run not recovered this pass", "groupRunID", id, "error", err)
		}
	}
	if err := r.pruner.Sweep(ctx); err != nil {
		slog.Warn("group gather reconciler: resource sweep incomplete", "error", err)
	}
	return nil
}

// RecoverRun brings one results_and_files run that is decided, gathering or
// resolving conflicts in line with its parent's state (see the task's
// "Recovery by parent state" decision). Other runs are left alone.
func (r *GroupGatherReconciler) RecoverRun(ctx context.Context, runID uuid.UUID) error {
	run, err := r.groups.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: load run %s: %w", runID, err)
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles || run.Decision == nil || !gatherableGroupState(run.State) {
		return nil
	}
	parent, err := r.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: load parent %s: %w", run.ID, run.ParentInstanceID, err)
	}
	switch parent.State {
	case instance.StateCancelled:
		return r.abort(ctx, run.ID, ResolutionOutcomeCancelled, gatherAbortParentCancelled, resolutionCloseCancelled, false)
	case instance.StateFailed:
		return r.abort(ctx, run.ID, ResolutionOutcomeError, gatherAbortParentFailed, resolutionCloseError, false)
	case instance.StateCompleted:
		return r.abort(ctx, run.ID, ResolutionOutcomeError, gatherAbortParentCompleted, resolutionCloseError, true)
	case instance.StateWaiting:
		if parent.CurrentNodeID != nil && *parent.CurrentNodeID == run.NodeID {
			return r.restore(ctx, run)
		}
	}
	return nil
}

// abort settles a run whose parent will never continue it. areaGone drops the
// gather handle first, so nothing is restored into a removed working area.
func (r *GroupGatherReconciler) abort(ctx context.Context, runID uuid.UUID, outcome, reason, sessionReason string, areaGone bool) error {
	unlock, ok := r.locker.TryLockGather(runID)
	if !ok {
		return nil // the parent's executor is writing this gather right now; it sees the terminal parent itself
	}
	defer unlock()

	run, err := r.groups.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: reload run %s: %w", runID, err)
	}
	if run.State == instance.GroupRunDone {
		return r.settle(ctx, run)
	}
	if !gatherableGroupState(run.State) {
		return nil
	}
	if run.Gather != nil && run.Gather.ActiveSession != nil {
		if err := r.closer.CloseSession(ctx, *run.Gather.ActiveSession, sessionReason); err != nil {
			return fmt.Errorf("group gather reconciler: run %s: close the open resolution session: %w", run.ID, err)
		}
	}
	target := run
	if areaGone {
		target = withoutGatherHandle(run)
	}
	if _, err := r.aborter.AbortGather(ctx, target, outcome, reason); err != nil {
		return fmt.Errorf("group gather reconciler: run %s: abort the gather: %w", run.ID, err)
	}
	fresh, err := r.groups.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: reload run %s: %w", runID, err)
	}
	slog.Info("group gather reconciler: aborted a file gather whose parent will not continue it",
		"groupRunID", run.ID, "parentID", run.ParentInstanceID, "reason", reason)
	return r.settle(ctx, fresh)
}

// settle records that a done run's continuation will never be consumed, so it
// leaves ListUnsettled.
func (r *GroupGatherReconciler) settle(ctx context.Context, run *instance.GroupRun) error {
	if run.ContinuationDelivered {
		return nil
	}
	if err := r.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
		return fmt.Errorf("group gather reconciler: settle run %s: %w", run.ID, err)
	}
	return nil
}

// restore handles a parent that is waiting at the group node.
func (r *GroupGatherReconciler) restore(ctx context.Context, run *instance.GroupRun) error {
	switch run.State {
	case instance.GroupRunDecided:
		if !run.ActionsApplied {
			return nil
		}
		return r.wake(ctx, run)
	case instance.GroupRunGathering:
		return r.wake(ctx, run) // crash window, or a failed rollback to retry
	}
	rep := run.Gather
	if rep == nil || rep.ActiveSession == nil {
		return r.wake(ctx, run) // the re-entered gatherer opens the recorded attempt's session
	}
	idx := latestAttemptIndex(rep, rep.MergeIndex)
	if idx < 0 {
		return r.wake(ctx, run)
	}
	active := *rep.ActiveSession
	if rep.Attempts[idx].EndedAt != nil {
		if err := r.closer.CloseSession(ctx, active, resolutionCloseEnded); err != nil {
			return fmt.Errorf("group gather reconciler: run %s: close the ended resolution session: %w", run.ID, err)
		}
		return r.wake(ctx, run)
	}
	exec, err := findGroupSessionExec(ctx, r.nodeExecs, active)
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: find the resolution session: %w", run.ID, err)
	}
	if exec != nil && IsOpenGroupResolutionSession(exec.LLMConversation) {
		if sent, _ := exec.LLMConversation["notificationSent"].(bool); !sent {
			return r.closer.NotifyResolutionRequired(ctx, active)
		}
		return nil // restored: the persisted session serves the next operator turn
	}
	abandoned := false
	if exec != nil {
		reason, _ := exec.LLMConversation[ConversationKeyEndReason].(string)
		abandoned = reason == resolutionEndAbandoned
	}
	if err := r.completer.OnResolutionSessionEnded(ctx, run.ParentInstanceID, run.NodeID, abandoned); err != nil {
		return fmt.Errorf("group gather reconciler: run %s: finish the interrupted resolution attempt: %w", run.ID, err)
	}
	return nil
}

func (r *GroupGatherReconciler) wake(ctx context.Context, run *instance.GroupRun) error {
	woke, err := r.waker.SetQueuedIfWaitingAtNode(ctx, run.ParentInstanceID, run.NodeID, r.now())
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: wake parent %s: %w", run.ID, run.ParentInstanceID, err)
	}
	if woke {
		r.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}

// Watch subscribes to every instance's events NOW (before returning) and, in
// its own goroutine until ctx ends, reacts to terminal instance events and
// group completions: a cancelled/failed/completed parent's file gathers are
// recovered at once, and runs whose last writer just stopped are pruned. The
// returned channel closes when the loop has stopped.
func (r *GroupGatherReconciler) Watch(ctx context.Context) <-chan struct{} {
	events, unsubscribe := r.bus.SubscribeAll()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				r.handleEvent(ctx, ev)
			}
		}
	}()
	return done
}

func (r *GroupGatherReconciler) handleEvent(ctx context.Context, ev ScopedEvent) {
	switch ev.Event.Type {
	case EventTypeInstanceCancelled, EventTypeInstanceFailed, EventTypeInstanceCompleted:
		r.recoverParent(ctx, ev.InstanceID)
		r.pruneForChild(ctx, ev.InstanceID)
	case EventTypeGroupCompleted:
		if id, ok := eventGroupRunID(ev.Event.Data); ok {
			r.prune(ctx, id)
		}
	}
}

func (r *GroupGatherReconciler) recoverParent(ctx context.Context, parentID uuid.UUID) {
	runs, err := r.groups.ListByParent(ctx, parentID)
	if err != nil {
		slog.Warn("group gather reconciler: list the parent's group runs failed; the sweep retries", "parentID", parentID, "error", err)
		return
	}
	for _, run := range runs {
		if run.GatherMode != workflow.GroupGatherResultsAndFiles {
			continue
		}
		if run.State != instance.GroupRunDone {
			if err := r.RecoverRun(ctx, run.ID); err != nil {
				slog.Warn("group gather reconciler: run not recovered; the sweep retries", "groupRunID", run.ID, "error", err)
			}
		}
		r.prune(ctx, run.ID)
	}
}

func (r *GroupGatherReconciler) pruneForChild(ctx context.Context, childID uuid.UUID) {
	run, err := r.groups.GetByChild(ctx, childID)
	if err != nil {
		slog.Warn("group gather reconciler: resolve a task's group run failed; the sweep retries", "childID", childID, "error", err)
		return
	}
	if run == nil {
		return
	}
	r.prune(ctx, run.ID)
}

func (r *GroupGatherReconciler) prune(ctx context.Context, runID uuid.UUID) {
	if _, err := r.pruner.PruneRun(ctx, runID); err != nil {
		slog.Warn("group gather reconciler: resources not released this pass", "groupRunID", runID, "error", err)
	}
}

// withoutGatherHandle returns a copy of run whose gather report has no
// working-area handle, so AbortGather records the failure without touching an
// area that no longer exists.
func withoutGatherHandle(run *instance.GroupRun) *instance.GroupRun {
	cp := *run
	if run.Gather != nil {
		rep := copyGatherReport(*run.Gather)
		rep.Handle = nil
		cp.Gather = &rep
	}
	return &cp
}

// eventGroupRunID reads the "groupRunId" every group.* event carries.
func eventGroupRunID(data any) (uuid.UUID, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return uuid.Nil, false
	}
	s, _ := m["groupRunId"].(string)
	id, err := uuid.Parse(s)
	return id, err == nil && id != uuid.Nil
}
```

- [x] Step 4: Run, confirm it passes.

```bash
go test ./domain/engine/ -run 'TestGroupGatherReconciler|TestNewGroupGatherReconciler' -race -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
Expected:
```
--- PASS: TestGroupGatherReconciler_CancelledParentMidSession_RollsBackAndSettles (0.00s)
--- PASS: TestGroupGatherReconciler_DecidedRunUnderCancelledParentSettlesWithoutStaging (0.00s)
--- PASS: TestGroupGatherReconciler_FailedParentMidGather_RestoresThePreGatherArea (0.00s)
--- PASS: TestGroupGatherReconciler_CompletedParentSettlesWithoutTouchingItsVanishedArea (0.00s)
--- PASS: TestGroupGatherReconciler_RollbackFailureStaysVisibleUntilRepaired (0.00s)
--- PASS: TestGroupGatherReconciler_WaitingParentWithAnEndedAttemptIsWoken (0.00s)
--- PASS: TestGroupGatherReconciler_WaitingParentWithAnOpenSessionIsRestoredUntouched (0.00s)
--- PASS: TestGroupGatherReconciler_MissingSessionRecordEndsTheAttemptThroughTheCompleter (0.00s)
--- PASS: TestGroupGatherReconciler_ClosedRecordWithAnOpenAttemptIsCompleted (0.00s)
--- PASS: TestGroupGatherReconciler_SessionNeverOpenedWakesTheParent (0.00s)
--- PASS: TestGroupGatherReconciler_WaitingParentOfAGatheringRunIsWokenToRetry (0.00s)
--- PASS: TestGroupGatherReconciler_RunningParentIsLeftToItsExecutor (0.00s)
--- PASS: TestGroupGatherReconciler_ActiveWriterIsSkippedUntilItReleases (0.00s)
--- PASS: TestGroupGatherReconciler_ResultsModeAndDoneRunsAreIgnored (0.00s)
--- PASS: TestGroupGatherReconciler_ReconcileRecoversUnsettledRunsThenSweepsResources (0.00s)
--- PASS: TestGroupGatherReconciler_WatchAbortsAsSoonAsTheParentIsCancelled (0.0Xs)
--- PASS: TestGroupGatherReconciler_WatchPrunesWhenTheLastWriterStops (0.1Xs)
--- PASS: TestNewGroupGatherReconciler_PanicsOnAMissingCollaborator (0.00s)
ok  	github.com/arbitron/arbitron/domain/engine	X.XXXs
```

- [x] Step 5: Full regression for everything this task touched, gofmt, vet, commit.

10.6: Confirmed missing-reconciler red, including supplemental notification/closed-automatic-session restart tests written before implementation. Focused recovery race tests pass, as do all task-focused race tests, the whole engine and Git packages, formatting, vet and diff checks. Ran the full module gate twice: only the previously recorded `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure remains; all other packages pass.

Ordered self-review before the final commit:
1. **Spec compliance:** checked all six slices against this task and Phase 3 design. Added red/green coverage for absolute paths containing `..` (not only escaping paths). Kept final group outcome distinct from immutable task outcome per P3-T6's amended contract. Found that the sample reconciler locked only aborts, not session restoration: added a failing active-writer restoration test, then moved the gate to cover both paths and read run/parent under it. No additional flags, modes or partial root activation.
2. **Anti-green-theater:** added exact full-session-ref notification assertions, retryable notification/lookup errors, and automatic-session crash recovery with no second provider turn. Extended the real Git + SQLite integration with both restart windows: real finalized task refs produce a conflict; the resolver edits its supplied staging area; the real reconciler/bridge either persists the conflict notification or wakes through SQL. The automatic restart then runs the real Engine + registered executor to completion and asserts persisted downstream file contents and group output, not only a wake callback. Temporarily removed notification delivery: the real persisted-notification sink test failed. Restored it, temporarily removed completion dispatch: automatic restart failed on the durable queue intake required for the downstream sink. Restored both, reran recovery and all task-focused tests with `-race`, green. These local stack probes do not substitute for production-root construction.
3. **Code quality:** reviewed adjacent launcher/wait, WorkingArea/runner, resolution bridge, workspace retention and reconciler conventions. Kept cleanup on the snapshot-producing capability, refcounted gate entries, existing port/error/log shapes and named constants. Removed the redundant abort-only lock/read after centralizing the recovery gate, corrected supplied test type assertions and obsolete first-entry fixtures, formatted/vetted and reran the full module gate and focused race suite after review.

```bash
go test ./domain/engine/ ./adapter/git/ -count=1
go test ./... -count=1
gofmt -l domain/engine/group_gather_reconciler.go domain/engine/group_gather_reconciler_test.go
go vet ./domain/engine/ ./adapter/git/
git add domain/engine/group_gather_reconciler.go domain/engine/group_gather_reconciler_test.go && git commit -m "feat(engine): reconcile interrupted file gathers, abort under cancelled parents, restore resolution sessions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
Expected: `ok` for `domain/engine` and `adapter/git`; `go test ./...` reports `ok` for every package except the pre-existing, unrelated `adapter/config` failure `TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` noted by P2-T14 (fails identically on the base commit); `gofmt -l`/`go vet` print nothing.

- [x] Completion gate: Prove file launch, executor gathering, recovery and resource pruning through the real production composition root.

> RESOLVED by P3-T11: `initDomain` now constructs the Git working area, file finalizer, resolution bridge, gatherer, resource pruner and gather reconciler with real repositories, workspace, profile stream, event bus and node-addressed queue. The launcher receives `SetFileWorkingArea`, the executor receives `SetGatherer`, startup reconciliation precedes queue intake, and the sweep/event watcher feed recovery and pruning. The former missing-production-wiring blocker is closed.

Completion verification pass (no new production behavior needed):
- Task-focused engine/Git tests pass with `-race`; the full engine and Git packages pass.
- All `TestGroupFilesE2E_*` tests pass with `-race` through `initDomain`: downstream nodes persist actual gathered file contents, startup and periodic recovery resume interrupted sessions, cancellation settles on failure without continuing the parent, and backing snapshots/task workspaces disappear from disk.
- `go test ./... -count=1` passes every package except the previously documented, unrelated `adapter/config: TestSampleFeaturePipeline_ValidatesWithDefaultProfiles` unknown-profile failure. Formatting, vet and diff checks are clean.

Completion self-review, in the required order:
1. **Spec compliance:** reviewed the six local slices and production-root gate against their contracts. Results-only launches still use filesystem snapshots; file launches receive real parent variables/workspace and Git preflight. Executor and reconciler share the per-run writer gate; rollback failure waits; cleanup is pinned until writers/dependents finish. No features/options or unrelated fixes added.
2. **Anti-green-theater:** reviewed the real-root persisted-output and on-disk pruning assertions. Temporarily removed only `gatherReconciler.Watch(groupSweepCtx)`: the all-succeed test failed at the snapshot/task-directory removal sink, and the cancelled-parent test failed at prompt gather settlement. Restored the subscription before further checks. The tests therefore detect absent production wiring, not merely constructor calls or canned input.
3. **Code quality:** checked neighboring spawn wait/recovery, launcher cleanup and workspace-retention conventions. No cleanup was necessary. Reran task-focused race tests and the real-root file suite after restoring the mutation; green. Only this plan's completion evidence changes.
