# Design — Phase 2: Persistence, Resolver, Cache & Seeding

**Feature:** profile-editing · **Phase:** 2 of 4
**Spec:** `docs/specs/profile-editing-spec.md` (§3 DB-backed, §7 recache, §8 seeding)
**Depends on:** Phase 1 (enums + ruleset) · **Blocks:** Phases 3, 4

## Purpose

Move profiles from the immutable, config-loaded `StaticProfileResolver` to a
**database-backed** store fronted by an in-memory **cache**, resolved at runtime
with no authz (engine path). Extend the domain `Profile`/`ResolvedBinding` with
the two new axes. **Seed once** from `config.yaml` when the table is empty; the
DB is the source of truth thereafter.

## Domain changes (`domain/provider`)

Extend `Profile` and `ResolvedBinding` (both in `provider.go`):
```go
type Profile struct {
    Alias, ConnectionRef, Model, Backend string
    Kind          ProfileKind
    AllowedTools  []string
    ContextWindow ContextWindow // NEW (default = "default")
    Effort        Effort        // NEW (default = "default")
}
type ResolvedBinding struct {
    // ...existing fields...
    ContextWindow ContextWindow // NEW — copied verbatim by the resolver
    Effort        Effort        // NEW — consumed by Phase-3 emission
}
```
New driven port:
```go
type ProfileStore interface {
    Create(ctx, Profile) error          // ErrProfileExists on dup alias
    Update(ctx, Profile) error          // ErrProfileNotFound
    Delete(ctx, alias string) error
    Get(ctx, alias string) (Profile, error)
    List(ctx) ([]Profile, error)
    Count(ctx) (int, error)             // feeds the seed gate
}
```
Sentinels: `ErrProfileExists`, `ErrProfileNotFound`. (Capability validation is
NOT in the store — it lives in the Phase-4 service so the runtime seed path and
the admin path share one validator but the store stays a dumb persistence port,
mirroring how `ConnectionStore` keeps URL/Azure checks in `ValidateConnection`
called by the repo. Here the store persists what it is given; the seed path and
service validate before calling it.)

## Persistence (`adapter/sqlite`)

- **Migration** `015_profiles.up.sql` / `.down.sql`:
  ```sql
  CREATE TABLE profiles (
    alias          TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,            -- chat | agent
    connection_ref TEXT NOT NULL DEFAULT '', -- '' = claude-cli no-key
    model          TEXT NOT NULL DEFAULT '',
    backend        TEXT NOT NULL DEFAULT '',
    allowed_tools  TEXT NOT NULL DEFAULT '[]', -- JSON array
    context_window TEXT NOT NULL DEFAULT 'default',
    effort         TEXT NOT NULL DEFAULT 'default',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
  );
  ```
  No FK to `connections` (connection may be created after the profile; reference
  integrity is checked by the existing `ConnectionChecker` at workflow-validation
  time, unchanged).
- `queries/profiles.sql` (sqlc) — Create/Update/Delete/Get/List/Count, following
  `queries/connections.sql`. Run `sqlc generate`.
- `ProfileRepo` (`profile_repo.go`) implementing `provider.ProfileStore`,
  modeled on `ConnectionRepo`: JSON (de)serialize `allowed_tools`; map unique
  violation → `ErrProfileExists`; pre-check existence on update/delete →
  `ErrProfileNotFound`. No crypto (profiles carry no secrets).

## Cache + resolver (`adapter/provider`)

- `ProfileCache`: `map[string]provider.Profile` behind a `sync.RWMutex` with
  `Load(ctx, store)` (full repopulate) and a read path `byAlias(alias)`.
- `DynamicProfileResolver` replaces `StaticProfileResolver`, same interface
  (`Resolve`, `Kind`) and same alias→connection→binding chain — but reads the
  **cache** instead of an immutable map, and copies `ContextWindow`/`Effort` onto
  the `ResolvedBinding`. The connection lookup + lazy `RevealKey` logic is
  unchanged (copy from `static.go`). `Kind()` stays zero-I/O (reads cache).
- `Reload(ctx)` re-runs `ProfileCache.Load` — called at startup and by the
  Phase-4 service after each successful write (the recache hook). Concurrency:
  writers take the cache write-lock; resolvers take the read-lock.
- The existing `ProfileChecker`/`ConnChecker` wrap the dynamic resolver
  unchanged, so workflow validation automatically reflects live edits.

`StaticProfileResolver` is removed once the dynamic resolver is wired (its tests
port over to the cache-backed resolver).

## Startup seeding (`cmd/arbitron/main.go` `initDomain`)

Replace the `NewStaticProfileResolver(domainProfiles, connStore)` wiring with:
1. Build `ProfileRepo` (store) + `ProfileCache`.
2. **Seed gate:** if `store.Count(ctx) == 0`, seed: take
   `ProfilesFromConfig(cfg.Profiles)` ∪ built-in defaults
   (`adapter/config/loader.go:defaults()`), and for each, **validate against the
   capability ruleset** (Phase 1) — a valid profile is `store.Create`d; an invalid
   one is **logged and skipped** (operator-authored config never crashes boot).
   New profiles from config that lack the new axes default to `default/default`
   (always valid).
3. `cache.Load(ctx, store)`; build `DynamicProfileResolver(cache, connStore)`.
4. Wire resolver into engine + checkers exactly as before.

After seeding, `config.yaml` profile entries are ignored on every subsequent
boot (the table is non-empty). Non-profile config is untouched.

## Testing (TDD — all 5 categories)
- **Store (integration, real SQLite):** CRUD happy paths; dup alias →
  `ErrProfileExists`; missing → `ErrProfileNotFound`; round-trip of
  `allowed_tools`, `context_window`, `effort`.
- **Resolver:** alias→binding incl. new axes on the binding; unknown alias →
  `ErrUnknownProfile`; connection-backed path still reveals lazily; `Kind` zero-I/O.
- **Cache:** `Reload` reflects a store mutation; concurrent read/reload safe
  (race test).
- **Seeding:** empty table seeds from config+defaults; invalid config entry is
  skipped+logged, valid ones persisted; non-empty table is left untouched
  (idempotent boot).
- **Migration:** up creates the table; down drops it; existing DB upgrades.

## Out of scope (this phase)
Runtime emission of the axes (Phase 3) and the MCP admin surface + capability
validation service (Phase 4). After Phase 2 profiles are DB-backed, cached, and
resolve with the new fields present but not yet emitted to any backend.
