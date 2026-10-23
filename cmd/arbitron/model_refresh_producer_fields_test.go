package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only HTTP is substituted: the common root feeds real classification, inventory,
// selector, transaction, publication and event producers, including on retry.
func TestRefreshProducerFieldsPersistOriginalObservationRoot(t *testing.T) {
	const extra = "unclassified-native"
	for _, mode := range []string{"happy", "stale_workflow", "persistence_fault", "policy_without_advances"} {
		t.Run(mode, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}}
			var hookMu sync.Mutex
			var hook func() error
			var hookErr error
			deps := ModelDependencies{
				FetchTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
					response, err := leaf.fetch(r)
					if err != nil {
						return nil, err
					}
					defer response.Body.Close()
					var catalog map[string]any
					if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
						return nil, err
					}
					// Same canonical identity, different catalog provider/tier/spelling;
					// no execution path has confirmed this other offering.
					catalog["ollama-cloud"] = map[string]any{
						"id": "ollama-cloud",
						"models": map[string]any{
							"offering-v4.1": map[string]any{"id": "offering-v4.1", "canonical_model_id": rootNew, "release_date": "2026-10-18"},
						},
					}
					raw, err := json.Marshal(catalog)
					if err != nil {
						return nil, err
					}
					return lifecycleResponse(r, string(raw)), nil
				}),
				ProviderTransport: catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					r.Body = io.NopCloser(bytes.NewReader(raw))
					var body struct{ Model string }
					if err = json.Unmarshal(raw, &body); err != nil {
						return nil, err
					}
					if body.Model == rootNew {
						hookMu.Lock()
						f := hook
						hook = nil
						hookMu.Unlock()
						if f != nil {
							hookErr = f()
							if hookErr != nil {
								return nil, hookErr
							}
						}
					}
					return leaf.provider(r)
				}),
			}
			ds, _, intervals := buildLifecycleRootWithDependencies(t, "profiles: []\n", leaf, deps)
			ctx := lifecycleConnection(t, ds)
			policy, err := ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)
			policy.Rules = append(policy.Rules, modelidentity.PatternRule{
				Providers: []string{"ollama-cloud"}, Tier: "pro",
				Pattern: `offering-v(\d+(?:\.\d+)?)`, VersionGroups: []int{1},
			})
			_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
			require.NoError(t, err)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			profile := provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", Model: rootOld, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}}
			if mode == "policy_without_advances" {
				profile.SelectionMode = provider.SelectionPinned
				profile.FollowTarget = nil
			}
			_, _, err = ds.profileSvc.Create(ctx, profile)
			require.NoError(t, err)
			lifecycleExecute(t, ds, ctx, rootOld)
			before, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			frozenBefore := catalogFrozenPayloads(t, ds)
			leaf.mu.Lock()
			leaf.source = []string{rootOld, rootNew, extra}
			leaf.models = nil
			leaf.mu.Unlock()
			switch mode {
			case "stale_workflow":
				hook = func() error {
					wf, err := ds.workflowSvc.Load(ctx, "lifecycle")
					if err != nil {
						return err
					}
					wf.Description = "edited during the exact-model probe"
					return ds.workflowSvc.Update(ctx, "lifecycle", wf)
				}
			case "policy_without_advances":
				hook = func() error {
					policy, err := ds.IdentityPolicy.Get(ctx)
					if err != nil {
						return err
					}
					_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
					return err
				}
			case "persistence_fault":
				_, err = ds.db.Exec(`CREATE TRIGGER refuse_cycle BEFORE INSERT ON model_usability_evidence BEGIN SELECT RAISE(ABORT,'injected'); END`)
				require.NoError(t, err)
			}
			report, err := ds.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, hookErr)
			if mode == "stale_workflow" || mode == "policy_without_advances" {
				require.Nil(t, hook, "the concurrent edit must actually run")
			}
			if mode == "persistence_fault" || mode == "policy_without_advances" {
				assert.Error(t, err)
				assert.Empty(t, report.ID)
				after, e := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, e)
				assert.Equal(t, before, after)
				assert.Equal(t, frozenBefore, catalogFrozenPayloads(t, ds))
				if mode == "persistence_fault" {
					_, err = ds.db.Exec(`DROP TRIGGER refuse_cycle`)
					require.NoError(t, err)
				}
			} else {
				require.NoError(t, err)
			}
			if mode == "stale_workflow" {
				assert.Empty(t, report.Changes)
				assert.Equal(t, "advance_conflict", report.PathStatus["refresh"])
				frozen := catalogFrozenPayloads(t, ds)
				assert.NotContains(t, frozen, rootNew, "only the dropped proposal's discovery waits")
				assert.Contains(t, frozen, "openrouter/"+extra, "unrelated discoveries must not starve")
				view, e := ds.catalogStore.Snapshot(ctx)
				require.NoError(t, e)
				require.NotEmpty(t, view.Evidence, "proof is retained even though the proposal was stale")
				leaf.mu.Lock()
				leaf.models = nil
				leaf.mu.Unlock()
			}
			if mode != "happy" {
				report, err = ds.modelRefresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
			}
			if mode == "stale_workflow" {
				leaf.mu.Lock()
				assert.Empty(t, leaf.models, "unchanged path/config confirmations must not be re-probed")
				leaf.mu.Unlock()
			}
			want := rootNew
			if mode == "policy_without_advances" {
				want = rootOld
				assert.Empty(t, report.Changes)
			} else {
				require.Len(t, report.Changes, 1)
				assert.Equal(t, rootNew, report.Changes[0].ToModel)
			}
			// Reopen through a separate real repository, rather than accepting an
			// in-memory report or an event callback as persistence evidence.
			reopened, err := adaptsqlite.Open(ds.cfg.Database.Path)
			require.NoError(t, err)
			defer reopened.Close()
			view, err := adaptsqlite.NewModelCatalogRepo(reopened).Snapshot(ctx)
			require.NoError(t, err)
			policy, err = ds.IdentityPolicy.Get(ctx)
			require.NoError(t, err)
			offerings := map[string]string{}
			for _, sl := range view.Catalog.Slices {
				for _, e := range sl.Entries {
					assert.Equal(t, policy.Revision, e.PolicyRevision)
					if e.CanonicalID == rootNew {
						offerings[e.Provider] = e.Tier
					}
				}
			}
			assert.Equal(t, map[string]string{"ollama-cloud": "pro", "openrouter": "flash"}, offerings, "both source offerings must survive normalization and reopen")
			var proofs []modelcatalog.UsabilityEvidence
			for _, ev := range view.Evidence {
				assert.Equal(t, "openrouter", ev.Provider, "provenance comes from the checked Entry, not the first canonical offering")
				if ev.CanonicalID == rootNew {
					proofs = append(proofs, ev)
					assert.True(t, ev.Confirmed())
					assert.Equal(t, rootNew, ev.NativeModel)
				}
			}
			require.NotEmpty(t, proofs)
			var event modelcatalog.AnnouncementPayload
			frozen := catalogFrozenPayloads(t, ds)
			require.Contains(t, frozen, rootNew)
			require.NoError(t, json.Unmarshal([]byte(frozen[rootNew]), &event))
			assert.Equal(t, "openrouter", event.Provider)
			assert.Equal(t, "flash", event.Tier)
			assert.Equal(t, proofs, event.Paths)
			assert.Equal(t, report.Changes, event.Changes)
			require.NotEmpty(t, view.ProbeCursor)
			lifecycleExecute(t, ds, ctx, want)
			_, err = ds.modelRefresh.coordinator.Refresh(ctx)
			require.NoError(t, err)
			assert.Equal(t, frozen, catalogFrozenPayloads(t, ds), "later cycles cannot rewrite the original payload")
		})
	}
}
