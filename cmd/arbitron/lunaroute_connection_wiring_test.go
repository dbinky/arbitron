package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lunaRouteMCP boots the real composition root and its production MCP deps.
func lunaRouteMCP(t *testing.T) (*domainStack, func(string, map[string]any) (bool, string)) {
	t.Helper()
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	ctx := context.Background()
	ds, err := initDomain(ctx, modelStateConfig(t, filepath.Join(t.TempDir(), "arbitron.db")))
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(ctx) })
	return ds, lunaRouteToolCaller(t, ds)
}

func lunaRouteToolCaller(t *testing.T, ds *domainStack) func(string, map[string]any) (bool, string) {
	t.Helper()
	return mcpToolCallerAs(t, ds, adminCtxForLocalUser(t, ds))
}

func mcpToolCallerAs(t *testing.T, ds *domainStack, ctx context.Context) func(string, map[string]any) (bool, string) {
	t.Helper()
	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	return func(name string, args map[string]any) (bool, string) {
		t.Helper()
		req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, req))
		require.NoError(t, err)
		return decodeLunaRouteMCPResult(t, data)
	}
}

func decodeLunaRouteMCPResult(t *testing.T, data []byte) (bool, string) {
	t.Helper()
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
		Error  any                  `json:"error"`
	}
	// Keep mcp-go's decoder: it materializes concrete TextContent values.
	require.NoError(t, json.Unmarshal(data, &resp))
	require.Nil(t, resp.Error, string(data))
	text := ""
	if len(resp.Result.Content) > 0 {
		if tc, ok := resp.Result.Content[0].(gomcp.TextContent); ok {
			text = tc.Text
		}
	}
	require.NotEmpty(t, text, "tools/call must return nonempty decoded text content")
	return resp.Result.IsError, text
}

func TestLunaRouteConnectionWriteWiring(t *testing.T) {
	ds, _ := lunaRouteMCP(t)
	var logs bytes.Buffer
	ds.logger = slog.New(slog.NewTextHandler(&logs, nil))
	call := lunaRouteToolCaller(t, ds)
	ctx := context.Background()
	row := func(name string) (string, string, bool) {
		var p, u string
		err := ds.db.QueryRow("SELECT provider, base_url FROM connections WHERE name = ?", name).Scan(&p, &u)
		return p, u, err == nil
	}

	isErr, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": "sk-or-v1-foreignkey"})
	assert.True(t, isErr)
	assert.Contains(t, text, `"lr_"`)
	assert.NotContains(t, text, "sk-or-v1-foreignkey")
	_, _, exists := row("lr")
	assert.False(t, exists, "refused create persisted nothing")

	for _, key := range []string{"lr_", "lr_\u2003", "lr_live\n", "lr_live\u0085"} {
		isErr, _ = call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": key})
		assert.True(t, isErr)
	}
	for _, base := range []string{"https://openrouter.ai/api/v1", "https://gw.lunaroute.com:"} {
		isErr, text = call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": base, "api_key": "lr_ok"})
		assert.True(t, isErr)
		assert.Contains(t, text, "https://gw.lunaroute.com")
	}
	_, _, exists = row("lr")
	assert.False(t, exists)

	isErr, text = call("connection_create", map[string]any{"name": "lr", "provider": " LunaRoute ", "base_url": "", "api_key": "lr_live_one", "extras": map[string]any{"region": "distinctive-preserved-region"}})
	require.False(t, isErr, text)
	var created struct {
		Connection struct {
			Provider string `json:"provider"`
			BaseURL  string `json:"base_url"`
		} `json:"connection"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &created))
	assert.Equal(t, "https://gw.lunaroute.com", created.Connection.BaseURL)
	assert.Equal(t, "lunaroute", created.Connection.Provider)
	assert.NotContains(t, text, "lr_live_one")
	p, u, _ := row("lr")
	assert.Equal(t, "lunaroute", p)
	assert.Equal(t, "https://gw.lunaroute.com", u)
	key, err := ds.connStore.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)
	var cipher, nonce []byte
	require.NoError(t, ds.db.QueryRow("SELECT api_key_cipher, api_key_nonce FROM connections WHERE name = 'lr'").Scan(&cipher, &nonce))
	assert.NotEmpty(t, cipher)
	assert.NotEmpty(t, nonce)
	assert.NotContains(t, string(cipher), key)

	// Omitted extras must flow through the real service's UpdatePatch merge.
	isErr, text = call("connection_update", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "https://gw.lunaroute.com/v1"})
	require.False(t, isErr, text)
	assert.Contains(t, text, "distinctive-preserved-region")
	got, err := ds.connStore.Get(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"region": "distinctive-preserved-region"}, got.Extras)
	assert.Equal(t, "https://gw.lunaroute.com", got.BaseURL)
	key, err = ds.connStore.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)

	isErr, text = call("connection_update", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": "sk-update-foreign"})
	assert.True(t, isErr)
	assert.NotContains(t, text, "sk-update-foreign")
	key, err = ds.connStore.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)

	isErr, text = call("connection_create", map[string]any{"name": "or", "provider": "openrouter", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or-v1-foreign"})
	require.False(t, isErr, text)
	isErr, text = call("connection_update", map[string]any{"name": "or", "provider": "lunaroute", "base_url": "", "api_key": ""})
	assert.True(t, isErr)
	assert.Contains(t, text, "lr_ api_key")
	p, _, _ = row("or")
	assert.Equal(t, "openrouter", p)

	isErr, text = call("connection_update", map[string]any{"name": "or", "provider": "lunaroute", "base_url": "", "api_key": "lr_switch"})
	require.False(t, isErr, text)
	p, u, _ = row("or")
	assert.Equal(t, "lunaroute", p)
	assert.Equal(t, "https://gw.lunaroute.com", u)
	key, err = ds.connStore.Reveal(ctx, "or")
	require.NoError(t, err)
	assert.Equal(t, "lr_switch", key)
	for _, secret := range []string{"sk-or-v1-foreignkey", "lr_live_one", "sk-or-v1-foreign", "sk-update-foreign", "lr_switch"} {
		assert.NotContains(t, logs.String(), secret)
	}
}

func TestLunaRouteMCPResultDecoder(t *testing.T) {
	for _, isError := range []bool{false, true} {
		data, err := json.Marshal(map[string]any{"result": map[string]any{"isError": isError, "content": []any{map[string]any{"type": "text", "text": `{"distinctive":"sink"}`}}}})
		require.NoError(t, err)
		gotError, text := decodeLunaRouteMCPResult(t, data)
		assert.Equal(t, isError, gotError)
		assert.JSONEq(t, `{"distinctive":"sink"}`, text)
	}
}

// Test the actual require assertion in a child process: malformed content must
// fail the test, not turn into an apparently successful empty tool response.
func TestLunaRouteMCPResultDecoderRejectsMissingText(t *testing.T) {
	if data := os.Getenv("ARBITRON_TEST_LR_MCP_RESPONSE"); data != "" {
		decodeLunaRouteMCPResult(t, []byte(data))
		return
	}
	for _, data := range []string{
		`{"result":{"content":[]}}`,
		`{"result":{"content":[{"type":"image","data":"aW1n","mimeType":"image/png"}]}}`,
		`{"result":{"content":[{"type":"text","text":""}]}}`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLunaRouteMCPResultDecoderRejectsMissingText$", "-test.count=1")
		cmd.Env = append(os.Environ(), "ARBITRON_TEST_LR_MCP_RESPONSE="+data)
		out, err := cmd.CombinedOutput()
		cancel()
		require.Error(t, err, "invalid content must fail helper assertion: %s", out)
		assert.Contains(t, string(out), "tools/call must return nonempty decoded text content")
	}
}
