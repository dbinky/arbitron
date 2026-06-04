# RBAC Phase 1 — Task 3: `user_departments` Migration, Queries & Repo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist per-user department membership in a new `user_departments` table (migration 009), expose sqlc queries, extend the `UserRepository` port with department read/replace operations, and implement them in the SQLite repo with a transactional, idempotent replace.

**Architecture:** New migration `009_user_departments.{up,down}.sql`. New sqlc query file `adapter/sqlite/queries/user_departments.sql` (regenerate `generated/`). Two new methods on the `instance.UserRepository` port: `ListDepartments` and `ReplaceDepartments`. `ReplaceDepartments` runs delete-all-then-insert in one transaction (mirroring the existing `DismissalRepo.DismissMany` tx pattern). All persistence sits behind the port (Postgres-ready: standard SQL only).

**Tech Stack:** Go, SQLite, sqlc, `database/sql` transactions.

> sqlc regeneration: this repo generates into `adapter/sqlite/generated/` from `adapter/sqlite/sqlc.yaml`. After editing the `.sql` files, run `sqlc generate` from inside `adapter/sqlite/`. If `sqlc` is not installed, install it first (`go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`) — do NOT hand-write the generated structs.

---

### Task 3: migration, queries, port methods, repo

**Files:**
- Create: `adapter/sqlite/migrations/009_user_departments.up.sql`
- Create: `adapter/sqlite/migrations/009_user_departments.down.sql`
- Create: `adapter/sqlite/queries/user_departments.sql`
- Regenerate: `adapter/sqlite/generated/` (via `sqlc generate`)
- Modify: `domain/instance/ports.go` (add `ListDepartments`, `ReplaceDepartments` to `UserRepository`)
- Modify: `adapter/sqlite/user_repo.go` (implement the two methods)
- Test: `adapter/sqlite/user_repo_departments_test.go`

**Dependencies:** Task 1 (uses `identity.NormalizeDept` only in tests for clarity; not strictly required). Independent of Tasks 2/4/5/6 at compile time, but the provisioner (Task 4) and middleware (Task 5) depend on the port methods added here. Do Task 3 before Tasks 4 and 5.

- [ ] **Step 1: Write the up migration**

Create `adapter/sqlite/migrations/009_user_departments.up.sql`:

```sql
-- RBAC Phase 1: per-user department membership, snapshotted at login by the
-- provisioner. Departments are derived from normalized group-claim values; there
-- is no separate department registry. Non-interactive triggers (webhooks / API
-- tokens) read this stored set because no live token is present for them.
--
-- Postgres-ready: standard SQL only (composite PK, FK with ON DELETE CASCADE,
-- a single secondary index). No SQLite-only constructs.
CREATE TABLE IF NOT EXISTS user_departments (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    department TEXT NOT NULL,
    PRIMARY KEY (user_id, department)
);

-- The dept index serves later phases' "who is in department X" reads (dashboard,
-- department-visibility filters). Added now so the schema is forward-compatible.
CREATE INDEX IF NOT EXISTS idx_user_departments_dept ON user_departments(department);
```

- [ ] **Step 2: Write the down migration**

Create `adapter/sqlite/migrations/009_user_departments.down.sql`:

```sql
DROP TABLE IF EXISTS user_departments;
```

- [ ] **Step 3: Write the sqlc queries**

Create `adapter/sqlite/queries/user_departments.sql`:

```sql
-- name: ListUserDepartments :many
SELECT department FROM user_departments WHERE user_id = ? ORDER BY department;

-- name: DeleteUserDepartments :exec
DELETE FROM user_departments WHERE user_id = ?;

-- name: InsertUserDepartment :exec
INSERT INTO user_departments (user_id, department) VALUES (?, ?)
ON CONFLICT(user_id, department) DO NOTHING;
```

- [ ] **Step 4: Regenerate sqlc**

Run: `sqlc generate -f adapter/sqlite/sqlc.yaml` (or from inside `adapter/sqlite/`: `sqlc generate`)
Expected: success; new methods `ListUserDepartments`, `DeleteUserDepartments`, `InsertUserDepartment` appear in `adapter/sqlite/generated/`.

Verify:

Run: `grep -l "ListUserDepartments" adapter/sqlite/generated/*.go`
Expected: at least one match.

- [ ] **Step 5: Add the port methods (with the compile-time test first)**

Write the failing repo test. Create `adapter/sqlite/user_repo_departments_test.go`:

```go
package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// newUserForDepts creates a user row and returns its ID, reusing the existing
// sqlite test-DB helper setupTestDB (returns testDB{DB *sql.DB}; opens a migrated
// :memory: DB with PRAGMA foreign_keys=ON, verified in dismissal_repo_test.go).
func newUserForDepts(t *testing.T, repo *UserRepo) uuid.UUID {
	t.Helper()
	id := uuid.New()
	subj := "subj-" + id.String()
	if err := repo.Create(context.Background(), &instance.User{
		ID:       id,
		Subject:  &subj,
		Username: "u-" + id.String(),
		Role:     "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func TestReplaceDepartments_HappyPath(t *testing.T) {
	db := setupTestDB(t) // testDB{DB: *sql.DB}, FKs ON
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	if err := repo.ReplaceDepartments(context.Background(), uid, []string{"engineering", "platform"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := repo.ListDepartments(context.Background(), uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"engineering", "platform"} // ORDER BY department
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReplaceDepartments_IsIdempotentReplace(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"sales", "eng"}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	// Second replace with a DIFFERENT set fully supersedes the first.
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("got %v, want [eng]", got)
	}
}

func TestReplaceDepartments_EmptySetClears(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.ReplaceDepartments(ctx, uid, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestReplaceDepartments_DeduplicatesWithinCall(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	// Duplicate values in one call must not error (ON CONFLICT DO NOTHING).
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng", "eng"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := repo.ListDepartments(ctx, uid)
	if !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("got %v, want [eng]", got)
	}
}

func TestListDepartments_CascadeOnUserDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Delete the user directly; ON DELETE CASCADE must remove the dept rows.
	if _, err := db.DB.ExecContext(ctx, "DELETE FROM users WHERE id = ?", uid.String()); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty after cascade", got)
	}
}
```

> The sqlite suite's helper is `setupTestDB(t) testDB` (in `integration_test.go`); it opens a migrated `:memory:` DB via `Open(":memory:")` which enables `PRAGMA foreign_keys=ON` (asserted in `dismissal_repo_test.go`), so the cascade test is valid. Access the raw handle via `db.DB`.

- [ ] **Step 6: Run the test to verify it fails to compile**

Run: `go test ./adapter/sqlite/ -run TestReplaceDepartments -v`
Expected: FAIL — `repo.ReplaceDepartments undefined` / `repo.ListDepartments undefined`.

- [ ] **Step 7: Add the port methods to `UserRepository`**

In `domain/instance/ports.go`, inside the `UserRepository interface`, after `CountAdmins`, add:

```go
	// ListDepartments returns the user's persisted, normalized department set
	// (sorted). Empty when the user has no stored departments. (RBAC Phase 1)
	ListDepartments(ctx context.Context, userID uuid.UUID) ([]string, error)
	// ReplaceDepartments atomically replaces the user's department set with the
	// given normalized values (delete-all + insert in one transaction). An empty
	// slice clears the set. Idempotent: re-running with the same values is a
	// no-op. The provisioner calls this on every login to snapshot membership.
	// (RBAC Phase 1)
	ReplaceDepartments(ctx context.Context, userID uuid.UUID, departments []string) error
```

- [ ] **Step 8: Implement the methods in the SQLite repo**

In `adapter/sqlite/user_repo.go`, add at the end of the file:

```go
// ListDepartments returns the user's persisted department set, sorted by the
// ORDER BY department in the query.
func (r *UserRepo) ListDepartments(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.queries.ListUserDepartments(ctx, uuidToString(userID))
	if err != nil {
		return nil, fmt.Errorf("listing departments for user %s: %w", userID, err)
	}
	return rows, nil
}

// ReplaceDepartments atomically supersedes the user's department set: delete-all
// then insert each value, in a single transaction. The caller passes already
// NORMALIZED values; ON CONFLICT DO NOTHING makes duplicate values within one
// call harmless. An empty/nil slice clears the set.
func (r *UserRepo) ReplaceDepartments(ctx context.Context, userID uuid.UUID, departments []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning replace-departments transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	qtx := r.queries.WithTx(tx)
	uid := uuidToString(userID)
	if err := qtx.DeleteUserDepartments(ctx, uid); err != nil {
		return fmt.Errorf("clearing departments for user %s: %w", userID, err)
	}
	for _, d := range departments {
		if err := qtx.InsertUserDepartment(ctx, generated.InsertUserDepartmentParams{
			UserID:     uid,
			Department: d,
		}); err != nil {
			return fmt.Errorf("inserting department %q for user %s: %w", d, userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing replace-departments transaction: %w", err)
	}
	return nil
}
```

> If sqlc named the params struct differently (e.g. `InsertUserDepartmentParams` vs a positional signature), match the generated signature. Confirm with:
> `grep -n "InsertUserDepartment\|DeleteUserDepartments\|ListUserDepartments" adapter/sqlite/generated/*.go`
> sqlc generates a `Params` struct only for multi-arg queries; `DeleteUserDepartments` and `ListUserDepartments` take a single string arg.

- [ ] **Step 9: Add the `uuid` import if missing**

`adapter/sqlite/user_repo.go` already imports `github.com/google/uuid`, `fmt`, and `generated`. No new imports needed. If `go build` complains, add what it names.

- [ ] **Step 10: Build and run the repo tests**

Run: `go build ./... && go test ./adapter/sqlite/ -run "Departments" -v`
Expected: PASS for all five department tests.

- [ ] **Step 11: Verify nothing else broke (other UserRepository implementers / fakes)**

Adding methods to the port breaks any in-memory fake that implements `instance.UserRepository`. Find them:

Run: `grep -rln "instance.UserRepository\b" --include=*.go . | grep -v adapter/sqlite/user_repo.go`
For each fake found (look for a struct with `var _ instance.UserRepository = ...` or that the compiler now flags), add the two new methods. A typical in-memory fake addition:

```go
func (f *fakeUserRepo) ListDepartments(_ context.Context, userID uuid.UUID) ([]string, error) {
	return append([]string(nil), f.depts[userID]...), nil
}

func (f *fakeUserRepo) ReplaceDepartments(_ context.Context, userID uuid.UUID, departments []string) error {
	if f.depts == nil {
		f.depts = map[uuid.UUID][]string{}
	}
	f.depts[userID] = append([]string(nil), departments...)
	return nil
}
```

Run the whole suite to surface every broken fake:

Run: `go build ./... && go test ./... 2>&1 | grep -i "does not implement\|missing method" || echo "no missing-method errors"`
Expected: `no missing-method errors` after all fakes are updated.

- [ ] **Step 12: Full test + vet**

Run: `go test ./adapter/sqlite/ ./domain/instance/ -v && go vet ./adapter/sqlite/ ./domain/instance/`
Expected: PASS, clean vet.

- [ ] **Step 13: Commit**

```bash
git add adapter/sqlite/migrations/009_user_departments.up.sql adapter/sqlite/migrations/009_user_departments.down.sql adapter/sqlite/queries/user_departments.sql adapter/sqlite/generated/ domain/instance/ports.go adapter/sqlite/user_repo.go adapter/sqlite/user_repo_departments_test.go
git add -A  # pick up any updated test fakes
git commit -m "feat(rbac): user_departments table + transactional replace repo

Migration 009 (composite PK, FK cascade, dept index), sqlc queries, and
UserRepository.ListDepartments/ReplaceDepartments (delete-all+insert in one tx,
idempotent). Postgres-ready standard SQL.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | Replace then list returns sorted set | 5 |
| Happy | Replace fully supersedes a prior set | 5 |
| Edge | Empty/nil set clears departments | 5 |
| Edge | Duplicate values within one call are de-duplicated (ON CONFLICT) | 5 |
| Edge | Replace is idempotent / transactional (no partial state on duplicate) | 5, 8 |
| Error | FK cascade removes dept rows on user delete | 5 |
| Authz | (N/A at repo layer — authz lives in services consuming this) | — |
| Mode | (N/A at repo layer — mode gating is in middleware/provisioner) | — |
| Migration | 009 up creates table+index; down drops cleanly | 1, 2 |
