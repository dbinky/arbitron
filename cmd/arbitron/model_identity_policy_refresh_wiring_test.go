package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An admin policy edit through the production MCP tool re-tiers the catalog on
// the NEXT refresh: the stored Entry carries the new Tier, OrderKey and
// PolicyRevision, and the durable selection (profiles.model, then the new run's
// upstream request body) moves only because of that persisted edit.
func TestIdentityPolicyEditDrivesRefreshToDurableSelection(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, `profiles:
  - alias: worker
    kind: chat
    backend: claude-code
    connectionref: policy-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: fast
`, func(workspace string) {
		writeWorkflow(t, workspace, "policy-exec", `{"name":"policy-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "policy-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "policy-sink", Provider: "openrouter", BaseURL: upstream.URL}, "policy-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	catalogURL := "https://catalog.example.test/api.json"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)

	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()

	// Shipped policy: the openrouter deepseek models are tier "flash", so the
	// "fast" target has no match and the working model stays.
	report, err := refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	assert.Empty(t, report.Changes)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	assert.Contains(t, view.UnmatchedTargets, provider.FollowTarget{Provider: "openrouter", Tier: "fast"})
	assert.Equal(t, int64(1), entryOf(t, view.Catalog.Slices, "openrouter", newModel).PolicyRevision)
	assert.Equal(t, "flash", entryOf(t, view.Catalog.Slices, "openrouter", newModel).Tier)

	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "model_identity_policy_update",
		"arguments": map[string]any{"expected_revision": 1, "rules": []any{map[string]any{
			"providers": []any{"openrouter"}, "tier": "fast", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}},
	}})
	require.NoError(t, err)
	data, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, request))
	require.NoError(t, err)
	var response struct {
		Result gomcp.CallToolResult `json:"result"`
		Error  any                  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(data, &response))
	require.Nil(t, response.Error, string(data))
	require.False(t, response.Result.IsError, string(data))

	report, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, newModel, report.Changes[0].ToModel)
	view, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	entry := entryOf(t, view.Catalog.Slices, "openrouter", newModel)
	assert.Equal(t, "fast", entry.Tier)
	assert.Equal(t, "4.1", entry.OrderKey)
	assert.Equal(t, int64(2), entry.PolicyRevision)
	assert.NotContains(t, view.UnmatchedTargets, provider.FollowTarget{Provider: "openrouter", Tier: "fast"})

	stored, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, newModel, stored.Model)
	run, _, err := ds.eng.Trigger(ctx, "policy-exec", nil, "normal")
	require.NoError(t, err)
	done := waitTerminal(t, ds, run)
	assert.Equal(t, instance.StateCompleted, done.State)
	assert.Equal(t, newModel, done.ModelPins["worker"])
	mu.Lock()
	assert.Contains(t, models, newModel, "the new run's request body names the policy-selected model")
	mu.Unlock()
}

func entryOf(t *testing.T, slices []modelcatalog.CatalogSlice, providerName, nativeID string) modelidentity.Entry {
	t.Helper()
	for _, s := range slices {
		if s.Provider != providerName {
			continue
		}
		for _, e := range s.Entries {
			if e.NativeID == nativeID {
				return e
			}
		}
	}
	require.FailNow(t, "catalog entry not stored", "%s/%s", providerName, nativeID)
	return modelidentity.Entry{}
}
