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
