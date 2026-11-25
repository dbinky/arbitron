package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paired:13. One lab model confirmed on every catalog provider that carries it,
// each under its own routing spelling, is one canonical identity end to end:
// real normalization and probes stamp the same key the repo uses for lifetime
// observation and event uniqueness, so the receiver gets one card per model.
// The card's tier is the confirming entry's tier under the policy revision that
// classified it, and a later re-tier neither re-announces nor rewrites it.
func TestIdentityCrossProviderConfirmationAnnouncesOnceRoot(t *testing.T) {
	const deepseek, glm = "deepseek/deepseek-v4.1-flash", "zhipuai/glm-5.1"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" || r.Header.Get("Authorization") != "Bearer cross-key" {
			http.Error(w, "missing execution input", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds, _ := bootSinkRootWithClose(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	connections := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	carriers := []string{"deepseek", "ollama-cloud", "openrouter", "zhipuai"}
	for _, name := range carriers {
		require.NoError(t, connections.Create(ctx, provider.Connection{Name: "cross-" + name, Provider: name, BaseURL: upstream.URL}, "cross-key"))
	}
	catalogURL, webhook := "https://catalog.example.test/cross.json", "https://hooks.example.test/teams/CROSS"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL, TeamsWebhookURL: &webhook})
	require.NoError(t, err)

	receiver := &receiverTransport{}
	deps := ds.refreshDeps
	deps.AnnouncementClient = &http.Client{Transport: receiver}
	deps.Fetcher = identityCatalogFetcher{url: catalogURL, body: `{
 "deepseek":{"id":"deepseek","models":{"deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash"}}},
 "ollama-cloud":{"id":"ollama-cloud","models":{
   "ollama-cloud/deepseek-v4.1-flash":{"id":"ollama-cloud/deepseek-v4.1-flash"},
   "ollama-cloud/glm-5.1":{"id":"ollama-cloud/glm-5.1"}}},
 "openrouter":{"id":"openrouter","models":{"deepseek/deepseek-v4.1-flash":{"id":"deepseek/deepseek-v4.1-flash"}}},
 "zhipuai":{"id":"zhipuai","models":{"glm-5.1":{"id":"glm-5.1"}}}
}`}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	t.Cleanup(refresh.stop)
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.NewlyUsable, 2, "one first observation per canonical model, not per offering")

	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	policy, err := ds.IdentityPolicy.Snapshot(ctx)
	require.NoError(t, err)
	// Direct deepseek and zhipuai connections have no chat lane, so their
	// probes record unknown; their proof must still key to the same identity.
	type seen struct{ probed, confirmed []string }
	want := map[string]seen{
		deepseek: {probed: []string{"deepseek", "ollama-cloud", "openrouter"}, confirmed: []string{"ollama-cloud", "openrouter"}},
		glm:      {probed: []string{"ollama-cloud", "zhipuai"}, confirmed: []string{"ollama-cloud"}},
	}
	probedOn := map[string][]string{}
	for _, proof := range view.Evidence {
		probedOn[proof.CanonicalID] = append(probedOn[proof.CanonicalID], proof.Provider)
	}
	assert.Len(t, probedOn, 2, "no provider-specific identity in stored proof: %v", probedOn)
	frozen := map[string]modelcatalog.AnnouncementPayload{}
	for canonical, providers := range want {
		slices.Sort(probedOn[canonical])
		assert.Equal(t, providers.probed, slices.Compact(probedOn[canonical]), "%s: every carrier's proof keys to one identity", canonical)
		var events int
		require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_first_observations WHERE canonical_id = ?`, canonical).Scan(&events))
		assert.Equal(t, 1, events, "%s: one lifetime observation", canonical)
		var raw string
		require.NoError(t, ds.db.QueryRow(`SELECT payload FROM model_announcement_events WHERE canonical_id = ?`, canonical).Scan(&raw))
		var payload modelcatalog.AnnouncementPayload
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		frozen[canonical] = payload
		// Evidence from every carrier keys to the catalog's canonical ID, so all
		// confirmations land in one event instead of one per provider.
		var confirmedOn []string
		for _, proof := range payload.Paths {
			assert.Equal(t, canonical, proof.CanonicalID)
			confirmedOn = append(confirmedOn, proof.Provider)
		}
		slices.Sort(confirmedOn)
		assert.Equal(t, providers.confirmed, slices.Compact(confirmedOn), "%s: every carrier's confirmation is in the one event", canonical)
		require.Contains(t, providers.confirmed, payload.Provider, "%s: the card names a confirming offering", canonical)
		var native string
		for _, proof := range payload.Paths {
			if proof.Provider == payload.Provider {
				native = proof.NativeModel
				break
			}
		}
		confirming := entryOf(t, view.Catalog.Slices, payload.Provider, native)
		assert.Equal(t, canonical, confirming.CanonicalID)
		assert.Equal(t, policy.Revision, confirming.PolicyRevision, "%s: the confirming entry was classified under the current policy", canonical)
		assert.Equal(t, confirming.Tier, payload.Tier, "%s: the card's tier is the confirming entry's tier", canonical)
	}
	var total int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&total))
	assert.Equal(t, 2, total, "no provider-specific events")
	require.Equal(t, 2, receiver.count(), "one card per canonical model")

	// Re-tier every DeepSeek offering and refresh: canonical identity does not
	// depend on policy, so nothing is observed or announced again and the
	// frozen tier keeps the revision that classified the confirming entry.
	res, _ := mcpToolCaller(t, ctx, ds)("model_identity_policy_update", map[string]any{"expected_revision": policy.Revision, "rules": []any{map[string]any{"providers": []any{"deepseek", "ollama-cloud", "openrouter"}, "tier": "retiered", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	view, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, "retiered", entryOf(t, view.Catalog.Slices, "openrouter", deepseek).Tier, "the edit reached classification")
	for canonical, before := range frozen {
		var raw string
		require.NoError(t, ds.db.QueryRow(`SELECT payload FROM model_announcement_events WHERE canonical_id = ?`, canonical).Scan(&raw))
		var after modelcatalog.AnnouncementPayload
		require.NoError(t, json.Unmarshal([]byte(raw), &after))
		assert.Equal(t, before.Tier, after.Tier, "%s: the frozen tier is not rewritten", canonical)
	}
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&total))
	assert.Equal(t, 2, total, "a re-tier creates no event")
	assert.Equal(t, 2, receiver.count(), "a re-tier sends no card")
}
