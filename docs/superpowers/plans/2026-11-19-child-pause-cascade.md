# Pause/Resume Cascade to Child Runs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pausing a run pauses its whole spawned subtree; resuming it resumes only the descendants its pause stopped.

**Architecture:** Two nullable columns on `instances` (`paused_by_instance_id`, `paused_from_state`) record a cascade pause. The narrow `instance.SpawnLinkRepository` port gains four marker methods (implemented by `*sqlite.InstanceRepo`). The engine walks `ListChildren` exactly like `cascadeCancel`, in a new `domain/engine/pause_cascade.go`.

**Tech Stack:** Go 1.25.5, SQLite (hand-written SQL in `instance_repo.go`, no `sqlc generate`), testify.

**Spec:** `docs/superpowers/specs/2026-11-19-child-pause-cascade-design.md`

## Global Constraints

- Hexagonal: domain uses ports only; SQL lives in `adapter/sqlite`.
- Do NOT run blanket `sqlc generate`; write the new SQL by hand in `instance_repo.go`, as `ListChildren` / `MarkSubtreeCancelRequested` already do.
- Do NOT widen `instance.InstanceRepository` (many in-memory fakes); extend `instance.SpawnLinkRepository`.
- Every descendant is re-authorized with `authorizeIntervention`; a descendant that fails authorization is skipped and its branch not descended (mirrors `cascadeCancel`).
- Cascade walks run on `context.WithoutCancel(ctx)` and use a visited set.
- Strict TDD; `gofmt`; `go test ./...` green at the end of each task.

## Review Focus

1. **Parent already paused before this fix shipped, children still running** → pausing the parent again must sweep its children and succeed (Task 2: `TestPause_AlreadyPausedParent_SweepsActiveChildren`).
2. **Child goes terminal between `ListChildren` and the marker write** → conditional update reports false; no error, no state change, walk continues (Task 1 sqlite test + Task 2 `TestPause_ChildFinishedMidWalk_Skipped`).
3. **Department peer / non-owner descendant** → a child the caller cannot intervene on is left running and its branch not descended; the parent pause still succeeds (Task 2: `TestPause_UnauthorizedChild_BranchSkipped`).
4. **Server restart between pause and resume** → marker is durable in SQLite; resume after reopening the DB still finds it (Task 1: `TestCascadePause_SurvivesReopen`).
5. **Child cascade-paused, then resumed directly by the operator and finished** → later parent resume is a no-op for that child, no error (Task 3: `TestResume_ChildAlreadyResumedDirectly_NoOp`).

---

### Task 1: Storage — marker columns and port methods

**Files:**
- Create: `adapter/sqlite/migrations/040_cascade_pause.up.sql`, `040_cascade_pause.down.sql`
- Modify: `domain/instance/spawn_link.go` (`ChildRef`, `SpawnLinkRepository`)
- Modify: `adapter/sqlite/instance_repo.go` (`ListChildren` + new methods, next to `MarkSubtreeCancelRequested`)
- Modify: `domain/engine/cancel_cascade.go` (`NoopSpawnLinks`), `domain/engine/cancel_cascade_test.go` (`fakeSpawnLinks`), `domain/engine/enricher_child_count_test.go` (its fake)
- Test: `adapter/sqlite/instance_cascade_pause_test.go`

**Interfaces — Produces:**
```go
// domain/instance/spawn_link.go
type ChildRef struct {
    ID       uuid.UUID
    State    State
    PausedBy *uuid.UUID // ancestor whose cascade paused this run; nil otherwise
}

// added to SpawnLinkRepository:
// MarkCascadePaused atomically sets state='paused', paused_by_instance_id=by and
// paused_from_state=<prior state> ONLY IF state IN (running, waiting, queued).
// false = row not in a pausable state (normal, not an error).
MarkCascadePaused(ctx context.Context, id, by uuid.UUID) (bool, error)
// ClaimCascadePause clears both columns ONLY IF state='paused' AND
// paused_by_instance_id=by, returning the cleared paused_from_state.
// claimed=false when the predicate does not match.
ClaimCascadePause(ctx context.Context, id, by uuid.UUID) (from State, claimed bool, err error)
// ClearCascadePause unconditionally clears both columns for id.
ClearCascadePause(ctx context.Context, id uuid.UUID) error
// CascadePausedBy returns paused_by_instance_id (nil when unset).
CascadePausedBy(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
```
Migration up: `ALTER TABLE instances ADD COLUMN paused_by_instance_id TEXT;` and `ALTER TABLE instances ADD COLUMN paused_from_state TEXT;` (no FK, same reasoning as `parent_instance_id`). Down: `DROP COLUMN` both (pattern: `025_following_profiles.down.sql`). `ClaimCascadePause` reads then clears inside one transaction. `NoopSpawnLinks` returns `false` / `"" ,false` / `nil` / `nil,nil`. Test fakes store markers in a map and honour the same predicates.

- [ ] **Step 1: Write failing tests** in `adapter/sqlite/instance_cascade_pause_test.go` (use the setup helpers from `instance_subtree_queries_test.go`):
  - `TestMarkCascadePaused_FromRunningWaitingQueued` — for each of the three states: returns `true`; `Get` shows `StatePaused`; `ListChildren` on its parent shows `PausedBy == &by`; `ClaimCascadePause(id, by)` returns `from == <that state>, claimed == true`.
  - `TestMarkCascadePaused_TerminalOrPaused_NoChange` — for `completed`, `failed`, `cancelled`, `paused`: returns `false`, state unchanged, `CascadePausedBy` nil.
  - `TestClaimCascadePause_WrongAncestor_NotClaimed` — marked by A, claim by B → `claimed == false`, marker still A.
  - `TestClearCascadePause_ClearsMarker` — after clear, `CascadePausedBy` nil and claim by A returns `claimed == false`.
  - `TestCascadePause_SurvivesReopen` — mark, close DB, reopen same file, `CascadePausedBy` == by.
- [ ] **Step 2:** Run `go test ./adapter/sqlite/ -run 'CascadePause|ClaimCascade|ClearCascade' -v` — expect compile failure (methods undefined).
- [ ] **Step 3:** Add the migration, port methods, `ChildRef.PausedBy` (select `paused_by_instance_id` in `ListChildren`), sqlite implementations, and Noop/fake implementations.
- [ ] **Step 4:** Run the Step 2 command → PASS; then `go test ./...` → PASS.
- [ ] **Step 5:** Commit: `feat(instance): durable cascade-pause marker on instances`.

---

### Task 2: Pause cascades to descendants

**Files:**
- Create: `domain/engine/pause_cascade.go`
- Modify: `domain/engine/engine.go` (`Pause`, ~line 1745)
- Test: `domain/engine/pause_cascade_test.go` (reuse `newCascadeFixture` / `run` / `links.link` from `cancel_cascade_test.go`)

**Interfaces:**
- Consumes: Task 1 port methods.
- Produces: `func (e *Engine) cascadePause(ctx context.Context, parentID, by uuid.UUID, visited map[uuid.UUID]bool) int` and `func (e *Engine) pauseDescendant(ctx context.Context, id, by uuid.UUID) error` (authorize → `MarkCascadePaused` → if it was running/waiting publish `instance.paused` + `NotifyCapacityFreed` for running → `recordIntervention(ActionPause)`; returns nil when not pausable).

Behaviour of `Pause(ctx, X)`:
- X running/waiting: as today, then `cascadePause(WithoutCancel(ctx), X, X, {X})`.
- X already paused: if `CascadePausedBy(X) != nil`, `ClearCascadePause(X)`, record `ActionPause`, return nil (operator claims it; no cascade needed, its subtree is already marked). Otherwise run `cascadePause` with `by = X`; return nil if it paused ≥1 descendant, else the original `ErrNotRunning`.
- Any other state: unchanged error, no cascade.
- `cascadePause` traverses every child from `ListChildren` (including terminal and already-paused ones) and calls `pauseDescendant` on those in running/waiting/queued; authorization failure → log + skip branch.

- [ ] **Step 1: Write failing tests:**
  - `TestPause_CascadesToRunningWaitingQueuedChildren` — parent running; children running, waiting, queued → all `StatePaused`; each `CascadePausedBy == parent`.
  - `TestPause_CascadesThroughGrandchildren_AndTerminalIntermediates` — parent → completed child → running grandchild; grandchild paused with marker = parent, completed child unchanged.
  - `TestPause_ManuallyPausedChild_MarkerUntouched` — child already paused with no marker stays unmarked; its running grandchild IS paused with marker = parent.
  - `TestPause_AlreadyPausedParent_SweepsActiveChildren` — parent paused (no marker), child running → `Pause(parent)` returns nil, child paused.
  - `TestPause_AlreadyPausedParentNoActiveChildren_ReturnsErrNotRunning`.
  - `TestPause_OperatorClaimsCascadePausedChild` — child marked by parent → `Pause(child)` returns nil, marker cleared.
  - `TestPause_ChildFinishedMidWalk_Skipped` — fake `MarkCascadePaused` returns false for that child → no error, sibling still paused.
  - `TestPause_UnauthorizedChild_BranchSkipped` — non-admin owner identity; child owned by another user (not viewable) → stays running, its grandchild stays running, parent paused, `Pause` returns nil.
  - `TestPause_CascadeRecordsInterventionPerDescendant` — intervention mock count == 1 + paused descendants.
- [ ] **Step 2:** `go test ./domain/engine/ -run TestPause_ -v` → FAIL.
- [ ] **Step 3:** Implement `pause_cascade.go` and rewire `Pause`.
- [ ] **Step 4:** Step 2 command → PASS; `go test ./domain/engine/` → PASS (existing `pause_resume_rerun_test.go` must stay green).
- [ ] **Step 5:** Commit: `feat(engine): pause cascades to spawned descendants`.

---

### Task 3: Resume cascades to what the pause stopped

**Files:**
- Modify: `domain/engine/pause_cascade.go`, `domain/engine/engine.go` (`Resume`, ~line 1786)
- Test: `domain/engine/pause_cascade_test.go`

**Interfaces:**
- Consumes: Task 1, Task 2.
- Produces: `func (e *Engine) resumePaused(ctx context.Context, inst *instance.Instance) error` — the existing webhook/timer/running dispatch extracted out of `Resume` (no auth, no intervention record); `func (e *Engine) cascadeResume(ctx context.Context, parentID, by uuid.UUID, visited map[uuid.UUID]bool) int`.

Behaviour of `Resume(ctx, X)`: as today, then `ClearCascadePause(X)` (a direct resume ends any cascade claim on X), then `cascadeResume(WithoutCancel(ctx), X, X, {X})`. For each child: authorize; `ClaimCascadePause(child, by)`; if claimed: when `from == queued` → `queueRepo.SetQueued(child, *inst.QueuedAt or now)` + `NotifyCapacityFreed` + `instance.resumed` event; else → `resumePaused(child)`; record `ActionResume`. Descend into every child regardless (a cascade-marked grandchild under a manually-paused child is still resumed).

- [ ] **Step 1: Write failing tests:**
  - `TestResume_ResumesCascadePausedChildren` — pause parent (running child, waiting-with-timer child), resume parent → running child `StateRunning`, timer child `StateWaiting`, markers cleared.
  - `TestResume_QueuedOriginChild_ReturnsToQueueWithOriginalQueuedAt` — `StateQueued`, `QueuedAt` equals the pre-pause value.
  - `TestResume_ManuallyPausedChild_StaysPaused` — child paused by operator before parent pause → still `StatePaused` after parent resume; its cascade-marked grandchild is resumed.
  - `TestResume_ClaimedChild_StaysPaused` — child claimed via `Pause(child)` (Task 2) → stays paused after parent resume.
  - `TestResume_ChildAlreadyResumedDirectly_NoOp` — child resumed directly then completed → parent resume returns nil; child still `StateCompleted`.
  - `TestResume_DirectResumeOfCascadePausedChild_ClearsMarker`.
- [ ] **Step 2:** `go test ./domain/engine/ -run TestResume_ -v` → FAIL.
- [ ] **Step 3:** Extract `resumePaused`, implement `cascadeResume`, rewire `Resume`.
- [ ] **Step 4:** Step 2 command → PASS; `go test ./domain/engine/` → PASS.
- [ ] **Step 5:** Commit: `feat(engine): resume restores only cascade-paused descendants`.

---

### Task 4: Children spawned during a pause, and awaited-child finish during pause

**Files:**
- Modify: `domain/engine/engine_spawn.go` (`Spawn`, after `TriggerWithDepartment` returns)
- Test: `domain/engine/engine_spawn_test.go`, `domain/engine/executor_spawn_wait_test.go` (use their existing fixtures)

**Interfaces:** Consumes Task 1 (`CascadePausedBy`, `MarkCascadePaused`) and Task 2 (`pauseDescendant`).

`Spawn`: after the child is created, re-read the parent; if `parent.State == StatePaused`, compute `by = CascadePausedBy(parent)` or `parent.ID` when nil, and call `pauseDescendant(WithoutCancel(ctx), childID, by)`. Errors are logged, never fail the spawn.

- [ ] **Step 1: Write failing tests:**
  - `TestSpawn_ParentPaused_ChildStartsCascadePaused` — parent paused directly → child `StatePaused`, marker = parent.
  - `TestSpawn_ParentCascadePaused_ChildMarkedWithRootPauser` — parent marked by grandparent G → child marker = G; `Resume(G)` resumes both.
  - `TestWaitForChild_ChildFinishesWhileParentPaused_ResumeRoutesSuccess` — parent waiting on awaited child; `Pause(parent)` (child cascade-paused); `Resume(child)` directly and let it complete; parent stays paused (wake is a no-op); `Resume(parent)` → parent proceeds through the spawn node's `success` port with `childOutput` populated.
- [ ] **Step 2:** `go test ./domain/engine/ -run 'TestSpawn_Parent|TestWaitForChild_ChildFinishesWhileParentPaused' -v` → FAIL (first two; the third may already pass — if so keep it as a regression guard and note it in the commit).
- [ ] **Step 3:** Implement the `Spawn` hook.
- [ ] **Step 4:** Step 2 command → PASS; `go test ./...` → PASS; `gofmt -l domain adapter` → empty.
- [ ] **Step 5:** Commit: `feat(engine): children spawned under a paused parent start paused`.
