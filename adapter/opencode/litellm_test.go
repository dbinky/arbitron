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

// happy — a litellm binding routes via -m litellm/<model> (1.17.x: litellm has
// its own opencode provider block, not the openai shim).
func TestBuildArgs_LiteLLMRoutesViaLiteLLM(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "litellm", Model: "gpt-4o"}
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(args, " "), "-m litellm/gpt-4o")
}

// happy — a litellm binding's env carries OPENAI_API_KEY but NO base-URL var.
// opencode 1.17.x ignores *_BASE_URL on its routing path; the base URL will
// be delivered via a generated OPENCODE_CONFIG_CONTENT JSON.
func TestOpencodeEnv_LiteLLMUsesOpenAIVars(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "litellm",
		BaseURL:   "https://litellm:4000",
		RevealKey: func(context.Context) (string, error) { return "sk", nil },
	}
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)
	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "OPENAI_API_KEY=sk")
	assert.NotContains(t, joined, "OPENAI_BASE_URL=", "base URL must not be in env on 1.17.x")
}

// boundary — a litellm model already in litellm/<model> form is not double-prefixed.
func TestBuildArgs_LiteLLMModelAlreadyPrefixed(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "litellm", Model: "litellm/gpt-4o"}
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	j := strings.Join(args, " ")
	assert.Contains(t, j, "-m litellm/gpt-4o")
	assert.NotContains(t, j, "litellm/litellm/")
}
