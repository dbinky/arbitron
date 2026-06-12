package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
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
	e, _ := rules().Emit("claude-code", "sonnet-4.6", provider.ContextDefault, provider.EffortXHigh)
	if e.Env["CLAUDE_CODE_EFFORT_LEVEL"] != "high" { // floored
		t.Fatalf("expected floored high, got %q", e.Env["CLAUDE_CODE_EFFORT_LEVEL"])
	}
}

// Boundary: effort none disables thinking via MAX_THINKING_TOKENS=0 (not an
// effort-level var).
func TestEmit_ClaudeCodeEffortNoneDisables(t *testing.T) {
	e, _ := rules().Emit("claude-code", "opus-4.8", provider.ContextDefault, provider.EffortNone)
	if e.Env["MAX_THINKING_TOKENS"] != "0" {
		t.Fatalf("none should disable: %+v", e.Env)
	}
}

// Happy (codex): a graded effort emits `-c model_reasoning_effort=<v>`; none maps
// to codex's "minimal".
func TestEmit_CodexEffort(t *testing.T) {
	e, _ := rules().Emit("codex", "gpt-5.5", provider.ContextDefault, provider.EffortMedium)
	if !contains(e.Args, "-c") || !contains(e.Args, "model_reasoning_effort=medium") {
		t.Fatalf("codex effort arg missing: %+v", e.Args)
	}
	none, _ := rules().Emit("codex", "gpt-5.5", provider.ContextDefault, provider.EffortNone)
	if !contains(none.Args, "model_reasoning_effort=minimal") {
		t.Fatalf("codex none→minimal missing: %+v", none.Args)
	}
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
	e, _ := rules().Emit("claude-code", "opus-4.8", provider.ContextDefault, provider.EffortDefault)
	if len(e.Env) != 0 || len(e.Args) != 0 {
		t.Fatalf("default should emit nothing: %+v", e)
	}
}
