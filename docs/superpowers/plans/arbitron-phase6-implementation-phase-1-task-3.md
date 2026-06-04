# Phase 1, Task 3: Migration 003 + Domain `User` Type + `UserRepository` Expansion + sqlc Queries + sqlite Adapter

## Goal

Establish the persisted user model that auth resolves identities against: add migration `003` (the `sessions` table plus `users.subject` and `users.email` columns, and confirm the default user's `role='admin'`), define the domain `User{ID, Subject, Email, Username, Role}` type in `domain/instance`, expand the existing `UserRepository` port in place with `GetByID`/`GetBySubject`/`Create`, add the sqlc queries, regenerate, and implement the new methods on the sqlite `UserRepo`. The `sessions` table DDL also lands here (one migration), but its repository is Task 4.

**Cross-phase seam (bootstrap-admin matching):** the nullable `email` column added here is the persisted authority for the bootstrap-admin rule used by Phase 2. Both Phase 2 provisioning (Task 5) and the Phase 2 demotion floor (Task 6) match `AuthConfig.BootstrapAdmins` against the user's **email (case-insensitive), falling back to subject**. Because the floor in Task 6 runs with only the persisted `User` available (no live Claims), `email` MUST be persisted here so the post-provision floor can match a bootstrap-by-email admin.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §3, §6, §7.

## Dependencies

- Task 2 (`domain/identity`) — the domain `User.Role` field is typed as `identity.Role`, so `domain/instance` imports `domain/identity` (allowed; no cycle because identity does not import instance).

## Background (current state)

- `adapter/sqlite/migrations/001_initial_schema.up.sql` creates `users(id TEXT PK, username TEXT UNIQUE, role TEXT CHECK(user|admin) DEFAULT 'user', created_at)` and seeds `('00000000-0000-0000-0000-000000000001', 'local', 'admin')`. The default user is ALREADY admin — migration 003 only needs to add the nullable `subject` column; no role backfill is required, but the plan still asserts the admin invariant in tests.
- `adapter/sqlite/migrate.go` embeds `migrations/*.sql`, runs `*.up.sql` in sorted order, tracks versions in `schema_migrations`. Adding `003_auth.up.sql` is picked up automatically; the file is run in a single transaction.
- `adapter/sqlite/sqlc.yaml` generates from `queries/` against `migrations/` into `generated/`. The generated `User` struct (`generated/models.go`) is `{ID, Username, Role, CreatedAt}` — adding `subject` to the schema + a query regenerates it with the new column.
- `adapter/sqlite/queries/users.sql` has only `GetDefaultUser :one`. `adapter/sqlite/generated/users.sql.go` is its generated form.
- `adapter/sqlite/user_repo.go` (`UserRepo`) implements `instance.UserRepository.GetDefaultUser` using `parseUUID` (from `convert.go`). `NewUserRepo(db)` builds `generated.New(db)`.
- `domain/instance/ports.go` declares `UserRepository interface { GetDefaultUser(ctx) (uuid.UUID, error) }` — the engine already imports it.
- `domain/instance/instance.go` defines `ErrNotFound`. The expanded `GetByID`/`GetBySubject` return wrapped `instance.ErrNotFound` on a missing row (mirroring the port doc-comment contract in `ports.go`).

## Files to Create/Modify

### Create: `adapter/sqlite/migrations/003_auth.up.sql`

```sql
-- Phase 6 (Identity Core): add the IdP-stable subject and email to users and
-- create the server-side sessions table. The default 'local' user keeps
-- role='admin' from migration 001; org-provisioned users carry a non-null subject.

ALTER TABLE users ADD COLUMN subject TEXT;
-- email is nullable (the pre-existing 'local' user has none). It is the persisted
-- authority for the bootstrap-admin rule consumed by Phase 2 (provisioning AND the
-- demotion floor), which matches AuthConfig.BootstrapAdmins on email (case-insensitive),
-- falling back to subject. Stored as provided by the IdP claim; comparisons lower-case.
ALTER TABLE users ADD COLUMN email TEXT;
-- Unique only among non-null subjects: the pre-existing 'local' user has a NULL
-- subject and must not collide with itself or block other NULLs.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_subject
    ON users (subject) WHERE subject IS NOT NULL;

-- Sessions: id_hash is sha256(raw cookie token) — the raw token is NEVER stored
-- (the cookie holds it). A DB read leak therefore cannot mint a session.
CREATE TABLE IF NOT EXISTS sessions (
    id_hash    TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
```

(Also add `003_auth.down.sql` if the repo keeps down-migrations — current `migrate.go` only runs `.up.sql`, so a down file is optional/documentation only. Check sibling migrations; if none have `.down.sql`, skip it.)

### Modify: `adapter/sqlite/queries/users.sql`

`GetDefaultUser` must keep working; sqlc generates the full row scan, so add `subject` to the selected columns there and in the new queries. Update + add:

```sql
-- name: GetDefaultUser :one
SELECT id, subject, email, username, role, created_at FROM users WHERE username = 'local' LIMIT 1;

-- name: GetUserByID :one
SELECT id, subject, email, username, role, created_at FROM users WHERE id = ? LIMIT 1;

-- name: GetUserBySubject :one
SELECT id, subject, email, username, role, created_at FROM users WHERE subject = ? LIMIT 1;

-- name: CreateUser :exec
INSERT INTO users (id, subject, email, username, role) VALUES (?, ?, ?, ?, ?);
```

`subject` and `email` are nullable, so the generated `User.Subject`/`User.Email` become `sql.NullString` and `GetUserBySubject` takes a `sql.NullString` / the param-struct equivalent.

### Regenerate: `adapter/sqlite/generated/` (sqlc)

Run `sqlc generate` from `adapter/sqlite/` (config `sqlc.yaml`). Expected: `generated/models.go` `User` gains `Subject sql.NullString` and `Email sql.NullString`; `generated/users.sql.go` gains `GetUserByID`, `GetUserBySubject`, `CreateUser`, and `GetDefaultUser` now scans `Subject`/`Email`. Commit the regenerated files (do not hand-edit generated code).

### Modify: `domain/instance/ports.go` (and a new `user.go`)

Define the domain `User` and expand the port. Put `User` in a new `domain/instance/user.go` (keeps `ports.go` interface-only), or alongside the port — either is fine; the plan uses `user.go`:

```go
// domain/instance/user.go
package instance

import (
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// User is the persisted account auth resolves identities against. Subject is the
// IdP-stable identifier; it is nil for the pre-existing default 'local' user and
// for any user created outside the OIDC path. Email is the IdP email claim
// (nullable); it is the persisted authority for the bootstrap-admin rule
// (Phase 2: AuthConfig.BootstrapAdmins matches email case-insensitively, falling
// back to subject), so the demotion floor can match a bootstrap-by-email admin
// using only the persisted User.
type User struct {
	ID       uuid.UUID
	Subject  *string // nullable: the default user has none
	Email    *string // nullable: IdP email claim; bootstrap-admin match authority
	Username string
	Role     identity.Role
}
```

```go
// domain/instance/ports.go — expand in place:
type UserRepository interface {
	GetDefaultUser(ctx context.Context) (uuid.UUID, error)        // existing, unchanged
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)     // ErrNotFound if absent
	GetBySubject(ctx context.Context, subject string) (*User, error) // ErrNotFound if new (JIT in Phase 2)
	Create(ctx context.Context, u *User) error
}
```

### Modify: `adapter/sqlite/user_repo.go`

Implement the three new methods, mapping `generated.User` → `instance.User` and wrapping a missing row as `instance.ErrNotFound`. Reuse `parseUUID`/`uuidToString` (convert.go) and `nullStringToPtr`/`ptrToNullString` for `subject`:

```go
import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ instance.UserRepository = (*UserRepo)(nil)

func toDomainUser(row generated.User) (*instance.User, error) {
	id, err := parseUUID(row.ID)
	if err != nil {
		return nil, err
	}
	return &instance.User{
		ID:       id,
		Subject:  nullStringToPtr(row.Subject),
		Email:    nullStringToPtr(row.Email),
		Username: row.Username,
		Role:     identity.Role(row.Role),
	}, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*instance.User, error) {
	row, err := r.queries.GetUserByID(ctx, uuidToString(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting user %s: %w", id, instance.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting user %s: %w", id, err)
	}
	return toDomainUser(row)
}

func (r *UserRepo) GetBySubject(ctx context.Context, subject string) (*instance.User, error) {
	row, err := r.queries.GetUserBySubject(ctx, ptrToNullString(&subject))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting user by subject: %w", instance.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting user by subject: %w", err)
	}
	return toDomainUser(row)
}

func (r *UserRepo) Create(ctx context.Context, u *instance.User) error {
	err := r.queries.CreateUser(ctx, generated.CreateUserParams{
		ID:       uuidToString(u.ID),
		Subject:  ptrToNullString(u.Subject),
		Email:    ptrToNullString(u.Email),
		Username: u.Username,
		Role:     string(u.Role),
	})
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}
```

(`GetUserBySubject` may take a bare `string` rather than `sql.NullString` depending on how sqlc types the nullable comparison — adjust the call to match the regenerated signature. If sqlc emits a `sql.NullString` param, pass `sql.NullString{String: subject, Valid: true}`.)

Also update the existing `GetDefaultUser` scan if the regenerated `GetDefaultUser` returns the new column shape (it still returns just the id via `user.ID`, so the existing body is unaffected beyond the regenerated row type).

## Testing (strict TDD — five scenario categories)

Add to `adapter/sqlite/integration_test.go` (uses `setupTestDB`) plus a `domain/instance` unit test. Write first.

- **Happy path** — `TestUserRepo_Create_GetByID_RoundTrip`: create a `User{ID: uuid.New(), Subject: ptr("sub-123"), Username: "alice", Role: identity.RoleUser}`; `GetByID` returns the same fields; `GetBySubject("sub-123")` returns the same user.
- **Input validation** — `TestUserRepo_Create_DuplicateSubject`: creating two users with the same non-nil subject returns an error (unique index violation); two users with NULL subject are allowed (the partial unique index excludes NULLs) — covers the default-user-coexistence invariant.
- **Error/failure** — `TestUserRepo_GetByID_NotFound` / `TestUserRepo_GetBySubject_NotFound`: a random UUID / unknown subject returns an error satisfying `errors.Is(err, instance.ErrNotFound)`.
- **Boundary** — `TestUserRepo_GetByID_DefaultUser`: `GetByID` of the seeded default-user UUID returns `Username=="local"`, `Role==identity.RoleAdmin`, `Subject==nil`, `Email==nil`; and `TestRunMigrations_003_SessionsTableExists`: after migrations, `sessions` and the `users.subject`/`users.email` columns exist (`PRAGMA table_info(users)` shows `subject` and `email`).
- **Isolation/security** — `TestRunMigrations_DefaultAdminSeeded` (extend existing): default user is `role='admin'` after 003; and `TestUserRepo_GetBySubject_NullSubjectNotMatched`: `GetBySubject("")` does NOT return the default user (a NULL subject must never match an empty-string lookup — guards against an empty-subject OIDC claim hijacking the admin account).

## Acceptance Criteria

- Migration 003 adds `users.subject` (nullable, partial-unique), `users.email` (nullable), and the `sessions` table; default user remains `role='admin'`.
- `domain/instance.User{ID, Subject, Email, Username, Role identity.Role}` defined; `domain/instance` imports `domain/identity` with no cycle.
- `UserRepository` has `GetByID`/`GetBySubject`/`Create`; `GetDefaultUser` unchanged; sqlite `UserRepo` satisfies the port (compile-time `var _` assertion).
- sqlc files regenerated (not hand-edited); `go generate`/`sqlc generate` reproduces them.
- `go build ./...` and `go test ./adapter/sqlite/... ./domain/instance/...` pass.
