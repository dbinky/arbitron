# Workflow Management — Phase 2, Task 1: Add workflow_id columns + backfill

**Goal:** Add a nullable `workflow_id` to `instances`, `schedules`, and
`published_workflows`, and backfill each from its existing `workflow_name` via the
Phase-1 registry — without yet changing read/trigger behavior.

**Architecture:** Additive migrations + a one-time backfill that resolves each row's
`workflow_name` to an id through the registry. `workflow_name` is retained as a label.
Rows whose name no longer resolves keep `workflow_id` NULL (dangling historical label).

**Tech Stack:** Go, SQLite (golang-migrate + sqlc), uuid, testify. Depends on Phase 1.

---

## Files
- Create: `adapter/sqlite/migrations/02X_workflow_id_refs.up.sql` / `.down.sql`
- Modify: `adapter/sqlite/queries/{instances,schedules,published_workflows}.sql` (selects/inserts include `workflow_id`)
- Create: `domain/workflow/ref_backfill.go` (`BackfillReferences(ctx)`)
- Modify: `cmd/arbitron/main.go` (call backfill at startup, after registry backfill)
- Test:   `adapter/sqlite/ref_backfill_test.go`

## Steps

- [ ] **Step 1 — Failing test: backfill resolves names to ids.** Seed the registry with
  ("alpha"→idA), insert an instance + schedule + publication with `workflow_name='alpha'`
  and NULL `workflow_id`; run `BackfillReferences`; assert all three now carry `idA`.
  Also seed one row with name `'ghost'` (not in registry) and assert it stays NULL.
  Run `go test ./adapter/sqlite/ -run RefBackfill` → FAIL.

- [ ] **Step 2 — Migration** `02X_workflow_id_refs.up.sql`:
  ```sql
  ALTER TABLE instances           ADD COLUMN workflow_id TEXT;
  ALTER TABLE schedules           ADD COLUMN workflow_id TEXT;
  ALTER TABLE published_workflows ADD COLUMN workflow_id TEXT;
  CREATE INDEX idx_instances_workflow_id ON instances(workflow_id);
  CREATE INDEX idx_schedules_workflow_id ON schedules(workflow_id);
  ```
  `.down.sql` drops the columns/indexes (note SQLite drop-column requires a recent
  version; follow the project's existing down-migration convention). Confirm it runs.

- [ ] **Step 3 — Backfill** `BackfillReferences(ctx)`: for each of the three tables,
  select rows with NULL `workflow_id` + their `workflow_name`, `ResolveID(name)`, and
  UPDATE the row when it resolves; leave NULL + `slog.Warn` when it doesn't. Idempotent.
  Make Step 1 pass.

- [ ] **Step 4 — Wire** the backfill into `main.go` startup after the registry backfill;
  test idempotency (run twice → stable).

- [ ] **Step 5 — Edge tests:** an instance whose workflow was deleted (name unresolved)
  stays NULL and is not an error; a row already carrying an id is left untouched.

- [ ] **Step 6 — Gate + commit.** `go test ./...` green. `git add -A && git commit -m
  "feat(workflow): add workflow_id to instances/schedules/publications + backfill"`
