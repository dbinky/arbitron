package main

import (
	"context"
	"encoding/json"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityPolicyMCPRejectsOversizedProviderListRoot(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	srv := adaptmcp.NewServerWithDeps("policy", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(expected int64, count int, pattern string) gomcp.CallToolResult {
		t.Helper()
		providers := make([]any, count)
		for i := range providers {
			providers[i] = " Anthropic "
		}
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "model_identity_policy_update", "arguments": map[string]any{"expected_revision": expected, "rules": []any{
				map[string]any{"providers": providers, "tier": " Opus ", "pattern": pattern, "version_groups": []any{1}},
			}},
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
		return response.Result
	}

	const submitted = `claude-opus-(\d+(?:\.\d+)*)`
	res := call(1, modelidentity.MaxProvidersPerRule, submitted)
	require.False(t, res.IsError, "%+v", res.Content)
	var accepted modelidentity.IdentityPolicy
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &accepted))
	require.Equal(t, modelidentity.IdentityPolicy{Revision: 2, Rules: []modelidentity.PatternRule{
		{Providers: []string{"anthropic"}, Tier: "opus", Pattern: submitted, VersionGroups: []int{1}},
	}}, accepted)

	res = call(accepted.Revision, modelidentity.MaxProvidersPerRule+1, `claude-opus-(\d+)`)
	assert.True(t, res.IsError, "normalization must not hide an oversized MCP submission: %+v", res.Content)
	require.NotEmpty(t, res.Content)
	assert.Contains(t, res.Content[0].(gomcp.TextContent).Text, "providers are required")
	internal, err := ds.IdentityPolicy.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, accepted, internal, "rejected input must leave the durable policy and revision unchanged")

	var dbPath string
	require.NoError(t, ds.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath))
	reopened, err := adaptsqlite.Open(dbPath)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, adaptsqlite.RunMigrations(reopened))
	persisted, err := adaptsqlite.NewModelIdentityPolicyRepo(reopened).Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, accepted, persisted, "reopen must retain the exact accepted submission, not the oversized edit or seed")
}
