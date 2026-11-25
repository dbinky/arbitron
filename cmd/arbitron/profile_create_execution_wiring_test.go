package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpToolCaller drives the registered MCP tools of the root-assembled stack.
func mcpToolCaller(t *testing.T, ctx context.Context, ds *domainStack) func(string, map[string]any) (gomcp.CallToolResult, string) {
	t.Helper()
	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	return func(name string, args map[string]any) (gomcp.CallToolResult, string) {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result, fmt.Sprint(response.Result.Content)
	}
}

// MCP profile_create → durable row → executed model. A following profile made
// with no model starts on the newest usable catalog model, and a run triggered on
// it sends exactly that model to the provider endpoint (the sink). A pinned
// create is held to the trigger's fixed-version grammar before it persists: a
// mutable alias or no model is refused and stores nothing, a fixed version
// stores and runs, and clearing that model later is refused.
func TestProfileCreatedThroughMCPExecutesSelectedModel(t *testing.T) {
	const newModel, pinnedModel = "deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4-flash"
	ds := bootSinkStack(t)
	sink := newModelSink(t)
	drainCtx, stopDrain := context.WithCancel(context.Background())
	drained := make(chan struct{})
	go func() { // discovery probes also land on the sink; keep its arrival channel from filling
		defer close(drained)
		for {
			select {
			case <-sink.arrived:
			case <-drainCtx.Done():
				return
			}
		}
	}()
	defer func() { stopDrain(); <-drained }()
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	catalogURL := "https://catalog.example.test/api.json"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)
	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)
	call := mcpToolCaller(t, ctx, ds)

	// A mutable alias never persists, through the real route inventory.
	res, text := call("profile_create", map[string]any{"alias": "pin-alias", "kind": "chat", "connection_ref": "sink", "model": "gpt-latest"})
	require.True(t, res.IsError, text)
	assert.Contains(t, text, "fixed model version")
	_, err = ds.profileSvc.Get(ctx, "pin-alias")
	require.ErrorIs(t, err, provider.ErrProfileNotFound, "a refused pinned create stores nothing")
	// Trigger refuses an empty model, so a model-less pinned create is refused
	// at write instead of failing every later run (paired:12).
	res, text = call("profile_create", map[string]any{"alias": "pin-alias", "kind": "chat", "connection_ref": "sink"})
	require.True(t, res.IsError, text)
	assert.Contains(t, text, "fixed model version")
	_, err = ds.profileSvc.Get(ctx, "pin-alias")
	require.ErrorIs(t, err, provider.ErrProfileNotFound)

	run := func(wantModel string) {
		t.Helper()
		before := len(sink.seen())
		id, _, err := ds.eng.Trigger(ctx, "pin-llm", map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
		assert.Equal(t, []string{wantModel, wantModel}, sink.seen()[before:], "both llm_call nodes send the profile's model")
	}

	res, text = call("profile_create", map[string]any{
		"alias": "pin-alias", "kind": "chat", "connection_ref": "sink",
		"selection_mode": "following",
		"follow_target":  map[string]any{"provider": "openrouter", "tier": "flash"},
	})
	require.False(t, res.IsError, text)
	stored, err := ds.profileSvc.Get(ctx, "pin-alias")
	require.NoError(t, err)
	require.Equal(t, newModel, stored.Model)
	run(newModel)

	// Switching the same alias to a pinned fixed version is accepted and runs.
	res, text = call("profile_update", map[string]any{"alias": "pin-alias", "selection_mode": "pinned", "follow_target": nil, "model": pinnedModel})
	require.False(t, res.IsError, text)
	run(pinnedModel)

	// Clearing the pinned model is refused and the stored version keeps running.
	res, text = call("profile_update", map[string]any{"alias": "pin-alias", "model": ""})
	require.True(t, res.IsError, text)
	assert.Contains(t, text, "fixed model version")
	run(pinnedModel)
}
