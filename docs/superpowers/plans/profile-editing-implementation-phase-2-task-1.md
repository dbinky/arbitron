# Profile-Editing Phase 2 — Persistence, Resolver, Cache & Seeding — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move profiles from the static config-loaded resolver to a SQLite-backed `ProfileStore` fronted by an in-memory cache, resolved at runtime via a `DynamicProfileResolver` that carries the new `ContextWindow`/`Effort` fields; seed the DB once from `config.yaml` when empty.

**Architecture:** New `profiles` table (migration 015) + sqlc queries + `ProfileRepo` (mirrors `ConnectionRepo`). Domain `Profile`/`ResolvedBinding` gain two fields. `ProfileCache` (RWMutex) + `DynamicProfileResolver` replace `StaticProfileResolver`. Startup seeding gate in `cmd/arbitron/main.go`.

**Tech Stack:** Go, SQLite, sqlc (`adapter/sqlite/sqlc.yaml`), embedded migrations (`adapter/sqlite/migrate.go`).

**Design ref:** `docs/superpowers/specs/profile-editing-design-phase-2.md`. **Depends on:** Phase 1 (enums).

---

### Task 1: Extend domain Profile + ResolvedBinding; add ProfileStore port

**Files:**
- Modify: `domain/provider/provider.go`
- Create: `domain/provider/profile_store.go`
- Test: `domain/provider/profile_store_test.go` (compile-time port assertions + zero-value defaults)

- [ ] **Step 1: Write failing test**

```go
package provider

import "testing"

func TestProfile_NewAxesDefault(t *testing.T) {
	var p Profile
	// zero value must mean "unset" so existing config profiles stay valid.
	if p.ContextWindow != "" || p.Effort != "" {
		t.Fatal("zero Profile should have empty axes (treated as default)")
	}
}
```

- [ ] **Step 2: Run, verify fail** (field undefined).

- [ ] **Step 3: Add fields** to `Profile` and `ResolvedBinding` in `provider.go`:

```go
// in Profile struct, after AllowedTools:
	ContextWindow ContextWindow // "" == default (no-op); validated against CapabilityRules
	Effort        Effort        // "" == default (no-op)

// in ResolvedBinding struct, after AllowedTools:
	ContextWindow ContextWindow // copied verbatim by the resolver; consumed by Phase-3 emission
	Effort        Effort
```

- [ ] **Step 4: Create `profile_store.go`**

```go
package provider

import (
	"context"
	"errors"
)

var (
	ErrProfileExists   = errors.New("provider: profile already exists")
	ErrProfileNotFound = errors.New("provider: profile not found")
)

// ProfileStore is the driven port for profile persistence. It is a dumb
// persistence port: capability validation lives in the ProfileService (Phase 4)
// and the startup seed path, NOT here.
type ProfileStore interface {
	Create(ctx context.Context, p Profile) error
	Update(ctx context.Context, p Profile) error
	Delete(ctx context.Context, alias string) error
	Get(ctx context.Context, alias string) (Profile, error)
	List(ctx context.Context) ([]Profile, error)
	Count(ctx context.Context) (int, error)
}
```

- [ ] **Step 5: Run, verify pass** — `go test ./domain/provider/... && go build ./...`

- [ ] **Step 6: Commit** — `git commit -m "feat(provider): Profile/ResolvedBinding axes + ProfileStore port"`

---

### Task 2: Migration 015_profiles + sqlc queries

**Files:**
- Create: `adapter/sqlite/migrations/015_profiles.up.sql`
- Create: `adapter/sqlite/migrations/015_profiles.down.sql`
- Create: `adapter/sqlite/queries/profiles.sql`
- Modify (generated): run `sqlc generate` → `adapter/sqlite/generated/profiles.sql.go`

- [ ] **Step 1: Write the up migration**

```sql
-- 015_profiles.up.sql
CREATE TABLE profiles (
    alias          TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    connection_ref TEXT NOT NULL DEFAULT '',
    model          TEXT NOT NULL DEFAULT '',
    backend        TEXT NOT NULL DEFAULT '',
    allowed_tools  TEXT NOT NULL DEFAULT '[]',
    context_window TEXT NOT NULL DEFAULT 'default',
    effort         TEXT NOT NULL DEFAULT 'default',
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);
```

- [ ] **Step 2: Write the down migration** — `DROP TABLE IF EXISTS profiles;`

- [ ] **Step 3: Write queries** (`queries/profiles.sql`, sqlc syntax mirroring `connections.sql`):

```sql
-- name: CreateProfile :exec
INSERT INTO profiles (alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateProfile :exec
UPDATE profiles SET kind=?, connection_ref=?, model=?, backend=?, allowed_tools=?, context_window=?, effort=?, updated_at=?
WHERE alias=?;

-- name: DeleteProfile :exec
DELETE FROM profiles WHERE alias=?;

-- name: GetProfile :one
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort FROM profiles WHERE alias=?;

-- name: ListProfiles :many
SELECT alias, kind, connection_ref, model, backend, allowed_tools, context_window, effort FROM profiles ORDER BY alias;

-- name: CountProfiles :one
SELECT COUNT(*) FROM profiles;
```

- [ ] **Step 4: Generate** — `cd adapter/sqlite && sqlc generate` (config: `sqlc.yaml`). Verify `generated/profiles.sql.go` appears.

- [ ] **Step 5: Verify migration applies** — `go test ./adapter/sqlite/ -run Migrat -v` (existing migration test harness picks up the new file via embed).

- [ ] **Step 6: Commit** — `git commit -m "feat(sqlite): 015_profiles migration + queries"`

---

### Task 3: ProfileRepo (SQLite adapter)

**Files:**
- Create: `adapter/sqlite/profile_repo.go`
- Test: `adapter/sqlite/profile_repo_test.go`

- [ ] **Step 1: Write failing tests** (use the existing test DB helper — see `connection_repo_test.go` for `newTestDB(t)`):

```go
func TestProfileRepo_CRUD(t *testing.T) {
	repo := NewProfileRepo(newTestDB(t))
	ctx := context.Background()
	p := provider.Profile{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Model: "opus-4.8", AllowedTools: []string{"Read", "Bash"},
		ContextWindow: provider.Context1M, Effort: provider.EffortHigh}
	if err := repo.Create(ctx, p); err != nil { t.Fatal(err) }
	got, err := repo.Get(ctx, "coder")
	if err != nil || got.ContextWindow != provider.Context1M || got.Effort != provider.EffortHigh ||
		len(got.AllowedTools) != 2 {
		t.Fatalf("round-trip mismatch: %+v err %v", got, err)
	}
	if err := repo.Create(ctx, p); !errors.Is(err, provider.ErrProfileExists) {
		t.Fatalf("want ErrProfileExists, got %v", err)
	}
	if _, err := repo.Get(ctx, "ghost"); !errors.Is(err, provider.ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
	n, _ := repo.Count(ctx)
	if n != 1 { t.Fatalf("count=%d", n) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `profile_repo.go`** mirroring `connection_repo.go` (no crypto). Marshal `AllowedTools` to JSON; map unique violation via the existing `isUniqueViolation` helper → `ErrProfileExists`; pre-check existence on Update/Delete → `ErrProfileNotFound`. Stamp `created_at`/`updated_at` with `time.Now().UTC().Format(time.RFC3339)`. Parse `context_window`/`effort` back through `provider.ParseContextWindow`/`ParseEffort` (a malformed stored value is a corruption error). Add `var _ provider.ProfileStore = (*ProfileRepo)(nil)`.

- [ ] **Step 4: Run, verify pass** — `go test ./adapter/sqlite/ -run Profile -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(sqlite): ProfileRepo implementing ProfileStore"`

---

### Task 4: ProfileCache + DynamicProfileResolver

**Files:**
- Create: `adapter/provider/cache.go`
- Create: `adapter/provider/dynamic.go`
- Test: `adapter/provider/dynamic_test.go`, `adapter/provider/cache_test.go`
- Modify/Remove: `adapter/provider/static.go` (port its tests to dynamic; remove once the resolver is wired in Task 5)

- [ ] **Step 1: Write failing tests**

```go
func TestDynamicResolver_ResolveCarriesAxes(t *testing.T) {
	store := newFakeStore( // in-memory ProfileStore fake
		provider.Profile{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code",
			Model: "opus-4.8", ContextWindow: provider.Context1M, Effort: provider.EffortHigh})
	cache := NewProfileCache(store)            // store bound at construction
	if err := cache.Load(context.Background()); err != nil { t.Fatal(err) }
	r := NewDynamicProfileResolver(cache, nil)
	b, err := r.Resolve(context.Background(), "coder")
	if err != nil || b.ContextWindow != provider.Context1M || b.Effort != provider.EffortHigh {
		t.Fatalf("binding axes missing: %+v err %v", b, err)
	}
}

func TestProfileCache_ReloadReflectsMutation(t *testing.T) {
	store := newFakeStore()
	cache := NewProfileCache(store)
	cache.Load(context.Background())
	r := NewDynamicProfileResolver(cache, nil)
	if _, err := r.Resolve(context.Background(), "coder"); err == nil { t.Fatal("expected unknown") }
	store.put(provider.Profile{Alias: "coder", Kind: provider.ProfileKindChat})
	if err := cache.Reload(context.Background()); err != nil { t.Fatal(err) } // recache (== Load)
	if _, err := r.Resolve(context.Background(), "coder"); err != nil { t.Fatalf("after reload: %v", err) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `cache.go`**

```go
package provider

import (
	"context"
	"sync"

	dprovider "github.com/arbitron/arbitron/domain/provider"
)

// ProfileCache binds the store at construction so it satisfies the Phase-4
// dprovider.Reloader interface (Reload(ctx) error) directly — the ProfileService
// recache hook calls Reload after every successful write.
type ProfileCache struct {
	store   dprovider.ProfileStore
	mu      sync.RWMutex
	byAlias map[string]dprovider.Profile
}

func NewProfileCache(store dprovider.ProfileStore) *ProfileCache {
	return &ProfileCache{store: store, byAlias: map[string]dprovider.Profile{}}
}

// Load (re)populates the cache from the bound store.
func (c *ProfileCache) Load(ctx context.Context) error {
	list, err := c.store.List(ctx)
	if err != nil { return err }
	m := make(map[string]dprovider.Profile, len(list))
	for _, p := range list { m[p.Alias] = p }
	c.mu.Lock(); c.byAlias = m; c.mu.Unlock()
	return nil
}

// Reload is the recache hook (alias of Load) satisfying dprovider.Reloader.
func (c *ProfileCache) Reload(ctx context.Context) error { return c.Load(ctx) }

func (c *ProfileCache) get(alias string) (dprovider.Profile, bool) {
	c.mu.RLock(); defer c.mu.RUnlock()
	p, ok := c.byAlias[alias]; return p, ok
}
```

- [ ] **Step 4: Implement `dynamic.go`** — copy the resolve chain from `static.go` but read `cache.get(alias)` instead of the immutable map, and copy `ContextWindow`/`Effort` onto both binding branches (no-key and connection-backed). Keep `Kind(alias)` zero-I/O via `cache.get`. The resolver may also expose `Reload(ctx)` delegating to `cache.Reload(ctx)` for callers that hold only the resolver.

- [ ] **Step 5: Run, verify pass; race test** — `go test ./adapter/provider/ -race -v`

- [ ] **Step 6: Commit** — `git commit -m "feat(provider): ProfileCache + DynamicProfileResolver"`

---

### Task 5: Startup seeding + wiring (replace StaticProfileResolver)

**Files:**
- Modify: `cmd/arbitron/main.go` (`initDomain`, around the current `NewStaticProfileResolver` call ~lines 544-563)
- Create: `cmd/arbitron/profile_seed.go` (seed helper, unit-testable)
- Test: `cmd/arbitron/profile_seed_test.go`
- Remove: `adapter/provider/static.go` + `static_test.go` (superseded)

- [ ] **Step 1: Write failing test for the seed helper**

```go
func TestSeedProfiles_EmptyTableSeedsValidSkipsInvalid(t *testing.T) {
	store := newFakeStore()
	rules := testRules() // claude-code valid; bogus backend invalid
	in := []provider.Profile{
		{Alias: "coder", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "opus-4.8"},
		{Alias: "weird", Kind: provider.ProfileKindAgent, Backend: "nonexistent", ContextWindow: provider.Context1M},
	}
	n, skipped, err := SeedProfiles(context.Background(), store, rules, in)
	if err != nil { t.Fatal(err) }
	if n != 1 || skipped != 1 { t.Fatalf("seeded=%d skipped=%d", n, skipped) }
}

func TestSeedProfiles_NonEmptyTableIsNoOp(t *testing.T) {
	store := newFakeStore(provider.Profile{Alias: "existing", Kind: provider.ProfileKindChat})
	n, _, err := SeedProfiles(context.Background(), store, testRules(), []provider.Profile{{Alias: "coder", Kind: provider.ProfileKindAgent}})
	if err != nil || n != 0 { t.Fatalf("expected no-op, seeded=%d err=%v", n, err) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `SeedProfiles`**

```go
// SeedProfiles seeds the store ONCE when empty. Each candidate is validated
// against the capability rules; invalid candidates are skipped (logged by the
// caller) so operator config never blocks boot. Returns (seeded, skipped, err).
func SeedProfiles(ctx context.Context, store provider.ProfileStore, rules provider.CapabilityRules, candidates []provider.Profile) (int, int, error) {
	n, err := store.Count(ctx)
	if err != nil { return 0, 0, err }
	if n > 0 { return 0, 0, nil } // already seeded; DB wins
	seeded, skipped := 0, 0
	for _, p := range candidates {
		backend := p.Backend
		if backend == "" && p.ConnectionRef == "" { backend = "claude-cli" } // chat subscription default
		if _, _, verr := rules.Validate(backend, p.Model, p.ContextWindow, p.Effort); verr != nil {
			skipped++; continue
		}
		if err := store.Create(ctx, p); err != nil {
			if errors.Is(err, provider.ErrProfileExists) { continue }
			return seeded, skipped, err
		}
		seeded++
	}
	return seeded, skipped, nil
}
```

- [ ] **Step 4: Wire into `initDomain`** — after building `connStore`, capability `rules` (from `config.LoadCapabilityRules(cfg.CapabilitiesPath)`), build `profileRepo := sqlite.NewProfileRepo(db)`; gather candidates `configadapter.ProfilesFromConfig(cfg.Profiles)` (∪ defaults already merged by the loader); call `SeedProfiles(ctx, profileRepo, rules, candidates)` and `slog`-log seeded/skipped counts; `cache := providersadapter.NewProfileCache(profileRepo); cache.Load(ctx)`; `profileResolver := providersadapter.NewDynamicProfileResolver(cache, connStore)`. Keep a reference to `cache` — it is injected into the Phase-4 `ProfileService` as the `Reloader`. Replace the old `NewStaticProfileResolver` usage; keep `NewResolverChecker(profileResolver)` and downstream wiring identical. Add a `CapabilitiesPath` field to `domain/config/config.go` (default `~/.arbitron/capabilities.yaml`, resolved like other paths).

- [ ] **Step 5: Build + full provider/sqlite/cmd tests** — `go build ./... && go test ./domain/provider/... ./adapter/provider/... ./adapter/sqlite/... ./cmd/...`

- [ ] **Step 6: Commit** — `git commit -m "feat(cmd): DB-backed profiles with one-time config seeding"`

---

## Self-Review (Phase 2)
- **Spec coverage:** §3 DB-backed → Tasks 2-3; §7 cache/recache → Task 4 (`Reload`); §8 seed-once/DB-wins → Task 5. ✓
- **Type consistency:** `ProfileStore`, `ProfileCache`, `DynamicProfileResolver`, `Reload`, `SeedProfiles` align with design Phase 2 and are consumed by Phases 3-4. `cfg.CapabilitiesPath` introduced here, consumed by Phase 4 composition. ✓
- **Placeholder scan:** none. Cache `Reload` hook is exercised by the Phase-4 service.
