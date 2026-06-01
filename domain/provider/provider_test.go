package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
)

// Happy path: the kind constants carry the spec'd string values so config and
// adapters share the same vocabulary.
func TestProfileKind_Values(t *testing.T) {
	assert.Equal(t, provider.ProfileKind("chat"), provider.ProfileKindChat)
	assert.Equal(t, provider.ProfileKind("agent"), provider.ProfileKindAgent)
}

// Success: a Profile carries alias, kind, model, connection-ref, and params
// straight through.
func TestProfile_Fields(t *testing.T) {
	p := provider.Profile{
		Alias:         "default-chat",
		Kind:          provider.ProfileKindChat,
		ConnectionRef: "azure-prod",
		Model:         "claude-sonnet",
		Params:        map[string]any{"temperature": 0.2},
	}
	assert.Equal(t, "default-chat", p.Alias)
	assert.Equal(t, provider.ProfileKindChat, p.Kind)
	assert.Equal(t, "azure-prod", p.ConnectionRef)
	assert.Equal(t, "claude-sonnet", p.Model)
	assert.Equal(t, map[string]any{"temperature": 0.2}, p.Params)
}

// Success: a ResolvedBinding carries the fully-resolved backend selection,
// including a lazy RevealKey closure.
func TestResolvedBinding_Fields(t *testing.T) {
	b := provider.ResolvedBinding{
		Kind:     provider.ProfileKindChat,
		Provider: "azure-openai",
		BaseURL:  "https://example.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
		Model:    "gpt-4o",
		Params:   map[string]any{"max_tokens": 100},
		RevealKey: func(context.Context) (string, error) {
			return "sk-secret", nil
		},
	}
	assert.Equal(t, provider.ProfileKindChat, b.Kind)
	assert.Equal(t, "azure-openai", b.Provider)
	assert.Equal(t, "https://example.azure.com", b.BaseURL)
	assert.Equal(t, map[string]string{"api-version": "2024-02-01"}, b.Extras)
	assert.Equal(t, "gpt-4o", b.Model)
	assert.Equal(t, map[string]any{"max_tokens": 100}, b.Params)

	key, err := b.RevealKey(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "sk-secret", key)
}

// Error: ErrUnknownProfile is a non-nil sentinel matchable via errors.Is, even
// when wrapped with context by a resolver.
func TestErrUnknownProfile_Sentinel(t *testing.T) {
	assert.NotNil(t, provider.ErrUnknownProfile)
	wrapped := errors.Join(provider.ErrUnknownProfile, errors.New("alias=nope"))
	assert.ErrorIs(t, wrapped, provider.ErrUnknownProfile)
}
