# RBAC Phase 3 — Task 1: Instance Visibility Schema & Model Fields

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `department` + `visibility` columns to the `instances` table (migration 012) and the matching `Department` + `Visibility` fields (plus a self-owned `Visibility` type) on `instance.Instance`, persisted/read through the SQLite repo.

**Architecture:** Strict hexagonal. The domain `instance.Instance` gains two string fields and a self-owned `Visibility` enum type whose values mirror the workflow visibility strings (`private|department|public`) — redeclared locally, NOT imported from `domain/workflow`, matching the existing `instance.Claims`/`identity.Claims` one-directional-edge pattern. The SQLite migration adds the columns + the dashboard/authz index. The repo's `Create` writes the two columns; `Get`/`ListRunning` read them back via the `SELECT *` queries (the generated row struct picks up the new columns automatically after `sqlc generate`).

**Tech Stack:** Go 1.23+, SQLite, sqlc v2, testify.

**Cross-phase dependencies:**
- **Phase 1** (identity departments) and **Phase 2** (workflow visibility + audit, migrations 009–011) MUST land first. This task adds migration **012**; migrations 009–011 are owned by Phases 1–2. Do not create them here.
- This task is the foundation for Phase 3 Tasks 2–6 and for the Phase 4 dashboard, which reads `instances.department` / `instances.visibility` via `idx_instances_dept_vis`.

**Self-owned type note:** `domain/instance` must not import `domain/workflow`. Declare the `Visibility` type and its three constants locally in `domain/instance`.

---

### Task 1.1: Migration 012 — add columns + index

**Files:**
- Create: `adapter/sqlite/migrations/012_instance_visibility.up.sql`
- Create: `adapter/sqlite/migrations/012_instance_visibility.down.sql`
- Test: `adapter/sqlite/instance_visibility_migration_test.go`

- [ ] **Step 1: Write the failing migration test**

Create `adapter/sqlite/instance_visibility_migration_test.go`:

```go
package sqlite

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration: the instances table gains department + visibility columns after
// RunMigrations, and a pre-existing-style row reads back visibility='private'
// with a NULL department (owner-only behavior, matching spec §Migration).
func TestMigration012_InstanceVisibilityColumns(t *testing.T) {
	db := setupTestDB(t)

	// Insert an instance the way pre-012 code would (no visibility/department),
	// relying on the column defaults supplied by migration 012.
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal')`,
		id,
	)
	require.NoError(t, err)

	var visibility string
	var department *string
	require.NoError(t, db.DB.QueryRow(
		`SELECT visibility, department FROM instances WHERE id = ?`, id,
	).Scan(&visibility, &department))

	assert.Equal(t, "private", visibility, "existing rows default to private (owner-only)")
	assert.Nil(t, department, "department defaults to NULL (default group / private)")
}

// The check constraint rejects an unknown visibility value.
func TestMigration012_VisibilityCheckConstraint(t *testing.T) {
	db := setupTestDB(t)
	id := uuid.New().String()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, visibility)
		 VALUES (?, '00000000-0000-0000-0000-000000000001', 'wf', '{}', 'queued', 'normal', 'bogus')`,
		id,
	)
	require.Error(t, err, "visibility CHECK must reject values outside private|department|public")
}

// The dashboard/authz index exists after migration.
func TestMigration012_IndexExists(t *testing.T) {
	db := setupTestDB(t)
	var name string
	require.NoError(t, db.DB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_instances_dept_vis'`,
	).Scan(&name))
	assert.Equal(t, "idx_instances_dept_vis", name)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/sqlite/ -run TestMigration012 -v`
Expected: FAIL — `no such column: visibility` (migration 012 does not exist yet).

- [ ] **Step 3: Write the up migration**

Create `adapter/sqlite/migrations/012_instance_visibility.up.sql`:

```sql
-- Carry a workflow's visibility onto the instances it produces (RBAC Phase 3).
-- department: the department the run was stamped under at trigger time
--   (a snapshot; NULL => default group / private). visibility: the run's
--   audience, mirroring the workflow visibility model.
ALTER TABLE instances ADD COLUMN department TEXT;  -- NULL => default group / private
ALTER TABLE instances ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('private','department','public'));

-- List-authorization + Phase-4 dashboard index. Ordered so the disjunctive
-- visibility filter (department IN (...) AND visibility='department', plus the
-- public scan) is a single index-backed range; created_at trails for the
-- ORDER BY created_at DESC paging. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_dept_vis
    ON instances (department, visibility, state, created_at);
```

- [ ] **Step 4: Write the down migration**

Create `adapter/sqlite/migrations/012_instance_visibility.down.sql`:

```sql
DROP INDEX IF EXISTS idx_instances_dept_vis;
-- SQLite cannot DROP COLUMN on older versions used by some deployments; rebuild
-- is out of scope for a down migration. Recreating the table is intentionally
-- omitted — forward-only is the project convention (see prior down files).
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./adapter/sqlite/ -run TestMigration012 -v`
Expected: PASS (all three).

- [ ] **Step 6: Run the full sqlite migration suite to confirm idempotency**

Run: `go test ./adapter/sqlite/ -run TestRunMigrations -v`
Expected: PASS — migration set still applies once and is idempotent.

- [ ] **Step 7: Commit**

```bash
git add adapter/sqlite/migrations/012_instance_visibility.up.sql \
        adapter/sqlite/migrations/012_instance_visibility.down.sql \
        adapter/sqlite/instance_visibility_migration_test.go
git commit -m "feat(rbac): migration 012 adds instance department+visibility columns and index"
```

---

### Task 1.2: `Visibility` type + `Department`/`Visibility` fields on `instance.Instance`

**Files:**
- Modify: `domain/instance/instance.go`
- Test: `domain/instance/instance_visibility_test.go`

- [ ] **Step 1: Write the failing test**

Create `domain/instance/instance_visibility_test.go`:

```go
package instance_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

func TestVisibilityConstants(t *testing.T) {
	assert.Equal(t, instance.Visibility("private"), instance.VisibilityPrivate)
	assert.Equal(t, instance.Visibility("department"), instance.VisibilityDepartment)
	assert.Equal(t, instance.Visibility("public"), instance.VisibilityPublic)
}

func TestInstance_HasDepartmentAndVisibilityFields(t *testing.T) {
	inst := instance.Instance{
		Department: "engineering",
		Visibility: instance.VisibilityDepartment,
	}
	assert.Equal(t, "engineering", inst.Department)
	assert.Equal(t, instance.VisibilityDepartment, inst.Visibility)
}

// Zero value is private with empty department — matching the owner-only default
// of pre-existing rows.
func TestInstance_ZeroValueIsPrivate(t *testing.T) {
	var inst instance.Instance
	assert.Equal(t, "", inst.Department)
	assert.Equal(t, instance.Visibility(""), inst.Visibility)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/instance/ -run 'TestVisibility|TestInstance_Has|TestInstance_ZeroValueIsPrivate' -v`
Expected: FAIL — `undefined: instance.Visibility`, `inst.Department undefined`.

- [ ] **Step 3: Add the `Visibility` type and constants**

In `domain/instance/instance.go`, after the `Priority` constants block (after line 44), add:

```go
// Visibility is an instance's audience, stamped at trigger time from the
// resolved workflow publication. Its values mirror the workflow visibility
// model. It is redeclared locally (NOT imported from domain/workflow) to keep
// the domain edge one-directional, the same pattern as instance.Claims
// redeclaring identity.Claims.
type Visibility string

const (
	VisibilityPrivate    Visibility = "private"
	VisibilityDepartment Visibility = "department"
	VisibilityPublic     Visibility = "public"
)
```

- [ ] **Step 4: Add the two fields to the `Instance` struct**

In `domain/instance/instance.go`, in the `Instance` struct, add the two fields after `WorkflowSnapshot map[string]any` (line 51):

```go
	// Department is the department this run was stamped under at trigger time
	// (a snapshot; "" => default group / private). Visibility is the run's
	// audience. Both are set by the engine Trigger path (Phase 3 Task 2).
	Department string
	Visibility Visibility
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./domain/instance/ -run 'TestVisibility|TestInstance_Has|TestInstance_ZeroValueIsPrivate' -v`
Expected: PASS.

- [ ] **Step 6: Build the whole module to catch struct-literal breakage**

Run: `go build ./...`
Expected: success (new fields are additive; no existing literal sets them positionally).

- [ ] **Step 7: Commit**

```bash
git add domain/instance/instance.go domain/instance/instance_visibility_test.go
git commit -m "feat(rbac): add Visibility type and Department/Visibility fields to instance.Instance"
```

---

### Task 1.3: Persist + read the two columns in the SQLite repo

**Files:**
- Modify: `adapter/sqlite/queries/instances.sql` (CreateInstance)
- Modify: `adapter/sqlite/instance_repo.go` (Create params, `rowToInstance`)
- Regenerate: `adapter/sqlite/generated/*` via `sqlc generate`
- Test: `adapter/sqlite/instance_repo_visibility_test.go`

- [ ] **Step 1: Write the failing repo round-trip test**

Create `adapter/sqlite/instance_repo_visibility_test.go`:

```go
package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Create persists department+visibility; Get reads them back unchanged.
func TestInstanceRepo_DepartmentVisibility_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Department:       "engineering",
		Visibility:       instance.VisibilityDepartment,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "engineering", got.Department)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
}

// A private run persists empty department + private visibility.
func TestInstanceRepo_PrivateRun_NullDepartment(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "", got.Department, "empty department stored as SQL NULL, read back as empty string")
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/sqlite/ -run 'TestInstanceRepo_DepartmentVisibility_RoundTrip|TestInstanceRepo_PrivateRun_NullDepartment' -v`
Expected: FAIL — `CreateInstanceParams` has no `Department`/`Visibility`; `rowToInstance` does not set them.

- [ ] **Step 3: Update the CreateInstance query**

In `adapter/sqlite/queries/instances.sql`, replace the `CreateInstance` query (lines 1–4) with:

```sql
-- name: CreateInstance :one
INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, queued_at, department, visibility)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;
```

- [ ] **Step 4: Regenerate sqlc**

Run: `cd adapter/sqlite && sqlc generate && cd -`
Expected: `generated/instances.sql.go` now has `Department` + `Visibility` on `CreateInstanceParams` and on the `Instance` row struct (the `GetInstance`/`ListRunningInstances` `SELECT *` rows pick them up automatically). If `sqlc` is not installed, run `go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate` from `adapter/sqlite`.

- [ ] **Step 5: Map the new column to a nullable SQL string in the repo**

In `adapter/sqlite/instance_repo.go`, in `Create`, add to the `generated.CreateInstanceParams{...}` literal (after `QueuedAt:`):

```go
		Department: stringToNullString(inst.Department),
		Visibility: string(inst.Visibility),
```

Note: `stringToNullString("")` must produce a SQL NULL (verify against the existing helper; if it produces an empty-but-valid string, use `ptrToNullString` with a nil pointer for the empty case, or add a small `deptToNullString` helper that maps `""` → `sql.NullString{}`). The round-trip test pins the empty→empty behavior.

- [ ] **Step 6: Map the columns back in `rowToInstance`**

In `adapter/sqlite/instance_repo.go`, in the returned `&instance.Instance{...}` literal inside `rowToInstance`, add:

```go
		Department: nullStringToString(row.Department),
		Visibility: instance.Visibility(row.Visibility),
```

If a `nullStringToString` helper does not exist, add it next to the other null helpers:

```go
// nullStringToString returns the string value of ns, or "" when NULL.
func nullStringToString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
```

- [ ] **Step 7: Run the round-trip tests to verify they pass**

Run: `go test ./adapter/sqlite/ -run 'TestInstanceRepo_DepartmentVisibility_RoundTrip|TestInstanceRepo_PrivateRun_NullDepartment' -v`
Expected: PASS.

- [ ] **Step 8: Run the full sqlite + instance packages to confirm no regression**

Run: `go test ./adapter/sqlite/... ./domain/instance/...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add adapter/sqlite/queries/instances.sql adapter/sqlite/instance_repo.go \
        adapter/sqlite/generated/ adapter/sqlite/instance_repo_visibility_test.go
git commit -m "feat(rbac): persist and read instance department+visibility in sqlite repo"
```

---

## Definition of done (Task 1)

- Migration 012 adds `department` (nullable) + `visibility` (NOT NULL DEFAULT 'private', CHECK) + `idx_instances_dept_vis`.
- `instance.Visibility` type + three constants exist in `domain/instance`, no import of `domain/workflow`.
- `instance.Instance` has `Department string` + `Visibility Visibility`.
- The SQLite repo writes both columns on `Create` and reads both on `Get`/`ListRunning`.
- `go test ./adapter/sqlite/... ./domain/instance/...` is green.
