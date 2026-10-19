package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/workflow"
)

// groupOtherUser owns a private workflow the local default user can never see.
var groupOtherUser = uuid.MustParse("00000000-0000-0000-0000-0000000000b2")

// bootGroupAuthoringStack boots the REAL composition root with:
//   - "group-child": a visible private child declaring a required "topic" input;
//   - "hidden-child": another user's private workflow (invisible to the caller);
//   - "legacy-orphan": a group workflow written straight to disk (bypassing the
//     write gate, as a definition whose child later disappeared would be) whose
//     workflows task references hidden-child.
func bootGroupAuthoringStack(t *testing.T) (*domainStack, string) {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	child := `{
  "name": "group-child",
  "description": "child",
  "inputs": [{"name": "topic", "type": "string", "description": "what to do", "required": true}],
  "nodes": [
    {"id": "c1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": []
}`
	writeWorkflow(t, workspaceDir, "group-child", child)

	hiddenDir := filepath.Join(workspaceDir, "users", groupOtherUser.String(), "hidden-child")
	require.NoError(t, os.MkdirAll(hiddenDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hiddenDir, "workflow.json"),
		[]byte(`{"name":"hidden-child","description":"secret","inputs":[],"nodes":[{"id":"h","type":"transform","entrypoint":true,"config":{}}],"edges":[]}`), 0o600))

	writeWorkflow(t, workspaceDir, "legacy-orphan", `{
  "name": "legacy-orphan",
  "description": "group whose child is invisible",
  "inputs": [],
  "nodes": [
    {"id": "fan", "type": "parallel_group", "entrypoint": true,
     "config": {"group": {"form": "workflows",
       "tasks": [{"key": "a", "workflow": "hidden-child"}],
       "completionRule": "all_succeed", "unfinishedBehavior": "cancel_unfinished", "gatherMode": "results"}}},
    {"id": "done", "type": "transform", "config": {"operations": [{"variable": "ok", "operation": "set", "value": true}]}},
    {"id": "failed", "type": "transform", "config": {"operations": [{"variable": "ok", "operation": "set", "value": false}]}}
  ],
  "edges": [
    {"source": "fan", "target": "done", "output": "success"},
    {"source": "fan", "target": "failed", "output": "failure"}
  ]
}`)

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	return ds, workspaceDir
}

// newGroupMCPServer builds the production MCP server over the real stack.
func newGroupMCPServer(ds *domainStack) *adaptmcp.Server {
	srv := adaptmcp.NewServerWithDeps("arbitron", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	return srv
}

// callMCPTool sends a real JSON-RPC tools/call through the registered server.
func callMCPTool(t *testing.T, srv *adaptmcp.Server, ctx context.Context, tool string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	require.NoError(t, err)
	raw, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, json.RawMessage(msg)))
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Nil(t, resp.Error, "JSON-RPC error calling %s", tool)
	return &resp.Result
}

func mcpText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", res.Content[0])
	return tc.Text
}

// registryRows counts workflow_identity rows for name — the registry SINK.
func registryRows(t *testing.T, ds *domainStack, name string) int {
	t.Helper()
	var n int
	require.NoError(t, ds.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM workflow_identity WHERE name = ?`, name).Scan(&n))
	return n
}

func privateDir(workspaceDir, name string) string {
	return filepath.Join(workspaceDir, "users", localDefaultUser.String(), name)
}

func groupCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

type groupValidation struct {
	Valid  bool                       `json:"valid"`
	Errors []workflow.ValidationError `json:"errors"`
}

func decodeGroupValidation(t *testing.T, text string) groupValidation {
	t.Helper()
	var v groupValidation
	require.NoError(t, json.Unmarshal([]byte(text), &v), text)
	return v
}

func filesModeGroup(name, resolverProfile string) *workflow.Workflow {
	transform := func(id string) workflow.Node {
		return workflow.Node{ID: id, Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
			Operations: []workflow.Operation{{Variable: id, Operation: "set", Value: true}},
		}}
	}
	return &workflow.Workflow{
		Name: name, Description: "files-mode group", Author: "e2e",
		Nodes: []workflow.Node{
			{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel,
				GatherMode:         workflow.GroupGatherResultsAndFiles,
				ResolverProfile:    resolverProfile,
			}}},
			transform("a1"), transform("b1"), transform("done"), transform("failed"),
		},
		Edges: []workflow.Edge{
			{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "fanout", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
}

// PROVENANCE PROBE. Delete `workflowSvc.SetGroupCheckers(profileChecker,
// fileReader)` from initDomain and this fails: the service has no checkers, so
// the rejection is GROUP_CHECKER_UNAVAILABLE (not the profile-kind finding)
// and the agent-kind positive control is rejected too.
func TestGroupAuthoringE2E_FilesModeChatResolverRejectedThroughCompositionRoot(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)

	// Precondition: the seeded built-in profiles have the kinds the test relies on.
	kind, ok := ds.profileChecker.ProfileKind("default-chat")
	require.True(t, ok)
	require.Equal(t, "chat", kind)
	kind, ok = ds.profileChecker.ProfileKind("default-agent")
	require.True(t, ok)
	require.Equal(t, "agent", kind)

	// Direct service call (the boundary every adapter shares).
	err := ds.workflowSvc.Create(ctx, "files-chat", filesModeGroup("files-chat", "default-chat"))
	require.Error(t, err)
	var gve *workflow.GroupValidationError
	require.True(t, errors.As(err, &gve), "got %T: %v", err, err)
	codes := groupCodes(gve.Errors)
	assert.Contains(t, codes, "GROUP_RESOLVER_PROFILE_KIND")
	assert.NotContains(t, codes, "GROUP_CHECKER_UNAVAILABLE", "the real profile checker must be wired")
	_, statErr := os.Stat(privateDir(workspaceDir, "files-chat"))
	assert.True(t, os.IsNotExist(statErr), "no folder for a rejected create")
	assert.Equal(t, 0, registryRows(t, ds, "files-chat"), "no registry row for a rejected create")

	// The same rejection through the production MCP server.
	srv := newGroupMCPServer(ds)
	manifest, err := json.Marshal(filesModeGroup("files-chat-mcp", "default-chat"))
	require.NoError(t, err)
	res := callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": string(manifest)})
	assert.True(t, res.IsError)
	v := decodeGroupValidation(t, mcpText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, groupCodes(v.Errors), "GROUP_RESOLVER_PROFILE_KIND")
	assert.Equal(t, 0, registryRows(t, ds, "files-chat-mcp"))

	// Positive control: an agent-kind resolver is accepted and persisted.
	manifest, err = json.Marshal(filesModeGroup("files-agent", "default-agent"))
	require.NoError(t, err)
	res = callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": string(manifest)})
	require.False(t, res.IsError, mcpText(t, res))
	assert.Equal(t, 1, registryRows(t, ds, "files-agent"))
	data, err := os.ReadFile(filepath.Join(privateDir(workspaceDir, "files-agent"), "workflow.json"))
	require.NoError(t, err)
	var stored workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &stored))
	require.NotNil(t, stored.Nodes[0].Config.Group)
	assert.Equal(t, "default-agent", stored.Nodes[0].Config.Group.ResolverProfile)
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, stored.Nodes[0].Config.Group.GatherMode)
}

func TestGroupAuthoringE2E_BranchesGroupAuthoredEntirelyThroughMCPValidatesClean(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)

	base := `{"name":"mcp-branches","description":"authored through MCP","author":"e2e","inputs":[],
 "nodes":[
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"b1","type":"transform","config":{"operations":[{"variable":"b","operation":"set","value":2}]}},
  {"id":"done","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],"edges":[]}`
	res := callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": base})
	require.False(t, res.IsError, mcpText(t, res))

	group := `{"nodeId":"fanout","label":"Fan out","entrypoint":true,
 "config":{"form":"branches","tasks":[{"key":"second","entry":"b1"},{"key":"first","entry":"a1"}],
   "completionRule":"first_success","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"},
 "successTarget":"done","failureTarget":"failed"}`
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "mcp-branches", "group": group})
	require.False(t, res.IsError, mcpText(t, res))
	var view workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(mcpText(t, res)), &view))
	require.Len(t, view.Config.Tasks, 2)
	assert.Equal(t, "second", view.Config.Tasks[0].Key, "author order is preserved")
	assert.Equal(t, map[string][]string{"second": {"b1"}, "first": {"a1"}}, view.Bodies)

	res = callMCPTool(t, srv, ctx, "workflow_validate", map[string]any{"name": "mcp-branches"})
	require.False(t, res.IsError, mcpText(t, res))
	v := decodeGroupValidation(t, mcpText(t, res))
	assert.True(t, v.Valid, "%v", v.Errors)
	assert.Empty(t, v.Errors)

	res = callMCPTool(t, srv, ctx, "workflow_group_get", map[string]any{"name": "mcp-branches"})
	require.False(t, res.IsError, mcpText(t, res))
	var list []workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(mcpText(t, res)), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "fanout", list[0].NodeID)

	// SINK: the definition on disk carries the group node and its edges.
	data, err := os.ReadFile(filepath.Join(privateDir(workspaceDir, "mcp-branches"), "workflow.json"))
	require.NoError(t, err)
	var stored workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &stored))
	var g *workflow.GroupConfig
	for _, n := range stored.Nodes {
		if n.ID == "fanout" {
			require.Equal(t, workflow.NodeTypeParallelGroup, n.Type)
			require.True(t, n.Entrypoint)
			g = n.Config.Group
		}
	}
	require.NotNil(t, g)
	assert.Equal(t, workflow.GroupRuleFirstSuccess, g.CompletionRule)
	assert.Equal(t, []workflow.Edge{
		{Source: "fanout", Target: "b1", Output: "task", Label: "second"},
		{Source: "fanout", Target: "a1", Output: "task", Label: "first"},
		{Source: "fanout", Target: "done", Output: "success"},
		{Source: "fanout", Target: "failed", Output: "failure"},
	}, stored.Edges)
	assert.Equal(t, 1, registryRows(t, ds, "mcp-branches"))
}

func TestGroupAuthoringE2E_InvisibleChildRejected(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)

	manifest := func(name, child string) string {
		return `{"name":"` + name + `","description":"workflows group","author":"e2e","inputs":[],
 "nodes":[
  {"id":"fan","type":"parallel_group","entrypoint":true,"config":{"group":{"form":"workflows",
     "tasks":[{"key":"alpha","workflow":"` + child + `","inputs":{"topic":"alpha"}},
              {"key":"beta","workflow":"` + child + `","inputs":{"topic":"beta"}}],
     "completionRule":"all_succeed","unfinishedBehavior":"wait_for_remaining","gatherMode":"results"}}},
  {"id":"done","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],
 "edges":[{"source":"fan","target":"done","output":"success"},{"source":"fan","target":"failed","output":"failure"}]}`
	}

	// Even an ADMIN caller cannot reference another user's PRIVATE workflow.
	res := callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": manifest("uses-hidden", "hidden-child")})
	assert.True(t, res.IsError)
	assert.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	_, statErr := os.Stat(privateDir(workspaceDir, "uses-hidden"))
	assert.True(t, os.IsNotExist(statErr))
	assert.Equal(t, 0, registryRows(t, ds, "uses-hidden"))

	// Positive control: the visible child with mapped inputs is accepted.
	res = callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": manifest("uses-visible", "group-child")})
	require.False(t, res.IsError, mcpText(t, res))

	// The validate tool runs the same scoped child reader over a definition that
	// bypassed the write gate (written to disk before boot).
	res = callMCPTool(t, srv, ctx, "workflow_validate", map[string]any{"name": "legacy-orphan"})
	require.False(t, res.IsError, mcpText(t, res))
	v := decodeGroupValidation(t, mcpText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, groupCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")

	// And the upsert path rejects it too, leaving the stored definition intact.
	before, err := os.ReadFile(filepath.Join(privateDir(workspaceDir, "uses-visible"), "workflow.json"))
	require.NoError(t, err)
	upsert := `{"nodeId":"fan","entrypoint":true,"config":{"form":"workflows",
   "tasks":[{"key":"alpha","workflow":"hidden-child"}],
   "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"},
 "successTarget":"done","failureTarget":"failed"}`
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "uses-visible", "group": upsert})
	assert.True(t, res.IsError)
	assert.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	after, err := os.ReadFile(filepath.Join(privateDir(workspaceDir, "uses-visible"), "workflow.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
