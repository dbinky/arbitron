package main

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteDeferredBackendsSaveWiring(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+writeLunaRouteCaps(t)+"\n", gw, func(string) {})
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	ctx := adminCtxForLocalUser(t, ds)
	for _, backend := range []string{"claude-code", "opencode"} {
		t.Run(backend, func(t *testing.T) {
			p := provider.Profile{Alias: "service-" + backend, Kind: provider.ProfileKindAgent, Backend: backend, ConnectionRef: "lr", Model: "glm-5.3"}
			_, _, err := ds.profileSvc.Create(ctx, p)
			require.ErrorIs(t, err, provider.ErrLunaRouteBackendUnsupported)
			var refusal *provider.LunaRouteRefusal
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, backend, refusal.Backend)
			bad, text := call("profile_create", map[string]any{"alias": "mcp-" + backend, "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": backend})
			require.True(t, bad)
			assert.Contains(t, text, "LunaRoute on "+backend+" not yet supported")
			assert.NotContains(t, text, lrRootKey)
			var rows int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias IN (?,?)`, p.Alias, "mcp-"+backend).Scan(&rows))
			assert.Zero(t, rows, "refused writes never persist")
		})
	}
	assert.Empty(t, gw.seen(), "refusal is local")
}
