package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identityCatalogFetcher struct{ url, body string }

func (f identityCatalogFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if url != f.url {
		return nil, fmt.Errorf("unexpected identity catalog URL %q", url)
	}
	return []byte(f.body), nil
}

// The prefixed rows are routing-spelling mutations of D2 examples, not new
// upstream provenance. Real normalization, persisted policy, probes, SQL and
// execution must agree on the canonical identity without rewriting that spelling.
func TestIdentityRoutingPrefixRoot(t *testing.T) {
	const oldModel = "ollama-cloud/deepseek-v4-flash"
	const newModel = "ollama-cloud/deepseek-v4.1-flash"
	const canonical = "deepseek/deepseek-v4.1-flash"
	var mu sync.Mutex
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string            `json:"model"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 ||
			r.Header.Get("Authorization") != "Bearer identity-key" ||
			body.Model != newModel {
			http.Error(w, "missing execution input", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"identity output"},"finish_reason":"stop"}]}`, newModel)
	}))
	defer upstream.Close()
	ds, closeStack := bootSinkRootWithClose(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: identity-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: ollama-cloud
      tier: flash
`, func(workspace string) {
		writeWorkflow(t, workspace, "identity-exec", `{"name":"identity-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "identity-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx,
		provider.Connection{Name: "identity-sink", Provider: "ollama-cloud", BaseURL: upstream.URL}, "identity-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	catalogURL, webhook := "https://catalog.example.test/identity.json", "https://hooks.example.test/teams/IDENTITY"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL, TeamsWebhookURL: &webhook})
	require.NoError(t, err)

	// Only the ollama-cloud connection exists, so the model is confirmed there
	// alone although deepseek sorts first among its catalog offerings (G9).
	receiver := &receiverTransport{}
	deps := ds.refreshDeps
	deps.AnnouncementClient = &http.Client{Transport: receiver}
	deps.Fetcher = identityCatalogFetcher{url: catalogURL, body: `{
 "deepseek":{"id":"deepseek","models":{"deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash"}}},
 "ollama-cloud":{"id":"ollama-cloud","models":{
   "ollama-cloud/deepseek-v4-flash":{"id":"ollama-cloud/deepseek-v4-flash"},
   "ollama-cloud/deepseek-v4.1-flash":{"id":"ollama-cloud/deepseek-v4.1-flash"},
   "openai/deepseek-v9-flash":{"id":"openai/deepseek-v9-flash"}}},
 "openrouter":{"id":"openrouter","models":{"deepseek/deepseek-v4.1-flash":{"id":"deepseek/deepseek-v4.1-flash"}}}
}`}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	t.Cleanup(refresh.stop)
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, newModel, report.Changes[0].ToModel)

	assertCatalog := func(stack *domainStack) {
		t.Helper()
		view, err := stack.catalogStore.Snapshot(ctx)
		require.NoError(t, err)
		for _, row := range []struct{ provider, native string }{
			{"deepseek", "deepseek-v4.1-flash"}, {"ollama-cloud", newModel}, {"openrouter", canonical},
		} {
			entry := entryOf(t, view.Catalog.Slices, row.provider, row.native)
			assert.Equal(t, canonical, entry.CanonicalID)
			assert.Equal(t, row.provider, entry.Provider)
			assert.Equal(t, row.native, entry.NativeID)
		}
		foreign := entryOf(t, view.Catalog.Slices, "ollama-cloud", "openai/deepseek-v9-flash")
		assert.Empty(t, foreign.Tier)
		assert.Empty(t, foreign.OrderKey)
		assert.False(t, foreign.Immutable)
		var events int
		require.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events WHERE canonical_id = ?`, canonical).Scan(&events))
		assert.Equal(t, 1, events, "exactly one durable event for the confirmed lab model")
		require.NoError(t, stack.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
		assert.Equal(t, 1, events, "other offerings must not create separate canonical events")
		var raw string
		require.NoError(t, stack.db.QueryRow(`SELECT payload FROM model_announcement_events WHERE canonical_id = ?`, canonical).Scan(&raw))
		var payload struct{ Provider, Tier string }
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		assert.Equal(t, "ollama-cloud", payload.Provider, "the frozen event names the confirming provider")
		assert.Equal(t, "flash", payload.Tier)
		// Discovery probes every catalog row on the path, so the working model and
		// the unrelated offering carry evidence too; only the lab model's proofs
		// are held to the routing-prefixed spelling.
		var proven int
		for _, proof := range view.Evidence {
			if proof.CanonicalID != canonical {
				continue
			}
			proven++
			assert.Equal(t, newModel, proof.NativeModel)
			assert.Equal(t, "ollama-cloud", proof.Provider, "proof records the catalog provider it confirmed")
			assert.True(t, proof.Confirmed())
		}
		require.NotZero(t, proven, "the lab model must carry confirmed proof")
		stored, err := stack.profileSvc.Get(ctx, "worker")
		require.NoError(t, err)
		assert.Equal(t, newModel, stored.Model)
		assert.Equal(t, &provider.FollowTarget{Provider: "ollama-cloud", Tier: "flash"}, stored.FollowTarget)
	}
	assertCatalog(ds)
	require.Equal(t, 1, receiver.count(), "one card for the canonical model")
	assert.Contains(t, receiver.bodies[0], `{"title":"Provider","value":"ollama-cloud"}`)
	assert.NotContains(t, receiver.bodies[0], `{"title":"Provider","value":"deepseek"}`)

	// Canonical identity is policy-independent: an admin re-tier through MCP
	// and a refresh neither observe nor announce the model again, and the frozen
	// tier is not rewritten (assertCatalog still reads "flash" after reopen).
	policy, err := ds.IdentityPolicy.Snapshot(ctx)
	require.NoError(t, err)
	res, _ := mcpToolCaller(t, ctx, ds)("model_identity_policy_update", map[string]any{"expected_revision": policy.Revision, "rules": []any{map[string]any{"providers": []any{"deepseek", "ollama-cloud", "openrouter"}, "tier": "pro", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, "pro", entryOf(t, view.Catalog.Slices, "ollama-cloud", newModel).Tier, "the edit reached classification")
	assert.Equal(t, 1, receiver.count(), "a re-tier does not announce the model again")

	// A policy read failure must stop advancement, not explicit-version pins or
	// execution on the already published model. Restore the table before reopen.
	_, err = ds.db.Exec(`ALTER TABLE model_identity_policy RENAME TO unavailable_identity_policy`)
	require.NoError(t, err)
	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Changes)
	assert.Equal(t, "parse_error", report.PathStatus["catalog"])
	mu.Lock()
	requests = nil
	mu.Unlock()
	run, _, err := ds.eng.Trigger(ctx, "identity-exec", nil, "normal")
	require.NoError(t, err)
	saved := waitTerminal(t, ds, run)
	assert.Equal(t, instance.StateCompleted, saved.State)
	assert.Equal(t, newModel, saved.ModelPins["worker"])
	trace, err := ds.nodeExecRepo.ListByInstance(ctx, run)
	require.NoError(t, err)
	require.Len(t, trace, 1)
	assert.Contains(t, fmt.Sprint(trace[0].Output), "identity output")
	mu.Lock()
	assert.Equal(t, []string{newModel}, requests, "the final execution preserves routing spelling")
	mu.Unlock()
	_, err = ds.db.Exec(`ALTER TABLE unavailable_identity_policy RENAME TO model_identity_policy`)
	require.NoError(t, err)

	configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
	refresh.stop()
	closeStack()
	reopened, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { reopened.Close(context.Background()) })
	assertCatalog(reopened)
	assert.Equal(t, 1, receiver.count(), "neither the failed cycle nor reopen re-sends the card")
}
