# Org-Readiness Phase 1 — Task 3: Email-keyed provisioning (`GetByEmail` + `ResolveByEmail`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, and superpowers:test-driven-development for every code change. Steps use checkbox (`- [ ]`) syntax.

**Objective:** Add an email-keyed user lookup to the `UserRepository` port (with the SQLite query + impl) and an `instance.Provisioner.ResolveByEmail` entry point that creates-on-first-sight by verified email, promotes to admin iff the email is on the bootstrap list (reusing the existing `IsBootstrapAdmin` email-first rule), and otherwise lands the user in the default group.

**Spec anchor:** Phase 1 — "Identity is the verified email address"; "Administrators are designated by an email allow-list"; "Default access shows everything"; "A new user's account is created automatically on first valid sign-in".
**Design anchor:** §Domain (User provisioning reuse → `ResolveByEmail`, `UserRepository.GetByEmail`).

**Dependencies:** None required from Tasks 1–2 to compile, but logically part of Phase 1 (the middleware in Task 4 calls `ResolveByEmail`). Reuses the existing `Provisioner` (`IsBootstrapAdmin`, `groups`, `Resolve`'s department snapshot) verbatim.

**Tech stack:** Go; sqlc (regenerate `adapter/sqlite/generated`); tests use the in-memory SQLite + migration helper already used by `adapter/sqlite/user_repo_test.go`, and a fake `UserRepository` for the domain-level provisioner tests.

---

## Files to create / modify

- Modify: `domain/instance/ports.go` — add `GetByEmail(ctx, email string) (*User, error)` to `UserRepository`.
- Modify: `adapter/sqlite/queries/users.sql` — add `GetUserByEmail :one`.
- Regenerate: `adapter/sqlite/generated/*` via `sqlc generate` (run from `adapter/sqlite`, config `sqlc.yaml`).
- Modify: `adapter/sqlite/user_repo.go` — implement `GetByEmail` (mirror `GetBySubject`, including the empty-input short-circuit).
- Modify: `domain/instance/provisioner.go` — add `ResolveByEmail`.
- Tests:
  - `adapter/sqlite/user_repo_test.go` — `GetByEmail` found / not-found / empty / case behavior.
  - `domain/instance/provisioner_test.go` — `ResolveByEmail` scenarios (new file or append to existing provisioner tests; check which exists).
- Update any in-test fake `UserRepository` implementations (e.g. in `adapter/http` and `domain/instance` tests) to satisfy the widened port — grep `instance.UserRepository` mocks before finishing so the build stays green.

---

## Important reuse / behavior decisions

- **Match rule is the existing canonical one.** Admin status uses `Provisioner.IsBootstrapAdmin(subject="", email)` — its email-first branch already does case-insensitive, trimmed membership against the normalized bootstrap set. Do NOT add a second matching rule.
- **Email is the identity in edge mode.** Unlike `Resolve` (keyed on `subject`), `ResolveByEmail` keys on `email`: look up by email, create with `Email` set and `Subject = nil`. The token is already signature-verified by the edge verifier (Task 2), so the email is trusted (the design's "Identity is the verified email"); there is no separate `email_verified` gate on this path because edge verification IS the verification.
- **Department snapshot reuse.** After resolving the user, reuse the same default-group snapshot as `Resolve`: `depts := identity.ResolveDepartments(nil, p.groups)` (no group claim is consumed in Phase 1 — every non-admin lands in the single default group that sees all) and `p.users.ReplaceDepartments(...)`. This satisfies "Default access shows everything".
- **Case-insensitivity.** Normalize the email for lookup-equality the same way matching does. Store the email as presented but query case-insensitively (the simplest robust approach: store lower-cased and look up lower-cased, OR add `COLLATE NOCASE` to the query — pick one and assert it with the returning-user-different-case test). Document the choice in the query comment.

---

## TDD plan

### Step group A — `GetByEmail` repo (adapter/sqlite)

- [ ] **A1. Failing test.** In `adapter/sqlite/user_repo_test.go` add, mirroring `TestUserRepo_GetBySubject*`:
  - `TestUserRepo_GetByEmail_Found`: create a user with `Email = ptr("dana@corp.example")`; `GetByEmail("dana@corp.example")` returns that user.
  - `TestUserRepo_GetByEmail_CaseInsensitive`: `GetByEmail("DANA@CORP.EXAMPLE")` returns the same user.
  - `TestUserRepo_GetByEmail_NotFound`: unseen email → `errors.Is(err, instance.ErrNotFound)`.
  - `TestUserRepo_GetByEmail_EmptyNotMatched`: `GetByEmail("")` → `ErrNotFound` (never matches a NULL-email row, mirroring the `GetBySubject("")` security note).
  Run `go test ./adapter/sqlite/ -run GetByEmail -v` → FAIL (method missing).
- [ ] **A2. Add the query.** In `adapter/sqlite/queries/users.sql`:
  ```sql
  -- name: GetUserByEmail :one
  -- Case-insensitive lookup; an empty/NULL email must never match (see repo guard).
  SELECT id, subject, email, username, role, created_at FROM users WHERE email = ? COLLATE NOCASE LIMIT 1;
  ```
  Run `sqlc generate` (cwd `adapter/sqlite`) to regenerate `generated/`.
- [ ] **A3. Implement the repo method.** In `adapter/sqlite/user_repo.go`, mirror `GetBySubject`:
  ```go
  func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*instance.User, error) {
      if email == "" {
          return nil, fmt.Errorf("getting user by email: %w", instance.ErrNotFound)
      }
      row, err := r.queries.GetUserByEmail(ctx, sql.NullString{String: email, Valid: true})
      if errors.Is(err, sql.ErrNoRows) {
          return nil, fmt.Errorf("getting user by email: %w", instance.ErrNotFound)
      }
      if err != nil {
          return nil, fmt.Errorf("getting user by email: %w", err)
      }
      return toDomainUser(row.ID, row.Subject, row.Email, row.Username, row.Role)
  }
  ```
  Run the tests → PASS.

### Step group B — widen the port + keep build green

- [ ] **B1.** Add to `UserRepository` in `domain/instance/ports.go`:
  ```go
  // GetByEmail returns the user with the given email (case-insensitive),
  // ErrNotFound if absent. Empty email never matches. Edge-mode provisioning
  // keys on the verified email; subject is nil for edge-created users.
  GetByEmail(ctx context.Context, email string) (*User, error)
  ```
- [ ] **B2.** `go build ./...` → find every other `UserRepository` implementation / test fake and add `GetByEmail`. Grep: `grep -rn "instance.UserRepository\|GetBySubject" --include=*.go`. Add stub methods to test fakes (return `ErrNotFound` by default). Build green before proceeding.

### Step group C — `ResolveByEmail` (domain/instance)

- [ ] **C1. Failing tests.** In `domain/instance/provisioner_test.go` (use the existing fake `UserRepository` and `NewProvisioner`; `groups := identity.GroupConfig{DefaultGroup: "default"}`):
  - **new email → user created in default group (user role):** repo returns `ErrNotFound` then records the `Create`; `ResolveByEmail("eve@corp.example")` returns a user with `Role == RoleUser`, `Email` set, `Subject == nil`, and `ReplaceDepartments` was called with `["default"]`.
  - **bootstrap-listed email → admin:** `NewProvisioner(repo, []string{"boss@corp.example"}, groups)`; new email `boss@corp.example` → created with `Role == RoleAdmin`.
  - **bootstrap match is case-insensitive:** bootstrap `boss@corp.example`, token email `BOSS@CORP.EXAMPLE` → admin.
  - **returning email → same user, no duplicate Create:** repo `GetByEmail` returns an existing user → `ResolveByEmail` returns it; `Create` not called; role re-evaluated to match the live bootstrap list (promote/demote, mirroring `Resolve`).
  - **empty email → `ErrInvalidClaims` (or a dedicated sentinel):** `ResolveByEmail(ctx, "")` errors and does not create a user.
  - **DB error surfaces (not swallowed):** repo `GetByEmail` returns a non-NotFound error → `ResolveByEmail` returns that error wrapped (so Task 4's middleware maps it to 500, not 401). Also: `Create` error and `ReplaceDepartments` error each surface.
  Run `go test ./domain/instance/ -run ResolveByEmail -v` → FAIL.
- [ ] **C2. Implement.** Add to `domain/instance/provisioner.go`:
  ```go
  // ResolveByEmail resolves a VERIFIED edge-token email to a persisted User,
  // creating one on first sight. The email is the identity (subject is nil for
  // edge-created users); it is trusted because the edge verifier already checked
  // the token signature/issuer/audience/expiry. Admin status is re-evaluated on
  // every call from the live bootstrap list (config authoritative), and the
  // user's departments are snapshotted to the default group (Phase 1 consumes no
  // group claim). A repository error surfaces unwrapped-as-401 by being a
  // non-ErrInvalidClaims error, so the middleware can distinguish 500 from 401.
  func (p *Provisioner) ResolveByEmail(ctx context.Context, email string) (*User, error) {
      if strings.TrimSpace(email) == "" {
          return nil, ErrInvalidClaims
      }
      wantRole := identity.RoleUser
      if p.IsBootstrapAdmin("", email) {
          wantRole = identity.RoleAdmin
      }
      u, err := p.users.GetByEmail(ctx, email)
      switch {
      case err == nil:
          if u.Role != wantRole {
              if err := p.users.UpdateRole(ctx, u.ID, string(wantRole)); err != nil {
                  return nil, fmt.Errorf("re-evaluating role at edge login: %w", err)
              }
              u.Role = wantRole
          }
      case errors.Is(err, ErrNotFound):
          e := email
          nu := &User{
              ID:       p.newID(),
              Subject:  nil,
              Email:    &e,
              Username: email, // edge tokens carry no username; email is the display name
              Role:     wantRole,
          }
          if err := p.users.Create(ctx, nu); err != nil {
              return nil, err
          }
          u = nu
      default:
          return nil, err
      }
      depts := identity.ResolveDepartments(nil, p.groups)
      if err := p.users.ReplaceDepartments(ctx, u.ID, depts); err != nil {
          return nil, fmt.Errorf("snapshotting departments at edge login: %w", err)
      }
      return u, nil
  }
  ```
  Run the tests → PASS.

### Step group D — verify

- [ ] **D1.** `go test ./domain/instance/ ./adapter/sqlite/ -v` → PASS. `go build ./...` green. `go vet ./...` clean. Do NOT commit.

---

## Acceptance criteria (this task)

- `GetByEmail` finds a user case-insensitively, returns `ErrNotFound` for unseen/empty email, and never matches a NULL-email row.
- `ResolveByEmail` creates a new user on first sight (auto-provision on first valid sign-in), returns the same user on return visits, sets admin iff the email is on the bootstrap list (case-insensitive), otherwise default-group user — satisfying "admins by email allow-list" + "default access shows everything" + "account created automatically".
- A repository failure surfaces as a non-`ErrInvalidClaims` error so the middleware can map it to 500.

## Notes — security & reuse

- **Single canonical admin rule:** reuse `IsBootstrapAdmin` (do not reimplement matching). It already lower-cases/trims and checks the normalized set; the email-first branch is exactly what edge mode needs.
- **Config authoritative on every login:** like `Resolve`, re-evaluate role each call so removing an email from the bootstrap list demotes on next request (no stale admin).
- **Email-keyed create sets `Subject = nil`:** edge users have no IdP subject; keep the email as the only identifier and as the username fallback (the `username` NOT NULL column).
- **Mirror `GetBySubject`** for the repo guard (empty input never matches) and error wrapping (`%w` over `ErrNotFound`) so the HTTP adapter's `errors.Is` mapping keeps working.

## Test scenario coverage

| Category | Scenario | Step |
|---|---|---|
| Happy | new email → default-group user created | C1 |
| Happy | bootstrap email → admin | C1 |
| Input validation | empty email → ErrInvalidClaims, no create | C1 |
| Input validation | empty / NULL email never matches in repo | A1 |
| Boundary | case-insensitive lookup + bootstrap match | A1,C1 |
| Error/failure | GetByEmail / Create / ReplaceDepartments DB error surfaces | C1 |
| Integration/state | returning email → same user, role re-evaluated, dept snapshot | C1 |
