# Phase 6 Implementation Design — Phase 1: Identity Core

## Overview

This phase establishes the identity foundation everything else in Phase 6 builds on: a mode flag (local vs org), an `Identity` value carried in `context.Context`, authentication middleware applied to **every** request surface (REST, WebSocket, interactive endpoints, and `/mcp`), the self-hosted OIDC browser login flow, and server-side sessions. After this phase, org mode authenticates every surface and browser login works end-to-end; non-browser client credentials (tokens, device-code) arrive in Phase 2.

The guiding principle: **local mode is org mode with one pre-authenticated admin user.** There is a single authorization code path; local mode simply injects a fixed identity at the middleware boundary.

---

## 1. Auth Configuration

Add an `AuthConfig` to `domain/config/config.go`, loaded via koanf in `adapter/config/loader.go`:

- `Mode` — `"local"` (default) or `"org"`. Drives every downstream branch.
- `OIDC` sub-config: `IssuerURL`, `ClientID`, `ClientSecret`, `RedirectURL`, `Scopes` (default `openid,profile,email`).
- `Session` sub-config: `TTLSeconds` (default e.g. 12h), `CookieName` (default `arbitron_session`), `CookieSecure` (default true).
- `BootstrapAdmins` — list of identity subjects/emails (consumed in Phase 2 for provisioning; declared here so the config shape is complete).

Loader validation: when `Mode == "org"`, the OIDC issuer/clientID/clientSecret/redirectURL must be non-empty, mirroring the existing fail-fast validation precedent (e.g. LLM `TimeoutSeconds` flooring). Local mode requires no auth config.

---

## 2. `domain/identity` Package

A new domain package with no infrastructure dependencies:

```
type Role string          // "user", "admin"
type Mode string          // "local", "org"

type Identity struct {
    UserID   uuid.UUID
    Username string
    Role     Role
}

func (i Identity) IsAdmin() bool
```

Context helpers (request-scoped value, the one pragmatic-hex allowance):

```
func WithIdentity(ctx context.Context, id Identity) context.Context
func FromContext(ctx context.Context) (Identity, bool)
func MustFromContext(ctx context.Context) Identity   // panics if absent — a programming error, since middleware always sets it
```

Domain services depend only on `identity.FromContext`, never on HTTP/session types.

---

## 3. Ports

**`SessionRepository`** (new, `domain/identity/ports.go`):

```
Create(ctx, *Session) error
Get(ctx, id string) (*Session, error)      // returns ErrNotFound when absent/expired
Delete(ctx, id string) error               // logout
DeleteExpired(ctx, now time.Time) (int, error)
```

`Session{ID, UserID, CreatedAt, ExpiresAt}`. `ID` is a high-entropy random token (the cookie value); only its hash is stored (see §6).

**`UserRepository`** — to minimize ripple, **expand the existing port in `domain/instance/ports.go`** in place (the engine already imports it for `GetDefaultUser`); do not relocate it. Add:

```
GetDefaultUser(ctx) (uuid.UUID, error)     // existing, unchanged
GetByID(ctx, id uuid.UUID) (*User, error)
GetBySubject(ctx, subject string) (*User, error)  // ErrNotFound if new — JIT path in Phase 2
Create(ctx, *User) error                          // JIT path in Phase 2
```

Define the domain `User{ID, Subject, Username, Role}` type in `domain/instance` alongside the port (`Subject` is the IdP-stable identifier, nullable for the pre-existing default user). The `domain/identity` package holds only the request-scoped `Identity` value, `Role`/`Mode`, context helpers, and the `SessionRepository`/`OIDCProvider` ports — it imports nothing from `domain/instance`, and the auth middleware maps a persisted `User` to an `Identity`.

**`OIDCProvider`** (new port abstracting the IdP so the OIDC library stays in the adapter):

```
AuthCodeURL(state, nonce string) string
Exchange(ctx, code string) (Claims, error)   // Claims{Subject, Username, Email}
```

---

## 4. Authentication Middleware

A new `adapter/http/auth_middleware.go` replacing the `TODO(phase-6)` block in `server.go`. One middleware, mode-aware:

**Local mode:** resolve the default user once (cached) and inject `Identity{UserID: default, Role: admin}` into the request context. No cookie, no token, no rejection. Current behavior preserved exactly.

**Org mode:**
1. Read the session cookie. If present, look up the session via `SessionRepository`; if valid and unexpired, load the user and inject their `Identity`.
2. (Phase 2 adds the `Authorization: Bearer` token branch here.)
3. If no valid credential: for REST/MCP/WS return `401`; for browser navigation routes, redirect to `/auth/login`. Unauthenticated requests never reach a handler.

The middleware is mounted at the top of the `/api/v1` group **and** wraps the `/mcp` mount and the WebSocket upgrade handlers, closing all three documented auth gaps in one place.

---

## 5. OIDC Login Flow

New handlers under `/auth` (outside the authenticated group, since they establish the session):

- `GET /auth/login` — generate `state` + `nonce` (stored in a short-lived signed cookie), redirect to `OIDCProvider.AuthCodeURL`.
- `GET /auth/callback` — verify `state`, call `OIDCProvider.Exchange`, obtain `Claims`. Resolve the user (`GetBySubject`; JIT-create is Phase 2 — in Phase 1, an unknown subject is provisioned as a plain User so the flow is testable end-to-end). Create a session, set the session cookie, redirect to the original destination (or `/`).
- `POST /auth/logout` — delete the session, clear the cookie.

The OIDC adapter (`adapter/oidc/`) wraps `coreos/go-oidc/v3` + `golang.org/x/oauth2`, implementing `OIDCProvider`. ID-token signature and nonce are verified inside the adapter.

---

## 6. Sessions Storage

Migration `003_auth.up.sql` adds a `sessions` table: `id_hash` (PK), `user_id` (FK users), `created_at`, `expires_at`. sqlc queries in `adapter/sqlite/queries/sessions.sql`; adapter `adapter/sqlite/session_repo.go`.

Security: the cookie holds the raw session token; the DB stores only `sha256(token)`. Lookups hash the incoming cookie before querying, so a DB read leak cannot mint sessions. Cookie is `HttpOnly`, `SameSite=Lax`, `Secure` per config. A background sweep (reuse the existing timer/poll loop pattern) calls `DeleteExpired`.

---

## 7. Wiring & Local-Mode Default User

`cmd/arbitron/main.go` constructs the middleware with the configured `Mode`, the `SessionRepository`, `UserRepository`, and (org mode) the `OIDCProvider`. In local mode the existing default-user seed is unchanged; the middleware promotes that user to the admin identity. The `users` migration ensures the default user's `role` is `admin`.

---

## 8. Testing Strategy

Following the project's strict-TDD discipline, each unit covers the five scenario categories (happy path, input validation, error/failure, boundary, and isolation/security):

- **Middleware (local mode):** every request gets the default admin identity; no credential required.
- **Middleware (org mode):** valid session → identity injected; missing/expired/forged cookie → 401 (or redirect for nav routes); tampered `id_hash` never matches.
- **OIDC flow:** `state` mismatch rejected; nonce mismatch rejected; successful exchange creates a session and sets a hashed cookie; logout deletes the session and the next request is unauthenticated.
- **SessionRepository:** create/get/delete round-trip; expired session returns `ErrNotFound`; `DeleteExpired` removes only past-due rows; stored value is a hash, never the raw token (a secrecy test mirroring the existing `secrecy_test.go`).
- **Surface coverage:** table test asserting REST, `/mcp`, and the WS upgrade all reject anonymous requests in org mode and all pass through in local mode.
- **Config:** org mode with missing OIDC fields fails loading; local mode loads with no auth config.
