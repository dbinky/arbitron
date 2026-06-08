# Org-Readiness Design — Phase 3: Scheduled / Recurring Triggers

**Spec:** `docs/specs/org-readiness-spec.md` (Phase 3)
**Phase order:** 3 of 3 (Auth → Agent/Provider → Triggers)

## Overview

Add the ability to start any workflow — including script-only, no-LLM workflows —
on a recurring schedule expressed as a **cron expression with a timezone**.
Schedules are persisted, survive restart, and run unattended on the single
instance. Each scheduled run is **attributed to the schedule's creator** (owner +
department), reusing the existing trigger-stamping. Collisions (prior run still
active) are **skipped**; occurrences missed during downtime are **not
backfilled**; both raise an alert.

This is distinct from the existing in-graph `timer_delay` node (a wait inside a
running instance) and the existing manual `instance_trigger`. It is a new
subsystem that *starts* instances on a clock.

## Scope

In scope: a `domain/schedule` package (entity + repository port), SQLite
persistence, an in-process cron evaluator wired in the composition root, trigger
integration as the creator, skip/misfire policies + alerts, and REST + MCP
management with authz.

Out of scope (deferred per spec): a scheduling dashboard UI (API/MCP only here);
multi-instance/distributed scheduling (single in-process evaluator is sufficient);
optional "run now" convenience (may be added with the management surface).

## Dependency

Add **`github.com/robfig/cron/v3`** (pure-Go, Apache-2.0) for cron parsing +
timezone-aware next-fire computation. Only the parser/schedule types are used;
the run loop is our own (so it integrates with the queue + lifecycle).

## Architecture & components

Hexagonal: a new domain entity + repository port, a SQLite adapter, an
application-level evaluator (driving the existing engine port), and REST/MCP
driving adapters.

### Domain — `domain/schedule`

- `Schedule` entity: `ID`, `WorkflowName`, `CronExpr`, `Timezone` (IANA, default
  `UTC`), `CreatorID`, `Department`, `Enabled`, `Variables map[string]any`,
  `LastFiredAt *time.Time`, `CreatedAt`, `UpdatedAt`.
- `ScheduleRepository` port: `Create`, `Get`, `List`, `ListEnabled`, `Update`,
  `Delete`, `SetLastFired`.
- Validation: cron expression parses; timezone resolves via `time.LoadLocation`;
  workflow name non-empty.

### Persistence — SQLite adapter

- Migration `adapter/sqlite/migrations/013_schedules.up.sql` (+ down): `schedules`
  table mirroring the entity, with `creator_id` FK to `users`, indexes on
  `workflow_name` and `creator_id`, `enabled`, and `last_fired_at`.
- `adapter/sqlite/queries/schedules.sql` (sqlc) + generated code.
- `adapter/sqlite/schedule_repo.go` implementing `ScheduleRepository`, mirroring
  `connection_repo.go` (compile-time `var _ schedule.ScheduleRepository`).

### Evaluator — in-process, single instance

A new application service (e.g. `domain/schedule/evaluator.go` with the engine as
a port), started in the composition root beside the queue scheduler:

- **Loop:** a ticker (e.g. every 30s — coarse enough to be cheap, fine enough for
  minute-granularity cron) computes, per enabled schedule, the next fire time
  from `robfig/cron` in the schedule's timezone; when due, fire.
- **Fire (trigger as creator):** inject the creator into the context
  (`identity.WithIdentity(ctx, identity.Identity{UserID: schedule.CreatorID, ...})`)
  then call the **existing** `Engine.TriggerWithDepartment(ctx, workflow,
  variables, "normal", schedule.Department)`. `triggerOwner` resolves the creator
  and `resolveStamp` stamps owner + department/visibility exactly as a manual
  trigger would. Record `LastFiredAt`.
- **Collision policy (skip when busy):** before firing, check for an active
  instance of the same workflow for this schedule (count running/queued); if
  present, skip this occurrence and raise a `schedule_skipped` alert. (A
  per-schedule "active run" check keyed by schedule id/workflow.)
- **Misfire policy (no backfill):** on startup recovery, for each schedule
  compute the next fire from `LastFiredAt`; if it is already in the past, do
  **not** run it — advance to the next future occurrence and raise a
  `schedule_missed` alert noting the gap.
- **Persistence of position:** next-fire is derived (cron + timezone +
  `LastFiredAt`), so a restart recomputes deterministically; no in-memory-only
  state is authoritative.
- **Lifecycle:** `Start(ctx)`/`Stop()` mirror `domain/queue/scheduler.go`; wired
  into the `domainStack` startup + graceful `Close()` (stop evaluator before the
  engine drains).

### Alerts — reuse notifications

- Add `notification.Type` values `schedule_skipped` and `schedule_missed`.
- On skip/miss, create a notification via the existing repository and the
  `ExternalNotifier` (Teams) path, so operators see that a run did not happen.

### Management surface — REST + MCP

- REST under the workflow/schedule namespace: create / list / get / update /
  enable-disable / delete schedules (parallel to the trigger handlers in
  `adapter/http/workflow_handler.go`).
- MCP tools mirroring the same operations (parallel to `instance_trigger` in
  `adapter/mcp/tools_instance.go`), e.g. `schedule_create`, `schedule_list`,
  `schedule_update`, `schedule_delete`.
- **Authz:** a user may create/manage a schedule for a workflow they are
  permitted to trigger; a user manages **their own** schedules; admins manage
  all. Reuse the request identity + the same publication/visibility checks the
  trigger path uses.

## Data flow (scheduled run)

1. Evaluator tick → schedule due (cron+tz, not currently busy).
2. Inject creator identity into context → `Engine.TriggerWithDepartment`.
3. Engine stamps owner = creator, department/visibility per publication +
   creator, enqueues the instance (normal capacity rules apply).
4. `SetLastFired` persists the fire time. Skips/misses emit notifications.

## Error handling / edge cases

- **Invalid cron / timezone** rejected at create/update (validation error), never
  persisted.
- **Disabled schedules** are skipped by the evaluator but retained.
- **Deleted workflow:** firing a schedule whose workflow no longer exists fails
  the trigger gracefully and raises an alert; the schedule may be disabled.
- **DST / timezone correctness:** next-fire computed in the schedule's IANA zone
  via `robfig/cron` + `time.LoadLocation` (e.g. "weekdays 09:00 America/New_York"
  tracks DST).
- **Restart during a due window:** no backfill; alert on miss; resume next.
- **Capacity backpressure:** scheduled instances enqueue like any other; the
  queue governs concurrency.

## Testing (strict TDD)

Across the project's scenario categories (happy/validation/error/boundary/
integration), using an injected clock so time is deterministic:

- **Cron next-fire**: standard expressions; per-timezone; DST boundary; invalid
  expr / invalid tz rejected.
- **Evaluator fire**: due schedule triggers as creator (owner + department
  stamped); variables passed through; `LastFiredAt` updated.
- **Collision**: prior run active → skip + `schedule_skipped` notification, no
  new instance.
- **Misfire/recovery**: `LastFiredAt` in the past on startup → no backfill,
  `schedule_missed` alert, next future fire scheduled.
- **Repository**: round-trip CRUD; enable/disable; list-enabled; FK to creator;
  unique-constraint / not-found errors.
- **Management authz**: non-admin manages own only; admin manages all; cannot
  schedule a workflow one cannot trigger.
- **Script-only workflow**: a no-LLM script workflow fires and runs to
  completion on schedule.

## Integration points (anchors)

- `domain/engine/engine.go` (`triggerOwner` 156-169, `TriggerWithDepartment`
  226-373), `domain/engine/trigger_stamp.go` (`resolveStamp`)
- `domain/queue/scheduler.go` / `recovery.go` (evaluator lifecycle pattern)
- `domain/workflow/workflow.go` + `validation.go` (script-only entrypoints valid)
- `domain/notification/notification.go` (new types), `domain/notification/ports.go`
  + `adapter/notification` (Teams)
- `adapter/sqlite/migrations/*`, `adapter/sqlite/queries/*`,
  `adapter/sqlite/connection_repo.go` (adapter pattern)
- `adapter/http/workflow_handler.go` (trigger handlers to parallel),
  `adapter/mcp/tools_instance.go` (MCP tools to parallel)
- `cmd/arbitron/main.go` (composition root: start/stop evaluator; recovery)
- `go.mod` (add `github.com/robfig/cron/v3`)

## Acceptance (from spec)

Cron schedule starts a (incl. script-only) workflow on time in its timezone;
overlapping/missed ticks are skipped/not-backfilled and alert; scheduled run
attributed to creator; non-admins manage own schedules, admins manage all;
schedules survive restart.
