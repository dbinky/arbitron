# Phase 2, Task 2: `ConnectionStore` Port + sqlc Queries + Migration + Adapter

> **Historical note (added 2026-06-02):** This document predates Arbitron's migration from Postgres to SQLite. All Postgres references below are obsolete — the live persistence layer is the SQLite adapter (`adapter/sqlite/`, configured via `database.path`, default `./arbitron.db`). Retained as a historical record.

## Goal

Introduce the `ConnectionStore` port in `domain/provider` and implement it over the existing sqlc-backed store in `adapter/sqlite`. Add the `connections` table migration, the sqlc query file, and the repo adapter that encrypts each API key with the Task 1 `Crypto` port before it ever touches a column. `Get`/`List` return metadata only; `Reveal` is the single decryption entry point. The plaintext key is never stored in a column, never passed as a query parameter beyond the seal call, and never logged.

This task realizes the persistence half of security acceptance **#7** (ciphertext + per-row nonce at rest, key scoped to its owning connection).

**Dependency ordering:** Builds on **Task 1** (`provider.Crypto`, `adapter/crypto.AESGCM`). Builds on the **Phase 1 `domain/provider`** package. Task 3 (resolution) and Task 5 (wiring) consume this store. Sources of truth: design Phase 2 §1, §6, §7; sqlc/migration conventions in `adapter/sqlite/` (engine `sqlite`, `migrations/*.up.sql` + `*.down.sql`, `queries/*.sql`, generated into `adapter/sqlite/generated`).

> Persistence note: the live sqlc store is the `sqlite` engine under `adapter/sqlite/` (see `adapter/sqlite/sqlc.yaml`, `migrate.go`), and the Phase-2 design doc now specifies SQLite conventions (BLOB cipher/nonce, TEXT-JSON extras, TEXT timestamps, `NNN_name.up.sql/.down.sql` migrations, `-- name:` annotated queries with `?` placeholders). Implement the `ConnectionStore` there to match the live conventions exactly; the `ConnectionStore` port stays storage-agnostic so a future Postgres engine would be a drop-in.

## Files to Create/Modify

### Modify (or Create if Phase 1 not yet landed): `domain/provider/connection.go`

- Define the `Connection` value and the `ConnectionStore` driven port (design §1). Domain-only; no adapter imports.
  ```go
  // Connection binds an endpoint to its credential. model-id is deliberately
  // excluded so one connection fronts many models/profiles.
  type Connection struct {
      Name     string            // primary key
      Provider string            // "azure-openai" | "azure-foundry" | "openrouter" | "litellm" | ...
      BaseURL  string
      Extras   map[string]string // e.g. api-version (Azure), region
      // No APIKey field: the plaintext key is never carried on this struct.
  }

  type ConnectionStore interface {
      Create(ctx context.Context, c Connection, apiKey string) error
      Update(ctx context.Context, c Connection, apiKey *string) error // nil key = leave unchanged
      Delete(ctx context.Context, name string) error
      Get(ctx context.Context, name string) (Connection, error)       // metadata only; no plaintext key
      List(ctx context.Context) ([]Connection, error)                 // metadata only
      Reveal(ctx context.Context, name string) (apiKey string, err error) // sole decryption entry point
  }
  ```
- Sentinel errors:
  ```go
  var ErrConnectionNotFound = errors.New("connection not found")
  var ErrConnectionExists   = errors.New("connection already exists")
  ```

### Create: `adapter/sqlite/migrations/002_connections.up.sql`

- Follow the existing migration conventions (`migrate.go` parses the leading `NNN_` version; statements use `IF NOT EXISTS`; sqlite types). Mirror the design schema, adapted to the sqlite column types used in `001_initial_schema.up.sql` (TEXT timestamps, BLOB for binary):
  ```sql
  CREATE TABLE IF NOT EXISTS connections (
      name           TEXT PRIMARY KEY,
      provider       TEXT NOT NULL,
      base_url       TEXT NOT NULL,
      extras         TEXT NOT NULL DEFAULT '{}',   -- JSON object (jsonb intent)
      api_key_cipher BLOB NOT NULL,
      api_key_nonce  BLOB NOT NULL,
      created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
      updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
  );
  ```
  `extras` holds the `map[string]string` as a JSON object (the sqlite analogue of the design's `extras jsonb`). `api_key_cipher`/`api_key_nonce` hold the AES-GCM output from Task 1; **no plaintext column exists.**

### Create: `adapter/sqlite/migrations/002_connections.down.sql`

- `DROP TABLE IF EXISTS connections;`

### Create: `adapter/sqlite/queries/connections.sql`

- sqlc query file matching the existing annotation style (`-- name: X :one|:many|:exec`, `?` placeholders, `RETURNING *` on insert as in `instances.sql`). Plaintext key is never a parameter; only cipher/nonce are.
  ```sql
  -- name: CreateConnection :exec
  INSERT INTO connections (name, provider, base_url, extras, api_key_cipher, api_key_nonce)
  VALUES (?, ?, ?, ?, ?, ?);

  -- name: UpdateConnectionWithKey :exec
  UPDATE connections
  SET provider = ?, base_url = ?, extras = ?, api_key_cipher = ?, api_key_nonce = ?,
      updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE name = ?;

  -- name: UpdateConnectionMetadata :exec
  UPDATE connections
  SET provider = ?, base_url = ?, extras = ?,
      updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE name = ?;

  -- name: DeleteConnection :exec
  DELETE FROM connections WHERE name = ?;

  -- name: GetConnectionMeta :one
  SELECT name, provider, base_url, extras, created_at, updated_at
  FROM connections WHERE name = ?;

  -- name: ListConnections :many
  SELECT name, provider, base_url, extras, created_at, updated_at
  FROM connections ORDER BY name;

  -- name: GetConnectionCipher :one
  SELECT api_key_cipher, api_key_nonce FROM connections WHERE name = ?;

  -- name: CountConnections :one
  SELECT COUNT(*) FROM connections;
  ```
  Note: the metadata reads (`GetConnectionMeta`, `ListConnections`) intentionally **exclude** the cipher/nonce columns so revealing a key is a deliberate separate query (`GetConnectionCipher`). `CountConnections` feeds the Task 1 `RequireMasterKeyIfConnections` boot gate.
- After writing the migration + query, regenerate sqlc per the repo's Makefile target so `adapter/sqlite/generated/connections.sql.go` + the `Connection` row model exist. Do not hand-edit generated code.

### Create: `adapter/sqlite/connection_repo.go`

- `ConnectionRepo` implementing `provider.ConnectionStore`, following the `InstanceRepo`/`UserRepo` shape (struct with `db *sql.DB` + `queries *generated.Queries`, `NewConnectionRepo(db, crypto)` constructor). It also holds the `provider.Crypto` from Task 1.
  ```go
  type ConnectionRepo struct {
      db      *sql.DB
      queries *generated.Queries
      crypto  provider.Crypto
  }
  func NewConnectionRepo(db *sql.DB, crypto provider.Crypto) *ConnectionRepo
  ```
- Compile-time assertion: `var _ provider.ConnectionStore = (*ConnectionRepo)(nil)`.
- `Create`: marshal `Extras` to JSON (reuse the `convert.go` JSON helper pattern; `Extras` is `map[string]string`, so add a small string-map JSON helper or marshal inline), `crypto.Seal([]byte(apiKey))` → cipher/nonce, call `CreateConnection`. Map a primary-key/unique violation to `provider.ErrConnectionExists`. The `apiKey` argument is consumed only by `Seal`; it is never logged and never placed in any other parameter.
- `Update`: when `apiKey == nil`, call `UpdateConnectionMetadata` (key columns untouched — preserves the existing key); when non-nil, `Seal` the new key and call `UpdateConnectionWithKey`. Map missing rows to `provider.ErrConnectionNotFound` (check affected rows / pre-`Get`).
- `Delete`: call `DeleteConnection`; map a no-row delete to `ErrConnectionNotFound` if the contract requires it (assert via tests).
- `Get`: `GetConnectionMeta` → `Connection` (unmarshal `extras` JSON); `sql.ErrNoRows` → `ErrConnectionNotFound`. Returns **no** key material.
- `List`: `ListConnections` → `[]Connection` (metadata only).
- `Reveal`: `GetConnectionCipher` → `crypto.Open(cipher, nonce)` → `string(plaintext)`; `sql.ErrNoRows` → `ErrConnectionNotFound`; propagate `provider.ErrDecrypt` from `Open`. SECURITY: the returned plaintext is never logged here and never persisted; callers (Task 3 `RevealKey`) hand it straight to a request builder/subprocess env.
- `Count(ctx) (int, error)` thin wrapper over `CountConnections` for the Task 1 boot gate.

### Create (if absent): `adapter/sqlite/extras_convert.go` (or extend `convert.go`)

- Small `map[string]string` ↔ JSON-string helpers (`extrasToJSON`, `extrasFromJSON`) mirroring the existing `toJSONString`/`fromJSONString` style. Reuse rather than duplicate if a suitable helper already exists.

## Testing (strict TDD — all five scenario categories)

Add to `adapter/sqlite/integration_test.go` (reuse `setupTestDB` with `:memory:` + `RunMigrations`) plus a unit test using a **fake `provider.Crypto`** (a deterministic in-test stub) where the round-trip semantics matter without real AES. Cover design §7's `ConnectionStore` bullet:

- **Happy path** — `Create` then `Get` returns the stored metadata (name/provider/base_url/extras) and **no** key; `Reveal` returns the exact plaintext key that was passed to `Create`.
- **Success** — `Update` with a new (non-nil) key changes what `Reveal` returns; `List` returns all created connections ordered by name with metadata only; `Delete` removes the row so a subsequent `Get` errors.
- **Failure** — `Reveal`/`Get` on a missing connection returns `ErrConnectionNotFound`; `Create` with a duplicate `name` returns `ErrConnectionExists`.
- **Error** — `Reveal` when the stored cipher is corrupted (tamper a `BLOB` directly in the DB, or use a crypto stub that returns `ErrDecrypt`) propagates the decrypt error and returns no key; `Update` on a missing connection returns `ErrConnectionNotFound`.
- **Edge case** — `Update` with `apiKey == nil` preserves the previously stored key (`Reveal` still returns the original); empty `Extras` round-trips as `{}`; a connection name with unusual but valid characters round-trips; `Count` reflects the number of rows (0 on a fresh DB, N after N creates).

### Secrecy assertions (explicit)

- After `Create`, query the raw `connections` row directly and assert **no column** contains the plaintext key substring (cipher/nonce are opaque bytes; `extras`/`base_url`/`provider` do not contain it).
- Wrap `Create`/`Reveal` with a buffer-backed `*slog.Logger` (if the repo logs at all) and assert the captured output contains no substring of the plaintext key — logs reference the connection **name** only. (The end-to-end resolved-use redaction test is in Task 5.)

## Acceptance Criteria

- `provider.ConnectionStore` + `Connection` exist in `domain/provider`; domain imports no adapter; the `Connection` struct carries no plaintext key field.
- `002_connections.up/down.sql`, `queries/connections.sql`, and regenerated `generated/connections.sql.go` follow the existing sqlc/migration conventions; `RunMigrations` applies the new table idempotently.
- `ConnectionRepo` seals every key via `provider.Crypto` before persistence; `Get`/`List` return metadata only; `Reveal` is the only decryption path; the plaintext key never lands in a non-cipher column, a non-`Seal` parameter, or a log line.
- `update-with-nil-key` preserves the existing key; duplicate-name and missing-connection map to the sentinel errors.
- All five TDD scenario categories and the secrecy assertions pass; existing tests still pass.
