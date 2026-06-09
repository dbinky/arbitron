package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// happy — litellm maps to the opencode openai provider id.
func TestMapProviderID_LiteLLMMapsToOpenAI(t *testing.T) {
	got, ok := mapProviderID("litellm")
	require.True(t, ok, "litellm must map")
	assert.Equal(t, "openai", got)
}

// happy — a litellm binding routes via -m openai/<model>.
func TestBuildArgs_LiteLLMRoutesViaOpenAI(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "litellm", Model: "gpt-4o"}
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(args, " "), "-m openai/gpt-4o")
}

// happy — a litellm binding's env carries OPENAI_BASE_URL + OPENAI_API_KEY.
func TestOpencodeEnv_LiteLLMUsesOpenAIVars(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "litellm",
		BaseURL:   "https://litellm:4000",
		RevealKey: func(context.Context) (string, error) { return "sk", nil },
	}
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)
	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "OPENAI_BASE_URL=https://litellm:4000")
	assert.Contains(t, joined, "OPENAI_API_KEY=sk")
}

// boundary — a litellm model already in openai/<model> form is not double-prefixed.
func TestBuildArgs_LiteLLMModelAlreadyPrefixed(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "litellm", Model: "openai/gpt-4o"}
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	j := strings.Join(args, " ")
	assert.Contains(t, j, "-m openai/gpt-4o")
	assert.NotContains(t, j, "openai/openai/")
}

// regression — claude-cli still does not map (fail-closed; never routes through
// opencode).
func TestMapProviderID_ClaudeCLIStillUnmapped(t *testing.T) {
	_, ok := mapProviderID("claude-cli")
	assert.False(t, ok)
}
