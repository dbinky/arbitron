package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryBeyondFollowingTargetsRoot(t *testing.T) {
	const older = "deepseek/deepseek-v3-flash"
	const other = "deepseek/deepseek-v4-pro"
	const unknown = "vendor/native-preview-20261018"
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	// This earlier offering shares the confirmed model's canonical identity,
	// but has a different native spelling/tier and no configured execution path.
	fetch := catalogHTTPLeaf(func(r *http.Request) (*http.Response, error) {
		response, err := leaf.fetch(r)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		var catalog map[string]any
		if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
			return nil, err
		}
		catalog["ollama-cloud"] = map[string]any{"id": "ollama-cloud", "models": map[string]any{
			"offering-v4.1": map[string]any{"id": "offering-v4.1", "canonical_model_id": rootNew},
		}}
		raw, err := json.Marshal(catalog)
		if err != nil {
			return nil, err
		}
		return lifecycleResponse(r, string(raw)), nil
	})
	ds, ticks, intervals := buildLifecycleRootWithDependencies(t, strings.Split(lifecycleFollowing, "  - alias: other")[0], leaf, ModelDependencies{FetchTransport: fetch})
	ctx := lifecycleConnection(t, ds)
	policy, err := ds.IdentityPolicy.Get(ctx)
	require.NoError(t, err)
	policy.Rules = append(policy.Rules, modelidentity.PatternRule{
		Providers: []string{"ollama-cloud"}, Tier: "pro", Pattern: `offering-v(\d+(?:\.\d+)?)`, VersionGroups: []int{1},
	})
	_, err = ds.IdentityPolicy.Update(ctx, policy.Revision, policy.Rules)
	require.NoError(t, err)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew, older, other, unknown}
	leaf.mu.Unlock()
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	rootTick(t, ticks)
	require.Eventually(t, func() bool {
		var model string
		return ds.db.QueryRow(`SELECT model FROM profiles WHERE alias='worker'`).Scan(&model) == nil && model == rootNew
	}, 2*time.Second, time.Millisecond, "the actual path proofs must reach durable following selection")
	rootReports(t, ds, len(view.Reports)+1)
	view, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, view.HistoricalModels, 6)
	frozen := catalogFrozenPayloads(t, ds)
	require.Len(t, frozen, 5)
	for _, m := range []string{older, other, "openrouter/" + unknown} {
		var payload modelcatalog.AnnouncementPayload
		require.Contains(t, frozen, m)
		require.NoError(t, json.Unmarshal([]byte(frozen[m]), &payload))
		assert.Empty(t, payload.Changes)
		assert.NotEmpty(t, payload.Paths)
	}
	for _, e := range view.HistoricalEntries {
		if e.NativeID == unknown {
			assert.Empty(t, e.Tier)
			assert.Empty(t, e.OrderKey)
		}
	}
	var event modelcatalog.AnnouncementPayload
	require.NoError(t, json.Unmarshal([]byte(frozen[rootNew]), &event))
	assert.Equal(t, "openrouter", event.Provider)
	assert.Equal(t, "flash", event.Tier)
	require.Len(t, event.Changes, 1)
	assert.Equal(t, "worker", event.Changes[0].Alias)
	assert.Equal(t, rootOld, event.Changes[0].FromModel)
	assert.Equal(t, rootNew, event.Changes[0].ToModel)
	require.NotEmpty(t, event.Paths)
	for _, ev := range event.Paths {
		assert.Equal(t, "openrouter", ev.Provider)
		assert.Equal(t, rootNew, ev.NativeModel)
		assert.True(t, ev.Confirmed())
	}
	lifecycleExecute(t, ds, ctx, rootNew)
	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	ds.Close(context.Background())
	reopened, err := initDomainWithModelDependencies(ctx, configPath, ModelDependencies{FetchTransport: catalogHTTPLeaf(leaf.fetch), ProviderTransport: catalogHTTPLeaf(leaf.provider)})
	require.NoError(t, err)
	defer reopened.Close(context.Background())
	restored, err := reopened.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, view, restored)
	assert.Equal(t, frozen, catalogFrozenPayloads(t, reopened))
	call := mcpToolCaller(t, ctx, reopened)
	res, _ := call("model_catalog_list", nil)
	require.False(t, res.IsError)
	var public modelcatalog.PublicCatalogView
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &public))
	require.Len(t, public.Entries, 6)
	offerings := map[string]modelcatalog.PublicCatalogEntry{}
	for _, e := range public.Entries {
		assert.Equal(t, modelcatalog.UsabilityConfirmed, e.Usability)
		if e.CanonicalID == rootNew {
			offerings[e.Provider] = e
		}
	}
	require.Len(t, offerings, 2, "the registered list must preserve both source offerings after reopen")
	assert.Equal(t, "pro", offerings["ollama-cloud"].Tier)
	assert.Equal(t, "offering-v4.1", offerings["ollama-cloud"].NativeID)
	assert.Equal(t, "flash", offerings["openrouter"].Tier)
	assert.Equal(t, rootNew, offerings["openrouter"].SpecificModel)
	for _, ev := range restored.Evidence {
		if ev.CanonicalID == rootNew {
			assert.Equal(t, "openrouter", ev.Provider, "canonical usability must not invent proof for the other offering")
		}
	}
}

func TestModelProfilesColdUnavailableSourceAndWrongProofRoot(t *testing.T) {
	for _, fault := range []string{"source", "proof"} {
		t.Run(fault, func(t *testing.T) {
			leaf := &lifecycleHTTP{source: []string{rootOld}, down: fault == "source", wrong: fault == "proof"}
			ds, _, intervals := buildLifecycleRoot(t, lifecycleFollowing, leaf)
			ctx := lifecycleConnection(t, ds)
			require.NoError(t, ds.Start(ctx))
			rootTicker(t, intervals)
			var profiles, events, pending int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&profiles))
			assert.Zero(t, profiles)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
			assert.Zero(t, events)
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_seed_pending`).Scan(&pending))
			assert.Equal(t, 2, pending, "original aliases remain retryable, never silently seeded")
			call := mcpToolCaller(t, ctx, ds)
			res, _ := call("model_catalog_list", nil)
			require.False(t, res.IsError)
			text := res.Content[0].(gomcp.TextContent).Text
			if fault == "source" {
				assert.Contains(t, text, "fetch_failed")
			} else {
				assert.Contains(t, text, "unknown")
				assert.NotContains(t, text, `"usability": "confirmed"`)
			}
		})
	}
}
