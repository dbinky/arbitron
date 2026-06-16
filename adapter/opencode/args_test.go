package opencode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ---------------------------------------------------------------------------
// posture.go — buildPosture / Posture
// ---------------------------------------------------------------------------

func bindingForArgs(prov, model string, allowed []string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider:     prov,
		Model:        model,
		AllowedTools: allowed,
	}
}

// Default posture (allowAllDefault=true, no per-profile allowlist) is allow-all.
func TestBuildPosture_DefaultAllowAll(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/claude-3-5-sonnet", nil), true)
	assert.True(t, p.AllowAll, "default posture must be allow-all")
	assert.Empty(t, p.AllowedTools, "no per-profile allowlist => empty narrowing set")
}

// Operator knob (a): config disables allow-all => posture is not allow-all.
func TestBuildPosture_ConfigDisablesAllowAll(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", nil), false)
	assert.False(t, p.AllowAll, "config knob must be able to disable allow-all")
}

// Operator knob (b): a per-profile AllowedTools allowlist narrows the tool set
// even when allow-all is the config default.
func TestBuildPosture_ProfileAllowlistNarrows(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", []string{"Read", "Write"}), true)
	assert.False(t, p.AllowAll, "a non-empty per-profile allowlist overrides allow-all")
	assert.Equal(t, []string{"Read", "Write"}, p.AllowedTools)
}

// A profile allowlist narrows even when the config knob already disabled allow-all.
func TestBuildPosture_AllowlistWithConfigDisabled(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", []string{"Read"}), false)
	assert.False(t, p.AllowAll)
	assert.Equal(t, []string{"Read"}, p.AllowedTools)
}

// ---------------------------------------------------------------------------
// args.go — buildArgs (1.17.x contract)
// ---------------------------------------------------------------------------

// argValue finds the value following the first occurrence of flag in args.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// HAPPY PATH — 1.17.x full argv contract: run <prompt> -m <provider>/<model>
// --dangerously-skip-permissions --format json. Posture flags no longer in argv.
func TestBuildArgs_1_17_Contract(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "ollama-cloud", Model: "deepseek-v4-flash"}
	mission := engine.AgentMission{Prompt: "do the thing"}
	args, err := buildArgs(mission, b, mustDesc(t, b.Provider))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"run", "do the thing",
		"-m", "ollama-cloud/deepseek-v4-flash",
		"--dangerously-skip-permissions",
		"--format", "json",
	}, args)
}

// HAPPY PATH — correct `run -m <provider>/<model>` argv per provider.
func TestBuildArgs_RunContractPerProvider(t *testing.T) {
	cases := []struct {
		prov  string
		model string
		wantM string
	}{
		{"anthropic", "claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
		{"anthropic-api", "claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
		{"openai", "gpt-4o", "openai/gpt-4o"},
		{"openrouter", "meta-llama/llama-3", "openrouter/meta-llama/llama-3"},
		{"ollama", "llama3", "ollama/llama3"},
		{"ollama-cloud", "llama3", "ollama-cloud/llama3"},
		{"bedrock", "anthropic.claude-3", "amazon-bedrock/anthropic.claude-3"},
		{"azure-openai", "gpt-4o", "azure/gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.prov, func(t *testing.T) {
			b := bindingForArgs(tc.prov, tc.model, nil)
			args, err := buildArgs(
				engine.AgentMission{Prompt: "do it", WorkDir: "/ws"},
				b,
				mustDesc(t, b.Provider),
			)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(args), 1)
			assert.Equal(t, "run", args[0], "first arg must be the run subcommand")
			assert.Contains(t, args, "do it", "prompt is the run positional")
			m, ok := argValue(args, "-m")
			require.True(t, ok, "-m flag must be present")
			assert.Equal(t, tc.wantM, m)
			// Permissions/base-url flags are NOT in argv (they ride OPENCODE_CONFIG_CONTENT).
			assert.Contains(t, args, "--dangerously-skip-permissions")
			assert.Contains(t, args, "--format")
		})
	}
}

// HAPPY PATH — a binding.Model already in provider/model form is not double-prefixed.
func TestBuildArgs_ModelAlreadyPrefixed(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/claude-3-5-sonnet", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, mustDesc(t, b.Provider))
	require.NoError(t, err)
	m, _ := argValue(args, "-m")
	assert.Equal(t, "anthropic/claude-3-5-sonnet", m, "must not become anthropic/anthropic/...")
}

// INPUT-VALIDATION — empty model is rejected.
func TestBuildArgs_EmptyModelRejected(t *testing.T) {
	b := bindingForArgs("anthropic", "", nil)
	_, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, mustDesc(t, b.Provider))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
}

// INPUT-VALIDATION / FAIL-CLOSED — empty model via new 1.17.x test name.
func TestBuildArgs_EmptyModelFailsClosed(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "ollama-cloud"}
	_, err := buildArgs(engine.AgentMission{Prompt: "x"}, b, mustDesc(t, b.Provider))
	require.ErrorIs(t, err, ErrModelRequired)
}

// NOTE: unknown-provider fail-closed for the run argv now lives at the RunAgent
// level (provider.go resolves the descriptor ONCE and threads it down), so the
// former TestBuildArgs_UnknownProvider* cases moved to provider_test.go
// (TestRunAgent_UnknownProviderFailsClosed and
// TestRunAgent_UnknownProviderControlCharsSafelyQuoted). buildArgs now takes an
// already-resolved descriptor and cannot see an unknown provider.

// SECURITY — no secret material is ever referenced by buildArgs; the argv is
// model/provider/prompt only. A binding whose RevealKey would yield a secret
// must not surface that secret in argv.
func TestBuildArgs_NoSecretsInArgv(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "do work", WorkDir: "/ws"}, b, mustDesc(t, b.Provider))
	require.NoError(t, err)
	for _, a := range args {
		assert.NotContains(t, a, "ANTHROPIC_API_KEY")
		assert.NotContains(t, a, "AWS_SECRET_ACCESS_KEY")
	}
}
