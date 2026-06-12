package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contains reports whether s contains v. Local helper for the Emit args assertions
// (the rules() ruleset is defined in capability_test.go, same _test package).
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Happy (claude context): a 200k tier emits the proven RalphFactory env pair
// (disable-1m + 200000 auto-compact window).
func TestEmit_ClaudeCode200K(t *testing.T) {
	e, err := rules().Emit("claude-code", "opus-4.8", provider.Context200K, provider.EffortDefault)
	if err != nil {
		t.Fatal(err)
	}
	if e.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "1" || e.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "200000" {
		t.Fatalf("200k env missing: %+v", e.Env)
	}
}

// Floor: sonnet-4.6 caps effort at high, so an xhigh request emits the floored
// CLAUDE_CODE_EFFORT_LEVEL=high (write-time and runtime never disagree).
func TestEmit_ClaudeCodeEffortAndFloor(t *testing.T) {
	e, err := rules().Emit("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, "high", e.Env["CLAUDE_CODE_EFFORT_LEVEL"], "floored xhigh -> high")
}

// Boundary: effort none disables thinking via MAX_THINKING_TOKENS=0 (not an
// effort-level var).
func TestEmit_ClaudeCodeEffortNoneDisables(t *testing.T) {
	e, err := rules().Emit("claude-code", "opus-4.8", provider.ContextDefault, provider.EffortNone)
	require.NoError(t, err)
	assert.Equal(t, "0", e.Env["MAX_THINKING_TOKENS"], "none should set MAX_THINKING_TOKENS=0")
}

// Happy (codex): a graded effort emits `-c model_reasoning_effort=<v>`; none maps
// to codex's "minimal".
func TestEmit_CodexEffort(t *testing.T) {
	e, err := rules().Emit("codex", "gpt-5.5", provider.ContextDefault, provider.EffortMedium)
	require.NoError(t, err)
	assert.True(t, contains(e.Args, "-c") && contains(e.Args, "model_reasoning_effort=medium"),
		"codex effort arg missing: %v", e.Args)

	none, err := rules().Emit("codex", "gpt-5.5", provider.ContextDefault, provider.EffortNone)
	require.NoError(t, err)
	assert.True(t, contains(none.Args, "model_reasoning_effort=minimal"),
		"codex none→minimal missing: %v", none.Args)
}

// Empty: a zero-value ruleset reports Empty()==true so callers can skip emission
// when no capability policy is configured (back-compat for adapters constructed
// without rules); a non-empty ruleset reports false.
func TestCapabilityRules_Empty(t *testing.T) {
	if !(provider.CapabilityRules{}).Empty() {
		t.Fatal("zero-value CapabilityRules must be Empty")
	}
	if rules().Empty() {
		t.Fatal("a populated ruleset must not be Empty")
	}
}

// Defaults: a default tier + default effort emits nothing on either axis.
func TestEmit_DefaultsEmitNothing(t *testing.T) {
	e, err := rules().Emit("claude-code", "opus-4.8", provider.ContextDefault, provider.EffortDefault)
	require.NoError(t, err)
	assert.Empty(t, e.Env, "default tier+effort must emit nothing in Env")
	assert.Empty(t, e.Args, "default tier+effort must emit nothing in Args")
}

// Context 1m: the 1m tier raises the auto-compact window but does NOT set
// CLAUDE_CODE_DISABLE_1M_CONTEXT (unlike 200k which disables and caps it).
func TestEmit_ClaudeCode1M(t *testing.T) {
	e, err := rules().Emit("claude-code", "opus-4.8", provider.Context1M, provider.EffortDefault)
	require.NoError(t, err)
	assert.Equal(t, "1000000", e.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"], "1m should set auto-compact to 1M")
	assert.Empty(t, e.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"], "1m must NOT set DISABLE_1M_CONTEXT")
}

// Claude-cli: emits the same env vars as claude-code (both drive the `claude` CLI).
func TestEmit_ClaudeCli(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-cli": {
			Backend:      "claude-cli",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K, provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh},
		},
	})
	e, err := r.Emit("claude-cli", "any", provider.Context200K, provider.EffortHigh)
	require.NoError(t, err)
	assert.Equal(t, "1", e.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"])
	assert.Equal(t, "200000", e.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"])
	assert.Equal(t, "high", e.Env["CLAUDE_CODE_EFFORT_LEVEL"])
}

// Describe: each call returns a fresh outer map so a caller mutating the result
// cannot corrupt the rules engine's internal state (the provider_capabilities
// tool must be able to serialize this without holding a reference).
func TestDescribe_ReturnsFreshCopy(t *testing.T) {
	r := rules()
	d := r.Describe()
	// Contents are present.
	assert.Contains(t, d, "claude-code")
	assert.Contains(t, d, "codex")
	// Injecting a key into the returned map does not affect future calls.
	d["injected"] = provider.BackendCapability{Backend: "injected"}
	d2 := r.Describe()
	assert.NotContains(t, d2, "injected", "mutating the Describe result must not affect the ruleset")
}

// Error: Emit propagates ErrUnknownBackend from Validate when the backend is
// absent from the ruleset (defensive: writes are pre-validated, but the error
// path must be exercised to prevent silent regression).
func TestEmit_UnknownBackendReturnsError(t *testing.T) {
	_, err := rules().Emit("not-a-backend", "any", provider.ContextDefault, provider.EffortDefault)
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrUnknownBackend)
}

// Error: Emit propagates ErrAxisNotApplicable when a non-default context tier is
// requested on a backend that has no context knob (codex in rules() has
// context_tiers: [default] only).
func TestEmit_InapplicableAxisReturnsError(t *testing.T) {
	_, err := rules().Emit("codex", "gpt-5.5", provider.Context200K, provider.EffortDefault)
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrAxisNotApplicable)
}

// Edge: codex with default effort emits no args (the EffortDefault case in
// emitCodexEffort). Only codex medium and none were previously exercised.
func TestEmit_CodexDefaultEffortEmitsNothing(t *testing.T) {
	e, err := rules().Emit("codex", "gpt-5.5", provider.ContextDefault, provider.EffortDefault)
	require.NoError(t, err)
	assert.Nil(t, e.Env, "codex default must produce nil Env")
	assert.Empty(t, e.Args, "codex default effort must emit no args")
}

// Pass 2 parity: the write-time and runtime invariant — Validate and Emit called
// with the same input must agree on the effective floored effort. This test
// observes the agreement directly so a future refactor that decouples the two
// (breaking the invariant) produces a failing test, not a silent divergence.
func TestEmit_ValidateAndEmitAgreeOnFlooredEffort(t *testing.T) {
	// sonnet-4.6: xhigh is floored to high by the explicit effort_floor map.
	_, effEffort, err := rules().Validate("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	require.Equal(t, provider.EffortHigh, effEffort, "Validate must floor xhigh -> high")

	e, err := rules().Emit("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	require.NoError(t, err)
	assert.Equal(t, string(effEffort), e.Env["CLAUDE_CODE_EFFORT_LEVEL"],
		"Emit must emit exactly the effective effort Validate returned")
}

// Opencode (and unknown backends): emits nothing for both axes (deferred).
func TestEmit_UnknownBackendEmitsNothing(t *testing.T) {
	r := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"opencode": {
			Backend:      "opencode",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault},
		},
	})
	e, err := r.Emit("opencode", "any", provider.ContextDefault, provider.EffortDefault)
	require.NoError(t, err)
	assert.Empty(t, e.Env)
	assert.Empty(t, e.Args)
}
