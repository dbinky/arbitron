# Phase 1, Task 4: SessionRepository sqlite Adapter — Token Hashing, Expiry Filtering, and the Expiry Sweep

## Goal

Implement the `identity.SessionRepository` port over SQLite in `adapter/sqlite/session_repo.go`: `Create`, `Get` (hash-on-lookup + server-side expiry filter), `Delete`, and `DeleteExpired`. The cookie carries the raw token; the DB stores only `sha256(token)`. Add the sqlc queries against the `sessions` table from migration 003, and a `secrecy`-style test proving the raw token is never persisted. Provide a background sweep that periodically calls `DeleteExpired` (wired in Task 8, reusing the existing scheduler/poll-loop pattern).

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §6.

## Dependencies

- Task 2 (`domain/identity`) — defines `Session`, `SessionRepository`, `ErrNotFound`.
- Task 3 — creates the `sessions` table (migration 003) and is the prerequisite for sqlc generation against it.

## Background (current state)

- The `sessions` table is created by `adapter/sqlite/migrations/003_auth.up.sql` (Task 3): `id_hash TEXT PK`, `user_id TEXT FK users(id) ON DELETE CASCADE`, `created_at`, `expires_at`, plus `idx_sessions_expires_at`.
- House sqlite adapter pattern: a struct holding `*sql.DB` + `*generated.Queries` (`generated.New(db)`), built by a `New...Repo(db)` constructor, with a compile-time `var _ <port> = (*Repo)(nil)` assertion (see `adapter/sqlite/connection_repo.go`, `user_repo.go`).
- Time conversion helpers exist in `adapter/sqlite/convert.go`: `timeToString` (RFC3339 UTC), `parseTime`, `uuidToString`, `parseUUID`. Timestamps are stored as RFC3339 TEXT, so string comparison on `expires_at` is chronological-safe (RFC3339 is lexicographically ordered for same-offset UTC `Z` strings).
- The secrecy-test precedent is `adapter/sqlite/secrecy_test.go` (`TestSecrecy_*`): drive a sentinel through the real store and assert it never appears in storage/logs.
- The sweep pattern: `queue.Scheduler`/`queue.Manager` already run a background timer loop (`scheduler.Start(ctx)`); a similar lightweight ticker can call `DeleteExpired`. The actual goroutine wiring is Task 8; this task provides the method + an optional `SessionSweeper` helper type that is trivially unit-testable without a real ticker.

## Files to Create/Modify

### Create: `adapter/sqlite/queries/sessions.sql`

```sql
-- name: CreateSession :exec
INSERT INTO sessions (id_hash, user_id, created_at, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetSession :one
-- Returns the row ONLY if not yet expired; an expired row is treated as absent
-- so the adapter maps both "no row" and "expired" to ErrNotFound uniformly.
SELECT id_hash, user_id, created_at, expires_at
FROM sessions
WHERE id_hash = ? AND expires_at > ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= ?;
```

Regenerate with `sqlc generate` (from `adapter/sqlite/`). `:execrows` makes `DeleteExpiredSessions` return `(int64, error)` so the sweep can report a count.

### Create: `adapter/sqlite/session_repo.go`

```go
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
)

// SessionRepo implements identity.SessionRepository backed by SQLite.
//
// SECURITY: the raw session token (the cookie value) is NEVER stored. Only its
// sha256 hex digest is written to / read from the id_hash column, so a database
// read leak yields hashes that cannot be replayed as cookies. Expiry is filtered
// server-side in SQL (GetSession's expires_at > now), so an expired row is
// indistinguishable from absent and never authenticates a request.
type SessionRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

var _ identity.SessionRepository = (*SessionRepo)(nil)

func NewSessionRepo(db *sql.DB) *SessionRepo {
	return &SessionRepo{db: db, queries: generated.New(db)}
}

// hashToken returns the hex sha256 of the raw token. Centralised so Create and
// Get hash identically.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (r *SessionRepo) Create(ctx context.Context, s *identity.Session) error {
	if s == nil || s.ID == "" {
		return fmt.Errorf("creating session: empty token")
	}
	err := r.queries.CreateSession(ctx, generated.CreateSessionParams{
		IDHash:    hashToken(s.ID),
		UserID:    uuidToString(s.UserID),
		CreatedAt: timeToString(s.CreatedAt),
		ExpiresAt: timeToString(s.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

func (r *SessionRepo) Get(ctx context.Context, id string) (*identity.Session, error) {
	row, err := r.queries.GetSession(ctx, generated.GetSessionParams{
		IDHash:    hashToken(id),
		ExpiresAt: timeToString(time.Now().UTC()),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting session: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	userID, err := parseUUID(row.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	// NOTE: ID is intentionally NOT repopulated with the raw token (the store
	// never had it); callers that need the cookie value already hold it.
	return &identity.Session{
		UserID:    userID,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}, nil
}

func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	if err := r.queries.DeleteSession(ctx, hashToken(id)); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

func (r *SessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	n, err := r.queries.DeleteExpiredSessions(ctx, timeToString(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("deleting expired sessions: %w", err)
	}
	return int(n), nil
}
```

(Exact generated param-struct/field names — `CreateSessionParams`, `GetSessionParams`, `IDHash` — come from sqlc's rename of `id_hash`; confirm against the regenerated `generated/sessions.sql.go` and adjust. The `rename:` block in `sqlc.yaml` does not currently map `id_hash`; sqlc's default camel-casing yields `IDHash`, which is fine.)

### Create (sweep helper): `adapter/sqlite/session_sweep.go` (or place in session_repo.go)

```go
// SweepExpiredSessions runs DeleteExpired on a ticker until ctx is cancelled.
// Wired in cmd/arbitron/main.go (Task 8) only in org mode. Errors are logged,
// not fatal — a transient DB error on one tick must not kill the loop.
func SweepExpiredSessions(ctx context.Context, repo identity.SessionRepository, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n, err := repo.DeleteExpired(ctx, now.UTC()); err != nil {
				log.Error("session sweep failed", "error", err)
			} else if n > 0 {
				log.Debug("session sweep removed expired sessions", "count", n)
			}
		}
	}
}
```

## Testing (strict TDD — five scenario categories)

Tests in `adapter/sqlite/session_repo_test.go` (uses `setupTestDB`) and an addition to the secrecy suite. Write first.

- **Happy path** — `TestSessionRepo_Create_Get_RoundTrip`: create a session (raw token, valid user from the seeded default user or a created user, `ExpiresAt = now+1h`); `Get(rawToken)` returns the session with the right `UserID` and `ExpiresAt` (within RFC3339 second granularity).
- **Input validation** — `TestSessionRepo_Create_EmptyToken`: `Create` with `ID==""` returns an error and writes no row.
- **Error/failure** — `TestSessionRepo_Get_Unknown`: `Get("never-issued")` returns `errors.Is(err, identity.ErrNotFound)`. `TestSessionRepo_Delete_ThenGet`: after `Delete(rawToken)`, `Get` returns `ErrNotFound` (logout invalidates immediately).
- **Boundary** — `TestSessionRepo_Get_Expired`: a session with `ExpiresAt = now-1s` returns `ErrNotFound` (server-side expiry filter); `ExpiresAt = now+1s` returns it. `TestSessionRepo_DeleteExpired_OnlyPastDue`: with three rows (expired, expiring-now, future), `DeleteExpired(now)` removes exactly the `<= now` rows and returns that count; the future row survives and is still gettable.
- **Isolation/security** — `TestSecrecy_SessionToken_NeverStoredRaw` (mirror `secrecy_test.go`): create a session with a recognizable sentinel token (e.g. `"raw-SENTINEL-TOKEN-DO-NOT-STORE"`); query the `sessions` table directly (`SELECT id_hash FROM sessions`) and assert no column contains the raw token, that the stored `id_hash` equals the hex sha256 of the token, and that a forged/tampered `id_hash` (one bit flipped) does not authenticate via `Get`. Also assert `Get` on a session created under one token returns `ErrNotFound` when queried with a *different* token whose hash differs.

## Acceptance Criteria

- `SessionRepo` satisfies `identity.SessionRepository` (compile-time assertion); all four methods implemented.
- Only `sha256(token)` is persisted; expired rows never authenticate (`Get` filters in SQL and maps to `ErrNotFound`).
- `DeleteExpired` removes only `expires_at <= now` and returns the count.
- sqlc `sessions.sql` generated, not hand-edited.
- `go build ./...` and `go test ./adapter/sqlite/...` pass.
