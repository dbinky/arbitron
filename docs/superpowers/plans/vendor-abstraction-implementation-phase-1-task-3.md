# Phase 1, Task 3: Slim Profile Registry — `domain/provider` + `StaticProfileResolver` + Config

## Goal

Introduce the slim profile-registry concept so nodes reference a backend by alias (`model_profile`) and a capability contract (`chat` vs `agent`) exists from Phase 1. Add the `domain/provider` package (`Profile`, `ProfileKind`, `ProfileResolver` port), the `adapter/provider` `StaticProfileResolver` over a koanf-loaded flat registry, and the `domain/config`/`adapter/config` additions to load it. No connections yet (those arrive in Phase 2) — every profile resolves to `claude-cli`.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §4 and §6.

## Dependencies

- **Task 1** (Port split) for the `ChatProvider`/`AgentProvider` vocabulary referenced in the resolver's role. No code dependency on Task 2, but logically the resolver selects which adapter kind a node uses.

## Background (current state)

- `domain/config/config.go` — `Config` aggregates `Server`, `Database`, `Workspace`, `LLM`, `Logging`, `Tracing`, `Queue`, `Script`. `LLMConfig` retains binary path / default model / timeout / tool scope / max response bytes.
- `adapter/config/loader.go` — koanf loader: `defaults()` flat map, optional YAML, `ARBITRON_`-prefixed env overrides (precedence env > YAML > defaults), special-cases `ANTHROPIC_API_KEY` and `ARBITRON_SCRIPT_ENVPASSTHROUGH` (CSV → slice). Unmarshals into `domainconfig.Config`.
- There is no existing list-of-structs config block loaded via koanf; `script.envpassthrough` is the only slice and it is a `[]string`. The profiles block is a `[]struct`, so it needs an explicit confmap default and a YAML example.

## Files to Create/Modify

### Create: `domain/provider/provider.go`

```go
// Package provider defines the slim profile registry: the alias indirection
// and capability contract that node kinds resolve a backend through. Phase 2
// adds ConnectionRef + default params; Phase 1 is alias + kind + model only.
package provider

type ProfileKind string

const (
    ProfileKindChat  ProfileKind = "chat"
    ProfileKindAgent ProfileKind = "agent"
)

// Profile is the slim P1 shape.
type Profile struct {
    Alias string
    Kind  ProfileKind
    Model string // model-id passed to the resolved backend
}

// ProfileResolver maps a node's alias to a Profile. Driven port; the engine
// and workflow validation depend on it, adapters implement it.
type ProfileResolver interface {
    Resolve(alias string) (Profile, error)
}
```

- Define sentinel errors in the same package for adapter + caller use:
  ```go
  var ErrUnknownProfile = errors.New("provider: unknown profile alias")
  ```
  (`ErrUnknownProfile` is returned by resolvers for a miss; validation/executors match it.)

### Create: `adapter/provider/static.go`

```go
// StaticProfileResolver resolves aliases against an immutable in-memory
// registry loaded once at startup from config. In P1 every profile resolves
// to claude-cli: kind chat -> ChatAdapter, kind agent -> AgentAdapter (the
// engine selects the adapter by Profile.Kind).
type StaticProfileResolver struct {
    byAlias map[string]provider.Profile
}

func NewStaticProfileResolver(profiles []provider.Profile) (*StaticProfileResolver, error)
func (r *StaticProfileResolver) Resolve(alias string) (provider.Profile, error)
```

- `NewStaticProfileResolver` builds the `byAlias` map. **Duplicate alias is a construction error** (fail fast at startup, not at resolve time): return an error naming the duplicate alias. Optionally validate each `Kind` is `chat`/`agent` and reject unknown kinds.
- `Resolve` returns the profile for a hit; returns `provider.ErrUnknownProfile` (wrapped with the alias for context) on a miss.
- `var _ provider.ProfileResolver = (*StaticProfileResolver)(nil)`.

### Modify: `domain/config/config.go`

- Add a `Profiles` registry block to `Config`:
  ```go
  type Config struct {
      // ... existing fields ...
      Profiles []ProfileConfig
  }

  // ProfileConfig is the config-file shape of one registry entry. It mirrors
  // domain/provider.Profile but stays in the config package to keep config
  // free of a provider-package import; adapter/config maps it across.
  type ProfileConfig struct {
      Alias string
      Kind  string // "chat" | "agent"
      Model string
  }
  ```
- Keep `LLMConfig` as-is (retained for the claude-cli backends). No API key required for the Claude subscription path; do not remove `APIKey`.

### Modify: `adapter/config/loader.go`

- Add a default `profiles` entry to `defaults()` so the map unmarshals cleanly. Provide a minimal, useful default pair so a fresh install validates a basic workflow:
  ```go
  "profiles": []map[string]interface{}{
      {"alias": "default-chat",  "kind": "chat",  "model": ""},
      {"alias": "default-agent", "kind": "agent", "model": ""},
  },
  ```
  (Empty `model` means "use claude-cli default model" — `cfg.LLM.Model`.)
- Add a commented `profiles:` block to `defaultYAMLContent()` documenting the shape (`alias`, `kind: chat|agent`, `model`) and that aliases are referenced by `model_profile` on nodes; a config swap repoints aliases without touching `workflow.json`.
- The koanf `Unmarshal("", &cfg)` already populates `[]ProfileConfig` from the `profiles` list (koanf maps a list of maps into a slice of structs). Add an env-override path mirroring `script.envpassthrough` only if needed; for P1 the YAML/defaults path is sufficient — do NOT invent a bespoke CSV env syntax for a list-of-structs.

### Create: `adapter/config/profiles.go` (or a small mapping helper in `main.go` — see Task 6)

- Provide a mapping from `[]domainconfig.ProfileConfig` → `[]provider.Profile`:
  ```go
  func ProfilesFromConfig(cfgs []domainconfig.ProfileConfig) []provider.Profile
  ```
  Maps `Alias`/`Model` straight across and `Kind string` → `provider.ProfileKind`. Keep this in `adapter/config` (or `adapter/provider`) so `domain/config` stays free of the provider-package import. This is the seam `main.go` (Task 6) uses to build the `StaticProfileResolver`.

## Testing (strict TDD — five scenario categories)

Per design §7 (`StaticProfileResolver` bullet).

### `StaticProfileResolver`
- **Happy path:** registry with one chat + one agent profile; `Resolve("default-chat")` returns `{Alias, Kind: chat, Model}`.
- **Success:** `Resolve` returns the agent profile for an agent alias; chat vs. agent kind is distinguishable by the caller (the returned `Kind` drives adapter selection).
- **Failure:** `Resolve("nope")` returns an error that `errors.Is(err, provider.ErrUnknownProfile)`.
- **Error:** `NewStaticProfileResolver` with a duplicate alias returns a construction error naming the alias; (optionally) an unknown `Kind` value is rejected at construction.
- **Edge case:** empty registry — `NewStaticProfileResolver(nil)` succeeds and every `Resolve` returns `ErrUnknownProfile`; alias case-sensitivity is exact (`Resolve("Default-Chat")` misses).

### Config loading (`adapter/config`)
- **Happy path:** a YAML file with a `profiles:` list unmarshals into `[]ProfileConfig` with correct alias/kind/model.
- **Success:** `ProfilesFromConfig` maps `[]ProfileConfig` → `[]provider.Profile` with `Kind` string → `ProfileKind`.
- **Failure / Error:** malformed kind value (e.g. `kind: "tool"`) flows through as a `ProfileKind("tool")` and is rejected downstream by `NewStaticProfileResolver` (assert the construction error) — config loader itself does not panic.
- **Edge case:** no `profiles:` block in YAML → defaults supply `default-chat`/`default-agent`; empty `model` is preserved (resolved to claude-cli default later).

Constraint: `domain/provider` imports nothing adapter-shaped; `adapter/provider` imports `domain/provider`; `domain/config` does not import `domain/provider`. Resolver tests use plain in-memory slices (no koanf), config tests use the real loader against a temp YAML file.

## Acceptance Criteria

- `domain/provider` defines `Profile`, `ProfileKind` (`chat`/`agent`), `ProfileResolver`, and `ErrUnknownProfile`.
- `adapter/provider.StaticProfileResolver` implements `ProfileResolver`, fails fast on duplicate aliases, and returns `ErrUnknownProfile` on misses.
- `domain/config.Config` carries a `Profiles []ProfileConfig` block; `adapter/config` loads it via koanf with sensible defaults and a documented YAML example.
- A `ProfilesFromConfig` mapping seam exists for `main.go` to build the resolver.
- `go build ./...` and `go test ./domain/provider/... ./adapter/provider/... ./adapter/config/...` pass; the dependency direction stays hex-clean.
