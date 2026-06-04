# Phase 2, Task 4: `device_codes` Migration + Repo + Device-Code Endpoints (Mint-on-Approval)

## Goal

Implement the OAuth 2.0 device authorization grant so a browserless client logs in and receives an **Arbitron** personal API token (not a raw IdP token). Add migration `005_device_codes`, the `DeviceCodeRepository` port + SQLite adapter, the `DeviceCodeService`, and three endpoints: `POST /auth/device/code` (start), the verification-page approval action (reuses Phase 1 session auth), and `POST /auth/device/token` (poll → `authorization_pending` until approved → **mint a token** via Task 2's `TokenService` and mark the row `consumed`). Expired/denied are terminal errors; double-consume is rejected.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §2 (Storage; Flow).

## Dependencies

- **Phase 2, Task 1** — `identity` token hashing helpers (reused to hash the `device_code` polling secret) and the migration-numbering precedent (`005` follows `004`).
- **Phase 2, Task 2** — `identity.TokenService.Mint` is called on approval to mint the returned token; **Task 5's `Provisioner`** is what resolves the approving user's claims to a `User` *for OIDC*, but device approval here uses the **already-authenticated session user** from Phase 1 (no fresh OIDC exchange — the approver is logged in via the browser). The approval handler just records the session user's id on the device row.
- **Phase 1** — session auth on the verification page (`GET/POST /auth/device` runs inside the authenticated browser surface so `identity.FromContext` yields the approver); `instance.UserRepository.GetByID` to confirm the user exists when minting.

## Background (current state)

- Auth/login routes are mounted **outside** the authenticated `/api/v1` group (Phase 1 §5: `/auth/login`, `/auth/callback`, `/auth/logout` establish or clear the session). The two unauthenticated device endpoints (`/auth/device/code`, `/auth/device/token`) join them there; the verification/approval page lives behind session auth (a browser nav route the middleware protects).
- Migrations + sqlc + repo pattern identical to Task 1 (`migrate.go` lexical ordering; `queries/*.sql` with `-- name:` annotations; `generated.New(db)`; map `sql.ErrNoRows` → a domain `ErrNotFound`).
- `TokenService.Mint(ctx, label)` returns `(plaintext, *Token, error)` and is scoped to `identity.FromContext`. On device approval the token must be minted **for the approving user**, so the mint runs in a context carrying that user's identity (the service builds an `identity.WithIdentity` ctx from the resolved device-row `user_id`).

## Files to Create/Modify

### Create: `adapter/sqlite/migrations/005_device_codes.up.sql`

```sql
-- OAuth 2.0 device authorization grant (Phase 6, Phase 2 §2). The polling
-- secret (device_code) is stored ONLY as its sha256 (device_code_hash); the
-- short human user_code is stored in clear (it is low-entropy and shown to the
-- approver). user_id is NULL until an authenticated browser approves the row.
CREATE TABLE IF NOT EXISTS device_codes (
    device_code_hash TEXT PRIMARY KEY,
    user_code        TEXT NOT NULL UNIQUE,
    status           TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','approved','denied','consumed')),
    user_id          TEXT REFERENCES users(id) ON DELETE CASCADE,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    expires_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_device_codes_user_code ON device_codes (user_code);
```

`.down.sql`: `DROP TABLE IF EXISTS device_codes;`

### Create: `adapter/sqlite/queries/device_codes.sql`

```sql
-- name: CreateDeviceCode :exec
INSERT INTO device_codes (device_code_hash, user_code, status, expires_at)
VALUES (?, ?, 'pending', ?);

-- name: GetDeviceCodeByHash :one
SELECT device_code_hash, user_code, status, user_id, created_at, expires_at
FROM device_codes WHERE device_code_hash = ? LIMIT 1;

-- name: GetDeviceCodeByUserCode :one
SELECT device_code_hash, user_code, status, user_id, created_at, expires_at
FROM device_codes WHERE user_code = ? LIMIT 1;

-- name: ApproveDeviceCode :execrows
UPDATE device_codes SET status = 'approved', user_id = ?
WHERE user_code = ? AND status = 'pending';

-- name: DenyDeviceCode :execrows
UPDATE device_codes SET status = 'denied'
WHERE user_code = ? AND status = 'pending';

-- name: ConsumeDeviceCode :execrows
UPDATE device_codes SET status = 'consumed'
WHERE device_code_hash = ? AND status = 'approved';

-- name: DeleteExpiredDeviceCodes :execrows
DELETE FROM device_codes WHERE expires_at < ?;
```

The `status = 'approved'` guard on `ConsumeDeviceCode` makes consume atomic: a second poll after consumption affects 0 rows → the service treats it as already-consumed (no double-mint). `ApproveDeviceCode`'s `status='pending'` guard prevents re-approving a denied/expired row. Run `sqlc generate`.

### Create / Modify: `domain/identity/ports.go` (add port + type)

```go
type DeviceCodeStatus string
const (
    DeviceCodePending  DeviceCodeStatus = "pending"
    DeviceCodeApproved DeviceCodeStatus = "approved"
    DeviceCodeDenied   DeviceCodeStatus = "denied"
    DeviceCodeConsumed DeviceCodeStatus = "consumed"
)

type DeviceCode struct {
    HashedCode string            // sha256 of the polling secret; never the raw code
    UserCode   string            // short, human, shown to the approver
    Status     DeviceCodeStatus
    UserID     *uuid.UUID        // nil until approved
    CreatedAt  time.Time
    ExpiresAt  time.Time
}

type DeviceCodeRepository interface {
    Create(ctx context.Context, dc *DeviceCode) error
    GetByHash(ctx context.Context, hashedCode string) (*DeviceCode, error)     // ErrNotFound
    GetByUserCode(ctx context.Context, userCode string) (*DeviceCode, error)   // ErrNotFound
    Approve(ctx context.Context, userCode string, userID uuid.UUID) error      // ErrNotFound if not pending
    Deny(ctx context.Context, userCode string) error
    Consume(ctx context.Context, hashedCode string) error                      // ErrNotFound if not 'approved'
    DeleteExpired(ctx context.Context, now time.Time) (int, error)
}
```

### Create: `adapter/sqlite/device_code_repo.go`

`var _ identity.DeviceCodeRepository = (*DeviceCodeRepo)(nil)`. Map `:execrows == 0` to `identity.ErrNotFound` for `Approve`/`Consume` so the service can distinguish "nothing to approve/consume" from success. Parse nullable `user_id` TEXT → `*uuid.UUID`. SECURITY: never log the raw device code (the repo never receives it — only the hash) and never log the hash.

### Create: `domain/identity/device_code_service.go`

```go
type DeviceCodeService struct {
    repo     DeviceCodeRepository
    tokens   *TokenService
    ttl      time.Duration   // e.g. 10m, from AuthConfig (Phase 1) or a const default
    interval int             // poll seconds, e.g. 5
    now      func() time.Time
    newCodes func() (deviceCode, userCode string, err error) // crypto/rand seam
}

// StartResponse mirrors the RFC 8628 device-authorization response.
type StartResponse struct {
    DeviceCode      string // the polling secret (plaintext, returned once)
    UserCode        string // shown to the human
    VerificationURI string
    Interval        int
    ExpiresIn       int
}

func (s *DeviceCodeService) Start(ctx context.Context, verificationURI string) (*StartResponse, error)
// generates deviceCode + userCode, stores Create{HashedCode: HashToken(deviceCode), UserCode, ExpiresAt: now+ttl}

func (s *DeviceCodeService) Approve(ctx context.Context, userCode string) error
// reads identity.FromContext (the logged-in approver), validates the row is pending+unexpired,
// calls repo.Approve(userCode, approver.UserID). Expired -> ErrExpired. ErrUnauthenticated if no identity.

func (s *DeviceCodeService) Deny(ctx context.Context, userCode string) error

// Poll is the token endpoint's core. Returns the minted plaintext on success.
func (s *DeviceCodeService) Poll(ctx context.Context, deviceCode string) (plaintext string, t *Token, err error) {
    dc, err := s.repo.GetByHash(ctx, HashToken(deviceCode))
    // ErrNotFound -> ErrInvalidGrant
    // now > dc.ExpiresAt -> ErrExpired (terminal)
    switch dc.Status {
    case DeviceCodePending:  return "", nil, ErrAuthorizationPending
    case DeviceCodeDenied:   return "", nil, ErrAccessDenied
    case DeviceCodeConsumed: return "", nil, ErrInvalidGrant // already used; no second mint
    case DeviceCodeApproved:
        // atomically consume; if Consume returns ErrNotFound someone raced us -> ErrInvalidGrant
        if err := s.repo.Consume(ctx, dc.HashedCode); err != nil { return "", nil, ErrInvalidGrant }
        // mint FOR the approver: build a ctx carrying that user's identity.
        mintCtx := WithIdentity(ctx, Identity{UserID: *dc.UserID})
        return s.tokens.Mint(mintCtx, "device-login")
    }
}
```

New sentinels: `ErrAuthorizationPending`, `ErrAccessDenied`, `ErrExpired`, `ErrInvalidGrant`. These map to the RFC 8628 token-endpoint error codes at the HTTP layer.

SECURITY: consume-before-mint ordering (atomic `status='approved'` guard) guarantees at-most-once minting even under concurrent polls — only the poll that wins the `Consume` proceeds to `Mint`. `Mint` runs with the approver's identity so the token is owned by the right user.

### Create: `adapter/http/device_handler.go`

- `POST /auth/device/code` (unauthenticated): call `Start(ctx, cfg.Server.PublicBaseURL + "/auth/device")`. Return RFC-8628 JSON `{device_code, user_code, verification_uri, interval, expires_in}`. Rate-limit-friendly but rate limiting is out of scope here.
- `GET /auth/device` (session-authenticated browser page): render/echo a form to enter the `user_code` (or accept `?user_code=`). The dashboard SPA may own the UI; the handler exposes `POST /auth/device/approve {user_code}` and `POST /auth/device/deny {user_code}` that call `Approve`/`Deny` with the session identity in ctx. Map `ErrExpired`/`ErrNotFound` → 400/404.
- `POST /auth/device/token` (unauthenticated): body `{device_code}`. Call `Poll`. Map:
  - success → 200 `{access_token: <plaintext>, token_type: "bearer"}`
  - `ErrAuthorizationPending` → 400 `{"error":"authorization_pending"}`
  - `ErrAccessDenied` → 400 `{"error":"access_denied"}`
  - `ErrExpired` → 400 `{"error":"expired_token"}`
  - `ErrInvalidGrant` → 400 `{"error":"invalid_grant"}`

### Modify: `adapter/http/server.go`

Mount the unauthenticated device endpoints alongside the Phase 1 `/auth/*` group (outside `/api/v1`), and the approve/deny actions inside the authenticated nav surface. Add `DeviceCodes *identity.DeviceCodeService` to `RouterDeps` (guard mounting on non-nil).

### Modify: `cmd/arbitron/main.go`

Build `deviceRepo := adaptsqlite.NewDeviceCodeRepo(db)` and `deviceSvc := identity.NewDeviceCodeService(deviceRepo, tokenSvc, ...)`; store on `domainStack`; pass `DeviceCodes: ds.deviceSvc` to `NewRouterFromDeps`. Add a background sweep calling `DeleteExpired` reusing the existing scheduler/timer poll pattern (mirror the session sweep Phase 1 adds).

## Testing (strict TDD — five scenario categories)

Tests first: `adapter/sqlite/device_code_repo_test.go`, `domain/identity/device_code_service_test.go`, `adapter/http/device_handler_test.go`. Service test uses a fake `DeviceCodeRepository` + a real `TokenService` over a fake token repo, and an injectable clock.

**`device_code_repo_test.go`:**
- **Happy path:** `Create` then `GetByHash`/`GetByUserCode` round-trips; `Approve(userCode, U)` sets status+user_id; `Consume(hash)` flips approved→consumed.
- **Input validation:** `GetByHash("")`/`GetByUserCode("nope")` → `ErrNotFound`.
- **Error/failure:** `Consume` of a `pending` row → `ErrNotFound` (the `status='approved'` guard hit 0 rows); `Approve` of an already-`denied` row → `ErrNotFound`.
- **Boundary:** `DeleteExpired(now)` removes only rows with `expires_at < now` and returns the count; equal timestamps are not deleted.
- **Isolation/security:** the stored `device_code_hash` equals `HashToken(rawDeviceCode)` and the raw code appears in no column (direct `SELECT *`); `user_code` is clear but the polling secret is not.

**`device_code_service_test.go`:**
- **Happy path:** `Start` returns a plaintext device_code, a user_code, and `Interval`/`ExpiresIn`; after seeding `Approve` (ctx = approver U), `Poll(deviceCode)` mints a token owned by U and the row becomes consumed; the minted plaintext is a valid `arb_` token.
- **Input validation:** `Poll("")`/`Poll("garbage")` → `ErrInvalidGrant`; `Approve` with no identity in ctx → `ErrUnauthenticated`.
- **Error/failure:** `Poll` on a pending row → `ErrAuthorizationPending`; on a denied row → `ErrAccessDenied`; on an expired row → `ErrExpired` even if approved.
- **Boundary:** **double-consume** — two `Poll`s on the same approved row: the first mints, the second (Consume races to 0 rows / status now consumed) → `ErrInvalidGrant`, and `TokenService.Mint` was called **exactly once** (assert mint count == 1).
- **Isolation/security:** the token minted on approval is owned by the **approver's** user id (assert the token repo's `Create` carried `UserID == U`), never by some attacker polling concurrently; the plaintext device_code never appears in logs; an unexpired-but-`pending` poll never mints.

**`device_handler_test.go` (httptest):**
- **Happy path:** `POST /auth/device/code` → 200 with the RFC fields; approve via a session-authed request; `POST /auth/device/token` → 200 `{access_token: arb_..}`.
- **Input validation:** `POST /auth/device/token` with no body / bad JSON → 400; approve with missing user_code → 400.
- **Error/failure:** pending poll → 400 `authorization_pending`; denied → `access_denied`; expired → `expired_token`.
- **Boundary:** consumed/second poll → `invalid_grant`.
- **Isolation/security:** `/auth/device/approve` requires a session (unauthenticated → 401/redirect via the middleware); the device-token response body and server logs never contain the raw device_code, and the minted token belongs to the approver.

Run `go build ./...` and `go test ./domain/identity/... ./adapter/sqlite/... ./adapter/http/...` green before handoff.
