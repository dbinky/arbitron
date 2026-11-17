package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteConnectionProviderUsesMetadataOnly(t *testing.T) {
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "opaque", Provider: "lunaroute"})
	f.addConn(provider.Connection{Name: "lunaroute", Provider: "openrouter"})
	f.wfs = &invWorkflows{scopeErr: errors.New("unrelated workflows unavailable")}
	f.runs.err = errors.New("unrelated active runs unavailable")
	inv := f.build(t)
	for ref, want := range map[string]string{"opaque": "lunaroute", "lunaroute": "openrouter", "": ""} {
		got, err := inv.ConnectionProvider(context.Background(), provider.Profile{ConnectionRef: ref})
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := inv.ConnectionProvider(context.Background(), provider.Profile{ConnectionRef: "gone"})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = inv.ConnectionProvider(ctx, provider.Profile{ConnectionRef: "opaque"})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, f.conns.revealCalls)
}
