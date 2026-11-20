# Stranded Parent Safety Net Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A parent stranded in `running` at an awaited spawn node, whose child has finished and which no walker
is driving, is found by the periodic spawn reconciler and routed through its normal success or failure path.

**Architecture:**
- A new storage query finds `running` parents at an awaited spawn node whose child is terminal.
- `Engine.AdoptStrandedParent` checks under `e.mu` that the parent has no live walker, then conditionally
  flips it `running → waiting` at that node.
- `SpawnWaitReconciler` runs that adoption pass before its existing `waiting` pass. The existing re-queue then
  wakes the parent, which re-enters the spawn node, short-circuits on the terminal link, and routes.
- Every write that puts a run into `running` and registers its walker is made atomic under `e.mu`, so a run
  being started can never look stranded.

**Tech Stack:** Go 1.25.5, SQLite (hand-written SQL in `adapter/sqlite/instance_repo.go`; never run a blanket
`sqlc generate`), testify.

**Spec:** `docs/superpowers/specs/2026-11-20-stranded-parent-safety-net-design.md`

## Global Constraints

- Hexagonal boundary: `domain/` must not import adapters. The reconciler reaches the engine through a narrow
  interface declared in `domain/engine`.
- Never touch a parent that has a live walker (`e.contexts` holds its ID). Never move a parent that is not
  `running`, or that is not at the candidate node.
- Every adoption logs at WARN: `"spawn-wait reconciler: adopted parent stranded in running"` with `parentID`,
  `childID`, `nodeID`, `childState`.
- Scope is `wait_for_child` only. Fire-and-forget children and parallel-group tasks (`spawn_mode =
  'group_task'`) are excluded.
- `gofmt` every edited Go file. The gate is `go test ./...`.

## Review Focus

1. **A run just started from the queue:** the state is already written `running` but the walker is not yet
   registered. It must not be adopted. Task 2 closes this window structurally, and
   `TestStartInstance_RegistersWalkerBeforeRunningIsVisible` pins it.
2. **A parent paused or cancelled between the query and the adoption** must stay as it is. The conditional
   write covers this; `TestAdoptStrandedParent_NotRunning_NoOp` pins it.
3. **A parent that moved on to a later node** after the query must not be adopted at the old node.
   `TestSuspendStrandedAt_WrongNode_NoChange` pins it.
4. **A parent stranded after a successful child** must route through `success`, not only through failure.
   `TestSpawnWaitReconciler_AdoptsStrandedParent_CompletedChild` pins it.
5. **An adopted parent must be re-queued in the same tick,** not one tick later.
   `TestSpawnWaitReconciler_AdoptedParentIsRequeuedSameTick` pins it.

---

### Task 1: Storage — find and suspend stranded parents

**Files:**
- Modify: `domain/instance/spawn_wait.go`. Add to `SpawnWaitRepository`:
  `ListRunningOnTerminalChild(ctx) ([]SpawnWaitCandidate, error)`.
- Modify: `domain/instance/spawn_link.go`. Add to `SpawnLinkRepository`:
  `SuspendStrandedAt(ctx, id uuid.UUID, nodeID string) (bool, error)`.
- Modify: `adapter/sqlite/instance_repo.go`, which implements both.
- Modify the fakes so everything compiles:
  - `NoopSpawnLinks` (`domain/engine/cancel_cascade.go`): return `false, nil`.
  - `fakeSpawnLinks` (`cancel_cascade_test.go`).
  - `countingSpawnLinks`.
  - `fakeSpawnWaits` (`executor_spawn_wait_test.go`): add a `runningCandidates` field.
  - `nodeClaims` (`executor_group_test.go`).
  - Any other implementer the compiler names.
- Test: `adapter/sqlite/instance_stranded_parent_test.go` (new).

**Interfaces:**
- Produces:
  - `instance.SpawnWaitRepository.ListRunningOnTerminalChild`: the same SELECT as `ListWaitingOnTerminalChild`,
    but with `p.state = 'running'`. It keeps the node-match, `c.spawn_mode = 'wait_for_child'` and terminal-child
    conjuncts.
  - `instance.SpawnLinkRepository.SuspendStrandedAt`: `UPDATE instances SET state='waiting', updated_at=… WHERE
    id=? AND state='running' AND current_node_id=?`. It returns whether a row changed.

- [ ] **Step 1: Write failing tests** in `adapter/sqlite/instance_stranded_parent_test.go`. Use the seeding
  helpers from `instance_spawn_wait_test.go`.
  - `TestListRunningOnTerminalChild_FindsRunningParentAtNodeWithFailedChild`: one candidate, with `ChildState ==
    failed`.
  - `TestListRunningOnTerminalChild_IgnoresActiveChild`: the child is `running`, so the list is empty.
  - `TestListRunningOnTerminalChild_IgnoresWaitingParent`: the parent is `waiting`, so the list is empty. That
    case belongs to the existing query.
  - `TestListRunningOnTerminalChild_IgnoresOtherNodeAndOtherMode`: the parent's current node differs, or the
    child is `fire_and_forget`, so the list is empty.
  - `TestSuspendStrandedAt_MovesRunningAtNodeToWaiting`: returns `true`, and the state is now `waiting`.
  - `TestSuspendStrandedAt_WrongNode_NoChange`: returns `false`, and the state is still `running`.
  - `TestSuspendStrandedAt_NotRunning_NoChange`: a `paused` parent returns `false` and stays `paused`.
- [ ] **Step 2: Run** `go test ./adapter/sqlite/ -run 'RunningOnTerminalChild|SuspendStrandedAt'`. Expected: it
  fails to compile because the methods are undefined.
- [ ] **Step 3: Implement** both methods in `instance_repo.go`, add them to the ports, and update the fakes.
  Share the row-scanning code with `ListWaitingOnTerminalChild` rather than copying it.
- [ ] **Step 4: Run** the same command, then `go build ./... && go vet ./domain/... ./adapter/sqlite/...`.
  Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(instance): query and suspend parents stranded in running`.

### Task 2: Engine — atomic walker registration and `AdoptStrandedParent`

**Files:**
- Modify: `domain/engine/engine.go`. Change `StartInstance` (around line 1017) and `Retry` (around line 2209).
  Add `AdoptStrandedParent`.
- Test: `domain/engine/stranded_parent_test.go` (new). Use the fixtures in `pause_resume_rerun_test.go`.

**Interfaces:**
- Consumes: `SpawnLinkRepository.SuspendStrandedAt` (Task 1).
- Produces: `func (e *Engine) AdoptStrandedParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error)`.
  It returns `true` only when it moved the parent to `waiting`.

- [ ] **Step 1: Write failing tests:**
  - `TestAdoptStrandedParent_NoWalker_SuspendsToWaiting`:
    - Seed a `running` parent at `spawn-1` with no walker.
    - The call returns `true, nil`, and the parent is now `waiting`.
  - `TestAdoptStrandedParent_LiveWalker_NoOp`:
    - Start a parent whose current node blocks on a channel, so the walker is registered and still running.
    - Call with that node's ID. It returns `false, nil`, and the parent is still `running`.
    - Release the channel, then cancel to clean up.
  - `TestAdoptStrandedParent_NotRunning_NoOp`: a `paused` parent returns `false` and stays `paused`.
  - `TestStartInstance_RegistersWalkerBeforeRunningIsVisible`:
    - Use an `instanceRepo` hook that, during the `UpdateState(running)` write, calls `AdoptStrandedParent` on
      another goroutine and waits for it.
    - Expect `false`: the adopter blocks on `e.mu` until registration completes, then sees the walker.
    - This fails today, because `UpdateState` runs before the lock.
- [ ] **Step 2: Run** `go test ./domain/engine/ -run 'AdoptStrandedParent|RegistersWalkerBeforeRunning' -race`.
  Expected: a compile failure, then a FAIL on the registration test.
- [ ] **Step 3: Implement:**
  - In `StartInstance` and `Retry`, hold `e.mu` across `UpdateState(running)` and `e.contexts[id] = cancel`, the
    same pattern `resumeFromRunning` uses. On an `UpdateState` error, unlock and do not register.
  - `AdoptStrandedParent`:
    - Lock `e.mu`.
    - If `e.contexts[parentID]` exists, return `false`.
    - Otherwise call `e.spawnLinks.SuspendStrandedAt(ctx, parentID, nodeID)` and unlock.
    - On success, log at WARN and publish nothing.
- [ ] **Step 4: Run** the step 2 command, then `go test ./domain/engine/ -race -count=3 -run
  'Pause|Resume|Retry|Start'`. Expected: PASS.
- [ ] **Step 5: Commit** with the message `feat(engine): adopt parents stranded in running with no walker`.

### Task 3: Reconciler adoption pass, wiring, e2e

**Files:**
- Modify: `domain/engine/spawn_reconciler.go`.
- Modify: `cmd/arbitron/main.go:1635`.
- Test: `domain/engine/spawn_reconciler_test.go`.
- Test: `cmd/arbitron/spawn_stranded_e2e_test.go` (new).

**Interfaces:**
- Consumes: `ListRunningOnTerminalChild` (Task 1) and `Engine.AdoptStrandedParent` (Task 2).
- Produces:
  - `type StrandedParentAdopter interface { AdoptStrandedParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error) }`
    in `domain/engine`.
  - `func (r *SpawnWaitReconciler) WithStrandedAdoption(a StrandedParentAdopter) *SpawnWaitReconciler`. It is
    optional. A nil adopter skips the pass, so the two test call sites of the constructor stay unchanged.

- [ ] **Step 1: Write failing tests** in `spawn_reconciler_test.go`. Use a fake adopter that records its calls
  and flips the parent to `waiting` in `f.instanceRepo`.
  - `TestSpawnWaitReconciler_AdoptsStrandedParent_FailedChild`:
    - The candidate is in `runningCandidates`.
    - After `Reconcile`, the adopter was called with `(parent, "spawn-1")` and the parent is `queued`.
  - `TestSpawnWaitReconciler_AdoptsStrandedParent_CompletedChild`: the same, with a `completed` child.
  - `TestSpawnWaitReconciler_AdoptedParentIsRequeuedSameTick`:
    - The adopted parent is not in the fake's `waiting` candidates list.
    - The reconciler must still re-queue it, by calling `SetQueuedIfWaiting` for each adopted parent.
    - Expect `queued` after one `Reconcile`.
  - `TestSpawnWaitReconciler_AdopterDeclines_ParentUntouched`: the adopter returns `false`, so the parent stays
    `running` and is never re-queued.
  - `TestSpawnWaitReconciler_NoAdopter_SkipsPass`: the existing behaviour, and `ListRunningOnTerminalChild` is
    never called.
- [ ] **Step 2: Run** `go test ./domain/engine/ -run SpawnWaitReconciler`. Expected: it fails to compile, then
  the tests fail.
- [ ] **Step 3: Implement** the adoption pass at the top of `Reconcile`:
  - List the candidates. A listing error is logged, and the existing pass still runs.
  - Adopt each candidate. On success, call `SetQueuedIfWaiting` and count it.
  - Use one `NotifyCapacityFreed` for both passes.
  - Wire it with `engine.NewSpawnWaitReconciler(...).WithStrandedAdoption(eng)` in `main.go`.
- [ ] **Step 4: Write the e2e** in `cmd/arbitron/spawn_stranded_e2e_test.go`:
  `TestSpawnStrandedE2E_ParentStrandedInRunning_FailedChildRoutesParentToFailed`. Model it on
  `spawn_wait_e2e_test.go` and `spawn_pause_e2e_test.go`, and set `spawnSweepInterval = 200ms` with a cleanup to
  restore it.
  - Run a parent with an awaited child whose workflow fails, and whose `failure` port leads to a script that
    exits 1.
  - Before the child finishes, strand the parent: once the parent is `waiting`, write `running` directly through
    the repo. The parent then has no walker, the same state the 2026-11-19 incident left.
  - Let the child fail.
  - Expect the parent to reach `failed` within 5s, with a node execution on the failure-path script.
- [ ] **Step 5: Run** the e2e with `-count=3`, then the full `go test ./...`. Expected: PASS.
- [ ] **Step 6: Commit** with the message `feat(engine): reconciler routes parents stranded in running`.
