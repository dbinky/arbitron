# Arbitron Phase 2 — Queue & Concurrency

## Overview

Phase 2 adds concurrency control, queue management, and two new wait node types to the Arbitron workflow engine. It builds on the Phase 1 foundation — the instance lifecycle, REST API, and execution engine — by enforcing the parallelism caps and priority system that Phase 1 stored but did not enforce, and by introducing the `waiting` state with Timer Delay and Webhook Wait nodes that free capacity while an instance is idle.

---

## 1. Parallelism Caps

The system enforces two configurable limits on simultaneously running workflow instances:

- **Global cap** (default: 20) — the maximum number of instances in `running` state across the entire system.
- **Per-user cap** (default: 5) — the maximum number of instances in `running` state for a single user.

Both caps only count instances in the `running` state. Instances in `waiting`, `paused`, `queued`, or any terminal state do not count. An instance can start only when both the global cap and the per-user cap have room.

Caps are configured via the existing config file and system config API. When an admin lowers a cap below the current running count, existing instances continue running (graceful drain) — no new instances start until the count drops naturally below the new limit.

A cap value of 0 means unlimited (disables that cap). This allows disabling per-user caps while keeping the global cap, or vice versa.

---

## 2. Priority System

Three priority levels: **High**, **Normal**, **Low**. Priority is assigned at trigger time (defaulting to Normal if not specified) and stored on the instance — this field already exists from Phase 1 but is now enforced.

Priority is mutable on instances in `queued` or `waiting` states via a new API operation. Once an instance is `running`, priority is irrelevant (it already has capacity). Priority changes on terminal states are rejected.

**Queue ordering:** When capacity opens, the system selects the next eligible instance by:

1. Highest priority first (High > Normal > Low)
2. Within the same priority level, first-in-first-out (oldest queued timestamp wins)

An instance is "eligible" if both the global cap and the per-user cap have room for that instance's owner. If the highest-priority queued instance belongs to a user who is at their per-user cap, it is skipped and the next eligible instance is considered.

---

## 3. Queue Management

**When capacity is checked:** The queue is evaluated whenever capacity may have increased:

- An instance leaves the `running` state (completing, failing, being cancelled, being paused, or transitioning to `waiting`)
- A parallelism cap is raised via the system config API

In each case, the system checks for eligible queued instances and starts as many as capacity allows.

**Trigger behavior change from Phase 1:** When a user triggers a workflow, the system checks capacity. If both caps have room, the instance transitions `queued → running` immediately (same as Phase 1). If either cap is full, the instance stays in `queued` and waits its turn.

**Queued instance operations:**

- **Cancel** — removes from queue, moves to `cancelled`
- **Priority change** — adjusts queue position

**Queue visibility:** The existing `GET /instances` endpoint with `state=queued` provides queue contents. Instances are returned in queue order (priority, then FIFO). No queue position number or estimated wait time — those are multi-user concerns for later phases.

---

## 4. Timer Delay Node

A new node type that pauses execution for a configured duration. When the engine reaches a timer delay node, the instance transitions from `running` to `waiting` (freeing its capacity slot) and the node records when it should resume.

### Configuration

- `durationSeconds` — the wait duration, supports `{{variable}}` substitution for dynamic delays

### Behavior

1. Engine reaches the timer delay node
2. Instance state transitions to `waiting`, capacity is released
3. The resume time is recorded (current time + resolved duration)
4. When the resume time arrives, the instance re-enters the queue as `queued` with its existing priority
5. When capacity is available and the instance reaches the front of the queue, it transitions to `running` and execution continues from the next node after the timer delay

### Edge Cases

- Duration resolves to 0 or negative: treated as "no delay" — the instance still goes through `waiting → queued → running` but is immediately eligible
- Duration resolves to a non-numeric value: the node fails (follows the node's error behavior)
- Server restarts while a timer is waiting: on startup, the system scans for `waiting` instances with timer delay nodes and re-evaluates their resume times. Past-due timers are immediately re-queued.

---

## 5. Webhook Wait Node

A new node type that pauses execution until an external system hits a unique endpoint. When the engine reaches a webhook wait node, the instance transitions from `running` to `waiting` (freeing its capacity slot).

### Configuration

- `secret` — optional shared secret. If provided, incoming requests must include it in an `X-Webhook-Secret` header. Rejected requests receive a 401 with no detail.
- `timeoutSeconds` — optional. If set and the webhook isn't received within this duration, the node fails (follows the node's error behavior). If omitted, waits indefinitely.

### Endpoint

- When the engine reaches a webhook wait node, it generates a unique endpoint: `/api/v1/webhooks/wait/{token}` where `{token}` is a cryptographically random, URL-safe string.
- The endpoint URL is recorded on the node execution record and available via the instance detail API, so callers know where to send their request.
- The endpoint accepts POST requests only. The full request body (parsed as JSON) is stored as the node's structured JSON output, available to downstream nodes via `{{nodeId.field}}`.
- Non-JSON bodies or empty bodies: the node's output is an empty object `{}` — the webhook still counts as received.

### Behavior

1. Engine reaches the webhook wait node
2. Instance transitions to `waiting`, capacity released, endpoint activated
3. When the endpoint is hit (with valid secret if configured), the payload is captured as node output
4. The endpoint is deactivated (subsequent calls return 410 Gone)
5. Instance re-enters the queue as `queued` with its existing priority
6. When capacity is available, transitions to `running` and continues from the next node

### Edge Cases

- Server restarts while waiting: on startup, re-activate endpoints for `waiting` instances on webhook wait nodes. The tokens are persisted, so the URLs remain stable.
- Timeout expires: node fails per its error behavior. Endpoint is deactivated.
- Multiple hits before processing: first valid request wins, subsequent requests get 410 Gone.

---

## 6. Waiting State

The `waiting` state was defined in Phase 1's state model but not reachable. Phase 2 makes it active.

**Entering waiting:** An instance transitions from `running` to `waiting` when it reaches a Timer Delay or Webhook Wait node. This is the only way to enter `waiting` — it cannot be set manually.

**Exiting waiting:** When the wait condition is satisfied (timer elapses, webhook received), the instance transitions from `waiting` to `queued`. It then follows normal queue rules — priority ordering, capacity checks — before transitioning to `running`.

**Cap accounting:** Instances in `waiting` do not count against global or per-user parallelism caps. This is the key distinction from `paused` (which is a manual user action) — waiting is a planned, temporary hold that the workflow author designed into the graph.

**Operations on waiting instances:**

- **Cancel** — terminates the instance, deactivates any webhook endpoint, cancels any pending timer. Moves to `cancelled`.
- **Pause** — moves to `paused`. The wait condition is preserved (timer countdown continues or webhook endpoint stays active). When resumed, returns to `waiting`. If the wait condition was satisfied while paused, the instance goes to `queued` on resume instead.
- **Priority change** — allowed, takes effect when the instance eventually re-enters the queue.

**Updated state transitions (additions to Phase 1):**

```
running → waiting          (timer delay or webhook wait node reached)
waiting → queued           (wait condition satisfied)
waiting → cancelled        (manual cancel)
waiting → paused           (manual pause)
paused → waiting           (resume, if wait condition not yet satisfied)
paused → queued            (resume, if wait condition was satisfied while paused)
```

---

## 7. API Changes

### New Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/instances/{id}/priority` | Change priority of a queued or waiting instance. Body: `{"priority": "high\|normal\|low"}` |
| POST | `/api/v1/webhooks/wait/{token}` | Webhook wait receiver. POST with JSON body. Optional `X-Webhook-Secret` header. |

### Modified Endpoints

| Endpoint | Change |
|----------|--------|
| `GET /instances` | Returns instances in queue order when filtered by `state=queued`. Supports `state=waiting` filter. |
| `GET /instances/{id}` | Response includes new fields: `waitingUntil` (ISO timestamp for timer delays), `webhookUrl` (endpoint URL for webhook waits), `webhookReceived` (boolean). |
| `POST /workflows/{name}/trigger` | Enforces capacity checks. Returns `202 Accepted` with instance in `queued` state when at capacity (previously always returned `201` with immediate `running`). |
| `GET /system/config` | Exposes current cap values and active counts: `globalCap`, `perUserCap`, `runningCount`, `queuedCount`, `waitingCount`. |

### Configuration

New section in `config.yaml`:

```yaml
queue:
  globalCap: 20
  perUserCap: 5
```

Overridable via `ARBITRON_QUEUE_GLOBALCAP` and `ARBITRON_QUEUE_PERUSERCAP` environment variables, consistent with the existing `ARBITRON_` prefix convention.

---

## 8. Database Changes

### New Columns on `instances` Table

| Column | Type | Description |
|--------|------|-------------|
| `waiting_until` | timestamp (nullable) | Resume time for timer delay nodes |
| `webhook_token` | text (nullable, unique) | Token for webhook wait endpoint |
| `webhook_secret_hash` | text (nullable) | Hashed shared secret for webhook validation |
| `webhook_received_at` | timestamp (nullable) | When the webhook was received |
| `queued_at` | timestamp (nullable) | When the instance entered/re-entered the queue (used for FIFO ordering within priority) |

No new tables. The wait state metadata fits naturally on the instance record since an instance can only be waiting on one thing at a time. Webhook payloads are stored as the node's output in the existing `node_executions` table.

**State enum update:** Add `waiting` to the `instances.state` enum.

**Node type updates:** Add `timer_delay` and `webhook_wait` to the node type vocabulary used in workflow definitions and `node_executions`.

---

## 9. Startup & Crash Recovery

Additions to the existing startup sequence (after Phase 1's crash recovery scan):

1. **Timer scan** — find all instances in `waiting` state on timer delay nodes. For each, check if `waiting_until` has passed. If past-due, transition to `queued`. If still pending, schedule the resume.

2. **Webhook reactivation** — find all instances in `waiting` state on webhook wait nodes. Re-register their endpoint tokens so the API can receive incoming webhooks. Check timeouts — if a `timeoutSeconds` was configured and has elapsed during downtime, fail the node per its error behavior.

3. **Queue drain** — after crash recovery and wait-state restoration, run a queue evaluation pass. Capacity may be available (crashed instances were moved to `failed` by Phase 1's recovery), and there may be queued instances ready to start.

The system needs a background mechanism to wake up when timer delays expire. From the product perspective, timer delays resume when their duration elapses, regardless of server restarts.

---

## 10. Scope Boundaries

### In Scope

- Global and per-user parallelism caps with configuration and enforcement
- Priority enforcement on queue ordering (High, Normal, Low)
- Queue management with capacity-aware state transitions
- Priority mutation on queued and waiting instances
- Timer Delay node type with `{{substitution}}` support for dynamic durations
- Webhook Wait node type with unique endpoint, optional shared secret, optional timeout
- Waiting state fully operational (entering, exiting, cap accounting, operations)
- Graceful drain when caps are lowered
- Crash recovery for waiting instances (timers and webhooks)
- API additions for priority changes, webhook receiver, and queue/cap visibility
- Database migrations for new columns and enum values

### Not In Scope

- Queue position numbers or estimated wait times (multi-user concern, Phase 6)
- Webhook request authentication beyond optional shared secret (full auth in Phase 6)
- Scheduled triggers / cron (Phase 7)
- Webhook triggers that start new instances (Phase 7 — distinct from webhook wait which resumes a running instance)
- Fan-out / Fan-in parallel execution (deferred — future, formerly Phase 8)
- Dashboard visualization of queue state (Phase 3)
- Rate limiting on webhook wait endpoints (addressed by auth in Phase 6)
