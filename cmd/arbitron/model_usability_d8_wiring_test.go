package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsabilityD8AndUnknownMappingRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "opaque routes must not be probed", http.StatusBadRequest)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	refresh, err := newModelRefresh(ds.refreshDeps)
	require.NoError(t, err)
	defer refresh.stop()
	for _, route := range []string{"azure", "azure-openai", "azure-foundry", "bedrock", "amazon-bedrock", "Azure-OpenAI", " AMAZON-BEDROCK ", "unmapped-provider"} {
		t.Run(route, func(t *testing.T) {
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: route, Provider: route, BaseURL: upstream.URL, Extras: map[string]string{"api-version": "2025-01-01", "region": "us-east-1", "access-key-id": "test-account"}}, "must-not-be-used"))
			snap, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			var path modelcatalog.ExecutionPath
			for _, p := range snap.Paths {
				if p.ConnectionRef == route && p.Mode == modelcatalog.ExecutionModeBatch && p.Backend == "" {
					path = p
					break
				}
			}
			require.NotEmpty(t, path.Key)
			// Even a forged run-pin exception cannot turn a catalog check into opaque
			// configured-ID execution. Native D8 following is rejected at the service.
			path.Binding.Pinned = true
			path.Binding.OpaquePinRoute = provider.Canonicalize(route)
			e := modelidentity.Entry{CanonicalID: "anthropic/claude-sonnet-4-6", Provider: "anthropic", NativeID: "claude-sonnet-4-6"}
			ev, err := refresh.probe.Check(ctx, path, e)
			if route == "unmapped-provider" {
				require.NoError(t, err)
				assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
			} else {
				assert.ErrorIs(t, err, provider.ErrInvalidSelection)
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: route, Kind: provider.ProfileKindChat, ConnectionRef: route, Model: e.NativeID, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}})
				assert.ErrorIs(t, err, provider.ErrInvalidSelection)
				_, err = ds.profileSvc.Get(ctx, route)
				assert.ErrorIs(t, err, provider.ErrProfileNotFound)
			}
			assert.False(t, ev.Confirmed())
			assert.Empty(t, ev.ProofRef)
			assert.Zero(t, requests.Load(), "neither opaque exceptions nor unknown mappings may send probes")
		})
	}
}
