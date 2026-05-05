# Arbitron Phase 2 — Design Phase 1: Queue Infrastructure & Capacity Enforcement

## Overview

Phase 1 builds the queue management system that enforces parallelism caps and priority ordering. No new node types — this is the foundation that Timer Delay (Phase 2) and Webhook Wait (Phase 3) build on.

---

## New Domain Package: `domain/queue/`

A `Manager` struct that is the single authority on capacity decisions. Nothing starts an instance without asking it.

### Responsibilities

- **Capacity checking** — given an owner ID, answer "can this user start an instance right now?" by counting running instances against both global and per-user caps
- **Next eligible selection** — query queued instances ordered by priority (High > Normal > Low) then `queued_at` (FIFO within same priority). Skip any instance whose owner is at their per-user cap. Return the first eligible.
- **Queue evaluation** — called whenever capacity may have increased (instance leaves running state, cap raised via config API). Starts as many eligible instances as capacity allows, not just one.

### Ports

New `QueueRepository` port with specialized queries:
- `CountRunning(ctx) (int, error)` — global running count
- `CountRunningByOwner(ctx, ownerID) (int, error)` — per-user running count
- `NextEligible(ctx, globalCap, perUserCap) (*Instance, error)` — the query that implements priority ordering + per-user cap skipping
- `SetQueued(ctx, id, queuedAt) error` — transition instance to queued with timestamp

### Dependencies

- `QueueRepository` (port, implemented by postgres adapter)
- `Engine` (to call `StartInstance` for dequeued instances)
- `Config` (for cap values)

### Cap Semantics

- Cap value of 0 means unlimited (disables that cap)
- When a cap is lowered below current running count: graceful drain — no new starts until count drops naturally
- When a cap is raised: immediate queue evaluation to start eligible instances

---

## Engine Changes

### `Trigger()` Modification

Currently creates an instance as `running` and launches `go e.run()`. Changes to:
1. Create instance as `queued` with `queued_at` set to now
2. Ask the queue manager to evaluate (which may immediately start the instance if capacity allows)

### New `StartInstance(ctx, id) error` Method

Extracted from the current `Trigger()` — the part that creates a context, registers the cancel func, and launches the goroutine. Called by the queue manager when dequeuing.

### State Transition Notifications

Every transition that leaves `running` notifies the queue manager:
- `SetCompleted` → evaluate queue
- `SetFailed` → evaluate queue
- `Cancel` → evaluate queue
- `Pause` → evaluate queue
- Future: `SetWaiting` → evaluate queue (wired in Phase 2)

---

## Instance Model Changes

### New Constants

- `StateWaiting State = "waiting"`
- `ErrNotQueued = errors.New("instance is not queued")`
- `ErrNotWaiting = errors.New("instance is not waiting")`

### New Fields on `Instance`

- `QueuedAt *time.Time` — when the instance entered/re-entered the queue

### New Fields on `InstanceSummary`

- `QueuedAt *time.Time` — for queue-ordered listing

---

## Config Changes

### New `QueueConfig` Struct

```go
type QueueConfig struct {
    GlobalCap  int  // 0 = unlimited
    PerUserCap int  // 0 = unlimited
}
```

Added to the top-level `Config` struct. Defaults: `GlobalCap: 20`, `PerUserCap: 5`.

### Config File

```yaml
queue:
  globalCap: 20
  perUserCap: 5
```

### Environment Variable Overrides

- `ARBITRON_QUEUE_GLOBALCAP`
- `ARBITRON_QUEUE_PERUSERCAP`

---

## Database Migration

Migration `005_add_queue_support`:
- Add `waiting` to the `instance_state` enum
- Add `queued_at` column (nullable timestamp) to `instances`
- Add index on `(state, priority, queued_at)` for efficient queue ordering queries

---

## API Changes

### New Endpoint

`POST /instances/{id}/priority` — change priority of a queued or waiting instance.
- Request body: `{"priority": "high|normal|low"}`
- Returns 200 on success
- Returns 409 if instance is not in `queued` or `waiting` state

### Modified Endpoints

- `POST /workflows/{name}/trigger` — returns `201 Created` when instance starts immediately, `202 Accepted` when queued at capacity. Response body includes instance with state indicating which occurred.
- `GET /instances?state=queued` — returns instances in queue order (priority, then FIFO). Supports `state=waiting` filter.
- `GET /instances/{id}` — response includes `queuedAt` field.
- `GET /system/config` — adds `queue` section with `globalCap`, `perUserCap`, `runningCount`, `queuedCount`, `waitingCount`.

---

## Tests

### Queue Manager Unit Tests

- Capacity checking: under cap, at cap, over cap (graceful drain)
- Per-user cap: user at cap skipped, next eligible selected
- Priority ordering: high before normal before low
- FIFO within priority: oldest queued_at wins
- Cap of 0 disables the check
- Queue evaluation starts multiple instances when capacity allows
- Cap raised triggers evaluation and starts eligible instances
- Cap lowered: no new starts, existing instances unaffected

### Integration Tests

- Trigger when at capacity → instance stays queued → another completes → queued instance starts
- Priority mutation on queued instance → affects queue ordering
- Full lifecycle with queue: trigger → queued → running → completed, with capacity checks at each transition
