# Queue List Visibility — Design Phase 3: Dashboard rich table, density views & row details

> Scope: present the enriched data (Phase 1) and queue position (Phase 2) as a
> richer, sortable, density-aware table with inline row details. Architecture level
> only — component boundaries, contracts, and data flow. Exact JSX, CSS, and the
> column-per-density mapping belong to produce-plans.

## Context & existing architecture this phase reuses

- `dashboard/src/routes/QueueView.tsx` — fetches active/failed/completed in
  parallel, manages the dismissal toggle, renders per-state sections.
- `dashboard/src/components/QueueSection.tsx` + `QueueSection.module.css` — one
  collapsible per-state section with **client-side** sort (priority/time/name) via
  `useMemo`; dismissal seam preserved.
- `dashboard/src/components/InstanceRow.tsx` + `InstanceRow.module.css` — current
  grid row (replaced/extended by the table here).
- `dashboard/src/api/client.ts` — `api.instances.list(params)`; this phase consumes
  the enriched fields the same call now returns, and uses the existing
  single-instance **trace endpoint** (`GetNodeTrace`) for last-error lazy-load.
- Vitest + `@testing-library/react` patterns (`QueueSection.test.tsx`,
  `client.test.ts`).

No new persistence pattern exists in the dashboard; the density preference
introduces a small, self-contained `localStorage` hook.

## Component architecture

1. **Column-definition module (single source of truth).** One job: declare each
   column once — key, header label, value accessor over the enriched row, whether
   it is sortable, and its per-density visibility. Every other component reads
   columns from here, so adding/reordering a column is one edit. Columns: workflow,
   run name, submitter, submitted local time, status, friendly step, elapsed,
   queue position, model/backend, retry count.

2. **`useDensityPreference` hook (new).** One job: own the density value
   (`balanced` | `dense` | `expanded`), defaulting to `balanced` on first use and
   persisting each change to `localStorage` under a per-user-scoped key. Pure view
   state; no network. Internals (storage key, serialization) can change without
   consumers caring — they get `{ density, cycle() }`.

3. **Density toggle button (new).** One job: a compact icon button that cycles the
   three views via the hook. Presentation only.

4. **Sortable table (extends `QueueSection`).** One job: render a section's rows as
   a table with sortable column headers, reading visible columns from the
   column-definition module for the current density, and ordering rows client-side
   per the Phase-2 default policy until the user picks a column. Reuses the existing
   section collapse + dismissal seams.

5. **Table row (replaces `InstanceRow`).** One job: render one enriched row's
   visible cells (run name, submitter rendered as "you" vs. display name per mode,
   status badge, friendly step, elapsed computed `now − createdAt`, position,
   model/backend, retry) plus an expand affordance. Row click still navigates to
   `ExecutionView` (`/instances/{id}`).

6. **Inline row-detail panel (new).** One job: when expanded, show secondary fields
   (submitter account identity/email in shared mode, full submitted timestamp,
   model/backend, retry detail) and **lazy-load last-error** via the trace endpoint
   only on expand. Collapsed by default; one open panel is independent of the row's
   navigation behavior.

Each unit is independently changeable: density logic is isolated in the hook,
column set in the definition module, last-error fetch in the detail panel.

## Data flow

```
QueueView
  → api.instances.list(...)            → enriched rows (Phase 1 + Phase 2 position)
  → useDensityPreference()             → density (localStorage, default balanced)
  → per state-section:
      QueueSection (table)
        • visible columns = columnDefs filtered by density
        • default order = Phase-2 policy; user can re-sort per column (client-side)
        → TableRow per enriched row
            • renders cells; elapsed = now − createdAt
            • submitter = "you" (local mode) | displayName (shared)
            • expand → RowDetailPanel
                 → (lazy) api trace GetNodeTrace(failedNodeId) for last error
```

Density changes layout only — **no refetch**; the same enriched rows serve all
three views.

## Data Provenance

| UI element | Producer | Status |
| --- | --- | --- |
| All row columns (workflow, run name, submitter, status, step, model/backend, retry, submitted time) | Phase-1 `enrichedSummaryDTO` via `api.instances.list` | ✅ Phase 1 |
| Queue position | Phase-2 `queue.Manager.QueuePositions`, carried on the enriched row | ✅ Phase 2 |
| Elapsed | client-side `now − row.createdAt` (`InstanceSummary.CreatedAt`) | ✅ existing field |
| Submitter "you" vs display name | Phase-1 owner enrichment (display name + account identity) + `domain/identity.Mode` | ✅ Phase 1 |
| Submitter account identity (shared-mode detail) | Phase-1 owner enrichment (email/subject) | ✅ Phase 1 |
| Last error (detail panel) | `domain/instance.NodeExecution.Error` via existing trace endpoint `GetNodeTrace(FailedNodeID)` (`adapter/http/instance_handler.go`) | ✅ existing endpoint |
| Density preference | `localStorage` via `useDensityPreference` (new, client-only) | ✅ no backend dependency |

There are **no `UNRESOLVED DEPENDENCY` items** in this phase: every displayed value
comes from Phase 1, Phase 2, an existing field, or an existing endpoint.

### Deferred to plans

- The exact column-per-density mapping (which columns hide in `dense`, which
  secondary fields inline in `expanded`).
- Exact `localStorage` key scheme (per-user-id keying when identity is known, else
  a global fallback key) and migration/empty-value handling.
- `make ui` rebuild so `adapter/dashboard/dist` reflects the new components.
- Visual/CSS-module specifics (row heights per density, table header styling),
  reusing existing `QueueSection`/`InstanceRow` CSS variables.

## Integration seams

- **Dismissal seam preserved:** the table keeps the existing per-row `dismissed`
  decoration and dismiss/restore controls; sort/group remain independent of
  dismissal (as the current `QueueSection.test.tsx` asserts).
- **Navigation preserved:** row click → `ExecutionView`; the inline detail panel is
  additive and does not replace deep inspection.
- **Client-side ordering only:** consistent with Phases 1–2 — the server supplies
  fields and position; the dashboard owns grouping/sorting.

## Error handling strategy

- **Missing enriched fields degrade gracefully:** any field Phase 1 omitted (best-
  effort) renders as an empty/placeholder cell; the row still shows. No field is
  assumed always-present except the run name and core summary.
- **Density preference:** unreadable/corrupt/absent `localStorage` value falls back
  to `balanced` without throwing; a write failure (e.g. storage disabled) is
  swallowed and the view still works for the session.
- **Last-error lazy-load:** the trace fetch is isolated to the open panel — a
  failure shows an inline "couldn't load details" state and never affects the table
  or other rows; a row with no `FailedNodeID` shows no error section.
- **List fetch failure** reuses `QueueView`'s existing error/loading handling.

## Testing strategy (strict TDD — all five categories required)

Vitest + `@testing-library/react`, near the changed components/hook.

- **Happy-path:** a section renders the full enriched column set in `balanced`
  density, default-ordered per the Phase-2 policy (active by elapsed desc, queued
  by position asc).
- **Success (variations):** density cycling balanced → dense → expanded changes
  visible columns/inline fields without refetch; preference persists and reloads
  from `localStorage`; submitter renders "you" in local mode and display name in
  shared mode; user re-sort by a column overrides the default order.
- **Failure:** last-error lazy-load rejects → inline error state in the panel only;
  table and sibling rows unaffected.
- **Error:** corrupt/absent `localStorage` density value → falls back to `balanced`
  without throwing; storage-write disabled → no crash.
- **Edge-case:** empty section (existing empty state); a row missing optional
  enriched fields (no model, retry 0, no current step) renders placeholders; a
  queued row with a position vs. an active row without one; expand/collapse of the
  detail panel; a row with no `FailedNodeID` shows no error section.
