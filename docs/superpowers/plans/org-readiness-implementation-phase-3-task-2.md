# Org-Readiness — Phase 3, Task 2: SQLite persistence for schedules

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Strict TDD: write each repo test against a real (temp-file/`:memory:`) migrated DB, watch it fail, then implement.

**Goal:** Persist `schedule.Schedule` in SQLite so schedules survive restart. Add migration `013_schedules.up/down.sql`, sqlc queries `queries/schedules.sql` + generated code, and `adapter/sqlite/schedule_repo.go` implementing `schedule.ScheduleRepository`, mirroring `adapter/sqlite/connection_repo.go`.

**Architecture:** Hexagonal driven adapter. The repo translates the domain entity ⇄ generated sqlc rows and maps `sql.ErrNoRows`/unique violations to the domain sentinels from Task 1. JSON-valued (`variables`) and nullable (`last_fired_at`) columns are marshalled in the adapter, exactly as `connection_repo.go` marshals `extras` and as `instances` stores its variable map. A compile-time `var _ schedule.ScheduleRepository = (*ScheduleRepo)(nil)` assertion keeps the port satisfied.

**Tech stack:** Go 1.25.5, SQLite via the project's driver, sqlc (`adapter/sqlite/sqlc.yaml`, `version: "2"`, `engine: "sqlite"`, generated package `generated`), `testing` + `testify`.

---

## Dependencies

- **Task 1** (the `schedule.Schedule` entity, `ScheduleRepository` port, and sentinels). Task 3 (evaluator) and Task 4 (management surface) depend on this repo.

## Spec / design references

- Spec `docs/specs/org-readiness-spec.md` (Phase 3) — "Schedules persist across restarts. Defined schedules survive a restart and continue firing without re-creation."
- Design `docs/superpowers/specs/org-readiness-design-phase-3.md` §"Persistence — SQLite adapter" (migration `013_schedules`, `creator_id` FK to `users`, indexes on `workflow_name`/`creator_id`/`enabled`/`last_fired_at`, mirror `connection_repo.go`, compile-time assertion).

## Concrete migration number

The latest migration in `adapter/sqlite/migrations/` is **`012_instance_visibility`**. This task adds **`013_schedules`**. (Verify before writing: `ls adapter/sqlite/migrations/ | sort | tail`.)

## Files

- **Create:** `adapter/sqlite/migrations/013_schedules.up.sql`
  ```sql
  -- Recurring schedules (org-readiness Phase 3). Each row starts a workflow on a
  -- cron + timezone, attributed to its creator. id/creator_id are TEXT UUIDs to
  -- match users.id and the existing TEXT-uuid convention (see published_workflows).
  CREATE TABLE IF NOT EXISTS schedules (
      id            TEXT PRIMARY KEY,
      workflow_name TEXT NOT NULL,
      cron_expr     TEXT NOT NULL,
      timezone      TEXT NOT NULL DEFAULT 'UTC',
      creator_id    TEXT NOT NULL REFERENCES users(id),
      department    TEXT NOT NULL DEFAULT '',
      enabled       INTEGER NOT NULL DEFAULT 1,   -- 0/1 boolean
      variables     TEXT NOT NULL DEFAULT '{}',   -- JSON object of workflow inputs
      last_fired_at TEXT,                          -- NULL until first fire (RFC3339 UTC)
      created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
      updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
  );

  CREATE INDEX IF NOT EXISTS idx_schedules_creator       ON schedules(creator_id);
  CREATE INDEX IF NOT EXISTS idx_schedules_workflow_name ON schedules(workflow_name);
  -- ListEnabled is the evaluator's hot path: enabled rows ordered for a stable scan.
  CREATE INDEX IF NOT EXISTS idx_schedules_enabled       ON schedules(enabled, last_fired_at);
  ```
- **Create:** `adapter/sqlite/migrations/013_schedules.down.sql`
  ```sql
  DROP INDEX IF EXISTS idx_schedules_enabled;
  DROP INDEX IF EXISTS idx_schedules_workflow_name;
  DROP INDEX IF EXISTS idx_schedules_creator;
  DROP TABLE IF EXISTS schedules;
  ```
  (A `DROP TABLE` down is appropriate here because the whole table is new — unlike the `012` ALTER-COLUMN forward-only note.)
- **Create:** `adapter/sqlite/queries/schedules.sql` (sqlc; mirror the `:exec`/`:one`/`:many` annotations and `strftime` `updated_at` idiom from `connections.sql`):
  ```sql
  -- name: CreateSchedule :exec
  INSERT INTO schedules (id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?);

  -- name: GetSchedule :one
  SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
  FROM schedules WHERE id = ?;

  -- name: ListSchedules :many
  SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
  FROM schedules
  WHERE (sqlc.narg('creator_id')    IS NULL OR creator_id    = sqlc.narg('creator_id'))
    AND (sqlc.narg('workflow_name') IS NULL OR workflow_name = sqlc.narg('workflow_name'))
    AND (sqlc.narg('enabled')       IS NULL OR enabled       = sqlc.narg('enabled'))
  ORDER BY created_at DESC, id;

  -- name: ListEnabledSchedules :many
  SELECT id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at
  FROM schedules WHERE enabled = 1 ORDER BY id;

  -- name: UpdateSchedule :exec
  UPDATE schedules
  SET workflow_name = ?, cron_expr = ?, timezone = ?, department = ?, enabled = ?, variables = ?,
      updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ?;

  -- name: SetScheduleLastFired :exec
  UPDATE schedules SET last_fired_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;

  -- name: DeleteSchedule :exec
  DELETE FROM schedules WHERE id = ?;
  ```
  - Add any needed `rename:` entries to `adapter/sqlite/sqlc.yaml` only if sqlc's default Go naming is unsatisfactory (it should not be for these snake_case columns; `cron_expr` → `CronExpr`, `last_fired_at` → `LastFiredAt` are fine).
- **Regenerate:** run the project's sqlc generation (the same command other tasks use, e.g. `make sqlc` or `sqlc generate -f adapter/sqlite/sqlc.yaml`). Commit the generated `adapter/sqlite/generated/*` changes. **Do not hand-edit generated files.**
- **Create:** `adapter/sqlite/schedule_repo.go`
  - `type ScheduleRepo struct { db *sql.DB; queries *generated.Queries }`.
  - `var _ schedule.ScheduleRepository = (*ScheduleRepo)(nil)` (compile-time assertion, mirroring `connection_repo.go` line 25).
  - `func NewScheduleRepo(db *sql.DB) *ScheduleRepo { return &ScheduleRepo{db: db, queries: generated.New(db)} }`.
  - `Create`: marshal `Variables` → JSON (`{}` when nil — reuse/mirror the `extrasToJSON` helper pattern), convert `enabled` bool ⇄ int64 (or rely on the driver's bool support if `generated` types it as `bool`), `uuid → String()`. Map unique-PK violation → `schedule.ErrExists` via the existing `isUniqueViolation` helper in `connection_repo.go` (reuse it; it lives in the same package).
  - `Get`: map `sql.ErrNoRows` → `schedule.ErrNotFound`; unmarshal JSON variables; parse `creator_id` via `uuid.Parse`; parse `last_fired_at` (nullable RFC3339) → `*time.Time`; parse `created_at`/`updated_at`.
  - `List`: pass `ListFilters` through to `ListSchedules` as `sqlc.narg` nullable params (creator_id as `*string` of the uuid, workflow_name `*string`, enabled `*bool`/`*int64`). Return `[]Schedule`.
  - `ListEnabled`: call `ListEnabledSchedules`; same row→entity mapping.
  - `Update`: pre-check existence via `GetSchedule` so a missing id maps to `schedule.ErrNotFound` (mirror `connection_repo.go` Update's pre-check), then `UpdateSchedule`.
  - `SetLastFired`: format `at.UTC()` as RFC3339 and call `SetScheduleLastFired`. A zero-row update (missing id) → `schedule.ErrNotFound` (pre-check or check rows-affected).
  - `Delete`: pre-check existence → `schedule.ErrNotFound`; then `DeleteSchedule`.
  - A private `rowToSchedule(generated.Schedule) (schedule.Schedule, error)` mapper keeps Get/List/ListEnabled DRY.
- **Test:** `adapter/sqlite/schedule_repo_test.go` (and add schedule coverage to `adapter/sqlite/integration_test.go` if that file is the canonical migration-applied-DB harness — check how `notification_repo_test.go` / `connection_repo` tests obtain a migrated `*sql.DB`).

## TDD test list (five categories)

Tests run against a real migrated SQLite DB (temp file or `:memory:`) using whatever migration-applying test helper the package already exposes (find it via `grep -n "func.*testDB\|applyMigrations\|openTestDB" adapter/sqlite/*_test.go`). A schedule's `creator_id` FK requires a user row — seed a user first using the existing user/test-fixture helper (mirror how instance/notification repo tests seed an owner).

### Happy-path

- [ ] `TestScheduleRepo_CreateThenGet_RoundTrip` — create a schedule (cron, tz `America/New_York`, department `eng`, variables `{"x":1}`, enabled true), `Get(id)`, assert every field round-trips incl. the JSON variables map and a nil `LastFiredAt`.
- [ ] `TestScheduleRepo_List_FiltersByCreator` — seed two users + a schedule each; `List(ListFilters{CreatorID:&u1})` returns only u1's; `List(ListFilters{})` (nil creator = admin) returns both.
- [ ] `TestScheduleRepo_ListEnabled_OmitsDisabled` — one enabled + one disabled; `ListEnabled` returns only the enabled one.

### Input-validation (persistence-level)

- [ ] `TestScheduleRepo_Create_DuplicateID_ReturnsErrExists` — insert the same `ID` twice ⇒ `assert.ErrorIs(err, schedule.ErrExists)`.
- [ ] `TestScheduleRepo_List_FilterByEnabledAndWorkflow` — `ListFilters{Enabled:&true, WorkflowName:&"report"}` returns only matching rows (proves the `narg` filter wiring).

### Error-handling

- [ ] `TestScheduleRepo_Get_NotFound` — random uuid ⇒ `assert.ErrorIs(err, schedule.ErrNotFound)`.
- [ ] `TestScheduleRepo_Update_NotFound` / `TestScheduleRepo_Delete_NotFound` / `TestScheduleRepo_SetLastFired_NotFound` — each ⇒ `schedule.ErrNotFound`.

### Edge-cases / boundary

- [ ] `TestScheduleRepo_Create_NilVariables_StoresEmptyObject` — `Variables: nil` persists as `{}` and `Get` returns a non-nil empty map (no `null`-unmarshal crash).
- [ ] `TestScheduleRepo_SetLastFired_ThenGet` — `SetLastFired(id, t)`, then `Get` returns `*LastFiredAt` equal to `t` truncated to second precision (RFC3339 round-trip); assert the value survives and is in UTC.
- [ ] `TestScheduleRepo_Update_TogglesEnabled` — update a schedule from enabled→disabled and back; `Get` and `ListEnabled` reflect each toggle (this is the enable/disable management primitive Task 4 calls).

### Integration (survives restart)

- [ ] `TestScheduleRepo_PersistsAcrossReopen` — create a schedule, close the `*sql.DB`, re-open the SAME backing file, re-apply migrations (idempotent `IF NOT EXISTS`), and `Get(id)` still returns it with all fields intact. Directly exercises the spec's "schedules defined before a restart are still present afterward."
- [ ] `TestScheduleRepo_SatisfiesPort` — `var _ schedule.ScheduleRepository = NewScheduleRepo(db)` compile + a smoke call through the interface type.

## Implementation steps (after each test goes red)

1. [ ] Write `013_schedules.up/down.sql`; confirm the test harness applies them cleanly.
2. [ ] Write `queries/schedules.sql`; run sqlc generation; confirm `generated.Schedule` + query methods exist.
3. [ ] Write `schedule_repo.go` Create + `rowToSchedule` + Get to pass round-trip/not-found.
4. [ ] Add List/ListEnabled with filter wiring.
5. [ ] Add Update (pre-check)/SetLastFired/Delete with not-found mapping.
6. [ ] `go test ./adapter/sqlite/...` green; `make test` stays green.

## Acceptance criteria

- A schedule round-trips through SQLite with all fields (incl. JSON variables, nullable `LastFiredAt`, timezone, department) intact, and survives a DB close/re-open — satisfying the spec's persistence-across-restart requirement.
- `Get`/`Update`/`Delete`/`SetLastFired` on a missing id return `schedule.ErrNotFound`; a duplicate id returns `schedule.ErrExists`.
- `List` honours the creator/workflow/enabled filters (nil creator = admin view); `ListEnabled` returns only enabled rows.
- `ScheduleRepo` satisfies `schedule.ScheduleRepository` (compile-time assertion present).
- sqlc generated code is regenerated (not hand-edited) and `go build ./...` is green.

## Notes

- `creator_id` is a TEXT-uuid FK to `users(id)` (the same convention as `published_workflows.publisher_id`), so deleting a user with live schedules is constrained by the FK — acceptable for this single-instance feature; user lifecycle / cascade is out of scope.
- The `enabled` 0/1 column and `variables` JSON column follow the existing instances/connections conventions; the adapter owns all (de)serialization so the domain entity stays driver-agnostic.
- `last_fired_at` is the persisted scheduling position that Task 3's recovery reads to compute the next fire deterministically (no-backfill), so its precision/round-trip is load-bearing — hence the explicit round-trip test.
