package main

import (
	"bytes"
	"context"
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
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The admin MCP tools reach the same coordinator and store the loop uses:
// model_catalog_refresh advances the following profile, the NEW run's upstream
// request names the new model, and model_catalog_list shows the redacted
// result. A non-admin is refused before any cycle runs.
func TestModelCatalogToolsRootRefreshAndList(t *testing.T) {
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
    connectionref: refresh-sink
    model: `+oldModel+`
    selection_mode: following
    follow_target:
      provider: openrouter
      tier: flash
`, func(workspace string) {
		writeWorkflow(t, workspace, "refresh-exec", `{"name":"refresh-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
		writePinTemplate(t, workspace, "refresh-exec")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
	storeConfiguredFollowing(t, ctx, ds, "worker")
	catalogURL := "https://catalog.example.test/api.json?token=URLSECRET"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)

	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	ds.modelRefresh = refresh // the stack's mcpDeps and Close use whichever refresh it holds
	srv := adaptmcp.NewServerWithDeps("catalog", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(callCtx context.Context, tool string) gomcp.CallToolResult {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]any{}}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(callCtx, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		return response.Result
	}

	res := call(context.Background(), "model_catalog_refresh")
	assert.True(t, res.IsError, "an unauthenticated refresh is refused")
	stored, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, oldModel, stored.Model)

	res = call(ctx, "model_catalog_refresh")
	require.False(t, res.IsError, "%+v", res.Content)
	var report modelcatalog.PublicRefreshReport
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &report))
	require.Len(t, report.Changes, 1)
	assert.Equal(t, newModel, report.Changes[0].ToModel)
	assert.False(t, report.PublicationPending)
	stored, err = ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, newModel, stored.Model)

	mu.Lock()
	models = nil
	mu.Unlock()
	fresh, _, err := ds.eng.Trigger(ctx, "refresh-exec", nil, "normal")
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, waitTerminal(t, ds, fresh).State)
	mu.Lock()
	assert.Equal(t, []string{newModel}, models, "the new run's request body names the advanced model")
	mu.Unlock()

	res = call(ctx, "model_catalog_list")
	require.False(t, res.IsError, "%+v", res.Content)
	text := res.Content[0].(gomcp.TextContent).Text
	var view modelcatalog.PublicCatalogView
	require.NoError(t, json.Unmarshal([]byte(text), &view))
	usability := map[string]string{}
	for _, entry := range view.Entries {
		usability[entry.NativeID] = entry.Usability
	}
	assert.Equal(t, modelcatalog.UsabilityConfirmed, usability[newModel])
	require.NotEmpty(t, view.Reports)
	assert.NotContains(t, text, "URLSECRET")
	assert.NotContains(t, text, "refresh-key")
	assert.NotContains(t, text, "catalog.example.test")
}

// Omitting the stored revision, substituting the current policy revision, or
// remapping on list would conceal stale classifications after a policy edit.
func TestModelCatalogListReportsStoredPolicyRevisionAcrossEditAndFetchFailure(t *testing.T) {
	const unknown = "unclassified-model"
	leaf := &lifecycleHTTP{source: []string{rootOld, unknown}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	call := mcpToolCaller(t, ctx, ds)
	res, _ := call("model_catalog_list", nil)
	require.False(t, res.IsError)
	var empty modelcatalog.PublicCatalogView
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &empty))
	assert.Empty(t, empty.Entries, "an unrefreshed catalog invents no classifications")
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)

	assertStoredList := func(tier string, revision int64) {
		t.Helper()
		before, err := ds.catalogStore.Snapshot(ctx)
		require.NoError(t, err)
		res, _ := call("model_catalog_list", nil)
		require.False(t, res.IsError, "%+v", res.Content)
		text := res.Content[0].(gomcp.TextContent).Text
		var view struct {
			Entries []struct {
				NativeID       string `json:"native_id"`
				Tier           string `json:"tier"`
				PolicyRevision int64  `json:"policy_revision"`
			} `json:"entries"`
		}
		require.NoError(t, json.Unmarshal([]byte(text), &view))
		require.Len(t, view.Entries, 2)
		for _, entry := range view.Entries {
			assert.Equal(t, revision, entry.PolicyRevision, "revision must describe the stored classification, including unknown tiers")
			switch entry.NativeID {
			case rootOld:
				assert.Equal(t, tier, entry.Tier)
			case unknown:
				assert.Empty(t, entry.Tier)
			default:
				t.Fatalf("unexpected catalog model %q", entry.NativeID)
			}
		}
		for _, private := range []string{"models.dev", "lifecycle-key", "source_ref", "proof_ref", ".model-probe"} {
			assert.NotContains(t, text, private)
		}
		after, err := ds.catalogStore.Snapshot(ctx)
		require.NoError(t, err)
		assert.Equal(t, before, after, "listing must not rewrite durable classifications or evidence")
	}
	assertStoredList("flash", 1)
	res, _ = call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": []any{map[string]any{"providers": []any{"openrouter"}, "tier": "custom", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	res, _ = call("model_identity_policy_get", nil)
	require.False(t, res.IsError)
	var policy struct {
		Revision int64 `json:"revision"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &policy))
	require.Equal(t, int64(2), policy.Revision)
	assertStoredList("flash", 1)

	leaf.mu.Lock()
	leaf.down = true
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	var report modelcatalog.PublicRefreshReport
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &report))
	assert.Equal(t, "fetch_failed", report.PathStatus["catalog"])
	assertStoredList("flash", 1)

	leaf.mu.Lock()
	leaf.down = false
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	assertStoredList("custom", 2)
}
