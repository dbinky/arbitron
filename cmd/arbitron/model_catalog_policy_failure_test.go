package main

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A retained confirmation may still await its first observation after every
// advancement loses CAS. A later fetch failure must not freeze its old tier.
func TestCatalogFetchFailureDefersStalePolicyObservationRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	call := mcpToolCaller(t, ctx, ds)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	res, _ := call("profile_create", map[string]any{"alias": "worker", "kind": "chat", "connection_ref": "root", "selection_mode": "following", "follow_target": map[string]any{"provider": "openrouter", "tier": "flash"}})
	require.False(t, res.IsError, "%+v", res.Content)
	original := catalogFrozenPayloads(t, ds)
	require.Len(t, original, 1)

	// Simulate a concurrent profile writer after global-token checks, before
	// the advancement CAS. Real collection, proof writes and refusal still run.
	_, err := ds.db.Exec(`CREATE TRIGGER move_profile BEFORE INSERT ON model_usability_evidence BEGIN UPDATE profiles SET revision=revision+1 WHERE alias='worker'; END`)
	require.NoError(t, err)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew}
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	var conflicted modelcatalog.PublicRefreshReport
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &conflicted))
	require.Equal(t, "advance_conflict", conflicted.PathStatus["profile:worker"])
	require.Empty(t, conflicted.Changes)
	_, err = ds.db.Exec(`DROP TRIGGER move_profile`)
	require.NoError(t, err)
	before, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	confirmed := false
	for _, ev := range before.Evidence {
		confirmed = confirmed || ev.NativeModel == rootNew && ev.Confirmed()
	}
	require.True(t, confirmed, "the deferred observation must have real durable proof")
	require.Equal(t, original, catalogFrozenPayloads(t, ds))

	res, _ = call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": []any{map[string]any{"providers": []any{"openrouter"}, "tier": "custom", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	leaf.mu.Lock()
	leaf.down = true
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	var failed modelcatalog.PublicRefreshReport
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &failed))
	assert.Equal(t, "fetch_failed", failed.PathStatus["catalog"])
	assert.Equal(t, original, catalogFrozenPayloads(t, ds), "a failed fetch cannot freeze a superseded tier")
	after, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, before.Catalog, after.Catalog)
	assert.Equal(t, before.Evidence, after.Evidence)
	var observations int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_first_observations`).Scan(&observations))
	assert.Equal(t, 1, observations, "deferral must leave the lifetime identity unreserved")

	leaf.mu.Lock()
	leaf.down = false
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	rows, err := ds.db.Query(`SELECT payload FROM model_announcement_events ORDER BY canonical_id`)
	require.NoError(t, err)
	defer rows.Close()
	payloads := map[string]modelcatalog.AnnouncementPayload{}
	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))
		var payload modelcatalog.AnnouncementPayload
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		payloads[payload.CanonicalID] = payload
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, payloads, 2)
	assert.Equal(t, "openrouter", payloads["deepseek/deepseek-v4.1-flash"].Provider)
	assert.Equal(t, "custom", payloads["deepseek/deepseek-v4.1-flash"].Tier)
	assert.Empty(t, payloads["deepseek/deepseek-v4.1-flash"].Changes)
	assert.Equal(t, original["deepseek/deepseek-v4-flash"], catalogFrozenPayloads(t, ds)["deepseek/deepseek-v4-flash"], "previously frozen events retain their original bytes")
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&observations))
	assert.Equal(t, 2, observations, "recovery creates only one event per canonical identity")
}
