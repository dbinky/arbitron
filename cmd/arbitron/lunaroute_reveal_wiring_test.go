package main

import (
	"testing"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both binding producers are built by initDomain and fed encrypted repository
// rows, including legacy/tampered rows that the connection write seam refuses.
func TestLunaRouteRevealGuardWiring(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr-foreign", Provider: "openrouter", BaseURL: "https://gw.lunaroute.com"}, "sk-or-v1-foreign"))
	_, err := ds.db.ExecContext(ctx, `UPDATE connections SET provider = 'lunaroute' WHERE name = 'lr-foreign'`)
	require.NoError(t, err)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr-moved", Provider: "lunaroute"}, "lr_live_moved"))
	// Corrupt the cipher as well: the endpoint refusal must precede decryption.
	_, err = ds.db.ExecContext(ctx, `UPDATE connections SET base_url = 'https://evil.example/v1', api_key_cipher = X'00' WHERE name = 'lr-moved'`)
	require.NoError(t, err)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "lr-good", Provider: "lunaroute"}, "lr_live_good"))

	// Insert below profile validation: Phase 1 has not enabled LR backends.
	repo := adaptsqlite.NewProfileRepo(ds.db)
	for alias, conn := range map[string]string{"p-foreign": "lr-foreign", "p-moved": "lr-moved", "p-good": "lr-good"} {
		require.NoError(t, repo.Create(ctx, provider.Profile{Alias: alias, Kind: provider.ProfileKindAgent, Backend: "opencode", ConnectionRef: conn, Model: "glm-5.3"}))
	}
	resolver, ok := ds.profileResolver.(*providersadapter.DynamicProfileResolver)
	require.True(t, ok, "root wires the dynamic resolver")
	require.NoError(t, resolver.Reload(ctx))

	check := func(conn string, b provider.ResolvedBinding) {
		t.Helper()
		key, err := b.RevealKey(ctx)
		switch conn {
		case "lr-foreign":
			require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
			assert.Empty(t, key)
			assert.NotContains(t, err.Error(), "sk-or-v1-foreign")
		case "lr-moved":
			require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
			assert.Empty(t, key)
		case "lr-good":
			require.NoError(t, err)
			assert.Equal(t, "lr_live_good", key)
		}
	}
	for alias, conn := range map[string]string{"p-foreign": "lr-foreign", "p-moved": "lr-moved", "p-good": "lr-good"} {
		b, err := ds.profileResolver.Resolve(ctx, alias)
		require.NoError(t, err, "resolution stays lazy, even with corrupt ciphertext")
		assert.Equal(t, conn, b.ConnectionRef)
		check(conn, b)
	}

	snap, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err, "inventory construction stays lazy too")
	seen := map[string]bool{}
	for _, p := range snap.Paths {
		switch p.ConnectionRef {
		case "lr-foreign", "lr-moved", "lr-good":
			assert.Equal(t, p.ConnectionRef, p.Binding.ConnectionRef)
			check(p.ConnectionRef, p.Binding)
			seen[p.ConnectionRef] = true
		}
	}
	assert.Equal(t, map[string]bool{"lr-foreign": true, "lr-moved": true, "lr-good": true}, seen)
}

func TestLunaRouteBackendProvenanceWiring(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	repo := adaptsqlite.NewProfileRepo(ds.db)
	profiles := []provider.Profile{
		{Alias: "lr-explicit", Kind: provider.ProfileKindAgent, Backend: "opencode", ConnectionRef: "lr-explicit", Model: "glm-5.3"},
		{Alias: "lr-legacy", Kind: provider.ProfileKindAgent, ConnectionRef: "lr-legacy", Model: "glm-5.3"},
	}
	for _, p := range profiles {
		require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: p.ConnectionRef, Provider: "lunaroute"}, "lr_live_"+p.Alias))
		require.NoError(t, repo.Create(ctx, p))
	}
	resolver, ok := ds.profileResolver.(*providersadapter.DynamicProfileResolver)
	require.True(t, ok)
	require.NoError(t, resolver.Reload(ctx))

	// Use the root's repository-backed pin lookup, not a canned lookup result.
	id := uuid.New()
	require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
		ID: id, OwnerID: localDefaultUser, WorkflowName: "provenance", State: instance.StateCompleted, Priority: instance.PriorityNormal,
		ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ModelPins:        map[string]string{"lr-explicit": "glm-5.3-flash", "lr-legacy": "glm-5.3-flash"},
	}))
	pins := provider.NewPinningResolver(ds.profileResolver, ds.pinLookup, engine.InstanceIDFromContext)
	pinCtx := engine.ContextWithInstanceID(ctx, id)
	for _, p := range profiles {
		b, err := ds.profileResolver.Resolve(ctx, p.Alias)
		require.NoError(t, err)
		assert.Equal(t, p.Backend != "", b.BackendExplicit, p.Alias)
		assert.Equal(t, p.ConnectionRef, b.ConnectionRef)
		pinned, err := pins.Resolve(pinCtx, p.Alias)
		require.NoError(t, err)
		assert.True(t, pinned.Pinned)
		assert.Equal(t, "glm-5.3-flash", pinned.Model)
		assert.Equal(t, p.Backend != "", pinned.BackendExplicit, "pin preserves provenance: %s", p.Alias)
		assert.Equal(t, p.ConnectionRef, pinned.ConnectionRef)
		key, err := pinned.RevealKey(ctx)
		require.NoError(t, err)
		assert.Equal(t, "lr_live_"+p.Alias, key, "pin preserves real lazy credential input")
	}

	snap, err := ds.pathInventory.Snapshot(ctx)
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, path := range snap.Paths {
		for _, p := range profiles {
			for _, alias := range path.Aliases {
				if alias != p.Alias {
					continue
				}
				assert.NotEmpty(t, path.Binding.Backend, "legacy profile has an effective deployment backend")
				assert.Equal(t, p.Backend != "", path.Binding.BackendExplicit, p.Alias)
				assert.Equal(t, p.ConnectionRef, path.Binding.ConnectionRef)
				seen[p.Alias] = true
			}
		}
	}
	assert.Equal(t, map[string]bool{"lr-explicit": true, "lr-legacy": true}, seen)
}
