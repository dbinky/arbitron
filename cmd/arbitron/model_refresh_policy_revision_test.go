package main

import (
	"context"
	"testing"

	adaptcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Preserve the real inventory token, then allow an admin edit before remapping.
// This models the policy changing during collection without inventing entries
// or bypassing the production policy, inventory, mapper or persistence ports.
type policyEditAfterInventory struct {
	modelcatalog.PathInventory
	edit func() error
}

func (i policyEditAfterInventory) Snapshot(ctx context.Context) (modelcatalog.PathSnapshot, error) {
	snapshot, err := i.PathInventory.Snapshot(ctx)
	if err != nil {
		return snapshot, err
	}
	return snapshot, i.edit()
}

type countedCycleStore struct {
	modelcatalog.CatalogStore
	commits int
}

func (s *countedCycleStore) CommitCycle(ctx context.Context, c modelcatalog.CycleCommit) (modelcatalog.RefreshReport, error) {
	s.commits++
	return s.CatalogStore.CommitCycle(ctx, c)
}

// Removing collect's known-entry PolicyRevision guard must fail both cases:
// selection's own stale-advance checks cannot protect discovery-only writes.
func TestRefreshRejectsPolicyChangeAfterInventoryBeforeAnyCycleWrite(t *testing.T) {
	for _, historicalOnly := range []bool{false, true} {
		name := "current source entries"
		if historicalOnly {
			name = "historical entries with empty source"
		}
		t.Run(name, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			before, _, err := ds.profileSvc.Create(ctx, provider.Profile{
				Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld,
				SelectionMode: provider.SelectionFollowing,
				FollowTarget:  &provider.FollowTarget{Provider: "openrouter", Tier: "flash"},
			})
			require.NoError(t, err)
			storedBefore, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, storedBefore.HistoricalModels)
			frozenBefore := catalogFrozenPayloads(t, ds)
			policy, err := ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)

			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			if historicalOnly {
				leaf.source = nil
			}
			leaf.mu.Unlock()
			inventory := policyEditAfterInventory{PathInventory: ds.pathInventory, edit: func() error {
				_, err := ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
				return err
			}}
			mapper, err := modelidentity.NewIdentityMapper(ds.IdentityPolicy)
			require.NoError(t, err)
			normalizer, err := adaptcatalog.NewNormalizer(mapper, ds.refreshDeps.Clock)
			require.NoError(t, err)
			store := &countedCycleStore{CatalogStore: ds.catalogStore}
			pipeline, err := modelcatalog.NewDiscoveryPipeline(modelcatalog.DiscoveryDeps{
				Settings: ds.SystemSettings, Fetcher: adaptcatalog.NewFetcherWithTransport(ds.refreshDeps.FetchTransport),
				Normalizer: normalizer, Mapper: mapper, Inventory: inventory, Profiles: ds.profileSvc,
				Store: store, Probe: ds.modelRefresh.probe, AdvanceValidator: ds.profileSvc,
				Clock: ds.refreshDeps.Clock,
			})
			require.NoError(t, err)
			service := ds.modelRefresh.service
			originalPipeline := service.Pipeline
			service.Pipeline, service.Store = pipeline, store

			report, err := ds.modelRefresh.coordinator.Refresh(ctx)
			assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			assert.Empty(t, report.ID, "a refused collection must not claim a committed report")
			assert.Zero(t, store.commits, "stale classification must stop before CommitCycle")
			currentPolicy, err := ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)
			assert.Equal(t, policy.Revision+1, currentPolicy.Revision, "the persisted edit must actually occur")
			storedAfter, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, storedBefore, storedAfter, "catalog, historical rows, proof, cursor and reports stay unchanged")
			assert.Equal(t, frozenBefore, catalogFrozenPayloads(t, ds))
			var journalRows int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journalRows))
			assert.Zero(t, journalRows)
			current, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, before, current)

			// Keep execution available even when the catalog temporarily omits X.
			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			lifecycleExecute(t, ds, ctx, rootOld)
			leaf.mu.Lock()
			assert.Equal(t, []string{rootOld, rootOld}, leaf.executed)
			leaf.mu.Unlock()

			// A later stable-policy cycle must still advance through the same
			// coordinator, durable transaction and cache to the new-run sink.
			service.Pipeline = originalPipeline
			report, err = ds.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			require.Len(t, report.Changes, 1)
			assert.Equal(t, rootNew, report.Changes[0].ToModel)
			assert.Equal(t, 1, store.commits)
			lifecycleExecute(t, ds, ctx, rootNew)
		})
	}
}
