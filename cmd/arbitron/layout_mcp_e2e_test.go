package main

import (
	"context"
	"encoding/json"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
)

// callMCPToolE2E dispatches a tools/call JSON-RPC message through the real
// mcp-go server with the given identity context (mirrors the adapter tests'
// callToolWithIdentity; the HTTP /mcp path injects the identity the same way).
func callMCPToolE2E(t *testing.T, srv *adaptmcp.Server, ctx context.Context, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": req.Params})
	require.NoError(t, err)
	result := srv.MCPServer().HandleMessage(ctx, json.RawMessage(msg))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func mcpE2EText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	return text.Text
}

func newLayoutE2EMCPServer(t *testing.T, ds *domainStack) *adaptmcp.Server {
	t.Helper()
	deps := ds.mcpDeps()
	srv := adaptmcp.NewServerWithDeps("t", "0", deps)
	adaptmcp.RegisterAllTools(srv)
	return srv
}

// SINK = the MCP tool result. Fails if mcpDeps drops Layouts (the summary would
// say "layout service not configured") or if the summary is not keyed by the
// revision the save path wrote to disk.
func TestLayoutMCPE2E_WorkflowGetArrangementMatchesManifestRevision(t *testing.T) {
	ds := bootQueryStack(t)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "mcp-wf", queryE2EWorkflow("mcp-wf", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "mcp-wf")

	res := callMCPToolE2E(t, srv, ctx, "workflow_get", map[string]any{"name": "mcp-wf"})
	require.False(t, res.IsError, mcpE2EText(t, res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &out))

	assert.Equal(t, "mcp-wf", out["name"])
	assert.Equal(t, rev, out["presentation"].(map[string]any)["revision"])
	arr, ok := out["arrangement"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, rev, arr["savedRevision"])
	assert.NotEqual(t, "layout service not configured", arr["availabilityError"], "mcpDeps must wire Deps.Layouts")
	assert.NotEqual(t, "unavailable", arr["displayStatus"], "the real SQLite status store answers: %v", arr["availabilityError"])
	assert.NotEqual(t, "legacy", arr["displayStatus"])
	_, hasGeometry := arr["geometry"]
	assert.False(t, hasGeometry)
}

func TestLayoutMCPE2E_RetryToolsThroughCompositionRoot(t *testing.T) {
	ds := bootQueryStack(t)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "mcp-retry", queryE2EWorkflow("mcp-retry", "Start")))

	res := callMCPToolE2E(t, srv, ctx, "workflow_layout_retry", map[string]any{"name": "mcp-retry", "expectedRevision": "stale-revision"})
	assert.True(t, res.IsError)
	assert.Contains(t, mcpE2EText(t, res), "layout revision conflict: expected stale-revision is not current")

	id, _, err := ds.eng.Trigger(ctx, "mcp-retry", map[string]any{}, "normal")
	require.NoError(t, err)
	require.NotEmpty(t, snapshotRevision(t, ds, id))

	res = callMCPToolE2E(t, srv, ctx, "instance_layout_retry", map[string]any{"id": id.String(), "expectedRevision": "stale-revision"})
	assert.True(t, res.IsError)
	assert.Contains(t, mcpE2EText(t, res), "layout revision conflict")
}

// mcpDeps never stores a typed-nil Layouts for a stack without a layout
// subsystem (the existing TestDomainStack_MCPDeps_WiresSecurityFields stack).
func TestDomainStack_MCPDeps_NilLayoutStackLeavesLayoutsNil(t *testing.T) {
	ds := &domainStack{}
	assert.Nil(t, ds.mcpDeps().Layouts)
}
