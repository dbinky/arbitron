package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCapabilityRules_LunaRouteOverrideReplacesPerModel(t *testing.T) {
	saved := defaultCapabilitiesYAML
	t.Cleanup(func() { defaultCapabilitiesYAML = saved })
	defaultCapabilitiesYAML = []byte(`backends:
  opencode:
    context_tiers: [default]
    effort_levels: [default]
lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: 1000, evidence: a}
    output_tokens: {tokens: 10, evidence: a}
    reasoning: {support: supported, evidence: a}
    request_settings:
      temperature: {support: unsupported, evidence: a}
      output_schema: {support: supported, evidence: a}
    efforts:
      chat: {levels: [high], evidence: a}
  deepseek-4.1-flash-1017:
    context_tokens: {tokens: 2000, evidence: b}
`)
	p := filepath.Join(t.TempDir(), "o.yaml")
	require.NoError(t, os.WriteFile(p, []byte("lunaroute_models:\n  glm-5.3:\n    context_tokens: {tokens: 9000, evidence: c}\n"), 0o600))
	r, err := LoadCapabilityRules(p)
	require.NoError(t, err)
	glm, ok := r.LunaRouteModel("glm-5.3")
	require.True(t, ok)
	assert.Equal(t, 9000, glm.ContextTokens.Tokens)
	assert.False(t, glm.OutputTokens.Known(), "replace, do not merge")
	assert.Equal(t, provider.SupportUnknown, glm.Reasoning.Support)
	assert.Empty(t, glm.RequestSettings)
	assert.False(t, glm.Effort("chat").Known)
	ds, ok := r.LunaRouteModel("deepseek-4.1-flash-1017")
	require.True(t, ok)
	assert.Equal(t, 2000, ds.ContextTokens.Tokens)
}
