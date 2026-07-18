# Dashboard queue: live current-model display + terminal run-duration

Two related dashboard-queue fixes, bundled because they touch the same
components (`QueueView`, `QueueSection`, `QueueTableRow`, `queueColumns`,
`time.ts`) and the same enrichment seam.

## Problem

### Bug A — active rows show the previous node's model

The enriched queue row assembles its `Model` field from `agg.LatestModel` — the
`llm_model` of the most-recently-*completed* node execution
(`enricher.go:147`, sourced from `node_execution_repo.go:163-173`). But
`FriendlyStep` and `Backend` come from the *current* node in the workflow
snapshot (`enricher.go:133-135`). So while a node runs, the row pairs the
current node's step/label with the **previous** node's model.

The node-event live patch made the mismatch visible: on `node.started` the
client advances the Step column (`currentNodeId` + `friendlyStep`) but
deliberately leaves Model/Backend alone (`QueueView.tsx:136-144`), because a
prior attempt to patch backend alone showed a `modelN-1 · backendN` pair (commit
`6563719`). Net effect, observed live: a running `pr-review` instance on the
`senior-engineer` node (profile `coder-opus-medium`) displayed
`claude-sonnet-5 · claude-code` — the Manager node's model.

Both authoritative sources (MCP `workflow_get`, filesystem manifest) confirm
`senior-engineer` is Opus-medium; the workflow is correct. The display was
wrong.

### Bug B — terminal rows show wall-clock "X ago", not run duration

All sections share one `COLUMNS` list via `visibleColumns(density)`
(`queueColumns.ts`). The Completed and Failed sections pass `timeMode="relative"`
(`QueueView.tsx:234,248`), so their "Elapsed" column renders
`relativeTime(updatedAt)` = "4d ago" (`QueueTableRow.tsx:46`) — a value that
grows with wall-clock time, not the fixed time the run actually took. The
terminal sections also carry columns that are meaningless once a run is done:
Step, Queue, and Model/Backend.

## Non-goal / scope note

The backend already records a terminal timestamp: `SetInstanceCompleted` and
`SetInstanceFailed` both stamp `completed_at = now`
(`adapter/sqlite/queries/instances.sql:44-48`), already exposed on the row as
`completedAt`. **No new backend timestamp is needed.** Bug B is client-only.

## Design

### Part 1 — Active-section model display (server + client)

Show the **currently-running node's configured model**, consistently in both the
refetch (enricher) path and the live-patch path, so the two never disagree.

**Server**

1. **Engine `ProfileResolver` seam.** Add `SetProfileResolver(r provider.ProfileResolver)`
   to `Engine`, mirroring the existing `SetWorkflowResolver`. Best-effort: a nil
   resolver (local mode, tests) yields an empty model and changes nothing.

2. **Shared helper extension.** `nodeLabelAndBackend(node, defaultBackend)`
   becomes `nodeLabelBackendModel(ctx, node, defaultBackend, resolver) (label,
   backend, model string)`. Model = `resolver.Resolve(ctx,
   node.Config.ModelProfile).Model` when the node has a `ModelProfile` and the
   resolver is non-nil; empty for non-agent nodes or on any resolver error
   (best-effort, never fails the caller). This single helper is used by both the
   enricher and the engine event emitters, guaranteeing a live patch matches a
   refetch.

3. **Enricher** (`enricher.go`). `resolveNodeFields` returns the configured model
   as a third value. Assembly becomes:
   - active (running/waiting/paused) and failed rows: `row.Model` = configured
     model of the relevant node (current node for active, failed node for
     failed).
   - completed/cancelled rows: relevant node is none → configured model `""` →
     `row.Model` falls back to `agg.LatestModel` (the correct end-state model).

   Concretely: keep `row.Model = agg.LatestModel` from the aggregate, then
   `if cfgModel != "" { row.Model = cfgModel }`.

4. **Node events** (`engine.go` `node.started` ~L809-813 and `node.failed`
   ~L893-897): add `"model"` to the `Data` map, from the same helper.

**Client**

5. `NodeEventData` (`api/types.ts`) gains `model?: string` and `backend?: string`.

6. `QueueView` node-patch handler (`QueueView.tsx:140-144`): patch `model` and
   `backend` alongside `currentNodeId`/`friendlyStep`; all advance together on
   `node.started`. Remove the obsolete "don't patch backend" comment.

### Part 2 — Terminal sections: column trim + real duration (client only)

**A. Per-section column sets.** `visibleColumns(density)` gains an options arg:
`visibleColumns(density, opts?: { terminal?: boolean })`. When `terminal` is
true it excludes **Step** (`friendlyStep`), **Queue** (`queuePosition`),
**Model/Backend** (`modelBackend`), and **Status** (`status`). Terminal sections
keep: Workflow, Run, Submitter, Duration, Submitted (expanded), Retries
(expanded). QueueView threads a `terminal` signal into QueueSection, whose
`columns` memo passes it through. Active/Pending sections are unchanged.

**B. Real run duration.**
- New pure helper `duration(startStr, endStr): string` in `time.ts`: formats
  `end − start` with the existing compact formatting (`1m 23s` / `2h 5m` /
  `4d 3h`), clamped to `0` when negative.
- New `timeMode: 'duration'` for terminal sections (replacing `'relative'`
  there). In `QueueTableRow` the elapsed-column branch renders
  `duration(row.queuedAt ?? row.createdAt, row.completedAt ?? row.updatedAt)` —
  i.e. **`queuedAt → completedAt`**, falling back to `createdAt` when `queuedAt`
  is absent and to `updatedAt` if `completedAt` is missing (defensive; both
  terminal SQLs stamp `completed_at`).
- **Header:** the terminal variant's duration column uses header **"Duration"**;
  the live Active section keeps **"Elapsed"**. Free, given per-section columns.

**C. Both terminal sections.** Apply A+B to Completed and Failed.

### Known consistency note (intentional, not a bug)

Per-row Duration uses `queued → completed`, while the dashboard's avg-duration
stat uses `completed − created` (`instance_repo.go:389`), which includes
pre-queue time. Per-row values will therefore read slightly shorter than the
aggregate. Left as-is; aligning the stat is a separate follow-up, out of scope
here.

## Testing (TDD)

Per repo convention, cover the five scenario categories where applicable.

**Part 1**
- Enricher: active row → `Model` is the current node's configured model; failed
  row → failed node's configured model; completed row → `agg.LatestModel`; nil
  resolver → model unchanged/empty (degradation).
- `nodeLabelBackendModel`: agent node with a known profile → resolved model;
  script/transform node → empty model; unknown/erroring profile → empty
  (best-effort).
- Node-event payload includes `model` (and `backend`) for an agent node and
  empty `model` for a script node.
- Client livepatch (`QueueView.livepatch.test.tsx`): a `node.started` event
  patches `model` + `backend` + `friendlyStep` together into the row.

**Part 2**
- `duration()`: sub-minute, minutes+seconds, hours+minutes, multi-day, negative
  → `0s`, missing endpoints handled.
- `visibleColumns(density, { terminal: true })` excludes Step/Queue/Model-Backend/Status
  for every density; non-terminal unchanged.
- Terminal-section render: a completed row shows a fixed `queuedAt→completedAt`
  duration (not a now-based value) under the "Duration" header, and does not
  render the four removed columns.

## Files touched

- `domain/engine/engine.go` — `SetProfileResolver`; `model` in node events.
- `domain/engine/enricher.go` — `nodeLabelBackendModel`; state-aware `Model`.
- `domain/provider` — (reuse existing `ProfileResolver` / `ResolvedBinding.Model`).
- wiring (`cmd/arbitron/main.go` or engine construction) — inject the resolver.
- `dashboard/src/api/types.ts` — `NodeEventData.model/backend`.
- `dashboard/src/routes/QueueView.tsx` — patch model/backend; thread `terminal`.
- `dashboard/src/components/queueColumns.ts` — `visibleColumns` terminal variant.
- `dashboard/src/components/QueueSection.tsx` — pass terminal/timeMode through.
- `dashboard/src/components/QueueTableRow.tsx` — duration render branch.
- `dashboard/src/utils/time.ts` — `duration()` helper.
- Tests alongside each.
