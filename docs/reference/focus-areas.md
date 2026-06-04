# Focus Areas Review Tracking

This document tracks the systematic review of **Phase 6 — Multi-User & Auth**
(`docs/specs/arbitron-phase6-spec.md`) for correctness, isolation, security, and
robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist (in `RALPH.md`) produces a passing
result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Auth config & mode (`domain/config` AuthConfig/OIDC/Session/BootstrapAdmins, `adapter/config/loader.go` — defaults, org-mode fail-fast validation, secret redaction) | [x] | [x] | Done |
| 2 | Identity package (`domain/identity` — `Identity`/`Role`/`Mode`, `WithIdentity`/`FromContext`/`MustFromContext`, no import of `domain/instance`) | [x] | [x] | Done |
| 3 | Identity ports (`domain/identity` — `SessionRepository`, `OIDCProvider`, `Claims`, `TokenRepository`) | [x] | [x] | Done |
| 4 | Users schema & repo (migration 003 sessions+`users.subject`/`users.email`; `instance.User`; `UserRepository` GetByID/GetBySubject/Create/UpdateUsername/List/UpdateRole/CountAdmins; sqlc) | [x] | [x] | Done |
| 5 | Session store (`adapter/sqlite` SessionRepo — SHA-256 hash-on-store, expiry filter, `DeleteExpired`, sweep; secrecy test) | [x] | [x] | Done |
| 6 | OIDC adapter (`adapter/oidc` — discovery, `AuthCodeURL`, code exchange, ID-token signature/issuer/audience verification, claims projection) | [x] | [x] | Done |
| 7 | Auth middleware (`adapter/http/auth_middleware.go` — local-mode admin injection; org-mode session→bearer resolve order; 401 vs login-redirect; covers REST/`/mcp`/WS) | [x] | [x] | Done |
| 8 | `/auth` handlers (`adapter/http` — login/callback/logout, signed state+nonce cookie, session cookie HttpOnly/SameSite/Secure, open-redirect-safe `next`) | [x] | [x] | Done |
| 9 | API tokens store & format (migration 004 `api_tokens`; `arb_<prefix>_<secret>`; SHA-256 of full token; `TokenRepository`; secrecy test) | [x] | [x] | Done |
| 10 | Token service & surface (`TokenService` Mint/List/Revoke owner-scoped; REST `/api/v1/tokens`; MCP `token_create`/`token_list`/`token_revoke`; plaintext once, hash never echoed) | [x] | [ ] | Pending |
| 11 | Bearer-token auth path (`AuthMiddleware.resolve` bearer branch — prefix pre-filter, hashed `GetByHash`, async `TouchLastUsed`, revoked→401; REST/`/mcp`/WS) | [ ] | [ ] | Pending |
| 12 | Device-code login (migration 005 `device_codes`; `/auth/device/code`/`token`/`approve`/`deny`; RFC-8628 errors; at-most-once consume→mint Arbitron token; expiry sweep) | [ ] | [ ] | Pending |
| 13 | JIT provisioning & bootstrap admin (`instance.Provisioner.Resolve` — GetBySubject-else-Create, default User, bootstrap promote email-ci-then-subject; wired in OIDC callback) | [ ] | [ ] | Pending |
| 14 | Role management (`instance.UserService.SetRole` admin-only; bootstrap-admin floor; last-admin guard; REST `/api/v1/users`+`/role`; MCP `user_list`/`user_set_role`) | [ ] | [ ] | Pending |
| 15 | Workflow scope & repo (`domain/workflow` `Scope`/`Private`/`Shared`; scope-aware `WorkflowRepository`; adapter path resolution `users/<id>` vs `shared/`; traversal+scope-escape guards; 0o700) | [ ] | [ ] | Pending |
| 16 | Scope resolution at call sites (`domain/workflow/resolve.go` `LoadVisible`; scope-aware `FileReader` owner threading via `engine.withOwner`/`OwnerFromContext`; HTTP/MCP/engine callers) | [ ] | [ ] | Pending |
| 17 | Workflow service authz (`domain/workflow/service.go` — create/edit/delete→private, edit-shared→`ErrSharedReadOnly`, list private+shared tagged, `LoadAs` admin-gated→`ErrUnauthorized`) | [ ] | [ ] | Pending |
| 18 | Publish/copy & publisher store (migration 006 `published_workflows`; `PublishedWorkflowRepo`; `Publish` publisher-only-overwrite+unique name; `Copy` independent; atomic `CopyTree`; REST+MCP) | [ ] | [ ] | Pending |
| 19 | Flat-store migration & run-shared-directly (marker-guarded startup move into `users/<default-user>/`; `Trigger(ctx,...)` resolves visible scope, owner=triggering user) | [ ] | [ ] | Pending |
| 20 | Instance list scoping (`instance.ListFilters.OwnerID`; `domain/engine/authz.go` `ownerConstraint`/`canView`; `Engine.ListInstances` service-set owner; `instances.sql` predicate; HTTP/MCP repointed) | [ ] | [ ] | Pending |
| 21 | Instance read scoping (scoped `GetInstance`/`GetTrace`/`GetNodeTrace`/`GetVariables`/`GetVariableHistory` → `ErrNotFound` for non-viewers; HTTP+MCP reads) | [ ] | [ ] | Pending |
| 22 | WebSocket scoping (per-instance connect authz before `Accept`; system stream `ScopedEvent` per-connection ownership filter, fail-closed; admins see all) | [ ] | [ ] | Pending |
| 23 | Interactive participation gate (`conversationHandler.loadAndValidate` owner-or-admin chokepoint → WS/REST/MCP `HandleMessage`/`EndSession`; `interactive_get_history` scoped) | [ ] | [ ] | Pending |
| 24 | Intervention authz & attribution (ctx-threaded Pause/Resume/Cancel/SetPriority/Retry/Rerun + `authorizeIntervention`; migration 007 `instance_interventions`; `recordIntervention` actor≠owner; `instance.intervened` event; REST `GET /interventions`+MCP `trace_interventions`) | [ ] | [ ] | Pending |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 25 | Auth middleware + session store (cookie→hashed lookup→identity; expired/forged cookie never matches; sweep removes only past-due) | [ ] | Pending |
| 26 | Auth middleware + token service (bearer→hashed `GetByHash`→identity; revoked token fails closed; one user cannot resolve another's token) | [ ] | Pending |
| 27 | OIDC handlers + provisioner (callback exchange→JIT create→bootstrap-admin promotion; state/nonce binding; email persisted for later floor checks) | [ ] | Pending |
| 28 | Device-code + token service (approval consumes the request exactly once and mints exactly one Arbitron token; never returns a raw IdP token) | [ ] | Pending |
| 29 | Role management + bootstrap floor (`SetRole` and `Provisioner.IsBootstrapAdmin` share one rule; bootstrap admin cannot be demoted; last admin cannot be demoted) | [ ] | Pending |
| 30 | Workflow scope/repo + workflow service authz (scope resolved from identity, not caller; edit-shared rejected; non-owner cannot reach another user's private path) | [ ] | Pending |
| 31 | Publish/copy + published store (publisher-only-overwrite and unique-name authority in `published_workflows`; re-publish leaves running instances' snapshots untouched) | [ ] | Pending |
| 32 | Trigger(ctx) + instance ownership + list/read scoping (run-shared-directly sets owner=triggering user; that owner is exactly what list/read scoping keys on) | [ ] | Pending |
| 33 | Instance list/read scoping + MCP tools (REST and MCP return the identical scoped set for the same identity — the MCP/REST parity test, extended) | [ ] | Pending |
| 34 | Intervention authz + attribution (gate allows owner-or-admin; an attribution row is written iff actor≠owner; the row is visible to owner+admins only) | [ ] | Pending |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage across the 5 categories (happy, success, failure, error, edge).
- The code does what the design doc says and satisfies the relevant spec success criterion (§8).
- Error handling is thorough — no silent failures; auth/authz failures fail **closed**.
- Module boundaries are clean (`domain/` imports no adapter; `domain/identity` imports no `domain/instance`).

**Pass 2 (Robustness & Security / Isolation):**
- Authorization lives in the domain service, so REST, WS, and MCP enforce the same rule — verify all three, not just REST.
- Non-owner non-admin access returns `ErrNotFound` (existence never leaked), and a caller cannot widen scope by supplying another owner.
- Secret hygiene: session/API tokens stored only as SHA-256 hashes, plaintext returned at most once, never echoed, never logged.
- Local mode stays frictionless (default admin, no credential); org mode is the only mode that enforces credentials.
- Edge cases: empty/expired/forged credentials, missing identity in context, traversal/cross-scope path escapes, re-publish during a running instance.

### Paired Area Reviews — What To Check

Paired reviews verify two components integrate correctly:
- Data flows cleanly across the boundary (identity → scope → owner filter; claims → user → role).
- Error propagation works end-to-end (auth failure → 401; authz failure → ErrNotFound/ErrForbidden; resolve error → instance failure).
- No assumption in one component the other doesn't satisfy (e.g. the hashing scheme, the bootstrap-admin rule, the snapshot-at-trigger invariant).
- Integration/parity tests cover the seam (especially REST↔MCP parity and the cross-surface isolation guarantee), not just unit happy paths.
