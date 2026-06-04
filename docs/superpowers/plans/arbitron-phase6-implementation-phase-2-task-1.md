# Phase 2, Task 1: `api_tokens` Migration + `TokenRepository` (SQLite) + Hashing

## Goal

Add the persistence foundation for personal API tokens: migration `004_api_tokens`, the `api_tokens` sqlc queries, the `TokenRepository` port in `domain/identity`, the SQLite `TokenRepo` adapter, and the token format/hashing primitives (`arb_<prefix>_<secret>`, stored only as `sha256(full)`). No service, no HTTP, no MCP yet — this task delivers the storage + crypto seam that Tasks 2–4 build on.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §1 (Storage; Token format & hashing; Port).

## Dependencies

- **Phase 1, Task — `domain/identity` package** must exist (the `Identity` value, `Role`, `WithIdentity`/`FromContext`/`MustFromContext`, and `domain/identity/ports.go` holding `SessionRepository`). This task **adds** `TokenRepository` and the `Token` type to that package. If Phase 1 has not yet created `domain/identity/ports.go`, create it here as part of this task (placing only the `TokenRepository` port; `SessionRepository`/`OIDCProvider` remain Phase 1's).
- **Phase 1 migration `003_auth`** (sessions) is numbered before this one; `004` follows it. `migrate.go` applies `migrations/*.up.sql` in lexical order (`sort.Strings`) and records each numbered version in `schema_migrations`, so a new `004_*.up.sql` is picked up with no code change.
- `domain/instance` `User` type (`User{ID, Subject, Username, Role}`) from Phase 1 — referenced by Task 3 (bearer→user load) but not needed here.

## Background (current state)

- Migrations live in `adapter/sqlite/migrations/` as numbered `NNN_name.up.sql` / `.down.sql`. Today only `001_initial_schema` and `002_connections` exist. `adapter/sqlite/migrate.go` embeds `migrations/*.sql`, parses the leading integer via `fmt.Sscanf(name, "%d_", ...)`, and runs each unapplied version inside a transaction.
- `001_initial_schema.up.sql` defines `users(id TEXT PK, username TEXT UNIQUE, role TEXT CHECK(role IN ('user','admin')), created_at TEXT)` and seeds the default `local` admin (`00000000-0000-0000-0000-000000000001`). FK columns reference `users(id)` as `TEXT`.
- sqlc queries live in `adapter/sqlite/queries/*.sql` with `-- name: X :one|:many|:exec` annotations; generated code lands in `adapter/sqlite/generated` (`generated.New(db)` + typed `XParams`). `connections.sql` is the closest model (CRUD + Count). `sqlc generate` is run after editing queries (see `sqlc.yaml` at repo root).
- The secrecy precedent is `adapter/sqlite/connection_repo.go`: the plaintext (API key) is consumed only by the seal step (`crypto.Seal`) and never written to a non-cipher column or logged; `adapter/sqlite/secrecy_test.go` asserts no plaintext appears in logs or serialized output. Tokens follow the same discipline but with a one-way `sha256` (no reveal path — tokens are never recoverable).
- `parseUUID` / `parseUUID`-style helpers exist in the sqlite package (see `user_repo.go`). UUIDs are stored as `TEXT`.

## Files to Create/Modify

### Create: `adapter/sqlite/migrations/004_api_tokens.up.sql`

```sql
-- Personal API tokens (Phase 6, Phase 2 §1). The DB stores ONLY the sha256 of
-- the full token (token_hash) plus a clear, non-secret prefix for display. The
-- plaintext arb_<prefix>_<secret> is shown once at creation and is never
-- recoverable. Revocation = row delete.
CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label        TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    last_used_at TEXT
);

-- GetByHash is the per-request auth hot path: index the unique hash.
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens (user_id, created_at DESC);
```

### Create: `adapter/sqlite/migrations/004_api_tokens.down.sql`

```sql
DROP TABLE IF EXISTS api_tokens;
```

### Create: `adapter/sqlite/queries/api_tokens.sql`

```sql
-- name: CreateAPIToken :exec
INSERT INTO api_tokens (id, user_id, label, token_hash, prefix)
VALUES (?, ?, ?, ?, ?);

-- name: GetAPITokenByHash :one
SELECT id, user_id, label, token_hash, prefix, created_at, last_used_at
FROM api_tokens WHERE token_hash = ? LIMIT 1;

-- name: ListAPITokensByUser :many
SELECT id, user_id, label, token_hash, prefix, created_at, last_used_at
FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = ? AND user_id = ?;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?;
```

Note: `DeleteAPIToken` is scoped by **both** `id` and `user_id` so the persistence layer itself enforces owner-isolation (a defense-in-depth backstop under the service-level check in Task 2); `:execrows` returns the affected-row count so the repo can map 0 rows → `ErrNotFound`.

Run `sqlc generate` after adding this file.

### Create / Modify: `domain/identity/ports.go`

Add the `Token` domain type and the `TokenRepository` port. Keep this package infrastructure-free (no sqlite/http imports). Reuse the package's existing `ErrNotFound` sentinel (Phase 1 defines one for sessions); if absent, declare `var ErrNotFound = errors.New("identity: not found")` here.

```go
// Token is the stored, non-secret view of a personal API token. The plaintext
// arb_<prefix>_<secret> is NEVER held here; only the prefix (for display) and
// metadata are. TokenHash is the sha256(full token) used for lookup and is
// never echoed on any read surface.
type Token struct {
    ID         uuid.UUID
    UserID     uuid.UUID
    Label      string
    Prefix     string     // clear, non-secret, ~8 chars, for display
    TokenHash  string     // sha256 hex; internal only, never serialized to clients
    CreatedAt  time.Time
    LastUsedAt *time.Time // nil until first use
}

// TokenRepository persists personal API tokens. Implementations wrap domain
// sentinels (ErrNotFound) via %w so errors.Is mapping works in adapters.
type TokenRepository interface {
    Create(ctx context.Context, t *Token) error
    ListByUser(ctx context.Context, userID uuid.UUID) ([]Token, error)
    GetByHash(ctx context.Context, tokenHash string) (*Token, error) // ErrNotFound if absent
    Delete(ctx context.Context, id, userID uuid.UUID) error          // ErrNotFound if no owned row
    TouchLastUsed(ctx context.Context, id uuid.UUID) error
}
```

### Create: `domain/identity/token_format.go`

The format + hashing primitives live in the domain (pure, no infra) so the service (Task 2), the middleware (Task 3), and the device-code mint (Task 4) all share one definition.

```go
const (
    tokenPrefixBytes = 6  // ~8 base32/hex chars of clear prefix
    tokenSecretBytes = 32 // 256 bits of entropy in the secret
    TokenScheme      = "arb"
)

// GenerateToken returns a fresh plaintext token "arb_<prefix>_<secret>", its
// clear prefix, and the sha256-hex of the FULL plaintext. The plaintext is the
// only secret-bearing return; callers show it once and never persist it.
func GenerateToken() (plaintext, prefix, hash string, err error) {
    // crypto/rand both halves; encode url-safe base64 (no '_' so the two '_'
    // separators stay unambiguous) or lowercase hex. Use hex for simplicity.
}

// HashToken returns the sha256-hex of a full presented token. Used by the
// middleware to look the token up by hash. Constant-format; no secret retained.
func HashToken(plaintext string) string

// ParsePrefix extracts <prefix> from a well-formed arb_<prefix>_<secret>, or
// "" if the token is not in the expected shape (cheap pre-filter before hashing).
func ParsePrefix(plaintext string) (string, bool)
```

Implementation notes: use `crypto/rand` (never `math/rand`); `sha256.Sum256([]byte(plaintext))` + `hex.EncodeToString`. `HashToken` MUST hash the **entire** `arb_<prefix>_<secret>` string, not just the secret, so the stored hash matches what the middleware computes from the raw Authorization header.

### Create: `adapter/sqlite/token_repo.go`

```go
type TokenRepo struct {
    db      *sql.DB
    queries *generated.Queries
}

var _ identity.TokenRepository = (*TokenRepo)(nil)

func NewTokenRepo(db *sql.DB) *TokenRepo { /* generated.New(db) */ }
```

- `Create`: insert `id, user_id, label, token_hash, prefix` (UUIDs as `.String()`). Map a `UNIQUE constraint` error on `token_hash` via the existing `isUniqueViolation` helper to a wrapped error (astronomically unlikely collision; surface, don't swallow).
- `GetByHash`: `sql.ErrNoRows` → `fmt.Errorf("token: %w", identity.ErrNotFound)`. Parse the row into `identity.Token`, parsing UUIDs with the existing `parseUUID` helper and the nullable `last_used_at`/`created_at` TEXT timestamps via the same time-parse helper the other repos use.
- `ListByUser`: map rows to `[]identity.Token`.
- `Delete`: call `DeleteAPIToken` (id+user_id); if affected rows == 0 → `identity.ErrNotFound` (no such token OR not owned — both are "not yours").
- `TouchLastUsed`: `TouchAPIToken(id)`; best-effort semantics are the caller's concern (Task 3 calls it async and ignores the error), but the repo still returns any error.

SECURITY: this adapter never logs the plaintext (it never receives it) and never logs `token_hash`. The only secret material in the system is the plaintext, which lives solely on the `Mint` return path (Task 2).

### Modify: `cmd/arbitron/main.go`

In `initDomain`, after `userRepo := adaptsqlite.NewUserRepo(db)`, construct `tokenRepo := adaptsqlite.NewTokenRepo(db)` and store it on `domainStack` (add a `tokenRepo identity.TokenRepository` field). It is not yet wired into HTTP/MCP (Tasks 2–3 do that); this task just makes it available so the later tasks have a single construction site. Add the `domain/identity` import.

## Testing (strict TDD — five scenario categories)

Tests first. New tests: `domain/identity/token_format_test.go`, `adapter/sqlite/token_repo_test.go`, and an extension to `adapter/sqlite/secrecy_test.go`. Use the existing `setupTestDB(t)` helper.

**`token_format_test.go`:**
- **Happy path:** `GenerateToken()` returns `plaintext` matching `^arb_[0-9a-f]+_[0-9a-f]+$`, `ParsePrefix(plaintext)` equals the returned `prefix`, and `HashToken(plaintext)` equals the returned `hash`.
- **Input validation:** `ParsePrefix("garbage")`, `ParsePrefix("arb_only")`, `ParsePrefix("")` all return `("", false)`; `HashToken("")` is a stable non-empty hex string (defensive, not a panic).
- **Error/failure:** N/A at the format layer except that `GenerateToken` surfaces a non-nil error if `crypto/rand` fails (inject a failing reader via a package-private `randRead` seam, assert the error propagates and no token is returned).
- **Boundary:** secret/prefix lengths exactly match `tokenSecretBytes`/`tokenPrefixBytes` after encoding; two successive `GenerateToken()` calls produce different plaintexts and different hashes (uniqueness/entropy).
- **Isolation/security:** `HashToken` of the full token ≠ `HashToken` of just the secret portion (confirms the whole token is hashed); the hash is one-way (no inverse exposed); `plaintext != hash` and `plaintext` contains the `prefix` but `hash` does not.

**`token_repo_test.go`:**
- **Happy path:** `Create` then `GetByHash(hash)` round-trips `UserID`, `Label`, `Prefix`, `TokenHash`, nil `LastUsedAt`; `ListByUser` returns it.
- **Input validation:** `GetByHash("")` and `GetByHash("not-a-real-hash")` return `ErrNotFound` (errors.Is).
- **Error/failure:** `Delete` of a non-existent id → `ErrNotFound`; `TouchLastUsed` on an existing token sets `last_used_at` (re-`GetByHash` shows non-nil); duplicate `token_hash` insert surfaces an error (not a silent no-op).
- **Boundary:** `ListByUser` of a user with zero tokens returns an empty (non-nil) slice; ordering is `created_at DESC`.
- **Isolation/security:** seed two users A and B, each with a token; `ListByUser(A)` never includes B's token; `Delete(bTokenID, aUserID)` returns `ErrNotFound` and B's token still resolves via `GetByHash` (cross-user delete is rejected at the persistence layer).

**`secrecy_test.go` extension (new `TestSecrecy_APIToken_NeverStoresPlaintext`):**
- Mint a token through the format helper, `Create` it via the real `TokenRepo`, then read the raw `api_tokens` row with a direct `db.Query("SELECT * FROM api_tokens")`. Assert no column value equals the plaintext or the secret half; assert the stored `token_hash` equals `HashToken(plaintext)`; assert the clear `prefix` is present and is a substring of the plaintext. Capture a buffered slog logger across `Create`/`GetByHash`/`TouchLastUsed` and assert the plaintext never appears in any log line.

Constraint: domain tests stay in `domain/identity` and use no sqlite import; adapter tests stay in `adapter/sqlite` and use the real DB via `setupTestDB`. Run `go build ./...` and `go test ./domain/identity/... ./adapter/sqlite/...` green before handing off.
