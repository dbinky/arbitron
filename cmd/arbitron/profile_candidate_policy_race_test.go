package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep the real prospective snapshot, then edit policy before CandidateService
// remaps its stored catalog. No inventory revision or classification is faked.
type candidatePolicyEditAfterInventory struct {
	modelcatalog.CandidateInventory
	edit func() error
}

func (i *candidatePolicyEditAfterInventory) PathsForProfile(ctx context.Context, p provider.Profile) (modelcatalog.PathSnapshot, error) {
	snap, err := i.CandidateInventory.PathsForProfile(ctx, p)
	if err == nil && i.edit != nil {
		edit := i.edit
		i.edit = nil
		err = edit()
	}
	return snap, err
}

func TestProfileCandidateReadRejectsPolicyRaceAndRetriesStoredProof(t *testing.T) {
	for _, historicalOnly := range []bool{false, true} {
		name := "current entries"
		if historicalOnly {
			name = "historical entries"
		}
		t.Run(name, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
			ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			call := mcpToolCaller(t, ctx, ds)
			args := map[string]any{
				"alias": "worker", "kind": "chat", "connection_ref": "root", "model": rootOld,
				"selection_mode": "following", "follow_target": map[string]any{"provider": "openrouter", "tier": "flash"},
			}
			res, _ := call("profile_create", args)
			require.False(t, res.IsError, "%+v", res.Content)
			require.NoError(t, ds.profileSvc.Delete(ctx, "worker"))
			if historicalOnly {
				leaf.mu.Lock()
				leaf.source = nil
				leaf.mu.Unlock()
				_, err := ds.modelRefresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
			}
			before, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, before.HistoricalModels)
			if historicalOnly {
				require.Empty(t, before.Catalog.Slices[0].Models)
			}
			frozen := catalogFrozenPayloads(t, ds)
			editPolicy := func(tier string) error {
				policy, err := ds.IdentityPolicy.Get(ctx)
				if err != nil {
					return err
				}
				_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, []modelidentity.PatternRule{{
					Providers: []string{"openrouter"}, Tier: tier, Pattern: `deepseek-v(4)-flash`, VersionGroups: []int{1},
				}})
				return err
			}
			require.NoError(t, editPolicy("renamed"))
			args["follow_target"] = map[string]any{"provider": "openrouter", "tier": "renamed"}
			leaf.mu.Lock()
			fetches, probes := leaf.fetches, len(leaf.models)
			leaf.mu.Unlock()

			// Old stored classifications alone cannot block a write or force new
			// network work: current rules reclassify the original raw metadata.
			res, _ = call("profile_create", args)
			require.False(t, res.IsError, "%+v", res.Content)
			stored, err := ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, rootOld, stored.Model)
			assert.Equal(t, "renamed", stored.FollowTarget.Tier)
			require.NoError(t, ds.profileSvc.Delete(ctx, "worker"))

			mapper, err := modelidentity.NewIdentityMapper(ds.IdentityPolicy)
			require.NoError(t, err)
			inventory := &candidatePolicyEditAfterInventory{CandidateInventory: ds.pathInventory, edit: func() error { return editPolicy("final") }}
			candidates, err := modelcatalog.NewCandidateService(modelcatalog.CandidateDeps{
				Store: ds.catalogStore, Inventory: inventory, Pipeline: ds.modelRefresh.service.Pipeline,
				Coordinator: ds.modelRefresh.coordinator, Gate: ds.executionGate, Mapper: mapper, Clock: ds.refreshDeps.Clock,
			})
			require.NoError(t, err)
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), candidates)
			p := stored.Clone()
			_, _, err = ds.profileSvc.Create(ctx, p)
			require.ErrorIs(t, err, provider.ErrExecutionRevisionStale, "reject the torn read before falling back to discovery")
			_, err = ds.profileSvc.Get(ctx, "worker")
			assert.ErrorIs(t, err, provider.ErrProfileNotFound)
			_, err = ds.profileResolver.Resolve(ctx, "worker")
			assert.ErrorIs(t, err, provider.ErrUnknownProfile)
			after, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a refused read cannot write catalog, proof or reports")
			assert.Equal(t, frozen, catalogFrozenPayloads(t, ds))

			args["follow_target"] = map[string]any{"provider": "openrouter", "tier": "final"}
			res, _ = call("profile_create", args)
			require.False(t, res.IsError, "%+v", res.Content)
			stored, err = ds.profileSvc.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, rootOld, stored.Model)
			assert.Equal(t, "final", stored.FollowTarget.Tier)
			leaf.mu.Lock()
			assert.Equal(t, fetches, leaf.fetches, "policy-only selection must reuse durable proof")
			assert.Len(t, leaf.models, probes, "neither the rejected read nor a fresh retry needs probes")
			leaf.source = []string{rootOld, rootNew}
			leaf.mu.Unlock()
			lifecycleExecute(t, ds, ctx, rootOld)
		})
	}
}
