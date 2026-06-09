# Final Org Gaps — Phase 2, Task 3: Startup Remap Reconciler (Four-Table Department Renamer)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an operator adds/changes a mapping for a department previously stored verbatim, rewrite every persisted occurrence of the old value to the friendly name consistently across **all four** department-bearing tables, via an idempotent startup reconcile pass that fails the boot loudly on a DB error.

**Architecture:** A new domain port `identity.DepartmentRenamer` declares `RenameDepartment(ctx, from, to) (rows int64, err error)`. A pure domain function `identity.ReconcileMappings(ctx, cfg, renamer, log)` walks `cfg.Mapping`, computes `from = NormalizeDept(rawKey)` / `to = NormalizeDept(friendly)`, and calls the renamer for each entry where `from != to`. The SQLite adapter implements the port by issuing `UPDATE ... SET department = ? WHERE department = ?` against `user_departments`, `instances`, `schedules`, and `published_workflows` inside **one transaction per mapping entry**. It is idempotent (once values are friendly the `WHERE` matches nothing) and safe to run on every boot. The composition root runs `ReconcileMappings` once after migrations + config load, before serving; a reconcile error aborts boot.

**Tech Stack:** Go, SQLite via raw `*sql.DB` `ExecContext` (matching `audit_repo.go`/`dismissal_repo.go`), `log/slog`, `testify`.

---

## Context the implementer needs

- **Spec:** `docs/specs/final-org-gaps-spec.md` — Capability B "Remap consistency": adding/changing a mapping for a previously-verbatim department reflects the friendly name everywhere (all dashboard views, existing schedules, historical runs) with **no orphaned or duplicated departments**.
- **Design:** `docs/superpowers/specs/final-org-gaps-design-phase-2-sso-group-mapping.md` — section 4. The four tables and the `published_workflows` rationale (omitting it would orphan a department-visibility publication). The per-entry transaction; idempotence; fail-fast at boot. `GroupConfig.Leads` keys are operator config (not data) and are **outside** the renamer's scope.
- **Tables & columns (verified against migrations):**
  - `user_departments.department` — migration `009_user_departments.up.sql` (membership snapshot; column `department TEXT NOT NULL`, part of composite PK `(user_id, department)`).
  - `instances.department` — migration `012_instance_visibility.up.sql` (`ADD COLUMN department TEXT`; nullable).
  - `schedules.department` — migration `013_schedules.up.sql` (`department TEXT NOT NULL DEFAULT ''`).
  - `published_workflows.department` — migration `010_workflow_visibility.up.sql` (`ADD COLUMN department TEXT`; nullable, set for `visibility='department'`).
- **DB access pattern:** repos hold a raw `*sql.DB` and use `db.ExecContext(ctx, sql, args...)` (see `adapter/sqlite/audit_repo.go:32`, `dismissal_repo.go:28`). Transactions use `db.BeginTx(ctx, nil)` + `tx.Rollback()` defer + `tx.Commit()` (see `user_repo.go:184`).
- **Wiring point:** `cmd/arbitron/main.go` — migrations run at line ~300 (`adaptsqlite.RunMigrations(db)`), `groupCfg := configadapter.ToGroupConfig(cfg.Auth.RBAC)` at line ~332. The reconcile pass runs AFTER both, BEFORE serving begins.

> **CAUTION — `user_departments` composite PK collision.** `user_departments` has `PRIMARY KEY (user_id, department)`. If a user is already a member of BOTH `from` and `to` (e.g. carried both the verbatim GUID and the friendly name in overlapping logins), a naive `UPDATE ... SET department = to WHERE department = from` would violate the unique PK and fail. Use `UPDATE OR IGNORE` for that one table so a row whose rename would collide with an existing `(user_id, to)` row is silently dropped (the membership already exists under the friendly name — no information lost). The other three tables have no such unique constraint on `department`, so a plain `UPDATE` is correct there. This keeps the reconcile idempotent and collision-safe.

---

### Task 3a: Define the `DepartmentRenamer` port and `ReconcileMappings`

**Files:**
- Create: `domain/identity/remap.go`
- Test: `domain/identity/remap_test.go`

- [ ] **Step 1: Write the failing tests** (happy path, remap state, idempotence, failure mode)

Create `domain/identity/remap_test.go`:

```go
package identity

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRenamer records each (from,to) it is asked to rename and can be made to
// fail on a chosen 'from'.
type fakeRenamer struct {
	calls   [][2]string
	failOn  string
	failErr error
}

func (f *fakeRenamer) RenameDepartment(_ context.Context, from, to string) (int64, error) {
	f.calls = append(f.calls, [2]string{from, to})
	if f.failOn != "" && from == f.failOn {
		return 0, f.failErr
	}
	return 1, nil
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(nopWriter{}, nil)) }

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReconcileMappings_RenamesEachChangedEntry(t *testing.T) {
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"guid-123": "engineering",
		"guid-b":   "sales",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.ElementsMatch(t, [][2]string{{"guid-123", "engineering"}, {"guid-b", "sales"}}, r.calls)
}

func TestReconcileMappings_SkipsIdentityRenames(t *testing.T) {
	// from == to after normalization => nothing to rewrite, skip the call.
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"engineering": "Engineering", // normalizes to engineering==engineering
		"guid-123":    "platform",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.Equal(t, [][2]string{{"guid-123", "platform"}}, r.calls, "identity rename skipped")
}

func TestReconcileMappings_NilMapping_NoOp(t *testing.T) {
	r := &fakeRenamer{}
	err := ReconcileMappings(context.Background(), GroupConfig{}, r, discardLog())
	require.NoError(t, err)
	assert.Empty(t, r.calls)
}

func TestReconcileMappings_RenamerError_FailsLoudly(t *testing.T) {
	boom := errors.New("db down")
	r := &fakeRenamer{failOn: "guid-123", failErr: boom}
	cfg := GroupConfig{Mapping: map[string]string{"guid-123": "engineering"}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.Error(t, err, "a renamer error must abort reconcile (fail-fast at boot)")
	assert.ErrorIs(t, err, boom)
}

func TestReconcileMappings_DropsBlankEntries(t *testing.T) {
	r := &fakeRenamer{}
	cfg := GroupConfig{Mapping: map[string]string{
		"   ":      "engineering", // blank from
		"guid-123": "   ",         // blank to
		"guid-ok":  "platform",
	}}
	err := ReconcileMappings(context.Background(), cfg, r, discardLog())
	require.NoError(t, err)
	assert.Equal(t, [][2]string{{"guid-ok", "platform"}}, r.calls)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./domain/identity/ -run TestReconcileMappings -v`
Expected: FAIL to compile — `DepartmentRenamer` / `ReconcileMappings` undefined.

- [ ] **Step 3: Implement the port and the reconciler**

Create `domain/identity/remap.go`:

```go
package identity

import (
	"context"
	"fmt"
	"log/slog"
)

// DepartmentRenamer rewrites every stored occurrence of a department string from
// one value to another, across ALL tables that persist a department string (the
// membership snapshot, run stamps, schedule attribution, and department-visibility
// publications). It is a domain port; the SQLite adapter implements it. RenameDepartment
// MUST be idempotent: once the stored values are already the target, a re-run
// matches nothing and renames zero rows. It returns the total rows changed.
type DepartmentRenamer interface {
	RenameDepartment(ctx context.Context, from, to string) (rows int64, err error)
}

// ReconcileMappings rewrites historical department strings so a newly-mapped
// department is seen under its friendly name everywhere. It walks cfg.Mapping and,
// for each entry whose normalized raw key differs from its normalized friendly
// value, calls renamer.RenameDepartment(from, to). Entries that normalize to a
// blank key or value are skipped, as are identity renames (from == to). The pass
// is idempotent (the renamer's WHERE matches nothing once values are friendly), so
// it is safe to run on every boot. The FIRST renamer error aborts and is returned
// so the caller fails the boot loudly (fail-fast); the per-entry transaction in
// the adapter prevents a partial rename within an entry.
func ReconcileMappings(ctx context.Context, cfg GroupConfig, renamer DepartmentRenamer, log *slog.Logger) error {
	for rawKey, friendly := range cfg.Mapping {
		from := NormalizeDept(rawKey)
		to := NormalizeDept(friendly)
		if from == "" || to == "" || from == to {
			continue
		}
		rows, err := renamer.RenameDepartment(ctx, from, to)
		if err != nil {
			return fmt.Errorf("reconcile mapping %q -> %q: %w", from, to, err)
		}
		if rows > 0 && log != nil {
			log.Info("reconciled department mapping", "from", from, "to", to, "rows", rows)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./domain/identity/ -run TestReconcileMappings -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/identity/remap.go domain/identity/remap_test.go
git commit -m "feat(identity): DepartmentRenamer port + idempotent ReconcileMappings"
```

---

### Task 3b: SQLite `DepartmentRenamer` over all four tables

**Files:**
- Create: `adapter/sqlite/department_renamer.go`
- Test: `adapter/sqlite/department_renamer_test.go`

- [ ] **Step 1: Inspect the test DB helper**

Run: `grep -rn "func newTestDB\|func testDB\|RunMigrations\|openTestDB\|func setupDB" adapter/sqlite/*_test.go | head`
Expected: find the helper that opens an in-memory/temp SQLite DB with migrations applied (e.g. `newTestDB(t)`). Reuse it in the test below. Also confirm how a user row is created for the FK on `user_departments.user_id` (likely `NewUserRepo(db).Create(...)` or a seeded default user).

- [ ] **Step 2: Write the failing test** (remap consistency across four tables + idempotence + collision)

Create `adapter/sqlite/department_renamer_test.go`. Adapt the DB helper / seeding to the conventions found in Step 1; the SQL inserts below assume the migration column sets verified above.

```go
package sqlite

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenameDepartment_RewritesAllFourTables(t *testing.T) {
	db := newTestDB(t) // adapt to the repo's helper name
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	// Seed a user so user_departments FK is satisfied.
	userID := uuid.New().String()
	_, err := db.ExecContext(ctx,
		`INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err) // adapt column list to the users table shape if it differs

	// 1) user_departments membership under the verbatim value.
	_, err = db.ExecContext(ctx,
		`INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)

	// 2) instance run stamped under verbatim.
	instID := uuid.New().String()
	_, err = db.ExecContext(ctx,
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, priority, variables, original_variables, department, visibility)
		 VALUES (?, ?, 'wf', '{}', 'queued', 'normal', '{}', '{}', 'guid-123', 'department')`, instID, userID)
	require.NoError(t, err) // NOTE: priority is TEXT with a CHECK ('high'|'normal'|'low'); state CHECK admits 'queued'

	// 3) schedule attributed to verbatim.
	schedID := uuid.New().String()
	_, err = db.ExecContext(ctx,
		`INSERT INTO schedules (id, workflow_name, cron_expr, creator_id, department)
		 VALUES (?, 'wf', '0 0 * * *', ?, 'guid-123')`, schedID, userID)
	require.NoError(t, err)

	// 4) department-visibility published workflow under verbatim.
	_, err = db.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name, visibility, department)
		 VALUES ('wf', ?, 'wf', 'department', 'guid-123')`, userID)
	require.NoError(t, err) // adapt to the published_workflows required columns

	rows, err := r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(4), rows, "one row rewritten in each of the four tables")

	// Every store now reads the friendly name; none retain the verbatim value.
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
	assertDeptCount(t, db, "instances", "engineering", 1)
	assertDeptCount(t, db, "schedules", "engineering", 1)
	assertDeptCount(t, db, "published_workflows", "engineering", 1)
}

func TestRenameDepartment_Idempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, `INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)

	rows, err := r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)

	// Second run: values are already friendly => WHERE matches nothing => 0 rows.
	rows, err = r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows, "idempotent: a re-run rewrites nothing")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
}

func TestRenameDepartment_MembershipCollision_DropsDuplicate(t *testing.T) {
	// A user already a member of BOTH the verbatim and the friendly value: the
	// UPDATE OR IGNORE keeps the existing friendly membership, drops the verbatim
	// duplicate (composite PK collision). No error, no duplicate.
	db := newTestDB(t)
	ctx := context.Background()
	r := NewDepartmentRenamer(db)

	userID := uuid.New().String()
	_, err := db.ExecContext(ctx, `INSERT INTO users (id, username, role) VALUES (?, 'alice', 'user')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'guid-123')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO user_departments (user_id, department) VALUES (?, 'engineering')`, userID)
	require.NoError(t, err)

	_, err = r.RenameDepartment(ctx, "guid-123", "engineering")
	require.NoError(t, err, "a PK collision must not fail the rename")
	assertDeptCount(t, db, "user_departments", "engineering", 1)
	assertDeptCount(t, db, "user_departments", "guid-123", 0)
}

// assertDeptCount asserts the number of rows in table whose department column equals dept.
func assertDeptCount(t *testing.T, db interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sqlRow
}, table, dept string, want int) {
	t.Helper()
	// NOTE: replace the interface above with the concrete *sql.DB if the helper
	// returns one; this signature is illustrative. Use a literal query per table to
	// avoid identifier interpolation.
	_ = table
	_ = dept
	_ = want
}
```

> The `assertDeptCount` helper above is a sketch — implement it with a straightforward `SELECT COUNT(*) FROM <table> WHERE department = ?` using the concrete `*sql.DB` the test helper returns, with a `switch table` over the four literal table names (do NOT interpolate the table identifier into the SQL string). Pattern:
> ```go
> func assertDeptCount(t *testing.T, db *sql.DB, table, dept string, want int) {
> 	t.Helper()
> 	var q string
> 	switch table {
> 	case "user_departments":
> 		q = `SELECT COUNT(*) FROM user_departments WHERE department = ?`
> 	case "instances":
> 		q = `SELECT COUNT(*) FROM instances WHERE department = ?`
> 	case "schedules":
> 		q = `SELECT COUNT(*) FROM schedules WHERE department = ?`
> 	case "published_workflows":
> 		q = `SELECT COUNT(*) FROM published_workflows WHERE department = ?`
> 	default:
> 		t.Fatalf("unknown table %q", table)
> 	}
> 	var n int
> 	require.NoError(t, db.QueryRowContext(context.Background(), q, dept).Scan(&n))
> 	assert.Equal(t, want, n, "%s.department=%q count", table, dept)
> }
> ```
> Drop the illustrative interface-based stub and use this concrete version.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./adapter/sqlite/ -run TestRenameDepartment -v`
Expected: FAIL to compile — `NewDepartmentRenamer` undefined.

- [ ] **Step 4: Implement the renamer**

Create `adapter/sqlite/department_renamer.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
)

var _ identity.DepartmentRenamer = (*DepartmentRenamer)(nil)

// DepartmentRenamer implements identity.DepartmentRenamer over SQLite. It rewrites
// a department string across ALL FOUR tables that persist one: user_departments
// (membership snapshot), instances (run stamp), schedules (attribution), and
// published_workflows (department-visibility publication). Omitting any of the four
// would leave a stale/orphaned department; in particular omitting published_workflows
// would hide a department-visibility published workflow from its members (now under
// the friendly name) because the publication still carried the old verbatim value.
type DepartmentRenamer struct {
	db *sql.DB
}

// NewDepartmentRenamer constructs the renamer over the given DB handle.
func NewDepartmentRenamer(db *sql.DB) *DepartmentRenamer {
	return &DepartmentRenamer{db: db}
}

// RenameDepartment rewrites every occurrence of `from` to `to` across the four
// department-bearing tables in a SINGLE transaction, returning the total rows
// changed. Idempotent: once the stored values are `to`, the WHERE clauses match
// nothing and it returns 0. The user_departments rename uses UPDATE OR IGNORE so a
// member already in BOTH `from` and `to` (composite PK (user_id, department))
// collapses to the single friendly membership instead of failing the unique
// constraint. The other three tables have no unique constraint on department, so a
// plain UPDATE is used.
func (r *DepartmentRenamer) RenameDepartment(ctx context.Context, from, to string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning rename-department transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	var total int64
	// (table-specific SQL, isOrIgnore) pairs. user_departments must use OR IGNORE.
	stmts := []struct {
		sql string
	}{
		{`UPDATE OR IGNORE user_departments SET department = ? WHERE department = ?`},
		{`UPDATE instances SET department = ? WHERE department = ?`},
		{`UPDATE schedules SET department = ? WHERE department = ?`},
		{`UPDATE published_workflows SET department = ? WHERE department = ?`},
	}
	for _, s := range stmts {
		res, err := tx.ExecContext(ctx, s.sql, to, from)
		if err != nil {
			return 0, fmt.Errorf("renaming department %q -> %q: %w", from, to, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("counting rows for %q -> %q: %w", from, to, err)
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing rename-department transaction: %w", err)
	}
	return total, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./adapter/sqlite/ -run TestRenameDepartment -v`
Expected: PASS for all three (`RewritesAllFourTables`, `Idempotent`, `MembershipCollision_DropsDuplicate`). If an `INSERT` in the test fails on a NOT NULL column not listed, add the missing column with a benign value to match that migration's required set (do not change the migrations).

- [ ] **Step 6: Commit**

```bash
git add adapter/sqlite/department_renamer.go adapter/sqlite/department_renamer_test.go
git commit -m "feat(sqlite): DepartmentRenamer rewrites all four department tables (txn, idempotent)"
```

---

### Task 3c: Run the reconcile pass at startup (fail-fast)

**Files:**
- Modify: `cmd/arbitron/main.go`
- Test: `cmd/arbitron/` has limited unit coverage; verify via an adapter-level integration assertion plus a manual boot check. (No new unit test file is required here if `cmd` has no existing test harness — the reconcile logic is already unit-tested in 3a/3b. If `cmd/arbitron` DOES have a bootstrap test harness, add an end-to-end "seed verbatim → boot → friendly" test there.)

- [ ] **Step 1: Wire the reconcile pass after migrations + groupCfg, before serving**

In `cmd/arbitron/main.go`, AFTER `groupCfg := configadapter.ToGroupConfig(cfg.Auth.RBAC)` (line ~332) and after the repos exist, add the reconcile call. Place it immediately after `groupCfg` is built (the renamer only needs `db`, which already exists):

```go
	// Remap reconcile (final-org-gaps Phase 2). Adding/changing a department
	// mapping must rewrite historical department strings so the friendly name is
	// seen everywhere (memberships, runs, schedules, department-visibility
	// publications). This runs ONCE at boot, after migrations + config load and
	// before serving; it is idempotent, so a clean boot with no new mappings is a
	// cheap no-op. A DB error here fails the boot loudly (fail-fast) rather than
	// serving a mis-scoped/duplicated department.
	deptRenamer := adaptsqlite.NewDepartmentRenamer(db)
	if err := identity.ReconcileMappings(context.Background(), groupCfg, deptRenamer, logger); err != nil {
		db.Close()
		logCloser.Close()
		return nil, fmt.Errorf("reconciling department mappings: %w", err)
	}
	logger.Info("department mapping reconcile complete")
```

(`identity` and `adaptsqlite` are already imported in main.go; `logger` and `db` are already in scope. `groupCfg` was built just above. Confirm the placement compiles — `groupCfg` must be declared before this block; if the build complains `groupCfg` is used before assignment, move this block to just after the `groupCfg :=` line.)

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: build succeeds.

- [ ] **Step 3: Run the full Phase-2-touched test surface**

Run: `go test ./domain/identity/ ./domain/instance/ ./adapter/edge/ ./adapter/http/ ./adapter/config/ ./adapter/sqlite/ -v`
Expected: PASS across all listed packages.

- [ ] **Step 4: Manual boot smoke check (remap consistency end-to-end)**

This verifies the reconcile actually rewrites on boot. Use a scratch DB:

```bash
# 1. Build.
go build -o /tmp/arbitron-p2 ./cmd/arbitron

# 2. Boot once with NO mapping to create the DB + a verbatim department row,
#    then seed a verbatim row directly (sqlite3) for instances/schedules/etc.
#    (Or rely on the adapter tests in 3b which already prove the rewrite.)
# 3. Boot with a mapping env var and confirm the log line:
ARBITRON_AUTH_MODE=edge \
ARBITRON_AUTH_EDGE_TEAMDOMAIN="https://example.cloudflareaccess.com" \
ARBITRON_AUTH_EDGE_AUDIENCES="app-aud" \
ARBITRON_AUTH_RBAC_GROUPCLAIM="groups" \
ARBITRON_AUTH_RBAC_MAPPING_GUID-123="engineering" \
/tmp/arbitron-p2 serve 2>&1 | grep -m1 "department mapping reconcile complete"
```

Expected: the boot logs "department mapping reconcile complete" (and, if a `guid-123` row existed, a "reconciled department mapping" line with `from=guid-123 to=engineering`). Note: edge boot performs OIDC discovery against the team domain, so a fully successful serve needs a reachable team domain — the reconcile line is emitted before serving regardless. If discovery is unreachable in your environment, rely on the adapter tests from 3b as the authoritative remap-consistency evidence.

- [ ] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "feat(boot): run idempotent department-mapping reconcile at startup (fail-fast)"
```

---

## Self-review checklist (run before handing off)

- [ ] `DepartmentRenamer` is a domain port (`domain/identity/remap.go`); the SQLite impl is the adapter — no infra import in `domain/identity`.
- [ ] The SQLite renamer rewrites **all four** tables (`user_departments`, `instances`, `schedules`, `published_workflows`) in ONE transaction per entry.
- [ ] `user_departments` uses `UPDATE OR IGNORE` (composite-PK collision safe); the other three use plain `UPDATE`.
- [ ] `ReconcileMappings` skips identity renames (`from == to`) and blank entries; first error aborts (fail-fast).
- [ ] Reconcile is idempotent — a second run rewrites 0 rows (proven by `TestRenameDepartment_Idempotent`).
- [ ] Boot runs the reconcile after migrations + config load, before serving; a DB error fails the boot.
- [ ] `Leads` keys are NOT touched by the renamer (operator config, normalized by the loader; out of scope per the design note).
- [ ] `go build ./...` green; the full Phase-2 package test surface passes.
