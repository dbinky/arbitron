# Org-Readiness Phase 1 — Task 4: Middleware edge branch + main.go wiring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, and superpowers:test-driven-development for every code change. Steps use checkbox (`- [ ]`) syntax.

**Objective:** Wire edge mode end-to-end: add a stateless `edge` branch to `AuthMiddleware.resolve` that reads the `Cf-Access-Jwt-Assertion` header, verifies it via `identity.EdgeVerifier`, provisions the user via `Provisioner.ResolveByEmail`, and enriches the request identity — fail-closed (401) on a missing/invalid token and 500 on a provisioning DB error — then construct the verifier and middleware (with nil session/token repos) in `cmd/arbitron/main.go` so the service boots and serves in edge mode from env/secrets.

**Spec anchor:** Phase 1 — "Fail closed"; "Dev keeps working without the edge"; "Operator configuration is minimal and headless".
**Design anchor:** §Adapter (`adapter/http/auth_middleware.go` edge branch), §Adapter (`cmd/arbitron/main.go`), §Error handling/security, §Data flow.

**Dependencies:** Task 1 (`identity.ModeEdge`, `config.EdgeConfig`, `identity.EdgeVerifier`), Task 2 (`adapter/edge.New`), Task 3 (`Provisioner.ResolveByEmail`).

**Tech stack:** Go; `net/http/httptest` for the middleware tests; an in-test fake `EdgeVerifier` and fake/real provisioner. Mirror `adapter/http/auth_middleware_test.go` conventions (check the existing org/local middleware tests for the harness).

---

## Files to create / modify

- Modify: `adapter/http/auth_middleware.go` — add an `edgeVerifier identity.EdgeVerifier` and `provisioner *instance.Provisioner` (or a small provisioning port) field; add the `edge` branch in `resolve`; extend `NewAuthMiddleware` (or add a focused constructor) to accept the verifier + provisioner. The header constant `cfAccessJWTHeader = "Cf-Access-Jwt-Assertion"`.
- Modify: `cmd/arbitron/main.go` — in the `authMode == identity.ModeEdge` branch, build `adaptedge.New(...)` from `cfg.Auth.Edge`, and construct the middleware with nil session/token repos. Add `adaptedge` import.
- Tests:
  - `adapter/http/auth_middleware_edge_test.go` — the edge-branch scenarios.

---

## Constructor / dependency note (decide during implementation)

`NewAuthMiddleware` today is `(mode, cookieName, sessions, tokens, users, groups)`. Two options — pick the lower-churn one that keeps existing call sites compiling:

- **Option A (preferred):** add two params `edgeVerifier identity.EdgeVerifier` and a provisioning dependency, threading them through `NewAuthMiddleware` (nil in local/org modes). Update the existing call site in `main.go` and the test harness.
- **Option B:** add a dedicated `NewEdgeAuthMiddleware(mode, headerName, verifier, provisioner, users, groups)` and keep `NewAuthMiddleware` unchanged. Less risk to org/local tests.

Whichever is chosen, the `edge` branch needs: the `EdgeVerifier`, something exposing `ResolveByEmail(ctx, email) (*instance.User, error)`, the `users` repo (for `enrich`'s `ListDepartments`), and `groups`. The `enrich` method is already mode-agnostic and is reused unchanged. Define a tiny port for provisioning if you want to avoid importing the concrete `*instance.Provisioner` into the middleware:
```go
type emailProvisioner interface {
    ResolveByEmail(ctx context.Context, email string) (*instance.User, error)
}
```

## The edge branch (place at the TOP of `resolve`, before the `ModeLocal` check or as its own `if m.mode == identity.ModeEdge`)

```go
if m.mode == identity.ModeEdge {
    raw := r.Header.Get(cfAccessJWTHeader)
    if raw == "" {
        return identity.Identity{}, errNoCredential // 401: no token at all
    }
    email, verr := m.edgeVerifier.Verify(r.Context(), raw)
    if verr != nil {
        // Invalid/forged/expired token: fail closed. NOT a server error.
        return identity.Identity{}, errNoCredential // 401
    }
    user, perr := m.provisioner.ResolveByEmail(r.Context(), email)
    if perr != nil {
        // Provisioning/DB failure is a SERVER fault, not an auth denial: surface
        // a non-errNoCredential error so Handler maps it to 500, distinguishing
        // "we could not check" from "you are not authenticated".
        return identity.Identity{}, fmt.Errorf("edge: provisioning %q: %w", email, perr)
    }
    return m.enrich(r.Context(), user), nil
}
```
No session cookie or bearer token is consulted in edge mode (session/token repos may be nil). `enrich` loads departments and stamps role/identity exactly as in org mode.

---

## TDD plan

### Step group A — middleware edge branch

- [ ] **A1. Build the harness.** In `adapter/http/auth_middleware_edge_test.go`, follow the existing middleware test pattern:
  - A fake `EdgeVerifier` whose `Verify` returns a configurable `(email, err)`.
  - A `*instance.Provisioner` over the in-test fake `UserRepository` (from Task 3), OR a fake `emailProvisioner` returning a configurable `(*User, err)`. Use the real provisioner + fake repo for the new/returning-user assertions; use a fake provisioner for the forced-DB-error case.
  - Build the middleware in edge mode with nil sessions/tokens, the fake verifier, the provisioner, the fake users repo, and a `groups` with `DefaultGroup: "default"`.
  - A trivial `next` handler that pulls `identity.FromContext` and writes the user's role + username so tests can assert who was authenticated; record whether `next` ran.
- [ ] **A2. Failing happy-path test.** `TestEdge_ValidHeader_Authenticates`:
  - Request with header `Cf-Access-Jwt-Assertion: <anything>`; fake verifier returns `("alice@corp.example", nil)`; repo has/creates that user.
  - Serve through `m.Handler(next)`; assert `next` ran, status 200, and the context identity has the expected `UserID`/`Role` (default-group user, or admin if bootstrap-listed).
  Run `go test ./adapter/http/ -run TestEdge -v` → FAIL (no edge branch / constructor).
- [ ] **A3. Implement** the constructor change + the edge branch (above) + the `cfAccessJWTHeader` constant. Run → PASS.

### Step group B — fail-closed + error mapping

- [ ] **B1. Absent header → 401.** No `Cf-Access-Jwt-Assertion` header → status 401, `next` did NOT run, body is the `UNAUTHORIZED` error shape. (Verifier must NOT be called — assert the fake verifier's call count is 0.)
- [ ] **B2. Verify failure → 401.** Header present, fake verifier returns `("", errors.New("bad sig"))` → 401, `next` not run, provisioner NOT called.
- [ ] **B3. Provision DB error → 500.** Verifier returns a valid email, fake provisioner returns `(nil, errors.New("db down"))` → status 500 with the `INTERNAL_ERROR` shape (the `errNoCredential` vs other-error split in `Handler` already does this) and `next` not run.
- [ ] **B4. Session cookie ignored in edge mode.** Request carries a `arbitron_session` cookie AND a valid edge header → authenticated via the edge path; the (nil) session repo is never consulted (no panic). Also: a request with ONLY a session cookie and no edge header → 401 (cookie alone is not a credential in edge mode).
- [ ] **B5. Bootstrap admin vs default group.** With the real provisioner + bootstrap list `["boss@corp.example"]`: a header verifying to `boss@corp.example` → identity role admin; a header verifying to `dev@corp.example` → role user with `Departments == ["default"]` (sees all). This is the spec's admin-vs-default acceptance at the request boundary.
- [ ] **B6. New user auto-provisioned.** First request for a never-seen email creates the user (repo now has it); a second request returns the same `UserID`. Confirms auto-provision on first valid sign-in and idempotency.
  Run all of B → PASS.

### Step group C — main.go wiring

- [ ] **C1. Wire the edge branch.** In `cmd/arbitron/main.go`, alongside the existing `if authMode == identity.ModeOrg { ... }` block, add:
  ```go
  var edgeVerifier identity.EdgeVerifier
  if authMode == identity.ModeEdge {
      ev, eerr := adaptedge.New(context.Background(), adaptedge.Config{
          TeamDomain: cfg.Auth.Edge.TeamDomain,
          Audiences:  cfg.Auth.Edge.Audiences,
      })
      if eerr != nil {
          db.Close()
          logCloser.Close()
          return nil, fmt.Errorf("initialising edge verifier: %w", eerr)
      }
      edgeVerifier = ev
  }
  ```
  Then construct the middleware. If using Option A, pass `edgeVerifier` and `provisioner` (already built at main.go:317) through `NewAuthMiddleware`; session/token repos still passed (they are nil-safe and unused in the edge branch). Update the `NewAuthMiddleware` call at main.go:340.
  - `authHandler` stays nil in edge mode (no `/auth/login` flow — sign-in/out at the edge). Verify the second `if authMode == identity.ModeOrg` block near main.go:698 (route mounting for auth handlers) is NOT entered for edge mode, so no login routes are mounted.
- [ ] **C2. Build + smoke.** `go build ./...`. Add/adjust any boot test (if `cmd/arbitron` has one) asserting that with `ARBITRON_AUTH_MODE=edge` + a stub team domain the server constructs (or that an unreachable team domain fails fast). At minimum, a unit test that `validateAuth` already gates missing edge config (Task 1) covers the headless-config acceptance; document that the live `adaptedge.New` discovery is exercised in Task 2.

### Step group D — verify

- [ ] **D1.** `go test ./adapter/http/ ./cmd/... -v` → PASS. `go build ./...` green; `go vet ./...` clean. Confirm local-mode and org-mode middleware tests still pass (no regression from the constructor change). Do NOT commit.

---

## Acceptance criteria (this task / Phase 1 end-to-end)

- With edge mode enabled, a request bearing a valid token (verified) is served as the corresponding user (B1/A2).
- A request with a missing or verify-rejected token gets 401 and never reaches handlers (B1/B2).
- A provisioning DB error returns 500, not 401 (B3) — infra faults are distinguishable from auth denials.
- A bootstrap-listed email is admin; any other valid email is a default-group member that sees all (B5).
- A new user is auto-provisioned on first valid sign-in (B6).
- The service boots and serves in edge mode with config supplied entirely via env/secrets, with no Arbitron login UI mounted (C1) — and local/dev mode is unaffected (D1).

## Notes — security & reuse

- **Fail closed:** every non-server failure in the edge path returns `errNoCredential` → 401. Only a genuine provisioning/DB fault returns a different error → 500. This reuses `Handler`'s existing `errors.Is(err, errNoCredential)` split unchanged.
- **Independent verification, not header trust:** the branch trusts the header ONLY after `EdgeVerifier.Verify` succeeds; a forged header without a valid Cloudflare signature is rejected at the verifier (proven in Task 2), so a direct/SSRF request that sets the header is denied.
- **Stateless:** no session is minted; the token is verified on every request (no session/token repo consulted — they may be nil).
- **Key/token never logged:** do not log the raw header value; the provisioning error wraps the email (already non-secret) and the underlying DB error, never the token.
- **Reuse:** `enrich` is mode-agnostic and used as-is; the verifier mirrors `adapter/oidc`; `ResolveByEmail` (Task 3) carries the canonical bootstrap rule. No new login routes — sign-in/out are the edge's job (spec "Arbitron does not present its own login page in edge mode").

## Test scenario coverage

| Category | Scenario | Step |
|---|---|---|
| Happy | valid header → authenticated request | A2 |
| Error/failure | verify failure → 401; provision DB error → 500 | B2,B3 |
| Input validation | absent header → 401 (verifier not called) | B1 |
| Boundary | bootstrap admin vs default-group user at request boundary | B5 |
| Integration/state | new user auto-provisioned; returning → same user; cookie ignored | B6,B4 |
| Wiring | main.go boots edge mode, fails fast on bad team domain, mounts no login routes | C1,C2 |
