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
}

// NewCapabilityRules constructs a CapabilityRules from a per-backend map. The
// map is taken as-is; callers must not mutate it after handing it over.
func NewCapabilityRules(m map[string]BackendCapability) CapabilityRules {
	return CapabilityRules{byBackend: m}
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
