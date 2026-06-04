# Phase 2, Task 6: In-App Role Management (Service + Guards + REST + MCP)

## Goal

Add admin-only role management: `UserService.SetRole(ctx, targetUserID, role)` enforced in the **domain** (so REST and MCP share one rule) with three guards — caller must be admin, a bootstrap admin cannot be demoted, and the last remaining admin cannot be demoted (no zero-admin system; self-demotion allowed only if another admin remains). Add `UserRepository.UpdateRole` and `CountAdmins`. Expose `GET /api/v1/users` + `PUT /api/v1/users/{id}/role` (admin-only) and MCP tools `user_list` + `user_set_role`. This completes the user lifecycle: bootstrap admins (Task 5) can now promote/demote others in-app.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-2.md` §5 (In-App Role Management) + §4 (demotion floor).

## Dependencies

- **Phase 1** — `identity.FromContext` + `Identity.IsAdmin()` (the admin gate); the auth middleware injects the caller's identity on REST and MCP.
- **Phase 2, Task 5** — `instance.Provisioner.IsBootstrapAdmin(subject, email)` (the demotion-floor check) and the `instance.User{ID, Subject, Username, Role}` type with `GetByID`/`GetBySubject`. The floor must consult the **same** normalized bootstrap set the provisioner used, so inject the `Provisioner` (or its `IsBootstrapAdmin`) into `UserService`.
- **Phase 2, Task 2** — establishes the REST/MCP wiring patterns this task mirrors (`RouterDeps` field + `/api/v1` sub-route; `RegisterXTools` + `Deps` field).

## Background (current state)

- `instance.UserRepository` today exposes only `GetDefaultUser` (Phase 1 adds `GetByID`/`GetBySubject`/`Create`/`UpdateUsername`). This task adds `UpdateRole(ctx, id, role) error`, `CountAdmins(ctx) (int, error)`, and `List(ctx) ([]User, error)` for the admin user list.
- Roles are constrained by the DB: `users.role CHECK (role IN ('user','admin'))`. An invalid role string must be rejected **before** hitting the DB (clean 400) rather than relying on the constraint error.
- REST handler + MCP tool patterns: see Task 2 (`adapter/http/token_handler.go` mounting in `server.go`; `adapter/mcp/tools_*.go` + `register.go` + `Deps`). Admin gating in the domain means the surface handlers do **not** re-check admin — they call the service and map `identity.ErrForbidden` → 403 (REST) / tool error (MCP). Defense-in-depth: the visibility model (spec §5) says admin-only operations "exist as tools but are rejected for non-admin callers"; rejection lives in the service.

## Files to Create/Modify

### Add to `domain/instance/ports.go` (UserRepository)

```go
List(ctx context.Context) ([]User, error)
UpdateRole(ctx context.Context, id uuid.UUID, role string) error
CountAdmins(ctx context.Context) (int, error)
```

### Create: `domain/instance/user_service.go`

```go
// bootstrapChecker is the narrow seam UserService needs from the Provisioner so
// the demotion floor consults the same normalized bootstrap set.
type bootstrapChecker interface {
    IsBootstrapAdmin(subject, email string) bool
}

type UserService struct {
    users     UserRepository
    bootstrap bootstrapChecker
}

func NewUserService(users UserRepository, bootstrap bootstrapChecker) *UserService

// List returns all users. Admin-only (full visibility per spec §5).
func (s *UserService) List(ctx context.Context) ([]User, error) {
    id, ok := identityFrom(ctx)
    if !ok { return nil, ErrUnauthenticated }
    if !id.IsAdmin() { return nil, ErrForbidden }
    return s.users.List(ctx)
}

func (s *UserService) SetRole(ctx context.Context, targetID uuid.UUID, role string) error {
    caller, ok := identityFrom(ctx)
    if !ok { return ErrUnauthenticated }
    if !caller.IsAdmin() { return ErrForbidden }
    if role != RoleUser && role != RoleAdmin { return ErrInvalidRole }

    target, err := s.users.GetByID(ctx, targetID) // ErrNotFound -> propagate
    if err != nil { return err }
    if target.Role == role { return nil } // no-op, idempotent

    // Guards apply only to DEMOTIONS (admin -> user).
    if target.Role == RoleAdmin && role == RoleUser {
        // Floor 1: a bootstrap admin can never be demoted.
        if s.bootstrap.IsBootstrapAdmin(target.Subject, "" /* + email if available */) {
            return ErrBootstrapAdminProtected
        }
        // Floor 2: never demote the last remaining admin.
        n, err := s.users.CountAdmins(ctx)
        if err != nil { return err }
        if n <= 1 { return ErrLastAdmin }
    }
    return s.users.UpdateRole(ctx, targetID, role)
}
```

`identityFrom` is the seam to `identity.FromContext` — but `domain/instance` must not import `domain/identity`? Per Phase 1 §3, `domain/identity` imports nothing from `domain/instance` (one-directional). The reverse edge (`instance` → `identity`) is allowed: `identity` is the leaf domain holding only the `Identity` value/role/context helpers. So `UserService` may import `domain/identity` and call `identity.FromContext` directly; drop the `identityFrom` indirection unless a cycle appears. Confirm no import cycle at build time.

New sentinels (in `domain/instance/errors.go` or alongside): `ErrForbidden`, `ErrInvalidRole`, `ErrBootstrapAdminProtected`, `ErrLastAdmin`, and reuse `ErrUnauthenticated`/`ErrNotFound`.

NOTE on the bootstrap-email arg: `IsBootstrapAdmin(subject, email)` ideally checks both. `User` carries `Subject` but not `Email` (claims-only). Either (a) match on `Subject` only at this layer (sufficient if bootstrap lists use subjects), or (b) add a nullable `email` column + `User.Email` so the floor can match a bootstrap-by-email admin. Pick (a) for minimal scope and document that bootstrap-by-email admins are floored at provisioning time (they were created admin) but the demotion floor matches by subject; if the design's "bootstrap email" floor must hold post-provision, do (b). Decide by checking whether `AuthConfig.BootstrapAdmins` entries are emails or subjects in the design — if emails are expected, implement (b).

### Create: `adapter/http/user_handler.go`

```go
type UserHandler struct { svc *instance.UserService }

type userView struct {
    ID       string `json:"id"`
    Username string `json:"username"`
    Role     string `json:"role"`
}
```

- `List` (`GET /users`): `svc.List`; map `ErrForbidden` → 403, `ErrUnauthenticated` → 401; 200 `[]userView`.
- `SetRole` (`PUT /users/{id}/role`): parse `{id}` (400 on bad UUID), decode `{"role":"admin"|"user"}` (`MaxBytesReader`), call `svc.SetRole`. Map: `ErrInvalidRole`→400, `ErrForbidden`→403, `ErrNotFound`→404, `ErrBootstrapAdminProtected`→409, `ErrLastAdmin`→409; success→200 (or 204).

### Modify: `adapter/http/server.go`

Add `Users *instance.UserService` to `RouterDeps`; inside `/api/v1` (guarded by non-nil):

```go
if d.Users != nil {
    uh := NewUserHandler(d.Users)
    r.Route("/users", func(r chi.Router) {
        r.Get("/", uh.List)
        r.Put("/{id}/role", uh.SetRole)
    })
}
```

These are inside the authenticated group; the domain service enforces admin-only, so no extra middleware is needed.

### Create: `adapter/mcp/tools_user.go`

Add `Users *instance.UserService` to `adapter/mcp/deps.go`. Tools:

```go
func RegisterUserTools(srv *Server) {
    srv.AddTool(gomcp.NewTool("user_list",
        gomcp.WithDescription("List all users (admin only). Returns id, username, role.")),
        userListHandler(srv))
    srv.AddTool(gomcp.NewTool("user_set_role",
        gomcp.WithDescription("Set a user's role to 'user' or 'admin' (admin only). Cannot demote a bootstrap admin or the last admin."),
        gomcp.WithString("id", gomcp.Required(), gomcp.Description("Target user id")),
        gomcp.WithString("role", gomcp.Required(), gomcp.Description("'user' or 'admin'"))),
        userSetRoleHandler(srv))
}
```

Handlers read identity from the tool `ctx` and call the service. Map sentinels to clear tool errors: `ErrForbidden`→"forbidden: admin only", `ErrBootstrapAdminProtected`→"cannot demote a bootstrap admin", `ErrLastAdmin`→"cannot demote the last admin", `ErrInvalidRole`→"role must be 'user' or 'admin'", `ErrNotFound`→"user not found", nil dep→"user service not available".

### Modify: `adapter/mcp/register.go`

Add `RegisterUserTools(srv)` to `RegisterAllTools`.

### Modify: `cmd/arbitron/main.go`

Build `userSvc := instance.NewUserService(userRepo, provisioner)`; store on `domainStack`; set `Users: ds.userSvc` in both `mcpDeps()` and the `NewRouterFromDeps` call. Add `List`/`UpdateRole`/`CountAdmins`/`GetByID` to the sqlite `UserRepo` with matching `users.sql` queries (mirror Task 5 conventions):

```sql
-- name: ListUsers :many
SELECT id, subject, username, role, created_at FROM users ORDER BY created_at;
-- name: UpdateUserRole :exec
UPDATE users SET role = ? WHERE id = ?;
-- name: CountAdmins :one
SELECT COUNT(*) FROM users WHERE role = 'admin';
```

## Testing (strict TDD — five scenario categories)

Tests first: `domain/instance/user_service_test.go` (fake `UserRepository` + a stub `bootstrapChecker`, identity seeded via `identity.WithIdentity`), `adapter/http/user_handler_test.go`, `adapter/mcp/tools_user_test.go`.

**`user_service_test.go`:**
- **Happy path:** admin caller promotes a `user`→`admin` (assert `UpdateRole` called with `"admin"`); admin demotes a non-bootstrap admin when another admin remains (`CountAdmins → 2`) → `UpdateRole("user")`. `List` as admin returns all users.
- **Input validation:** `SetRole` with role `"superuser"` → `ErrInvalidRole`, no repo write; `SetRole` to the role the target already has → no-op success, no `UpdateRole` call.
- **Error/failure:** `GetByID → ErrNotFound` propagates; `CountAdmins`/`UpdateRole` repo errors propagate.
- **Boundary:** demoting the **last** admin (`CountAdmins → 1`) → `ErrLastAdmin`, no write; self-demotion allowed when another admin remains (caller==target, `CountAdmins → 2` → success) but blocked when last (`→ ErrLastAdmin`); promotion is never blocked by the admin-count or bootstrap floors.
- **Isolation/security:** a **non-admin** caller → `ErrForbidden` for both `SetRole` and `List`, with **zero** repo writes (assert `UpdateRole`/`List` never called); no identity in ctx → `ErrUnauthenticated`; demoting a **bootstrap** admin → `ErrBootstrapAdminProtected` even when several other admins exist (the floor consults the injected `bootstrapChecker`, asserted to be called with the target's subject).

**`user_handler_test.go` (httptest, identity via `identity.WithIdentity`):**
- **Happy path:** admin `PUT /users/{id}/role {"role":"admin"}` → 200; `GET /users` → 200 list of `userView` (no extra fields like subject leaking — assert body has no `"subject"`).
- **Input validation:** bad UUID → 400; invalid role → 400; malformed JSON → 400.
- **Error/failure:** unknown target → 404.
- **Boundary:** demote last admin → 409; demote bootstrap admin → 409.
- **Isolation/security:** non-admin caller → 403 on both endpoints; unauthenticated → 401.

**`tools_user_test.go` (mirror `tools_connection_test.go`):**
- **Happy path:** admin `user_set_role` promotes; `user_list` returns rows.
- **Input validation:** missing `id`/`role`, bad role → tool error.
- **Error/failure:** unknown id → "user not found".
- **Boundary:** last-admin / bootstrap-admin demotion → the respective tool error.
- **Isolation/security:** non-admin caller → "forbidden: admin only" for both tools; nil `Users` dep → "user service not available".

Run `go build ./...` and `go test ./domain/instance/... ./adapter/http/... ./adapter/mcp/...` green before handoff.
