package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentityPolicyRootWiring drives the real initDomain: the policy tools are
// registered from the production Deps projection, serve the persisted D2 table,
// and an edit written to SQLite survives a restart without being reseeded.
func TestIdentityPolicyRootWiring(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  level: debug\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0600))
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})

	ds, err := initDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	require.NotNil(t, ds.IdentityPolicy)
	require.NotNil(t, ds.mcpDeps().IdentityPolicy, "the production MCP Deps must carry the policy service")
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

	res := call("model_identity_policy_get", map[string]any{})
	require.False(t, res.IsError)
	var got modelidentity.IdentityPolicy
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(gomcp.TextContent).Text), &got))
	assert.Equal(t, int64(1), got.Revision)
	assert.Equal(t, modelidentity.InitialRules(), got.Rules)

	// The shared ExecutionGate serializes the update: while a snapshot lease is
	// held the admin edit must wait, and once released it persists revision 2.
	require.NotNil(t, ds.executionGate, "initDomain must construct the shared ExecutionGate")
	leaseHeld := make(chan struct{})
	releaseLease := make(chan struct{})
	go func() {
		_ = ds.executionGate.WithRead(context.Background(), func(context.Context) error {
			close(leaseHeld)
			<-releaseLease
			return nil
		})
	}()
	<-leaseHeld
	updated := make(chan gomcp.CallToolResult, 1)
	go func() {
		updated <- call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": policyRulesJSON(`claude-opus-(\d+)`)})
	}()
	select {
	case <-updated:
		t.Fatal("policy update ran while a gate read lease was held: the policy service is not on the shared gate")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLease)
	res = <-updated
	require.False(t, res.IsError, fmt.Sprint(res.Content))
	after, err := ds.IdentityPolicy.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), after.Revision)

	edited := []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}}
	assert.Equal(t, edited, after.Rules, "the registered MCP edit reaches the internal source exactly")
	ds.Close(context.Background())

	ds, err = initDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	reopened, err := ds.IdentityPolicy.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), reopened.Revision, "restart must not reseed over an admin edit")
	assert.Equal(t, edited, reopened.Rules)
}

func policyRulesJSON(pattern string) []any {
	return []any{map[string]any{"providers": []any{"anthropic"}, "tier": "opus", "pattern": pattern, "version_groups": []any{1}}}
}
