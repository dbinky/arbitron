# Phase 2, Task 5: JIT Provisioner + Bootstrap Admin (OIDC Callback & Device Approval)

## Goal

Add the `Provisioner` domain service that resolves verified IdP `Claims` to a persisted `User` — creating one just-in-time on first login, as an **admin** when the claim's email/subject is on `AuthConfig.BootstrapAdmins`, else as a plain **user**. Wire it into the Phase 1 OIDC callback (replacing the Phase 1 "provision every unknown subject as a plain user" placeholder) and into the device-code approval path so both login mechanisms share one provisioning rule. The bootstrap list is also recorded as the demotion floor consumed in Task 6.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §3 (JIT Provisioning) and §4 (Bootstrap Admin).

## Dependencies

- **Phase 1** — `instance.UserRepository.GetBySubject(ctx, subject) (*User, error)` (returns `ErrNotFound` for a new subject) and `Create(ctx, *User) error`; the `instance.User{ID, Subject, Username, Role}` type; the `OIDCProvider.Exchange(ctx, code) (Claims, error)` with `Claims{Subject, Username, Email}`; the OIDC callback handler (`adapter/http/auth_handler.go` or equivalent) that currently provisions an unknown subject as a plain user (the placeholder this task replaces); `AuthConfig.BootstrapAdmins []string` declared on `config.Config`.
- **Phase 2, Task 4** — the device-code approval path (`DeviceCodeService.Approve`). NOTE: device approval is performed by an **already-authenticated** browser user (Task 4), so provisioning has *already* happened for the approver at their own OIDC login. The Provisioner is therefore invoked in the **OIDC callback** (both the browser login and any future device-by-OIDC path); device approval reuses the approver's existing `User`. This task ensures the single provisioning rule lives in one service that the OIDC callback calls.

## Background (current state)

- `domain/instance/ports.go` currently has only `UserRepository.GetDefaultUser`; Phase 1 expands it with `GetByID`/`GetBySubject`/`Create` and defines `User`. The default `local` admin row is seeded in `001_initial_schema.up.sql` and must remain untouched — provisioning only ever **creates new** rows for real subjects.
- `AuthConfig` and `AuthConfig.BootstrapAdmins` are declared in Phase 1 (`domain/config/config.go` — added there; today `config.go` has no `Auth` field, so this depends on Phase 1 having added it). The loader (`adapter/config/loader.go`) populates it from koanf.
- The Provisioner is a pure domain service (no infra import): it takes the `UserRepository` port and the bootstrap list (a `[]string` or a small `BootstrapAdmins` value object) and returns a `*instance.User`. Per Phase 1 §3, `domain/identity` imports nothing from `domain/instance`; the Provisioner therefore lives in **`domain/instance`** (alongside `User`/`UserRepository`) — or in a small `domain/provisioning` package that imports `domain/instance`. Choose `domain/instance` to avoid a new package and because it operates directly on `User`.

## Files to Create/Modify

### Create: `domain/instance/provisioner.go`

```go
// Claims is the minimal verified-identity projection the Provisioner needs.
// It mirrors identity.Claims but is redeclared here to keep domain/instance
// free of an identity import (identity already imports nothing from instance;
// keep the dependency edge one-directional). The OIDC adapter maps its Claims
// to this shape at the call site.
type Claims struct {
    Subject  string
    Username string
    Email    string
}

// Provisioner resolves verified Claims to a persisted User, creating one on
// first sight. A subject/email on the bootstrap list is created as admin.
type Provisioner struct {
    users      UserRepository
    bootstrap  map[string]struct{} // lower-cased subjects AND emails
    now        func() time.Time
    newID      func() uuid.UUID
}

func NewProvisioner(users UserRepository, bootstrapAdmins []string) *Provisioner {
    // normalize bootstrapAdmins to a lower-cased set for O(1) membership.
}

// Resolve returns the existing user for claims.Subject, or creates a new one.
func (p *Provisioner) Resolve(ctx context.Context, c Claims) (*User, error) {
    if c.Subject == "" { return nil, ErrInvalidClaims }
    u, err := p.users.GetBySubject(ctx, c.Subject)
    switch {
    case err == nil:
        // Existing user: refresh username if the IdP changed it; role untouched.
        if c.Username != "" && c.Username != u.Username {
            u.Username = c.Username
            _ = p.users.UpdateUsername(ctx, u.ID, c.Username) // see note
        }
        return u, nil
    case errors.Is(err, ErrNotFound):
        role := RoleUser
        if p.isBootstrap(c) { role = RoleAdmin }
        nu := &User{ID: p.newID(), Subject: c.Subject, Username: pickUsername(c), Role: role}
        if err := p.users.Create(ctx, nu); err != nil { return nil, err }
        return nu, nil
    default:
        return nil, err
    }
}

// isBootstrap matches on lower-cased subject OR email.
func (p *Provisioner) isBootstrap(c Claims) bool

// IsBootstrapAdmin is exported for Task 6's demotion-floor guard so the floor
// and the provisioning rule consult the SAME normalized set.
func (p *Provisioner) IsBootstrapAdmin(subject, email string) bool
```

Notes:
- `RoleUser`/`RoleAdmin`: reuse the role constants already implied by the `users.role CHECK (role IN ('user','admin'))`. If `instance` has no role constants, add `const (RoleUser = "user"; RoleAdmin = "admin")` here.
- `pickUsername(c)`: prefer `c.Username`, fall back to `c.Email`, then `c.Subject` (never empty — the `users.username` column is `NOT NULL UNIQUE`).
- `UpdateUsername`: design §3 says "update username if changed". This needs a `UserRepository.UpdateUsername(ctx, id, username) error` method. **Add it to `domain/instance/ports.go` + the sqlite `UserRepo` + a `users.sql` query** as part of this task (small). If Phase 1 already added it, reuse. Keep the refresh best-effort-but-surfaced (return the error or log; do not silently corrupt). Username collisions on refresh (another user already owns that username) should be tolerated — keep the old username and continue (the IdP subject, not the username, is the identity).

### Modify: `adapter/sqlite/user_repo.go` + `adapter/sqlite/queries/users.sql`

Add the queries/methods Phase 1 may not have covered that this task needs: `GetBySubject`, `Create`, `UpdateUsername` (and `GetByID` if Phase 1 didn't add it). Mirror existing repo conventions (UUID as TEXT, `sql.ErrNoRows` → `instance.ErrNotFound` wrapped via `%w`, unique-violation mapping for `username`/`subject`). Example queries:

```sql
-- name: GetUserBySubject :one
SELECT id, subject, username, role, created_at FROM users WHERE subject = ? LIMIT 1;

-- name: CreateUser :exec
INSERT INTO users (id, subject, username, role) VALUES (?, ?, ?, ?);

-- name: UpdateUsername :exec
UPDATE users SET username = ? WHERE id = ?;
```

NOTE: `001_initial_schema.up.sql`'s `users` table has **no `subject` column** today. Phase 1's `003_auth` (or a Phase-1 users migration) is expected to add `subject TEXT UNIQUE` (nullable for the pre-existing default row). If Phase 1 did **not** add it, this task must add a migration `006_users_subject.up.sql` doing `ALTER TABLE users ADD COLUMN subject TEXT; CREATE UNIQUE INDEX idx_users_subject ON users(subject) WHERE subject IS NOT NULL;`. Verify Phase 1's migrations before writing code; do not duplicate the column.

### Modify: OIDC callback handler (`adapter/http/auth_handler.go`, Phase 1)

Replace the Phase 1 placeholder ("provision unknown subject as plain user") with a call to `Provisioner.Resolve(ctx, mapClaims(oidcClaims))`. The handler then creates the session for the resolved `User` exactly as Phase 1 does. The adapter maps `identity.Claims` → `instance.Claims` at this seam (one struct copy).

### Modify: `cmd/arbitron/main.go`

Build `provisioner := instance.NewProvisioner(userRepo, cfg.Auth.BootstrapAdmins)`; store on `domainStack`; pass it into the OIDC callback handler's construction (Phase 1 wiring) and make it available to Task 6's `UserService` (which needs `IsBootstrapAdmin` for the demotion floor — inject the same `provisioner` or share the normalized set).

## Testing (strict TDD — five scenario categories)

Tests first: `domain/instance/provisioner_test.go` (fake `UserRepository`), plus an extension to the OIDC-callback handler test (Phase 1) asserting provisioning is invoked.

**`provisioner_test.go`:**
- **Happy path:** unknown subject (`GetBySubject → ErrNotFound`) with claims not on the bootstrap list → `Create` called once with `Role == "user"`, `Subject`/`Username` from claims; returned user matches.
- **Input validation:** empty `Subject` → `ErrInvalidClaims`, no repo write; claims with empty `Username` but present `Email` → created user's username falls back to email (never empty).
- **Error/failure:** `GetBySubject` returning a non-`ErrNotFound` error propagates and **no** `Create` happens; `Create` returning an error propagates.
- **Boundary:** **known** subject (`GetBySubject` returns an existing user) → **no** `Create`, role unchanged even if the subject is *also* on the bootstrap list (provisioning only sets role at creation; promotion of an existing user is Task 6's job); username refresh fires only when the claim's username differs (assert `UpdateUsername` called once on change, zero times when unchanged).
- **Isolation/security:** a **bootstrap** subject (by subject) and a **bootstrap email** (subject not listed but email listed) each create an `admin`; matching is case-insensitive (`Foo@Bar.com` claim matches `foo@bar.com` in the list); a subject *not* on the list never becomes admin even if its email merely *contains* a listed substring (exact normalized match, not substring). `IsBootstrapAdmin` returns the same verdict the provisioning path used (shared set), so Task 6's floor is consistent.

**OIDC callback test extension:**
- A callback for a brand-new subject results in a created user + a session; for a bootstrap subject the created user has `admin` role; for a returning subject no duplicate user is created. (Reuse Phase 1's callback test harness with a fake `OIDCProvider.Exchange` returning controlled claims and the real `Provisioner` over a fake `UserRepository`.)

Run `go build ./...` and `go test ./domain/instance/... ./adapter/http/... ./adapter/sqlite/...` green before handoff.
