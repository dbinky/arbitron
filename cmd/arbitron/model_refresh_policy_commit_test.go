package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	adaptcatalog "github.com/arbitron/arbitron/adapter/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production probe runs after collect's PolicyRevision guard, so an admin
// edit inside it lands between collection and commit (G8, paired:7).
type policyEditDuringProbe struct {
	modelcatalog.UsabilityProbe
	once sync.Once
	edit func() error
	err  error
}

func (p *policyEditDuringProbe) Check(ctx context.Context, path modelcatalog.ExecutionPath, e modelidentity.Entry) (modelcatalog.UsabilityEvidence, error) {
	p.once.Do(func() { p.err = p.edit() })
	return p.UsabilityProbe.Check(ctx, path, e)
}

// Removing CommitCycle's collected-policy check must fail both cases: the
// discovery-only write and the stale-advance fallback both run under WithRead.
func TestRefreshRejectsPolicyEditBetweenCollectionAndCommitRoot(t *testing.T) {
	for _, following := range []bool{false, true} {
		name := "discovery only"
		if following {
			name = "stale advance fallback"
		}
		t.Run(name, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			if following {
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{
					Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld,
					SelectionMode: provider.SelectionFollowing,
					FollowTarget:  &provider.FollowTarget{Provider: "openrouter", Tier: "flash"},
				})
				require.NoError(t, err)
			} else {
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{
					Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld,
				})
				require.NoError(t, err)
			}
			before, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			storedBefore, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			frozenBefore := catalogFrozenPayloads(t, ds)
			policy, err := ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)
			var withoutFlash []modelidentity.PatternRule
			for _, r := range policy.Rules {
				if r.Tier != "flash" {
					withoutFlash = append(withoutFlash, r)
				}
			}
			require.Less(t, len(withoutFlash), len(policy.Rules))

			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			probe := &policyEditDuringProbe{UsabilityProbe: ds.modelRefresh.probe, edit: func() error {
				_, err := ds.IdentityPolicy.Update(ctx, policy.Revision, withoutFlash)
				return err
			}}
			mapper, err := modelidentity.NewIdentityMapper(ds.IdentityPolicy)
			require.NoError(t, err)
			normalizer, err := adaptcatalog.NewNormalizer(mapper, ds.refreshDeps.Clock)
			require.NoError(t, err)
			store := &countedCycleStore{CatalogStore: ds.catalogStore}
			pipeline, err := modelcatalog.NewDiscoveryPipeline(modelcatalog.DiscoveryDeps{
				Settings: ds.SystemSettings, Fetcher: adaptcatalog.NewFetcherWithTransport(ds.refreshDeps.FetchTransport),
				Normalizer: normalizer, Mapper: mapper, Inventory: ds.pathInventory, Profiles: ds.profileSvc,
				Store: store, Probe: probe, AdvanceValidator: ds.profileSvc,
				Clock: ds.refreshDeps.Clock,
			})
			require.NoError(t, err)
			service := ds.modelRefresh.service
			originalPipeline := service.Pipeline
			service.Pipeline, service.Store = pipeline, store

			report, err := ds.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, probe.err)
			assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			assert.Empty(t, report.ID, "a stale cycle must not claim a committed report")
			assert.NotZero(t, store.commits, "the edit must land after collect's guard, at CommitCycle")
			currentPolicy, err := ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)
			assert.Equal(t, policy.Revision+1, currentPolicy.Revision, "the persisted edit must actually occur")
			storedAfter, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, storedBefore, storedAfter, "no stale tier, proof, cursor or report is committed")
			assert.Equal(t, frozenBefore, catalogFrozenPayloads(t, ds), "no stale tier is frozen into an event")
			var journalRows int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal`).Scan(&journalRows))
			assert.Zero(t, journalRows)
			current, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, before, current)
			lifecycleExecute(t, ds, ctx, rootOld)

			// A fresh cycle classifies under the edited policy: flash no longer
			// exists, so nothing advances and no frozen event names it.
			service.Pipeline = originalPipeline
			report, err = ds.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.NotEmpty(t, report.ID)
			assert.Empty(t, report.Changes)
			stored, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			for _, sl := range stored.Catalog.Slices {
				for _, e := range sl.Entries {
					assert.Equal(t, currentPolicy.Revision, e.PolicyRevision, "%s/%s", e.Provider, e.NativeID)
					assert.NotEqual(t, "flash", e.Tier, "%s/%s", e.Provider, e.NativeID)
				}
			}
			for canonical, raw := range catalogFrozenPayloads(t, ds) {
				if _, old := frozenBefore[canonical]; old {
					continue
				}
				var payload modelcatalog.AnnouncementPayload
				require.NoError(t, json.Unmarshal([]byte(raw), &payload))
				assert.NotEqual(t, "flash", payload.Tier, canonical)
			}
			lifecycleExecute(t, ds, ctx, rootOld)
		})
	}
}
