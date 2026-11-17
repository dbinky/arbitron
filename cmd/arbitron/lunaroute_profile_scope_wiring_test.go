package main

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteProfileScopeWiring(t *testing.T) {
	gw := newFakeLunaRouteGateway(t, "lr_scope", "glm-5.3")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, lrChatCaps)+"\n", gw, func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "opaque", Provider: "lunaroute"}, "lr_scope"))
	routing, err := ds.modelRefresh.candidates.ConnectionProvider(ctx, provider.Profile{ConnectionRef: "opaque"})
	require.NoError(t, err)
	require.Equal(t, "lunaroute", routing)
	// No model is available to route inspection yet. The actual connection row,
	// not the reference's spelling or catalog target, must reject following.
	for _, model := range []string{"", "glm-5.3"} {
		_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "scope-follow", Kind: provider.ProfileKindChat, ConnectionRef: "opaque", Model: model, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "zhipuai", Tier: "glm"}})
		require.ErrorIs(t, err, provider.ErrLunaRoutePinnedOnly)
	}
	p := provider.Profile{Alias: "scope-edit", Kind: provider.ProfileKindChat, ConnectionRef: "opaque", Model: "glm-5.3", Effort: provider.EffortHigh}
	saved, _, err := ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)
	// The selection stays unchanged, but connection policy is read afresh.
	azureKey := "azure-scope-key"
	require.NoError(t, ds.connSvc.Update(ctx, provider.Connection{Name: "opaque", Provider: "azure-foundry", BaseURL: "https://d8-azure.example.com", Extras: map[string]string{"api-version": "2024-10-21"}}, &azureKey))
	saved.Effort = provider.EffortMedium
	saved, _, err = ds.profileSvc.Update(ctx, saved)
	require.NoError(t, err)
	key := "lr_scope"
	require.NoError(t, ds.connSvc.Update(ctx, provider.Connection{Name: "opaque", Provider: "lunaroute"}, &key))
	_, _, err = ds.profileSvc.Update(ctx, saved)
	require.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
	stored, err := ds.profileSvc.Get(ctx, saved.Alias)
	require.NoError(t, err)
	assert.Equal(t, saved.Revision, stored.Revision, "refused unchanged edit must not persist")
	assert.Equal(t, provider.EffortMedium, stored.Effort)
	var count int
	require.NoError(t, ds.db.QueryRow("SELECT COUNT(*) FROM profiles WHERE alias='scope-follow'").Scan(&count))
	assert.Zero(t, count)
	assert.Empty(t, gw.Requests(), "profile validation must perform no online discovery or inference")
}
