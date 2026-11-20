# Stranded parent safety net — design

Status: approved 2026-11-20 (short design, small change)

## Intent

On the night of 2026-11-19, a `feature-pipeline` run (`54156054`, "practice-hints") hung in `running` at its
`implement` stage. Its `implement` child (`10f025a5`) failed at 02:27 MST. The parent never
took its failure path. It sat in `running` until someone cancelled it by hand, and the whole time it held one of
the server's concurrent run slots.

`feature-pipeline` already does the right thing when a child fails: the failure path records the failure, sends
the Teams alert, and ends at `halt`, which exits 1, so the parent lands in the failed list. The parent never
got there because it never received the wake.

## Root cause

- The engine wakes a parent only when it is `waiting` at the spawn node (`Engine.OnChildTerminal`).
- On 2026-11-19 the tree was paused and resumed: the child's history has a 6-hour gap, 13:03–19:03 MST. The
  deployed build predates PR #4. In that build, resuming a parent whose child is still alive re-enters the
  spawn node, returns a wait, and never puts the parent back to `waiting`.
- So the parent was left `running`, with no walker driving it. The child's terminal wake found a non-waiting
  parent and skipped it, as designed.
- `SpawnWaitReconciler` runs at startup and on a ticker, but it only considers `waiting` parents. Nothing
  recovers a `running` parent with no walker until a restart. A restart marks the parent failed as a crash,
  with no reason and no Teams alert.

PR #4 (`resuspend`) fixes this cause. This design adds the safety net for the class of bug.

## Goals

- A parent stranded in `running` at an awaited spawn node, whose child has finished and which no walker in
  this server is driving, gets the child's outcome within one reconciler tick. It then takes its normal success
  or failure path, so `feature-pipeline` ends in the failed list with its Teams alert, and the slot is freed.
- Each recovery is logged at WARN with the parent ID, child ID and node, because a recovery means some other
  path has a bug.

## Non-goals

- No engine-wide "child failed, so the parent fails" rule. Every awaited spawn must already wire a `failure`
  port, and workflows decide what failure means; some recover.
- Deploying PR #4 is the operator's call and is not part of this change.
- Fire-and-forget children and parallel-group tasks are out of scope. Neither one wakes a parent through this
  path.

## Anti-goals

- Never touch a parent that a live walker is driving, even at the spawn node. A false positive would start a
  second walker: the duplicate-chain bug.
- Never resurrect a parent that is paused, cancelled or terminal.

## Approaches

1. **Extend the existing reconciler sweep (chosen).** It also finds `running` parents at an awaited spawn node
   whose child is terminal. For each one, it checks under the engine's walker lock that the parent has no live
   walker, then flips it `running → waiting` (conditional on state and node) and lets the existing re-queue
   take over. The re-queued parent re-enters the spawn node, short-circuits on the terminal link, and routes
   success or failure. This reuses all the existing routing.
2. **An engine-wide rule: fail the parent when its child fails.** Rejected. It overrides workflows that
   recover through the failure port, and it would not free a parent stranded after a successful child.
3. **Only deploy PR #4.** Rejected. It fixes this cause, but the next path that strands a parent would hang
   silently again.

## Design

- A new candidate query, the `running` twin of `ListWaitingOnTerminalChild`: a parent in `running` whose
  `current_node_id` equals the `parent_node_id` of a `wait_for_child` child in a terminal state.
- An engine method, for example `Engine.AdoptStrandedParent(ctx, parentID, nodeID) (bool, error)`. Under
  `e.mu` it:
  - skips the parent if `e.contexts` holds a walker for it;
  - otherwise moves the parent `running → waiting` with the existing conditional `SuspendRunning`.

  Holding `e.mu` serialises it against `resumeFromRunning` and the walker's park and registration, so a walker
  cannot appear between the check and the write.
- `SpawnWaitReconciler.Reconcile` adopts these candidates first and then runs its existing `waiting` pass in
  the same tick, so an adopted parent is re-queued immediately. The reconciler reaches the engine through a
  narrow port or function, keeping the hexagonal boundary.
- The startup ordering stays as it is: `RecoverCrashedInstances` still fails `running` rows at boot, and there
  are no walkers at boot, so adoption at startup only matters for rows the crash sweep did not touch.

## Left to the builder

- The exact port shape between the reconciler and the engine.
- Whether a minimum age on the `running` row is needed. The walker check should make it unnecessary;
  add one only if a test shows a window.
- An end-to-end test reproducing the night of 2026-11-19: put the parent `running` with no walker and a failed
  child, run one tick, and the parent ends `failed` after taking its failure path.
