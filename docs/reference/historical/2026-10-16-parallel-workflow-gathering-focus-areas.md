# Focus Areas — Parallel Workflow Gathering Review Plan

Each area below is one **behaviour** the branch promises, traced from its entry
point to its sink across every layer. Pass criteria live in `RALPH.md` ("What every
review actually checks"). Each area needs **two approvals**: Review 1 on the
**wiring lens** (does the path exist and is it fed?) and Review 2 on the **failure
lens** (how does it fail?). Seams between areas are reviewed separately in
`docs/reference/paired-reviews.md`.

Branch point: `315ee04c7e2a9acc3ee64d2905c8a80156bdf893`. Composition root:
`cmd/arbitron/main.go:initDomain`. HTTP router: `adapter/http/server.go`.

Budget: `2 × 13 + 23 = 49 ≤ 500 × 0.35 = 175`; headroom **126** passes.

## Single-System Reviews

| # | Focus Area | Risk | Review 1 Status | Review 2 Status |
|---|---|---|---|---|
| 1 | a task terminal decides its group once by the completion rule and continues the parent exactly once on the right port with ordered frozen results | high | [x] | [x] |
| 2 | a decided files-mode group merges only eligible task changes into the immediate parent's working area, or rolls back and routes failure | high | [x] |[x] |
| 3 | an unresolved merge conflict pauses the gather in a resolution session and resumes only once the staging area is verifiably clean; abandon rolls back to failure | high | [x] | [x] |
| 4 | a parallel group launches one fixed roster under the parent owner with isolated starting variables and files, then waits without a queue slot | high | [x] | [x] |
| 5 | a successful files-mode task's changes are frozen into an immutable ref before its workspace is cleaned up, and task resources live exactly as long as something needs them | high | [x] | [x] |
| 6 | cancelling a parent stops all of its unfinished parallel work, through terminal intermediates and after the parent itself completed | high | [x] | [x] |
| 7 | after a crash or a missed callback, startup and periodic group recovery finish partially applied decisions, wakes and gathers without a second continuation or a second apply | high | [x] |[x] |
| 8 | the run view and queue live pane show authorized descendant node states, task progress, frozen outcomes and gather history | high | [x] | [x] |
| 9 | a run view subscribed to a subtree receives every visible descendant's events, authorized per event, and stays live while any descendant is active | high | [x] | [x] |
| 10 | existing awaited single-child spawn and fire-and-forget spawn keep working unchanged beside group tasks | med | [x] | [x] |
| 11 | an author creates, inspects, revises and deletes both group forms through MCP and invalid definitions are refused before anything persists | med | [x] | [x] |
| 12 | completed ancestors that own active independent work stay discoverable in the queue and every fixed task is reachable from hierarchy rows | med | [x] | [x] |
| 13 | the workflow review page draws fan-out, gathers, nested groups and child-workflow links from the static definition | low | [x] | [x] |

---

### Area 1 — a task terminal decides its group once by the completion rule and continues the parent exactly once on the right port with ordered frozen results

- **Promise:** `all_succeed` (wait/cancel) and `first_success` (cancel/finish independently) groups pick one outcome from durable terminal order, freeze ordered task results, apply the unfinished-task behaviour, and continue the parent exactly once through `success` or `failure`; later terminals change live status only.
- **Entry:** `domain/engine/engine.go:run` child terminal (also `failInstance`, `Cancel`, webhook timeout) → `domain/engine/spawn_wake.go:OnChildTerminal` → `domain/engine/group_dispatch.go:dispatchGroupTerminal`.
- **Sink:** `domain/engine/executor_group.go:deliver` writing the frozen output into the parent's variables and routing the `success`/`failure` port (after `adapter/sqlite/queue_repo.go:SetQueuedIfWaitingAtNode` wakes the parent).
- **Files:** `domain/engine/group_coordinator.go`, `domain/engine/group_decision.go`, `domain/engine/group_output.go`, `domain/engine/group_dispatch.go`, `domain/engine/spawn_wake.go`, `domain/engine/executor_group.go` (`reenter`, `deliver`, `recoverLostWake`), `domain/engine/group_events.go`, `domain/instance/group_run.go`, `adapter/sqlite/group_run_repo.go` (`Decide`, `SetFinalOutput`, `MarkContinuationDelivered`), `adapter/sqlite/migrations/019_parallel_groups.up.sql` (terminal triggers), `adapter/sqlite/queue_repo.go`, `adapter/sqlite/queries/queue.sql`, `adapter/sqlite/assert.go`, `cmd/arbitron/main.go` (lines ~1079, ~1086); unchanged: `domain/engine/engine.go:executeWithRetry`, `domain/engine/executor_spawn.go:childOutput`.
- **Folded in:**
  1. `adapter/sqlite/migrations/019_parallel_groups.up.sql` `group_task_terminal_update`/`_insert` triggers: `result = json_remove(variables,'$.workspace')` hardcodes the reserved-name set; check it still matches `domain/variable` `ReservedInputNames`.
  2. `adapter/sqlite/migrations/019_parallel_groups.down.sql`: check down-then-up works (seed sweep reports columns such as `subtree_cancel_requested_at` are left behind).
  3. `adapter/sqlite/assert.go`: `NodeAddressedWaker` compile-time assertion on `QueueRepo`.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `cmd/arbitron/main.go` `SetGroupTerminalHandler(groupCoord)` (~1086) is the only handler and is set before `FreezeExecutors`; `NewGroupCoordinator` receives the real `queueRepo` waker, `eng` canceller and `identityResolver` (~1079). Test: `cmd/arbitron/group_behaviors_e2e_test.go:TestGroupBehaviorsE2E_FirstSuccessCancelUnfinished`.
  2. `adapter/sqlite/migrations/019_parallel_groups.up.sql:group_task_terminal_update` — Every production terminal writer changes `instances.state` so the 019 trigger writes `TerminalSeq`, terminal status, result and failure in the same transaction; the coordinator never allocates order itself. Tests: `adapter/sqlite/group_terminal_test.go`, `cmd/arbitron/group_provenance_e2e_test.go:TestGroupE2E_FirstSuccessUsesCommitOrderWithinOneTimestamp`.
  3. `domain/engine/spawn_wake.go:OnChildTerminal` — Group tasks never fall through to the legacy spawn wake (`spawn_wake.go` dispatch before the mode check). Test: `group_dispatch_test.go:TestGroupDispatch_TerminalGroupTaskRoutesToHandlerNotLegacyWake`.
  4. `WakeParent` uses the node-addressed claim, not `SetQueuedIfWaiting`. Test: `group_coordinator_test.go:TestGroupCoordinator_WakeParentIsNodeAddressed`.
  5. The sink is asserted, not a hop: downstream nodes read `{{group.tasks.N.result}}` / `{{group.winner}}` in author order on both ports. Tests: `cmd/arbitron/group_e2e_test.go:TestGroupE2E_BranchesAllSucceedGatherResultsInAuthorOrder`, `TestGroupBehaviorsE2E_AllSucceedWaitForRemaining`.
- **Review 2 — failure lens:**
  1. `domain/engine/group_coordinator.go:evaluateOnce` — A late terminal after decision updates live status only; frozen tasks/winner and the continuation are untouched. Test: `TestGroupCoordinator_FirstSuccess_ALaterCompletionNeverChangesTheOutcome`.
  2. `domain/engine/group_decision.go:DecideGroup` — A cancelled task can never win, and cancellation *requested* is not reported as confirmed `cancelled` at the decision boundary. Tests: `TestGroupTerminal_CancelledTaskCannotBecomeWinnerAndReplayIsImmutable`, `TestGroupBehaviorsE2E_FirstSuccessCancelledTaskNeverWins`.
  3. `domain/engine/group_coordinator.go:applyActions` — Owner identity failure in `applyActions`/`cancelOpenTasks` returns before `WakeParent`; the decided parent is never continued and the sweep retries forever with no escalation (see gaps). Test: `TestGroupCoordinator_OwnerIdentityFailureLeavesActionsForTheSweep` (no bound tested).
  4. `deliver` calls `MarkContinuationDelivered` before the engine persists node output/cursor; a crash between them loses the continuation. Decide whether at-most-once is intended and test the crash point.
  5. Simultaneous terminals and the fast-task lost wake: exactly one walker continues the parent. Tests: `queue_repo_node_wake_test.go:TestSetQueuedIfWaitingAtNode_ExactlyOneWinsAgainstClaimWaitingParent`, `TestParallelGroupExecutor_FastTasksLostWakeIsClaimedInProcess`.
  6. `Engine.Retry` on a failed group-task child: the immutable-terminal trigger aborts only after `UpdateCurrentNode`/`ClearForInstance` ran, surfacing a raw SQLite error with partial side effects. No test found.
- **Seed evidence:**
  - `grep -rn SetGroupTerminalHandler --include=*.go | grep -v _test` → only `cmd/arbitron/main.go:1086`.
  - `grep -rn "NewGroupCoordinator(" | grep -v _test` → only `main.go:1079` (`groupRunRepo, instanceRepo, queueRepo, eng, identityResolver, bus, queueMgr`).
  - `domain/engine/group_coordinator.go:intake` fails closed on a terminal task without a receipt (~282); `TestGroupE2E_MissingTerminalTriggerCannotFabricateSuccess` covers it.
  - `group_coordinator_test.go:TestGroupCoordinator_IndependentTasksAreCancelledWhenTheParentDiesLater` calls `Evaluate` directly on a delivered run, a hop production never takes (see Area 6 gap).
  - group.* emitters: `group_coordinator.go` (~211, ~292, ~311, ~368, ~467) via `publishGroupEvent`.

### Area 2 — a decided files-mode group merges only eligible task changes into the immediate parent's working area, or rolls back and routes failure

- **Promise:** `results_and_files` gathers every task's finalized changes in author order after an all-succeed success, only the winner's for first-success, nothing for a failed group; a merge or apply failure restores the exact pre-gather area and routes `failure`; the final output's `outcome` follows the final route while `taskOutcome` keeps the decision.
- **Entry:** `domain/engine/executor_group.go:reenter` (decided files run) → `domain/engine/executor_group_files.go:continueFileGather` (takes `gatherGate`) → `domain/engine/group_gatherer.go` `Gather`.
- **Sink:** `adapter/git/working_area_merge.go:Apply` → `syncArea` writing the parent's working area, then `group_gatherer.go:finish` → `adapter/sqlite/group_run_repo.go:SetFinalOutput` (`gathering→done`, `continuation_ready=1`).
- **Files:** `domain/engine/executor_group_files.go`, `domain/engine/group_gatherer.go`, `domain/engine/working_area.go`, `domain/engine/group_files_ports.go`, `domain/engine/group_output.go`, `domain/instance/group_gather.go`, `adapter/git/working_area_merge.go`, `adapter/git/content.go`, `adapter/git/runner.go`, `adapter/git/procgroup_unix.go`, `adapter/git/procgroup_windows.go`, `adapter/sqlite/group_run_gather.go`, `adapter/sqlite/migrations/020_group_gather.up.sql`, `adapter/sqlite/group_run_repo.go`, `domain/engine/executor_group.go`, `cmd/arbitron/main.go` (~1073, ~1218-1219); unchanged: `domain/engine/engine.go:handleNodeError`, `domain/engine/workspace.go`.
- **Folded in:**
  1. `adapter/git/runner.go`: bounded output (`ErrOutputLimit`), process-group kill on cancel, `ErrGitUnavailable` when the binary is missing; check `group_gatherer.go` routes a missing Git before staging permanently to `failure` rather than suspending, and decide if that is intended.
  2. `adapter/sqlite/migrations/020_group_gather.down.sql`: reversible.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `main.go` `NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)` → `groupExecutor.SetGatherer` (~1218-1219), and `eng.SetGroupSupport(GroupSupportResultsAndFiles)` (~1229) only after both. Test: `cmd/arbitron/group_trigger_e2e_test.go:TestGroupTriggerE2E_Phase3RunsBothGatherModes`.
  2. `domain/engine/group_gatherer.go:parentWorkspace` — The target directory is the parent's reserved `workspace` variable (for a nested group, the enclosing task's fork). Tests: `TestFileGatherer_Provenance_ParentDirComesFromParentWorkspaceVariable`, `TestGroupFilesE2E_NestedGroupGathersIntoItsImmediateParent`.
  3. `domain/engine/group_gatherer.go:eligibleGatherTasks` — Eligibility comes from the frozen `Decision` (`WinnerKey`, `Outcome`) and roster `Position`, never live status. Tests: `TestGroupFilesE2E_FirstSuccessMergesOnlyTheWinnerAndALateLoserNeverReachesTheParent`, `TestGroupFilesE2E_FailedGroupMergesNothingAndRetainsDebuggingContent`.
  4. Merge **order** is author order on real Git, not just report order: the e2e test uses disjoint files and only proves the sorted report; order is proven only by `group_gatherer_test.go` `fakeGatherArea.mergeOrder`. Add or cite an overlapping-file real-Git test.
  5. Parent bytes **and modes** are asserted at the sink. Tests: `TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder`, `group_files_sinks_e2e_test.go:TestGroupFilesE2E_StartingCopyAndGatherPreservePermissions`.
- **Review 2 — failure lens:**
  1. `adapter/git/working_area_merge.go:Rollback` — Apply failure restores bytes and modes and routes `failure`. Test: `TestGroupFilesE2E_FailedApplyRoutesFailureAndRestoresBytesAndModes`.
  2. `domain/engine/executor_group_files.go:continueFileGather` — Every gather error other than rollback failure is returned as a node error from `continueFileGather`, so a `skip`/`route` ErrorBehavior continues past a half-applied gather with no frozen output (validation does not restrict ErrorBehavior on `parallel_group`). No test found.
  3. `domain/engine/group_gather_reconciler.go:restore` — Rollback failure suspends the parent; but the parent sits in plain `waiting`, no notification, retried only by the sweep. Test: `…RollbackFailureSuspendsUntilRepaired` (fake area only).
  4. A crash after `Apply` but before `SetFinalOutput` resumes without applying twice. Tests: `TestFileGatherer_ResumeAfterApplyBeforeFinalOutput…`, `TestGather_InterruptedApplyThenRestartRestoresBytesAndModes`.
  5. Dirty/untracked parent state, deletion, rename, binary, mode and symlink changes, files outside `repoPath`. Tests: `TestGather_AuthorRepositoryMetadataUntouchedAndDirtyStateRestored`, `TestGather_DeletionRenameBinaryModeAndSymlinkChanges`.
  6. `recoverLostWake` can run the gather up to three times per wake (`executor_group.go` ~171/180/250-264); on rollback failure that repeats rollback attempts and events.
- **Seed evidence:**
  - `grep -rn "SetGatherer(" | grep -v _test` → only `main.go:1219`; a nil gatherer returns `ErrGroupExecutionUnsupported` (`executor_group.go` ~107, ~195), not a silent no-op.
  - `gatherGate` is an in-process mutex map (`executor_group_files.go:21`); `NewGroupGatherReconciler` receives `groupExecutor` as the locker (`main.go:1224-1227`), so both writers share it.
  - `executor_group.go` decided→gathering transition (~192) does not check `ActionsApplied`, unlike `group_coordinator.go:WakeParent` (~495).

### Area 3 — an unresolved merge conflict pauses the gather in a resolution session and resumes only once the staging area is verifiably clean; abandon rolls back to failure

- **Promise:** a conflicted merge persists `resolving_conflicts`, runs an autonomous attempt with the configured agent-kind `resolverProfile` and prompt, verifies the real staging index, continues automatically if clean, otherwise keeps an interactive session open (MCP/WS/HTTP/UI) with a notification; ending re-verifies, abandon rolls back and routes `failure`; a cancelled parent is never resurrected.
- **Entry:** `domain/engine/group_gatherer.go:mergeLoop` (merge not clean) → `openResolution` → `domain/engine/group_resolution.go:StartResolution`.
- **Sink:** `group_gatherer.go:reenterResolution` → `adapter/git/working_area_merge.go:CommitResolution` → continue merging and `finish(success)`; or `group_gatherer.go` abort → `Rollback` → `finish(failure)`; plus `notifications` row of type `conflict_resolution`.
- **Files:** `domain/engine/group_resolution.go`, `domain/engine/group_resolver_prompt.go`, `domain/engine/assets/group_resolver_prompt.md`, `domain/engine/conversation.go` (`SetGroupResolution`, `openGroupSession`, `completeGroupSession`, `EndSession`, `AbandonSession`), `domain/engine/executor_interactive.go`, `domain/engine/group_gatherer.go`, `adapter/git/working_area_merge.go` (`ConflictPaths`, `CommitResolution`), `domain/notification/notification.go`, `adapter/mcp/tools_interactive.go`, `adapter/http/interactive_handler.go`, `adapter/http/stream_handler.go` (`interactive.abandon`), `adapter/http/server.go`, `dashboard/src/components/InteractivePanel.tsx`, `dashboard/src/hooks/useInteractiveSession.ts`, `dashboard/src/components/NotificationPopover.tsx`, `cmd/arbitron/main.go` (~1202-1217); unchanged: `domain/engine/engine.go:emitNotification`, `adapter/provider/dynamic.go:DynamicProfileResolver`, `domain/engine/template_include.go:loadTemplate`.
- **Folded in:**
  1. `domain/engine/executor_interactive.go`: the succeeded-trace short-circuit now skips group-resolution conversations; check an earlier group wait trace or an earlier merge's session is never treated as completion of this one.
  2. `plugin/skills/workflow-running.md`: documents `interactive_abandon_session` and "end = verify"; check it matches the MCP tool names and semantics.
  3. `adapter/sqlite/migrations/020_group_gather.up.sql`: notification CHECK admits `conflict_resolution`.
  4. `dashboard/src/components/NotificationPopover.tsx`: icon for the new type.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `main.go` builds one `NewGroupResolutionBridge` and injects it both as the gatherer's starter and via `SetGroupResolution` on the shared conversation handler (startup fails if the handler cannot host it, ~1209-1215). Test: `TestConversationHandler_Group_UnwiredHandlerKeepsGroupsNonInteractive`.
  2. `domain/engine/group_resolution.go:StartResolution` — The resolver profile resolves through the real `ProfileResolver`/`InteractiveStreamSelector` with an agent-kind guard. Test: `group_resolution_test.go:StartResolution_ChatProfileFailsClosed`.
  3. `domain/engine/conversation.go:AbandonSession` — Abandon is reachable on every surface and reaches the real handler: MCP `interactive_abandon_session`, HTTP abandon route, WS `interactive.abandon`, dashboard button. Tests: `TestRouter_InteractiveAbandonRoute`, `cmd/arbitron/group_files_e2e_test.go:TestGroupFilesE2E_ConflictAbandon_RoutesFailureAndRestoresParentFiles` (HTTP/MCP unit tests use a mock handler).
  4. A clean autonomous attempt continues without operator end. Test: `group_files_sinks_e2e_test.go:TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator`.
  5. Dashboard shows Abandon for group sessions: `useInteractiveSession.ts:seedFromInstance` defaults the session kind to `interactive` when no history frame arrived, hiding Abandon (a hook test asserts this as intended). Decide and test.
- **Review 2 — failure lens:**
  1. `adapter/git/working_area_merge.go:ConflictPaths` — Verification is the staging truth, not model claims: `working_area_merge.go:ConflictPaths` only checks recorded paths for markers while `MERGE_HEAD` exists; a resolver running `git merge --abort`/`reset` leaves no unmerged entries and no `MERGE_HEAD`, and `CommitResolution` commits the pre-merge tree while the report says merged. No test found.
  2. `domain/engine/group_gatherer.go:reenterResolution` — Still-conflicted end stays blocked or reopens a fresh session; downstream never runs. Test: `TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved` (no e2e for "end while still conflicted").
  3. `domain/engine/group_resolution.go:OnResolutionSessionEnded` — Repeated end/sentinel/abandon callbacks are idempotent and never wake a cancelled parent. Tests: `OnEnded_IsIdempotent`, `OnEnded_NeverWakesACancelledParent`, `conversation_group_test.go:RepeatedEndIsRejected`, `TestGroupFilesE2E_ParentCancelledMidSession_AbortsAndStaysCancelled`.
  4. Notification is delivered once per attempt: `NotifyResolutionRequired` sets `notificationSent=true` even though `EmitNotification` returns nothing and only logs on failure, so a failed create is never retried. No test (the fake notifier has no error path).
  5. The gatherer's abort never calls `CloseSession`; the autonomous turn has no attempt cap or timeout; a parent cancelled during that turn is recorded as `error`, not `cancelled`.
- **Seed evidence:**
  - `grep -rn "NewGroupResolutionBridge(\|SetGroupResolution(" | grep -v _test` → only `main.go:1205`, `main.go:1217`.
  - `domain/engine/group_resolution.go:365 NotifyResolutionRequired` → `EmitNotification` (no error return) then `notificationSent=true`.
  - `adapter/git/working_area_merge.go:336 ConflictPaths` → recorded-path marker check guarded by `readMergeHead`.
  - `domain/engine/group_resolution.go:398 OnResolutionSessionEnded` read-modify-writes `gather_json` outside `gatherGate` (see paired row Area 2 ↔ Area 3).

### Area 4 — a parallel group launches one fixed roster under the parent owner with isolated starting variables and files, then waits without a queue slot

- **Promise:** variables are captured once and deep-copied per task (reserved names excluded, task inputs overlaid via the typed recursive resolver, engine-owned `workspace` reseeded); the parent's working area is snapshotted once and forked into independent task directories (results mode via the filesystem adapter, files mode via private Git backing) with Git metadata that never points back at the parent's writable refs or index; writable shared paths are rejected before launch. first entry parses the Phase 1 config and ownership, persists the complete ordered roster with reserved child IDs before any enqueue, creates branch children (parent snapshot, preset cursor) and workflow children (child visibility, owner identity, depth, priority checks), opens the decision barrier last, writes the wait trace, and parks the parent `waiting` while freeing its slot; re-entry never launches a second roster; a partial launch decides failure and cancels launched tasks.
- **Entry:** `domain/engine/executor_group.go:Execute` → `firstEntry` → `domain/engine/group_launcher.go:Launch`; preparation, snapshot/fork, roster persistence, enqueue and suspension are one path. The trigger enters through `domain/engine/engine.go:TriggerWithDepartment` / `checkGroupSupport`.
- **Sink:** the child's persisted `Variables` in `domain/engine/engine_group_task.go:StartGroupTask` (workflow form ~182, branch form ~265) and its forked directory created by `ForkTask` into `EnsureInstanceDir(childID)`, reused by `domain/engine/workspace.go:prepareWorkspace`. `adapter/sqlite/group_run_repo.go:CreateWithTasks` roster rows and child `instances` rows created by `domain/engine/engine_group_task.go:StartGroupTask` → `engine.go:createAndEnqueue` → `domain/queue/manager.go:Enqueue`; parent `waiting` via `executor_group.go:suspend` → `NotifyCapacityFreed`.
- **Files:** `domain/engine/group_task_vars.go`, `domain/variable/deepcopy.go`, `domain/variable/typed_resolve.go`, `domain/engine/group_workspace.go`, `domain/engine/group_launch_files.go`, `domain/engine/group_repo_path.go`, `domain/instance/group_snapshot.go`, `adapter/filesystem/group_workspace.go`, `adapter/filesystem/group_git_metadata.go`, `adapter/filesystem/workspace.go`, `adapter/git/working_area.go` (`Snapshot`, `ForkTask`), `adapter/git/preflight.go`, `adapter/git/metadata.go`, `domain/engine/group_launcher.go`, `domain/engine/engine_group_task.go`, `cmd/arbitron/main.go` (~1065, ~1073, ~1080-1081); unchanged: `domain/variable/variable.go` (`Snapshot` is shallow), `domain/variable/substitution.go:Resolve`, `domain/engine/workspace.go`. `domain/engine/executor_group.go`, `domain/engine/group_launcher.go`, `domain/engine/engine_group_task.go`, `domain/engine/engine.go` (`trigger(spec)`, `createAndEnqueue`, `checkGroupSupport`), `domain/engine/group_support.go`, `domain/instance/group_run.go`, `domain/instance/spawn_link.go`, `adapter/sqlite/group_run_repo.go`, `adapter/sqlite/migrations/019_parallel_groups.up.sql`, `domain/queue/manager.go`, `cmd/arbitron/main.go` (~1036, ~1079-1083, ~1229); unchanged: `cmd/arbitron/main.go:userIdentityResolver`, `domain/engine/executor_spawn.go:validateChildInputs`, `domain/engine/engine_spawn.go:ChildInputs`.
- **Folded in:**
  1. `adapter/filesystem/workspace.go:RemoveInstanceDir` now uses `removeTree`; check it handles read-only files left by forks and never follows symlinks out of the instance dir.
  2. `domain/engine/group_repo_path.go`: `repoPath` resolution and containment at launch. Test: `TestGroupFilesE2E_InvalidRepoPathFailsBeforeAnyTaskStarts`.
  3. The documented `work_dir` convention (absolute path under the workspace) is rejected pre-launch unless each task overrides it: check `adapter/mcp/authoring_guidance.go` tells authors this.
  4. `domain/queue/manager.go:Enqueue` now loads the instance and returns `started=true` if it is already running (affects every trigger and spawn caller); check a `Get` failure after `Create` committed does not fail the caller while a queued row later runs. Test: `queue/manager_enqueue_test.go:TestEnqueue_EvaluatorMayStartBetweenPersistenceAndEnqueue`.
  5. `domain/engine/group_support.go` / `engine.go:checkGroupSupport`: the staged Phase 1/2 trigger guard is now `GroupSupportResultsAndFiles`; check group-free workflows are never refused and nothing still runs a files group as results-only. Tests: `engine_group_support_test.go`.
  6. `domain/engine/engine.go` `Rerun` of a branch task reruns the parent's whole workflow (branch `WorkflowName` is the parent's); check it is refused or intended.
- **Review 1 — wiring lens:**
  1. `domain/engine/group_launch_files.go:launchWorkspace` — `main.go` `filesystem.NewGroupWorkspace(cfg.Workspace.Path)` and `gitarea.NewWorkingArea(cfg.Workspace.Path)` feed `NewGroupLauncher` and `SetFileWorkingArea` (~1065-1081); a missing file area refuses loudly (`group_launch_files.go` ~28), a nil `groupWS` with a parent workspace refuses (`group_launcher.go` ~168). Test: `TestGroupLauncher_NilGroupWorkspaceWithAParentWorkspaceRefuses`.
  2. `domain/engine/engine_group_task.go:StartGroupTask` — The child's real persisted variables and directory are asserted at the sink, not the fake starter's request (`TestGroupLauncher_EveryTaskStartsFromAnIsolatedCopy` asserts only the hop). Tests: `TestGroupLauncher_RealStarterEnqueuesComposedVariablesAndForkedFiles`, `TestGroupFilesE2E_ResultsOnlyModeCopiesStartingFilesWithoutMerging`.
  3. `domain/engine/group_launcher.go:prepareTask` — Both forms inherit the same base; a required child input may come from the base; explicit mappings are type-checked before enqueue. Test: `TestGroupE2E_WorkflowsFormRepeatsOneChildWithDistinctOrderedResults`.
  4. Linked-worktree metadata isolation in results mode (P2-T17) and files mode. Tests: `TestGroupE2E_ResultsLinkedWorktreeMetadataIsolation`, `group_provenance_e2e_test.go:TestGroupE2E_ResultsFormsIsolateLinkedWorktreeMetadata`.
  5. Files-mode snapshot is immutable and forks are independent on real Git. Test: `adapter/git/working_area_test.go:TestSnapshot_IsImmutableAndForksAreIndependent`.
  6. `cmd/arbitron/main.go:initDomain` — `main.go` `NewGroupLauncher(groupRunRepo, eng, eng, wsMgr, groupWS, identityResolver, eng, bus)` → `NewParallelGroupExecutor` → `RegisterExecutor(NodeTypeParallelGroup)` (~1080-1083), `SetGroupSupport` after registration and before `FreezeExecutors`. Test: `TestGroupTriggerE2E_Phase3RunsBothGatherModes`.
  7. `domain/engine/group_launcher.go:Launch` — The launcher's `identities` is the same `identityResolver` the coordinator and spawn executor use (~1036); branch and workflow tasks run under the parent owner's reconstructed identity. No org-mode (non-default user) test found.
  8. `domain/engine/engine_group_task.go:StartGroupTask` — Workflow-form tasks go through `triggerSpec` with the reserved ID and link stamped in the same row; branch tasks preset the cursor to the entry on the parent's snapshot. Tests: `TestStartGroupTask_WorkflowKind_CreatesTheLinkedChildUnderTheReservedID`, `TestStartGroupTask_BranchKind_RunsTheParentsSnapshotFromItsEntry`.
  9. Roster before enqueue, barrier last. Test: `TestGroupLauncher_PersistsTheRosterBeforeLaunchingAndOpensTheBarrierLast`.
  10. A waiting parent holds no slot at a per-owner cap of one. Test: `TestGroupBehaviorsE2E_PerUserCapOneDoesNotDeadlock`.
- **Review 2 — failure lens:**
  1. `domain/engine/group_launcher.go:captureStartVariables` — Deep-copy independence at the composition root: mutating a nested map/array in one task never shows in a sibling or the parent. Unit tests only; no e2e found.
  2. `domain/engine/group_task_vars.go:findSharedPath` — `group_task_vars.go:pathWithin` compares path text only (no symlink resolution, e.g. macOS `/private/var`, case, embedded absolute paths); `findSharedPath` skips `map[string]string`, `[][]string`, named types and structs. No test for the alias case.
  3. `adapter/filesystem/group_git_metadata.go:CopyGroupGitMetadata` — Git lock files are checked, not held: `group_git_metadata.go` checks `*.lock` but a Git writer starting mid-copy can tear index/refs; `preflight.go` treats a `.git` pointer file (ENOTDIR) as unlocked. Test: `TestSnapshot_LockedAuthorIndexIsActionableAndLeavesNothing` (pre-existing lock only).
  4. `git.WorkingArea.ForkTask` does not check `taskDir` is outside `.group-backing`; a local-path `remote.url` in copied `.git/config` still points at the parent repository.
  5. Fork failure releases the snapshot and cleans the destination. Tests: `TestGroupLauncher_FilesMode_ForkFailureReleasesTheFileSnapshot`, `TestGroupGitMetadata_ForkMetadataErrorCleansDestination`.
  6. `domain/engine/executor_group.go:firstEntry` — Error after launch: if `nodeExecs.Create` or `suspend`'s `UpdateState(waiting)` fails after tasks started, a plain error applies the node's ErrorBehavior (design limits that to pre-creation failures); with continue/route the parent moves on while tasks run. Test: `TestParallelGroupExecutor_WaitTraceWriteFailureIsANodeError` (never checks the launched tasks).
  7. `domain/engine/group_launcher.go:failLaunch` — Partial launch failure records ordered available results, cancels launched tasks, marks unlaunched ones unfinished, routes `failure` once. Test: `TestGroupLauncher_PartialLaunchFailureDecidesFailureAndCancelsLaunchedTasks` (no e2e).
  8. `domain/engine/group_launcher.go:existingRun` — `ErrGroupRunExists` race and re-entry never relaunch. Test: `TestGroupLauncher_ExistingRunIsReturnedWithoutRelaunching`.
  9. `StartGroupTask`'s idempotent branch (`resumeExistingGroupTask`) is reachable only from tests (launcher always mints fresh IDs, coordinator never relaunches): confirm it is dead or wire the recovery that needs it.
  10. Depth limit, contract/type failure, and a child edited between `ChildInputs` and `trigger`'s reload are caught before or by the partial-launch path.
  11. `domain/queue/manager.go:Enqueue` after `domain/engine/engine.go:createAndEnqueue` must handle an evaluator winning the race and a reload failure after Create; exercise ordinary Spawn and group-task callers, inspecting the persisted child and walker count before retry. This shared enqueue implementation check owns former seam 27.
  12. `domain/engine/executor_group.go:revalidateGroup` calls the same `workflow.AnalyzeGroups` as authoring and rejects hard ownership errors before `Launch`. Exercise imported/seeded definitions and missing profile/prompt checks here; an explicit late resolver error is not a silent cross-behaviour seam (former seed seam 14).
- **Seed evidence:**
  - `grep -rn "ComposeTaskVariables(" | grep -v _test` → `domain/engine/group_launcher.go` only.
  - `grep -rn "SetFileWorkingArea(" | grep -v _test` → `main.go:1081` only.
  - Test-only exports: `git.WithRunner`, `GroupWorkspaceAdapter.SnapshotsDir`, `WorkingArea.ResolveRef` (no production caller; confirm they are test seams, not unfinished wiring).
- `grep -rn "NewGroupLauncher(\|NewParallelGroupExecutor(\|SetGroupSupport(" | grep -v _test` → `main.go:1080`, `:1082`, `:1229` only.
  - `grep -rn "StartGroupTask(" | grep -v _test` → `domain/engine/group_launcher.go:377` only.
  - `executor_group.go:firstEntry` ~154-165 returns `fmt.Errorf("parallel_group %q: write node execution record: %w", ...)` after `Launch` succeeded.
  - Critique: `group_launcher.go:Launch` calls `captureStartVariables`, `prepareTask`, snapshot/fork and `CreateWithTasks` before `launchAll`; preparation has no independent trigger, and its sink is the same child consumed by the launcher.

### Area 5 — a successful files-mode task's changes are frozen into an immutable ref before its workspace is cleaned up, and task resources live exactly as long as something needs them

- **Promise:** before a blocking files-mode task is marked completed, its whole working area (committed or not) is finalized into a ref and persisted on the roster; workspaces, snapshots and refs are retained while a gather, recovery or independently running descendant needs them and pruned only after every writer and pin releases.
- **Entry:** `domain/engine/engine.go:run` success tail → `domain/engine/group_finalize.go:finalizeGroupTask` → `FinalizeTerminal`. Cleanup entry: `domain/engine/group_gather_reconciler.go:Watch` / `Reconcile` → `domain/engine/group_prune.go:PruneRun` / `Sweep`.
- **Sink:** `adapter/git/working_area.go:FinalizeTask` (`update-ref refs/arbitron/tasks/<id>`) → `adapter/sqlite/group_run_gather.go:SetTaskFinalizedRef` (`group_tasks.finalized_ref`), then `domain/engine/group_prune.go:PruneRun` (`RemoveInstanceDir`, `ReleaseRef`, `ReleaseSnapshot`).
- **Files:** `domain/engine/group_finalize.go`, `domain/engine/group_prune.go`, `domain/engine/engine.go` (`run`, `finalizeGroupTask`, `retainGroupTaskWorkspace` call sites), `domain/engine/working_area.go`, `adapter/git/working_area.go`, `adapter/git/working_area_merge.go` (`ReleaseRef`), `adapter/git/preflight.go` (`ListSnapshots`), `adapter/sqlite/group_run_gather.go`, `adapter/sqlite/migrations/020_group_gather.up.sql`, `adapter/filesystem/group_workspace.go` (results snapshots), `domain/engine/group_launcher.go` (`releaseResultsSnapshot`), `cmd/arbitron/main.go` (~930, ~996, ~1085, ~1220); unchanged: `domain/engine/workspace.go` (`cleanupWorkspace`, `warnWorkspaceRetained`), `adapter/filesystem/workspace.go:EnsureInstanceDir`. `domain/engine/group_gather_reconciler.go` (pruner call sites).
- **Folded in:**
  1. `domain/engine/group_prune.go:warnGroupTaskWorkspaceRetained`: retained-content log line; check a retained path is never also scheduled for removal.
- **Review 1 — wiring lens:**
  1. `domain/engine/group_finalize.go:SetGroupTaskFinalizer` — `main.go` `eng.SetGroupTaskFinalizer(engine.NewGroupFileFinalizer(groupRunRepo, workingArea, wsMgr))` (~1085); a nil finalizer silently means "no ref, clean up immediately" (`group_finalize.go` ~132, ~189), so this wiring is load-bearing. Test: `TestGroupFilesE2E_AllSucceedGathersEveryTaskIntoTheParentInAuthorOrder` (ref non-empty).
  2. `domain/engine/engine.go:run` — Ordering FinalizeTask → SetTaskFinalizedRef → SetCompleted → cleanup → OnChildTerminal holds in `Engine.run`. Test: `group_finalize_engine_test.go:TestGroupFinalize_FilesTaskFinalizesAndPersistsRefBeforeCompleting` (fake area).
  3. `domain/engine/group_gather_reconciler.go:Watch` — `NewGroupResourcePruner(groupRunRepo, instanceRepo, instanceRepo, wsMgr, workingArea, workingArea)` (~1220) is invoked from both `GroupGatherReconciler.Watch` and `Sweep`. Tests: `group_gather_reconciler_test.go:WatchPrunesWhenTheLastWriterStops`, `cmd/arbitron/group_sweep_test.go`.
  4. `SetSpawnLinks(instanceRepo)` (~996) feeds `anyActiveDescendant` so a terminal task with live descendants retains its workspace. Test: `TestGroupFinalize_ActiveDescendantRetainsWorkspace` (no e2e).
  5. Results-mode snapshots under `.group-snapshots/<run>` are released by someone in every outcome: today only `releaseResultsSnapshot` from `launchAll`/`failLaunch`; `PruneRun` skips non-files runs and `Sweep` inventories only `.group-backing`.
  6. `domain/engine/group_gather_reconciler.go:Watch` / `Reconcile` must actually reach `domain/engine/group_prune.go:PruneRun` / `Sweep`; drive the last grandchild terminal and then the sweep, checking real workspace/ref retention while active and release when eligible. These are the pruner's entry points, formerly seam 25.
- **Review 2 — failure lens:**
  1. `domain/engine/group_finalize.go:FinalizeTerminal` — A vanished task directory: `FinalizeTerminal` calls `EnsureInstanceDir` (MkdirAll), so a missing directory finalizes as an empty tree, a delete-everything delta that the gather would apply to the parent. No test.
  2. `adapter/git/working_area.go:FinalizeTask` — The ref is replaceable, not immutable: `update-ref` has no old-value check and `SetTaskFinalizedRef` has no state guard; `Engine.Retry` on a failed task while the run is still running re-finalizes and overwrites it. Test: `TestFinalizeTask_RefinalizeReplacesRef` (asserts replacement as behaviour).
  3. `domain/engine/group_finalize.go:finalizeGroupTask` — Finalize or persist failure fails the task and still notifies the group; a task finishing after decision is not finalized. Tests: `FinalizeFailureFailsTaskAndStillNotifiesGroup`, `PersistFailureFailsTask`, `MissingSnapshotHandleFailsTask`, `TaskFinishingAfterDecisionIsNotFinalized`.
  4. Results-mode workspaces retained only for active descendants must eventually be released; no pruner handles them. Files-mode workspaces with no applied gather are intentionally retained for debugging by P3-T10: verify the retention log and snapshot release, without inventing a deletion requirement.
  5. Prune never removes a workspace or ref while an independent descendant still writes or a recovery still needs it. Tests: `group_prune_test.go` (fakes), `TestSnapshot_ReplacementAndFinalizedRefSurviveLiveAreaRemoval`.
- **Seed evidence:**
  - `grep -rn "SetGroupTaskFinalizer(\|NewGroupResourcePruner(" | grep -v _test` → `main.go:1085`, `main.go:1220`.
  - `domain/engine/group_finalize.go:96` `dir, err := f.workspaces.EnsureInstanceDir(inst.ID)` then `f.area.FinalizeTask(ctx, run.WorkspaceSnapshot, inst.ID, dir)`.
  - `domain/engine/group_prune.go:81` returns early for non-files runs.

### Area 6 — cancelling a parent stops all of its unfinished parallel work, through terminal intermediates and after the parent itself completed

- **Promise:** cancelling a parent stops every unfinished descendant: nested groups, branch and workflow tasks, and non-blocking independent tasks, including those below completed intermediates and after the parent itself completed, without reopening the parent's outcome; the group records cancellation and never routes success.
- **Entry:** `adapter/http/instance_handler.go:Cancel` and `adapter/mcp/tools_instance.go:instanceCancelHandler` → `domain/engine/engine.go:Cancel`.
- **Sink:** every descendant `instances.state='cancelled'` via `cancelAuthorized` → `InstanceRepo.UpdateState` (group tasks also get their roster receipt from the 019 trigger); terminal parents stamped `subtree_cancel_requested_at` for the orphan sweep.
- **Files:** `domain/engine/engine.go` (`Cancel`), `domain/engine/cancel_cascade.go`, `domain/engine/orphan_reconciler.go`, `adapter/sqlite/instance_repo.go` (`ListChildren`, `MarkSubtreeCancelRequested`, orphan SQL), `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/migrations/019_parallel_groups.up.sql` (`subtree_cancel_requested_at`), `domain/instance/spawn_link.go`, `domain/engine/group_coordinator.go` (`settleUnderDeadParent`, `cancelOpenTasks`), `domain/engine/group_gather_reconciler.go` (abort on cancelled parent), `cmd/arbitron/main.go` (~996, ~1314-1316); unchanged: `domain/engine/engine.go:cancelAuthorized`, `adapter/http/instance_handler.go:mapEngineError`.
- **Folded in:**
  1. `adapter/http/instance_handler.go:Cancel` returns `{"status":"cancelled"}` for a completed parent whose own state stays `completed`; check the response is truthful.
- **Review 1 — wiring lens:**
  1. `domain/engine/cancel_cascade.go:SetSpawnLinks` — `main.go` `eng.SetSpawnLinks(instanceRepo)` (~996) feeds `ListChildren` and `MarkSubtreeCancelRequested`; `NoopSpawnLinks` is not used in production. Test: `cmd/arbitron/cancel_subtree_e2e_test.go`.
  2. `domain/engine/cancel_cascade.go:cascadeCancel` — The cascade crosses completed intermediates and reaches independent tasks after parent completion. Tests: `TestGroupBehaviorsE2E_ParentCancellationCascadesThroughNestedGroups`, `TestGroupBehaviorsE2E_CancellingCompletedParentStopsIndependentTask`, `TestGroupBehaviorsE2E_CancelReachesIndependentTaskBelowCompletedBranch`.
  3. `adapter/sqlite/instance_repo.go:ListOrphanedActiveChildren` — The orphan ticker picks up stamped terminal ancestors (`NewOrphanedChildReconciler` ~1314). Test: `InterruptedTerminalParentWalkIsFinishedBySweep` (domain only; no composition-root test).
  4. The adapters reach the real engine: every composition-root test calls `ds.eng.Cancel` directly; no HTTP/MCP cancel test for a group parent.
- **Review 2 — failure lens:**
  1. `domain/engine/group_launcher.go:launchAll` — Cancel during launch: `group_launcher.go:launchAll` never re-checks the parent's state between `StartGroupTask` calls and `StartGroupTask` does not refuse a cancelled parent (see paired row Area 6 ↔ Area 4). No test.
  2. `adapter/sqlite/instance_repo.go:ListOrphanedActiveChildren` — Orphan SQL compares second-precision `created_at <= subtree_cancel_requested_at`; a child created in the same second after the stamp is swept wrongly.
  3. `domain/engine/cancel_cascade.go:cascadeCancel` — The immutable-terminal trigger aborting `UpdateState` reaches `cascadeCancel` as a generic error, not `ErrCannotCancel`, so that branch's descendants are skipped until the sweep (`cancel_cascade.go` ~95).
  4. Independent (`finish_independently`) tasks under a parent that later **fails** (not cancelled) are never stopped: the run leaves `ListUnsettled` once delivered and `failInstance` cascades nothing. Decide against the design ("recovery must clean up groups under failed parents") and test.
  5. Intent write failure fails closed; an authorization failure on an intermediate halts that branch. Tests: `TestCancelSubtree_IntentWriteFailureFailsClosed`, `domain/engine/cancel_subtree_test.go`.
  6. `domain/engine/engine.go:Cancel` and `adapter/sqlite/instance_repo.go:ListOrphanedActiveChildren` apply to ordinary spawn children too: cancel a completed parent, then create a child after its cancellation stamp within the same second; only descendants covered by the intent may be swept, and a terminal parent with no active descendants must still refuse cancellation. This mode-agnostic cancellation check owns former seam 13.
- **Seed evidence:**
  - `grep -rn "SetSpawnLinks(" | grep -v _test` → `main.go:996`.
  - `grep -rn "MarkSubtreeCancelRequested" | grep -v _test` → `domain/engine/cancel_cascade.go`/`engine.go` caller, `adapter/sqlite/instance_repo.go:784` implementation.
  - `cancelOpenTasks` (`group_coordinator.go:412`) calls `Engine.Cancel` on a task that may have just completed, which now stamps subtree intent and cancels that task's own fire-and-forget descendants.

### Area 7 — after a crash or a missed callback, startup and periodic group recovery finish partially applied decisions, wakes and gathers without a second continuation or a second apply

- **Promise:** at startup (after crash recovery and spawn-wait reconciliation) and on a periodic sweep, the coordinator settles groups whose terminal callbacks, unfinished-task actions or wakes were lost, and the gather reconciler resumes, rolls back or restores interrupted gathers and resolution sessions; nothing continues a parent twice or applies a delta twice.
- **Entry:** `cmd/arbitron/main.go:initDomain` startup pass (`groupCoord.Reconcile` ~1284, `gatherReconciler.Reconcile` ~1291) and `sweepGroupReconciler` ×2 (~1318-1319), plus `gatherReconciler.Watch` (~1322).
- **Sink:** `domain/engine/group_coordinator.go` `settleUnderDeadParent` / `WakeParent`; `domain/engine/group_gather_reconciler.go` `abort` / `restore` / `wake` / `settle`.
- **Files:** `cmd/arbitron/main.go` (`sweepGroupReconciler`, `groupReconciler`, startup block ~1281-1322, shutdown ~306), `domain/engine/group_coordinator.go` (`Reconcile`, `settleUnderDeadParent`), `domain/engine/group_gather_reconciler.go`, `domain/engine/executor_group_files.go` (`gatherGate`), `domain/engine/group_gatherer.go` (resume entry points), `domain/engine/group_resolution.go` (`findGroupSessionExec`), `adapter/sqlite/group_run_repo.go` (`ListUnsettled`), `domain/engine/engine.go` (`RecoverCrashedInstances`, `Retry`); unchanged: `domain/engine/spawn_reconciler.go`, `domain/queue/manager.go:NotifyCapacityFreed`.
- **Folded in:**
  1. `cmd/arbitron/main.go` startup comments (~1281-1289) claim recovery runs "before the queue starts admitting" work; the queue has no start gate and `NotifyCapacityFreed` already spawns `Evaluate`. Correct the comment or the ordering.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — Both reconcilers run at startup in the order crash recovery → spawn-wait → group coordinator → gather reconciler, then on the ticker. Tests: `cmd/arbitron/group_e2e_test.go:TestGroupE2E_StartupReconcileSettlesGroupAfterCrash`, `TestGroupE2E_PeriodicSweepSettlesGroupWithoutAWake`, `group_sweep_test.go:TestSweepGroupReconciler_RunsOnEveryTickAndStopsOnCancel`.
  2. `domain/engine/executor_group_files.go:TryLockGather` — The gather reconciler shares the executor's `gatherGate` (`NewGroupGatherReconciler(..., groupExecutor, ...)` ~1224-1227). Test: `TestGroupGatherReconciler_ActiveWriterBlocksSessionRestoration`.
  3. `domain/engine/group_gather_reconciler.go:handleEvent` — `Watch` handles `instance.failed`/`instance.cancelled` promptly. No direct e2e.
  4. Interrupted resolution sessions are restored from persisted records. Tests: `TestGroupFilesE2E_RestartRestoresAnInterruptedResolution`, `TestGroupFilesE2E_SweepWakesAParentWhoseSessionEndWasLost`.
  5. A restarted queued task can still win and `onSuccess` runs exactly once. Test: `TestGroupBehaviorsE2E_RestartQueuedTaskStillWinsAfterCrash`.
- **Review 2 — failure lens:**
  1. `domain/engine/engine.go:Retry` — `Engine.Retry` after a dead-parent settle: `GetByParentNode` always finds attempt 1, so retry replays the settled decision. P3-T10 explicitly requires replaying frozen failure for files-mode parents: assert that route and output shape, not a new launch. Results-mode Retry/Pause/Resume still need an explicit expectation and coverage.
  2. `domain/engine/group_coordinator.go:settleUnderDeadParent` — `settleUnderDeadParent` calls `finalize` regardless of gather mode, giving a decided files-mode run a results-shaped output (`gather.mode="results"`), which Retry would replay.
  3. `domain/engine/group_gather_reconciler.go:abort` — Crash mid-`gathering` with the parent running: crash recovery fails the parent and `abort` must restore the area. Unit tests only.
  4. The queue racing the gather reconciler at startup is guarded only by the in-process gate; `Watch` subscribes after the startup pass, so terminal events in between wait for the next tick.
  5. Crash during launch: parent failed with the run in `launching` is cleaned up by `settleUnderDeadParent`. Test: `TestGroupCoordinator_FailedParentWithAnInterruptedLaunchIsCleanedUp` (unit only).
  6. Runs stuck on identity errors or on a parent that moved past the node via ErrorBehavior/pause stay in `ListUnsettled` and are rescanned every minute forever.
  7. `domain/engine/group_gather_reconciler.go:restore` calls `domain/engine/group_resolution.go:OnResolutionSessionEnded` while `RecoverRun` holds the gather gate; live end/abandon callbacks run outside it. Concurrent recovery and completion must preserve the exact attempt and abandon intent without lost report writes or reentrant-lock deadlock (paired review).
- **Seed evidence:**
  - `grep -rn "sweepGroupReconciler(" cmd/arbitron/main.go` → ~1318, ~1319; `gatherReconciler.Watch(groupSweepCtx)` ~1322.
  - `adapter/sqlite/group_run_repo.go:408 ListUnsettled`: `state<>'done' OR continuation_delivered=0 OR actions_applied=0`.
  - `TestRecoverCrashedInstances_DoesNotDispatchGroupTasks` (`group_dispatch_test.go`).

### Area 8 — the run view and queue live pane show authorized descendant node states, task progress, frozen outcomes and gather history

- **Promise:** `GET /instances/{id}/graph-state` returns the selected instance's graph plus every descendant execution context (branch and workflow tasks, recursively) with `(instanceID, nodeID)` identity and states queued/running/waiting/paused/completed/failed/cancelled, each descendant authorized via `Engine.GetInstance` and redacted when hidden; `ExecutionView` and `RunLivePane` render and frame every active node from it. each group panel in the run views shows completion rule, unfinished behaviour, gather mode, state, ordered live task statuses, progress, blocking/independent flags, outstanding dependencies, winner, frozen gathered results and failure details, merge/resolution history and session link; live status stays separate from the frozen decision; history survives worktree pruning.
- **Entry:** `dashboard/src/routes/ExecutionView.tsx` and `dashboard/src/components/RunLivePane.tsx` → `dashboard/src/hooks/useGraphState.ts` → `adapter/http/instance_handler.go:GraphState` → `domain/engine/graph_state.go:GraphState`, including `graph_state_groups.go:buildGroupPanels` in the same response.
- **Sink:** rendered React Flow nodes in `ExecutionView.tsx` `GraphView` (with `useFollowActiveNode`) and the RunLivePane "Active now" list; server side `domain/engine/graph_state.go:GraphStateReader.GraphState` response. `dashboard/src/components/GroupPanel.tsx` and the footer/task-card rows of `dashboard/src/graph/nodes/GroupContainerNode.tsx`.
- **Files:** `domain/engine/graph_state.go`, `adapter/http/instance_handler.go` (`GraphState`, `SetGraphState`), `adapter/http/server.go` (graph-state route ~353), `adapter/sqlite/instance_repo.go` (`ListChildren`), `dashboard/src/api/types.ts`, `dashboard/src/api/client.ts`, `dashboard/src/hooks/useGraphState.ts`, `dashboard/src/graph/aggregateState.ts`, `dashboard/src/graph/liveSummary.ts`, `dashboard/src/graph/groupLayout.ts`, `dashboard/src/graph/useGraphLayout.ts`, `dashboard/src/graph/useFollowActiveNode.ts`, `dashboard/src/graph/nodes/ArbitronNode.tsx`, `dashboard/src/graph/nodes/ArbitronNode.module.css`, `dashboard/src/graph/types.ts`, `dashboard/src/graph/constants.ts`, `dashboard/src/routes/ExecutionView.tsx`, `dashboard/src/routes/ExecutionView.module.css`, `dashboard/src/components/RunLivePane.tsx`, `dashboard/src/components/RunLivePane.module.css`, `dashboard/src/components/QueueSection.tsx`, `cmd/arbitron/main.go` (~1380, ~425-430); unchanged: `domain/engine/engine.go:GetInstance`, `dashboard/src/hooks/useRefetchOnReconnect.ts`. `domain/engine/graph_state_groups.go` (`panelFor`, `taskCard`, `waitingReasons`, `redactDecision`, `redactGroupOutput`), `domain/instance/group_run.go`, `domain/instance/group_gather.go`, `adapter/sqlite/group_run_repo.go` (`ListByParent`, `hydrateGather`), `dashboard/src/components/GroupPanel.tsx`, `dashboard/src/components/GroupPanel.module.css`, `dashboard/src/graph/nodes/GroupContainerNode.tsx`, `dashboard/src/graph/nodes/GroupContainerNode.module.css`, `dashboard/src/graph/groupText.ts`, `dashboard/src/graph/aggregateState.ts` (`buildGroupLive`, `gatherActivity`), `dashboard/src/routes/ExecutionView.tsx` (panel mount ~442), `dashboard/src/components/RunLivePane.tsx`.
- **Folded in:**
  1. `adapter/dashboard/dist/*` (embedded bundle from `make ui`): check it was rebuilt from the final dashboard source (the HEAD commit `49bfc7e` claims it).
  2. `dashboard/src/graph/nodes/ArbitronNode.tsx` queued/paused/cancelled/unknown, non-blocking and hidden-dependency styles.
  3. `dashboard/src/graph/groupText.ts`: no mapping for raw waiting-reason wire strings (`waiting_for_tasks:a,b`, `cancelling:x`), which are shown verbatim.
- **Review 1 — wiring lens:**
  1. `cmd/arbitron/main.go:initDomain` — `main.go` `NewGraphStateReader(eng, nodeExecRepo, groupRunRepo, instanceRepo)` (~1380) → `routerDeps` typed-nil guard (~425-430) → `server.go` `SetGraphState` and route registered only when non-nil. Tests: `cmd/arbitron/graph_state_e2e_test.go:TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot`, `graph_state_handler_test.go:TestRouter_GraphStateRoute_RegisteredOnlyWhenReaderWired`.
  2. `domain/engine/graph_state.go:GraphState` — The Go JSON reaches the TS types unchanged: the e2e decodes into Go `engine.GraphState`, and only hand-written JSON in `client.test.ts` checks the TS side. Seed sweep found no tag mismatch; keep it pinned by a test.
  3. `dashboard/src/graph/aggregateState.ts:buildAggregateView` — `QueueSection.tsx` `renderDetail` mounts `RunLivePane` with the expanded row's id; ExecutionView passes `buildAggregateView` into `useGraphLayout`. Tests: `QueueView.livepane.test.tsx`, `ExecutionView.groups.test.tsx` ("highlights the running nodes of BOTH concurrent branches").
  4. `ExecutionContext.Layout` (sent for workflow/spawn contexts) is never read by the dashboard (only `gs.layout`), so nested groups inside child workflows are never laid out live. Wire or drop.
  5. A branch-task instance opened as the route root: `graph_state.go:contextNodeIDs` keeps only body nodes, `buildGroupPanels` skips the group node, and `aggregateState.ts:resolveNodeContexts` falls back to `instanceId=null`, rendering the body as pending; the livepane fixture models the branch root as `kind 'branch'` with no graph, masking it.
  6. `domain/engine/graph_state_groups.go:panelFor` — `ListByParent` → `hydrateGather` → `GroupPanel.Gather` is fed from persisted records, not worktrees. Test: `group_run_gather_test`, e2e frozen decision survives completion.
  7. `dashboard/src/graph/aggregateState.ts:buildGroupLive` — `buildGroupLive` feeds the footer and task cards. Test: `useGraphLayout.groups.test`.
  8. `dashboard/src/components/GroupPanel.tsx:GroupPanel` — `onOpenSession` opens the parent's linked resolution session in ExecutionView. Test: `ExecutionView.groups.test` ("waits on resolution… opens the parent's session"); RunLivePane's navigate path has no test.
  9. Live vs frozen: a former unfinished non-winner shows completed live while the gathered snapshot still says unfinished. Test: `GroupPanel.test.tsx` ("keeps the live status separate from the frozen gathered status").
- **Review 2 — failure lens:**
  1. `domain/engine/graph_state.go:walk` — Each included descendant is authorized through `Engine.GetInstance`; hidden ones are redacted (ids, definitions, results, traces) without leaking existence; a redacted context never counts as active. Tests: `graph_state_test.go`, livepane "redacted branch is a hidden dependency".
  2. `domain/engine/graph_state.go:addRedacted` — An unreadable descendant yields unknown/uncertain with a warning (Active stays true so polling continues), never pending or completed. Test: `graph_state_test.go` (verify).
  3. `domain/engine/graph_state.go:walk` — No silent 1,000-context cutoff. Test: `cmd/arbitron/subtree_stream_e2e_test.go` (>1000 children).
  4. `activeIds` (`ExecutionView.tsx` ~140-152) frames only running/waiting, while `liveSummary.activeNodesOf` includes paused; queued/paused concurrent work is not framed.
  5. Graph-state 501/failure fallback: ExecutionView dropped its per-instance WebSocket, so the legacy fallback view has no live refresh; RunLivePane never refetches after a queue-row Retry/Resume once the run went inactive. No test.
  6. `domain/engine/graph_state_groups.go:panelFor` — `panelFor` redacts hidden tasks' results and child ids in `Decision`/`FinalOutput` but not `Gather.Tasks[].ConflictPaths`, `Gather.Attempts[]` or `FinalOutput["gather"]`, so hidden tasks' file paths leak (~170-180). No test.
  7. `domain/engine/graph_state_groups.go:waitingReasons` — `waitingReasons` emits `continuation_pending` for done-but-undelivered, but `GroupPanel.tsx` (~111, `waiting = panel.state !== 'done'`) hides the whole "Waiting on" section then, so a stuck continuation is invisible. No test.
  8. `domain/engine/graph_state_groups.go:unknownPanel` — An unreadable group record renders an unknown panel (`unknownPanel`) and forces uncertain activity, never a completed gather.
  9. A task whose child row was purged lands in `hiddenKeys`, so its frozen results are redacted after completion, contradicting "keeps them after completion".
  10. Zero outstanding tasks does not imply ready (waiting on gathering/resolution/rollback failure). Test: `GroupPanel.test.tsx` ("waiting with zero outstanding tasks").
- **Seed evidence:**
  - `grep -rn "NewGraphStateReader(" | grep -v _test` → `main.go:1380`.
  - `LiveNodeState.Key()` / `NodeStateKey` used only in tests.
  - `graph_state.go:443 contextNodeIDs`, `dashboard/src/graph/aggregateState.ts:54 resolveNodeContexts`.
- `graph_state_groups.go:266 waitingReasons`, `:318 redactDecision`, `:334 redactGroupOutput`.
  - `GroupPanel.activeSession` (top level) is fed but the UI reads `gather.activeSession` (same value).
  - Critique: `aggregateState.ts:resolveNodeContexts` calls `findGroupPanel` for every ownership step; missing panel tasks turn branch nodes pending. `graph_state.go:GraphState` builds contexts and panels from the same `graphTraversal` authorization maps. The payload split cannot be reviewed independently.

### Area 9 — a run view subscribed to a subtree receives every visible descendant's events, authorized per event, and stays live while any descendant is active

- **Promise:** a WebSocket opened with `?subtree=1` streams events from the selected instance and every descendant (ancestry from persisted links), authorizing every forwarded event (no cached allow/deny), tagging each frame with its source instance, and closing only once the subtree is proven inactive; the dashboard refetches graph-state on `node.*`/`instance.*`/`group.*` events, polls while active, and resyncs on reconnect.
- **Entry:** `dashboard/src/hooks/useGraphState.ts` → `dashboard/src/hooks/useInstanceWebSocket.ts` (`{subtree:true}`) → `adapter/http/stream_handler.go:HandleWebSocket` → `adapter/http/stream_subtree.go:handleSubtree`.
- **Sink:** `subtreeWSFrame{sourceInstanceId}` written to the socket (`stream_subtree.go`), and the client's `isGraphStateRefreshEvent` debounce triggering a graph-state refetch; socket close via `closeSubtreeInactive`.
- **Files:** `domain/engine/subtree_events.go`, `adapter/http/stream_subtree.go`, `adapter/http/stream_handler.go`, `adapter/http/server.go` (`SetSubtreeStreaming` ~228), `domain/engine/group_events.go`, `dashboard/src/hooks/useInstanceWebSocket.ts`, `dashboard/src/hooks/useGraphState.ts`, `cmd/arbitron/main.go` (~1382, ~429); unchanged: `adapter/eventbus/eventbus.go` (`SubscribeAll`, drop-on-full), `domain/engine/engine.go:createAndEnqueue` (publishes `instance.created`), `dashboard/src/hooks/useRefetchOnReconnect.ts`.
- **Folded in:**
  1. `stream_handler.go` refactor "extract interactive routing and history building (no behaviour change)" (`4b1a141`): check the per-instance (non-subtree) stream behaves exactly as before.
- **Review 1 — wiring lens:**
  1. `adapter/http/stream_subtree.go:SetSubtreeStreaming` — `main.go` `NewSubtreeEventBridge(bus, eng, instanceRepo)` (~1382) → `routerDeps` → `server.go` `SetSubtreeStreaming(d.SubtreeEvents, d.GraphState, 0)`. Test: `TestRouter_SubtreeStreamingWiredFromDeps`.
  2. `dashboard/src/hooks/useGraphState.ts:isGraphStateRefreshEvent` — The dashboard opens the subtree socket and refetches on `group.*`. Tests: `useGraphState.test.ts` ("opens a SUBTREE WebSocket"), `isGraphStateRefreshEvent` test.
  3. `domain/engine/group_events.go:publishGroupEvent` — A real coordinator/gatherer-emitted `group.*` event arrives as a WS frame: `cmd/arbitron/subtree_stream_e2e_test.go` publishes synthetic events via `ssPublish`; `group_e2e_test.go:TestGroupE2E_PublishesGroupLifecycleEventsOnTheParent` observes the bus, not the socket. No real-emitter-to-frame test.
  4. Ancestry comes from persisted links and the `instance.created` invalidation clears a negative entry cached before the row existed. Test: `subtree_events_internal_test`.
- **Review 2 — failure lens:**
  1. `domain/engine/subtree_events.go:route` — Every event (root included, interactive tokens included) is re-authorized; visibility revoked mid-socket stops delivery and restored resumes. Tests: `stream_subtree_test.go:UnauthorizedDescendantDropped`, e2e `VisibilityRevocationAndRestorationOnOneSocket`.
  2. `adapter/http/stream_subtree.go:handleSubtree` — A terminal descendant does not close the parent's stream; a terminal root with active descendants keeps streaming; a silent transition is closed by the periodic recheck. Tests: `TerminalDescendantDoesNotClose`, `TerminalRootWithActiveDescendantsKeepsStreaming`, `PeriodicRecheckClosesAfterSilentTransition`.
  3. `adapter/http/stream_subtree.go:subtreeActivity` — A storage error that prevents proving inactivity keeps the socket alive with unknown state, never closing early.
  4. A dropped final event is recovered by polling and reconnect resync. Test: `useGraphState` "recovers a dropped final event".
  5. If the initial graph-state fetch fails (404/403), `state` stays null, `subscribe` stays true and the socket reconnect-loops every 3s. No test.
  6. Cost: `route` calls `GetInstance` per event (including `interactive.token`), `subtreeActivity` runs a full graph-state walk on every terminal event and every 15s.
  7. `domain/engine/group_resolution.go:StartResolution` publishes task `conflictPaths` and resolver tokens on the parent ID; `domain/engine/subtree_events.go:route` authorizes only that source. With a visible parent and hidden workflow task, inspect actual frames for hidden task data, even when the graph-state panel is correctly redacted.
- **Seed evidence:**
  - `grep -rn "NewSubtreeEventBridge(\|SetSubtreeStreaming(" | grep -v _test` → `main.go:1382`, `adapter/http/server.go:228-230`.
  - `SubtreeSubscriber.InSubtree` used in production by `stream_subtree.go:229 subtreeTargetAllowed`.
  - group.* emitters on the parent id: `group_coordinator.go`, `group_launcher.go` (~391, ~419) via `publishGroupEvent`; `group_gatherer.go` (~250, ~545, ~605, ~656) via its own `publish`; `group_resolution.go` (~342) inline `bus.Publish`.

### Area 10 — existing awaited single-child spawn and fire-and-forget spawn keep working unchanged beside group tasks

- **Promise:** `spawn_workflow` keeps exactly its two public modes; an awaited child still wakes its parent and passes `childOutput` to later steps; a fire-and-forget parent is never woken; group tasks (`spawn_mode=NULL` + group link columns, decoded as `group_task`) never match legacy spawn queries and legacy links never carry group fields.
- **Entry:** `domain/engine/executor_spawn.go` (unchanged) → `domain/engine/engine_spawn.go:Spawn` → `domain/engine/engine.go` `trigger(spec)` / `createAndEnqueue` (refactored).
- **Sink:** the parent's variables carrying `childOutput` after `domain/engine/spawn_wake.go:OnChildTerminal` → `SetQueuedIfWaiting` → `executor_spawn.go` resume via `FindChildBySpawnNode`; fire-and-forget parent untouched.
- **Files:** `domain/instance/spawn_link.go` (`SpawnMode.Valid`, `SpawnLink.Valid`), `domain/instance/instance.go`, `adapter/sqlite/instance_spawn_rows.go`, `adapter/sqlite/convert.go` (`nullStringToGroupLink`), `adapter/sqlite/instance_repo.go` (`Create`, read paths), `adapter/sqlite/queries/instances.sql`, `adapter/sqlite/migrations/019_parallel_groups.up.sql`, `domain/engine/spawn_wake.go`, `domain/engine/engine.go`, `domain/workflow/workflow.go`; unchanged: `domain/engine/executor_spawn.go`, `domain/engine/engine_spawn.go`, `domain/engine/spawn_reconciler.go`, migration `018_instance_spawn_link`.
- **Folded in:**
  1. `adapter/sqlite/queue_repo.go:ListQueued` uses `rowToInstance` with no link decode, so group tasks appear as roots there; check no consumer relies on their parent link.
- **Review 1 — wiring lens:**
  1. `adapter/sqlite/instance_spawn_rows.go:scanInstanceWithLink` — `scanInstanceWithLink` is used by `Get`, `ListRunning`, `NextEligible`, `ListWaiting*`. Test: `instance_group_link_test.go` `GroupTaskLinkRoundTrip`.
  2. `domain/engine/spawn_wake.go:OnChildTerminal` — The `OnChildTerminal` group dispatch precedes the mode check; legacy awaited children still wake with the group handler wired. Tests: `group_dispatch_test.go:LegacyWaitForChildStillWakesWithHandlerWired`, `FireAndForgetChildNeverReachesHandler`.
  3. `adapter/sqlite/instance_repo.go:FindChildBySpawnNode` — Legacy queries never match group tasks. Test: `LegacySpawnQueries_NeverMatchGroupTasks`.
  4. Sink asserted: downstream reads the child's output. Tests: `cmd/arbitron/spawn_wait_e2e_test.go:CompletedChildRoutesParentThroughSuccess`, `cmd/arbitron/spawn_e2e_test.go`.
- **Review 2 — failure lens:**
  1. `domain/engine/engine_spawn.go:Spawn` — `Engine.Spawn` and the context-link path in `TriggerWithDepartment` now accept a `SpawnModeGroupTask` link because `Valid()` admits it; such a child has no roster row and the terminal trigger aborts every terminal write, so it can never finish. No test.
  2. `adapter/sqlite/migrations/019_parallel_groups.up.sql:group_run_id` — Migration 018's `spawn_mode` CHECK is preserved and a legacy link carrying group fields is rejected at insert. Test: `LegacyLinkIgnoresGroupFields`.
  3. `adapter/sqlite/convert.go:nullStringToGroupLink` — A malformed `group_run_id` errors on read; an incomplete group tuple writes no link at all (cancel-invisible root). Tests: `GroupLinkRead_MalformedGroupRunIDIsAnError`, `IncompleteGroupLinkWritesNoLinkAtAll`.
  4. The refactored trigger pipeline (`04b772e`) preserves authorization, owner identity, visibility/department stamping and priority for ordinary triggers and spawns. Covered by the existing spawn e2e suites.
  5. `domain/engine/spawn_wake.go:OnChildTerminal` with the real group handler installed must dispatch group tasks exclusively, preserve awaited-child output, and ignore fire-and-forget wakes. `domain/engine/engine_spawn.go:Spawn` must refuse the internal group-task mode before creating an unrostered child. This single spawn-path review owns former seam 10.
- **Seed evidence:**
  - `grep -rn "SpawnModeGroupTask" | grep -v _test` → set in production only by `domain/engine/engine_group_task.go:StartGroupTask`.
  - `workflow.IsValidSpawnMode` still two modes; `executor_spawn.go` hardcodes its modes (~89, ~135).

### Area 11 — an author creates, inspects, revises and deletes both group forms through MCP and invalid definitions are refused before anything persists

- **Promise:** `workflow_group_upsert/get/delete` and group-aware `workflow_create/update/validate` (MCP and HTTP) author branches and workflow groups, nesting, task order and inputs, rules and gathering; validation (config, ports, closed body ownership, cycles, caller-scoped child contracts, resolver profile kind, prompt file, `repoPath`) rejects with structured errors before any registry or folder write.
- **Entry:** `adapter/mcp/tools_workflow_group.go` (`workflowGroupUpsertHandler`, `workflowGroupGetHandler`, `workflowGroupDeleteHandler`); `adapter/mcp/tools_workflow.go` (`workflowCreateHandler`, `workflowUpdateHandler`, `workflowValidateHandler`); `adapter/http/workflow_handler.go` (`Create`, `Update`, `Validate`).
- **Sink:** `domain/workflow/service.go` `Create`/`Update` → repository write (registry + folder); refusal via `domain/workflow/service_group.go:rejectInvalidGroups` → `GroupValidationError` (MCP structured result, HTTP 422).
- **Files:** `adapter/mcp/tools_workflow_group.go`, `adapter/mcp/tools_workflow.go`, `adapter/mcp/authoring_guidance.go`, `adapter/http/workflow_handler.go`, `domain/workflow/service.go`, `domain/workflow/service_group.go`, `domain/workflow/group_ops.go`, `domain/workflow/group.go`, `domain/workflow/group_ownership.go`, `domain/workflow/validation.go`, `domain/workflow/validation_group.go`, `domain/workflow/validation_group_children.go`, `domain/workflow/workflow.go`, `cmd/arbitron/main.go` (~807-811); unchanged: `domain/workflow/lint.go` (`nodeOnCycle`, `buildAdjacency`), `adapter/filesystem/workflow_repo.go`, `domain/samples/seeder.go`.
- **Folded in:**
  1. `adapter/mcp/authoring_guidance.go:WorkflowManifestHint`: forms, nesting, ports, rule/behaviour choices, gathering prerequisites and examples; check every example validates.
  2. `domain/workflow/workflow.go`: `NodeTypeParallelGroup` and port constants.
  3. `domain/workflow/validation_group.go:groupRelativePathProblem` does not reject Windows drive paths (`C:\x`).
- **Review 1 — wiring lens:**
  1. `domain/workflow/service_group.go:SetGroupCheckers` — `main.go` `workflowSvc.SetGroupCheckers(profileChecker, fileReader)` (~811) is the only production call; unwired fallback services fail closed with `GROUP_CHECKER_UNAVAILABLE`. Tests: `tools_workflow_group_test.go:Upsert_DefaultServiceWithoutCheckersIsRejected`, `cmd/arbitron/group_authoring_e2e_test.go:TestGroupAuthoringE2E_BranchesGroupAuthoredEntirelyThroughMCPValidatesClean`.
  2. `domain/workflow/service_group.go:ChildContracts` — Child contracts come from the caller-scoped `Service.ChildContracts` on writes and on `workflow_validate`. Test: `TestGroupAuthoringE2E_InvisibleChildRejected`.
  3. `domain/workflow/service_group.go:ValidateGroups` — The validate endpoints read `Deps.ProfileChecker`/`FileReader` while writes read the service's `groupProfiles`/`groupFiles`: two sources, identical today (main.go ~811 vs ~340/~412). Prove they cannot drift or unify them.
  4. HTTP `Create`/`Update` return 422 group errors through the production service. Test: `workflow_handler_group_test.go:TestWorkflowHandler_CreateUpdate_GroupErrorsAre422` (not composition-root).
  5. Composition-root coverage for `workflow_group_delete` and for `resolverPromptFile` against the real filesystem `FileReader`. No test found.
- **Review 2 — failure lens:**
  1. `domain/workflow/group_ownership.go:AnalyzeGroups` — Ownership rejects escaped continuations absorbed by naive reachability, sibling overlap, external incoming body edges, and a group on a cycle. Tests: `domain/workflow/group_ownership_test.go`.
  2. `domain/workflow/service_group.go:rejectInvalidGroups` — Rejected writes leave registry and folder byte-identical. Tests: `service_group_test.go:Create_InvalidGroupRejectedBeforeRegistryAndFolder`, `TestWorkflowGroupTools_Delete`.
  3. `domain/workflow/group_ops.go:DeleteGroup` — `DeleteGroup` gates only on `GROUP_*` codes, so it can persist a workflow with no entrypoint or orphaned body nodes; `UpsertGroup` re-validates after a successful write and can report checker-unavailable advisories on success.
  4. An admin's in-place shared edit resolves prompt files and child contracts in the admin's private-then-shared scope (`service_group.go` ~154-169), so a resource only the admin can see validates but fails other users at runtime.
  5. Child lookup storage failure reports `GROUP_CHECKER_UNAVAILABLE`, not "unknown child". Test: `TestServiceGroup_Create_ChildLookupFailureRejected`.
  6. `domain/samples/seeder.go` writes through `repo.Create`, and `domain/workflow/service.go:Duplicate` / `Copy` bypass `rejectInvalidGroups`; inspect each write gate directly. Invalid persisted definitions are an authoring-path gap even without starting a group (former seed seam 14).
- **Seed evidence:**
  - `grep -rn "SetGroupCheckers(" | grep -v _test` → `main.go:811`.
  - Gate bypasses: `domain/samples/seeder.go:104` `repo.Create` (seeding ~main.go:599, before ~811); `Duplicate`, `Copy`, `Rename`, `Backfill` copy manifests without re-validation.
  - `adapter/http/workflow_handler.go` `GroupLayout` (~205) discards `AnalyzeGroups` errors (see Area 13).

### Area 12 — completed ancestors that own active independent work stay discoverable in the queue and every fixed task is reachable from hierarchy rows

- **Promise:** the queue lists completed ancestors that still own active independent descendants (authorized), hierarchy rows show branch/workflow-task, group·key, non-blocking and active-summary markers, levels page past 100 tasks with load-more, and open levels refresh on group events and bounded polling.
- **Entry:** `dashboard/src/routes/QueueView.tsx` `fetchAll` (`list({activeDescendantAncestors:true, includeDismissed:true})`) → `dashboard/src/api/client.ts` → `adapter/http/instance_handler.go:List` → `adapter/sqlite/instance_repo.go:List` (`activeDescendantAncestorsCTE`).
- **Sink:** rendered rows in `dashboard/src/components/QueueSection.tsx` / `dashboard/src/components/HierarchyRows.tsx` (`RowMarkers`) and `dashboard/src/hooks/useRunHierarchy.ts` `loadMore`.
- **Files:** `dashboard/src/routes/QueueView.tsx`, `dashboard/src/routes/queueRefresh.ts`, `dashboard/src/components/QueueSection.tsx`, `dashboard/src/components/QueueTableRow.tsx`, `dashboard/src/components/QueueTableRow.module.css`, `dashboard/src/components/HierarchyRows.tsx`, `dashboard/src/components/HierarchyRows.module.css`, `dashboard/src/hooks/useRunHierarchy.ts`, `dashboard/src/graph/liveSummary.ts` (`buildHierarchyMarkers`), `dashboard/src/api/client.ts`, `adapter/http/instance_handler.go`, `domain/instance/instance.go` (`ListFilters.ActiveDescendantAncestors`), `adapter/sqlite/instance_repo.go`, `adapter/sqlite/queries/instances.sql`; unchanged: `dashboard/src/routes/topLevelRows.ts`, `adapter/http/system_stream_handler.go`.
- **Folded in:**
  1. `dashboard/src/routes/queueRefresh.ts`: refresh policy (group lifecycle events, bounded poll interval).
- **Review 1 — wiring lens:**
  1. `adapter/sqlite/instance_repo.go:activeDescendantAncestorsCTE` — Query param → filter → CTE argument order (`withArgs` before `where` args). Tests: `instance_repo_active_ancestors_test.go`, `instance_list_active_ancestors_test.go`, e2e `TestQueueAncestorsE2E_CompletedParentWithActiveChildIsDiscoverable`.
  2. `dashboard/src/routes/QueueView.tsx:fetchAll` — The fixture rejects any request other than `{activeDescendantAncestors:true, includeDismissed:true}` (load-bearing args asserted). File: `dashboard/src/routes/queueWorld.fixtures.ts`.
  3. `dashboard/src/hooks/useRunHierarchy.ts:useRunHierarchy` — Markers come from the parent's graph-state (`useRunHierarchy.ts` ~105) and `ownsActiveWork` reaches nested levels.
  4. Load-more reaches the 101st task; open levels refresh on group events and polling. Tests: `QueueView.groups.test.tsx`.
- **Review 2 — failure lens:**
  1. `adapter/sqlite/instance_repo.go:activeDescendantAncestorsCTE` — Visibility: the CTE checks the outer row and the descendant, not intermediate ancestors; prove the "owns active work" flag cannot leak a hidden descendant's existence.
  2. `dashboard/src/routes/QueueView.tsx:withAncestors` — Ancestor rows are added to Active but not removed from Completed, so a completed parent inside the window appears twice. No test.
  3. `adapter/http/instance_handler.go:List` — The ancestor query uses the default 500 limit and truncates silently.
  4. Ancestor fetch failure keeps the queue up; an unreadable aggregate leaves rows unannotated. Tests exist (`QueueView.groups.test.tsx`).
  5. Queue refresh on `group.*` rides the owner-only system stream, so department/public-visible runs refresh only on the poll (pre-existing `canSee`; see Won't Fix).
- **Seed evidence:**
  - `grep -rn ActiveDescendantAncestors --include=*.go | grep -v _test` → set only by REST (`instance_handler.go` ~440-450); MCP `instance_list` does not expose it.
  - `adapter/sqlite/instance_repo.go:645 activeDescendantAncestorsCTE`.

### Area 13 — the workflow review page draws fan-out, gathers, nested groups and child-workflow links from the static definition

- **Promise:** `WorkflowReview` renders each group as a fan-out header and gather footer with nested group boxes, ordered workflow task cards linking to child workflows, and presentation-only sink connectors, using Phase 1's ownership analysis; workflows without groups and existing loop layouts render exactly as before.
- **Entry:** `dashboard/src/routes/WorkflowReview.tsx` → `dashboard/src/api/client.ts` `workflows.groupLayout` → `adapter/http/workflow_handler.go:GroupLayout`.
- **Sink:** `GroupContainer`/header/footer/task-card nodes from `dashboard/src/graph/useGraphLayout.ts` rendered by `dashboard/src/graph/nodes/GroupContainerNode.tsx`, and the `GroupDetails` panel.
- **Files:** `dashboard/src/routes/WorkflowReview.tsx`, `dashboard/src/routes/WorkflowReview.module.css`, `adapter/http/workflow_handler.go` (`GroupLayout`), `adapter/http/server.go` (~285), `domain/workflow/group_ownership.go` (`AnalyzeGroups`), `dashboard/src/graph/groupLayout.ts`, `dashboard/src/graph/useGraphLayout.ts`, `dashboard/src/graph/nodes/GroupContainerNode.tsx`, `dashboard/src/graph/nodes/nodeTypes.ts`, `dashboard/src/api/client.ts`; unchanged: `domain/workflow/service.go:Load`, `dashboard/src/graph/loopLayout.ts`.
- **Folded in:**
  1. `dashboard/src/graph/groupLayout.fixtures.ts`: fixtures must match the real `AnalyzeGroups` output shape.
- **Review 1 — wiring lens:**
  1. `adapter/http/server.go:NewRouter` — `server.go` registers `GET /workflows/{name}/group-layout` (~285) on the production workflow service. Tests: `graph_state_handler_test.go:TestRouter_GroupLayoutRouteRegistered`, e2e `TestGroupLayoutE2E_ServesPhaseOneOwnershipThroughCompositionRoot`.
  2. `dashboard/src/graph/useGraphLayout.ts:useGraphLayout` — The layout response reaches `useGraphLayout(groups)`; header/footer select the same group node. Test: `WorkflowReview.groups.test.tsx`.
  3. `dashboard/src/graph/nodes/GroupContainerNode.tsx:GroupTaskCardNodeImpl` — Child-workflow task cards link to `/workflows/{name}`.
- **Review 2 — failure lens:**
  1. `adapter/http/workflow_handler.go:GroupLayout` — `GroupLayout` discards `AnalyzeGroups` errors and returns partial ownership; the frontend must tolerate it. Test: `parallelGroupTolerance.test.tsx`.
  2. `dashboard/src/routes/WorkflowReview.tsx:WorkflowReview` — Layout fetch error degrades to the plain graph. Test exists in `WorkflowReview.groups.test.tsx`.
  3. `dashboard/src/graph/useGraphLayout.ts:useGraphLayout` — Loops inside a body stay in their task cluster; a workflow without groups renders as before. Tests: `useGraphLayout.groups.test`, "a workflow without groups renders exactly as before".
  4. Workflow and layout are fetched separately; an edit between the two calls desynchronises them (low).
- **Seed evidence:**
  - `adapter/http/workflow_handler.go:190 GroupLayout` → `wfSvc.Load` → `workflow.AnalyzeGroups` (errors ignored ~205).
  - `toDefinitionPositions`, `definitionNodeIdOf` used in production.
