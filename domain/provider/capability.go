package provider

import (
	"errors"
	"fmt"
)

// ErrAxisNotApplicable is returned by Validate when a non-default axis value is
// requested on a backend/model that exposes no control for that axis (e.g. a
// non-default context tier on a backend with no context knob). The write is
// rejected. Callers match it with errors.Is.
var ErrAxisNotApplicable = errors.New("provider: axis not applicable to backend")

// ErrUnknownBackend is returned by Validate when the backend is absent from the
// capability ruleset. Callers match it with errors.Is.
var ErrUnknownBackend = errors.New("provider: unknown backend in capability rules")

// ModelCapability narrows or maps a backend's axes for a specific model id.
// A nil ContextTiers/EffortLevels means "inherit the backend's list"; a
// non-nil list fully replaces it for this model. EffortFloor maps an
// unsupported-but-requested level to the model's ceiling (e.g. xhigh -> high);
// when absent, Validate falls back to flooring to the highest allowed level
// at or below the request.
type ModelCapability struct {
	ContextTiers []ContextWindow
	EffortLevels []Effort
	EffortFloor  map[Effort]Effort
}

// BackendCapability is the per-backend allowlist of axis values, with optional
// per-model overrides. Absence of a non-default context tier means the context
// axis is inapplicable to that backend (a non-default request is rejected).
type BackendCapability struct {
	Backend        string
	ContextTiers   []ContextWindow
	EffortLevels   []Effort
	ModelOverrides map[string]ModelCapability
}

// CapabilityRules is the single source of truth for which (tier, effort) values
// are valid on a backend/model and how an unsupported effort floors to that
// backend/model's ceiling. It is immutable after construction (loaded once at
// startup).
type CapabilityRules struct {
	byBackend map[string]BackendCapability
	// lunaroute is the curated per-model table (lunaroute_capability.go).
	lunaroute map[string]LunaRouteModel
}

// NewCapabilityRules constructs a CapabilityRules from a per-backend map. The
// map is taken as-is; callers must not mutate it after handing it over.
func NewCapabilityRules(m map[string]BackendCapability) CapabilityRules {
	return CapabilityRules{byBackend: m}
}

// Empty reports whether the ruleset carries no backends (the zero value). Adapters
// constructed without a capability ruleset (back-compat / tests) use this to skip
// emission entirely rather than treating every backend as unknown — a configured
// ruleset always has at least one backend, so Empty is the unambiguous "no policy"
// signal.
func (r CapabilityRules) Empty() bool {
	return len(r.byBackend) == 0
}

// Describe returns a read-only copy of the per-backend capability map so a
// caller (the provider_capabilities tool) can serialize the allowed tiers/effort
// per backend/model without holding a reference to the ruleset's internal map.
// The outer map is freshly allocated; the BackendCapability values are copied by
// value (their slices/maps are not mutated by callers, only read).
func (r CapabilityRules) Describe() map[string]BackendCapability {
	out := make(map[string]BackendCapability, len(r.byBackend))
	for k, v := range r.byBackend {
		out[k] = v
	}
	return out
}

// Has reports whether the ruleset carries a capability entry for backend. It lets
// a caller (ProfileService) detect the "no agent-backend ruleset applies" case —
// e.g. the empty, connection-supplied backend of a chat profile — and branch to
// the chat-tier axis policy instead of provoking Validate's ErrUnknownBackend.
func (r CapabilityRules) Has(backend string) bool {
	_, ok := r.byBackend[backend]
	return ok
}

// containsCtx reports whether s contains v.
func containsCtx(s []ContextWindow, v ContextWindow) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// containsEff reports whether s contains v.
func containsEff(s []Effort, v Effort) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// caps returns the effective context tiers, effort levels, and effort-floor map
// for a backend+model. A present model override wins over the backend list
// per-axis (a non-nil override list replaces; a nil one inherits). ok is false
// when the backend is unknown.
func (r CapabilityRules) caps(backend, model string) (tiers []ContextWindow, levels []Effort, floor map[Effort]Effort, ok bool) {
	bc, ok := r.byBackend[backend]
	if !ok {
		return nil, nil, nil, false
	}
	tiers, levels = bc.ContextTiers, bc.EffortLevels
	if mc, has := bc.ModelOverrides[model]; has {
		if mc.ContextTiers != nil {
			tiers = mc.ContextTiers
		}
		if mc.EffortLevels != nil {
			levels = mc.EffortLevels
		}
		floor = mc.EffortFloor
	}
	return tiers, levels, floor, true
}

// Validate resolves a requested (tier, effort) for a backend+model and returns
// the effective (possibly floored) values:
//   - An invalid tier/effort string returns ErrInvalidContextWindow /
//     ErrInvalidEffort.
//   - An unknown backend returns ErrUnknownBackend.
//   - A non-default tier the backend/model does not allow returns
//     ErrAxisNotApplicable.
//   - default/none effort always pass through unchanged; a graded effort that is
//     unsupported is floored via the explicit EffortFloor map or, failing that,
//     to the highest allowed graded level at or below the request. If no graded
//     level is available at all, the effort axis is inapplicable
//     (ErrAxisNotApplicable).
func (r CapabilityRules) Validate(backend, model string, tier ContextWindow, effort Effort) (ContextWindow, Effort, error) {
	if !tier.Valid() {
		return "", "", ErrInvalidContextWindow
	}
	if !effort.Valid() {
		return "", "", ErrInvalidEffort
	}
	tiers, levels, floor, ok := r.caps(backend, model)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrUnknownBackend, backend)
	}
	// Context: default always passes; a non-default tier must be allowed.
	if tier != ContextDefault && !containsCtx(tiers, tier) {
		return "", "", fmt.Errorf("%w: context %q on backend %q", ErrAxisNotApplicable, tier, backend)
	}
	// Effort: default/none pass through; a graded level must be allowed or floored.
	effEffort := effort
	if effort != EffortDefault && effort != EffortNone && !containsEff(levels, effort) {
		if f, has := floor[effort]; has {
			effEffort = f
		} else {
			effEffort = floorToHighest(levels, effort)
			if effEffort == "" {
				return "", "", fmt.Errorf("%w: effort %q on backend %q", ErrAxisNotApplicable, effort, backend)
			}
		}
	}
	return tier, effEffort, nil
}

// Emission is the native runtime artifact derived from a (validated) requested
// tier+effort: env additions (claude-code) and/or argv additions (codex). It is
// the runtime counterpart of Validate's write-time check — a single deterministic
// mapping so the value emitted at launch is the SAME effective value the write
// validated. A zero Emission (nil Env, nil Args) means "emit nothing".
type Emission struct {
	Env  map[string]string // backend env additions (e.g. CLAUDE_CODE_EFFORT_LEVEL)
	Args []string          // backend argv additions (e.g. -c model_reasoning_effort=medium)
}

// Emit turns a (validated) requested tier+effort into native env/argv for the
// backend. It floors effort via the same rules as Validate, so write-time and
// runtime never disagree. Defaults/unknown axes emit nothing for that axis.
//
// The backend is the EFFECTIVE backend already resolved onto the binding (the
// executor overwrites ResolvedBinding.Backend with the effective node>profile>
// deployment value before dispatch). claude-cli and claude-code share one
// emission (both drive the `claude` CLI); codex emits its `-c` arg; opencode and
// any other backend are deferred and emit nothing.
func (r CapabilityRules) Emit(backend, model string, tier ContextWindow, effort Effort) (Emission, error) {
	// Reuse Validate to floor effort + reject inapplicable (defensive; writes are
	// pre-validated). default/none for effort and default for tier never error.
	effTier, effEffort, err := r.Validate(backend, model, tier, effort)
	if err != nil {
		return Emission{}, err
	}
	out := Emission{Env: map[string]string{}}
	switch backend {
	case "claude-code", "claude-cli":
		emitClaudeContext(&out, effTier)
		emitClaudeEffort(&out, effEffort)
	case "codex":
		emitCodexEffort(&out, effEffort)
	default:
		// opencode and others: deferred — emit nothing.
	}
	if len(out.Env) == 0 {
		out.Env = nil
	}
	return out, nil
}

// emitClaudeContext maps a context tier to the claude-code env pair. 200k pins the
// standard window via the proven RalphFactory pair; 1m raises the auto-compact
// window; default emits nothing.
func emitClaudeContext(out *Emission, tier ContextWindow) {
	switch tier {
	case Context200K:
		out.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] = "1"
		out.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = "200000"
	case Context1M:
		out.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = "1000000"
	case ContextDefault:
		// emit nothing
	}
}

// emitClaudeEffort maps an effective effort to the claude-code env. default emits
// nothing; none disables thinking (MAX_THINKING_TOKENS=0); a graded level sets
// CLAUDE_CODE_EFFORT_LEVEL to that level verbatim.
func emitClaudeEffort(out *Emission, e Effort) {
	switch e {
	case EffortDefault:
		// nothing
	case EffortNone:
		out.Env["MAX_THINKING_TOKENS"] = "0"
	default:
		out.Env["CLAUDE_CODE_EFFORT_LEVEL"] = string(e)
	}
}

// emitCodexEffort maps an effective effort to the codex `-c model_reasoning_effort`
// argv pair. default emits nothing; none maps to codex's "minimal"; a graded level
// is passed verbatim.
func emitCodexEffort(out *Emission, e Effort) {
	switch e {
	case EffortDefault:
		// nothing
	case EffortNone:
		out.Args = append(out.Args, "-c", "model_reasoning_effort=minimal")
	default:
		out.Args = append(out.Args, "-c", "model_reasoning_effort="+string(e))
	}
}

// floorToHighest returns the highest allowed graded level at or below want, or
// "" when no graded level qualifies. EffortDefault and EffortNone are skipped
// because they sit outside the graded order.
func floorToHighest(levels []Effort, want Effort) Effort {
	best := Effort("")
	for _, l := range levels {
		if l == EffortDefault || l == EffortNone {
			continue
		}
		if !l.GreaterThan(want) && (best == "" || l.GreaterThan(best)) {
			best = l
		}
	}
	return best
}
