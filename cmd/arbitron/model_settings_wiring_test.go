package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelSettingsWiringRoundTrip(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  level: debug\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0600))
	ds, err := initDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			ds.Close(context.Background())
		}
	}()
	const catalog = "https://catalog.example.test/root.json"
	const secret = "https://teams.example.test/hooks/root-secret"
	router := adapthttp.RequestLoggingMiddleware(ds.logger)(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("PUT", "/api/v1/system/settings", strings.NewReader(`{"catalogUrl":"`+catalog+`","teamsWebhookUrl":"`+secret+`"}`)))
	assert.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), secret)
	stored, err := ds.SystemSettings.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, catalog, stored.CatalogURL)
	assert.Equal(t, secret, stored.TeamsWebhookURL)
	ds.Close(context.Background())
	closed = true
	ds, err = initDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	reopened, err := ds.SystemSettings.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, catalog, reopened.CatalogURL)
	assert.Equal(t, secret, reopened.TeamsWebhookURL)
	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(name string, args map[string]any) gomcp.CallToolResult {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(admin, request))
		require.NoError(t, err)
		assert.NotContains(t, string(data), secret)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result
	}
	const mcpCatalog = "https://catalog.example.test/mcp-root.json"
	assert.False(t, call("system_settings_update", map[string]any{"catalog_url": mcpCatalog}).IsError)
	stored, err = ds.SystemSettings.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, mcpCatalog, stored.CatalogURL, "MCP must write through to SQLite")
	assert.Equal(t, secret, stored.TeamsWebhookURL)
	result := call("system_settings_get", map[string]any{})
	require.False(t, result.IsError)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	var public struct {
		CatalogURL      string `json:"catalogUrl"`
		TeamsConfigured bool   `json:"teamsConfigured"`
		TeamsWebhookURL string `json:"teamsWebhookUrl"`
	}
	require.NoError(t, json.Unmarshal([]byte(text.Text), &public))
	assert.True(t, public.TeamsConfigured)
	assert.Equal(t, mcpCatalog, public.CatalogURL)
	assert.Equal(t, config.RedactedSecret, public.TeamsWebhookURL)
	require.False(t, call("system_settings_update", map[string]any{"teams_webhook_url": ""}).IsError)
	reopened, err = ds.SystemSettings.ReadInternal(admin)
	require.NoError(t, err)
	assert.Empty(t, reopened.TeamsWebhookURL)
	// The HTTP read must observe the MCP write through the production projection.
	w = httptest.NewRecorder()
	adapthttp.NewRouterFromDeps(ds.httpDeps()).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/system/settings", nil))
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"teamsConfigured":false`)
	logs, err := os.ReadFile(filepath.Join(root, "arbitron.log"))
	require.NoError(t, err)
	assert.NotContains(t, string(logs), secret)
}

// TestModelSettingsMCPHTTPRouteWiring drives system_settings_* through the
// mounted /mcp route exactly as runServe assembles it (httpDeps plus the MCP
// handler behind the auth middleware), so the identity the middleware attaches
// is what the tool sees. It fails if the route, auth wrapping or SystemSettings
// projection is dropped.
func TestModelSettingsMCPHTTPRouteWiring(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  level: debug\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0600))
	ds, err := initDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	defer ds.Close(context.Background())

	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", "0.1.0", ds.mcpDeps())
	adaptmcp.RegisterAllTools(mcpSrv)
	deps := ds.httpDeps()
	deps.MCPHandler = mcpSrv.HTTPHandler()
	router := adapthttp.NewRouterFromDeps(deps)

	const catalog = "https://catalog.example.test/via-http-mcp.json"
	const secret = "https://teams.example.test/hooks/via-http-mcp"
	post := func(session string, payload map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, 200, w.Code, w.Body.String())
		return w
	}
	init := post("", map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "0"}}})
	session := init.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, session)
	call := func(name string, args map[string]any) string {
		t.Helper()
		w := post(session, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		assert.NotContains(t, w.Body.String(), secret)
		assert.NotContains(t, w.Body.String(), `"isError":true`, w.Body.String())
		return w.Body.String()
	}
	call("system_settings_update", map[string]any{"catalog_url": catalog, "teams_webhook_url": secret})
	stored, err := ds.SystemSettings.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, catalog, stored.CatalogURL, "MCP over HTTP must reach SQLite")
	assert.Equal(t, secret, stored.TeamsWebhookURL)
	out := call("system_settings_get", map[string]any{})
	assert.Contains(t, out, config.RedactedSecret)
	assert.Contains(t, out, catalog)
}
