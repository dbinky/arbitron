# Design — Phase 4: Admin Services + MCP Tools

**Feature:** profile-editing · **Phase:** 4 of 4
**Spec:** `docs/specs/profile-editing-spec.md` (§5 write-time validation, §6 authz, §7 recache)
**Depends on:** Phases 1 (validation) + 2 (store/cache/resolver) + 3 (so edits take effect)

## Purpose

Deliver the admin-facing editing surface: domain services that enforce **authz +
capability validation + recache**, and the MCP tools that expose them — plus
retrofitting the existing connection tools to **admin-only**. After this phase
the full loop works: admin edits via MCP → validated → persisted → cache
reloaded → next run emits the new context/effort.

## Domain services (`domain/provider`)

Authz lives in the domain (one rule shared by MCP and any future REST), mirroring
`instance.UserService`.

### ProfileService
```go
type ProfileService struct {
    store    ProfileStore
    rules    CapabilityRules
    reloader interface{ Reload(ctx context.Context) error } // the ProfileCache
}
```
- `List(ctx) ([]Profile, error)` / `Get(ctx, alias)` — require an **authenticated**
  identity (any role); profiles are read-open per spec §6. (`identity.FromContext`;
  missing → `ErrUnauthenticated`.)
- `Create/Update(ctx, Profile) (Profile, Effective, error)` — require **admin**
  (`IsAdmin`, else `ErrForbidden`); then `rules.Validate(backend, model, tier,
  effort)`:
  - inapplicable axis → `ErrAxisNotApplicable` (nothing persisted),
  - else persist via `store`, then `reloader.Reload(ctx)` (recache),
  - return the stored profile **plus** the `Effective{ContextWindow, Effort}`
    (the floored values) for the echo.
- `Delete(ctx, alias)` — admin; `store.Delete` then `Reload`.
- Recache is **after** a successful write and best-effort-logged. A reload failure
  does not fail the write (the DB already holds the truth): the service logs it
  and returns the stored profile with a non-fatal `reload_warning` field.

### ConnectionService
Thin admin gate over the existing `provider.ConnectionStore` so connection
mutation **and** metadata reads become admin-only (spec §6):
```go
type ConnectionService struct { store provider.ConnectionStore }
// Create/Update/Delete/Get/List — all require IsAdmin (ErrForbidden otherwise).
// Reveal stays internal to the resolver path and is NOT exposed here.
```
The runtime resolver keeps using the raw `ConnectionStore` (no authz on the
engine path); only the MCP management handlers move to `ConnectionService`.

## MCP tools (`adapter/mcp`)

### New: `tools_profile.go`
Registered in `register.go`. Handlers map sentinels exactly like `tools_user.go`
(`ErrForbidden`→"forbidden: admin only", `ErrUnauthenticated`→"authentication
required", `ErrAxisNotApplicable`→specific message, `ErrProfileExists/NotFound`,
`ErrUnknownBackend`, `ErrInvalid*`).
- `profile_list` — any authenticated; returns alias/kind/connection_ref/model/
  backend/allowed_tools/context_window/effort.
- `profile_get` — any authenticated.
- `profile_create` — admin. Args: alias, kind, connection_ref?, model?, backend?,
  allowed_tools?, context_window?, effort?. Response echoes stored values **and**
  `effective` (floored) settings.
- `profile_update` — admin. Same shape; full replace by alias.
- `profile_delete` — admin.
- `provider_capabilities` — read-only (any authenticated). Returns the effective
  `CapabilityRules` (allowed tiers + effort per backend/model) so a caller can
  discover valid values before writing. No secrets.

### Retrofit: `tools_connection.go`
Switch the five existing handlers from `srv.deps.Connections` (raw store) to
`srv.deps.ConnectionAdmin` (the new `ConnectionService`), so every connection
operation now enforces admin. Error mapping gains the `ErrForbidden`/
`ErrUnauthenticated` cases. Response shapes (no api_key) are unchanged.

### Deps wiring (`deps.go`)
Add `ProfileAdmin *provider.ProfileService`, `ConnectionAdmin
*provider.ConnectionService`, and `Capabilities provider.CapabilityRules`. Keep
`Connections provider.ConnectionStore` only if still needed by non-admin paths;
otherwise the management handlers use the services. Nil-guard each (report
"service not available") as existing handlers do.

## Composition root (`cmd/arbitron/main.go`)
Construct `CapabilityRules` (Phase 1 loader), `ProfileService` (store + rules +
cache), `ConnectionService` (existing store), and inject all into MCP `Deps`.
Register the new tools.

## Local-mode behavior
No special-casing: `auth_middleware.go` (ModeLocal) already injects an admin
identity, so the single local user passes every `IsAdmin` gate — the admin
restriction is inert locally and active only in org/edge modes. Confirmed at
`adapter/http/auth_middleware.go:223-238`.

## Error handling
All mutation validates **before** persisting (no partial writes). Capability
rejection names the offending axis/backend. Recache failure after a successful
persist is logged and reported as a non-fatal `reload_warning` (the DB is already
the source of truth; the next boot reloads cleanly).

## Testing (TDD — all 5 categories)
- **Service authz (unit, fake store + ctx identity):** non-admin
  create/update/delete → `ErrForbidden`; missing identity → `ErrUnauthenticated`;
  admin happy path persists + reloads; read-open for list/get with a non-admin
  identity.
- **Capability validation:** inapplicable axis rejected (nothing persisted);
  floor case persists requested + returns floored `effective`.
- **Recache visibility:** after `profile_update`, the resolver (same cache)
  returns the new binding without restart (integration).
- **MCP e2e (mirror `authz_e2e_test.go`):** admin vs non-admin over each
  profile_* and connection_* tool; `provider_capabilities` readable; echo of
  effective values present; connection_get/list now refuse non-admin.
- **Connection retrofit regression:** existing connection CRUD still works for an
  admin; api_key never echoed.

## Definition of done (whole feature)
Spec §10 success criteria 1–6 all demonstrably pass: live admin edits take
effect with no restart; non-admin mutation refused; invalid writes rejected with
clear messages and floored writes echo effective values; context/effort reach
claude-code + codex at runtime; config.yaml deployments upgrade with profiles
intact and persist across restarts; reads honor the open-profiles/admin-only-
connections split with no secret ever returned.
