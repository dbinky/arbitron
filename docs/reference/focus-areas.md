# Focus Areas — Workflow Spawn Node Review Plan

Systematic review of the `spawn_workflow` implementation (built task by task,
each task verified in isolation). What this plan checks is whether the pieces are
**wired together** and behave correctly **as a whole**.

Pass criteria live in `RALPH.md` ("What every review actually checks"). Each
**single-system** area needs **2 independent approvals** — two separate passes
must each conclude it meets the bar; never record both in one pass. Each
**paired** review needs **1 approval**.

Area shorthand used in the paired table:

| # | Area | Primary files |
|---|---|---|
| 1 | spawn node type & mode vocabulary | `domain/workflow/workflow.go` |
| 2 | spawn author-time validation | `domain/workflow/validation.go` |
| 3 | spawn binding-kind guard | `domain/engine/kind_guard.go` |
| 4 | instance spawn-link model & parent scope | `domain/instance/spawn_link.go`, `instance.go` |
| 5 | awaited-child port (`SpawnWaitRepository`) | `domain/instance/spawn_wait.go` |
| 6 | spawn-link context carrier | `domain/instance/spawn_context.go` |
| 7 | SQLite spawn-link schema & row mapping | `adapter/sqlite/migrations/018_*`, `instance_spawn_rows.go`, `convert.go` |
| 8 | SQLite link-addressed queries | `adapter/sqlite/instance_repo.go`, `queries/instances.sql` |
| 9 | `ChildInputPreparer` port & request contract | `domain/engine/ports.go` |
| 10 | `LLMChildInputPreparer` | `domain/engine/child_input_preparer.go` |
| 11 | child input schema synthesis & contract validation | `domain/engine/child_input_contract.go` |
| 12 | `IdentityResolver` port & `userIdentityResolver` | `domain/engine/ports.go`, `cmd/arbitron/main.go` |
| 13 | `ChildSpawner` — `Engine.Spawn` / `Engine.ChildInputs` | `domain/engine/engine_spawn.go` |
| 14 | trigger-path link stamping & `sharedVisibilityFilter` | `domain/engine/engine.go` |
| 15 | spawn executor — fire-and-forget path | `domain/engine/executor_spawn.go` |
| 16 | composition-root spawn wiring | `cmd/arbitron/main.go` |
| 17 | spawn executor — wait-for-child suspension | `domain/engine/executor_spawn.go` |
| 18 | awaited-spawn resumption short-circuit | `domain/engine/executor_spawn.go` |
| 19 | child-terminal wake & terminal call sites | `domain/engine/spawn_wake.go`, `engine.go` |
| 20 | queue-manager child-terminal hook | `domain/queue/manager.go` |
| 21 | startup spawn-wait reconciler | `domain/engine/spawn_reconciler.go` |
| 22 | cancel cascade | `domain/engine/cancel_cascade.go`, `engine.go` |
| 23 | batched child counts on enriched rows | `domain/engine/enricher.go` |
| 24 | REST list parent selector & hierarchy DTO | `adapter/http/instance_handler.go` |
| 25 | MCP `instance_list` parent selector | `adapter/mcp/tools_instance.go` |
| 26 | dashboard API client & hierarchy types | `dashboard/src/api/client.ts`, `types.ts` |
| 27 | dashboard run-hierarchy state hook | `dashboard/src/hooks/useRunHierarchy.ts` |
| 28 | dashboard section-aware top-level rule | `dashboard/src/components/topLevelRows.ts` |
| 29 | dashboard hierarchy rendering | `HierarchyRows.tsx`, `QueueTableRow.tsx`, `QueueSection.tsx` |
| 30 | dashboard graph accent for `spawn_workflow` | `dashboard/src/graph/nodes/ArbitronNode.tsx` |
| 31 | embedded SPA bundle | `adapter/dashboard/dist` |
| 32 | AGENTS.md spawn vocabulary & loop-body restriction | `AGENTS.md` |

---

## Single-System Reviews

| # | Focus Area | Review 1 Status | Review 2 Status |
|---|---|---|---|
| 1 | `spawn_workflow` node type & mode vocabulary — `NodeTypeSpawnWorkflow`, `SpawnModeFireAndForget`/`SpawnModeWaitForChild`, `SpawnPortSuccess`/`SpawnPortFailure`, `IsValidSpawnMode` (empty is INVALID), `NodeConfig.ChildWorkflow`/`.SpawnMode` JSON tags (`domain/workflow/workflow.go`) | [x] | [x] |
| 2 | Spawn node author-time validation — `validateSpawnWorkflowNode` (entrypoint, child name, mode short-circuit, wait-mode `success`+`failure` required and no unnamed edge, fire-mode exactly one unnamed edge and no branch ports), plus the `NodeTypeSpawnWorkflow` chat arm and optional-alias carve-out in `validateModelProfiles` (`domain/workflow/validation.go`) | [x] | [x] |
| 3 | Spawn binding-kind guard — `expectedKindForNodeType` mapping `NodeTypeSpawnWorkflow` → `ProfileKindChat`; verify an UNMAPPED type really would no-op `assertBindingKind` (`domain/engine/kind_guard.go`) | [x] | [x] |
| 4 | Instance spawn-link model & parent scope — `instance.SpawnMode`/`.Valid()`, `SpawnLink`/`.Valid()`, `ParentScope`, `ParseParentScope`, `ChildVisibility`, `SpawnLinkRepository`; `Instance.ParentInstanceID`/`.ParentNodeID`/`.SpawnMode`, `Instance.IsRoot()`, `State.IsTerminal()`, `InstanceSummary.ParentInstanceID`, `EnrichedInstanceSummary.ChildCount`, `ListFilters.ParentScope`/`.ParentInstanceID` (`domain/instance/spawn_link.go`, `instance.go`) | [x] | [x] |
| 5 | Awaited-child port — `ChildLink`, `SpawnWaitCandidate`, `SpawnWaitRepository.FindChildBySpawnNode` / `.ListWaitingOnTerminalChild`; interface-segregation rationale still holds (`domain/instance/spawn_wait.go`) | [x] | [x] |
| 6 | Spawn-link context carrier — `WithSpawnLink` / `SpawnLinkFromContext`, private key type, sibling-of-`Origin` decision (`domain/instance/spawn_context.go`) | [x] | [x] |
| 7 | SQLite spawn-link schema & row mapping — migration `018_instance_spawn_link` up/down, column types/nullability, the parent-keyed index, `parentIDToNullString`/`nullStringToParentID`, `instance_spawn_rows.go` scan (`adapter/sqlite`) | [x] | [x] |
| 8 | SQLite link-addressed queries — `ChildCountsByParents` (+`childVisibilityClause`, empty-input short-circuit, absent≠zero), `ListActiveChildren`, `FindChildBySpawnNode`, `ListWaitingOnTerminalChild`, and the `ParentScope` clause in `List` (`adapter/sqlite/instance_repo.go`, `queries/instances.sql`) | [x] | [x] |
| 9 | `ChildInputPreparer` port & `ChildInputRequest` contract — field set, no-partial-result guarantee, replaceability by a non-LLM implementation (`domain/engine/ports.go`) | [x] | [x] |
| 10 | `LLMChildInputPreparer` — resolve → `assertBindingKind` → select → chat → parse ordering, `structuredOutputInstruction` reuse, zero-input short-circuit, `parseChildInputResponse` (code fence, prose prefix, non-object), `boundedExcerpt`, no internal retry (`domain/engine/child_input_preparer.go`) | [x] | [x] |
| 11 | Child input schema synthesis & contract validation — `childInputSchema`, `childInputPrompt`, `validateChildInputs` (missing required, present-but-null, optional absent left absent, extra keys passed through, no type coercion) (`domain/engine/child_input_contract.go`) | [x] | [x] |
| 12 | `IdentityResolver` port & `userIdentityResolver` — the redeclared-across-domain-edge twin, the schedule→neutral rename, `identity.BuildIdentity` fed by `GetByID`+`ListDepartments`, local-mode default user (`domain/engine/ports.go`, `cmd/arbitron/main.go`) | [x] | [x] |
| 13 | `ChildSpawner` — `Engine.Spawn` (link validity rejected before creation, thin delegation) and `Engine.ChildInputs` (owner-scoped `LoadVisibleFiltered`, not-found on invisible) (`domain/engine/engine_spawn.go`) | [x] | [x] |
| 14 | Trigger-path link stamping & `sharedVisibilityFilter` — link read from context and stamped into the SAME `Instance` literal as `TriggerSource`/`ScheduleID` (never a follow-up write), incomplete link ignored wholesale, the extracted filter helper's behaviour unchanged for existing callers (`domain/engine/engine.go`) | [x] | [x] |
| 15 | Spawn executor — fire-and-forget path: `Execute` mode re-check against the snapshot, `prepareAndSpawn` (parent load → identity → child inputs → guidance read+`variable.Resolve` → prepare → spawn), `spawnOutput` shape, normal result advancing the single edge (`domain/engine/executor_spawn.go`) | [x] | [x] |
| 16 | Composition-root spawn wiring — `instanceEnricher.SetSpawnLinks`, `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)`, `eng.SetSpawnLinks`, the single shared `identityResolver`, `NewChildInputPreparer`, post-construction `executors[NodeTypeSpawnWorkflow]`, and the `NewSpawnWaitReconciler` run ordered after `RecoverCrashedInstances` (`cmd/arbitron/main.go`) | [x] | [x] |
| 17 | Spawn executor — wait-for-child suspension: `executeWait` first-execution path, `assertSpawnBranchPorts` against the snapshot BEFORE any child is created, and the ordering invariant enqueue → node-exec record → `UpdateState(waiting)` → `NotifyCapacityFreed` (`domain/engine/executor_spawn.go`) | [x] | [x] |
| 18 | Awaited-spawn resumption short-circuit — `resumeFromLink`: keys on the LINK not node-exec status, still-active → wait result with no re-spawn, terminal → `success`/`failure` port (cancelled routes to failure), `childState` in the output (`domain/engine/executor_spawn.go`) | [x] | [x] |
| 19 | Child-terminal wake — `Engine.OnChildTerminal` three-part match (waiting ∧ `CurrentNodeID`==`ParentNodeID` ∧ mode==wait_for_child), best-effort/void posture, and its three engine call sites: run-loop completion, `failInstance`, `cancelAuthorized`; plus the `SetFailed`→`failInstance` consolidation at the three run-loop terminal paths (`domain/engine/spawn_wake.go`, `engine.go`) | [x] | [x] |
| 20 | Queue-manager child-terminal hook — `SetChildTerminalHook`, nil-safe `notifyChildTerminal`, invocation on the webhook-timeout `SetFailed` path in `OnTimerFired`, no `domain/engine` import (`domain/queue/manager.go`) | [x] | [x] |
| 21 | Startup spawn-wait reconciler — `SpawnWaitReconciler.Reconcile`, `SetQueued`+`NotifyCapacityFreed` per candidate, failure logged not fatal, must-run-after-`RecoverCrashedInstances` requirement (`domain/engine/spawn_reconciler.go`) | [x] | [x] |
| 22 | Cancel cascade — `Engine.Cancel` = `cancelAuthorized` then `cascadeCancel`, parent-transitions-first ordering, both spawn modes cascaded, visited-set guard, `ErrCannotCancel` treated as success, partial failure logged not fatal, intervention record per cancelled run, nil-`spawnLinks` no-op (`domain/engine/cancel_cascade.go`, `engine.go`) | [x] | [ ] |
| 23 | Batched child counts on enriched rows — `InstanceEnricher.SetSpawnLinks`, `batchChildCounts` one round trip per page, `ChildVisibility` taken from the same `listConstraint` that authorized the page, degrade-not-fail on error, the no-identity skip (`domain/engine/enricher.go`) | [ ] | [ ] |
| 24 | REST list parent selector & hierarchy DTO — `parent` query param through `instance.ParseParentScope`, `400 INVALID_PARENT` on malformed input, `summaryDTO.ParentInstanceID`, `enrichedSummaryDTO.ChildCount`, still routed through `ListEnriched` (`adapter/http/instance_handler.go`) | [ ] | [ ] |
| 25 | MCP `instance_list` parent selector — the `parent` string argument, its description, the SHARED `ParseParentScope` decode, error on malformed input, `MarshalResult` field names on the enriched rows (`adapter/mcp/tools_instance.go`) | [ ] | [ ] |
| 26 | Dashboard API client & hierarchy types — `ListInstancesParams.parent` → `?parent=`, `EnrichedInstanceSummary.parentInstanceId` / `.childCount` matching the server's JSON exactly (`dashboard/src/api/client.ts`, `types.ts`) | [ ] | [ ] |
| 27 | Dashboard run-hierarchy state hook — `useRunHierarchy` expansion state, one-request-per-level lazy fetch, per-row error confinement, error levels not cached (retry on re-expand), `invalidate` (`dashboard/src/hooks/useRunHierarchy.ts`) | [ ] | [ ] |
| 28 | Dashboard section-aware top-level rule — `topLevelRows`: suppress only when the parent is in the SAME section, self-referential link never erases its own row, pure and independently unit-tested (`dashboard/src/components/topLevelRows.ts`) | [ ] | [ ] |
| 29 | Dashboard hierarchy rendering — `HierarchyRows` recursion and depth passthrough, `QueueTableRow` expander (`aria-expanded`, `aria-label`, `stopPropagation`, indent, spacer), `QueueSection` integration with the detail panel it replaced (`HierarchyRows.tsx`, `QueueTableRow.tsx`, `QueueSection.tsx`, `*.module.css`) | [ ] | [ ] |
| 30 | Dashboard graph accent for `spawn_workflow` — the `typeColors` entry and the claim that no editor-side port rendering is required (`dashboard/src/graph/nodes/ArbitronNode.tsx`) | [ ] | [ ] |
| 31 | Embedded SPA bundle — `adapter/dashboard/dist` regenerated from the current `dashboard/src` per `AGENTS.md:28`; the committed hashed assets actually contain the hierarchy UI and the spawn accent (`adapter/dashboard/dist`) | [ ] | [ ] |
| 32 | AGENTS.md spawn vocabulary & loop-body restriction — `spawn_workflow` added to the node-type list, the one-type-two-modes note, the awaited-spawn loop-body prohibition and the fire-and-forget loop-safety carve-out (`AGENTS.md`) | [ ] | [ ] |

---

## Paired Integration Reviews

Each row: *I am reviewing the completeness, correctness, and spec alignment of
**A** as it is impacted by updates in **B**. Both should meet a stringent bar for
"done" and work together correctly.* Every row names the concrete seam.

| # | Paired Review | Status |
|---|---|---|
| 1 | 2 (validation) as impacted by 1 (node vocabulary) — seam: `IsValidSpawnMode`, `NodeConfig.SpawnMode`/`.ChildWorkflow`, `SpawnPortSuccess`/`SpawnPortFailure` | [ ] |
| 2 | 1 (node vocabulary) as impacted by 2 (validation) — same seam | [ ] |
| 3 | 3 (kind guard) as impacted by 1 (node vocabulary) — seam: `workflow.NodeTypeSpawnWorkflow` case in `expectedKindForNodeType` | [ ] |
| 4 | 1 (node vocabulary) as impacted by 3 (kind guard) — same seam | [ ] |
| 5 | 4 (link model) as impacted by 1 (node vocabulary) — seam: `instance.SpawnMode*` string values MUST equal `workflow.SpawnMode*`; two vocabularies with no compile-time link | [ ] |
| 6 | 1 (node vocabulary) as impacted by 4 (link model) — same seam | [ ] |
| 7 | 15 (fire-and-forget executor) as impacted by 1 (node vocabulary) — seam: `node.Config.SpawnMode`/`.ChildWorkflow` read and `IsValidSpawnMode` re-checked at execution | [ ] |
| 8 | 1 (node vocabulary) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 9 | 17 (wait suspension) as impacted by 1 (node vocabulary) — seam: `assertSpawnBranchPorts` over `workflow.SpawnPortSuccess`/`SpawnPortFailure` | [ ] |
| 10 | 1 (node vocabulary) as impacted by 17 (wait suspension) — same seam | [ ] |
| 11 | 30 (graph accent) as impacted by 1 (node vocabulary) — seam: the literal `spawn_workflow` key in `typeColors` must equal `NodeTypeSpawnWorkflow` | [ ] |
| 12 | 1 (node vocabulary) as impacted by 30 (graph accent) — same seam | [ ] |
| 13 | 32 (AGENTS.md) as impacted by 1 (node vocabulary) — seam: the documented node-type list and mode names must match the constants | [ ] |
| 14 | 1 (node vocabulary) as impacted by 32 (AGENTS.md) — same seam | [ ] |
| 15 | 17 (wait suspension) as impacted by 2 (validation) — seam: the two-port contract, enforced at author time and re-asserted against the snapshot | [ ] |
| 16 | 2 (validation) as impacted by 17 (wait suspension) — same seam | [ ] |
| 17 | 15 (fire-and-forget executor) as impacted by 2 (validation) — seam: the single-unnamed-edge contract the executor's normal result depends on | [ ] |
| 18 | 2 (validation) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 19 | 10 (preparer) as impacted by 3 (kind guard) — seam: `assertBindingKind(req.NodeID, NodeTypeSpawnWorkflow, binding.Kind)` — the guard GATES the LLM call | [ ] |
| 20 | 3 (kind guard) as impacted by 10 (preparer) — same seam | [ ] |
| 21 | 7 (SQLite schema) as impacted by 4 (link model) — seam: `Instance.ParentInstanceID`/`.ParentNodeID`/`.SpawnMode` ↔ the 018 columns and the row scan | [ ] |
| 22 | 4 (link model) as impacted by 7 (SQLite schema) — same seam | [ ] |
| 23 | 8 (link queries) as impacted by 4 (link model) — seam: `SpawnLinkRepository`, `ChildVisibility`, `ListFilters.ParentScope` implemented by `InstanceRepo` | [ ] |
| 24 | 4 (link model) as impacted by 8 (link queries) — same seam | [ ] |
| 25 | 14 (trigger stamping) as impacted by 4 (link model) — seam: the three link fields written onto the `Instance` literal at creation | [ ] |
| 26 | 4 (link model) as impacted by 14 (trigger stamping) — same seam | [ ] |
| 27 | 23 (child counts) as impacted by 4 (link model) — seam: `EnrichedInstanceSummary.ChildCount` + `ChildCountsByParents(ids, ChildVisibility)` | [ ] |
| 28 | 4 (link model) as impacted by 23 (child counts) — same seam | [ ] |
| 29 | 22 (cancel cascade) as impacted by 4 (link model) — seam: `SpawnLinkRepository.ListActiveChildren` | [ ] |
| 30 | 4 (link model) as impacted by 22 (cancel cascade) — same seam | [ ] |
| 31 | 24 (REST selector) as impacted by 4 (link model) — seam: `ParseParentScope`, `ListFilters.ParentScope`/`.ParentInstanceID`, `InstanceSummary.ParentInstanceID`; the narrowing-never-widening security contract | [ ] |
| 32 | 4 (link model) as impacted by 24 (REST selector) — same seam | [ ] |
| 33 | 25 (MCP selector) as impacted by 4 (link model) — seam: the same shared `ParseParentScope` decoder and `ListFilters` fields | [ ] |
| 34 | 4 (link model) as impacted by 25 (MCP selector) — same seam | [ ] |
| 35 | 8 (link queries) as impacted by 5 (awaited-child port) — seam: `FindChildBySpawnNode` / `ListWaitingOnTerminalChild` implemented by `InstanceRepo` | [ ] |
| 36 | 5 (awaited-child port) as impacted by 8 (link queries) — same seam | [ ] |
| 37 | 18 (resumption short-circuit) as impacted by 5 (awaited-child port) — seam: `ChildLink.ChildState` drives the `success`/`failure` port choice | [ ] |
| 38 | 5 (awaited-child port) as impacted by 18 (resumption short-circuit) — same seam | [ ] |
| 39 | 21 (reconciler) as impacted by 5 (awaited-child port) — seam: `SpawnWaitCandidate` from `ListWaitingOnTerminalChild` | [ ] |
| 40 | 5 (awaited-child port) as impacted by 21 (reconciler) — same seam | [ ] |
| 41 | 13 (`ChildSpawner`) as impacted by 6 (context carrier) — seam: `instance.WithSpawnLink(ctx, req.Link)` in `Engine.Spawn` | [ ] |
| 42 | 6 (context carrier) as impacted by 13 (`ChildSpawner`) — same seam | [ ] |
| 43 | 14 (trigger stamping) as impacted by 6 (context carrier) — seam: `SpawnLinkFromContext(ctx)` read inside `TriggerWithDepartment` | [ ] |
| 44 | 6 (context carrier) as impacted by 14 (trigger stamping) — same seam | [ ] |
| 45 | 8 (link queries) as impacted by 7 (SQLite schema) — seam: the 018 columns and parent-keyed index the four queries and the parent-scope clause read | [ ] |
| 46 | 7 (SQLite schema) as impacted by 8 (link queries) — same seam | [ ] |
| 47 | 8 (link queries) as impacted by 24 (REST selector) — seam: the caller-supplied `ParentScope` clause must COMPOSE with (never replace) the service-set owner/department clause | [ ] |
| 48 | 24 (REST selector) as impacted by 8 (link queries) — same seam | [ ] |
| 49 | 10 (preparer) as impacted by 9 (preparer port) — seam: `ChildInputPreparer.Prepare(ctx, ChildInputRequest)` | [ ] |
| 50 | 9 (preparer port) as impacted by 10 (preparer) — same seam | [ ] |
| 51 | 15 (fire-and-forget executor) as impacted by 9 (preparer port) — seam: the `ChildInputRequest` literal the executor builds (`NodeID`, `Inputs`, `ParentContext`, `Guidance`, `ProfileAlias`) | [ ] |
| 52 | 9 (preparer port) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 53 | 11 (contract synthesis/validation) as impacted by 10 (preparer) — seam: `childInputSchema` / `childInputPrompt` / `validateChildInputs` called from `Prepare` | [ ] |
| 54 | 10 (preparer) as impacted by 11 (contract synthesis/validation) — same seam | [ ] |
| 55 | 11 (contract synthesis/validation) as impacted by 13 (`ChildSpawner`) — seam: `workflow.InputField` produced by `Engine.ChildInputs` is the contract being synthesized/validated | [ ] |
| 56 | 13 (`ChildSpawner`) as impacted by 11 (contract synthesis/validation) — same seam | [ ] |
| 57 | 15 (fire-and-forget executor) as impacted by 12 (identity resolver) — seam: `IdentityResolver.IdentityForUser(parent.OwnerID)` → `identity.WithIdentity(childCtx)` | [ ] |
| 58 | 12 (identity resolver) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 59 | 12 (identity resolver) as impacted by 16 (composition root) — seam: ONE `userIdentityResolver` value passed to the schedule evaluator, the run-now runner, AND the spawn executor | [ ] |
| 60 | 16 (composition root) as impacted by 12 (identity resolver) — same seam | [ ] |
| 61 | 15 (fire-and-forget executor) as impacted by 13 (`ChildSpawner`) — seam: `spawner.ChildInputs(childCtx, name)` and `spawner.Spawn(childCtx, SpawnRequest{...Link})` | [ ] |
| 62 | 13 (`ChildSpawner`) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 63 | 13 (`ChildSpawner`) as impacted by 14 (trigger stamping) — seam: `Engine.Spawn` delegates to `TriggerWithDepartment`; `ChildInputs` shares `sharedVisibilityFilter` | [ ] |
| 64 | 14 (trigger stamping) as impacted by 13 (`ChildSpawner`) — same seam | [ ] |
| 65 | 14 (trigger stamping) as impacted by 15 (fire-and-forget executor) — seam: owner inheritance — parent `OwnerID` → injected identity → `triggerOwner`; child priority inherited via `SpawnRequest.Priority` | [ ] |
| 66 | 15 (fire-and-forget executor) as impacted by 14 (trigger stamping) — same seam | [ ] |
| 67 | 15 (fire-and-forget executor) as impacted by 16 (composition root) — seam: `executors[workflow.NodeTypeSpawnWorkflow] = NewSpawnWorkflowExecutor(...)` registered AFTER `NewEngine` on the map the engine holds by reference | [ ] |
| 68 | 16 (composition root) as impacted by 15 (fire-and-forget executor) — same seam | [ ] |
| 69 | 17 (wait suspension) as impacted by 15 (fire-and-forget executor) — seam: the shared `prepareAndSpawn` helper and the mode dispatch in `Execute` | [ ] |
| 70 | 15 (fire-and-forget executor) as impacted by 17 (wait suspension) — same seam | [ ] |
| 71 | 18 (resumption short-circuit) as impacted by 17 (wait suspension) — seam: `FindChildBySpawnNode` is consulted BEFORE the first-execution spawn; the link written by suspension is what resumption reads | [ ] |
| 72 | 17 (wait suspension) as impacted by 18 (resumption short-circuit) — same seam | [ ] |
| 73 | 19 (child-terminal wake) as impacted by 17 (wait suspension) — seam: `UpdateState(waiting)` + `CurrentNodeID` are exactly the two fields the wake's match condition reads | [ ] |
| 74 | 17 (wait suspension) as impacted by 19 (child-terminal wake) — same seam | [ ] |
| 75 | 20 (queue hook) as impacted by 17 (wait suspension) — seam: parent and child are enqueued under the SAME owner and share per-user capacity; `NotifyCapacityFreed` ordering is the deadlock guard | [ ] |
| 76 | 17 (wait suspension) as impacted by 20 (queue hook) — same seam | [ ] |
| 77 | 18 (resumption short-circuit) as impacted by 19 (child-terminal wake) — seam: the wake's `SetQueued` re-entry lands on `CurrentNodeID`, where the short-circuit routes by port | [ ] |
| 78 | 19 (child-terminal wake) as impacted by 18 (resumption short-circuit) — same seam | [ ] |
| 79 | 19 (child-terminal wake) as impacted by 16 (composition root) — seam: `queueMgr.SetChildTerminalHook(eng.OnChildTerminal)` | [ ] |
| 80 | 16 (composition root) as impacted by 19 (child-terminal wake) — same seam | [ ] |
| 81 | 20 (queue hook) as impacted by 19 (child-terminal wake) — seam: `notifyChildTerminal` on the webhook-timeout `SetFailed` path invokes `OnChildTerminal` across the domain edge | [ ] |
| 82 | 19 (child-terminal wake) as impacted by 20 (queue hook) — same seam | [ ] |
| 83 | 21 (reconciler) as impacted by 19 (child-terminal wake) — seam: both re-queue a waiting parent via `SetQueued` + `NotifyCapacityFreed`; the duplicate-wake no-op depends on the same waiting-state guard | [ ] |
| 84 | 19 (child-terminal wake) as impacted by 21 (reconciler) — same seam | [ ] |
| 85 | 22 (cancel cascade) as impacted by 19 (child-terminal wake) — seam: cancel-then-cascade ordering; a cascaded child's `OnChildTerminal` must find its parent already cancelled (the resurrection guard) | [ ] |
| 86 | 19 (child-terminal wake) as impacted by 22 (cancel cascade) — same seam | [ ] |
| 87 | 21 (reconciler) as impacted by 16 (composition root) — seam: `NewSpawnWaitReconciler(...).Reconcile` invoked AFTER `RecoverCrashedInstances` in `initDomain`'s startup sequence | [ ] |
| 88 | 16 (composition root) as impacted by 21 (reconciler) — same seam | [ ] |
| 89 | 22 (cancel cascade) as impacted by 16 (composition root) — seam: `eng.SetSpawnLinks(instanceRepo)` — without it `cascadeCancel` silently returns | [ ] |
| 90 | 16 (composition root) as impacted by 22 (cancel cascade) — same seam | [ ] |
| 91 | 23 (child counts) as impacted by 16 (composition root) — seam: `instanceEnricher.SetSpawnLinks(instanceRepo)` — without it every row reports 0 and no expander renders | [ ] |
| 92 | 16 (composition root) as impacted by 23 (child counts) — same seam | [ ] |
| 93 | 24 (REST selector) as impacted by 23 (child counts) — seam: `EnrichedInstanceSummary.ChildCount` → `enrichedSummaryDTO.ChildCount` (`omitempty`) | [ ] |
| 94 | 23 (child counts) as impacted by 24 (REST selector) — same seam | [ ] |
| 95 | 25 (MCP selector) as impacted by 23 (child counts) — seam: the same enriched rows marshalled by `MarshalResult`; field naming vs the REST DTO | [ ] |
| 96 | 23 (child counts) as impacted by 25 (MCP selector) — same seam | [ ] |
| 97 | 25 (MCP selector) as impacted by 24 (REST selector) — seam: REST/MCP parity on the `parent` selector, asserted by `adapter/mcp/parity_test.go` | [ ] |
| 98 | 24 (REST selector) as impacted by 25 (MCP selector) — same seam | [ ] |
| 99 | 26 (dashboard client/types) as impacted by 24 (REST selector) — seam: the `?parent=` query parameter and the `parentInstanceId` / `childCount` JSON field names | [ ] |
| 100 | 24 (REST selector) as impacted by 26 (dashboard client/types) — same seam | [ ] |
| 101 | 27 (hierarchy hook) as impacted by 26 (dashboard client/types) — seam: `api.instances.list({ parent: id })` | [ ] |
| 102 | 26 (dashboard client/types) as impacted by 27 (hierarchy hook) — same seam | [ ] |
| 103 | 28 (top-level rule) as impacted by 26 (dashboard client/types) — seam: `EnrichedInstanceSummary.parentInstanceId` is the only field `topLevelRows` reads | [ ] |
| 104 | 26 (dashboard client/types) as impacted by 28 (top-level rule) — same seam | [ ] |
| 105 | 29 (hierarchy rendering) as impacted by 26 (dashboard client/types) — seam: `childCount ?? 0 > 0` gates the expander | [ ] |
| 106 | 26 (dashboard client/types) as impacted by 29 (hierarchy rendering) — same seam | [ ] |
| 107 | 29 (hierarchy rendering) as impacted by 27 (hierarchy hook) — seam: the `RunHierarchy` interface (`isExpanded`/`childrenOf`/`isLoading`/`errorOf`/`toggle`/`invalidate`) consumed by `HierarchyRows` and `QueueSection` | [ ] |
| 108 | 27 (hierarchy hook) as impacted by 29 (hierarchy rendering) — same seam | [ ] |
| 109 | 29 (hierarchy rendering) as impacted by 28 (top-level rule) — seam: `topLevelRows(sorted)` is what `QueueSection` passes as `HierarchyRows.rows` | [ ] |
| 110 | 28 (top-level rule) as impacted by 29 (hierarchy rendering) — same seam | [ ] |
| 111 | 31 (embedded bundle) as impacted by 29 (hierarchy rendering) — seam: `adapter/dashboard/dist` must be rebuilt from the current `dashboard/src` (`make ui`, `AGENTS.md:28`) | [ ] |
| 112 | 29 (hierarchy rendering) as impacted by 31 (embedded bundle) — same seam | [ ] |
| 113 | 31 (embedded bundle) as impacted by 30 (graph accent) — seam: the `spawn_workflow` accent must be present in the committed hashed asset, not only in source | [ ] |
| 114 | 30 (graph accent) as impacted by 31 (embedded bundle) — same seam | [ ] |
| 115 | 32 (AGENTS.md) as impacted by 18 (resumption short-circuit) — seam: the documented awaited-spawn loop-body prohibition describes exactly the short-circuit's back-edge behaviour | [ ] |
| 116 | 18 (resumption short-circuit) as impacted by 32 (AGENTS.md) — same seam | [ ] |

---

## No seam

Combinations deliberately **not** paired, and why. An omission written down is a
decision; a silent omission is a coverage gap nobody can audit.

- **30 (graph accent) × 4–29 (every runtime/persistence area)** — `typeColors` is
  a read-only string→colour map on a non-editing visualization. No data flows
  either way; the only thing it shares with anything is the node-type string
  (paired with 1) and the built bundle (paired with 31).
- **3 (kind guard) × 4, 5, 6, 7, 8** — the guard runs entirely before any
  provider call and never touches the link model, its ports, its context carrier,
  or storage.
- **2 (validation) × 4–8, 12–14, 19–29** — author-time validation runs at workflow
  load against `wf.Nodes`/`wf.Edges`. It shares no symbol, table, or runtime hop
  with persistence, wiring, wake, cascade, or read paths. Its only runtime seams
  are the two executor paths that re-check the same contract (paired: 15, 17).
- **9, 10, 11 (input preparation) × 4, 5, 6, 7, 8, 14, 19, 20, 21, 22** — the
  preparer holds no repository, no engine and no context carrier by design
  (Phase 2's isolation claim). It meets the rest of the feature only at the
  executor (paired: 15) and the kind guard (paired: 10×3).
- **21 (reconciler) × 22–29** — startup-only; it writes queue state and is never
  rendered, listed, or cancelled through. Its seams are 5, 16, 19.
- **20 (queue hook) × 22 (cancel cascade)** — the cascade routes through
  `Engine.Cancel`/`cancelAuthorized`, never through `queue.Manager`. The hook's
  only terminal path is the webhook timeout.
- **22 (cancel cascade) × 23–29** — the cascade produces no list rows; the
  hierarchy read observes cancelled runs only through the ordinary list path
  already covered by 4×23 and 4×24.
- **12 (identity resolver) × 17–32** — it appears exactly twice in production:
  inside `prepareAndSpawn` (paired: 15) and at the composition root (paired: 16).
- **6 (context carrier) × everything except 13, 14** — a two-function context
  key with exactly one writer and one reader.
- **27, 28 (dashboard hook and rule) × 30, 31** — expansion state and the
  top-level predicate are independent of graph rendering; their build-output
  dependency is covered once through 29×31.
- **24, 25 (API surfaces) × 27, 28, 29** — the dashboard reaches the API only
  through the client module (26). Pairing the handlers directly with components
  would review a hop that does not exist.
- **32 (AGENTS.md) × everything except 1 and 18** — the file documents the node
  vocabulary and the loop-body restriction only; no other area is described there.
- **31 (embedded bundle) × 1–28, 32** — the bundle is a build artefact of
  `dashboard/src` alone. Its only seams are the two source areas whose output it
  must contain (29, 30).
- **7 (SQLite schema) × 13–29** — every consumer reaches the columns through the
  repository methods (8) or the domain aggregate (4); nothing else reads a column
  name.
- **16 (composition root) × 1–11, 17, 18, 24–32** — the root wires *constructed
  collaborators*. Areas it does not construct (vocabulary, validation, guard,
  ports, value types, executor sub-paths, adapters already wired before this
  feature, docs, build output) have no wiring seam with it.
