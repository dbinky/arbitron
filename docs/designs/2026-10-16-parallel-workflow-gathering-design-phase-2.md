# Parallel Workflow Gathering — Design Phase 2: Execution Core (Results Only)

Spec: `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
Phases: 1 — Definition/Validation/MCP · **2 — Execution core (results only)** · 3 — Working-area VCS & file gathering · 4 — Run visibility

## Goal

Execute both group forms with isolated copies of group-start variables and files, apply
completion rules, freeze ordered results, and continue once. Nested groups and parent
cancellation apply to both forms. Enable `results`; retain Phase 1's actionable guard for
`results_and_files` until Phase 3. Existing awaited single-child outputs and fire-and-forget
behavior remain available unchanged.

## Design decisions

Every task is a child instance. `domain/engine/engine.go:Engine.run` remains single-cursor
per instance; instances compete through existing `queue.Manager` limits. Branch instances
run the parent's definition snapshot from a validated branch entry. Workflow tasks use
visible child definitions. A durable group coordinator owns decisions and reconciliation;
in-process wakes only accelerate it. Group outcomes are node outputs, never parent-variable
merges.

## Components

### 1. Durable group-run record and repository

New domain type/port under `domain/instance` and SQLite adapter/migration (next available
number, currently 019). Persist:

- Group-run ID, parent/node IDs, launch attempt, config snapshot, validated body ownership,
  a deep group-start variable snapshot, and the workspace snapshot handle.
- The complete ordered task roster **before enqueue**: task key, origin, reserved child ID,
  launch state, live child state, result/failure data, terminal ordering, cancellation intent,
  and blocking/continuing-independently flags. Children carry group/task linkage and a
  branch-kind marker in addition to existing spawn ancestry.
- State: `launching → running → decided → done`; Phase 3 inserts `gathering` and
  `resolving_conflicts` after `decided`. Launch failures can also decide failure.
- Atomic decision (rule, success/failure, winner, immutable ordered task snapshot), durable
  unfinished-task actions, final gathered output, and continuation readiness/delivery.
  Separate live statuses from frozen outcome: later task results only affect live inspection.

Repository operations must make child registration/enqueue recovery idempotent and serialize
terminal observation with decision selection. Reconciliation finishes partially applied
cancel/wake actions after a crash; losing the decision CAS is not permission to drop them.
A decision CAS alone does not make its subsequent side effects durable.

### 2. Task launcher and starting copies

- Capture variables once before any child starts. `VariableStore.Snapshot` is a **shallow**
  map copy; deep-copy nested maps/arrays for the stored start state and separately for every
  task. Exclude reserved names, then overlay that task's resolved input map. Both forms
  inherit this base. Pass the combined map to workflow child creation, so a required input
  may come from the base; explicit mappings are checked against declared child inputs and
  runtime required/type checks run before enqueue.
- Define a typed recursive input resolver: literals retain type; a whole-value `{{path}}`
  reference preserves the referenced value's type; interpolated strings use
  `domain/variable/substitution.go:Resolve`; nested maps/arrays recurse. Missing references
  are actionable errors. `Resolve(string, store) string` alone neither resolves a map nor
  rejects missing references; use `VariableStore.Get` for typed path reads.
- Workflow form shares `Engine.Spawn` / `TriggerWithDepartment` authorization, owner
  identity, visibility/department stamping, priority and `instance.MaxSpawnDepth` checks.
  Reconstruct the parent's owner identity via `IdentityResolver` before creating/cancelling
  tasks, as `SpawnWorkflowExecutor.prepareAndSpawn` does. The new launch path must support
  reserved child IDs and preparation **before enqueue**; current `Engine.Spawn` immediately
  enqueues and cannot be called and then patched with task linkage/workspace setup.
- Branch form gets a new snapshot trigger path: persist the parent's `WorkflowSnapshot`,
  validated branch entry as `CurrentNodeID`, copied variables and ancestry, then run to the
  body's sink. `Engine.run` already honors a preset cursor. Apply the same owner/depth/
  priority checks as workflow children; do not reload the parent's definition by name.
- Both forms need a new workspace snapshot/fork capability before enqueue. Existing
  `prepareWorkspace` / `EnsureInstanceDir` only create directories. Take one immutable
  group-start copy of the parent's working area and fork independent task directories;
  preserve relative file layout even for results-only groups without `repoPath`. No hard
  links, writable symlinks, or Git metadata pointing back to the parent's mutable worktree.
  Reseed engine-owned `workspace`; arbitrary author strings are not silently rewritten.
  P2-B must define task-local path binding for workspace-derived input templates and reject
  writable shared-path configurations; passing a resolved parent `work_dir` into every task
  would defeat isolation. Do not assume path provenance can be inferred from any string.
  P2-T17 materializes independent metadata for linked worktrees before results execution
  is enabled, including the bundled feature-pipeline layout. Phase 3 reuses this metadata
  helper and extends the full-area snapshot/fork boundary with mergeable VCS backing.
- Extend `SpawnMode.Valid` and link persistence to admit internal `group_task` while
  keeping public `spawn_workflow` at its existing two modes. Preserve migration 018's
  `spawn_mode` CHECK: group tasks use `spawn_mode=NULL` plus all three group-link
  columns, decoded as `group_task` by the adapter (P2-T1/T2), without rebuilding instances. Register all
  tasks before enabling decisions, and finish or fail the launch barrier before normal
  rule evaluation. A fast early success must not cause later tasks to be launched after
  an already-applied cancellation decision.

### 3. Coordinator and result contract

Feed terminals from `Engine.OnChildTerminal` (explicit new group-task dispatch) and a new
group sweep alongside `main.go:sweepSpawnReconcilers`. Read persisted child state/results
and terminal failure traces, including terminals missed during crash recovery.

- `all_succeed`: succeed only when every task is completed; a failed/cancelled task counts
  as non-success after its own configured error handling. `cancel_unfinished` decides
  failure immediately and durably requests cancellation; `wait_for_remaining` decides
  failure only when all tasks are terminal.
- `first_success`: earliest successfully completed task wins; failures/cancellations do
  not stop other candidates. Fail only when all are terminal without success. Persist an
  ordering in the SAME transaction as the terminal state transition (P2-T16's SQLite
  terminal triggers and durable clock), so delayed/out-of-order callbacks and restart
  cannot select a later success. Second-resolution timestamps and callback-time
  `MAX+1` intake do not establish terminal order. A missing receipt blocks decision.
- Winning decision freezes tasks in author order with task key, branch entry or workflow
  origin, child ID, status (`completed`, `failed`, `cancelled`, `unfinished`), available
  result, failure details, rule, and winner key. Use child final `Instance.Variables` minus
  reserved names. Failed/cancelled tasks retain available variables, not just successes.
  Unfinished tasks have no final result; a cancellation request is shown separately and
  must not falsely report confirmed `cancelled` at the decision boundary.
- Apply cancellation idempotently under the reconstructed owner identity, or mark
  non-winners continuing independently. Later terminals update live status only: never
  modify the frozen task results/winner or trigger another parent continuation.
- Queue parent only if it is waiting at **this group node**, has continuation readiness,
  and is not cancelled/terminal. Current `QueueRepo.SetQueuedIfWaiting` checks state only;
  add a group/node-addressed claim. Persist action progress so the reconciler recovers a
  crash between decision, cancellation and wake. Use the same CAS discipline to close the
  fast-task lost-wake window without two engine goroutines walking the parent graph.

### 4. Executor and Phase 3 seam

First entry parses Phase 1 config/ownership, creates durable launch state, prepares and
launches tasks, writes the wait trace, transitions parent to `waiting`, performs the
lost-wake check, and releases queue capacity via `NotifyCapacityFreed`. A waiting parent
holds no slot, including at a per-owner cap of one.

Re-entry never launches a second roster. Undecided/not-ready returns `IsWait`; decided
results-only groups finalize output and mark continuation ready. Phase 3 file groups must
finish gathering first. Persist final output before readiness and replay the same result
on recovery at this node; parent variable/trace/advance handling must be idempotent for
that group-run ID. `success` and `failure` both expose the same output shape, e.g.
`{{group-1.tasks.0.result...}}`, `{{group-1.winner}}`.

Task outcomes freeze at decision. Final output adds a separate merge/resolution report
only before first continuation (Phase 3), then freezes entirely. A merge failure changes
the final group route to `failure` without rewriting the task-decision history.

Register `NodeTypeParallelGroup` before `FreezeExecutors`; enable results execution only
when all Phase 2 dependencies are wired. Preserve old spawn executors/output contracts.

### 5. Cancellation, lifecycle, events

Parent cancellation stops every unfinished descendant, including nested groups, workflow
children and non-blocking tasks. Reuse authorized `Engine.Cancel`, but extend traversal:
`ListActiveChildren` currently omits terminal intermediate children that may still have
active grandchildren. The existing orphan sweep only joins directly cancelled parents;
it cannot repair that hole. Traverse retained ancestry through terminal intermediates and
reconcile cancellation intent throughout the subtree. Parent cancellation after its own
completion must still support stopping retained independent work without reopening its
outcome (current `Cancel` returns `ErrCannotCancel` for terminal instances).

Preserve task resources while needed by gathering or independently running descendants;
Phase 3 supplies the VCS retention hook. Publish `group.*` lifecycle events through
`domain/engine/ports.go:EventBus`; expose live task states `queued`, `running`, `waiting`,
`completed`, `failed`, `cancelled`. An instance paused by existing controls is displayed as
paused/waiting with a reason, never completed. Phase 4 reads persisted state as authority.

## Data Provenance

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Config, ordered tasks/inputs, branch ownership | Executor / launcher | **PLANNED DEPENDENCY P1-A** (Phase 1 typed config/ownership); raw definition is `domain/instance/instance.go:Instance.WorkflowSnapshot` |
| Group-start variables, reserved names, typed path values | Launcher | `domain/variable/variable.go:VariableStore.Snapshot`, `Get`, `ReservedInputNames`; reconstructed in `engine.go:Engine.run` with `FromSnapshotWithNodeIDs` |
| Deep copies, typed recursive resolved map, immutable workspace starting copy | Launcher | **PLANNED DEPENDENCY P2-B**: no deep-copy/map resolver/workspace snapshot or task-local path-binding producer exists; `substitution.go:Resolve` is string-only; `domain/engine/workspace.go:Engine.prepareWorkspace` only seeds an empty directory |
| Parent workspace path | Snapshot/fork | `domain/engine/workspace.go:Engine.prepareWorkspace` → `domain/engine/ports.go:WorkspaceManager.EnsureInstanceDir`, implemented in `adapter/filesystem/workspace.go` |
| Child declared inputs and visible definition | Workflow launcher | `domain/engine/engine_spawn.go:Engine.ChildInputs`, `Engine.Spawn` → `Engine.TriggerWithDepartment`; author-time injection remains **P1-B** |
| Owner identity, priority, depth, branch definition/start cursor | Launcher / cancellation | `domain/instance/instance.go:Instance` fields, `cmd/arbitron/main.go:userIdentityResolver.IdentityForUser` implementing `domain/engine/ports.go:IdentityResolver`, `domain/engine/engine_spawn.go:Engine.Spawn`; preset cursor is honored by `engine.go:Engine.run` |
| Prepare-before-enqueue, reserved IDs, snapshot branch trigger, group-task links | Launcher | **PLANNED DEPENDENCY P2-C**: extend creation paths and `domain/instance/spawn_link.go:SpawnMode.Valid`; current `Spawn` enqueues immediately |
| Child final/available variables, terminal state | Coordinator | `domain/instance/ports.go:InstanceRepository.Get`; `domain/engine/executor_spawn.go:childOutput` excludes reserved names |
| Failure messages and attempts | Coordinator | `domain/instance/ports.go:NodeExecutionRepository` and `domain/instance/instance.go:NodeExecution.Error`, `Attempt`; `Instance.State` alone has no complete failure details |
| Terminal notifications and recovery sweep | Coordinator | `domain/engine/spawn_wake.go:Engine.OnChildTerminal`, queue terminal hook wired in `cmd/arbitron/main.go`; new group dispatch/sweep is **P2-A** |
| Group persistence, first-terminal order, decision/side-effect claims, immutable final output, group events | Executor / coordinator / later phases | **PLANNED DEPENDENCY P2-A**: new group-run repository/coordinator and terminal-order persistence; existing spawn wait claims do not supply them |
| Parent wake primitives | Coordinator | `adapter/sqlite/queue_repo.go:QueueRepo.SetQueuedIfWaiting`, `domain/queue/manager.go:Manager.NotifyCapacityFreed`; node-addressed group claim is **P2-A** |
| Descendant links and authorized cancellation | Cancellation | `domain/instance/spawn_link.go:SpawnLinkRepository`, `domain/engine/engine.go:Engine.Cancel`, `cancel_cascade.go:Engine.cascadeCancel`, `orphan_reconciler.go:OrphanedChildReconciler.Reconcile` |
| Complete subtree cancellation, including terminal intermediates/terminal parent independent work | Cancellation | **PLANNED DEPENDENCY P2-D**: current active-child query and terminal-parent refusal do not provide this contract |

## Integration and plan obligations

Implement P2-A–D and P1 prerequisites; pick the next migration number, extend sqlc and
SQLite link constraints, wire repository/launcher/coordinator/executor/reconciler in
`cmd/arbitron/main.go`. Phase 3 consumes the same launch boundary and group-run contract;
Phase 4 consumes live states separately from immutable gathered data. These are new tasks,
not resolved inputs just because they have a proposed component name.

## Error handling

Before group creation, invalid config/inputs or snapshot preparation failures use normal
node `ErrorBehavior`. After launch begins, a partial launch failure records a failed group
with ordered available results and launch details, cancels launched tasks, marks unlaunched
tasks unfinished with a reason, and routes the group's `failure` path once. Retry/re-entry
cannot duplicate children. Coordinator persistence failures retry through reconciliation.
Crash recovery can mark running children/parents failed; waiting parents survive. Recovery
must also clean up partially launched/decided groups under failed parents, not only waiting
ones. Cancellation never resurrects a parent or merges partial work.

## Testing — STRICT TDD

Write a failing test, observe red, implement, observe green, then refactor for every unit.

- **Happy-path:** both forms inherit identical starting variables/files, make concurrent
  progress within caps, and return all-succeed results in author order on `success`.
- **Success:** earliest success wins despite reversed callback delivery; both unfinished
  behaviors; repeat a child workflow with different inputs; both ports expose results;
  existing awaited `childOutput` and fire-and-forget spawning remain correct.
- **Failure:** all-succeed wait/cancel rules, every first-success candidate fails/cancels,
  cancelled task never wins, failed task's available variables and error details retained.
- **Error:** partial launch, failed contract/type resolution, depth limit, missing workspace
  snapshot capability, repository failure after decision before cancellation/wake, crash
  during launch, retry without duplicate children, failed-parent resource reconciliation.
- **Edge-case:** deep map/array and task-path isolation, missing template path, entrypoint
  group, single task, both forms nested in either form, cap of one, simultaneous terminals, fast-task
  lost wake, cancelled parent not requeued, immutable late results, restart in every state,
  full subtree cancellation through completed intermediates and after parent completion,
  cancellation requested versus confirmed cancelled, parent variables never auto-merged.

## Plan dependency closure

The [producer/wiring/test map](../plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md) closes
the design IDs above with concrete tasks and orders the nonnumeric prerequisites.
These are planned capabilities, not claims that implementation already exists.
