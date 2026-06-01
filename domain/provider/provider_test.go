package provider_test

import (
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

// Success: a Profile carries alias, kind, and model straight through.
func TestProfile_Fields(t *testing.T) {
	p := provider.Profile{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "claude-sonnet"}
	assert.Equal(t, "default-chat", p.Alias)
	assert.Equal(t, provider.ProfileKindChat, p.Kind)
	assert.Equal(t, "claude-sonnet", p.Model)
}

// Error: ErrUnknownProfile is a non-nil sentinel matchable via errors.Is, even
// when wrapped with context by a resolver.
func TestErrUnknownProfile_Sentinel(t *testing.T) {
	assert.NotNil(t, provider.ErrUnknownProfile)
	wrapped := errors.Join(provider.ErrUnknownProfile, errors.New("alias=nope"))
	assert.ErrorIs(t, wrapped, provider.ErrUnknownProfile)
}
