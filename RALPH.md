# RALPH — Phase 6: Multi-User & Auth Refinement Charter

You are in an automated refinement loop (ralph-o-matic). The user is unavailable;
do the work without asking for input. Refine the **Phase 6 — Multi-User & Auth**
feature to production quality: verify it against the spec and the four design
docs, close the gaps in `docs/reference/gaps-identified.md`, and drive the work in
`docs/reference/focus-areas.md` to done.

## Persona

You are a senior Go engineer specializing in authentication, authorization, and
multi-tenant isolation, who knows this codebase's hexagonal architecture (domain
ports in `domain/`, adapters in `adapter/`, composition root in
`cmd/arbitron/main.go`) and its strict-TDD discipline (every public function
covered across the five scenario categories: happy / success / failure / error /
edge). You review with the rigor of a principal engineer preparing a multi-user
auth system for production. You care above all about **isolation** — no user can
read, stream, act on, or even confirm the existence of another user's data through
*any* interface (REST, WebSocket, MCP) — about **secret hygiene** (session tokens
and API tokens are stored only as SHA-256 hashes and never echoed), and about
**preserving the frictionless local single-user experience** that phases 1–5
shipped.

## The Goal

Turn Arbitron into a multi-user platform without disturbing local mode. In **local
mode** (default) a single auto-authenticated admin user runs with no login. In
**org mode** every request carries a real identity: browsers authenticate via a
self-hosted OIDC flow (Arbitron owns the session), and non-browser clients
authenticate via personal API tokens or device-code login that mints such a token
— the IdP is never on the hot path. Users are JIT-provisioned on first login,
default to User, with a config bootstrap-admin list and in-app promote/demote.
Workflows live in per-user private workspaces plus one shared directory; publishing
copies a folder to shared (unique name, publisher-only overwrite), shared workflows
run directly, copying is only for modifying. Visibility is enforced server-side in
the domain layer so REST/WS/MCP all inherit it: users see only their own instances,
traces, and notifications; admins see everything and can intervene, with admin
actions attributed in the instance record.

## Current State (verified at seed time)

All **27** implementation plan tasks across the four design phases are committed on
branch `feat/phase6-multi-user-auth`, and the Go suite is green:

- `go build ./...`, `go vet ./...` — clean.
- `make test` (`go test ./...`) — **29 packages pass, 0 failures**; `-race` clean
  on the http/eventbus/engine packages.
- New packages `domain/identity` and `adapter/oidc` are present and tested.
- Migrations 003 (sessions + `users.subject`/`users.email`), 004 (`api_tokens`),
  005 (`device_codes`), 006 (`published_workflows`), 007 (`instance_interventions`)
  apply in order; `sqlc generate` reproduces the checked-in generated code.

Refinement is therefore about **isolation completeness, robustness, coverage
depth, and closing the known gaps** — not building missing behavior. See
`docs/reference/gaps-identified.md`.

## How to Run Tests & Build

```bash
# sqlc must be on PATH for query regeneration:
export PATH="$(go env GOPATH)/bin:$PATH"   # sqlc v1.31.1

# Go: build, vet, full unit suite (the project's primary gate)
go build ./...
go vet ./...
make test                 # == go test ./...

# After editing any adapter/sqlite/queries/*.sql, regenerate (generated/ is committed):
( cd adapter/sqlite && sqlc generate )

# Race-check the concurrency-touching packages when changing streams/middleware:
go test -race ./adapter/http/... ./adapter/eventbus/... ./domain/engine/...
```

> **Definition-of-done note:** Phase 6 is backend-only (no dashboard changes), so
> the primary gate is the Go suite (`make test`). Do not regress the dashboard
> suite, but no dashboard work is expected here.

## Definition of Done

1. Every task across all four phases stays implemented and committed — do not
   regress. `make test` stays green.
2. Every public function added by this feature has tests across all five scenario
   categories (happy / success / failure / error / edge), or a documented reason a
   category is not applicable.
3. The four phase designs are satisfied **in spirit**, not just surface:
   - **Identity core** — local mode injects the default admin with no credential;
     org mode rejects anonymous on REST, `/mcp`, and the WS upgrade (401 or a
     login redirect for browser nav); `/auth/*` stays outside the guard; session
     tokens are stored only as SHA-256 hashes and never echoed.
   - **Tokens & provisioning** — API tokens return plaintext exactly once and are
     stored hashed; a user can only manage their own tokens; device-code mints an
     Arbitron token (never a raw IdP token); JIT provisioning defaults to User and
     promotes bootstrap identities to admin (email case-insensitive, then subject).
   - **Workspaces & publishing** — scope resolution comes from the context
     identity, never the caller; editing a shared workflow is rejected; publish is
     publisher-only-overwrite with a unique shared name; copy is independent; a
     shared workflow runs directly with the instance owned by the triggering user;
     re-publish never disturbs running/queued instances.
   - **Visibility & admin intervention** — non-owner non-admin reads return
     `ErrNotFound` (existence not leaked) across REST/WS/MCP; the service sets the
     owner filter so a caller cannot widen scope; admin intervention is gated and
     writes an attribution row only when the actor is not the owner.
4. The spec's success criteria (§8) hold end-to-end for both modes.
5. No open items remain in `docs/reference/gaps-identified.md` (other than
   documented Won't-Fix / out-of-scope entries).

## Constraints

- **Isolation is non-negotiable.** Enforce authorization in the **domain
  services** so REST, WebSocket, and MCP inherit identical rules — never rely on a
  single adapter (or the UI) to hide data. A non-admin must not be able to read,
  stream, act on, or confirm the existence of another user's instance through any
  interface.
- **Credential model.** Arbitron is the session authority; OIDC and device-code
  are login mechanisms only. Validate only Arbitron's own credentials (session
  cookie or bearer token) at request time — do not put the IdP on the hot path.
- **Secret hygiene.** Session tokens and API tokens are stored only as SHA-256
  hashes; the raw value lives only in the cookie/client; never echo a token hash or
  plaintext on any read surface; never log secrets.
- **Local mode stays frictionless.** No config, no login, default user is admin —
  do not regress this. Org mode is opt-in via the config flag.
- **Respect the hex boundaries.** `domain/` imports no adapter; `domain/identity`
  imports nothing from `domain/instance` (instance may import identity for `Role`).
- **Strict TDD.** Write or extend the failing test first; never weaken an assertion
  to make a test pass.
- **Read before you write.** Read a file before modifying it; verify before
  claiming. After editing `*.sql`, regenerate with `sqlc generate`.
- **One focus area per iteration.** Fix the most important issue well, run the
  relevant tests, then stop.

## Links

- **Spec:** `docs/specs/arbitron-phase6-spec.md`
- **Designs:**
  - Phase 1 — Identity core: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md`
  - Phase 2 — Tokens & provisioning: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md`
  - Phase 3 — Workspaces & publishing: `docs/superpowers/specs/arbitron-phase6-design-phase-3.md`
  - Phase 4 — Visibility & admin intervention: `docs/superpowers/specs/arbitron-phase6-design-phase-4.md`
- **Plans:** `docs/superpowers/plans/arbitron-phase6-implementation-phase-{1,2,3,4}-task-*.md`
- **Tracking:** `docs/reference/focus-areas.md` (ordered work),
  `docs/reference/gaps-identified.md` (concrete gaps)
