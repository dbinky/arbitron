package codex

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// argValue finds the value following the first occurrence of flag in args.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func bindingForArgs(model string) provider.ResolvedBinding {
	return provider.ResolvedBinding{Provider: "openai", Model: model}
}

// testCapRules builds a capability ruleset exercising the codex emission surface:
// codex has no context knob (default-only tier) and graded effort levels so a
// medium effort emits `-c model_reasoning_effort=medium`.
func testCapRules() provider.CapabilityRules {
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"codex": {
			Backend:      "codex",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh},
		},
	})
}

// assertContainsSeq asserts args contains a..b as an adjacent pair (a immediately
// followed by b), matching how `-c <kv>` must reach codex.
func assertContainsSeq(t *testing.T, args []string, a, b string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return
		}
	}
	t.Fatalf("expected adjacent sequence %q %q in %v", a, b, args)
}

// contains reports whether args contains v.
func contains(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

// capability — a codex binding with medium effort appends `-c
// model_reasoning_effort=medium` after the model flag; the model flag is still
// present.
func TestBuildArgs_AppendsEffort(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5", Effort: provider.EffortMedium}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.NoError(t, err)
	assertContainsSeq(t, args, "-c", "model_reasoning_effort=medium")
	assertContainsSeq(t, args, "-m", "gpt-5.5")
}

// capability (default) — a default-effort binding emits NO `-c` arg (codex picks
// its own default).
func TestBuildArgs_NoEffortWhenDefault(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5"}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.NoError(t, err)
	if contains(args, "model_reasoning_effort=default") || contains(args, "-c") {
		t.Fatalf("default effort must not emit -c: %v", args)
	}
}

// HAPPY PATH — the pinned `codex exec` argv: exec subcommand, the bypass posture,
// -m <model>, --cd <WorkDir>, --json. The PROMPT is NOT in argv (it is delivered
// via stdin in provider.go).
func TestBuildArgs_ExecContract(t *testing.T) {
	b := bindingForArgs("gpt-5-codex")
	args, err := buildArgs(engine.AgentMission{Prompt: "edit the workspace", WorkDir: "/tmp/ws"}, b)
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(args), 1)
	assert.Equal(t, execSubcommand, args[0], "first arg must be the exec subcommand")
	assert.Contains(t, args, bypassFlag, "v1 posture: bypass approvals + sandbox")
	assert.Contains(t, args, jsonFlag, "machine-readable JSON event stream requested")

	m, ok := argValue(args, modelFlag)
	require.True(t, ok, "-m flag must be present")
	assert.Equal(t, "gpt-5-codex", m)

	cd, ok := argValue(args, cdFlag)
	require.True(t, ok, "--cd flag must carry the WorkDir")
	assert.Equal(t, "/tmp/ws", cd)

	// SECURITY: the prompt must NEVER appear in argv (stdin-only delivery).
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "edit the workspace", "prompt must not reach argv")
}

// SECURITY — the verified codex-cli 0.137.0 flag spellings are pinned exactly.
// If a binary re-verification changes a spelling, this test localizes the break.
func TestBuildArgs_PinnedFlagSpellings(t *testing.T) {
	assert.Equal(t, "exec", execSubcommand)
	assert.Equal(t, "--dangerously-bypass-approvals-and-sandbox", bypassFlag)
	assert.Equal(t, "-m", modelFlag)
	assert.Equal(t, "--cd", cdFlag)
	assert.Equal(t, "--json", jsonFlag)
}

// INPUT-VALIDATION — an empty model is rejected (fail-closed); no argv.
func TestBuildArgs_EmptyModelRejected(t *testing.T) {
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, bindingForArgs(""))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Nil(t, args, "no argv on empty model")
}

// INPUT-VALIDATION — a whitespace-only model is treated as empty (trimmed),
// matching opencode's TrimSpace fail-closed.
func TestBuildArgs_WhitespaceModelRejected(t *testing.T) {
	_, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, bindingForArgs("   "))
	require.ErrorIs(t, err, ErrModelRequired)
}

// SECURITY — no secret material is ever referenced by buildArgs; the argv is
// model/working-dir/flags only. codex carries no key, but the property is pinned
// to mirror opencode's "no secrets in argv" guarantee.
func TestBuildArgs_NoSecretsInArgv(t *testing.T) {
	args, err := buildArgs(engine.AgentMission{Prompt: "do work", WorkDir: "/ws"}, bindingForArgs("gpt-5-codex"))
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "OPENAI_API_KEY")
	assert.NotContains(t, joined, "do work")
}

// capability (none→minimal) — EffortNone maps to codex's "minimal" keyword, not
// the literal string "none". The mapping lives in emitCodexEffort (domain layer)
// and is relayed verbatim through codexEffortArgs; this test verifies it survives
// the full adapter path.
func TestBuildArgs_NoneEffortYieldsMinimal(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5", Effort: provider.EffortNone}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.NoError(t, err)
	assertContainsSeq(t, args, "-c", "model_reasoning_effort=minimal")
}

// INPUT-VALIDATION — an empty model in buildArgsWithRules (not buildArgs) also
// returns ErrModelRequired. The fail-closed guarantee applies to both paths.
func TestBuildArgsWithRules_EmptyModelRejected(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "", Effort: provider.EffortMedium}
	_, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
}

// Edge: an empty Backend in the binding falls back to codexBackend ("codex")
// inside codexEffortArgs so the effort arg is emitted correctly without an
// explicit Backend on the binding.
func TestBuildArgs_EmptyBackendFallsBackToCodex(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "", Model: "gpt-5.5", Effort: provider.EffortHigh}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.NoError(t, err)
	assertContainsSeq(t, args, "-c", "model_reasoning_effort=high")
}

// Edge: extra effort args appear BEFORE --cd and --json in the final argv.
// This ordering is required by buildExecArgs design so the effort override is
// part of the model section, not appended after the path flags.
func TestBuildArgs_ExtraArgsOrderedBeforePathFlags(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5", Effort: provider.EffortMedium}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	require.NoError(t, err)

	var cdIdx, jsonIdx, cIdx int = -1, -1, -1
	for i, a := range args {
		switch a {
		case "-c":
			cIdx = i
		case "--cd":
			cdIdx = i
		case "--json":
			jsonIdx = i
		}
	}
	require.Greater(t, cIdx, 0, "-c flag must be present")
	require.Greater(t, cdIdx, 0, "--cd flag must be present")
	require.Greater(t, jsonIdx, 0, "--json flag must be present")
	assert.Less(t, cIdx, cdIdx, "-c must appear before --cd")
	assert.Less(t, cIdx, jsonIdx, "-c must appear before --json")
}

// EDGE — a model with surrounding whitespace is trimmed to the clean name in
// argv, not rejected. This documents the trim-then-check contract: callers
// feeding a config-loaded model string with accidental whitespace get the
// trimmed model name in the exec slice, not an ErrModelRequired.
func TestBuildArgs_ModelWithSurroundingWhitespaceTrimmedInArgv(t *testing.T) {
	args, err := buildArgs(engine.AgentMission{WorkDir: "/ws"}, bindingForArgs("  gpt-5-codex  "))
	require.NoError(t, err, "model with surrounding whitespace must succeed after trim")
	m, ok := argValue(args, modelFlag)
	require.True(t, ok, "-m flag must be present")
	assert.Equal(t, "gpt-5-codex", m, "trimmed model name must appear in argv, not raw input")
}
