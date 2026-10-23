package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing observation reconciliation must either abort refresh on orphaned
// legacy proof or create another observation/event for this same model.
func TestIdentityUpgradeDoesNotObserveOrAnnounceRoutingAliasAgainRoot(t *testing.T) {
	const native = "openrouter/deepseek-v4-flash"
	leaf := &lifecycleHTTP{source: []string{native, rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	fixture, err := os.ReadFile("../../adapter/sqlite/testdata/catalog_before_routing_prefix.sql")
	require.NoError(t, err)
	_, err = ds.db.Exec(string(fixture))
	require.NoError(t, err)
	frozen := catalogFrozenPayloads(t, ds)
	require.Len(t, frozen, 1)
	var observation string
	require.NoError(t, ds.db.QueryRow(`SELECT observation FROM model_first_observations`).Scan(&observation))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld})
	require.NoError(t, err)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)

	check := func(stack *domainStack) {
		t.Helper()
		report, err := stack.modelRefresh.coordinator.Refresh(ctx)
		require.NoError(t, err, "old canonical proof must not poison the upgraded catalog")
		assert.Empty(t, report.NewlyUsable, "both route spellings were already observed before upgrade")
		assert.Equal(t, frozen, catalogFrozenPayloads(t, stack), "event identity, payload and suppression remain frozen")
		var count int
		require.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&count))
		assert.Equal(t, 1, count)
		var current string
		require.NoError(t, stack.db.QueryRow(`SELECT observation FROM model_first_observations`).Scan(&current))
		assert.Equal(t, observation, current, "the original first-confirmed time and proof stay frozen")
		view, err := stack.modelRefresh.admin.List(ctx)
		require.NoError(t, err)
		require.Len(t, view.Entries, 2)
		for _, entry := range view.Entries {
			assert.Equal(t, rootOld, entry.CanonicalID)
			assert.Equal(t, modelcatalog.UsabilityConfirmed, entry.Usability)
		}
		lifecycleExecute(t, stack, ctx, rootOld)
	}
	check(ds)
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	ds.Close(context.Background())
	reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider),
	})
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close(context.Background()) })
	require.NoError(t, reopened.Start(ctx))
	check(reopened)
}
