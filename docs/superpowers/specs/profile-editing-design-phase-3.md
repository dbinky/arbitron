# Design — Phase 3: Runtime Emission Across Backends

**Feature:** profile-editing · **Phase:** 3 of 4
**Spec:** `docs/specs/profile-editing-spec.md` (§3 "take effect at runtime", §10.4)
**Depends on:** Phases 1 (mapping) + 2 (binding carries the axes) · **Blocks:** nothing

## Purpose

Make `ContextWindow` and `Effort` on a `ResolvedBinding` actually change what the
backend subprocess requests — for **claude-code** and **codex**. Emission always
goes through Phase-1 `CapabilityRules` so the effective value is deterministic
(not reliant on a CLI's silent floor). opencode carries the fields but emits
nothing yet (deferred).

## Mapping seam

A small per-backend **emitter** consults `CapabilityRules` to turn the binding's
requested `(model, tier, effort)` into native arguments/env. The rules are
injected once at startup and handed to each adapter (or to a shared helper the
adapters call). Mapping is computed against the **effective** value
(`Validate` from Phase 1) so e.g. `xhigh` on Sonnet 4.6 emits `high`.

```go
// domain/provider (or a thin adapter helper)
type Emission struct {
    Env  map[string]string // backend env additions
    Args []string          // backend argv additions
}
func (r CapabilityRules) Emit(backend, model string, tier ContextWindow, effort Effort) (Emission, error)
```

### claude-code / claude-cli mapping
- **Effort** → env `CLAUDE_CODE_EFFORT_LEVEL=<effective>` for
  `low|medium|high|xhigh|max`; `none` → `MAX_THINKING_TOKENS=0` (disable);
  `default` → emit nothing. (Env chosen over the `--effort` flag because env has
  highest precedence and the existing claudecli env seam already injects per-call
  vars; both chat and agent paths share it.)
- **Context** → `200k`: env `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` +
  `CLAUDE_CODE_AUTO_COMPACT_WINDOW=200000` (the proven RalphFactory pair);
  `1m`: emit nothing (default reaches the model max) or
  `CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000`; `default`: emit nothing.

### codex mapping
- **Effort** → argv `-c model_reasoning_effort=<effective>` for
  `low|medium|high|xhigh`; `none` → `-c model_reasoning_effort=minimal`;
  `default` → emit nothing.
- **Context** → inapplicable (already rejected at write in Phase 4; runtime emits
  nothing).

## Wiring points (exact seams)

### claude-code (`adapter/claudecli`)
- `env.go:agentEnv(ctx, binding)` currently returns `procenv.CredentialEnv(...)`.
  Append the Phase-1 `Emit(...).Env` entries to that slice (after credentials).
  The chat path's env builder gets the same additions (shared helper
  `applyCapabilityEnv(base, binding, rules)` so chat + agent cannot drift).
- `claude.go:buildArgs` is left as-is (effort/context go through env, not flags),
  keeping the request-vs-policy split intact.
- The adapter struct gains a `rules provider.CapabilityRules` field, set at
  construction in `initDomain`.

### codex (`adapter/codex`)
- `args.go:buildArgs(mission, binding)` calls
  `buildExecArgs(binding.Model, mission.WorkDir)`. Extend `buildExecArgs` to also
  take the effort emission and append `-c model_reasoning_effort=<v>` after the
  model flag. The streaming path (`stream_adapter.go`) that also calls
  `buildExecArgs` passes the same emission from its binding. Effort is sourced
  from `binding.Effort` via `rules.Emit`.
- Keep `ErrModelRequired` fail-closed behavior unchanged.

### Effective-backend precedence
The agent factory already computes the effective backend (`node > profile >
deployment default`) and overwrites `ResolvedBinding.Backend` before dispatch.
Emission uses that **effective backend** + the binding's `Model`/`ContextWindow`/
`Effort`. (Node-level override of context/effort is a non-goal — §9 — so only the
profile's values flow here.)

## Security / correctness
- New env vars are non-secret; they ride the same per-call env slice as the
  credential vars and are never logged with values that matter (safe to log keys
  like `CLAUDE_CODE_EFFORT_LEVEL=high`).
- Emission is pure/deterministic given the ruleset; no I/O.
- Unknown/disabled combos can't reach here: Phase 4 rejects inapplicable axes at
  write time, so `Emit` only ever sees storable values; an unexpected value
  still fails closed (returns an error the executor surfaces, never a silent
  wrong window).

## Testing (TDD — all 5 categories)
- **claudecli env (extend `env_test.go`):** `200k` → both CLAUDE_CODE_* vars
  present; `1m`/`default` → absent; effort `high` → `CLAUDE_CODE_EFFORT_LEVEL=high`;
  `none` → `MAX_THINKING_TOKENS=0`; `default` → no effort var. Credentials still
  injected (no regression).
- **codex args (extend `args_test.go`):** effort `medium` → argv contains
  `-c model_reasoning_effort=medium`; `default` → no `-c`; `none` → `minimal`;
  model still required.
- **Floor:** Sonnet-4.6 + `xhigh` → emitted `high` (via ruleset).
- **Chat parity:** claudecli chat path emits the same env as agent for the same
  binding.
- **Deferred backend:** opencode binding with non-default axes emits nothing
  (and Phase-4 rejects setting them, so this is belt-and-suspenders).

## Out of scope
opencode/ollama (DeepSeek) `think` emission and per-node override.
