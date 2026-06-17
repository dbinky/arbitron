package opencode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

func decodeConfig(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestBuildConfig_OllamaCloudScoped(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "ollama-cloud", Model: "deepseek-v4-flash", BaseURL: "https://ollama.com/v1"}
	post := Posture{AllowedTools: []string{"Read", "Write", "Edit", "Grep", "Glob", "Bash"}}
	out, err := buildConfig(b, post)
	require.NoError(t, err)
	m := decodeConfig(t, out)
	assert.Equal(t, "https://opencode.ai/config.json", m["$schema"])
	_, hasProvider := m["provider"]
	assert.False(t, hasProvider, "built-in ollama-cloud needs no provider block")
	perm := m["permission"].(map[string]any)
	assert.Equal(t, "deny", perm["*"])
	for _, tool := range []string{"read", "write", "edit", "grep", "glob", "bash"} {
		assert.Equal(t, "allow", perm[tool], "tool %s allowed", tool)
	}
}

func TestBuildConfig_LiteLLMEmitsProviderBlock(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "litellm", Model: "gpt-4o", BaseURL: "https://gw.example/v1"}
	out, err := buildConfig(b, Posture{AllowAll: true})
	require.NoError(t, err)
	m := decodeConfig(t, out)
	prov := m["provider"].(map[string]any)["litellm"].(map[string]any)
	assert.Equal(t, "@ai-sdk/openai-compatible", prov["npm"])
	opts := prov["options"].(map[string]any)
	assert.Equal(t, "https://gw.example/v1", opts["baseURL"])
	assert.Equal(t, "{env:OPENAI_API_KEY}", opts["apiKey"])
	assert.Equal(t, "allow", m["permission"].(map[string]any)["*"])
	assert.NotContains(t, out, "sentinel")
}

func TestBuildConfig_OpenAICustomBaseURL(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "openai", Model: "gpt-4o", BaseURL: "https://org-gw.example/v1"}
	out, err := buildConfig(b, Posture{AllowAll: true})
	require.NoError(t, err)
	m := decodeConfig(t, out)
	opts := m["provider"].(map[string]any)["openai"].(map[string]any)["options"].(map[string]any)
	assert.Equal(t, "https://org-gw.example/v1", opts["baseURL"])
	assert.Equal(t, "{env:OPENAI_API_KEY}", opts["apiKey"])
}

func TestBuildConfig_AnthropicDefaultNoProviderBlock(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "anthropic-api", Model: "claude-x"}
	out, err := buildConfig(b, Posture{AllowAll: true})
	require.NoError(t, err)
	m := decodeConfig(t, out)
	_, hasProvider := m["provider"]
	assert.False(t, hasProvider)
}

func TestBuildConfig_EmptyEffectiveAllowlistFailsClosed(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "ollama-cloud", Model: "m"}
	_, err := buildConfig(b, Posture{AllowedTools: []string{"MultiEdit"}})
	require.Error(t, err)
}

func TestBuildConfig_UnknownProviderFailsClosed(t *testing.T) {
	_, err := buildConfig(provider.ResolvedBinding{Provider: "mystery", Model: "m"}, Posture{AllowAll: true})
	require.ErrorIs(t, err, ErrUnknownProvider)
}
