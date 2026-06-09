# Final Org Gaps — Phase 3, Task 2: Persist Run Origin (migration + repo + summary API)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `trigger_source` + `schedule_id` columns to the `instances` table (additive migration), write/read both through the SQLite repo and the hand-maintained sqlc-generated code, surface them on `InstanceSummary`, and expose them on the `GET /instances` JSON (`triggerSource`, optional `scheduleId`). Existing rows backfill to `manual`; a deleted schedule's historical runs still list with their stored id (no FK).

**Architecture:** Forward-only additive migration (matching `012_instance_visibility`). The project's `generated/` sqlc output is hand-edited when sqlc is unavailable (documented in `adapter/sqlite/schedule_repo.go`); this task edits both `queries/instances.sql` (source of truth for a future `sqlc generate`) AND the hand-maintained `generated/instances.sql.go` + `generated/models.go`. The HTTP `summaryDTO` gains the two fields so the dashboard (Task 4) can consume them.

**Tech Stack:** Go, SQLite, sqlc-style generated code (hand-maintained), `database/sql`, `github.com/google/uuid`.

---

## DEPENDENCY

Requires **Phase 3 Task 1** (`...-phase-3-task-1.md`) — `instance.Instance` and `instance.InstanceSummary` must already carry `TriggerSource` and `ScheduleID`, and the engine must already stamp them. This task makes those in-memory fields persist. Independent of Phases 1 and 2.

## Context (existing code you must match)

- Migrations live in `adapter/sqlite/migrations/NNN_name.up.sql` / `.down.sql`. The latest is `013_schedules`. Add `014_instance_trigger_origin`. Follow `012_instance_visibility.up.sql` exactly for column-add + index style and its forward-only down file.
- `adapter/sqlite/queries/instances.sql` — sqlc query source. `CreateInstance` (line 1) and `ListInstances` (line 9) need the two columns.
- `adapter/sqlite/generated/models.go` — `Instance` struct at line 50 (hand-maintained; sqlc is NOT on PATH).
- `adapter/sqlite/generated/instances.sql.go` — `createInstance` SQL + `CreateInstanceParams` (line 29), `getInstance` (line 89), `listInstances` SQL + `ListInstancesParams` + `ListInstancesRow` (line 121), and the `RETURNING ...` / `SELECT ...` column lists and `row.Scan(...)` calls. ALL must be updated in lockstep — a column added to the SQL but not the `Scan` list is a runtime scan-mismatch panic.
- `adapter/sqlite/instance_repo.go` — `Create` (line 30, builds `CreateInstanceParams`), `List` (line 93, builds `InstanceSummary`), `rowToInstance` (line 369). Helpers in `adapter/sqlite/convert.go`: `uuidToString`, `parseUUID`, `nullStringToPtr`, `ptrToNullString`, `nullStringToString`.
- `adapter/http/instance_handler.go` — `summaryDTO` (line 166) + `toSummaryDTO` (line 180).

Note the order of generated `Instance` columns: `... WebhookReceivedAt, Department, Visibility`. Append the two new columns AFTER `Visibility` in every list (struct, SQL column lists, Scan calls) so positional scans stay aligned.

---

### Task 1: Additive migration

**Files:**
- Create: `adapter/sqlite/migrations/014_instance_trigger_origin.up.sql`
- Create: `adapter/sqlite/migrations/014_instance_trigger_origin.down.sql`
- Test: `adapter/sqlite/instance_trigger_origin_migration_test.go` (create — model on `adapter/sqlite/instance_visibility_migration_test.go`)

- [ ] **Step 1: Write the failing migration test**

Inspect `adapter/sqlite/instance_visibility_migration_test.go` to reuse its DB-open + migrate helper. Create `adapter/sqlite/instance_trigger_origin_migration_test.go` asserting:

```go
// After migrating up:
//  1. PRAGMA table_info(instances) includes a column "trigger_source" (TEXT,
//     NOT NULL, default 'manual') and a column "schedule_id" (nullable).
//  2. A row inserted WITHOUT specifying trigger_source reads back
//     trigger_source = 'manual' (the DEFAULT backfills existing/legacy rows).
//  3. schedule_id accepts NULL and a UUID string.
```

Write real assertions: open a migrated DB, `INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables) VALUES (...)` (minimal columns), then `SELECT trigger_source, schedule_id FROM instances WHERE id=?` and assert `'manual'` + NULL. Use the same minimal-insert pattern the visibility migration test uses for backfill verification.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/sqlite/ -run TriggerOrigin -v`
Expected: FAIL — no such column `trigger_source` (migration 014 does not exist yet).

- [ ] **Step 3: Write the up migration**

Create `adapter/sqlite/migrations/014_instance_trigger_origin.up.sql`:

```sql
-- Record each run's provenance (Phase 3 — schedule & run visibility).
-- trigger_source: how the run started — 'manual' (REST/MCP), 'schedule' (cron
--   tick), or 'run_now' (on-demand fire). Existing rows backfill to 'manual'
--   via the column DEFAULT.
-- schedule_id: the originating schedule's UUID for schedule/run_now runs; NULL
--   for manual runs. NO foreign key is enforced: a schedule may be deleted while
--   its historical runs remain (the dashboard renders "scheduled (deleted)").
ALTER TABLE instances ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual'
    CHECK (trigger_source IN ('manual','schedule','run_now'));
ALTER TABLE instances ADD COLUMN schedule_id TEXT;  -- NULL => manual run

-- Index the schedule linkage so per-schedule run lookups (and the dashboard's
-- schedule-origin annotation) are index-backed. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_schedule_id
    ON instances (schedule_id, created_at);
```

- [ ] **Step 4: Write the down migration (forward-only convention)**

Create `adapter/sqlite/migrations/014_instance_trigger_origin.down.sql`:

```sql
DROP INDEX IF EXISTS idx_instances_schedule_id;
-- SQLite cannot DROP COLUMN on older versions used by some deployments; rebuild
-- is out of scope for a down migration. Recreating the table is intentionally
-- omitted — forward-only is the project convention (see 012_instance_visibility).
```

- [ ] **Step 5: Run to verify the migration test passes**

Run: `go test ./adapter/sqlite/ -run TriggerOrigin -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add adapter/sqlite/migrations/014_instance_trigger_origin.up.sql adapter/sqlite/migrations/014_instance_trigger_origin.down.sql adapter/sqlite/instance_trigger_origin_migration_test.go
git commit -m "feat(sqlite): migration adding trigger_source and schedule_id to instances"
```

---

### Task 2: Update the sqlc query source

**Files:**
- Modify: `adapter/sqlite/queries/instances.sql` (`CreateInstance` line 1, `ListInstances` line 9)

This file is the source for a future `sqlc generate`; keep it in sync even though we hand-edit the generated output.

- [ ] **Step 7: Update `CreateInstance`**

In `adapter/sqlite/queries/instances.sql`, change the `CreateInstance` insert (lines 1–4) to:

```sql
-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;
```

- [ ] **Step 8: Update `ListInstances` projection**

In the same file, change the `ListInstances` SELECT projection (line 19) to append the two columns:

```sql
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id
```

(Leave the WHERE/ORDER/LIMIT clauses unchanged.)

- [ ] **Step 9: Commit the query source**

```bash
git add adapter/sqlite/queries/instances.sql
git commit -m "chore(sqlite): add trigger_source/schedule_id to instance query source"
```

---

### Task 3: Hand-update the generated sqlc code

**Files:**
- Modify: `adapter/sqlite/generated/models.go` (`Instance` struct, line 50)
- Modify: `adapter/sqlite/generated/instances.sql.go` (`createInstance`, `getInstance`, `listInstances`)

Since sqlc is not on PATH, edit the generated files by hand (the documented project fallback). There is no separate test for this step — it is verified by the repo round-trip test in Task 4.

- [ ] **Step 10: Add columns to the generated `Instance` model**

In `adapter/sqlite/generated/models.go`, in the `Instance` struct (ends with `Visibility string` at line 70), add after `Visibility`:

```go
	TriggerSource string
	ScheduleID    sql.NullString
```

- [ ] **Step 11: Update `createInstance` SQL + params + scan**

In `adapter/sqlite/generated/instances.sql.go`:

(a) Change the `createInstance` const (lines 29–33):

```go
const createInstance = `-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility, trigger_source, schedule_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, current_node_id, failed_node_id, created_at, updated_at, completed_at, queued_at, waiting_until, webhook_token, webhook_secret_hash, webhook_received_at, department, visibility, trigger_source, schedule_id
`
```

(b) Add to `CreateInstanceParams` (after `Visibility string`, line 46):

```go
	TriggerSource     string
	ScheduleID        sql.NullString
```

(c) Add to the `QueryRowContext` arg list in `CreateInstance` (after `arg.Visibility,`, line 61):

```go
		arg.TriggerSource,
		arg.ScheduleID,
```

(d) Add to the `row.Scan(...)` list (after `&i.Visibility,`, line 84):

```go
		&i.TriggerSource,
		&i.ScheduleID,
```

- [ ] **Step 12: Update `getInstance` SQL + scan**

In the same file:

(a) Change the `getInstance` const SELECT (line 90) to append `, trigger_source, schedule_id` before `FROM instances`:

```go
const getInstance = `-- name: GetInstance :one
SELECT id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, current_node_id, failed_node_id, created_at, updated_at, completed_at, queued_at, waiting_until, webhook_token, webhook_secret_hash, webhook_received_at, department, visibility, trigger_source, schedule_id FROM instances WHERE id = ?
`
```

(b) Add to its `row.Scan(...)` (after `&i.Visibility,`, line 116):

```go
		&i.TriggerSource,
		&i.ScheduleID,
```

- [ ] **Step 13: Update `listInstances` SQL + row type + scan**

In the same file:

(a) Change the `listInstances` const projection (line 122) to:

```go
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at, department, visibility, trigger_source, schedule_id
```

(b) Add to `ListInstancesRow` (after `Visibility string`, line 162):

```go
	TriggerSource string
	ScheduleID    sql.NullString
```

(c) Add to the per-row `i.Scan(...)` in `ListInstances` (after `&i.Visibility,`, line 204):

```go
			&i.TriggerSource,
			&i.ScheduleID,
```

- [ ] **Step 14: Also update `listRunningInstances` scan (positional safety)**

`listRunningInstances` (line 219) selects `*`-equivalent columns ending `..., department, visibility` and scans into the full `Instance` struct. Since the `Instance` struct now has two MORE fields, its SELECT projection and Scan must also grow or scans misalign.

(a) Append `, trigger_source, schedule_id` to the `listRunningInstances` const SELECT (before `FROM instances WHERE state = 'running'`).

(b) Add to its `row.Scan(...)` list (after `&i.Visibility,`) the same two `&i.TriggerSource,` / `&i.ScheduleID,` lines.

> Search the file for EVERY `Scan(` that targets a full `generated.Instance` (createInstance, getInstance, listRunningInstances, and any other `SELECT * ... FROM instances`). Each must scan the two new trailing columns. A `ListInstancesRow` scan only needs the two list columns. Verify with: `grep -n "&i.Visibility" adapter/sqlite/generated/instances.sql.go` and ensure each is immediately followed by the two new fields.

- [ ] **Step 15: Build the package**

Run: `go build ./adapter/sqlite/...`
Expected: exit 0 (compiles). Runtime correctness is checked in Task 4.

- [ ] **Step 16: Commit**

```bash
git add adapter/sqlite/generated/models.go adapter/sqlite/generated/instances.sql.go
git commit -m "chore(sqlite): hand-regenerate instance queries for trigger_source/schedule_id"
```

---

### Task 4: Repo writes and reads the origin fields

**Files:**
- Modify: `adapter/sqlite/instance_repo.go` (`Create` line 52, `List` line 164, `rowToInstance` line 416)
- Test: `adapter/sqlite/instance_repo_origin_test.go` (create)

- [ ] **Step 17: Write the failing round-trip test (happy path, all three sources, + deleted-schedule)**

Inspect `adapter/sqlite/instance_repo_test.go` / `instance_repo_visibility_test.go` for the harness that opens a migrated DB and builds an `InstanceRepo`. Create `adapter/sqlite/instance_repo_origin_test.go` with tests asserting:

```go
// Reuse the package's migrated-DB + repo + seeded-owner harness.
//
// 1. Create an instance with TriggerSource=OriginSchedule, ScheduleID=&sid;
//    repo.Get round-trips both fields; repo.List returns a summary with the
//    same TriggerSource + ScheduleID.
// 2. Create with TriggerSource=OriginRunNow, ScheduleID=&sid -> round-trips.
// 3. Create with TriggerSource=OriginManual, ScheduleID=nil -> Get returns
//    OriginManual + nil ScheduleID; summary likewise.
// 4. Deleted-schedule: create an instance with ScheduleID=&sid, then never
//    create/already-deleted that schedule; List still returns the row with its
//    stored ScheduleID (no FK, no error). (There is no schedules FK to violate;
//    this asserts the run lists fine with an arbitrary schedule id.)
```

Write these as real subtests. For the engine path you may construct the `instance.Instance` directly and call `repo.Create` (the repo is the unit under test, not the engine).

- [ ] **Step 18: Run to verify it fails**

Run: `go test ./adapter/sqlite/ -run InstanceRepoOrigin -v`
Expected: FAIL — `Create` doesn't pass the new params; `Get`/`List` return zero-value `TriggerSource`.

- [ ] **Step 19: Add a `scheduleIDToNullString` helper (or reuse `ptrToNullString`)**

`ScheduleID` is a `*uuid.UUID`. Add a small helper to `adapter/sqlite/convert.go` (near `ptrToNullString`):

```go
// scheduleIDToNullString maps a nilable schedule UUID to a nullable SQL string:
// nil (manual run) => SQL NULL; a value => its canonical UUID string.
func scheduleIDToNullString(id *uuid.UUID) sql.NullString {
	if id == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// nullStringToScheduleID maps a nullable SQL string back to *uuid.UUID; NULL or
// an unparseable value => nil (a deleted schedule's id is still a valid UUID
// string, so this round-trips it).
func nullStringToScheduleID(ns sql.NullString) (*uuid.UUID, error) {
	if !ns.Valid {
		return nil, nil
	}
	id, err := uuid.Parse(ns.String)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
```

Ensure `adapter/sqlite/convert.go` imports `database/sql` and `github.com/google/uuid` (it already uses both).

- [ ] **Step 20: Default + write the fields in `Create`**

In `adapter/sqlite/instance_repo.go` `Create`, after the existing `visibility` defaulting block (line ~47–50), add:

```go
	// Default an unset trigger source to manual, matching the column default and
	// the engine's OriginManual default. ScheduleID stays nil (NULL) for manual.
	triggerSource := inst.TriggerSource
	if triggerSource == "" {
		triggerSource = instance.OriginManual
	}
```

Then add to the `generated.CreateInstanceParams{...}` literal (after `Visibility: string(visibility),`, line 63):

```go
		TriggerSource: string(triggerSource),
		ScheduleID:    scheduleIDToNullString(inst.ScheduleID),
```

- [ ] **Step 21: Read the fields in `rowToInstance`**

In `rowToInstance` (line ~416), before the final `return &instance.Instance{...}`, add:

```go
	scheduleID, err := nullStringToScheduleID(row.ScheduleID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
```

Then add to the returned `&instance.Instance{...}` literal (after `Visibility: ...,`):

```go
		TriggerSource:     instance.TriggerSource(row.TriggerSource),
		ScheduleID:        scheduleID,
```

- [ ] **Step 22: Read the fields in `List` (summary projection)**

In `List` (line ~164), the loop builds `instance.InstanceSummary{...}`. Before constructing the summary inside the loop, add:

```go
		scheduleID, err := nullStringToScheduleID(row.ScheduleID)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
```

Then add to the `instance.InstanceSummary{...}` literal (after `Visibility: ...,`):

```go
			TriggerSource: instance.TriggerSource(row.TriggerSource),
			ScheduleID:    scheduleID,
```

- [ ] **Step 23: Run to verify the repo round-trip tests pass**

Run: `go test ./adapter/sqlite/ -run InstanceRepoOrigin -v`
Expected: PASS (all four subtests).

- [ ] **Step 24: Run the full sqlite package (no positional-scan regressions)**

Run: `go test ./adapter/sqlite/...`
Expected: PASS. (If a `Scan` panics with a column-count mismatch, a generated `Scan` list missed the two new columns — revisit Task 3 Step 14.)

- [ ] **Step 25: Commit**

```bash
git add adapter/sqlite/convert.go adapter/sqlite/instance_repo.go adapter/sqlite/instance_repo_origin_test.go
git commit -m "feat(sqlite): persist and read instance trigger_source/schedule_id"
```

---

### Task 5: Expose origin on the instance-list HTTP JSON

**Files:**
- Modify: `adapter/http/instance_handler.go` (`summaryDTO` line 166, `toSummaryDTO` line 180)
- Test: `adapter/http/instance_handler_response_test.go` (add cases) — inspect it for the existing summary-DTO assertions and extend.

- [ ] **Step 26: Write the failing HTTP-projection test**

In `adapter/http/instance_handler_response_test.go`, add a test that builds an `instance.InstanceSummary` with `TriggerSource: instance.OriginSchedule` + a `ScheduleID`, runs it through `toSummaryDTO` (or marshals a `summaryResponse`), and asserts the JSON contains `"triggerSource":"schedule"` and `"scheduleId":"<uuid>"`; and a manual summary (`OriginManual`, nil id) emits `"triggerSource":"manual"` and OMITS `scheduleId`.

```go
// schedule case:
//   dto := toSummaryDTO(instance.InstanceSummary{TriggerSource: instance.OriginSchedule, ScheduleID: &sid, ...minimal required fields...})
//   marshal -> contains "triggerSource":"schedule" and "scheduleId":"<sid>"
// manual case:
//   toSummaryDTO(instance.InstanceSummary{TriggerSource: instance.OriginManual, ...})
//   marshal -> "triggerSource":"manual"; does NOT contain "scheduleId"
```

- [ ] **Step 27: Run to verify it fails**

Run: `go test ./adapter/http/ -run Summary -v`
Expected: FAIL — `summaryDTO` has no `TriggerSource`/`ScheduleID` field.

- [ ] **Step 28: Add fields to `summaryDTO`**

In `adapter/http/instance_handler.go`, in `summaryDTO` (after `Visibility string ...`, line 172):

```go
	TriggerSource string  `json:"triggerSource"`
	ScheduleID    *string `json:"scheduleId,omitempty"`
```

- [ ] **Step 29: Populate them in `toSummaryDTO`**

In `toSummaryDTO` (line 180), add to the `summaryDTO{...}` literal:

```go
		TriggerSource: string(s.TriggerSource),
```

And after the literal (alongside the `CompletedAt`/`QueuedAt` pointer handling), add:

```go
	if s.ScheduleID != nil {
		v := s.ScheduleID.String()
		dto.ScheduleID = &v
	}
```

- [ ] **Step 30: Run to verify it passes**

Run: `go test ./adapter/http/ -run Summary -v`
Expected: PASS.

- [ ] **Step 31: Commit**

```bash
git add adapter/http/instance_handler.go adapter/http/instance_handler_response_test.go
git commit -m "feat(http): expose triggerSource and scheduleId on instance summaries"
```

---

### Task 6: Full gate

- [ ] **Step 32: Build, vet, and test the affected packages**

Run: `go build ./... && go vet ./... && go test ./adapter/sqlite/... ./adapter/http/... ./domain/...`
Expected: all PASS, exit 0.

---

## Self-review checklist

- [ ] Migration is additive only; `trigger_source TEXT NOT NULL DEFAULT 'manual'` with a CHECK; `schedule_id TEXT` nullable; NO foreign key.
- [ ] Existing rows backfill to `manual` (verified by the migration test inserting without the column).
- [ ] EVERY generated `Scan` over a full `generated.Instance` (create/get/listRunning) appends the two new columns; `ListInstancesRow` scan appends its two.
- [ ] `scheduleId` is omitted from JSON when nil (`omitempty` + pointer).
- [ ] A row with an arbitrary (deleted-schedule) `schedule_id` lists without error — no FK to violate.
