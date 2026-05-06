# Arbitron Phase 2 — Design Phase 2: Timer Delay Node

## Overview

Phase 2 adds the Timer Delay node type — the first node that uses the `waiting` state. When the engine reaches a timer delay node, the instance transitions to `waiting` (freeing capacity), waits for the configured duration, then re-enters the queue.

---

## Workflow Model Changes

### New Node Type

Add `NodeTypeTimerDelay NodeType = "timer_delay"` to `domain/workflow/workflow.go`.

### Timer Delay Config

```go
type TimerDelayConfig struct {
    DurationSeconds string  // supports {{substitution}}
}
```

Stored in the node's `Config` field within the workflow manifest JSON.

### Validation Rules

- Timer delay nodes must have exactly one outgoing edge
- `durationSeconds` is required (must not be empty)
- Timer delay nodes cannot be entry points

---

## Timer Delay Executor: `domain/engine/executor_timer.go`

Unlike execution nodes, the timer delay executor sets up a wait rather than running external work.

### Execution Flow

1. Resolve `{{substitution}}` on `durationSeconds` against current instance variables
2. Parse to float64 — if non-numeric, fail the node (follows node's error behavior)
3. If value is ≤ 0, treat as zero delay (still goes through the full waiting→queued→running cycle but is immediately eligible)
4. Calculate `waiting_until` = now + duration
5. Write a node execution record with status `succeeded`
6. Transition instance to `waiting` state, set `waiting_until` on the instance
7. Register the timer with the scheduler
8. Notify the queue manager to evaluate (capacity just freed)
9. Return a `WaitResult` — a new result type that tells the engine's `run()` loop to stop walking without treating it as completion or failure

### Engine `run()` Loop Change

The `run()` loop currently expects executors to return a `NodeResult` and continues to the next node. Add handling for `WaitResult`: when received, the loop exits cleanly without changing instance state (the executor already transitioned to `waiting`).

---

## Timer Scheduler: `domain/queue/scheduler.go`

Lives in the queue package since timer expiration directly triggers queue evaluation.

### Design: Hybrid Single-Timer

One goroutine, one `time.Timer`, set to the earliest pending expiration.

### Data Structure

In-memory map: `map[uuid.UUID]time.Time` — instance ID to `waiting_until`. Protected by mutex.

### Methods

- `Schedule(instanceID uuid.UUID, waitingUntil time.Time)` — add or update a timer. If earlier than the current timer, reset it.
- `Cancel(instanceID uuid.UUID)` — remove a timer. If it was the earliest, recalculate and reset.
- `Start(ctx context.Context)` — launch the scheduler goroutine. Called at startup.
- `Stop()` — shutdown the scheduler goroutine gracefully.

### Timer Fire Flow

1. Timer fires for the earliest pending instance
2. Remove from in-memory map
3. Transition instance from `waiting` to `queued` (set `queued_at`, clear `waiting_until`)
4. Notify queue manager to evaluate
5. Set timer for the next earliest pending (if any)

### Edge Cases

- Multiple timers expire at the same moment: process them all in sequence before resetting the timer
- Schedule called while processing: mutex protects the map, new timer is picked up on next reset
- Cancel called for a non-existent ID: no-op
- Timer fires for a paused instance: the instance is in `paused` state (not `waiting`), so the `waiting → queued` transition is a no-op. Remove from in-memory map and move on. The resume logic will check `waiting_until` and handle the transition.

---

## Instance Model Changes

### New Fields

- `WaitingUntil *time.Time` — when the timer delay expires

---

## Waiting State Operations

Now that waiting instances exist, implement the operations defined in the spec:

### Cancel on Waiting Instance

1. Cancel the pending timer via scheduler
2. Transition to `cancelled`
3. Notify queue manager to evaluate (no capacity change, but consistent)

### Pause on Waiting Instance

1. Transition to `paused` — timer keeps counting (do NOT cancel the scheduler entry)
2. On resume:
   - If `waiting_until` has passed → transition to `queued` (set `queued_at`), notify queue manager
   - If `waiting_until` has not passed → transition back to `waiting`

### Priority Change on Waiting Instance

Already supported by Phase 1's priority endpoint (which accepts `queued` or `waiting`). No additional work needed.

---

## Database Migration

Migration `006_add_timer_delay`:
- Add `waiting_until` column (nullable timestamp) to `instances`

---

## API Changes

### Instance Response

- `waitingUntil` field (ISO timestamp string, nullable) — populated when instance is waiting on a timer delay

---

## Crash Recovery

Added to the startup sequence after Phase 1's queue evaluation:

1. Query all instances in `waiting` state with non-null `waiting_until`
2. For each:
   - If `waiting_until` has passed → transition to `queued`, set `queued_at`
   - If `waiting_until` is in the future → register with the scheduler
3. Run a queue evaluation pass (past-due timers may have added to the queue)

---

## Tests

### Executor Unit Tests

- Substitution resolves correctly, duration parsed
- Non-numeric duration fails the node
- Zero/negative duration: instance goes to waiting with immediate eligibility
- Node execution record written with succeeded status

### Scheduler Unit Tests

- Single timer: fires at correct time, instance transitions to queued
- Multiple timers: earliest fires first, then next
- Cancel: removes timer, recalculates next
- Schedule earlier than current: timer resets
- Concurrent schedule/cancel: no races (run with `-race`)

### Integration Tests

- Full flow: trigger → running → timer delay node → waiting → timer fires → queued → capacity available → running → continues to next node
- Timer with pause: waiting → pause → resume after timer expired → queued
- Timer with cancel: waiting → cancel → cancelled
- Crash recovery: create waiting instance with past-due timer → restart → instance transitions to queued
