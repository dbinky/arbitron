# Design — Phase 1: Capability Ruleset (foundation)

**Feature:** profile-editing · **Phase:** 1 of 4
**Spec:** `docs/specs/profile-editing-spec.md` (§4 axes, §5 validity ruleset)
**Depends on:** nothing · **Blocks:** Phases 2, 3, 4

## Purpose

Introduce the two new profile axes as domain enums and a **capability ruleset**
that is the single source of truth for (a) what is *valid* on a given
backend/model at write time and (b) how a requested value *maps to native
emission* at runtime. Pure domain logic plus a startup loader. No DB, no MCP, no
runtime wiring yet.

## New domain types (`domain/provider`)

### Enums
```go
// context_window.go
type ContextWindow string
const (
    ContextDefault ContextWindow = "default" // emit nothing; backend/model default
    Context200K    ContextWindow = "200k"    // pin standard window
    Context1M      ContextWindow = "1m"      // allow metered extended window
)

// effort.go
type Effort string
const (
    EffortDefault Effort = "default" // emit nothing
    EffortNone    Effort = "none"    // explicitly disable reasoning/thinking
    EffortLow     Effort = "low"
    EffortMedium  Effort = "medium"
    EffortHigh    Effort = "high"
    EffortXHigh   Effort = "xhigh"
    EffortMax     Effort = "max"
)
```
Each enum has a `Valid()` membership check and parses from string (reject unknown
values with a sentinel). Ordering of effort is encoded (for floor logic):
`none < low < medium < high < xhigh < max` (`default` is "unset", outside the
order).

### Capability ruleset
```go
// capability.go
type BackendCapability struct {
    Backend         string          // "claude-code" | "codex" | "opencode" | "claude-cli"
    ContextTiers    []ContextWindow // allowed tiers; absence of non-default => axis inapplicable
    EffortLevels    []Effort        // allowed user-facing levels for this backend
    // ModelOverrides narrows/maps per model id (e.g. sonnet-4.6 caps effort at high).
    ModelOverrides  map[string]ModelCapability
}
type ModelCapability struct {
    ContextTiers []ContextWindow
    EffortLevels []Effort
    // EffortFloor maps an unsupported-but-requested level to the model's ceiling
    // (e.g. xhigh -> high). Optional; default is "floor to highest <= requested".
    EffortFloor  map[Effort]Effort
}

type CapabilityRules struct { /* byBackend map, immutable after load */ }
```

### Core operation
```go
// Validate resolves a requested (tier, effort) for a backend+model.
//   - Inapplicable axis (e.g. a non-default tier on a backend with no context
//     control) => ("", "", ErrAxisNotApplicable) — the write is rejected.
//   - Supported axis but unsupported *level* => stored value is returned as the
//     requested value, effective* is the floored value (e.g. xhigh->high).
//   - Unknown backend => ErrUnknownBackend.
func (r CapabilityRules) Validate(backend, model string, tier ContextWindow, effort Effort)
    (effectiveTier ContextWindow, effectiveEffort Effort, err error)
```
Sentinels: `ErrAxisNotApplicable`, `ErrUnknownBackend`, `ErrInvalidContextWindow`,
`ErrInvalidEffort`. `default`/`none` are always valid inputs (they map to
"emit nothing" / "disable"); only *non-default* values are checked for
applicability.

> Companion method: Phase 3 adds `CapabilityRules.Emit(backend, model, tier,
> effort) → Emission` to this same rules engine — the runtime counterpart of
> `Validate` that turns the (already-validated, floored) values into native env/
> argv. It is documented in Phase 3 because that is where it is first consumed,
> but it is part of this Phase-1 component conceptually and shares its per-backend/
> per-model tables. Keeping both on `CapabilityRules` guarantees write-time
> validation and runtime emission can never disagree.

## Loader (`adapter/config`)

- **Embedded default** ruleset compiled in (`//go:embed capabilities.default.yaml`)
  so a fresh install has working rules with zero config.
- **Optional override**: if `~/.arbitron/capabilities.yaml` (path from config)
  exists, it is parsed and **replaces** the embedded entry per backend (a present
  backend key fully overrides; absent keys keep the embedded default). Loaded
  **once at startup** — deliberately no hot reload (slow-moving).
- `LoadCapabilityRules(path string) (provider.CapabilityRules, error)` — parse +
  validate the file (unknown enum value => fail fast with a clear message naming
  the offending backend/model/field).

### Embedded default content (initial roster, from research)
```yaml
backends:
  claude-code:
    context_tiers: [default, 200k, 1m]
    effort_levels: [default, none, low, medium, high, xhigh, max]
    model_overrides:
      # Sonnet 4.6 has no xhigh; floor it to high.
      "sonnet-4.6": { effort_levels: [default, none, low, medium, high, max],
                      effort_floor: { xhigh: high } }
  claude-cli:        # chat subscription path — same axes as claude-code
    context_tiers: [default, 200k, 1m]
    effort_levels: [default, none, low, medium, high, xhigh, max]
  codex:
    context_tiers: [default]            # no context knob => non-default rejected
    effort_levels: [default, none, low, medium, high, xhigh]  # codex floor: none->minimal handled at emit
  opencode:
    context_tiers: [default]
    effort_levels: [default]            # deferred: accepts only default until Phase-3 follow-up
```
(Model ids are matched flexibly — exact id or a normalized alias; the matching
rule is defined in `capability.go` and unit-tested.)

## Testing (TDD — all 5 scenario categories)

Pure-unit matrix over `Validate`:
- **Happy:** `(claude-code, opus-4.8, 1m, high)` → `(1m, high, nil)`.
- **Floor:** `(claude-code, sonnet-4.6, default, xhigh)` → `(default, high, nil)`.
- **Inapplicable:** `(codex, gpt-5.5, 200k, medium)` → `ErrAxisNotApplicable`.
- **Invalid input:** unknown effort string → `ErrInvalidEffort`.
- **Unknown backend/model:** `ErrUnknownBackend`; unknown model falls back to
  backend-level rules (not an error).
- Loader: embedded default parses; override merges per-backend; malformed file
  fails fast.

## Out of scope (this phase)
DB, MCP tools, resolver changes, runtime emission. Phase 1 ships the vocabulary
and the rules engine that every later phase consumes.
