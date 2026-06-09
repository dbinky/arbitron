package provider_test

import (
	"context"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// happy — subscription arm copies the profile's Backend onto the binding while
// still pinning Provider to claude-cli.
func TestStaticProfileResolver_SubscriptionArmCopiesBackend(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "agent", Kind: provider.ProfileKindAgent, Model: "m", Backend: "claude-code"},
	}, nil)
	require.NoError(t, err)

	b, err := r.Resolve(context.Background(), "agent")
	require.NoError(t, err)
	assert.Equal(t, "claude-code", b.Backend)
	assert.Equal(t, "claude-cli", b.Provider)
}

// happy — connection arm copies the profile's Backend; Provider comes from the
// connection as today.
func TestStaticProfileResolver_ConnectionArmCopiesBackend(t *testing.T) {
	store := newFakeConnectionStore()
	store.put(provider.Connection{Name: "lite", Provider: "litellm", BaseURL: "https://lite:4000"}, "sk")

	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "agent", Kind: provider.ProfileKindAgent, ConnectionRef: "lite", Model: "m", Backend: "opencode"},
	}, store)
	require.NoError(t, err)

	b, err := r.Resolve(context.Background(), "agent")
	require.NoError(t, err)
	assert.Equal(t, "opencode", b.Backend)
	assert.Equal(t, "litellm", b.Provider)
}

// boundary — a profile with no backend resolves to an empty Backend (no
// implicit default injected at the resolver layer).
func TestStaticProfileResolver_EmptyBackendStaysEmpty(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "agent", Kind: provider.ProfileKindAgent, Model: "m"},
	}, nil)
	require.NoError(t, err)

	b, err := r.Resolve(context.Background(), "agent")
	require.NoError(t, err)
	assert.Equal(t, "", b.Backend, "resolver injects no default; the executor/factory owns the default")
}
