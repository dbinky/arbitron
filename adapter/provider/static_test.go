package provider_test

import (
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Happy path: a registry with one chat + one agent profile resolves the chat
// alias to the full chat profile.
func TestStaticProfileResolver_ResolveChat(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	})
	require.NoError(t, err)

	got, err := r.Resolve("default-chat")
	require.NoError(t, err)
	assert.Equal(t, provider.Profile{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"}, got)
}

// Success: an agent alias resolves to the agent profile; the returned Kind
// distinguishes chat from agent so the caller can pick the adapter.
func TestStaticProfileResolver_ResolveAgentKindSelection(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	})
	require.NoError(t, err)

	got, err := r.Resolve("default-agent")
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindAgent, got.Kind)
	assert.NotEqual(t, provider.ProfileKindChat, got.Kind)
}

// Failure: an unknown alias returns an error matchable as ErrUnknownProfile,
// and the wrapped message carries the offending alias for context.
func TestStaticProfileResolver_ResolveUnknown(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
	})
	require.NoError(t, err)

	_, err = r.Resolve("nope")
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	assert.Contains(t, err.Error(), "nope")
}

// Error: a duplicate alias is a construction-time failure naming the alias.
func TestNewStaticProfileResolver_DuplicateAlias(t *testing.T) {
	_, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "dup", Kind: provider.ProfileKindChat, Model: "a"},
		{Alias: "dup", Kind: provider.ProfileKindAgent, Model: "b"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dup")
}

// Error: an unknown Kind value is rejected at construction.
func TestNewStaticProfileResolver_UnknownKind(t *testing.T) {
	_, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "weird", Kind: provider.ProfileKind("tool"), Model: "a"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool")
}

// Edge: an empty/nil registry constructs successfully and every Resolve misses
// with ErrUnknownProfile.
func TestNewStaticProfileResolver_EmptyRegistry(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver(nil)
	require.NoError(t, err)

	_, err = r.Resolve("anything")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// Edge: alias matching is exact / case-sensitive.
func TestStaticProfileResolver_CaseSensitive(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m"},
	})
	require.NoError(t, err)

	_, err = r.Resolve("Default-Chat")
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// The resolver satisfies the domain ProfileResolver port.
func TestStaticProfileResolver_ImplementsPort(t *testing.T) {
	var _ provider.ProfileResolver = (*adapterprovider.StaticProfileResolver)(nil)
	// Sanity: a constructed resolver is usable through the port.
	r, err := adapterprovider.NewStaticProfileResolver(nil)
	require.NoError(t, err)
	var port provider.ProfileResolver = r
	_, err = port.Resolve("x")
	assert.True(t, errors.Is(err, provider.ErrUnknownProfile))
}
