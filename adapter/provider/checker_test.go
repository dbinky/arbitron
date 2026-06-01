package provider_test

import (
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolver is a tiny ProfileResolver for exercising ResolverChecker in
// isolation, including the non-ErrUnknownProfile error path.
type stubResolver struct {
	prof provider.Profile
	err  error
}

func (s stubResolver) Resolve(string) (provider.Profile, error) {
	return s.prof, s.err
}

// Happy path: a resolvable chat alias yields its kind as a plain string, ok=true.
func TestResolverChecker_ProfileKindChat(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	})
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("default-chat")
	assert.True(t, ok)
	assert.Equal(t, "chat", kind)
}

// Success: an agent alias maps to the "agent" kind string.
func TestResolverChecker_ProfileKindAgent(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	})
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("default-agent")
	assert.True(t, ok)
	assert.Equal(t, "agent", kind)
}

// Error: an unknown alias (ErrUnknownProfile) reports ok=false, empty kind.
func TestResolverChecker_UnknownAlias(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver(nil)
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("ghost")
	assert.False(t, ok)
	assert.Equal(t, "", kind)
}

// Edge: any resolver error (not just ErrUnknownProfile) collapses to ok=false.
func TestResolverChecker_GenericResolveError(t *testing.T) {
	c := adapterprovider.NewResolverChecker(stubResolver{err: errors.New("boom")})

	kind, ok := c.ProfileKind("whatever")
	assert.False(t, ok)
	assert.Equal(t, "", kind)
}
