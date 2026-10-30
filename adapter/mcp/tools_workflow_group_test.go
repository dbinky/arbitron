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
	fr := filesystem.NewFileReader(root)
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

func TestWorkflowGroupTools_Upsert_RevisesPersistedGroup(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))
	require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": gtBranchesGroup}).IsError)
	revised := `{"nodeId":"fanout","label":"Revised","config":{"form":"branches",
	 "tasks":[{"key":"b","entry":"b1"},{"key":"a","entry":"a1"}],
	 "completionRule":"first_success","unfinishedBehavior":"finish_independently","gatherMode":"results"},
	 "successTarget":"failed","failureTarget":"done"}`
	res := callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": revised})
	require.False(t, res.IsError, extractText(t, res))
	stored := f.stored(t, "wf")
	node := gtNode(t, stored, "fanout")
	assert.True(t, node.Entrypoint, "omitting entrypoint retains it")
	assert.Equal(t, "Revised", node.Label)
	require.NotNil(t, node.Config.Group)
	assert.Equal(t, workflow.GroupRuleFirstSuccess, node.Config.Group.CompletionRule)
	assert.Equal(t, workflow.GroupUnfinishedFinishIndependently, node.Config.Group.UnfinishedBehavior)
	assert.Equal(t, []workflow.GroupTask{{Key: "b", Entry: "b1"}, {Key: "a", Entry: "a1"}}, node.Config.Group.Tasks)
	assert.Equal(t, []workflow.Edge{
		{Source: "fanout", Target: "b1", Output: "task", Label: "b"},
		{Source: "fanout", Target: "a1", Output: "task", Label: "a"},
		{Source: "fanout", Target: "failed", Output: "success"},
		{Source: "fanout", Target: "done", Output: "failure"},
	}, stored.Edges)
}

func TestWorkflowGroupTools_Upsert_PromptFileUsesCallerAndWorkflow(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtBaseManifest("wf"))
	f.create(t, gtBaseManifest("other"))
	res := callTool(t, f.srv, "workflow_write_file", map[string]any{
		"name": "other", "path": "prompts/resolve.md", "content": "Resolve the conflicts.",
	})
	require.False(t, res.IsError, extractText(t, res))
	group := `{"nodeId":"fanout","entrypoint":true,"config":{"form":"branches",
	 "tasks":[{"key":"a","entry":"a1"},{"key":"b","entry":"b1"}],
	 "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results_and_files",
	 "resolverProfile":"agent-p","resolverPromptFile":"prompts/resolve.md"},"successTarget":"done","failureTarget":"failed"}`
	before := f.raw(t, "wf")
	res = callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": group})
	require.True(t, res.IsError)
	assert.Contains(t, gtCodes(gtDecodeValidation(t, extractText(t, res)).Errors), "GROUP_MISSING_PROMPT_FILE")
	assert.Equal(t, before, f.raw(t, "wf"), "another workflow's prompt cannot satisfy the check")

	res = callTool(t, f.srv, "workflow_write_file", map[string]any{
		"name": "wf", "path": "prompts/resolve.md", "content": "Resolve this workflow's conflicts.",
	})
	require.False(t, res.IsError, extractText(t, res))
	res = callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "wf", "group": group})
	require.False(t, res.IsError, extractText(t, res))
	g := gtNode(t, f.stored(t, "wf"), "fanout").Config.Group
	require.NotNil(t, g)
	assert.Equal(t, "agent-p", g.ResolverProfile)
	assert.Equal(t, "prompts/resolve.md", g.ResolverPromptFile)
	v := gtDecodeValidation(t, extractText(t, callTool(t, f.srv, "workflow_validate", map[string]any{"name": "wf"})))
	assert.True(t, v.Valid, "%v", v.Errors)

	res = callTool(t, f.srv, "workflow_delete_file", map[string]any{"name": "wf", "path": "prompts/resolve.md"})
	require.False(t, res.IsError, extractText(t, res))
	v = gtDecodeValidation(t, extractText(t, callTool(t, f.srv, "workflow_validate", map[string]any{"name": "wf"})))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_MISSING_PROMPT_FILE")
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
	before := f.raw(t, "wf")

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
		"trailing brace":  {map[string]any{"name": "wf", "group": gtBranchesGroup + `}`}, "invalid group JSON"},
		"trailing array":  {map[string]any{"name": "wf", "group": gtBranchesGroup + `]`}, "invalid group JSON"},
		"unknown target":  {map[string]any{"name": "wf", "group": `{"nodeId":"g","config":{"form":"branches"},"successTarget":"nowhere","failureTarget":"failed"}`}, "invalid group"},
		"unknown wf name": {map[string]any{"name": "ghost", "group": gtBranchesGroup}, "workflow not found: ghost"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res := callTool(t, f.srv, "workflow_group_upsert", tc.args)
			assert.True(t, res.IsError)
			assert.Contains(t, extractText(t, res), tc.want)
			assert.Equal(t, before, f.raw(t, "wf"), "rejected input cannot persist")
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

func TestWorkflowValidate_Groups_VisibleChildContractIsChecked(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtChildManifest("reviewer"))
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtTwoGroupsManifest), &wf))
	wf.Nodes[0].Config.Group = &workflow.GroupConfig{
		Form: workflow.GroupFormWorkflows, Tasks: []workflow.GroupTask{{Key: "review", Workflow: "reviewer", Inputs: map[string]any{"topic": 42}}},
		CompletionRule: workflow.GroupRuleAllSucceed, UnfinishedBehavior: workflow.GroupUnfinishedCancel, GatherMode: workflow.GroupGatherResults,
	}
	// Keep only the first group's continuations and their targets.
	wf.Nodes = []workflow.Node{wf.Nodes[0], gtNode(t, &wf, "done"), gtNode(t, &wf, "failed")}
	wf.Edges = []workflow.Edge{{Source: "first", Target: "done", Output: "success"}, {Source: "first", Target: "failed", Output: "failure"}}
	require.NoError(t, f.repo.Create(context.Background(), workflow.Private(mcpTestUserID), "two", &wf))
	before := f.raw(t, "two")
	res := callTool(t, f.srv, "workflow_validate", map[string]any{"name": "two"})
	require.False(t, res.IsError)
	v := gtDecodeValidation(t, extractText(t, res))
	assert.False(t, v.Valid)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_INPUT_TYPE_MISMATCH", "name-only visibility cannot check the child's declared input type")
	assert.Equal(t, before, f.raw(t, "two"), "validate never saves")
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

func TestWorkflowValidate_Groups_MissingFileCheckerIsReported(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtTwoGroupsManifest)
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{
		Workflows: f.repo, ProfileChecker: gtProfiles, // FileReader deliberately nil.
	})
	adaptmcp.RegisterWorkflowTools(srv)
	res := callTool(t, srv, "workflow_validate", map[string]any{"name": "two"})
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
	var inherits workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtBaseManifest("inherits")), &inherits))
	// Workflows-form tasks have no local branch bodies.
	inherits.Nodes = []workflow.Node{gtNode(t, &inherits, "done"), gtNode(t, &inherits, "failed")}
	manifest, err := json.Marshal(inherits)
	require.NoError(t, err)
	f.create(t, string(manifest))
	inherit := `{"nodeId":"reviews","entrypoint":true,"config":{"form":"workflows","tasks":[{"key":"a","workflow":"reviewer"}],
	  "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"},
	  "successTarget":"done","failureTarget":"failed"}`
	require.False(t, callTool(t, f.srv, "workflow_group_upsert", map[string]any{"name": "inherits", "group": inherit}).IsError)

	v = gtDecodeValidation(t, extractText(t, callTool(t, f.srv, "workflow_validate", map[string]any{"name": "inherits"})))
	assert.True(t, v.Valid, "an advisory alone never makes a workflow invalid: %v", v.Errors)
	assert.Contains(t, gtCodes(v.Errors), "GROUP_REQUIRED_INPUT_RUNTIME")
}

func TestWorkflowValidateTool_ReportsSpawnInputFindings(t *testing.T) {
	f := newGroupToolServer(t, true)
	f.create(t, gtChildManifest("reviewer"))
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(gtBaseManifest("spawner")), &wf))
	wf.Nodes = append([]workflow.Node{{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true,
		Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: 1}}}},
		{ID: "sp", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
			ChildWorkflow: "reviewer", SpawnMode: workflow.SpawnModeFireAndForget,
			Inputs: map[string]any{"topic": "x", "tpoic": "typo"},
		}}}, wf.Nodes...)
	wf.Edges = []workflow.Edge{{Source: "entry", Target: "sp"}, {Source: "sp", Target: "done"},
		{Source: "entry", Target: "a1"}, {Source: "entry", Target: "b1"}, {Source: "entry", Target: "failed"}}
	require.NoError(t, f.repo.Create(context.Background(), workflow.Private(mcpTestUserID), "spawner", &wf))

	res := callTool(t, f.srv, "workflow_validate", map[string]any{"name": "spawner"})
	require.False(t, res.IsError)
	assert.Contains(t, extractText(t, res), "SPAWN_UNKNOWN_CHILD_INPUT")
}
