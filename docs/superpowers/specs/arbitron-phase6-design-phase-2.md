# Phase 6 Implementation Design — Phase 2: Tokens & Provisioning

## Overview

This phase makes org mode usable by non-browser clients and completes the user lifecycle. It adds personal API tokens (mint/list/revoke), the OIDC device-code login that mints a token for browserless clients, just-in-time user provisioning on first login, the config-driven bootstrap admin, and in-app role management. After this phase, the MCP server / Claude Code, the CLI, and scripts can all authenticate as real users, and admins can manage who is an admin.

This phase depends on Phase 1's identity middleware, `OIDCProvider`, `UserRepository`, and session infrastructure.

---

## 1. Personal API Tokens

### Storage

Migration `004_api_tokens.up.sql` adds an `api_tokens` table: `id` (PK), `user_id` (FK), `label`, `token_hash`, `prefix`, `created_at`, `last_used_at` (nullable). sqlc queries in `queries/api_tokens.sql`; adapter `adapter/sqlite/token_repo.go`.

### Token format & hashing

A token is `arb_<prefix>_<secret>` where `prefix` (≈8 chars) is stored in clear for display/identification and `secret` is high-entropy. The DB stores only `sha256(full token)`. The plaintext token is returned **once** at creation and never recoverable — mirrors the session-token secrecy rule from Phase 1.

### Port & service

`TokenRepository` (`domain/identity/ports.go`): `Create`, `ListByUser`, `GetByHash`, `Delete`, `TouchLastUsed`.

`TokenService` (domain): `Mint(ctx, label) (plaintext, *Token, error)`, `List(ctx) ([]Token, error)`, `Revoke(ctx, tokenID) error`. All operate on the **context identity's** user — a user can only mint/list/revoke their own tokens. `List` and the API never echo `token_hash`.

### Middleware integration

Extend Phase 1's middleware with the `Authorization: Bearer arb_...` branch: hash the presented token, `GetByHash`, load the user, inject `Identity`, and `TouchLastUsed` (best-effort, async). Invalid/revoked token → 401. Bearer tokens are accepted on REST, `/mcp`, and WS; sessions remain browser-only.

### Surfaces

REST: `POST /api/v1/tokens`, `GET /api/v1/tokens`, `DELETE /api/v1/tokens/{id}`. Matching MCP tools (`token_create`, `token_list`, `token_revoke`) so tokens can be minted from Claude Code once an initial credential exists (e.g. just after a device-code login).

---

## 2. Device-Code Login

Implements the OAuth 2.0 device authorization grant so a browserless client can log in and receive an **Arbitron** token (not a raw IdP token — consistent with the Arbitron-native credential model).

### Storage

Migration `005_device_codes.up.sql` adds a `device_codes` table: `device_code_hash` (PK), `user_code`, `status` (`pending`/`approved`/`denied`/`consumed`), `user_id` (nullable until approved), `created_at`, `expires_at`.

### Flow

- `POST /auth/device/code` — client requests a code. Server stores a `device_codes` row and returns `{ user_code, verification_uri, device_code, interval, expires_in }`. `device_code` is the polling secret (stored hashed).
- The user opens `verification_uri` in any browser (an authenticated page from Phase 1's session flow), sees the `user_code`, and approves. Approval resolves the row to their `user_id` and sets `status=approved`.
- `POST /auth/device/token` — client polls with `device_code`. While `pending` → `authorization_pending`; on `approved` → the server **mints a personal API token** for that user, marks the row `consumed`, and returns the token. Expired/denied → terminal error.

The CLI/MCP client caches the returned `arb_` token and uses it as a normal bearer credential thereafter. Device-code thus reuses the token machinery from §1 rather than introducing a second request-time credential type.

---

## 3. Just-In-Time Provisioning

A `Provisioner` domain service called from the OIDC callback (Phase 1) and device-code approval:

`Resolve(ctx, claims Claims) (*User, error)`:
1. `GetBySubject(claims.Subject)` → if found, return (update username if changed).
2. If not found, create a new `User{Subject, Username, Role}` where `Role = admin` if `claims.Email`/`claims.Subject` is in `AuthConfig.BootstrapAdmins`, else `user`.

This replaces the Phase 1 placeholder that provisioned every unknown subject as a plain user. New-user creation is the only write; the default user remains untouched.

---

## 4. Bootstrap Admin

`AuthConfig.BootstrapAdmins` (declared in Phase 1) is consulted in two places:
- **Provisioning** (§3) — a bootstrap identity is created as admin on first login.
- **Demotion floor** (§5) — a user whose subject/email is on the bootstrap list cannot be demoted below admin, preventing accidental lockout. Attempting to demote one returns a domain error.

---

## 5. In-App Role Management

`UserService.SetRole(ctx, targetUserID, role)` — **admin-only** (checks `identity.FromContext().IsAdmin()`), enforced in the domain so REST and MCP share it. Guards: cannot demote a bootstrap admin (§4); cannot demote the last remaining admin (avoid a zero-admin system); self-demotion allowed only if another admin remains.

Add `UserRepository.UpdateRole` and `CountAdmins`. Surfaces: REST `GET /api/v1/users`, `PUT /api/v1/users/{id}/role` (admin-only); MCP tools `user_list`, `user_set_role`.

---

## 6. Testing Strategy

Each unit covers happy path, validation, error/failure, boundary, and isolation/security:

- **TokenService:** mint returns plaintext once and stores only a hash; list/revoke scoped to the caller (a user cannot revoke another's token — isolation); revoked token fails auth; `last_used_at` updates.
- **Token middleware:** valid bearer → identity; revoked/garbage/expired → 401; bearer accepted on REST/MCP/WS.
- **Device-code:** pending poll → `authorization_pending`; approval mints a token and consumes the row; expired/denied terminal; double-consume rejected.
- **Provisioner:** unknown subject creates a User; bootstrap subject creates an Admin; known subject returns existing without role change; username refresh on change.
- **Role management:** non-admin `SetRole` → forbidden; demoting a bootstrap admin → error; demoting the last admin → error; valid promote/demote persists.
- **Secrecy:** tokens table read never exposes plaintext; API responses never include `token_hash` (extend `secrecy_test.go`).
