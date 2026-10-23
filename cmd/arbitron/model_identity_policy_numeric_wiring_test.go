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

// The sink is the actual policy row/revision, not a handler or validation call.
// A valid submitted replacement must persist; malformed capture grammars must
// leave that exact replacement intact through a separately reopened repository.
func TestIdentityPolicyMCPRejectsMalformedNumericGrammarRoot(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	srv := adaptmcp.NewServerWithDeps("policy", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(expected int64, pattern string) gomcp.CallToolResult {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
			"name": "model_identity_policy_update", "arguments": map[string]any{"expected_revision": expected, "rules": policyRulesJSON(pattern)},
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
	res := call(1, submitted)
	require.False(t, res.IsError, "%+v", res.Content)
	var accepted modelidentity.IdentityPolicy
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &accepted))
	require.Equal(t, int64(2), accepted.Revision)
	require.Equal(t, []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: submitted, VersionGroups: []int{1}}}, accepted.Rules)

	for _, capture := range []string{`(\d*)`, `(\.+)`, `([0-9.]+)`, `(\.\d+)`, `(\d+\.)`, `(\d+\.\.\d+)`, `(\d+|\.)`} {
		t.Run(capture, func(t *testing.T) {
			before, err := ds.IdentityPolicy.Snapshot(context.Background())
			require.NoError(t, err)
			res := call(before.Revision, `claude-opus-`+capture)
			assert.True(t, res.IsError, "malformed grammar must not replace the policy: %+v", res.Content)
			require.NotEmpty(t, res.Content)
			assert.Contains(t, res.Content[0].(gomcp.TextContent).Text, "nonempty dotted numeric tuple")
			internal, err := ds.IdentityPolicy.Snapshot(context.Background())
			require.NoError(t, err)
			assert.Equal(t, before, internal, "invalid MCP update must preserve the authoritative rules/revision")
		})
	}

	var dbPath string
	require.NoError(t, ds.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath))
	reopened, err := adaptsqlite.Open(dbPath)
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, adaptsqlite.RunMigrations(reopened))
	persisted, err := adaptsqlite.NewModelIdentityPolicyRepo(reopened).Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, accepted, persisted, "reopen/migration must retain the submitted valid policy, not an invalid edit or seed")
}
