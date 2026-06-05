# RBAC Phase 2 — Task 1: `published_workflows` Visibility Schema + Repo Extension

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `visibility` + `department` columns to `published_workflows` (migration 010, with `DEFAULT 'public'` serving as the shared→public data migration) and extend the SQLite `PublishedWorkflowRepo` to read/write them via a `PublicationOf` accessor.

**Architecture:** Metadata-on-a-single-tree. The shared workflow content stays one copy under `shared/<name>`; the publication row now carries the sharing level. This task is pure persistence — no domain gating yet (that arrives in Tasks 2–4).

**Tech Stack:** Go, SQLite via sqlc (`adapter/sqlite/generated`), `github.com/google/uuid`, testify. Migrations are plain `.up.sql`/`.down.sql` embedded by `adapter/sqlite/migrate.go` and applied in version order.

**Cross-phase dependency:** None for this task. Phase 1's migration `009_user_departments` is independent; this task uses `010`. Do NOT renumber.

---

## Context you need

- Existing migration: `adapter/sqlite/migrations/006_published_workflows.up.sql` creates `published_workflows(name PK, publisher_id, source_name, published_at)`.
- Existing queries: `adapter/sqlite/queries/published_workflows.sql` (`GetPublishedWorkflow`, `UpsertPublishedWorkflow`, `ListPublishedWorkflows`, `DeletePublishedWorkflow`).
- Existing repo: `adapter/sqlite/published_workflow_repo.go` — `PublishedWorkflowRepo` with `PublisherOf`, `Get`, `Upsert`, `List`, `Delete`. `PublishedWorkflow` struct holds `Name, PublisherID, SourceName, PublishedAt`.
- sqlc config: `adapter/sqlite/sqlc.yaml` (queries in `queries/`, schema in `migrations/`, generated package `generated`). Regenerate with `sqlc generate` run from `adapter/sqlite/`.
- Test DB helper: `setupTestDB(t)` in `adapter/sqlite/integration_test.go` returns a `testDB` whose `.DB` is a migrated `*sql.DB`. Seeded local admin id is `00000000-0000-0000-0000-000000000001` (see `defaultUserID` in `published_workflow_repo_test.go`).
- Convert helpers in `adapter/sqlite/convert.go`: `parseUUID`, `parseTime`, `stringToNullString`. SQLite NULL text maps to `sql.NullString`.

## Files

- Create: `adapter/sqlite/migrations/010_workflow_visibility.up.sql`
- Create: `adapter/sqlite/migrations/010_workflow_visibility.down.sql`
- Modify: `adapter/sqlite/queries/published_workflows.sql` (add visibility/department columns to all four queries)
- Regenerate: `adapter/sqlite/generated/*` (via `sqlc generate`)
- Modify: `adapter/sqlite/published_workflow_repo.go` (extend `PublishedWorkflow` struct; add `Visibility`/`Department` to `Get`/`List`; new `PublicationOf`; widen `Upsert` signature)
- Test: `adapter/sqlite/published_workflow_repo_test.go` (extend)

---

## Steps

- [ ] **Step 1: Write the failing migration round-trip test**

Append to `adapter/sqlite/published_workflow_repo_test.go`:

```go
// --- Phase 2: visibility column migration ---

func TestPublishedWorkflowRepo_PreExistingRowDefaultsToPublic(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Simulate a row that existed before migration 010 by inserting only the
	// pre-010 columns; the column DEFAULT must fill visibility='public'.
	_, err := db.DB.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name) VALUES (?, ?, ?)`,
		"legacy-shared", defaultUserID.String(), "src")
	require.NoError(t, err)

	repo := NewPublishedWorkflowRepo(db.DB)
	pub, ok, err := repo.PublicationOf(ctx, "legacy-shared")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "public", pub.Visibility)
	assert.Equal(t, "", pub.Department) // NULL department reads back as empty string
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/sqlite/ -run TestPublishedWorkflowRepo_PreExistingRowDefaultsToPublic -v`
Expected: FAIL — compile error (`repo.PublicationOf` undefined) and/or migration column missing.

- [ ] **Step 3: Write migration 010**

Create `adapter/sqlite/migrations/010_workflow_visibility.up.sql`:

```sql
-- Visibility metadata for the shared workspace. The shared workflow content
-- remains a single copy under shared/<name>; only this row's level changes.
-- The DEFAULT 'public' IS the data migration (spec §Migration of Existing
-- Data): every pre-existing shared row becomes public, preserving today's
-- "everyone can see it" behavior. A 'department' row must carry a non-NULL
-- department (enforced in the domain service; SQLite cannot CHECK across the
-- DEFAULT cleanly so the guard lives in code).
ALTER TABLE published_workflows
    ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('department','public'));

ALTER TABLE published_workflows
    ADD COLUMN department TEXT;  -- NULL for public; set for department

CREATE INDEX IF NOT EXISTS idx_published_visibility
    ON published_workflows(visibility, department);
```

Create `adapter/sqlite/migrations/010_workflow_visibility.down.sql`:

```sql
DROP INDEX IF EXISTS idx_published_visibility;
ALTER TABLE published_workflows DROP COLUMN department;
ALTER TABLE published_workflows DROP COLUMN visibility;
```

- [ ] **Step 4: Update the sqlc queries**

Replace the contents of `adapter/sqlite/queries/published_workflows.sql` with:

```sql
-- name: GetPublishedWorkflow :one
SELECT name, publisher_id, source_name, published_at, visibility, department
FROM published_workflows
WHERE name = ?;

-- name: UpsertPublishedWorkflow :exec
INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    publisher_id = excluded.publisher_id,
    source_name  = excluded.source_name,
    visibility   = excluded.visibility,
    department   = excluded.department,
    published_at = strftime('%Y-%m-%dT%H:%M:%SZ');

-- name: ListPublishedWorkflows :many
SELECT name, publisher_id, source_name, published_at, visibility, department
FROM published_workflows
ORDER BY name;

-- name: DeletePublishedWorkflow :exec
DELETE FROM published_workflows WHERE name = ?;
```

- [ ] **Step 5: Regenerate sqlc**

Run: `cd adapter/sqlite && sqlc generate && cd -`
Expected: `adapter/sqlite/generated/*.go` now expose `Visibility string` and `Department sql.NullString` on the published-workflow row/param structs. If `sqlc` is not installed, install it (`go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`) — the project pins sqlc via the same path used for other tables.

- [ ] **Step 6: Extend the repo — struct + accessors**

In `adapter/sqlite/published_workflow_repo.go`, extend the domain-facing struct and add the read/write surface. Add `Visibility`/`Department` to `PublishedWorkflow`:

```go
// PublishedWorkflow is the domain-facing publication record.
type PublishedWorkflow struct {
	Name        string
	PublisherID uuid.UUID
	SourceName  string
	PublishedAt time.Time
	Visibility  string // "department" | "public"
	Department  string // "" for public
}
```

Update `Get` to populate the new fields (read the generated row's `Visibility` and `Department` — the latter is a `sql.NullString`, map invalid to `""`):

```go
return &PublishedWorkflow{
	Name:        row.Name,
	PublisherID: id,
	SourceName:  row.SourceName,
	PublishedAt: publishedAt,
	Visibility:  row.Visibility,
	Department:  row.Department.String, // "" when NULL
}, true, nil
```

Apply the same two new fields in the `List` loop's appended struct.

Add a `PublicationOf` method (an alias of `Get` with a name that matches the domain port introduced in Task 2, so the seam is obvious):

```go
// PublicationOf returns the full publication record for name, including its
// visibility and department, or (nil, false, nil) when no publication exists.
// It is the read surface the domain workflow service consults for gating.
func (r *PublishedWorkflowRepo) PublicationOf(ctx context.Context, name string) (*PublishedWorkflow, bool, error) {
	return r.Get(ctx, name)
}
```

Widen `Upsert` to accept visibility + department (department `""` ⇒ SQL NULL):

```go
// Upsert records (or re-records) a publication with its visibility level. A
// department string is stored as SQL NULL when empty (public). The
// publisher-only-overwrite and role-gating rules are enforced by the caller
// (Task 4) before this is invoked.
func (r *PublishedWorkflowRepo) Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName, visibility, department string) error {
	dept := sql.NullString{}
	if department != "" {
		dept = sql.NullString{String: department, Valid: true}
	}
	if err := r.queries.UpsertPublishedWorkflow(ctx, generated.UpsertPublishedWorkflowParams{
		Name:        name,
		PublisherID: publisher.String(),
		SourceName:  sourceName,
		Visibility:  visibility,
		Department:  dept,
	}); err != nil {
		return fmt.Errorf("upsert published %q: %w", name, err)
	}
	return nil
}
```

(The existing `var _ workflow.PublisherLookup = (*PublishedWorkflowRepo)(nil)` compile assertion at the top of the file will be revisited in Task 2 once the port widens; leave it for now — it still holds since `PublisherOf` is unchanged.)

- [ ] **Step 7: Run the migration round-trip test to verify it passes**

Run: `go test ./adapter/sqlite/ -run TestPublishedWorkflowRepo_PreExistingRowDefaultsToPublic -v`
Expected: PASS.

- [ ] **Step 8: Add the explicit department round-trip test**

Append:

```go
func TestPublishedWorkflowRepo_UpsertDepartment_RoundTrips(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "triage", defaultUserID, "src", "department", "engineering"))

	pub, ok, err := repo.PublicationOf(ctx, "triage")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "department", pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

func TestPublishedWorkflowRepo_UpsertPublic_StoresNullDepartment(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "pub-wf", defaultUserID, "src", "public", ""))

	// department column is SQL NULL, surfaced as "".
	pub, ok, err := repo.PublicationOf(ctx, "pub-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "public", pub.Visibility)
	assert.Equal(t, "", pub.Department)

	var deptIsNull bool
	require.NoError(t, db.DB.QueryRowContext(ctx,
		`SELECT department IS NULL FROM published_workflows WHERE name = ?`, "pub-wf").Scan(&deptIsNull))
	assert.True(t, deptIsNull)
}

func TestPublishedWorkflowRepo_UpsertRejectsBadVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	// CHECK constraint rejects an out-of-vocab visibility.
	err := repo.Upsert(ctx, "bad", defaultUserID, "src", "private", "")
	require.Error(t, err)
}
```

Note: every existing call to `repo.Upsert(...)` in this test file currently passes 3 args after ctx; update each to the new 5-arg form, defaulting to `"public", ""` so prior behavior is preserved. Search the file for `repo.Upsert(` and `.Upsert(ctx,` and fix all call sites.

- [ ] **Step 9: Run the full sqlite package test suite**

Run: `go test ./adapter/sqlite/... -v`
Expected: PASS. If existing tests fail to compile, it is because of the widened `Upsert` signature — fix those call sites as in Step 8.

- [ ] **Step 10: Commit**

```bash
git add adapter/sqlite/migrations/010_workflow_visibility.up.sql \
        adapter/sqlite/migrations/010_workflow_visibility.down.sql \
        adapter/sqlite/queries/published_workflows.sql \
        adapter/sqlite/generated \
        adapter/sqlite/published_workflow_repo.go \
        adapter/sqlite/published_workflow_repo_test.go
git commit -m "feat(rbac): add visibility+department to published_workflows (migration 010)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Blocks:** Task 2 (the domain port widens to consume `PublicationOf` and the 5-arg `Upsert`).
- **Depends on:** nothing in Phase 2. Independent of Phase 1's migration 009.
