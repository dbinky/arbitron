# Parallel Workflow Gathering — Phase 1 Task 8: Composition-root wiring of the group checkers

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make parallel-group authoring live on the real server: wire the production profile checker and file reader into the workflow service with `workflowSvc.SetGroupCheckers(profileChecker, fileReader)` in `cmd/arbitron/main.go:initDomain`, and prove it end to end through `initDomain` and the real MCP server built from `ds.mcpDeps()`.
**Architecture:** One line (plus comment) in the composition root, placed directly after `profileChecker` is constructed (it is built ~190 lines after `workflowSvc`). The same `profileChecker` and `fileReader` values already feed `ds.mcpDeps()` / the REST router, so write paths and both validate surfaces check groups with identical capabilities. A new `package main` e2e test boots the real stack in local auth mode and drives the service directly and through MCP `tools/call` JSON-RPC messages.
**Tech Stack:** Go 1.25.5, the real composition root (`initDomain`), SQLite (`workflow_identity` table), filesystem workflow repo, `github.com/mark3labs/mcp-go` v0.53.0, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` Outcomes ("An author can configure both forms of parallel execution, including nesting and gathering, entirely through MCP"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §3 ("Put required checker dependencies at the service boundary ... A missing checker or failed contract lookup must be reported") and the Data Provenance row "Profile kind and prompt-file existence" (`ResolverChecker.ProfileKind` → `DynamicProfileResolver.Kind`; `FileReaderAdapter.FileExists`; "new resolver-field checks/wiring belong to **P1-B**" — resolved here).

**Interfaces**
- Consumes:
  - P1-T5: `func (s *workflow.Service) SetGroupCheckers(profiles workflow.ProfileChecker, files workflow.OwnerFileChecker)`; `*workflow.GroupValidationError{Errors}`; `workflow.ErrGroupInvalid`.
  - P1-T6: `workflow.GroupView`, `workflow.GroupUpsert` (JSON shape of the `group` tool argument).
  - P1-T7: MCP tools `workflow_create`, `workflow_group_upsert`, `workflow_group_get`, `workflow_validate` (registered by `adaptmcp.RegisterAllTools`), structured `{"valid":false,"errors":[...]}` results with `IsError=true`.
  - Existing composition root: `cmd/arbitron/main.go:340` `initDomain`, `:466` `fileReader := filesystem.NewFileReader(cfg.Workspace.Path)` (`*filesystem.FileReaderAdapter`, has `FileExists(owner uuid.UUID, workflowName, relativePath string) bool`), `:510` `workflowSvc := workflow.NewWorkflowService(...)`, `:701` `profileChecker := providersadapter.NewResolverChecker(profileResolver)` (`ResolverChecker`, satisfies `workflow.ProfileChecker`), `:287` `(*domainStack).mcpDeps()`.
  - Existing test helpers (package `main`): `cmd/arbitron/spawn_e2e_test.go:19` `localDefaultUser`, `:22` `writeWorkflow`, `:87` `adminCtxForLocalUser`.
  - Built-in profiles seeded on first boot from the loader defaults (`adapter/config/loader.go:124-126`): `default-chat` (kind `chat`) and `default-agent` (kind `agent`).
- Produces:
  - Live wiring: `workflowSvc.SetGroupCheckers(profileChecker, fileReader)` in `initDomain`.
  - Test helpers in `cmd/arbitron/group_authoring_e2e_test.go` (package `main`), reused by P1-T9: `func bootGroupAuthoringStack(t *testing.T) (*domainStack, string)` (returns the stack and the workspace dir), `func newGroupMCPServer(ds *domainStack) *adaptmcp.Server`, `func callMCPTool(t *testing.T, srv *adaptmcp.Server, ctx context.Context, tool string, args map[string]any) *gomcp.CallToolResult`, `func mcpText(t *testing.T, res *gomcp.CallToolResult) string`, `func registryRows(t *testing.T, ds *domainStack, name string) int`, `func filesModeGroup(name, resolverProfile string) *workflow.Workflow` (valid entrypoint branches group, `gatherMode results_and_files`, tasks a→a1, b→b1, success→done, failure→failed), `var groupOtherUser uuid.UUID`.

**Wiring & data provenance**
- This task IS the composition-root wiring for P1-B. After it, every producer is live:
  - `profiles` ⇐ `profileChecker` = `providersadapter.NewResolverChecker(profileResolver)` (main.go:701), `ResolverChecker.ProfileKind` → `DynamicProfileResolver.Kind` over the SQLite-backed `ProfileCache` (main.go:692-700). The same value is `ds.profileChecker` (main.go:1220) → `Deps.ProfileChecker` (main.go:304) and `RouterDeps.ProfileChecker` (main.go:1287), so `workflow_validate` and REST validate use the identical checker.
  - `files` ⇐ `fileReader` = `filesystem.NewFileReader(cfg.Workspace.Path)` (main.go:466), also `ds.fileReader` → `Deps.FileReader` (main.go:296), and the engine's executors' reader (main.go:788-790).
  - Child contracts ⇐ `workflowSvc`'s own repository (`workflowRepo`, main.go:465) through `Service.ChildContracts` (P1-T5) — no extra wiring.
- Placement constraint: `workflowSvc` exists from main.go:510 but `profileChecker` only from main.go:701, so the call goes immediately after line 701 (it cannot sit with the other `workflowSvc.Set*` calls at main.go:514-548).
- Provenance probe (fails when the wiring line is deleted): `TestGroupAuthoringE2E_FilesModeChatResolverRejectedThroughCompositionRoot` asserts the rejection code is `GROUP_RESOLVER_PROFILE_KIND` and NOT `GROUP_CHECKER_UNAVAILABLE` — without the line the service has no checkers and returns `GROUP_CHECKER_UNAVAILABLE`; its positive control (an agent-kind resolver is accepted and persisted) and `TestGroupAuthoringE2E_BranchesGroupAuthoredEntirelyThroughMCPValidatesClean` (the upsert would be rejected) also fail without it.

### Task 8: Wire SetGroupCheckers at the composition root + e2e proof

**Files:**
- Modify: `cmd/arbitron/main.go:701` (insert the wiring after `profileChecker := providersadapter.NewResolverChecker(profileResolver)`)
- Test: `cmd/arbitron/group_authoring_e2e_test.go` (package `main`)

- [x] Step 1: Write the failing composition-root tests. Create `cmd/arbitron/group_authoring_e2e_test.go`:

```go
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
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + workspaceDir + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
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
```

- [x] Step 2: Run it, confirm it fails (P1-T5..T7 are in place; only the wiring is missing).

```bash
go test ./cmd/arbitron/ -run 'TestGroupAuthoringE2E' -count=1
```
Expected:
```
--- FAIL: TestGroupAuthoringE2E_FilesModeChatResolverRejectedThroughCompositionRoot (0.XXs)
    group_authoring_e2e_test.go:...: 
        	Error:      "[GROUP_CHECKER_UNAVAILABLE ...]" does not contain "GROUP_RESOLVER_PROFILE_KIND"
--- FAIL: TestGroupAuthoringE2E_BranchesGroupAuthoredEntirelyThroughMCPValidatesClean (0.XXs)
    group_authoring_e2e_test.go:...: 
        	Error:      Should be false
        	Messages:   {"valid": false, "errors": [{"code": "GROUP_CHECKER_UNAVAILABLE", ...
--- FAIL: TestGroupAuthoringE2E_InvisibleChildRejected (0.XXs)
FAIL	github.com/arbitron/arbitron/cmd/arbitron
```

- [x] Step 3: Implement the wiring. In `cmd/arbitron/main.go`, directly after line 701 (`profileChecker := providersadapter.NewResolverChecker(profileResolver)`), insert:

```go
	// Parallel-group authoring (P1-B): the workflow service REQUIRES these
	// checkers for any definition containing a parallel_group node — the
	// resolverProfile kind (the same alias->kind lookup workflow_validate uses)
	// and the owner-aware resolverPromptFile existence check (the same reader the
	// engine executes from). Without this call every group write is rejected
	// with GROUP_CHECKER_UNAVAILABLE. It sits here, not with the other
	// workflowSvc setters above, because profileChecker only exists from here.
	workflowSvc.SetGroupCheckers(profileChecker, fileReader)
```

- [x] Step 4: Run the e2e tests and the whole composition-root package.

```bash
go test ./cmd/arbitron/ -run 'TestGroupAuthoringE2E' -count=1 -v
go test ./cmd/arbitron/... -count=1
go build ./...
```
Expected:
```
--- PASS: TestGroupAuthoringE2E_FilesModeChatResolverRejectedThroughCompositionRoot (0.XXs)
--- PASS: TestGroupAuthoringE2E_BranchesGroupAuthoredEntirelyThroughMCPValidatesClean (0.XXs)
--- PASS: TestGroupAuthoringE2E_InvisibleChildRejected (0.XXs)
PASS
ok  	github.com/arbitron/arbitron/cmd/arbitron	X.XXXs
```

- [x] Step 5: Prove the probe bites: temporarily comment out the inserted `workflowSvc.SetGroupCheckers(profileChecker, fileReader)` line, re-run `go test ./cmd/arbitron/ -run 'TestGroupAuthoringE2E_FilesModeChatResolverRejectedThroughCompositionRoot' -count=1`, confirm the `does not contain "GROUP_RESOLVER_PROFILE_KIND"` failure from Step 2, then restore the line and re-run to green.

- [x] Step 6: Commit.

```bash
git add cmd/arbitron/main.go cmd/arbitron/group_authoring_e2e_test.go
git commit -m "feat(cmd): wire parallel-group checkers into the workflow service

initDomain now calls workflowSvc.SetGroupCheckers(profileChecker, fileReader)
so group writes are validated with the production profile resolver and file
reader (the same values MCP/REST validate use). E2E through initDomain and the
real MCP server: chat-kind resolver rejected (GROUP_RESOLVER_PROFILE_KIND, no
folder, no registry row), branches group authored via workflow_create +
workflow_group_upsert validates clean, invisible child rejected.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
