# Parallel Workflow Gathering — Phase 1 Task 7: MCP group tools, validate parity and authoring guidance

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Let authors create, inspect, revise and delete parallel groups entirely through MCP (`workflow_group_upsert`, `workflow_group_get`, `workflow_group_delete`), give every write tool and both validate surfaces (MCP `workflow_validate`, REST `POST /workflows/{name}/validate`) the same caller-scoped group checkers, return group failures in `workflow_validate`'s structured `{"valid":false,"errors":[...]}` shape, and teach the feature in `WorkflowManifestHint` with a complete, machine-checked example.
**Architecture:** A new adapter file `adapter/mcp/tools_workflow_group.go` holds the three tool handlers (thin: decode → `workflowSvc(srv)` P1-T6 op → marshal) plus the shared `groupValidationResult` / `hasHardValidationError` helpers; `RegisterWorkflowTools` calls its registrar. `workflow_create` / `workflow_update` surface `*workflow.GroupValidationError` structurally; `workflow_validate` and the REST validate endpoint replace their ad-hoc VisibleNames option with `svc.GroupValidateOptions(ctx)` (P1-T5, a superset) and compute `valid` from hard errors only (advisories never invalidate). REST create/update map group rejections to `422 GROUP_VALIDATION_FAILED` with the same `errors` array.
**Tech Stack:** Go 1.25.5, `github.com/mark3labs/mcp-go` v0.53.0, chi, testify; `adapter/filesystem.WorkflowRepo` in tests for real persistence.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` §"MCP authoring" (create/inspect/revise branches, multi-child groups, nesting, child selection + inputs, task order, rules/behaviour, file gathering; actionable feedback); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §3 (new tools in `RegisterWorkflowTools` delegating to the service; group validation failures use `workflow_validate`'s structured errors shape; extend `WorkflowManifestHint`; "`workflow_validate` must receive the same scoped group checkers as write paths").

**Interfaces**
- Consumes:
  - P1-T6 (`domain/workflow/group_ops.go`): `(*Service).UpsertGroup(ctx, workflowName string, up GroupUpsert) (*GroupView, error)`, `GetGroup(ctx, workflowName, nodeID string) (*GroupView, error)`, `ListGroups(ctx, workflowName string) ([]GroupView, error)`, `DeleteGroup(ctx, workflowName, nodeID string) (*GroupDeleteResult, error)`; `GroupUpsert`, `GroupView`, `GroupDeleteResult`; `ErrGroupNotFound`, `ErrGroupUpsertInvalid`.
  - P1-T5 (`domain/workflow/service_group.go`): `(*Service).GroupValidateOptions(ctx) []ValidateOption`; `(*Service).SetGroupCheckers(ProfileChecker, OwnerFileChecker)`; `*GroupValidationError{Errors}`; `ErrGroupInvalid`.
  - P1-T4: `WithChildContractReader`, `WithGroupCheckersRequired`, `ChildContractReader` (test only); P1-T2: `AnalyzeGroups`, `HasGroups` (test only).
  - Existing: `adapter/mcp/tools_workflow.go:26` `workflowSvc(srv)`, `:35` `ownerBoundChecker`, `:228` `mapLifecycleError`; `adapter/mcp/helpers.go:17,24` `safeWorkflowName`, `MarshalResult`; `adapter/mcp/server.go:45` `(*Server).AddTool`; `adapter/http/errors.go:27,42` `WriteError`, `WriteJSON`; `adapter/http/workflow_handler.go:38` `ownerBoundChecker`, `:50` `writeSharedReadOnlyOrNotFound`.
- Produces:
  - MCP tools (registered by `RegisterWorkflowTools`): `workflow_group_upsert` (required string args `name`, `group` = JSON `workflow.GroupUpsert`; unknown JSON fields rejected), `workflow_group_get` (required `name`, optional `nodeId`; no `nodeId` ⇒ JSON array of `GroupView` in workflow order), `workflow_group_delete` (required `name`, `nodeId`; returns `GroupDeleteResult`).
  - Group validation failures from upsert/delete/create/update: tool result JSON `{"valid":false,"errors":[...ValidationError]}` with `IsError=true`.
  - `workflow_validate` / REST validate: `errors` includes group findings; `valid` is false only when a non-`"warning"` finding exists.
  - REST `POST /workflows` and `PUT /workflows/{name}`: `422` body `{"error":<msg>,"code":"GROUP_VALIDATION_FAILED","valid":false,"errors":[...]}`.
  - `adapter/mcp/authoring_guidance.go`: `const ParallelGroupExample string` (complete manifest JSON), `const ParallelGroupGuidance string`, and `WorkflowManifestHint` = previous hint + `ParallelGroupGuidance`.
  - Unexported helpers in `adapter/mcp/tools_workflow_group.go`: `registerWorkflowGroupTools(srv *Server)`, `groupValidationResult(err error) (*gomcp.CallToolResult, bool)`, `hasHardValidationError(errs []workflow.ValidationError) bool`, `groupToolError(name, op string, err error) *gomcp.CallToolResult`, `decodeGroupUpsert(raw string) (workflow.GroupUpsert, error)`.

**Wiring & data provenance**
- Composition root: the tools are registered for production by the existing `adaptmcp.RegisterAllTools(mcpSrv)` in `cmd/arbitron/main.go:runServe` (main.go:1259) and `runMCP` (main.go:1401), both over `ds.mcpDeps()` (main.go:287) whose `WorkflowSvc` is `ds.workflowSvc` (main.go:291) and whose `ProfileChecker`/`FileReader` are `ds.profileChecker`/`ds.fileReader` (main.go:304,296). The REST handler receives the same service via `wfHandler.SetWorkflowService(d.WorkflowSvc)` (`adapter/http/server.go:199`). P1-T8 wires `workflowSvc.SetGroupCheckers(profileChecker, fileReader)` so writes stop returning `GROUP_CHECKER_UNAVAILABLE`, and adds the composition-root test.
- Producers: `name`/`group`/`nodeId` ⇐ the MCP client's `tools/call` arguments (`request.GetArguments()`); caller identity ⇐ the `/mcp` auth middleware (`identity.FromContext`); every domain answer ⇐ `workflowSvc(srv)` = `Deps.WorkflowSvc`; validate checkers ⇐ `Deps.ProfileChecker`, `Deps.FileReader` (owner-bound via `ownerBoundChecker`), `Deps.ConnChecker` and `workflowSvc(srv).GroupValidateOptions(ctx)`.
- Provenance probes: `TestWorkflowGroupTools_Upsert_DefaultServiceWithoutCheckersIsRejected` (a server whose `Deps.WorkflowSvc` is nil gets the lazily built, checker-less service ⇒ `GROUP_CHECKER_UNAVAILABLE`, proving the tools route through the injected service and that P1-T8's wiring is required); `TestWorkflowValidate_Groups_InvisibleChildIsReported` and `TestWorkflowValidate_Groups_MissingProfileCheckerIsReported` fail if `GroupValidateOptions` is not appended; `TestWorkflowHandler_Validate_GroupOptionsAppended` is the REST twin.

### Task 7: MCP group tools, structured group errors, validate parity, guidance

**Files:**
- Create: `adapter/mcp/tools_workflow_group.go`
- Modify: `adapter/mcp/tools_workflow.go:221` (end of `RegisterWorkflowTools`: call `registerWorkflowGroupTools(srv)`)
- Modify: `adapter/mcp/tools_workflow.go:302-307` (`workflowCreateHandler` error mapping)
- Modify: `adapter/mcp/tools_workflow.go:337-345` (`workflowUpdateHandler` error mapping)
- Modify: `adapter/mcp/tools_workflow.go:395-410` (`workflowValidateHandler` options + `valid`)
- Modify: `adapter/mcp/authoring_guidance.go:31-33` (`WorkflowManifestHint`; new `ParallelGroupGuidance`, `ParallelGroupExample`)
- Modify: `adapter/mcp/register_test.go:40-47` (`expectedTools`: three new names)
- Modify: `adapter/http/workflow_handler.go:204-223` (`Validate`), `:508-515` (`Create`), `:537-543` (`Update`)
- Test: `adapter/mcp/tools_workflow_group_test.go` (package `mcp_test`; reuses `callTool`, `callToolAdmin`, `extractText`, `stubFileReader`, `mcpStubProfileChecker`, `mcpTestUserID` from `adapter/mcp/tools_workflow_test.go:157,168,209,121,698,24`)
- Test: `adapter/mcp/authoring_guidance_test.go` (append)
- Test: `adapter/http/workflow_handler_group_test.go` (package `http_test`; reuses `newMockFileReader`, `mockWorkflowService`, `stubProfileChecker`, `withChiURLParam`, `withTestIdentity`, `testUserID` from `adapter/http/workflow_handler_test.go`)

- [x] Step 1: Write the failing MCP tool tests. Create `adapter/mcp/tools_workflow_group_test.go`:

```go
package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/workflow"
)

var gtProfiles = mcpStubProfileChecker{"chat-p": "chat", "agent-p": "agent"}

type gtFixture struct {
	srv  *adaptmcp.Server
	repo *filesystem.WorkflowRepo
	root string
}

func gtFileReader() *stubFileReader {
	return &stubFileReader{fileExistsFn: func(_, path string) bool { return path == "prompts/resolve.md" }}
}

// newGroupToolServer is production-shaped: a real filesystem repository, a
// real workflow.Service, and — when wire is true — SetGroupCheckers called with
// the same profile checker and file reader the Deps carry (what P1-T8 does in
// cmd/arbitron/main.go).
func newGroupToolServer(t *testing.T, wire bool) *gtFixture {
	t.Helper()
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	svc := workflow.NewWorkflowService(repo, nil)
	fr := gtFileReader()
	if wire {
		svc.SetGroupCheckers(gtProfiles, fr)
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{
		Workflows: repo, WorkflowSvc: svc, FileReader: fr, ProfileChecker: gtProfiles,
	})
	adaptmcp.RegisterWorkflowTools(srv)
	return &gtFixture{srv: srv, repo: repo, root: root}
}

func gtBaseManifest(name string) string {
	return fmt.Sprintf(`{"name":%q,"description":"base","author":"t","inputs":[],"nodes":[
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"b1","type":"transform","config":{"operations":[{"variable":"b","operation":"set","value":1}]}},
  {"id":"done","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
],"edges":[]}`, name)
}

func gtChildManifest(name string) string {
	return fmt.Sprintf(`{"name":%q,"description":"child","author":"t",
  "inputs":[{"name":"topic","type":"string","description":"what","required":true}],
  "nodes":[{"id":"c1","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"seen","operation":"set","value":"{{topic}}"}]}}],
  "edges":[]}`, name)
}

const gtBranchesGroup = `{"nodeId":"fanout","label":"Fan out","entrypoint":true,
 "config":{"form":"branches","tasks":[{"key":"a","entry":"a1"},{"key":"b","entry":"b1"}],
           "completionRule":"all_succeed","unfinishedBehavior":"wait_for_remaining","gatherMode":"results"},
 "successTarget":"done","failureTarget":"failed"}`

func gtWorkflowsGroup(child string) string {
	return fmt.Sprintf(`{"nodeId":"reviews","entrypoint":true,
 "config":{"form":"workflows","tasks":[
     {"key":"alpha","workflow":%q,"inputs":{"topic":"alpha"}},
     {"key":"beta","workflow":%q,"inputs":{"topic":"beta"}}],
   "completionRule":"first_success","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"},
 "successTarget":"done","failureTarget":"failed"}`, child, child)
}

func (f *gtFixture) create(t *testing.T, manifest string) {
	t.Helper()
	res := callTool(t, f.srv, "workflow_create", map[string]any{"manifest": manifest})
	require.False(t, res.IsError, extractText(t, res))
}

func (f *gtFixture) stored(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	wf, err := f.repo.Load(context.Background(), workflow.Private(mcpTestUserID), name)
	require.NoError(t, err)
	return wf
}

// raw returns the persisted workflow.json bytes, nil when the folder does not exist.
func (f *gtFixture) raw(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, "users", mcpTestUserID.String(), name, "workflow.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return b
}

type gtValidation struct {
	Valid  bool                       `json:"valid"`
	Errors []workflow.ValidationError `json:"errors"`
}

func gtDecodeValidation(t *testing.T, text string) gtValidation {
	t.Helper()
	var v gtValidation
	require.NoError(t, json.Unmarshal([]byte(text), &v), text)
	return v
}

func gtCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

func gtNode(t *testing.T, wf *workflow.Workflow, id string) workflow.Node {
	t.Helper()
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not found", id)
	return workflow.Node{}
}

func gtHasNode(wf *workflow.Workflow, id string) bool {
	for _, n := range wf.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// ---------- registration ----------

func TestWorkflowGroupTools_Registered(t *testing.T) {
	f := newGroupToolServer(t, true)
	required := map[string][]string{}
	for _, tool := range f.srv.ListTools() {
		required[tool.Name] = tool.InputSchema.Required
	}
	assert.ElementsMatch(t, []string{"name", "group"}, required["workflow_group_upsert"])
	assert.ElementsMatch(t, []string{"name"}, required["workflow_group_get"])
	assert.ElementsMatch(t, []string{"name", "nodeId"}, required["workflow_group_delete"])
}

// ---------- upsert ----------

func TestWorkflowGroupTools_Upsert_BranchesHappyPath(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))

	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup})
	require.False(t, res.IsError, extractText(t, res))

	var view workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &view))
	assert.Equal(t, "fanout", view.NodeID)
	assert.True(t, view.Entrypoint)
	require.Len(t, view.Config.Tasks, 2)
	assert.Equal(t, "a", view.Config.Tasks[0].Key)
	assert.Equal(t, "b", view.Config.Tasks[1].Key)
	assert.Equal(t, map[string][]string{"a": {"a1"}, "b": {"b1"}}, view.Bodies)
	assert.Equal(t, "done", view.SuccessTarget)

	// Sink: the persisted manifest on disk.
	stored := f.stored(t, "wf")
	node := gtNode(t, stored, "fanout")
	assert.Equal(t, workflow.NodeTypeParallelGroup, node.Type)
	require.NotNil(t, node.Config.Group)
	assert.Equal(t, workflow.GroupRuleAllSucceed, node.Config.Group.CompletionRule)
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "fanout", Target: "a1", Output: "task", Label: "a"})
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "fanout", Target: "b1", Output: "task", Label: "b"})
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "fanout", Target: "done", Output: "success"})
	assert.Contains(t, stored.Edges, workflow.Edge{Source: "fanout", Target: "failed", Output: "failure"})
}

func TestWorkflowGroupTools_Upsert_WorkflowsFormRepeatedChild(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtChildManifest("reviewer"))
	f.create(t, gtBaseManifest("wf"))

	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtWorkflowsGroup("reviewer")})
	require.False(t, res.IsError, extractText(t, res))

	g := gtNode(t, f.stored(t, "wf"), "reviews").Config.Group
	require.NotNil(t, g)
	require.Len(t, g.Tasks, 2)
	assert.Equal(t, "reviewer", g.Tasks[0].Workflow)
	assert.Equal(t, "reviewer", g.Tasks[1].Workflow)
	assert.Equal(t, map[string]any{"topic": "alpha"}, g.Tasks[0].Inputs)
	assert.Equal(t, map[string]any{"topic": "beta"}, g.Tasks[1].Inputs)
}

func TestWorkflowGroupTools_Upsert_GroupValidationFailureIsStructured(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))
	before := f.raw(t, "wf")

	bad := `{"nodeId":"fanout","entrypoint":true,"config":{"form":"branches","tasks":[{"key":"a","entry":"a1"}],
	  "completionRule":"quorum","unfinishedBehavior":"wait_for_remaining","gatherMode":"results"},
	  "successTarget":"done","failureTarget":"failed"}`
	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": bad})
	assert.True(t, res.IsError)
	v := gtDecodeValidation(t, extractText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_INVALID_RULE")
	for _, e := range v.Errors {
		if e.Code == "GROUP_INVALID_RULE" {
			assert.Equal(t, "fanout", e.NodeID, "findings identify the node")
		}
	}
	assert.Equal(t, before, f.raw(t, "wf"), "nothing persisted")
}

func TestWorkflowGroupTools_Upsert_ChatResolverRejectedStructured(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))
	group := `{"nodeId":"fanout","entrypoint":true,"config":{"form":"branches","tasks":[{"key":"a","entry":"a1"},{"key":"b","entry":"b1"}],
	  "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results_and_files","resolverProfile":"chat-p"},
	  "successTarget":"done","failureTarget":"failed"}`
	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": group})
	assert.True(t, res.IsError)
	assert.Contains(t, gtCodes(gtDecodeValidation(t, extractText(t, res)).Errors), "GROUP_RESOLVER_PROFILE_KIND")
}

func TestWorkflowGroupTools_Upsert_ArgumentAndInputErrors(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))

	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"missing name":    {map[string]any{"group": gtBranchesGroup}, "name is required"},
		"invalid name":    {map[string]any{"name": "../x", "group": gtBranchesGroup}, "invalid workflow name"},
		"missing group":   {map[string]any{"name": "wf"}, "group is required"},
		"malformed json":  {map[string]any{"name": "wf", "group": `{"nodeId":`}, "invalid group JSON"},
		"unknown field":   {map[string]any{"name": "wf", "group": `{"nodeId":"g","completion_rule":"all_succeed"}`}, "unknown field"},
		"trailing data":   {map[string]any{"name": "wf", "group": gtBranchesGroup + `{}`}, "invalid group JSON"},
		"unknown target":  {map[string]any{"name": "wf", "group": `{"nodeId":"g","config":{"form":"branches"},"successTarget":"nowhere","failureTarget":"failed"}`}, "invalid group"},
		"unknown wf name": {map[string]any{"name": "ghost", "group": gtBranchesGroup}, "workflow not found: ghost"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := callTool(t, f.srv, "workflow_group_upsert", tc.args)
			assert.True(t, res.IsError)
			assert.Contains(t, extractText(t, res), tc.want)
		})
	}
	assert.False(t, gtHasNode(f.stored(t, "wf"), "g"))
}

func TestWorkflowGroupTools_Upsert_DefaultServiceWithoutCheckersIsRejected(t *testing.T) {
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	// Deps.WorkflowSvc nil: workflowSvc(srv) builds a fresh Service with NO group
	// checkers — exactly what an unwired composition root would produce.
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{
		Workflows: repo, FileReader: gtFileReader(), ProfileChecker: gtProfiles,
	})
	adaptmcp.RegisterWorkflowTools(srv)
	res := callTool(t, srv, "workflow_create", map[string]any{"manifest": gtBaseManifest("wf")})
	require.False(t, res.IsError, extractText(t, res))

	res = callTool(t, srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup})
	assert.True(t, res.IsError)
	assert.Contains(t, gtCodes(gtDecodeValidation(t, extractText(t, res)).Errors), "GROUP_CHECKER_UNAVAILABLE")
}

func TestWorkflowGroupTools_Upsert_SharedReadOnlyForNonAdmin(t *testing.T) {
	f := newGroupToolServer(t, true)
	var base workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtBaseManifest("shared-wf")), &base))
	require.NoError(t, f.repo.Create(context.Background(), workflow.Shared(), "shared-wf", &base))

	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "shared-wf", "group": gtBranchesGroup})
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "shared and read-only")

	res = callToolAdmin(t, f.srv, "workflow_group_upsert", map[string]any{"name": "shared-wf", "group": gtBranchesGroup})
	require.False(t, res.IsError, extractText(t, res))
	shared, err := f.repo.Load(context.Background(), workflow.Shared(), "shared-wf")
	require.NoError(t, err)
	assert.True(t, gtHasNode(shared, "fanout"), "admin edits the shared copy in place")
}

// ---------- get ----------

func TestWorkflowGroupTools_Get_SingleListAndNotFound(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtChildManifest("reviewer"))
	f.create(t, gtBaseManifest("wf"))
	require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup}).IsError)
	before := f.raw(t, "wf")

	res := callTool(t, f.srv, "workflow_group_get", map[string]any{"name": "wf", "nodeId": "fanout"})
	require.False(t, res.IsError, extractText(t, res))
	var view workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &view))
	assert.Equal(t, "fanout", view.NodeID)
	assert.Equal(t, []string{"a1"}, view.Bodies["a"])

	res = callTool(t, f.srv, "workflow_group_get", map[string]any{"name": "wf"})
	require.False(t, res.IsError, extractText(t, res))
	var list []workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "fanout", list[0].NodeID)

	res = callTool(t, f.srv, "workflow_group_get", map[string]any{"name": "reviewer"})
	require.False(t, res.IsError)
	assert.Equal(t, "[]", extractText(t, res), "a group-free workflow lists as [] not null")

	res = callTool(t, f.srv, "workflow_group_get", map[string]any{"name": "wf", "nodeId": "a1"})
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "parallel group not found")

	res = callTool(t, f.srv, "workflow_group_get", map[string]any{"name": "ghost"})
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "workflow not found: ghost")

	assert.Equal(t, before, f.raw(t, "wf"), "get never persists")
}

// ---------- delete ----------

const gtTwoGroupsManifest = `{"name":"two","description":"two sequential groups","author":"t","inputs":[],
 "nodes":[
  {"id":"first","type":"parallel_group","entrypoint":true,"config":{"group":{"form":"branches",
     "tasks":[{"key":"a","entry":"a1"},{"key":"b","entry":"b1"}],
     "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
  {"id":"second","type":"parallel_group","config":{"group":{"form":"branches",
     "tasks":[{"key":"x","entry":"x1"},{"key":"y","entry":"y1"}],
     "completionRule":"first_success","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"b1","type":"transform","config":{"operations":[{"variable":"b","operation":"set","value":1}]}},
  {"id":"x1","type":"transform","config":{"operations":[{"variable":"x","operation":"set","value":1}]}},
  {"id":"y1","type":"transform","config":{"operations":[{"variable":"y","operation":"set","value":1}]}},
  {"id":"done","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],
 "edges":[
  {"source":"first","target":"a1","output":"task","label":"a"},
  {"source":"first","target":"b1","output":"task","label":"b"},
  {"source":"first","target":"second","output":"success"},
  {"source":"first","target":"failed","output":"failure"},
  {"source":"second","target":"x1","output":"task","label":"x"},
  {"source":"second","target":"y1","output":"task","label":"y"},
  {"source":"second","target":"done","output":"success"},
  {"source":"second","target":"failed","output":"failure"}
 ]}`

func TestWorkflowGroupTools_Delete(t *testing.T) {
	t.Run("removes the group and reports retained nodes", func(t *testing.T) {
		f := newGroupToolServer(t, true)
		f.create(t, gtBaseManifest("wf"))
		require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup}).IsError)

		res := callTool(t, f.srv, "workflow_group_delete", map[string]any{"name": "wf", "nodeId": "fanout"})
		require.False(t, res.IsError, extractText(t, res))
		var out workflow.GroupDeleteResult
		require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &out))
		assert.Equal(t, "fanout", out.NodeID)
		assert.Equal(t, []string{"a1", "b1", "done", "failed"}, out.RetainedNodes)

		stored := f.stored(t, "wf")
		assert.False(t, gtHasNode(stored, "fanout"))
		assert.Empty(t, stored.Edges)
	})
	t.Run("deletion leaving another group invalid is rejected structurally", func(t *testing.T) {
		f := newGroupToolServer(t, true)
		f.create(t, gtTwoGroupsManifest)
		before := f.raw(t, "two")

		res := callTool(t, f.srv, "workflow_group_delete", map[string]any{"name": "two", "nodeId": "second"})
		assert.True(t, res.IsError)
		v := gtDecodeValidation(t, extractText(t, res))
		assert.False(t, v.Valid)
		assert.Contains(t, gtCodes(v.Errors), "GROUP_MISSING_PORT")
		assert.Equal(t, before, f.raw(t, "two"))
	})
	t.Run("errors", func(t *testing.T) {
		f := newGroupToolServer(t, true)
		f.create(t, gtBaseManifest("wf"))
		res := callTool(t, f.srv, "workflow_group_delete", map[string]any{"name": "wf"})
		assert.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "nodeId is required")
		res = callTool(t, f.srv, "workflow_group_delete", map[string]any{"name": "wf", "nodeId": "a1"})
		assert.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "parallel group not found")
	})
}

// ---------- create / update surface group errors structurally ----------

func TestWorkflowCreateUpdate_GroupErrorsAreStructured(t *testing.T) {
	f := newGroupToolServer(t, true)
	bad := `{"name":"bad","description":"d","author":"t","inputs":[],
	 "nodes":[{"id":"g","type":"parallel_group","entrypoint":true,"config":{"group":{"form":"lanes","tasks":[],
	    "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
	   {"id":"done","type":"transform","config":{}},{"id":"failed","type":"transform","config":{}}],
	 "edges":[{"source":"g","target":"done","output":"success"},{"source":"g","target":"failed","output":"failure"}]}`

	res := callTool(t, f.srv, "workflow_create", map[string]any{"manifest": bad})
	assert.True(t, res.IsError)
	v := gtDecodeValidation(t, extractText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_INVALID_FORM")
	assert.Nil(t, f.raw(t, "bad"), "rejected create writes no folder")

	f.create(t, gtTwoGroupsManifest)
	before := f.raw(t, "two")
	broken := `{"name":"two","nodes":[{"id":"first","type":"parallel_group","entrypoint":true,"config":{"group":{"form":"branches",
	   "tasks":[{"key":"a","entry":"a1"}],"completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
	   {"id":"a1","type":"transform","config":{}}],
	 "edges":[{"source":"first","target":"a1","output":"task","label":"a"}]}`
	res = callTool(t, f.srv, "workflow_update", map[string]any{"name": "two", "manifest": broken})
	assert.True(t, res.IsError)
	assert.Contains(t, gtCodes(gtDecodeValidation(t, extractText(t, res)).Errors), "GROUP_MISSING_PORT")
	assert.Equal(t, before, f.raw(t, "two"))
}

// ---------- workflow_validate gets the scoped group checkers ----------

func TestWorkflowValidate_Groups_InvisibleChildIsReported(t *testing.T) {
	f := newGroupToolServer(t, true)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtBaseManifest("orphaned")), &wf))
	// Persist directly (bypassing the write gate), as a definition whose child was
	// later deleted would be.
	wf.Nodes = append(wf.Nodes, workflow.Node{ID: "reviews", Type: workflow.NodeTypeParallelGroup, Entrypoint: true,
		Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
			Form: workflow.GroupFormWorkflows, CompletionRule: workflow.GroupRuleAllSucceed,
			UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResults,
			Tasks: []workflow.GroupTask{{Key: "a", Workflow: "ghost-child", Inputs: map[string]any{"topic": "x"}}},
		}}})
	wf.Edges = []workflow.Edge{
		{Source: "reviews", Target: "done", Output: "success"},
		{Source: "reviews", Target: "failed", Output: "failure"},
	}
	require.NoError(t, f.repo.Create(context.Background(), workflow.Private(mcpTestUserID), "orphaned", &wf))

	res := callTool(t, f.srv, "workflow_validate", map[string]any{"name": "orphaned"})
	require.False(t, res.IsError)
	v := gtDecodeValidation(t, extractText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
}

func TestWorkflowValidate_Groups_MissingProfileCheckerIsReported(t *testing.T) {
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	svc := workflow.NewWorkflowService(repo, nil)
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{
		Workflows: repo, WorkflowSvc: svc, FileReader: gtFileReader(), // ProfileChecker deliberately nil
	})
	adaptmcp.RegisterWorkflowTools(srv)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtBaseManifest("files")), &wf))
	wf.Nodes = append(wf.Nodes, workflow.Node{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true,
		Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
			Form: workflow.GroupFormBranches, CompletionRule: workflow.GroupRuleAllSucceed,
			UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResultsAndFiles,
			ResolverProfile: "agent-p",
			Tasks:           []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
		}}})
	wf.Edges = []workflow.Edge{
		{Source: "fanout", Target: "a1", Output: "task", Label: "a"},
		{Source: "fanout", Target: "b1", Output: "task", Label: "b"},
		{Source: "fanout", Target: "done", Output: "success"},
		{Source: "fanout", Target: "failed", Output: "failure"},
	}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(mcpTestUserID), "files", &wf))

	res := callTool(t, srv, "workflow_validate", map[string]any{"name": "files"})
	require.False(t, res.IsError)
	v := gtDecodeValidation(t, extractText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_CHECKER_UNAVAILABLE")
}

func TestWorkflowValidate_Groups_ValidAndAdvisoryOnly(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))
	require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup}).IsError)

	v := gtDecodeValidation(t, extractText(t, callTool(t, f.srv, "workflow_validate", map[string]any{"name": "wf"})))
	assert.True(t, v.Valid, "%v", v.Errors)
	assert.Empty(t, v.Errors)

	f.create(t, gtChildManifest("reviewer"))
	f.create(t, gtBaseManifest("inherits"))
	inherit := `{"nodeId":"reviews","entrypoint":true,"config":{"form":"workflows","tasks":[{"key":"a","workflow":"reviewer"}],
	  "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"},
	  "successTarget":"done","failureTarget":"failed"}`
	require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "inherits", "group": inherit}).IsError)

	v = gtDecodeValidation(t, extractText(t, callTool(t, f.srv, "workflow_validate", map[string]any{"name": "inherits"})))
	assert.True(t, v.Valid, "an advisory alone never makes a workflow invalid: %v", v.Errors)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_REQUIRED_INPUT_RUNTIME")
}
```

- [x] Step 2: Append the failing guidance tests to `adapter/mcp/authoring_guidance_test.go` (add `"fmt"` and `"github.com/arbitron/arbitron/domain/workflow"` to its import block, lines 3-11):

```go
func TestWorkflowManifestHint_DocumentsParallelGroups(t *testing.T) {
	for _, want := range []string{
		"parallel_group", "config.group", `"branches"`, `"workflows"`, `"entry"`, `"workflow"`, `"inputs"`,
		`"task"`, `"success"`, `"failure"`,
		"all_succeed", "first_success", "wait_for_remaining", "cancel_unfinished", "finish_independently",
		`"results"`, "results_and_files", "resolverProfile", "AGENT-kind", "resolverPromptFile", "workflow_write_file",
		"repoPath", "Nesting", "order", "workspace", "cycle", "entrypoint",
		"workflow_group_upsert", "workflow_group_get", "workflow_group_delete", "workflow_validate",
		adaptmcp.ParallelGroupExample,
	} {
		assert.Contains(t, adaptmcp.WorkflowManifestHint, want)
	}
	assert.Contains(t, adaptmcp.WorkflowManifestHint, "gitVariable", "the original conventions hint is kept")
}

// guidanceChildren is the child contract the example's workflows group targets.
type guidanceChildren map[string][]workflow.InputField

func (g guidanceChildren) ChildInputs(name string) ([]workflow.InputField, error) {
	in, ok := g[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return in, nil
}

type guidanceFiles struct{}

func (guidanceFiles) FileExists(string, string) bool { return true }

func TestParallelGroupExample_IsAValidNestedTwoFormManifest(t *testing.T) {
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(adaptmcp.ParallelGroupExample), &wf), "the example must be valid manifest JSON")
	require.True(t, workflow.HasGroups(&wf))

	children := guidanceChildren{"pr-audit": {
		{Name: "focus", Type: "string", Required: true},
		{Name: "label", Type: "string"},
		{Name: "pr_number", Type: "number"},
	}}
	errs := workflow.Validate(&wf, guidanceFiles{}, mcpStubProfileChecker{}, nil,
		workflow.WithChildContractReader(children), workflow.WithGroupCheckersRequired())
	for _, e := range errs {
		assert.Equal(t, "warning", e.Severity, "the published example must validate clean: %s %s", e.Code, e.Message)
	}

	graph, gerrs := workflow.AnalyzeGroups(&wf)
	assert.Empty(t, gerrs)
	require.Contains(t, graph.Groups, "fanout")
	require.Contains(t, graph.Groups, "audit-fanout")
	assert.Equal(t, workflow.GroupFormBranches, graph.Groups["fanout"].Config.Form)
	assert.Equal(t, workflow.GroupFormWorkflows, graph.Groups["audit-fanout"].Config.Form)
	require.NotNil(t, graph.Groups["audit-fanout"].Parent, "the example demonstrates nesting")
	assert.Equal(t, "fanout", graph.Groups["audit-fanout"].Parent.GroupNodeID)
	assert.Equal(t, "audit", graph.Groups["audit-fanout"].Parent.TaskKey)
	assert.Equal(t, []string{"audit-fanout"}, graph.Groups["fanout"].Nested)
}

func TestWorkflowGroupTools_DescriptionsCarryGuidance(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	seen := map[string]bool{}
	for _, tool := range srv.ListTools() {
		switch tool.Name {
		case "workflow_group_upsert":
			seen[tool.Name] = true
			assert.Contains(t, tool.Description, "first_success")
			assert.Contains(t, tool.Description, "results_and_files")
		case "workflow_group_get", "workflow_group_delete":
			seen[tool.Name] = true
			assert.Contains(t, tool.Description, "parallel_group")
		case "workflow_create", "workflow_update":
			assert.Contains(t, tool.Description, "workflow_group_upsert")
		}
	}
	assert.Len(t, seen, 3)
}
```

Update `adapter/mcp/register_test.go:25-47` so the API-surface list includes the new tools: change the comment `// Workflow management (12)` to `// Workflow management (16)` and, after `"workflow_copy",` (line 39), add:

```go
	"workflow_group_upsert",
	"workflow_group_get",
	"workflow_group_delete",
```

- [x] Step 3: Write the failing REST parity tests. Create `adapter/http/workflow_handler_group_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/workflow"
)

type hgValidation struct {
	Valid  bool                       `json:"valid"`
	Errors []workflow.ValidationError `json:"errors"`
	Code   string                     `json:"code"`
	Error  string                     `json:"error"`
}

func hgCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

func newGroupWorkflowHandler(t *testing.T) (*adapthttp.WorkflowHandler, *filesystem.WorkflowRepo, string) {
	t.Helper()
	root := t.TempDir()
	repo := filesystem.NewWorkflowRepo(root)
	fr := newMockFileReader()
	profiles := stubProfileChecker{"agent-p": "agent", "chat-p": "chat"}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetGroupCheckers(profiles, fr)
	h := adapthttp.NewWorkflowHandler(repo, &mockWorkflowService{}, fr, nil, profiles)
	h.SetWorkflowService(svc)
	return h, repo, root
}

func hgWorkflowsGroup(name, child string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "d", Author: "t",
		Nodes: []workflow.Node{
			{ID: "reviews", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form: workflow.GroupFormWorkflows, CompletionRule: workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResults,
				Tasks: []workflow.GroupTask{{Key: "a", Workflow: child}},
			}}},
			{ID: "done", Type: workflow.NodeTypeTransform},
			{ID: "failed", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "reviews", Target: "done", Output: "success"},
			{Source: "reviews", Target: "failed", Output: "failure"},
		},
	}
}

func hgValidate(t *testing.T, h *adapthttp.WorkflowHandler, name string) hgValidation {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/workflows/"+name+"/validate", nil)
	req = withChiURLParam(req, "name", name)
	w := httptest.NewRecorder()
	h.Validate(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var v hgValidation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))
	return v
}

func TestWorkflowHandler_Validate_GroupOptionsAppended(t *testing.T) {
	h, repo, _ := newGroupWorkflowHandler(t)
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "orphaned", hgWorkflowsGroup("orphaned", "ghost-child")))

	v := hgValidate(t, h, "orphaned")
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW", "REST validate must use the same scoped child reader as MCP")
}

func TestWorkflowHandler_Validate_AdvisoryOnlyIsValid(t *testing.T) {
	h, repo, _ := newGroupWorkflowHandler(t)
	child := &workflow.Workflow{Name: "reviewer", Inputs: []workflow.InputField{{Name: "topic", Type: "string", Required: true}},
		Nodes: []workflow.Node{{ID: "c1", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "reviewer", child))
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "inherits", hgWorkflowsGroup("inherits", "reviewer")))

	v := hgValidate(t, h, "inherits")
	assert.True(t, v.Valid, "%v", v.Errors)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_REQUIRED_INPUT_RUNTIME")
}

func TestWorkflowHandler_CreateUpdate_GroupErrorsAre422(t *testing.T) {
	h, repo, root := newGroupWorkflowHandler(t)

	bad := hgWorkflowsGroup("bad", "ghost-child")
	body, err := json.Marshal(bad)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/workflows", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Create(w, withTestIdentity(req))

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var v hgValidation
	require.NoError(t, json.NewDecoder(w.Body).Decode(&v))
	assert.Equal(t, "GROUP_VALIDATION_FAILED", v.Code)
	assert.False(t, v.Valid)
	assert.Contains(t, hgCodes(v.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	_, statErr := os.Stat(filepath.Join(root, "users", testUserID.String(), "bad"))
	assert.True(t, os.IsNotExist(statErr), "rejected create must not write the folder")

	good := &workflow.Workflow{Name: "edit-me", Nodes: []workflow.Node{{ID: "x", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(testUserID), "edit-me", good))
	upd := hgWorkflowsGroup("edit-me", "ghost-child")
	body, err = json.Marshal(upd)
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPut, "/workflows/edit-me", strings.NewReader(string(body)))
	req = withChiURLParam(req, "name", "edit-me")
	w = httptest.NewRecorder()
	h.Update(w, req)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	stored, err := repo.Load(context.Background(), workflow.Private(testUserID), "edit-me")
	require.NoError(t, err)
	assert.Len(t, stored.Nodes, 1, "rejected update leaves the stored definition untouched")
}
```

- [x] Step 4: Run the tests, confirm they fail.

  Red confirmed: MCP fails to build (missing `ParallelGroupExample`); REST omits the required-input advisory and returns 500 instead of structured 422 group rejections.

```bash
go test ./adapter/mcp/ -run 'TestWorkflowGroupTools|TestWorkflowCreateUpdate_GroupErrorsAreStructured|TestWorkflowValidate_Groups|TestWorkflowManifestHint_DocumentsParallelGroups|TestParallelGroupExample|TestRegisterAllTools' -count=1
go test ./adapter/http/ -run 'TestWorkflowHandler_Validate_GroupOptionsAppended|TestWorkflowHandler_Validate_AdvisoryOnlyIsValid|TestWorkflowHandler_CreateUpdate_GroupErrorsAre422' -count=1
```
Expected:
```
# github.com/arbitron/arbitron/adapter/mcp_test [github.com/arbitron/arbitron/adapter/mcp.test]
adapter/mcp/authoring_guidance_test.go:...: undefined: adaptmcp.ParallelGroupExample
FAIL	github.com/arbitron/arbitron/adapter/mcp [build failed]
--- FAIL: TestWorkflowHandler_Validate_GroupOptionsAppended (0.00s)
    workflow_handler_group_test.go:...: Should contain "GROUP_UNKNOWN_CHILD_WORKFLOW"
--- FAIL: TestWorkflowHandler_Validate_AdvisoryOnlyIsValid (0.00s)
--- FAIL: TestWorkflowHandler_CreateUpdate_GroupErrorsAre422 (0.00s)
    workflow_handler_group_test.go:...: expected: 422 actual: 500
FAIL	github.com/arbitron/arbitron/adapter/http
```

- [x] Step 5: Implement the tools. Create `adapter/mcp/tools_workflow_group.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// registerWorkflowGroupTools registers the parallel_group authoring tools. It
// is called from RegisterWorkflowTools so every surface that exposes the
// workflow tools exposes these too. Each handler is a thin delegate to the
// workflow.Service group operations (authorization, validation and atomic
// persistence live there).
func registerWorkflowGroupTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_group_upsert",
			gomcp.WithDescription("Create or revise ONE parallel_group node: all settings, ordered tasks with their inputs, and the success/failure continuation targets. "+
				"Rewrites the group node and all of its outgoing edges atomically and re-validates; nothing is saved when validation fails "+
				`(the result is then {"valid":false,"errors":[...]}). Body nodes and edges INTO the group are authored with workflow_create/workflow_update.`+
				ParallelGroupGuidance),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("group", gomcp.Required(), gomcp.Description(
				`JSON-encoded group: {"nodeId":"g","label":"...","description":"...","entrypoint":true|false (omit to keep),`+
					`"config":{"form":"branches"|"workflows","tasks":[{"key":"k","entry":"<node id>"} or {"key":"k","workflow":"<name>","inputs":{...}}],`+
					`"completionRule":"all_succeed"|"first_success","unfinishedBehavior":"wait_for_remaining"|"cancel_unfinished"|"finish_independently",`+
					`"gatherMode":"results"|"results_and_files","repoPath":"...","resolverProfile":"<agent profile>","resolverPromptFile":"<file>"},`+
					`"successTarget":"<node id>","failureTarget":"<node id>"}`)),
		),
		workflowGroupUpsertHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_group_get",
			gomcp.WithDescription("Inspect parallel_group nodes (read-only): ordered tasks and config, continuation targets, computed branch-body node ids, nested groups, the enclosing group/task, and advisories. Omit nodeId to list every group in workflow order."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("nodeId", gomcp.Description("Group node id; omit to list all groups")),
		),
		workflowGroupGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_group_delete",
			gomcp.WithDescription("Delete one parallel_group node and every edge touching it. Returns retainedNodes (former body nodes and continuation targets left in the workflow) so you can reconnect or remove them. Refused, with structured errors, when the deletion would leave another group invalid."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("nodeId", gomcp.Required(), gomcp.Description("Group node id to delete")),
		),
		workflowGroupDeleteHandler(srv),
	)
}

func workflowGroupUpsertHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		raw, _ := args["group"].(string)
		if raw == "" {
			return gomcp.NewToolResultError("group is required"), nil
		}
		up, err := decodeGroupUpsert(raw)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid group JSON: %s", err)), nil
		}
		view, err := workflowSvc(srv).UpsertGroup(ctx, name, up)
		if err != nil {
			return groupToolError(name, "upsert", err), nil
		}
		return MarshalResult(view)
	}
}

func workflowGroupGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		nodeID, _ := args["nodeId"].(string)
		if nodeID == "" {
			list, err := workflowSvc(srv).ListGroups(ctx, name)
			if err != nil {
				return groupToolError(name, "list", err), nil
			}
			if list == nil {
				list = []workflow.GroupView{}
			}
			return MarshalResult(list)
		}
		view, err := workflowSvc(srv).GetGroup(ctx, name, nodeID)
		if err != nil {
			return groupToolError(name, "get", err), nil
		}
		return MarshalResult(view)
	}
}

func workflowGroupDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		nodeID, _ := args["nodeId"].(string)
		if nodeID == "" {
			return gomcp.NewToolResultError("nodeId is required"), nil
		}
		res, err := workflowSvc(srv).DeleteGroup(ctx, name, nodeID)
		if err != nil {
			return groupToolError(name, "delete", err), nil
		}
		return MarshalResult(res)
	}
}

// decodeGroupUpsert strictly decodes the group argument: unknown fields (a
// typo such as "completion_rule") and trailing data are rejected so the author
// gets actionable feedback instead of a silently ignored setting.
func decodeGroupUpsert(raw string) (workflow.GroupUpsert, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var up workflow.GroupUpsert
	if err := dec.Decode(&up); err != nil {
		return workflow.GroupUpsert{}, err
	}
	if dec.More() {
		return workflow.GroupUpsert{}, errors.New("unexpected data after the group object")
	}
	return up, nil
}

// groupValidationResult renders a *workflow.GroupValidationError in
// workflow_validate's {"valid":false,"errors":[...]} shape with IsError=true.
// It returns (nil, false) for any other error.
func groupValidationResult(err error) (*gomcp.CallToolResult, bool) {
	var gve *workflow.GroupValidationError
	if !errors.As(err, &gve) {
		return nil, false
	}
	errs := gve.Errors
	if errs == nil {
		errs = []workflow.ValidationError{}
	}
	res, _ := MarshalResult(map[string]any{"valid": false, "errors": errs})
	res.IsError = true
	return res, true
}

// groupToolError maps a group-operation error to a tool result.
func groupToolError(name, op string, err error) *gomcp.CallToolResult {
	if res, ok := groupValidationResult(err); ok {
		return res
	}
	switch {
	case errors.Is(err, workflow.ErrGroupUpsertInvalid):
		return gomcp.NewToolResultError(fmt.Sprintf("invalid group: %s", err))
	case errors.Is(err, workflow.ErrGroupNotFound):
		// The wrapped message already reads `parallel group not found: "<id>" in workflow "<name>"`.
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("not authenticated")
	}
	if res, ok := mapLifecycleError(name, err); ok {
		return res
	}
	return gomcp.NewToolResultError(fmt.Sprintf("failed to %s parallel group: %s", op, err))
}

// hasHardValidationError reports whether any finding gates validity; advisory
// findings (Severity "warning") never do.
func hasHardValidationError(errs []workflow.ValidationError) bool {
	for _, e := range errs {
		if e.Severity != "warning" {
			return true
		}
	}
	return false
}
```

In `adapter/mcp/tools_workflow.go`:

1. At the end of `RegisterWorkflowTools` (after the `workflow_audit` registration, line 221, before the closing brace), add:
```go
	// Parallel-group authoring tools (workflow_group_upsert/get/delete).
	registerWorkflowGroupTools(srv)
```

2. In `workflowCreateHandler`, replace lines 302-307 with:
```go
		if err := workflowSvc(srv).Create(ctx, wf.Name, &wf); err != nil {
			if res, ok := groupValidationResult(err); ok {
				return res, nil
			}
			if errors.Is(err, workflow.ErrAlreadyExists) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow already exists: %s", wf.Name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to create workflow: %s", err)), nil
		}
```

3. In `workflowUpdateHandler`, replace lines 337-345 with:
```go
		if err := workflowSvc(srv).Update(ctx, name, &wf); err != nil {
			if res, ok := groupValidationResult(err); ok {
				return res, nil
			}
			if errors.Is(err, workflow.ErrSharedReadOnly) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", name)), nil
			}
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to update workflow: %s", err)), nil
		}
```

4. In `workflowValidateHandler`, replace lines 395-410 with:
```go
		checker := ownerBoundChecker{fr: srv.deps.FileReader, owner: callerOwner(ctx)}
		// Same author-time checks the REST validate endpoint runs, so the two
		// surfaces cannot drift: the spawn reference check (skipped when the
		// listing fails) plus the caller-scoped parallel_group checkers the write
		// paths use (child contracts; missing profile/file checkers reported).
		opts := workflowSvc(srv).GroupValidateOptions(ctx)
		validationErrors := workflow.Validate(wf, checker, srv.deps.ProfileChecker, srv.deps.ConnChecker, opts...)
		if validationErrors == nil {
			validationErrors = []workflow.ValidationError{}
		}
		result := map[string]any{
			// Advisories (Severity "warning", e.g. GROUP_REQUIRED_INPUT_RUNTIME)
			// are reported but never make a workflow invalid.
			"valid":  !hasHardValidationError(validationErrors),
			"errors": validationErrors,
		}
		return MarshalResult(result)
```

- [x] Step 6: Implement the guidance. In `adapter/mcp/authoring_guidance.go` replace lines 31-33 (the `WorkflowManifestHint` declaration) with:

```go
// WorkflowManifestHint is appended to the workflow_create and workflow_update
// tool descriptions: the variable conventions plus the parallel_group guide.
const WorkflowManifestHint = " Use the standard variable conventions from the server instructions (repo/repo_url, base_branch, pr_number, slug, branch, work_dir); set the optional top-level gitVariable when the run's branch lives under a non-standard variable." +
	ParallelGroupGuidance

// ParallelGroupGuidance documents parallel_group authoring for every tool
// that writes workflow definitions (workflow_create, workflow_update,
// workflow_group_upsert).
const ParallelGroupGuidance = ` Parallel groups: a "parallel_group" node fans out a FIXED list of tasks, in the author order you give, and gathers them; its settings live under config.group. ` +
	`Forms: "branches" - each task names an "entry" node of a branch body inside this workflow, and the group has exactly one edge per task {"source":<group>,"target":<entry>,"output":"task","label":<task key>}; entries are distinct, each body is owned by exactly one task (only the group's task edge may enter it, no edge may leave it to the main path or a sibling body, and bodies end at sink nodes). ` +
	`"workflows" - each task names a child "workflow" you can see (the same workflow may repeat with different "inputs") and has no task edges. ` +
	`Every group has exactly one "success" and one "failure" outgoing edge and no other outputs. ` +
	`Tasks: "key" is unique and matches [A-Za-z0-9_-]+; "inputs" are literals or {{templates}} applied to that task's own copy of the group-start variables ("workspace" is reserved and set by the engine); a required child input you do not map is checked at run time (validation reports an advisory). ` +
	`Results are never merged into parent variables: read them downstream as {{<group>.tasks.N.result.<var>}}, {{<group>.byKey.<key>.status}} or {{<group>.winner}}. ` +
	`completionRule "all_succeed" (unfinishedBehavior "wait_for_remaining" or "cancel_unfinished" once a task has failed) or "first_success" (unfinishedBehavior "cancel_unfinished" or "finish_independently" after the winner). ` +
	`gatherMode "results" or "results_and_files"; file gathering requires resolverProfile naming an AGENT-kind profile, an optional resolverPromptFile that must already exist in the workflow (write it with workflow_write_file first), and accepts an optional repoPath relative to the parent's {{workspace}}. ` +
	`Nesting: a group may sit inside another group's branch body (its success/failure continuations stay in that body) and owns its own task bodies. A group may be the entrypoint but never on a cycle. ` +
	`Author bodies and groups together in one workflow_create/workflow_update manifest, revise one group with workflow_group_upsert, inspect with workflow_group_get, remove with workflow_group_delete (it lists retained nodes), and check with workflow_validate. A server that cannot execute parallel groups yet still accepts and validates them but refuses to trigger them. ` +
	`Example (branches group whose "audit" task is a nested first_success workflows group): ` + ParallelGroupExample

// ParallelGroupExample is a complete, valid manifest demonstrating both forms,
// nesting, ordered tasks, per-task inputs on a repeated child workflow, both
// completion rules and all continuation ports. A unit test validates it.
const ParallelGroupExample = `{"name":"parallel-review","description":"Lint and audit a PR in parallel, then report","author":"you",
"inputs":[{"name":"pr_number","type":"number","description":"pull request under work","required":true}],
"nodes":[
 {"id":"fanout","type":"parallel_group","entrypoint":true,"label":"Lint and audit","config":{"group":{"form":"branches",
   "tasks":[{"key":"lint","entry":"lint-run"},{"key":"audit","entry":"audit-fanout"}],
   "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
 {"id":"lint-run","type":"tool_call","label":"Lint","config":{"command":"make","args":["lint"]}},
 {"id":"audit-fanout","type":"parallel_group","label":"Audit (first answer wins)","config":{"group":{"form":"workflows",
   "tasks":[{"key":"security","workflow":"pr-audit","inputs":{"focus":"security","label":"PR {{pr_number}} security"}},
            {"key":"perf","workflow":"pr-audit","inputs":{"focus":"performance","label":"PR {{pr_number}} performance"}}],
   "completionRule":"first_success","unfinishedBehavior":"finish_independently","gatherMode":"results"}}},
 {"id":"audit-ok","type":"transform","config":{"operations":[{"variable":"audit_winner","operation":"set","value":"{{audit-fanout.winner}}"}]}},
 {"id":"audit-failed","type":"transform","config":{"operations":[{"variable":"audit_failed","operation":"set","value":true}]}},
 {"id":"report","type":"transform","config":{"operations":[{"variable":"lint_status","operation":"set","value":"{{fanout.byKey.lint.status}}"}]}},
 {"id":"report-failure","type":"transform","config":{"operations":[{"variable":"review_failed","operation":"set","value":true}]}}
],
"edges":[
 {"source":"fanout","target":"lint-run","output":"task","label":"lint"},
 {"source":"fanout","target":"audit-fanout","output":"task","label":"audit"},
 {"source":"fanout","target":"report","output":"success"},
 {"source":"fanout","target":"report-failure","output":"failure"},
 {"source":"audit-fanout","target":"audit-ok","output":"success"},
 {"source":"audit-fanout","target":"audit-failed","output":"failure"}
]}`
```

- [x] Step 7: Implement REST parity. In `adapter/http/workflow_handler.go`:

1. In `Validate`, replace lines 204-222 (from `checker := ownerBoundChecker{...}` through the final `WriteJSON(...)` call; the function's closing brace on line 223 stays) with:
```go
	checker := ownerBoundChecker{fr: h.fileReader, owner: callerOwner(r.Context())}
	// The SAME option set MCP workflow_validate uses: the spawn_workflow
	// reference check over the caller's visible names (skipped when the listing
	// fails) plus the caller-scoped parallel_group checkers.
	opts := h.wfSvc.GroupValidateOptions(r.Context())
	errs := workflow.Validate(wf, checker, h.profileChecker, h.connChecker, opts...)
	valid := !hasHardValidationError(errs)
	if errs == nil {
		errs = []workflow.ValidationError{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"valid":  valid,
		"errors": errs,
	})
```
   Then, directly after `Validate`'s closing brace (before the `const maxTriggerBodyBytes` line), add the two helpers:
```go
// hasHardValidationError reports whether any finding gates validity;
// advisories (Severity "warning") never do.
func hasHardValidationError(errs []workflow.ValidationError) bool {
	for _, e := range errs {
		if e.Severity != "warning" {
			return true
		}
	}
	return false
}

// writeGroupValidationError renders a rejected parallel-group write as 422 with
// the same errors array validate returns. It reports false for other errors.
func writeGroupValidationError(w http.ResponseWriter, err error) bool {
	var gve *workflow.GroupValidationError
	if !errors.As(err, &gve) {
		return false
	}
	errs := gve.Errors
	if errs == nil {
		errs = []workflow.ValidationError{}
	}
	WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  gve.Error(),
		"code":   "GROUP_VALIDATION_FAILED",
		"valid":  false,
		"errors": errs,
	})
	return true
}
```

2. In `Create`, replace lines 508-515 with:
```go
	if err := h.wfSvc.Create(r.Context(), wf.Name, &wf); err != nil {
		if writeGroupValidationError(w, err) {
			return
		}
		if errors.Is(err, workflow.ErrAlreadyExists) {
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create workflow")
		return
	}
```

3. In `Update`, replace lines 537-543 with:
```go
	if err := h.wfSvc.Update(r.Context(), name, &wf); err != nil {
		if writeGroupValidationError(w, err) {
			return
		}
		if writeSharedReadOnlyOrNotFound(w, name, err) {
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update workflow")
		return
	}
```

- [x] Step 8: Run the new tests and both adapter packages (regression for every existing MCP/HTTP test).

  All specified focused tests, both full adapter packages, and `go vet` passed.
  Self-review completed in order: spec compliance, anti-green-theater, code quality.
  Corrected the advisory-only fixture to exclude unused local branch nodes (their
  `UNREACHABLE_NODE` findings are hard errors, not advisories). Added red→green
  regressions for trailing JSON delimiters and absent file checkers; validated
  real prompt-file creation/deletion and persisted group revisions. Temporarily
  removing `GroupValidateOptions` made both adapters' visible-child input-contract
  tests fail; restored the options and reran all verifications green. Production
  registration flows through `RegisterAllTools`; service checker initialization
  remains owned by P1-T8 as specified.

```bash
go test ./adapter/mcp/ -run 'TestWorkflowGroupTools|TestWorkflowCreateUpdate_GroupErrorsAreStructured|TestWorkflowValidate_Groups|TestWorkflowManifestHint_DocumentsParallelGroups|TestParallelGroupExample|TestRegisterAllTools|TestWorkflowCreateUpdate_DescriptionsPointAtConventions' -count=1 -v
go test ./adapter/http/ -run 'TestWorkflowHandler_Validate|TestWorkflowHandler_CreateUpdate_GroupErrorsAre422|TestWorkflowHandler_Create|TestWorkflowHandler_Update' -count=1
go test ./adapter/mcp/... ./adapter/http/... -count=1
go vet ./adapter/mcp/ ./adapter/http/
```
Expected:
```
--- PASS: TestWorkflowGroupTools_Registered (0.00s)
--- PASS: TestWorkflowGroupTools_Upsert_BranchesHappyPath (0.00s)
...
--- PASS: TestRegisterAllTools_Count (0.00s)
PASS
ok  	github.com/arbitron/arbitron/adapter/mcp	0.XXXs
ok  	github.com/arbitron/arbitron/adapter/http	0.XXXs
```

- [x] Step 9: Commit.

```bash
git add adapter/mcp/tools_workflow_group.go adapter/mcp/tools_workflow.go adapter/mcp/authoring_guidance.go \
  adapter/mcp/tools_workflow_group_test.go adapter/mcp/authoring_guidance_test.go adapter/mcp/register_test.go \
  adapter/http/workflow_handler.go adapter/http/workflow_handler_group_test.go
git commit -m "feat(mcp): workflow_group_upsert/get/delete tools and group-aware validate

Group tools delegate to the workflow.Service group ops; group rejections from
upsert/delete/create/update return workflow_validate's structured
{valid:false, errors} shape. MCP workflow_validate and REST validate append
Service.GroupValidateOptions (caller-scoped child contracts, required
checkers) and treat advisories as non-gating. REST create/update map group
rejections to 422 GROUP_VALIDATION_FAILED. WorkflowManifestHint documents
forms, nesting, tasks, rules, ports, gathering prerequisites and a validated
example.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
