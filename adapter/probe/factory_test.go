package probe

import (
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy-path: ProbeFor dispatches every known provider string to the right
// concrete probe, purely on binding.Provider (Kind/Model untouched).
func TestProbeFactory_ProbeFor_DispatchesByProvider(t *testing.T) {
	f := NewProbeFactory(Deps{})
	cases := []struct {
		provider string
		want     domprovider.ConnectionProbe
	}{
		{"openai", (*openAICompatProbe)(nil)},
		{"openrouter", (*openAICompatProbe)(nil)},
		{"litellm", (*openAICompatProbe)(nil)},
		{"ollama", (*openAICompatProbe)(nil)},
		{"ollama-cloud", (*openAICompatProbe)(nil)},
		{"azure-openai", (*openAICompatProbe)(nil)},
		{"azure-foundry", (*openAICompatProbe)(nil)},
		{"anthropic-api", (*anthropicProbe)(nil)},
		{"bedrock", (*bedrockProbe)(nil)},
		{"amazon-bedrock", (*bedrockProbe)(nil)},
		{"", (*claudeCLIProbe)(nil)},
		{"claude-cli", (*claudeCLIProbe)(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			// Set Kind+Model to prove they are never consulted in dispatch.
			p, err := f.ProbeFor(domprovider.ResolvedBinding{
				Provider: tc.provider,
				Kind:     domprovider.ProfileKindAgent,
				Model:    "should-be-ignored",
			})
			require.NoError(t, err)
			assert.IsType(t, tc.want, p)
		})
	}
}

// Input-validation: an unknown provider returns ErrNoProbeForProvider and names
// the offending provider for operator diagnostics.
func TestProbeFactory_ProbeFor_UnknownProvider_Errors(t *testing.T) {
	f := NewProbeFactory(Deps{})
	p, err := f.ProbeFor(domprovider.ResolvedBinding{Provider: "made-up"})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.ErrorIs(t, err, domprovider.ErrNoProbeForProvider)
	assert.Contains(t, err.Error(), "made-up")
}

// Input-validation: provider matching is case- and whitespace-insensitive.
func TestProbeFactory_ProbeFor_NormalizesCaseWhitespace(t *testing.T) {
	f := NewProbeFactory(Deps{})

	p, err := f.ProbeFor(domprovider.ResolvedBinding{Provider: "  Anthropic-API "})
	require.NoError(t, err)
	assert.IsType(t, (*anthropicProbe)(nil), p)

	p, err = f.ProbeFor(domprovider.ResolvedBinding{Provider: "  Amazon-Bedrock"})
	require.NoError(t, err)
	assert.IsType(t, (*bedrockProbe)(nil), p)

	p, err = f.ProbeFor(domprovider.ResolvedBinding{Provider: "OPENAI"})
	require.NoError(t, err)
	assert.IsType(t, (*openAICompatProbe)(nil), p)
}
