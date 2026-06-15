package opencode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ---------------------------------------------------------------------------
// provider_id.go — mapProviderID
// ---------------------------------------------------------------------------

func TestMapProviderID(t *testing.T) {
	cases := []struct {
		arbitron string
		want     string
	}{
		{"anthropic", "anthropic"},
		{"anthropic-api", "anthropic"},
		{"openai", "openai"},
		{"openrouter", "openrouter"},
		{"ollama", "ollama"},
		{"ollama-cloud", "ollama"},
		{"bedrock", "amazon-bedrock"},
		{"amazon-bedrock", "amazon-bedrock"},
		{"azure", "azure"},
		{"azure-openai", "azure"},
		{"azure-foundry", "azure"},
	}
	for _, tc := range cases {
		t.Run(tc.arbitron, func(t *testing.T) {
			got, ok := mapProviderID(tc.arbitron)
			require.True(t, ok, "provider %q must map", tc.arbitron)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMapProviderID_CaseAndWhitespaceInsensitive(t *testing.T) {
	got, ok := mapProviderID("  Anthropic-API ")
	require.True(t, ok)
	assert.Equal(t, "anthropic", got)
}

func TestMapProviderID_UnknownFailsClosed(t *testing.T) {
	for _, p := range []string{"", "mystery", "claude-cli"} {
		_, ok := mapProviderID(p)
		assert.False(t, ok, "provider %q must NOT map (fail-closed)", p)
	}
}

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
// args.go — buildArgs
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
		{"bedrock", "anthropic.claude-3", "amazon-bedrock/anthropic.claude-3"},
		{"azure-openai", "gpt-4o", "azure/gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.prov, func(t *testing.T) {
			b := bindingForArgs(tc.prov, tc.model, nil)
			args, err := buildArgs(
				engine.AgentMission{Prompt: "do it", WorkDir: "/ws"},
				b,
				Posture{AllowAll: true},
			)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(args), 1)
			assert.Equal(t, "run", args[0], "first arg must be the run subcommand")
			assert.Contains(t, args, "do it", "prompt is the run positional")
			m, ok := argValue(args, "-m")
			require.True(t, ok, "-m flag must be present")
			assert.Equal(t, tc.wantM, m)
			// Legacy flags are gone.
			joined := strings.Join(args, " ")
			assert.NotContains(t, joined, "-p ")
			assert.NotContains(t, joined, "-c ")
		})
	}
}

// HAPPY PATH — a binding.Model already in provider/model form is not double-prefixed.
func TestBuildArgs_ModelAlreadyPrefixed(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/claude-3-5-sonnet", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	m, _ := argValue(args, "-m")
	assert.Equal(t, "anthropic/claude-3-5-sonnet", m, "must not become anthropic/anthropic/...")
}

// INPUT-VALIDATION — empty model is rejected.
func TestBuildArgs_EmptyModelRejected(t *testing.T) {
	b := bindingForArgs("anthropic", "", nil)
	_, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
}

// INPUT-VALIDATION / FAIL-CLOSED — unknown provider yields no argv (fail closed).
func TestBuildArgs_UnknownProviderFailsClosed(t *testing.T) {
	b := bindingForArgs("mystery", "m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Nil(t, args, "no argv on unknown provider")
}

// SECURITY — control chars in a provider string must be %q-escaped in the error
// message so they never reach logs as raw bytes (same property as the factory).
func TestBuildArgs_UnknownProviderControlCharsSafelyQuoted(t *testing.T) {
	b := bindingForArgs("foo\nmalicious", "m", nil)
	_, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.ErrorIs(t, err, ErrUnknownProvider)
	assert.NotContains(t, err.Error(), "\n",
		"raw newline must not appear in the error message")
	assert.Contains(t, err.Error(), `\n`,
		"escaped \\n should appear via %q-quoted provider string")
}

// SECURITY — default allow-all posture grants broad tools/permissions in the argv.
func TestBuildArgs_AllowAllEmitsBroadPermissions(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, permissionAllowAllFlag,
		"allow-all posture must emit the broad-permission flag")
}

// SECURITY — a per-profile AllowedTools allowlist measurably narrows the emitted
// tool scope: only the named tools appear, and the allow-all flag does not.
func TestBuildArgs_AllowlistNarrowsToolScope(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", []string{"Read", "Write"})
	posture := buildPosture(b, true) // config default allow-all, but profile narrows
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, posture)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag, "narrowed run must not be allow-all")
	tools, ok := argValue(args, toolScopeFlag)
	require.True(t, ok, "narrowed run must emit the tool-scope flag")
	assert.Equal(t, "Read,Write", tools)
}

// SECURITY — mission.ToolScope (the per-request scope) narrows when set, even
// without a per-profile allowlist; it takes precedence over the posture allowlist.
func TestBuildArgs_MissionToolScopeNarrows(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(
		engine.AgentMission{Prompt: "x", WorkDir: "/ws", ToolScope: []string{"Read"}},
		b,
		Posture{AllowAll: true},
	)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag)
	tools, ok := argValue(args, toolScopeFlag)
	require.True(t, ok)
	assert.Equal(t, "Read", tools)
}

// PRECEDENCE — mission.ToolScope wins over posture.AllowedTools when both are
// set. The per-request scope (knob c) always overrides the per-profile restriction
// (knob b), matching the claudecli ToolScope-over-global precedence.
func TestBuildArgs_MissionToolScopeWinsOverProfileAllowlist(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", []string{"Read"}) // profile: only Read
	posture := buildPosture(b, false)                                  // => AllowedTools=["Read"]
	args, err := buildArgs(
		engine.AgentMission{Prompt: "x", WorkDir: "/ws", ToolScope: []string{"Read", "Write", "BashTool"}},
		b,
		posture,
	)
	require.NoError(t, err)
	tools, ok := argValue(args, toolScopeFlag)
	require.True(t, ok, "tool-scope flag must be present when mission.ToolScope is set")
	// Mission scope has 3 tools; if profile won we would see only "Read".
	assert.Equal(t, "Read,Write,BashTool", tools,
		"mission.ToolScope must win over posture.AllowedTools when both are non-empty")
	assert.NotContains(t, strings.Join(args, " "), permissionAllowAllFlag)
}

// EDGE-CASE — config-disabled allow-all with no allowlist emits neither the
// allow-all flag nor a tool-scope flag (opencode's own default permissioning applies).
func TestBuildArgs_NoAllowAllNoAllowlist(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: false})
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag)
	assert.NotContains(t, joined, toolScopeFlag)
}

// SECURITY — no secret material is ever referenced by buildArgs; the argv is
// model/provider/prompt only. A binding whose RevealKey would yield a secret
// must not surface that secret in argv.
func TestBuildArgs_NoSecretsInArgv(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "do work", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY")
	assert.NotContains(t, joined, "AWS_SECRET_ACCESS_KEY")
}
