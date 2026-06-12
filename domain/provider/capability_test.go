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
