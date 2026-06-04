# UI Nits Phase 3: Per-User Instance Dismissal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let each user dismiss finished (Completed/Failed) queue items from their own view, dismiss a whole category at once, reveal/restore dismissed items via a global toggle, and have a retried item resurface on its new outcome — all per user and decoupled from instance lifecycle.

**Architecture:** Go hexagonal — a `domain/instance.DismissalRepository` port persisted by a SQLite adapter (`adapter/sqlite/dismissal_repo.go`) over hand-written SQL on a new `instance_dismissals` table; chi HTTP handlers mutate/query dismissals through a `currentUser(r)` acting-user seam; the engine's `Retry` hook clears dismissals; the React/TypeScript dashboard adds dismiss/restore controls and a Show/Hide toggle. Dismissal state is per `(user_id, instance_id)` and independent of instance state.

**Tech Stack:** Go, SQLite (sqlc for instance/connection queries; hand-written SQL for the dismissal repo, matching `notification_repo.go`), chi, React 19/TypeScript, Vitest + Testing Library (dashboard), `go test` (backend).

---

### Task 1: Migration `003_instance_dismissals` + domain `DismissalRepository` port + SQLite adapter + repo tests

**Context for the implementer (read before starting):**

- The migration runner is `adapter/sqlite/migrate.go`: it embeds `migrations/*.sql`, sorts `*.up.sql` lexically, parses the leading integer version with `fmt.Sscanf(name, "%d_", &versionNum)`, and applies each unapplied file in a transaction. Down-migrations are **not** run by the runner; the `.down.sql` file is kept for parity with `002_connections.down.sql` and manual rollback. Latest existing migration is `002_connections`.
- The `notifications`/`instances`/`users` repos in this package use **hand-written SQL** via `r.db.ExecContext` / `r.db.QueryContext` (see `adapter/sqlite/notification_repo.go`). `instance_repo.go` uses sqlc-generated queries. **Follow the `notification_repo.go` hand-written style for the dismissal repo** — it is simpler, needs no `sqlc generate` step, and matches the design's "following `notification_repo.go` conventions." (If the reviewer prefers sqlc, a sqlc variant is noted at the end of this task, but the hand-written path is the default.)
- UUID/time helpers live in `adapter/sqlite/convert.go`: `uuidToString(uuid.UUID) string`, `parseUUID(string) (uuid.UUID, error)`, `timeToString(time.Time) string`, `parseTime(string) (time.Time, error)`.
- Test style: `adapter/sqlite/connection_repo_test.go` + `integration_test.go`. Tests use `setupTestDB(t)` (returns `testDB{DB *sql.DB}`, in-memory DB with migrations applied, auto-closed via `t.Cleanup`), `testify/require` + `testify/assert`, and section-comment banners (Happy / Success / Failure / Error / Edge). The seeded default user is `00000000-0000-0000-0000-000000000001` (username `local`, role `admin`).
- `instances.owner_id` is `TEXT NOT NULL REFERENCES users(id)`; `instances.id` is `TEXT PRIMARY KEY`. SQLite stores UUIDs as their `.String()` form.
- **SQLite foreign keys are OFF by default.** For `ON DELETE CASCADE` to fire, the connection must have `PRAGMA foreign_keys = ON`. Verify whether `adapter/sqlite/Open` already sets this (grep for `foreign_keys` / `_pragma` / `_foreign_keys` in `adapter/sqlite`). If it does not, the cascade test below documents the gap and asserts the actually-observed behavior; do **not** silently change global PRAGMAs without confirming with the existing `Open` configuration.

**Files:**
- `adapter/sqlite/migrations/003_instance_dismissals.up.sql` (new)
- `adapter/sqlite/migrations/003_instance_dismissals.down.sql` (new)
- `domain/instance/dismissal.go` (new — port + sentinel, in package `instance`)
- `adapter/sqlite/dismissal_repo.go` (new)
- `adapter/sqlite/dismissal_repo_test.go` (new)

---

#### Step 1.1 — Failing test: the `instance_dismissals` table exists after migration

- [ ] Create `adapter/sqlite/dismissal_repo_test.go` with the package and a migration-existence test (mirrors `TestConnectionsMigration_TableExists`):

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

const seededLocalUserID = "00000000-0000-0000-0000-000000000001"

// ---------------------------------------------------------------------------
// Migration: the instance_dismissals table exists after RunMigrations.
// ---------------------------------------------------------------------------

func TestInstanceDismissalsMigration_TableExists(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM instance_dismissals").Scan(&count))
	assert.Equal(t, 0, count)
}
```

- [ ] Run: `go test ./adapter/sqlite/ -run TestInstanceDismissalsMigration_TableExists`
- [ ] Expected: **FAIL** — `no such table: instance_dismissals`.

#### Step 1.2 — Minimal impl: write migration 003 up/down

- [ ] Create `adapter/sqlite/migrations/003_instance_dismissals.up.sql`:

```sql
-- Per-user dismissal of terminal (completed/failed) instances. A row means the
-- user has dismissed that instance from their own queue view. It does not alter
-- or delete the instance. PRIMARY KEY (user_id, instance_id) makes dismiss
-- idempotent (INSERT ... ON CONFLICT DO NOTHING). ON DELETE CASCADE cleans up
-- if an instance row is ever deleted (requires PRAGMA foreign_keys = ON).
CREATE TABLE IF NOT EXISTS instance_dismissals (
    user_id      TEXT NOT NULL REFERENCES users(id),
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    dismissed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    PRIMARY KEY (user_id, instance_id)
);

CREATE INDEX IF NOT EXISTS idx_instance_dismissals_user
    ON instance_dismissals (user_id);
```

- [ ] Create `adapter/sqlite/migrations/003_instance_dismissals.down.sql`:

```sql
DROP TABLE IF EXISTS instance_dismissals;
```

- [ ] Run: `go test ./adapter/sqlite/ -run TestInstanceDismissalsMigration_TableExists`
- [ ] Expected: **PASS**.
- [ ] Commit: `feat(dismissal): migration 003_instance_dismissals [ui-nits p3 t1]`

#### Step 1.3 — Failing test: domain port shape compiles

- [ ] Append to `dismissal_repo_test.go` a compile-time interface assertion that pins the port and the concrete repo (this fails to compile until both exist):

```go
// dismissalRepoSatisfiesPort fails to compile if *DismissalRepo no longer
// implements the domain port.
var _ instance.DismissalRepository = (*DismissalRepo)(nil)
```

- [ ] Run: `go test ./adapter/sqlite/ -run TestInstanceDismissalsMigration_TableExists`
- [ ] Expected: **FAIL (build)** — `undefined: instance.DismissalRepository` and `undefined: DismissalRepo`.

#### Step 1.4 — Minimal impl: domain port + sentinel

- [ ] Create `domain/instance/dismissal.go`:

```go
package instance

import (
	"context"

	"github.com/google/uuid"
)

// DismissalRepository is the port for per-user instance dismissals. A dismissal
// is keyed by (userID, instanceID) and is independent of the instance's own
// lifecycle, except that engine.Retry clears all dismissals for an instance via
// ClearForInstance. Implementations must wrap any domain sentinel errors with
// fmt.Errorf %w so the HTTP adapter's errors.Is mapping works.
type DismissalRepository interface {
	// Dismiss records that userID has dismissed instanceID. It is idempotent:
	// dismissing an already-dismissed instance is a no-op success.
	Dismiss(ctx context.Context, userID, instanceID uuid.UUID) error
	// Restore removes userID's dismissal of instanceID. Restoring a
	// non-dismissed instance is a no-op success.
	Restore(ctx context.Context, userID, instanceID uuid.UUID) error
	// DismissedIDs returns the set of instance IDs userID has dismissed.
	DismissedIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]bool, error)
	// ClearForInstance removes the dismissal of instanceID for every user.
	// Called on retry so a re-running instance resurfaces for everyone who had
	// dismissed it.
	ClearForInstance(ctx context.Context, instanceID uuid.UUID) error
}
```

- [ ] Do not add this method to the existing `InstanceRepository` interface in `ports.go`; it is a separate port so existing mocks (e.g. `errInstanceRepo` in `adapter/http/instance_handler_test.go`) keep compiling.

#### Step 1.5 — Minimal impl: SQLite adapter

- [ ] Create `adapter/sqlite/dismissal_repo.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// DismissalRepo implements instance.DismissalRepository backed by SQLite,
// following the hand-written-SQL conventions of NotificationRepo.
type DismissalRepo struct {
	db *sql.DB
}

// NewDismissalRepo creates a DismissalRepo using the given database connection.
func NewDismissalRepo(db *sql.DB) *DismissalRepo {
	return &DismissalRepo{db: db}
}

// Dismiss is idempotent: ON CONFLICT DO NOTHING means dismissing an
// already-dismissed instance succeeds without changing dismissed_at.
func (r *DismissalRepo) Dismiss(ctx context.Context, userID, instanceID uuid.UUID) error {
	query := `INSERT INTO instance_dismissals (user_id, instance_id)
VALUES (?, ?)
ON CONFLICT(user_id, instance_id) DO NOTHING`
	_, err := r.db.ExecContext(ctx, query, uuidToString(userID), uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("dismissing instance: %w", err)
	}
	return nil
}

// Restore removes the dismissal; deleting zero rows (not dismissed) is a no-op
// success, matching the idempotent semantics in the design.
func (r *DismissalRepo) Restore(ctx context.Context, userID, instanceID uuid.UUID) error {
	query := `DELETE FROM instance_dismissals WHERE user_id = ? AND instance_id = ?`
	_, err := r.db.ExecContext(ctx, query, uuidToString(userID), uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("restoring instance: %w", err)
	}
	return nil
}

// DismissedIDs returns the set of instance IDs userID has dismissed.
func (r *DismissalRepo) DismissedIDs(ctx context.Context, userID uuid.UUID) (map[uuid.UUID]bool, error) {
	query := `SELECT instance_id FROM instance_dismissals WHERE user_id = ?`
	rows, err := r.db.QueryContext(ctx, query, uuidToString(userID))
	if err != nil {
		return nil, fmt.Errorf("listing dismissed instance ids: %w", err)
	}
	defer rows.Close()

	ids := make(map[uuid.UUID]bool)
	for rows.Next() {
		var idStr string
		if err := rows.Scan(&idStr); err != nil {
			return nil, fmt.Errorf("scanning dismissed instance id: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("converting dismissed instance id: %w", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dismissed instance ids: %w", err)
	}
	return ids, nil
}

// ClearForInstance removes the dismissal of instanceID for every user.
func (r *DismissalRepo) ClearForInstance(ctx context.Context, instanceID uuid.UUID) error {
	query := `DELETE FROM instance_dismissals WHERE instance_id = ?`
	_, err := r.db.ExecContext(ctx, query, uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("clearing dismissals for instance: %w", err)
	}
	return nil
}
```

- [ ] Run: `go build ./...`
- [ ] Expected: **PASS (builds)**; the `var _ instance.DismissalRepository = (*DismissalRepo)(nil)` assertion now compiles.
- [ ] Run: `go test ./adapter/sqlite/ -run TestInstanceDismissals`
- [ ] Expected: **PASS** (only the migration test exists so far).
- [ ] Commit: `feat(dismissal): DismissalRepository port + sqlite adapter [ui-nits p3 t1]`

#### Step 1.6 — Failing tests: full repo behavior (5 scenario categories)

- [ ] Add a test helper and the behavioral tests to `dismissal_repo_test.go`. The helper seeds two users (the seeded `local` + a second user) and one terminal instance, since dismissals reference `users(id)` and `instances(id)`:

```go
func newDismissalRepo(t *testing.T) (*DismissalRepo, testDB) {
	t.Helper()
	db := setupTestDB(t)
	return NewDismissalRepo(db.DB), db
}

// seedUser inserts a user row and returns its UUID.
func seedUser(t *testing.T, db testDB, username string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		uuidToString(id), username)
	require.NoError(t, err)
	return id
}

// seedInstance inserts a minimal completed instance owned by ownerID and
// returns its UUID. workflow_snapshot/variables default to '{}' via the schema
// but are set explicitly here for clarity.
func seedInstance(t *testing.T, db testDB, ownerID uuid.UUID, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state)
		 VALUES (?, ?, 'wf', '{}', ?)`,
		uuidToString(id), uuidToString(ownerID), state)
	require.NoError(t, err)
	return id
}

func localUserID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(seededLocalUserID)
	require.NoError(t, err)
	return id
}
```

```go
// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// Happy: Dismiss then DismissedIDs returns that instance for the user.
func TestDismissalRepo_DismissThenListed(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.True(t, ids[inst], "dismissed instance must appear in DismissedIDs")
	assert.Len(t, ids, 1)
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// Edge: Dismiss is idempotent — dismissing twice succeeds and leaves one row.
func TestDismissalRepo_DismissIdempotent(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "failed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))
	require.NoError(t, repo.Dismiss(ctx, user, inst), "second Dismiss must be a no-op success")

	var count int
	require.NoError(t, db.DB.QueryRow(
		"SELECT COUNT(*) FROM instance_dismissals WHERE user_id = ? AND instance_id = ?",
		uuidToString(user), uuidToString(inst)).Scan(&count))
	assert.Equal(t, 1, count, "idempotent dismiss must not create duplicate rows")
}

// Edge: Restore of a non-dismissed instance is a no-op success.
func TestDismissalRepo_RestoreNonDismissedNoOp(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Restore(ctx, user, inst), "restore of a non-dismissed item is a no-op success")

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// Edge: DismissedIDs on an empty store returns a non-nil empty map.
func TestDismissalRepo_DismissedIDsEmpty(t *testing.T) {
	repo, _ := newDismissalRepo(t)
	ids, err := repo.DismissedIDs(context.Background(), localUserID(t))
	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Empty(t, ids)
}

// ---------------------------------------------------------------------------
// Success
// ---------------------------------------------------------------------------

// Success: Dismiss then Restore removes the dismissal.
func TestDismissalRepo_DismissThenRestore(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))
	require.NoError(t, repo.Restore(ctx, user, inst))

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.False(t, ids[inst])
	assert.Empty(t, ids)
}

// ---------------------------------------------------------------------------
// Boundary / scoping
// ---------------------------------------------------------------------------

// Boundary: DismissedIDs is scoped per user — user A's dismissal is invisible
// to user B even for the same instance.
func TestDismissalRepo_DismissedIDsScopedPerUser(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	userA := localUserID(t)
	userB := seedUser(t, db, "bob")
	inst := seedInstance(t, db, userA, "failed")

	require.NoError(t, repo.Dismiss(ctx, userA, inst))

	idsA, err := repo.DismissedIDs(ctx, userA)
	require.NoError(t, err)
	assert.True(t, idsA[inst])

	idsB, err := repo.DismissedIDs(ctx, userB)
	require.NoError(t, err)
	assert.Empty(t, idsB, "user B must not see user A's dismissal")
}

// Boundary: ClearForInstance removes the dismissal across ALL users.
func TestDismissalRepo_ClearForInstanceAcrossUsers(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	userA := localUserID(t)
	userB := seedUser(t, db, "bob")
	inst := seedInstance(t, db, userA, "failed")

	require.NoError(t, repo.Dismiss(ctx, userA, inst))
	require.NoError(t, repo.Dismiss(ctx, userB, inst))

	require.NoError(t, repo.ClearForInstance(ctx, inst))

	idsA, err := repo.DismissedIDs(ctx, userA)
	require.NoError(t, err)
	idsB, err := repo.DismissedIDs(ctx, userB)
	require.NoError(t, err)
	assert.Empty(t, idsA, "clear must remove user A's dismissal")
	assert.Empty(t, idsB, "clear must remove user B's dismissal")
}

// ---------------------------------------------------------------------------
// Integration: cascade on instance delete
// ---------------------------------------------------------------------------

// Integration: deleting the instance removes the dismissal row IF the SQLite
// connection has foreign keys enabled. This documents the cascade contract.
// If Open() does not enable PRAGMA foreign_keys, assert the observed behavior
// (row remains) and the cascade is exercised only when FKs are on.
func TestDismissalRepo_CascadeOnInstanceDelete(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")
	require.NoError(t, repo.Dismiss(ctx, user, inst))

	// Confirm whether FK enforcement is active on this connection.
	var fk int
	require.NoError(t, db.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk))

	_, err := db.DB.Exec("DELETE FROM instances WHERE id = ?", uuidToString(inst))
	require.NoError(t, err)

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	if fk == 1 {
		assert.Empty(t, ids, "ON DELETE CASCADE must remove the dismissal when FKs are enabled")
	} else {
		t.Skip("foreign_keys pragma is OFF on this connection; cascade not exercised")
	}
}
```

- [ ] Run: `go test ./adapter/sqlite/ -run TestDismissalRepo`
- [ ] Expected: **PASS** for all (the impl from Step 1.5 already satisfies them). If the cascade test reports FKs OFF, leave the `t.Skip` branch — do not change global PRAGMA settings as part of this task; record the gap in the task report instead.

> Note: if `Open()` enables `_foreign_keys=on` (check the DSN/connector in `adapter/sqlite`), the cascade assertion runs and must pass.

- [ ] Run the full backend gate: `make test` (equivalent to `go test ./...`).
- [ ] Expected: **PASS** — no existing test regressed (the new port is additive; no existing interface changed).
- [ ] Commit: `test(dismissal): repo idempotency/scoping/cascade tests [ui-nits p3 t1]`

---

#### Optional sqlc variant (only if the reviewer mandates generated queries)

If hand-written SQL is rejected in review, mirror `instances.sql`:

- [ ] Add `adapter/sqlite/queries/instance_dismissals.sql` with: `DismissInstance :exec` (`INSERT ... ON CONFLICT(user_id, instance_id) DO NOTHING`), `RestoreInstance :exec` (`DELETE WHERE user_id = ? AND instance_id = ?`), `ListDismissedInstanceIDs :many` (`SELECT instance_id WHERE user_id = ?`), `ClearInstanceDismissals :exec` (`DELETE WHERE instance_id = ?`).
- [ ] There is **no Makefile target** for sqlc. Generate manually from the adapter dir: `cd adapter/sqlite && sqlc generate` (config: `adapter/sqlite/sqlc.yaml`, which writes package `generated` into `adapter/sqlite/generated/`). **The generated `.sql.go` files MUST be committed** (the repo checks in generated code).
- [ ] Rewrite `dismissal_repo.go` to hold `queries *generated.Queries` (see `instance_repo.go` constructor) and call the generated methods; `DismissedIDs` still builds the `map[uuid.UUID]bool` from the returned `[]string`.
- [ ] Re-run the same Step 1.6 tests; they are implementation-agnostic and must still pass.

**Definition of done for Task 1:** migration 003 up/down committed; `domain/instance.DismissalRepository` port committed; `adapter/sqlite/dismissal_repo.go` implements it; repo tests cover happy/edge/success/boundary/integration (dismiss idempotent, restore no-op, scoping per user, clear across users, cascade contract); `make test` green.
