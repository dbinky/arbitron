# Paired Reviews — Parallel Workflow Gathering

Phase 2 of the loop: runs only once every single-system box in
`docs/reference/focus-areas.md` is ticked. Each row is one **seam** between two
behaviours that (1) share persisted state, a wire contract, an event, a flag or an
ordering constraint, (2) were built by different plan tasks or phases, and (3) would
fail **silently**: each side green alone, only the combination wrong. One row, one
approval, covering both directions. Area numbers refer to `focus-areas.md`.

The reviewer follows TRACE and shows that BREAK cannot happen, with a test that goes
red or a cited guard at `file:symbol`. Rows are ordered highest risk first.

Plan: **13 areas, 23 seams**, with **55** other area pairs classified below.

## Paired Integration Reviews

| # | Paired Review | Seam | Risk | Status |
|---|---|---|---|---|
| 1 | Area 6 parent cancellation ↔ Area 4 roster launch | SHARED: the parent's state and the child set that `cascadeCancel` lists via `ListChildren` while `group_launcher.go:launchAll` is still calling `StartGroupTask` (P2-T12 vs P2-T8) · TRACE: start a 3-task group, block the launcher after task 1 is enqueued, call `Engine.Cancel(parent)`, release the launcher, then read every child's state and the roster · BREAK: tasks 2 and 3 are created after the cascade listed children and run until the 1-minute sweep (`StartGroupTask` does not refuse a cancelled parent; `launchAll` never re-checks) | high | [x] |
| 2 | Area 4 roster launch ↔ Area 1 decide and continue once | SHARED: the `group_runs` launch barrier (`launching` to `running`) and roster `launch_state`; register all tasks before decisions are enabled (P2-T8 vs P2-T10/T11) · TRACE: `first_success` + `cancel_unfinished` group where task 1 succeeds before task 3 is launched; follow `GroupLauncher.Launch` and `GroupCoordinator.evaluateOnce` to the roster and the cancel calls · BREAK: a decision is applied before the barrier opens, so a task launched after the applied cancellation runs uncancelled, or the decision freezes a roster missing late tasks | high | [x] |
| 3 | Area 5 task ref finalization ↔ Area 2 file gather | SHARED: `group_tasks.finalized_ref` and `refs/arbitron/tasks/<id>`, written before `SetCompleted` and consumed as the merge input (P3-T5 vs P3-T6) · TRACE: files-mode task completes, `FinalizeTerminal` writes the ref, gather merges it into the parent; repeat with the task directory removed before finalize, and with `Engine.Retry` on a failed task while the run is running · BREAK: a vanished directory finalizes as an empty tree and the gather applies a delete-everything delta to the parent, or a retried task overwrites an eligible ref the gather then merges | high | [x] |
| 4 | Area 1 decide and continue once ↔ Area 2 file gather | SHARED: `group_runs.decision` (`Outcome`, `WinnerKey`), `actions_applied`, and the `decided` to `gathering` to `done` state handoff; the coordinator skips finalizing files runs (P2-T10/T11 vs P3-T6) · TRACE: files-mode `first_success` decides, coordinator applies unfinished actions, parent re-enters and `continueFileGather` reads the decision; then a dead parent settles the same run · BREAK: gather starts before unfinished actions are applied (executor does not check `ActionsApplied`), merges a non-winner, or `settleUnderDeadParent` writes a results-shaped final output for a files run that Retry later replays | high | [x] |
| 5 | Area 4 isolated starting copies ↔ Area 5 task ref finalization | SHARED: `group_runs.workspace_snapshot`, the one baseline both `ForkTask` and `FinalizeTask` diff against (absolute path in results mode, backing UUID in files mode) (P2-T5/P3-T2 vs P3-T5) · TRACE: dirty and untracked parent, files-mode launch snapshots and forks, task edits one file, finalize, inspect the ref's delta against the baseline · BREAK: finalize diffs against a different or missing baseline, so the delta carries the whole starting area or drops the task's edits, and the gather applies it without error | high | [x] |
| 6 | Area 2 file gather ↔ Area 3 conflict resolution | SHARED: `group_runs.gather_json` (blind replace while state is not done), the session identity `(groupRunID, mergeIndex, attempt)` and `WorkingArea.ConflictPaths` as the verification truth (P3-T6 vs P3-T8) · TRACE: conflicted merge opens a session, operator ends it while the executor re-enters, read `gather_json` and the next merge index; repeat with the resolver aborting the merge in staging · BREAK: `OnResolutionSessionEnded` (outside `gatherGate`) and the executor's persist overwrite each other's outcome, a stale session from an earlier merge is accepted, or an aborted merge is committed as resolved and the task's changes are lost | high | [x] |
| 7 | Area 3 conflict resolution ↔ Area 7 group recovery | SHARED: `gather_json` attempt identity/end/outcome and node-execution `llm_conversation` end reason; recovery assumes the persisted session is authoritative, while live completion assumes its report update survives recovery (P3-T8/T9 vs P3-T10) · TRACE: persist an open attempt, interleave live end/abandon with `group_gather_reconciler.go:RecoverRun` / `restore` / `abort`, then inspect the exact attempt, parent state and report; `restore` calls `group_resolution.go:OnResolutionSessionEnded` while holding the gather gate · BREAK: a stale writer erases abandon/end or rollback state, the wrong attempt is woken, or adding callback locking deadlocks recovery through reentrant acquisition | high | [x] |
| 8 | Area 7 group recovery ↔ Area 2 file gather | SHARED: the in-process `gatherGate`, `gather_json` merge progress and applied marker, and the startup ordering claim that recovery runs before the queue admits work (P3-T10 vs P3-T6) · TRACE: crash after `Apply` before `SetFinalOutput`, restart: `gatherReconciler.Reconcile` and a requeued parent's `continueFileGather` both reach the run · BREAK: the delta is applied twice, the reconciler rolls back while the executor finishes, or a completed resolution is lost | high | [x] |
| 9 | Area 2 file gather ↔ Area 6 parent cancellation | SHARED: the parent's terminal state as the abort signal; `continueFileGather` returns `context.Canceled` and leaves the abort and rollback to `GroupGatherReconciler` (P3-T6/T10 vs P2-T12) · TRACE: cancel the parent during `gathering` and during `resolving_conflicts`, follow `Watch`/sweep to `abort`, read the parent's working area bytes and modes and its state · BREAK: the parent area keeps partially applied changes, the cancelled parent is woken through the gather route, or a paused parent is never aborted nor resumed | high | [x] |
| 10 | Area 1 decide and continue once ↔ Area 6 parent cancellation | SHARED: roster `cancel_requested`, live status and the 019 terminal receipts; both the coordinator (`cancelOpenTasks`) and the cascade call `Engine.Cancel` (P2-T10 vs P2-T12) · TRACE: `cancel_unfinished` decision cancels a task that just completed; separately cancel the parent while a decision is pending, then read the frozen output and the parent's state · BREAK: cancelling a just-completed task now stamps subtree intent and kills its unrelated fire-and-forget descendants, a cancelled parent is requeued, or cancellation-requested is frozen as confirmed `cancelled` | high | [x] |
| 11 | Area 4 isolated starting copies ↔ Area 2 file gather | SHARED: the reserved `workspace` variable: the launcher reseeds it to each task's fork, and a nested gather applies to the parent's `workspace` (P2-T6/T7/T8 vs P3-T6) · TRACE: files group nested inside a branch task; the inner group gathers, the outer task finalizes, the outer group gathers · BREAK: the inner gather writes into the outer parent's directory instead of the enclosing task's fork, bypassing the outer group's eligibility | high | [x] |
| 12 | Area 2 file gather ↔ Area 8 gather panel | SHARED: `GatherReport` (tasks, attempts, conflict paths, rollback status, `Normalized()` arrays) hydrated into the panel (P3-T4/T6 vs P4-T2/T8) · TRACE: gather with one conflicted and one hidden task, then rollback failure; read graph-state as a viewer who cannot see the hidden task and render `GroupPanel` · BREAK: hidden tasks' conflict paths and attempts leak through `Gather.Tasks`/`Attempts`/`FinalOutput.gather`, a rollback-failed suspension is shown as ordinary waiting, or a non-normalized report crashes the panel | high | [x] |
| 13 | Area 2 file gather ↔ Area 9 subtree stream | SHARED: parent-scoped `group.*` event names/source IDs and task-derived payloads; gather/resolution emitters assume parent visibility permits delivery, while the subtree bridge authorizes only the source (P3-T6/T8 vs P4-T4/T5/T6) · TRACE: run a real files gather through conflict and completion with a socket on its visible parent, including a workflow task hidden from that viewer; follow `group_resolution.go:StartResolution` and `group_gatherer.go:publish` through `subtree_events.go:route` to actual frames and client refetch · BREAK: parent-authorized frames disclose the hidden task's conflict paths or resolver content, or mismatched type/source leaves the gather stale | high | [x] |
| 14 | Area 3 conflict resolution ↔ Area 9 subtree stream | SHARED: interactive tokens, history hydration and `interactive.*` controls (including `interactive.abandon`) carried on the subtree socket with explicit target instance ids (P3-T9 vs P4-T5) · TRACE: subtree socket on a grandparent while a nested group's parent is in a resolution session; send end and abandon with the target id; reconnect mid-session · BREAK: a control is routed to whichever child last emitted, history for the descendant session is never hydrated, or a viewer without access to the session parent receives its tokens | high | [x] |
| 15 | Area 7 group recovery ↔ Area 4 roster launch | SHARED: a `group_runs` row left in `launching` with reserved child IDs, forked task directories and a results-mode `.group-snapshots/<run>` snapshot (P2-T8 vs P2-T10/P3-T10 recovery) · TRACE: crash between `StartGroupTask` calls, restart, follow `settleUnderDeadParent`/`cancelOpenTasks` to the reserved children and the filesystem · BREAK: a reserved child keeps running under a failed parent, or the snapshot and forked directories are never released (`PruneRun` skips results runs and `Sweep` inventories only `.group-backing`) | med | [x] |
| 16 | Area 11 MCP authoring ↔ Area 3 conflict resolution | SHARED: `resolverProfile` and the resolver prompt override path, validated in the author's scope and resolved at runtime in the run owner's scope (P1-T5 vs P3-T7/T8) · TRACE: admin edits a shared workflow with a prompt file only the admin can see, another user runs it to a conflict, follow `group_resolver_prompt.go` loading · BREAK: validation passes while runtime silently falls back to the embedded default prompt or fails the resolution, with no authoring feedback | med | [x] |
| 17 | Area 4 roster launch ↔ Area 8 live run view | SHARED: branch-body ownership: the launcher runs a branch instance from its entry over the parent snapshot; `graph_state.go:contextNodeIDs` and `aggregateState.ts:resolveNodeContexts` attribute body nodes to that instance (P2-T7 vs P4-T1/T8) · TRACE: open a running branch task from a hierarchy row or task card as the route root and as a nested context; compare node states with the branch instance's cursor and trace · BREAK: the branch's own body renders pending (group panel skipped, `instanceId` null), or a body node's state is taken from the parent | med | [x] |
| 18 | Area 1 decide and continue once ↔ Area 8 gather panel | SHARED: `group_runs.state`, `continuation_delivered`, roster `blocking`, `cancel_requested`, `live_status`, and the frozen `decision` read by `waitingReasons`/`panelFor` (P2-T10 vs P4-T2/T8) · TRACE: `first_success` with `finish_independently`: winner decided, losers keep running, continuation delivered; read the panel at each step and after a late loser completes · BREAK: the panel shows ready while the continuation is undelivered (done hides "Waiting on"), a stale `live_status` shows unknown forever, or a late completion alters the frozen gathered status | med | [x] |
| 19 | Area 3 conflict resolution ↔ Area 8 gather panel | SHARED: `ResolutionSessionRef{groupRunId, mergeIndex, attempt, instanceId, nodeId}` persisted in `GatherReport.ActiveSession` and the `llm_conversation` group-session keys (P3-T8 vs P4-T2/T8) · TRACE: open a conflict session, load the run view, click "Open resolution session", end then reopen a fresh attempt · BREAK: the session button disappears (ref instance is not the parent, or the node execution is missing), or it opens an earlier attempt's closed session | med | [x] |
| 20 | Area 1 decide and continue once ↔ Area 9 subtree stream | SHARED: `group.*` events from `publishGroupEvent` (coordinator and launcher), published on the parent id, and the client refresh rule on the `group.` prefix (P2-T8/T10 vs P4-T5/T6) · TRACE: real coordinator decision on a nested group; a socket subscribed at the grandparent and one at the intermediate parent; inspect the frames and the refetch · BREAK: the decision event is published on a child id or under a non-`group.` type so no view refreshes until polling, or a sibling subtree receives it | med | [x] |
| 21 | Area 8 live run view ↔ Area 9 subtree stream | SHARED: subtree activity (server `subtreeActivity` from graph-state) versus client activity (`useGraphState` subscribes while `state.active`), and the refetch-on-event plus polling contract (P4-T3 vs P4-T5/T6) · TRACE: run a group to completion with an independent task still running; then fail the initial graph-state fetch · BREAK: the server closes the socket while the client still shows active (or the client stops subscribing while descendants run) so the view freezes, or a failed initial fetch reconnect-loops every 3s | med | [x] |
| 22 | Area 12 queue discoverability ↔ Area 8 live run view | SHARED: hierarchy markers built from the parent's graph-state (`useRunHierarchy.ts` with `buildHierarchyMarkers`) and the `RunLivePane` mounted per detail row in `QueueSection` (P4-T10 vs P4-T1/T3/T8) · TRACE: queue with a completed parent owning a running independent task and one redacted task; expand rows, open the live pane on a branch row · BREAK: a redacted task's activity marks a visible row as owning active work, markers vanish when graph-state is unreadable, or the branch row's pane renders its body as pending | med | [x] |
| 23 | Area 4 roster launch ↔ Area 9 subtree stream | SHARED: the parent link written atomically with the group tuple by `InstanceRepo.Create`, and the bridge's ancestry cache that assumes links are immutable (`instance.created` invalidation, generation counter) (P2-T1/T2/T7/T8 vs P4-T4) · TRACE: open a subtree socket on the parent before launch, launch tasks, have a task emit events immediately · BREAK: a task's events are dropped because a negative ancestry entry cached before its row existed is not invalidated, or an incomplete tuple writes no link and the task vanishes from stream, graph-state and cancel alike | med | [x] |

## No seam

Every pair of the 13 areas not paired above, with the reason. Merged-away pairs
(seed Areas 4 ↔ 8 and 9 ↔ 13) are internal to current Areas 4 and 8;
their checks are retained in those complete blocks.

### Inside one behaviour

- Area 1 ↔ Area 5: finalize reads `run.State` to skip tasks finishing after decision; ordering is internal to the task-terminal path, a check in Area 5 failure lens.
- Area 1 ↔ Area 7: the coordinator's `Reconcile` and `Evaluate` are the same component (P2-T10); recovery is Area 7 split out by entry point, and its checks live there.
- Area 1 ↔ Area 10: `spawn_wake.go:OnChildTerminal` is one dispatch path; the existing-spawn lens already proves mode exclusion and unrostered-link refusal, without needing a second behaviour to fail.
- Area 1 ↔ Area 11: both consume the one `workflow.ParseGroupConfig`; rule/behaviour compatibility is a check in Area 11 failure lens.
- Area 3 ↔ Area 6: a cancelled parent is never woken by a session end; check in Area 3 failure lens (node-addressed CAS).
- Area 4 ↔ Area 10: The BREAK is inside the shared `queue/manager.go:Enqueue` call after Create and is reproducible with either caller alone; keep its caller regression checks in Area 4.
- Area 5 ↔ Area 6: cancelled tasks are never finalized and their workspaces are retained for inspection; check in Area 5 failure lens.
- Area 5 ↔ Area 7: Watch and Reconcile are the pruner's callers, already required to judge resource retention wired-and-fed in Area 5; this is an internal hop.
- Area 5 ↔ Area 8: completed history survives worktree pruning because panels read persisted records; check in Area 8 wiring lens.
- Area 6 ↔ Area 7: the orphan sweep is in Area 6's files; group recovery under cancelled parents is covered by rows 9 and 10.
- Area 6 ↔ Area 9: a cancelled descendant must not close the parent's stream; check in Area 9 failure lens.
- Area 6 ↔ Area 10: The same mode-agnostic Cancel/orphan-sweep path owns terminal-parent and timestamp checks; ordinary spawning adds no independent failure contract.
- Area 7 ↔ Area 10: startup order of the spawn-wait reconciler before group recovery is a check in Area 7 wiring lens.
- Area 8 ↔ Area 10: graph-state includes ordinary spawn children as contexts; check in Area 8 failure lens.
- Area 8 ↔ Area 13: both use `groupLayout.ts`/`useGraphLayout.ts`; the static path's "existing layouts unaffected" is Area 13 failure lens, the live mapping is Area 8.
- Area 10 ↔ Area 12: `ListQueued` decoding group tasks as roots is Area 10 Folded in checks.
- Area 11 ↔ Area 13: the group-layout endpoint calls the same `AnalyzeGroups` and discards its errors; check in Area 13 failure lens.

- Area 4 ↔ Area 11: `executor_group.go:revalidateGroup` and authoring use the same `AnalyzeGroups`; bad ownership fails explicitly before launch. Gate-bypassing writes and late resolver errors are detectable in their owning areas alone (former seed seam 14).

### Genuinely unrelated

- Area 1 ↔ Area 3: no shared state; the decision reaches resolution only through the gather (rows 4 and 6).
- Area 1 ↔ Area 12: the queue reads instance rows and graph-state markers, not decisions.
- Area 1 ↔ Area 13: static definition only.
- Area 2 ↔ Area 10: no shared state.
- Area 2 ↔ Area 11: `gatherMode` is parsed by the shared parser; the author-scoped resolver inputs are row 16.
- Area 2 ↔ Area 12: no shared state.
- Area 2 ↔ Area 13: no shared state.
- Area 3 ↔ Area 4: no shared state.
- Area 3 ↔ Area 5: resolution works in the staging area, not on finalized refs.
- Area 3 ↔ Area 10: no shared state.
- Area 3 ↔ Area 12: no shared state.
- Area 3 ↔ Area 13: no shared state.
- Area 4 ↔ Area 12: hierarchy discovery reads ordinary persisted child links; tuple persistence/decoding is checked in Areas 4 and 10, and the distinct graph-state marker contract is row 22. No additional queue/launcher ordering is introduced.
- Area 4 ↔ Area 13: no shared state.
- Area 5 ↔ Area 9: no shared state.
- Area 5 ↔ Area 10: no shared state.
- Area 5 ↔ Area 11: no shared state.
- Area 5 ↔ Area 12: no shared state.
- Area 5 ↔ Area 13: no shared state.
- Area 6 ↔ Area 8: confirmed cancellation is an ordinary instance-state read; requested-vs-confirmed and blocking flags are the coordinator/panel contract in row 18.
- Area 6 ↔ Area 11: no shared state.
- Area 6 ↔ Area 12: a completed parent's cancel response is Area 6 Folded in checks; the queue reads the resulting states.
- Area 6 ↔ Area 13: no shared state.
- Area 7 ↔ Area 8: no shared state.
- Area 7 ↔ Area 9: no shared state.
- Area 7 ↔ Area 11: no shared state.
- Area 7 ↔ Area 12: no shared state.
- Area 7 ↔ Area 13: no shared state.
- Area 8 ↔ Area 11: no shared state.
- Area 9 ↔ Area 10: no shared state.
- Area 9 ↔ Area 11: no shared state.
- Area 9 ↔ Area 12: the queue refreshes from the system stream, not the subtree stream.
- Area 9 ↔ Area 13: no shared state.
- Area 10 ↔ Area 11: `spawn_workflow` validation is unchanged.
- Area 10 ↔ Area 13: no shared state.
- Area 11 ↔ Area 12: no shared state.
- Area 12 ↔ Area 13: no shared state.
