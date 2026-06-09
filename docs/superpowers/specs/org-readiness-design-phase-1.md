# Org-Readiness Design — Phase 1: Edge-Trusted SSO (Cloudflare Access)

**Spec:** `docs/specs/org-readiness-spec.md` (Phase 1)
**Phase order:** 1 of 3 (Auth → Agent/Provider → Triggers)

## Overview

Add a third authentication mode, `edge`, in which Arbitron identifies users from
a cryptographically-verified Cloudflare Access JWT presented in the
`Cf-Access-Jwt-Assertion` request header. Identity is the verified `email`
claim. Verification is stateless (per request); there is no Arbitron login flow
in edge mode. The existing `local` (dev/single-admin) and `org` (direct OIDC)
modes are unchanged.

**Key simplification:** Cloudflare Access is OIDC-discoverable (discovery doc at
the team domain, JWKS at `/cdn-cgi/access/certs`). The codebase already depends
on `github.com/coreos/go-oidc/v3` (used by `org` mode in `adapter/oidc`). Edge
verification reuses that verifier pattern — signature + issuer + audience +
expiry, with JWKS auto-fetched and cached. **No new dependency, no hand-rolled
JWT crypto.**

## Scope

In scope: the `edge` auth mode, JWT verification adapter, email-keyed user
provisioning, config + validation, middleware wiring, fail-closed behavior.

Out of scope (deferred per spec): mapping SSO group/department claims to RBAC
(no group claim consumed yet — every non-admin lands in the default group that
sees all); any Arbitron-hosted login UI for edge mode (handled at the edge).

## Architecture & components

Hexagonal: a new domain mode value + a new driven adapter; the HTTP middleware
(driving adapter) consumes a domain port.

### Domain

- `domain/identity/identity.go` — add `ModeEdge Mode = "edge"`; extend
  `ModeFromString` with a `case "edge"`.
- `domain/config/config.go` — add `AuthModeEdge = "edge"`; add `Edge EdgeConfig`
  to `AuthConfig`; new `EdgeConfig{ TeamDomain string; Audiences []string }`.
- **Identity verifier port.** Define a small port the middleware depends on so
  the adapter is swappable/testable:
  ```go
  // domain/identity (or domain/auth) — EdgeVerifier verifies an edge token and
  // returns the authenticated email.
  type EdgeVerifier interface {
      Verify(ctx context.Context, rawToken string) (email string, err error)
  }
  ```
- **User provisioning reuse.** `domain/instance/provisioner.go` already exposes
  `IsBootstrapAdmin(subject, email)` with **email-first** matching. Add an
  email-keyed entry point `ResolveByEmail(ctx, email) (*User, error)` that:
  create-on-first-sight by email, set admin iff email ∈ bootstrap list, else
  default group. Requires `UserRepository.GetByEmail(ctx, email)` on the port in
  `domain/instance/ports.go` (+ SQLite query).

### Adapter

- **`adapter/edge/verifier.go`** (new) — implements `EdgeVerifier`.
  - Construct from `EdgeConfig`: derive issuer/discovery from `TeamDomain`
    (`https://<teamdomain>`), build a `coreos/go-oidc` provider + verifier.
  - `Verify`: parse + verify the JWT (signature via cached JWKS, `iss` = team
    domain, `aud` ∈ configured audiences, `exp`/`nbf`), then read the `email`
    claim. Return `("", err)` on any failure (caller maps to 401).
  - JWKS caching/refresh is handled by the go-oidc remote key set (same as org).
- **`adapter/http/auth_middleware.go`** — add a stateless `edge` branch in
  `resolve`: read `Cf-Access-Jwt-Assertion`; empty → `errNoCredential` (401);
  `verifier.Verify` → email; `provisioner.ResolveByEmail` → user; `enrich` →
  identity. No session/token repositories are consulted (they may be nil in edge
  mode). `enrich` is already mode-agnostic.
- **`adapter/config/loader.go`** — defaults for `auth.edge.teamdomain` and
  `auth.edge.audiences`; `validateAuth` gains an `AuthModeEdge` case requiring
  both; commented YAML example; env overlay already generic
  (`ARBITRON_AUTH_EDGE_TEAMDOMAIN`, `ARBITRON_AUTH_EDGE_AUDIENCES`).
- **`cmd/arbitron/main.go`** — in edge mode, construct the verifier and the
  middleware with nil session/token repos; everything else (user repo,
  provisioner, groups) wires as in org mode.

## Data flow (request)

1. Request arrives at the HTTP server with `Cf-Access-Jwt-Assertion` set by
   Cloudflare.
2. `AuthMiddleware.resolve` (edge branch) → `EdgeVerifier.Verify` → `email`.
3. `Provisioner.ResolveByEmail(email)` → user (created if new; admin if
   bootstrap-listed; else default group).
4. `enrich` builds the request-scoped `identity.Identity`; request proceeds with
   the same authz boundary all surfaces already use.

## Configuration

| Setting | Env | Required (edge) | Meaning |
|---|---|---|---|
| `auth.mode` | `ARBITRON_AUTH_MODE` | yes (`edge`) | selects edge mode |
| `auth.edge.teamdomain` | `ARBITRON_AUTH_EDGE_TEAMDOMAIN` | yes | Cloudflare team domain (JWKS + issuer) |
| `auth.edge.audiences` | `ARBITRON_AUTH_EDGE_AUDIENCES` | yes | application AUD tag(s) to accept |
| `auth.bootstrapadmins` | `ARBITRON_AUTH_BOOTSTRAPADMINS` | no | admin email allow-list (reused) |

## Error handling / security

- **Fail closed:** missing, malformed, expired, wrong-issuer, wrong-audience, or
  signature-invalid token → 401. A user-repo/DB failure during provisioning →
  500 (not 401), so infra faults are distinguishable from auth denials.
- **Independent verification, not header trust:** a direct/SSRF request that
  forges the header without a valid Cloudflare signature is rejected.
- **No secret in config:** only the public team domain + AUD tag are configured;
  verification uses Cloudflare's published JWKS.
- **Stateless:** the token is verified on every request; no session is minted.

## Testing (strict TDD)

Drive each unit test-first across the project's scenario categories (happy path,
input validation, error/failure, boundary, integration/state):

- **Verifier** (`adapter/edge`): valid token → email; expired; not-yet-valid
  (`nbf`); wrong `aud`; wrong `iss`; bad signature; missing `email` claim;
  malformed JWT. Use a locally-generated RSA key + a stub JWKS endpoint (httptest)
  so signatures are real and deterministic.
- **Provisioner email path** (`domain/instance`): new email → user created in
  default group; bootstrap-listed email → admin; returning email → same user;
  email case-insensitivity; DB error surfaces.
- **Middleware edge branch** (`adapter/http`): valid header → authed request;
  absent header → 401; verify failure → 401; provision DB error → 500; session
  cookie ignored in edge mode.
- **Config/validation**: edge mode without teamdomain/audiences → error;
  env-overlay parsing of audiences list; mode string round-trip.

## Integration points (anchors)

- `domain/identity/identity.go` (Mode consts, `ModeFromString`)
- `domain/config/config.go` (`AuthConfig`, new `EdgeConfig`, `AuthModeEdge`)
- `adapter/config/loader.go` (defaults, `validateAuth`, YAML, env overlay)
- `adapter/http/auth_middleware.go` (`resolve` edge branch), `current_user.go`
- `adapter/oidc/provider.go` (verifier pattern to mirror)
- `domain/instance/provisioner.go` (`IsBootstrapAdmin`, new `ResolveByEmail`),
  `domain/instance/ports.go` (`UserRepository.GetByEmail`)
- `cmd/arbitron/main.go` (mode wiring)

## Acceptance (from spec)

Valid CF token → correct user/role; forged/absent token → 401; bootstrap email →
admin; any other valid email → default group sees all; new user auto-provisioned;
runs headless from env/secrets.
