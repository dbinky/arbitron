package engine_test

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretCanary = "sk-CANARY-never-persist"

func connBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Kind: provider.ProfileKindAgent, Provider: "azure-openai", BaseURL: "https://east.example.com/openai?api-key=" + secretCanary,
		Extras: map[string]string{"api-version": "2025-01-01", "token": secretCanary}, Model: "gpt-5", Backend: "codex",
		BackendExplicit: true, AllowedTools: []string{"Read", "Edit"}, ContextWindow: provider.Context200K,
		Effort: provider.EffortHigh, ConnectionRef: "azure-east",
	}
}

// Happy: every accepted field is captured; no credential enters the snapshot.
func TestTurnBinding_CaptureFreezesAcceptedFieldsWithoutSecrets(t *testing.T) {
	b := connBinding()
	got, err := engine.CaptureAcceptedTurnBinding(b)
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindAgent, got.Kind)
	assert.Equal(t, "azure-openai", got.Provider)
	assert.Equal(t, "gpt-5", got.Model)
	assert.Equal(t, "codex", got.Backend)
	assert.True(t, got.BackendExplicit)
	assert.Equal(t, []string{"Read", "Edit"}, got.AllowedTools)
	assert.Equal(t, provider.Context200K, got.ContextWindow)
	assert.Equal(t, provider.EffortHigh, got.Effort)
	assert.Equal(t, "azure-east", got.ConnectionRef)
	assert.Equal(t, engine.RouteConfigHash(b.Provider, b.BaseURL, b.Extras), got.RouteConfigHash)
	assert.Len(t, got.RouteConfigHash, 64)

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secretCanary, "neither query auth values nor raw Extras are persisted")
	assert.NotContains(t, string(raw), "east.example.com", "the endpoint is represented only by its hash")

	b.AllowedTools[0] = "Bash"
	assert.Equal(t, "Read", got.AllowedTools[0], "the snapshot does not alias the live binding's slice")
}

// Success: pin evidence follows the binding's own rule.
func TestTurnBinding_CaptureRecordsModelEvidenceRequirement(t *testing.T) {
	b := connBinding()
	b.Pinned = true
	got, err := engine.CaptureAcceptedTurnBinding(b)
	require.NoError(t, err)
	assert.True(t, got.Pinned)
	assert.Equal(t, b.RequiresModelEvidence(), got.RequireExecutedModel)
}

// Failure: an unusable binding is never accepted.
func TestTurnBinding_CaptureRejectsIncompleteBinding(t *testing.T) {
	for name, mut := range map[string]func(*provider.ResolvedBinding){
		"model":    func(b *provider.ResolvedBinding) { b.Model = "" },
		"provider": func(b *provider.ResolvedBinding) { b.Provider = "" },
		"kind":     func(b *provider.ResolvedBinding) { b.Kind = "" },
	} {
		b := connBinding()
		mut(&b)
		_, err := engine.CaptureAcceptedTurnBinding(b)
		assert.ErrorIs(t, err, engine.ErrTurnBindingChanged, name)
	}
}

// Error/Edge: the hash is canonical (alias, key order, query) but route-sensitive.
func TestTurnBinding_RouteConfigHashCanonicalButSensitive(t *testing.T) {
	base := engine.RouteConfigHash("azure-openai", "https://east.example.com/openai", map[string]string{"a": "1", "b": "2"})
	assert.Equal(t, base, engine.RouteConfigHash("azure-openai", "https://east.example.com/openai?api-key=rotated", map[string]string{"b": "2", "a": "1"}),
		"query values (credentials) and map order do not change the route")
	assert.NotEqual(t, base, engine.RouteConfigHash("azure-openai", "https://west.example.com/openai", map[string]string{"a": "1", "b": "2"}))
	assert.NotEqual(t, base, engine.RouteConfigHash("azure-openai", "https://east.example.com/openai", map[string]string{"a": "1", "b": "3"}))
	assert.NotEqual(t, base, engine.RouteConfigHash("openrouter", "https://east.example.com/openai", map[string]string{"a": "1", "b": "2"}))
	sub := engine.RouteConfigHash("claude-cli", "", nil)
	assert.Equal(t, sub, engine.RouteConfigHash("claude-cli", "", map[string]string{}), "nil and empty Extras are the same route")
}
