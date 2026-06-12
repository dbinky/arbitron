package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rules builds a small capability ruleset exercising the full surface: a backend
// with context tiers, effort levels, and a per-model override (sonnet-4.6 caps
// effort at high with an explicit xhigh->high floor); plus a context-less codex.
func rules() provider.CapabilityRules {
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K, provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax},
			ModelOverrides: map[string]provider.ModelCapability{
				"sonnet-4.6": {
					EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortMax},
					EffortFloor:  map[provider.Effort]provider.Effort{provider.EffortXHigh: provider.EffortHigh},
				},
			},
		},
		"codex": {
			Backend:      "codex",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh},
		},
	})
}

// Happy: a fully-supported (tier, effort) on a known backend (unknown model
// falls back to backend rules) returns the requested values unchanged.
func TestValidate_Happy(t *testing.T) {
	tier, eff, err := rules().Validate("claude-code", "opus-4.8", provider.Context1M, provider.EffortHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.Context1M, tier)
	assert.Equal(t, provider.EffortHigh, eff)
}

// Floor (explicit map): sonnet-4.6 has no xhigh, so an explicit effort_floor maps
// xhigh -> high.
func TestValidate_FloorEffort(t *testing.T) {
	_, eff, err := rules().Validate("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff)
}

// Floor (highest <= requested): a backend that allows up to high with no explicit
// floor map floors xhigh down to high automatically.
func TestValidate_FloorToHighest(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"capped": {
			Backend:      "capped",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh},
		},
	})
	_, eff, err := r.Validate("capped", "any", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortHigh, eff)
}

// Inapplicable axis: codex has no context knob, so a non-default tier is rejected
// with ErrAxisNotApplicable.
func TestValidate_InapplicableContext(t *testing.T) {
	_, _, err := rules().Validate("codex", "gpt-5.5", provider.Context200K, provider.EffortMedium)
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
}

// Invalid input: a malformed effort value is rejected with ErrInvalidEffort
// before any backend lookup.
func TestValidate_InvalidEffort(t *testing.T) {
	_, _, err := rules().Validate("claude-code", "opus-4.8", provider.ContextDefault, provider.Effort("ultra"))
	assert.ErrorIs(t, err, provider.ErrInvalidEffort)
}

// Unknown backend: a backend absent from the ruleset returns ErrUnknownBackend.
func TestValidate_UnknownBackend(t *testing.T) {
	_, _, err := rules().Validate("mystery", "x", provider.ContextDefault, provider.EffortDefault)
	assert.ErrorIs(t, err, provider.ErrUnknownBackend)
}

// Invalid context window: a malformed tier string is rejected with
// ErrInvalidContextWindow before any backend lookup (mirrors TestValidate_InvalidEffort).
func TestValidate_InvalidContextWindow(t *testing.T) {
	_, _, err := rules().Validate("claude-code", "opus-4.8", provider.ContextWindow("4m"), provider.EffortDefault)
	assert.ErrorIs(t, err, provider.ErrInvalidContextWindow)
}

// Effort inapplicable: when a backend's effort_levels list contains only
// default/none (no graded levels), any graded effort request must return
// ErrAxisNotApplicable — the fail-closed path via floorToHighest returning "".
func TestValidate_EffortInapplicable(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"opencode-like": {
			Backend:      "opencode-like",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault}, // no graded levels
		},
	})
	_, _, err := r.Validate("opencode-like", "any", provider.ContextDefault, provider.EffortMedium)
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
}

// None always passes through: EffortNone is special-cased to bypass the level
// check (it explicitly disables reasoning, so it is always acceptable on any
// backend regardless of what effort_levels contains).
func TestValidate_NonePassthrough(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"no-none": {
			Backend:      "no-none",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortLow}, // no EffortNone listed
		},
	})
	// EffortNone should still pass through (bypass level check) even if not listed.
	_, eff, err := r.Validate("no-none", "any", provider.ContextDefault, provider.EffortNone)
	require.NoError(t, err)
	assert.Equal(t, provider.EffortNone, eff)
}

// none/default are UNIVERSAL: they validate on every backend regardless of
// whether the backend's effort_levels lists them — the list governs only the
// GRADED levels (low..max). The YAML therefore does not (and must not) list
// `none` per-backend; the code handles none/default in the short-circuit before
// the allowlist check. This table pins that contract across the real backend
// shapes, including the opencode shape (effort_levels: [default] only) where
// `none` validates clean and Emit produces nothing — a documented DELIBERATE
// no-op, not a silent accept-then-ignore.
func TestValidate_NoneAndDefaultUniversalAcrossBackends(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		// Full-axis claude shape: lists graded levels, never `none`.
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K, provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax},
		},
		// codex shape: graded up to xhigh, no `none` in the list.
		"codex": {
			Backend:      "codex",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh},
		},
		// opencode shape: ONLY default; no graded levels and no `none` listed.
		"opencode": {
			Backend:      "opencode",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault},
		},
	})

	backends := []string{"claude-code", "codex", "opencode"}
	for _, backend := range backends {
		for _, eff := range []provider.Effort{provider.EffortNone, provider.EffortDefault} {
			t.Run(backend+"/"+string(eff), func(t *testing.T) {
				_, got, err := r.Validate(backend, "any-model", provider.ContextDefault, eff)
				require.NoError(t, err, "%s/%s must validate without a YAML effort_levels entry", backend, eff)
				assert.Equal(t, eff, got, "none/default pass through unchanged")
			})
		}
	}

	// Documented no-op: `none` on opencode validates, but Emit produces nothing
	// (opencode is deferred), so the knob is a deliberate no-op rather than a
	// silent accept-then-ignore of a graded value.
	em, err := r.Emit("opencode", "any-model", provider.ContextDefault, provider.EffortNone)
	require.NoError(t, err)
	assert.Nil(t, em.Env, "opencode none emits no env")
	assert.Nil(t, em.Args, "opencode none emits no args")
}

// Unknown model: a model not in ModelOverrides falls back to backend rules; it
// is not an error.
func TestValidate_UnknownModel_FallsBackToBackend(t *testing.T) {
	// "unknown-model" is not in any ModelOverrides, so backend-level rules apply.
	tier, eff, err := rules().Validate("claude-code", "unknown-model", provider.Context200K, provider.EffortHigh)
	require.NoError(t, err)
	assert.Equal(t, provider.Context200K, tier)
	assert.Equal(t, provider.EffortHigh, eff)
}
