# Profile-Editing Phase 4 — Admin Services + MCP Tools — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose admin-only profile management via MCP (`profile_create/update/delete/list/get`, `provider_capabilities`) with capability validation + live recache + effective-value echo, and retrofit the existing `connection_*` tools to admin-only.

**Architecture:** `ProfileService`/`ConnectionService` in `domain/provider` own authz (`identity.IsAdmin`) + validation + recache (mirroring `instance.UserService`). MCP handlers in `adapter/mcp` delegate to the services and map sentinels to tool errors (mirroring `tools_user.go`). Wired through `adapter/mcp/deps.go` + composition root.

**Tech Stack:** Go, mark3labs/mcp-go, `domain/identity` (FromContext/IsAdmin).

**Design ref:** `docs/superpowers/specs/profile-editing-design-phase-4.md`. **Depends on:** Phases 1, 2, 3.

---

### Task 1: ProfileService (authz + validation + recache)

**Files:**
- Create: `domain/provider/profile_service.go`
- Test: `domain/provider/profile_service_test.go`

- [ ] **Step 1: Write failing tests** (fake store + cache reloader + ctx identity helpers)

```go
func admins() context.Context { return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin}) }
func users()  context.Context { return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser}) }

func TestProfileService_CreateRequiresAdmin(t *testing.T) {
	svc := NewProfileService(newFakeStore(), testRules(), &fakeReloader{})
	_, _, err := svc.Create(users(), Profile{Alias: "x", Kind: ProfileKindAgent, Backend: "claude-code"})
	if !errors.Is(err, ErrForbidden) { t.Fatalf("want ErrForbidden, got %v", err) }
	_, _, err = svc.Create(context.Background(), Profile{Alias: "x"})
	if !errors.Is(err, ErrUnauthenticated) { t.Fatalf("want ErrUnauthenticated, got %v", err) }
}

func TestProfileService_CreateValidatesAndReloads(t *testing.T) {
	rl := &fakeReloader{}
	svc := NewProfileService(newFakeStore(), testRules(), rl)
	// inapplicable axis rejected, nothing persisted, no reload
	_, _, err := svc.Create(admins(), Profile{Alias: "bad", Kind: ProfileKindAgent, Backend: "codex", ContextWindow: Context1M})
	if !errors.Is(err, ErrAxisNotApplicable) { t.Fatalf("want ErrAxisNotApplicable, got %v", err) }
	if rl.calls != 0 { t.Fatal("no reload on rejected write") }
	// floor case: stored xhigh, effective high, reload called once
	_, eff, err := svc.Create(admins(), Profile{Alias: "ok", Kind: ProfileKindAgent, Backend: "claude-code", Model: "sonnet-4.6", Effort: EffortXHigh})
	if err != nil || eff.Effort != EffortHigh { t.Fatalf("effective effort %q err %v", eff.Effort, err) }
	if rl.calls != 1 { t.Fatalf("reload calls=%d", rl.calls) }
}

func TestProfileService_ListReadOpenToAuthenticated(t *testing.T) {
	svc := NewProfileService(newFakeStore(Profile{Alias: "a", Kind: ProfileKindChat}), testRules(), &fakeReloader{})
	if _, err := svc.List(users()); err != nil { t.Fatalf("non-admin read should succeed: %v", err) }
	if _, err := svc.List(context.Background()); !errors.Is(err, ErrUnauthenticated) { t.Fatal("unauth read must fail") }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3a: Create the shared `domain/provider/authz.go`** (sentinels + helpers used by BOTH services — declare them once here, not in each service file):

```go
package provider

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
)

// Reuse the canonical authz sentinels so errors.Is matches across surfaces
// (identity → instance → provider all alias the same vars). domain/identity
// imports nothing from domain/provider, so there is no import cycle.
var (
	ErrUnauthenticated = identity.ErrUnauthenticated
	ErrForbidden       = identity.ErrForbidden
)

func requireAuthed(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok { return identity.Identity{}, ErrUnauthenticated }
	return id, nil
}
func requireAdmin(ctx context.Context) error {
	id, err := requireAuthed(ctx); if err != nil { return err }
	if !id.IsAdmin() { return ErrForbidden }
	return nil
}

// Reloader is the recache hook the ProfileService calls after a successful
// write. The adapter ProfileCache (Phase 2) satisfies it via Reload(ctx).
type Reloader interface{ Reload(ctx context.Context) error }
```

- [ ] **Step 3b: Implement `profile_service.go`**

```go
package provider

import "context"

// Effective is the floored, runtime-effective view echoed back on a write.
type Effective struct {
	ContextWindow ContextWindow
	Effort        Effort
}

type ProfileService struct {
	store    ProfileStore
	rules    CapabilityRules
	reloader Reloader
}

func NewProfileService(store ProfileStore, rules CapabilityRules, reloader Reloader) *ProfileService {
	return &ProfileService{store: store, rules: rules, reloader: reloader}
}

func (s *ProfileService) List(ctx context.Context) ([]Profile, error) {
	if _, err := requireAuthed(ctx); err != nil { return nil, err }
	return s.store.List(ctx)
}
func (s *ProfileService) Get(ctx context.Context, alias string) (Profile, error) {
	if _, err := requireAuthed(ctx); err != nil { return Profile{}, err }
	return s.store.Get(ctx, alias)
}

func (s *ProfileService) Create(ctx context.Context, p Profile) (Profile, Effective, error) {
	return s.write(ctx, p, s.store.Create)
}
func (s *ProfileService) Update(ctx context.Context, p Profile) (Profile, Effective, error) {
	return s.write(ctx, p, s.store.Update)
}

func (s *ProfileService) write(ctx context.Context, p Profile, persist func(context.Context, Profile) error) (Profile, Effective, error) {
	if err := requireAdmin(ctx); err != nil { return Profile{}, Effective{}, err }
	backend := p.Backend
	if backend == "" && p.ConnectionRef == "" { backend = "claude-cli" }
	effTier, effEffort, err := s.rules.Validate(backend, p.Model, p.ContextWindow, p.Effort)
	if err != nil { return Profile{}, Effective{}, err }
	if err := persist(ctx, p); err != nil { return Profile{}, Effective{}, err }
	_ = s.reloader.Reload(ctx) // best-effort; failure logged by caller, write already durable
	return p, Effective{ContextWindow: effTier, Effort: effEffort}, nil
}

func (s *ProfileService) Delete(ctx context.Context, alias string) error {
	if err := requireAdmin(ctx); err != nil { return err }
	if err := s.store.Delete(ctx, alias); err != nil { return err }
	_ = s.reloader.Reload(ctx)
	return nil
}

// Capabilities exposes the rules for the provider_capabilities read tool.
func (s *ProfileService) Capabilities(ctx context.Context) (CapabilityRules, error) {
	if _, err := requireAuthed(ctx); err != nil { return CapabilityRules{}, err }
	return s.rules, nil
}
```
Add a `CapabilityRules.Describe() map[string]BackendCapability` accessor (read-only copy) so the MCP tool can serialize it.

- [ ] **Step 4: Run, verify pass** — `go test ./domain/provider/ -run ProfileService -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(provider): ProfileService authz+validate+recache"`

---

### Task 2: ConnectionService (admin gate)

**Files:**
- Create: `domain/provider/connection_service.go`
- Test: `domain/provider/connection_service_test.go`

- [ ] **Step 1: Write failing tests** — non-admin Create/Get/List/Delete/Update → `ErrForbidden`; missing identity → `ErrUnauthenticated`; admin passes through to the store; `Reveal` is NOT on the service.

```go
func TestConnectionService_AdminGate(t *testing.T) {
	svc := NewConnectionService(newFakeConnStore())
	if _, err := svc.List(users()); !errors.Is(err, ErrForbidden) { t.Fatal("list admin-only") }
	if err := svc.Create(users(), Connection{Name: "c", Provider: "openrouter", BaseURL: "https://x"}, "k"); !errors.Is(err, ErrForbidden) { t.Fatal("create admin-only") }
	if _, err := svc.List(admins()); err != nil { t.Fatalf("admin list: %v", err) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `connection_service.go`** — wrap `ConnectionStore`; every method calls the shared `requireAdmin(ctx)` from `authz.go` (created in Task 1 Step 3a) then delegates. Signatures mirror the store: `Create(ctx, Connection, apiKey string)`, `Update(ctx, Connection, *string)`, `Delete(ctx, name)`, `Get(ctx, name)`, `List(ctx)`. Do **not** expose `Reveal`. (If implementing Task 2 before Task 1, create `authz.go` here instead — it must exist exactly once.)

- [ ] **Step 4: Run, verify pass.**

- [ ] **Step 5: Commit** — `git commit -m "feat(provider): admin-gated ConnectionService"`

---

### Task 3: MCP profile tools + provider_capabilities

**Files:**
- Create: `adapter/mcp/tools_profile.go`
- Modify: `adapter/mcp/register.go` (register the new tools), `adapter/mcp/deps.go` (add fields)
- Test: `adapter/mcp/tools_profile_test.go`

- [ ] **Step 1: Add Deps fields** in `deps.go`:

```go
	// ProfileAdmin powers the admin-only profile_* mutation tools and the
	// read-open profile_list/get + provider_capabilities tools. Authz + capability
	// validation + recache live in the service.
	ProfileAdmin *provider.ProfileService
	// ConnectionAdmin powers the now admin-only connection_* tools.
	ConnectionAdmin *provider.ConnectionService
```

- [ ] **Step 2: Write failing handler tests** mirroring `tools_user_test.go` / `authz_e2e_test.go`: non-admin `profile_create` → "forbidden: admin only"; admin create echoes `effective`; `profile_list` works for a non-admin; `provider_capabilities` returns backends; inapplicable axis → specific error.

- [ ] **Step 3: Implement `tools_profile.go`** — register `profile_create/update/delete/list/get` + `provider_capabilities`; build `provider.Profile` from args (parse `context_window`/`effort` via `provider.ParseContextWindow`/`ParseEffort`, mapping parse errors to tool errors); call `srv.deps.ProfileAdmin`; map sentinels with a `profileErrorResult(err)` helper:

```go
func profileErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, provider.ErrForbidden):
		return gomcp.NewToolResultError("forbidden: admin only")
	case errors.Is(err, provider.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, provider.ErrAxisNotApplicable):
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, provider.ErrProfileExists):
		return gomcp.NewToolResultError("profile already exists")
	case errors.Is(err, provider.ErrProfileNotFound):
		return gomcp.NewToolResultError("profile not found")
	case errors.Is(err, provider.ErrUnknownBackend),
		errors.Is(err, provider.ErrInvalidEffort),
		errors.Is(err, provider.ErrInvalidContextWindow):
		return gomcp.NewToolResultError(err.Error())
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("profile operation failed: %s", err))
	}
}
```
The create/update success result includes `{"status":"created","profile":<meta>,"effective":{"context_window":..,"effort":..}}`. `provider_capabilities` serializes `ProfileAdmin.Capabilities(ctx)` → `Describe()`.

- [ ] **Step 4: Register** in `register.go` (call `RegisterProfileTools(srv)` alongside the others; guard nil `ProfileAdmin`).

- [ ] **Step 5: Run, verify pass** — `go test ./adapter/mcp/ -run Profile -v`

- [ ] **Step 6: Commit** — `git commit -m "feat(mcp): profile_* + provider_capabilities tools"`

---

### Task 4: Retrofit connection tools to admin-only

**Files:**
- Modify: `adapter/mcp/tools_connection.go` (route through `srv.deps.ConnectionAdmin`)
- Test: `adapter/mcp/tools_connection_test.go` (add non-admin refusal cases)

- [ ] **Step 1: Write failing tests** — non-admin `connection_list`/`connection_get`/`connection_create` now return "forbidden: admin only"; admin path unchanged; api_key still never echoed.

- [ ] **Step 2: Run, verify fail** (currently open to any caller).

- [ ] **Step 3: Implement** — change handlers from `srv.deps.Connections` to `srv.deps.ConnectionAdmin`; extend `connErrorResult` with `ErrForbidden`/`ErrUnauthenticated` cases. Keep `connectionMeta` (no key) and logging unchanged. If `ConnectionAdmin` is nil, fall back to "connection service not available".

- [ ] **Step 4: Run, verify pass** — `go test ./adapter/mcp/ -run Connection -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(mcp): connection_* tools admin-only via ConnectionService"`

---

### Task 5: Composition root wiring + authz e2e

**Files:**
- Modify: `cmd/arbitron/main.go` (`initDomain` — construct services, inject into `Deps`)
- Test: `adapter/mcp/profile_authz_e2e_test.go` (mirror `authz_e2e_test.go`)

- [ ] **Step 1:** Build `provider.NewProfileService(profileRepo, rules, cache)` — the `cache` from Phase 2 Task 4 (`*ProfileCache`, store bound at construction) satisfies `provider.Reloader` directly via its `Reload(ctx)` method, so it is passed verbatim. Build `provider.NewConnectionService(connStore)`. Set `deps.ProfileAdmin`, `deps.ConnectionAdmin`, `deps.Capabilities`.
- [ ] **Step 2: Write e2e test** — admin vs non-admin across every profile_* and connection_* tool; recache visibility (update a profile, resolve reflects it); effective echo present; `provider_capabilities` readable by non-admin; no secret in any connection response.
- [ ] **Step 3: Build + full suite** — `go build ./... && make test`
- [ ] **Step 4: Commit** — `git commit -m "feat(cmd): wire profile/connection admin services into MCP"`

---

## Self-Review (Phase 4)
- **Spec coverage:** §5 write-time validation + effective echo → Task 1/3; §6 admin-only mutation (profiles+connections), read-open profiles, admin-only connections, local-implicit-admin → Tasks 1-4 (+ middleware already injects admin locally); §7 recache → Task 1/5; `provider_capabilities` discovery → Task 3. ✓
- **Type consistency:** `ProfileService`/`ConnectionService`/`Effective`/`Reloader`/`profileErrorResult` align with design Phase 4; `Reloader` satisfied by `*ProfileCache.Reload` from Phase 2. ✓
- **Placeholder scan:** none. `ProfileCache` is store-bound and satisfies `provider.Reloader` directly (Phase 2 Task 4), so Task 5 passes the cache verbatim — no adapter glue needed.
