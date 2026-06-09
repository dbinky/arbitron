# Final Org Gaps — Phase 3 Design: Schedule & Run Visibility

Implements **Capability C** of `docs/specs/final-org-gaps-spec.md`: surface
schedules and scheduled/run-now run history in the dashboard (read-only), while
all schedule management stays on REST + MCP. Also closes the Phase 1 seam by
stamping run origin onto instances.

## Context (existing code)

- `domain/instance/instance.go` — `Instance` carries `Department`, `Visibility`,
  `OwnerID`, timestamps, but **no schedule linkage**. `InstanceSummary` is the
  list projection used by the dashboard.
- `domain/engine/engine.go` — `TriggerWithDepartment` creates and enqueues the
  instance. It resolves the owner from context identity (or the default system
  user for scheduler/webhook triggers). It does **not** know about schedules and
  must not import `domain/schedule`.
- `domain/schedule/evaluator.go` — `fireOne` (cron) and the Phase-1 `Runner`
  (run-now) are the two schedule-originated trigger sites.
- `adapter/http/schedule_handler.go` — `List` (`GET /schedules`) returns the
  caller's schedules (owner-or-admin). `adapter/sqlite` has the instance repo and
  sqlc-generated queries.
- `dashboard/src/routes/{WorkflowsCatalog,WorkflowReview,QueueView,ExecutionView}.tsx`,
  `dashboard/src/api/{client,types}.ts`.

## Design

### A. Run origin via context (no Trigger signature change)

Add a self-owned origin type and context helpers in `domain/instance` (the
package that owns the field), so the engine reads origin without importing
`domain/schedule` — mirroring how identity is threaded into the trigger context.

```go
// domain/instance/origin.go
type TriggerSource string
const (
    OriginManual   TriggerSource = "manual"   // default when nothing is set
    OriginSchedule TriggerSource = "schedule"  // cron tick
    OriginRunNow   TriggerSource = "run_now"   // on-demand fire (Phase 1)
)
type Origin struct {
    Source     TriggerSource
    ScheduleID *uuid.UUID
}
func WithOrigin(ctx context.Context, o Origin) context.Context
func OriginFromContext(ctx context.Context) (Origin, bool)
```

`Instance` gains two fields: `TriggerSource TriggerSource` and `ScheduleID
*uuid.UUID`. `InstanceSummary` gains the same (JSON `triggerSource`,
`scheduleId,omitempty`).

In `TriggerWithDepartment`, when building the instance:
`if o, ok := instance.OriginFromContext(ctx); ok { inst.TriggerSource = o.Source;
inst.ScheduleID = o.ScheduleID } else { inst.TriggerSource = OriginManual }`.
Manual REST/MCP triggers set nothing ⇒ `manual`.

The two schedule sites set the origin before triggering:
- `Evaluator.fireOne`: `ctx = instance.WithOrigin(ctx, Origin{OriginSchedule, &s.ID})`.
- `Runner.Fire` (Phase 1 seam): `Origin{OriginRunNow, &s.ID}`.

### B. Persistence (migration)

Add columns to the `instances` table via a new sqlc migration:
- `trigger_source TEXT NOT NULL DEFAULT 'manual'`
- `schedule_id TEXT NULL` (UUID string; no FK enforced — a schedule may be
  deleted while its historical runs remain).

Update the sqlc insert + the row→`Instance` and row→`InstanceSummary` mappers to
write/read both columns. Backfilled existing rows default to `manual`.

### C. Schedule list filtered by workflow

The dashboard's per-workflow schedule panel needs a workflow-scoped list.
`ListFilters` already has `WorkflowName`; wire it through:
- `Service.List` accepts an optional workflow filter (e.g. a new
  `Service.ListForWorkflow(ctx, workflowName)` or an options arg), keeping the
  **owner-or-admin** creator scoping unchanged (the service still sets
  `CreatorID` from identity for non-admins — never from request input).
- REST: `GET /schedules?workflowName=<name>` passes the query value into the
  filter. No new authz model — a user sees their own (or, as admin, all)
  schedules for that workflow.

### D. Dashboard — read-only surfaces

`dashboard/src/api/types.ts`: extend the instance summary type with
`triggerSource` and optional `scheduleId`; add a `Schedule` type matching
`Schedule.WireMap` (id, workflowName, cronExpr, timezone, creatorId, department,
enabled, lastFiredAt). `client.ts`: add `listSchedules(workflowName?)` and reuse
existing instance listing.

- **`SchedulesPanel`** (new, read-only) rendered in `WorkflowsCatalog` and/or
  `WorkflowReview`: lists the workflow's schedules with cron expression,
  timezone, creator, enabled/disabled state, and last-fired time. No
  create/edit/enable/disable/delete/run controls. Empty state when none.
- **Run-origin annotation** in `QueueView` / `ExecutionView` rows: a small badge
  showing `manual` / `scheduled` / `run-now`, and for schedule-originated runs
  the schedule reference + the attributed creator/department (already on the
  summary). Inherits the existing instance visibility scoping — no new filtering.

### E. Composition / wiring

Origin stamping requires only the engine change (reads ctx) plus the two
schedule sites setting origin; both schedule sites already build the trigger
context, so this is additive. The dashboard consumes existing + extended REST
endpoints.

## Data flow

```
cron tick ─► Evaluator.fireOne ─► WithOrigin(schedule,id)
run-now   ─► Runner.Fire        ─► WithOrigin(run_now,id)        ┐
manual    ─► REST/MCP trigger   ─► (no origin)                    ├─► Engine.TriggerWithDepartment
                                                                  ┘        │ reads OriginFromContext
                                                                           ▼
                                          instance{TriggerSource, ScheduleID} ─► persisted (migration)
                                                                           ▼
                          GET /instances (summary incl. triggerSource, scheduleId)
                          GET /schedules?workflowName= (owner-or-admin scoped)
                                                                           ▼
                  dashboard: SchedulesPanel (workflow views) + origin badge (run history)
```

## Error handling

- Origin absent from context ⇒ `manual` (safe default; manual triggers are the
  common case).
- A `schedule_id` whose schedule was later deleted still renders: the badge shows
  "scheduled (deleted)" using the stored id; no FK, so no orphan error.
- Migration is additive with defaults; existing rows and in-flight instances are
  unaffected.
- Dashboard fetch failures degrade gracefully (panel shows an error/empty state;
  run rows still render without the badge).

## Testing (TDD — 5 scenario categories)

- **Happy path:** a cron fire stamps `trigger_source=schedule` + `schedule_id`; a
  run-now stamps `run_now` + `schedule_id`; a manual trigger stamps `manual`,
  null schedule id. All round-trip through the repo and appear on the summary.
- **Input validation:** `GET /schedules?workflowName=` empty/missing → caller's
  full (owner-scoped) list; unknown workflow name → empty list, not an error.
- **Authorization:** non-admin sees only their own schedules for the workflow;
  admin sees all; instance run history stays within the viewer's existing
  visibility scope; no UI path mutates a schedule.
- **State/migration:** existing instances backfill to `manual`; a deleted
  schedule's historical runs still list with their stored id.
- **Failure modes:** origin context missing → `manual`; dashboard schedule fetch
  error → empty/error panel without breaking the workflow view.
- **Frontend unit tests:** `SchedulesPanel` renders fields read-only and exposes
  no mutation control; origin badge renders the three sources distinctly.

## Out of scope (this phase)

- Any schedule mutation from the dashboard (create/edit/enable/disable/delete/run
  remain REST + MCP only).
- Per-schedule detailed run-history drill-down beyond annotating existing run
  rows with their originating schedule.
- A new visibility/authorization model for schedules (reuses the shipped
  owner-or-admin scoping).
