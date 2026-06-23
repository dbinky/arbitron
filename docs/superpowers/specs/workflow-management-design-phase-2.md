# Workflow Management — Design Phase 2: Reference Decoupling (Instances · Schedules · Publications by ID)

## Scope
Repoint the three name-referencing stores onto the stable workflow ID from Phase 1,
so a later rename or in-place edit cannot break a running instance, a schedule, or a
publication. No admin lifecycle operations yet (Phase 3).

## Architecture & components
- **Schema + backfill.** Migrations add a nullable `workflow_id` column to
  `instances`, `schedules`, and `published_workflows`; a backfill populates each from
  its existing `workflow_name` via the Phase-1 registry. The `workflow_name` columns
  are retained as display labels / snapshots, not as the key.
- **Instances** (`domain/instance`, `engine.TriggerWithDepartment`). The trigger path
  resolves name→id and stamps `WorkflowID` on the instance alongside the existing
  `WorkflowName` (display label) and `WorkflowSnapshot` (execution copy). Rerun keys
  off `WorkflowID`.
- **Schedules** (`domain/schedule`). Stored and fired by `WorkflowID`; the cron
  evaluator resolves id→current name→definition at fire time, so a schedule keeps
  firing across renames.
- **Publications** (`published_workflows`). Add `workflow_id` as the real link to the
  workflow; `name`/`source_name` are retained as the published label. Publish /
  SetVisibility / Copy carry the id.

## Data flow
`trigger(name)` → `resolveID` → `instance{WorkflowID, WorkflowName(label),
WorkflowSnapshot}`. Schedule fire → load `schedule.WorkflowID` → `resolveName(id)` →
load definition → trigger. Publish → registry id linked into `published_workflows`.

## Integration seams
- The Phase-1 registry is the single resolver used by the trigger path, the cron
  evaluator, and the publish path.
- Backfill is a migration-time concern. Rows whose name no longer resolves (the
  workflow was already deleted) keep `workflow_id` null and are treated as dangling
  historical labels — never a crash.

## Error handling
- Dangling reference (id resolves to nothing, or a legacy null id) → schedule fire
  logs and skips gracefully (loop continues); instance history with a null id renders
  by its stored label.
- Backfill is idempotent and best-effort; unresolved names are logged, not fatal.

## Testing strategy — strict TDD (REQUIRED, all five categories)
- **Happy-path:** trigger stamps `WorkflowID`; a new schedule is stored with
  `WorkflowID`.
- **Success:** after a registry name change (simulating a rename), firing the
  schedule / rerunning the instance still resolves to the correct definition.
- **Failure:** a schedule whose workflow was deleted → fire skips with a logged
  error, the cron loop continues.
- **Error:** backfill of a row whose name doesn't resolve → left null, logged.
- **Edge:** legacy instance/schedule rows (name present, id null) coexist and still
  render/operate alongside id-stamped rows.

Test gate: `go test ./...`.
