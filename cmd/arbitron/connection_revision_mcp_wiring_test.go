package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnectionRevisionMCPToolWiring drives connection_create / update / delete
// through the MCP server built from the composition root's mcpDeps and asserts
// the durable connections.config_revision row and sealed key, so it fails if the
// tool, ConnectionAdmin service or revision-aware store wiring is dropped.
func TestConnectionRevisionMCPToolWiring(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	ctx := context.Background()
	ds, err := initDomain(ctx, modelStateConfig(t, filepath.Join(t.TempDir(), "arbitron.db")))
	require.NoError(t, err)
	defer ds.Close(ctx)
	admin := identity.WithIdentity(ctx, identity.Identity{Role: identity.RoleAdmin})
	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(name string, args map[string]any) gomcp.CallToolResult {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(admin, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result
	}
	revision := func() int64 {
		t.Helper()
		var r int64
		require.NoError(t, ds.db.QueryRow("SELECT config_revision FROM connections WHERE name = 'mcp-rev'").Scan(&r))
		return r
	}
	base := map[string]any{"name": "mcp-rev", "provider": "openrouter", "base_url": "https://openrouter.ai/api/v1"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	require.False(t, call("connection_create", with(map[string]any{"api_key": "key-one"})).IsError)
	assert.Equal(t, int64(1), revision())
	require.False(t, call("connection_update", with(map[string]any{"base_url": "https://openrouter.ai/api/v2"})).IsError)
	assert.Equal(t, int64(2), revision(), "metadata edit through the tool bumps the stored revision")
	key, err := ds.connStore.Reveal(ctx, "mcp-rev")
	require.NoError(t, err)
	assert.Equal(t, "key-one", key, "metadata edit preserves the sealed key")
	require.False(t, call("connection_update", with(map[string]any{"api_key": "key-two"})).IsError)
	assert.Equal(t, int64(3), revision(), "credential edit through the tool bumps the stored revision")
	key, err = ds.connStore.Reveal(ctx, "mcp-rev")
	require.NoError(t, err)
	assert.Equal(t, "key-two", key)

	require.False(t, call("connection_delete", map[string]any{"name": "mcp-rev"}).IsError)
	require.False(t, call("connection_create", with(map[string]any{"api_key": "key-three"})).IsError)
	assert.Equal(t, int64(4), revision(), "a recreated connection continues above the deleted one's revision")
	key, err = ds.connStore.Reveal(ctx, "mcp-rev")
	require.NoError(t, err)
	assert.Equal(t, "key-three", key)
}
