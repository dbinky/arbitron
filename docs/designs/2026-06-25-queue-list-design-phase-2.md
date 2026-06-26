# Queue List Visibility — Design Phase 2: Default ordering & priority-aware queue position

> Scope: give each queued instance a priority-aware position that mirrors real run
> order, and define the default ordering policy the table applies. Architecture
> level only — the exact simulation algorithm and SQL belong to produce-plans.

## Context & existing architecture this phase reuses

- `domain/queue.Manager` (`domain/queue/manager.go`) — the **single authority** on
  capacity and scheduling order. It holds `config.Queue.GlobalCap` /
  `config.Queue.PerUserCap`, enforces the per-user cap (`canStartLocked`), and
  selects the next item via `instance.QueueRepository.NextEligible(ctx, globalCap,
  perUserCap)` using priority-desc then FIFO ordering.
- `domain/instance.Instance` fields `Priority`, `QueuedAt`, `OwnerID`,
  `State` — the inputs to ordering.
- The Phase-1 **enrichment step**, which is the single consumer that stamps the
  position onto queued rows.
- `dashboard/src/routes/QueueView.tsx` + `dashboard/src/components/QueueSection.tsx`
  — already group instances into per-state sections and sort client-side; Phase 3
  applies the ordering policy this phase defines.

This phase is deliberately small: one new read-only method on the existing
ordering authority, plus a written ordering policy. No new component, no schema.

## Component architecture

1. **`queue.Manager.QueuePositions` (new, read-only).** One job: return a
   per-instance queue position for all currently-queued instances, computed by the
   *same* ordering rules the manager already uses to pick `NextEligible` — so the
   position never diverges from real scheduling. Read-only: it must not mutate
   queue state, start work, or change capacity. Keeping it on `Manager` means the
   ordering logic lives in exactly one place and is reused, not duplicated into the
   enrichment step or the dashboard.

2. **Ordering policy (specification artifact).** Not code in this phase — a
   documented contract that Phase 3's client-side sort implements. Defined below.

The position value is a **live snapshot**: it is recomputed on each list call from
current queue + running state, exactly like `NextEligible` is evaluated fresh each
time. It is not persisted.

## Data flow

```
Phase-1 enrichment step
  → queue.Manager.QueuePositions(ctx)          (one call per list page)
      • read all queued instances (priority, QueuedAt, OwnerID)
      • read running counts (global + per owner)
      • simulate scheduler selection order, honoring per-user cap
      → map[instanceID]int   (1-based position)
  → stamp position onto each queued EnrichedInstanceSummary row
  (non-queued rows carry no position)
```

## Queue-position semantics — cap-aware simulated rank

Position reflects the order in which queued work will **actually** run, not a naive
sort:

1. Order the queued set by `Priority` (high → normal → low), then `QueuedAt` FIFO
   within a priority — matching `NextEligible`'s ordering.
2. Walk that ordered set assigning run-slots while honoring the per-user cap
   against currently-running counts: an item whose owner is already at the per-user
   cap is **deferred** past later items whose owners still have headroom, mirroring
   what the manager does at dispatch time.
3. Assign 1-based positions in that simulated dispatch order.

This honors the spec's goal: users understand *why* work will run in that order,
including when a per-user cap reorders execution.

## Default ordering policy (applied client-side in Phase 3)

- **Grouping** stays at the existing section level (`QueueView` already does this):
  - **Active** — `running`, `waiting`, `paused`.
  - **Pending** — `queued`.
  - **Terminal** — `failed`, `completed`, `cancelled`.
- **Within Active:** longest-running first (elapsed desc, i.e. `CreatedAt` asc), so
  the most-aged in-flight work surfaces first.
- **Within Pending:** queue **position ascending** (next-to-run on top).
- **Within Terminal:** most-recent first (`CompletedAt`/`UpdatedAt` desc), as today.
- **Re-sort:** client-side, per column. The server provides the fields (including
  position); the client orders them. No server-side ordering is introduced.

The exact placement of the Pending group (its own section vs. the tail of Active)
is a Phase-3 layout detail.

## Data Provenance

| Input to position | Producer (`file:symbol`) | Status |
| --- | --- | --- |
| Queued instance set | `instance.QueueRepository` reads via `domain/queue.Manager` | ✅ exists |
| Priority | `domain/instance.Instance.Priority` | ✅ exists |
| Enqueue time (FIFO tie-break) | `domain/instance.Instance.QueuedAt` | ✅ exists |
| Owner (per-user cap grouping) | `domain/instance.Instance.OwnerID` | ✅ exists |
| Global / per-user caps | `domain/queue.Manager.config.Queue.GlobalCap` / `PerUserCap` (set via `UpdateConfig`) | ✅ exists |
| Currently-running counts | `domain/instance.QueueRepository.CountRunning` via `Manager` | ✅ exists |
| Position consumer | Phase-1 enrichment step | ✅ defined in Phase 1 |

All inputs already live inside `queue.Manager`. There are **no
`UNRESOLVED DEPENDENCY` items** in this phase.

### Deferred to plans

- Wiring `QueuePositions` into the Phase-1 enrichment step in the composition root.
- The exact simulation implementation (data structures, single-pass vs. repeated
  selection) and any read method needed on `QueueRepository` to list the full
  queued set with the fields above (today `NextEligible` returns only one item).
- The exact section layout for the Pending group in the dashboard.

## Integration seams

- **Single authority:** position is computed only inside `queue.Manager`; neither
  the enrichment step nor the dashboard re-derives ordering. If scheduling rules
  change, position changes with them automatically.
- **Read-only contract:** `QueuePositions` must be free of side effects; it shares
  the manager's view of caps/running state but starts nothing.
- **No execution-behavior change:** this phase does not alter scheduling, priority
  calculation, or concurrency (a spec non-goal) — it only *reports* the order.

## Error handling strategy

- `QueuePositions` is consumed inside the best-effort enrichment step: if it errors
  or returns nothing for an id, the position field is simply absent on that row and
  the row still renders (degrade-per-field, per Phase 1).
- An empty queue yields an empty map — not an error.
- The method must be safe under concurrent dispatch (the queue can change between
  read and use); a stale-but-consistent snapshot is acceptable, since position is
  explicitly a live, recomputed-each-call value.

## Testing strategy (strict TDD — all five categories required)

Tests in `domain/queue` (and the enrichment consumer), written before
implementation.

- **Happy-path:** a mixed queue (varied priorities, distinct owners, no caps hit)
  ranks 1..N in priority-then-FIFO order.
- **Success (variations):** per-user cap forces deferral — an owner at cap has
  their later items pushed behind other owners' work; positions reflect the
  simulated dispatch order, not naive sort.
- **Failure:** underlying queue read errors → `QueuePositions` surfaces the error;
  enrichment degrades that field without failing the list.
- **Error:** caps misconfigured / zero (unlimited) — verify zero-cap means no
  deferral and positions are pure priority-FIFO.
- **Edge-case:** empty queue (empty map); single queued item (position 1); exact
  ties on priority + `QueuedAt`; all items owned by one user under a per-user cap;
  global cap already saturated by running work.
