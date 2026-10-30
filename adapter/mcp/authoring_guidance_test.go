package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthoringGuidance_NamesStandardConventions(t *testing.T) {
	for _, name := range []string{
		"repo", "repo_url", "base_branch", "pr_number", "slug", "teams_webhook_url",
		"branch", "work_dir", "gitVariable", "model_profile",
	} {
		assert.Contains(t, adaptmcp.AuthoringGuidance, name, "guidance should document %q", name)
	}
}

func TestWorkflowCreateUpdate_DescriptionsPointAtConventions(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	found := map[string]bool{}
	for _, tool := range srv.ListTools() {
		if tool.Name != "workflow_create" && tool.Name != "workflow_update" {
			continue
		}
		found[tool.Name] = true
		assert.Contains(t, tool.Description, "gitVariable", tool.Name)
		assert.Contains(t, tool.Description, "conventions", tool.Name)
	}
	assert.True(t, found["workflow_create"] && found["workflow_update"], "both tools registered")
}

func TestServer_InitializeCarriesAuthoringInstructions(t *testing.T) {
	srv := adaptmcp.NewServer("arbitron", "test")
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp := srv.MCPServer().HandleMessage(context.Background(), json.RawMessage(req))
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var decoded struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, adaptmcp.AuthoringGuidance, decoded.Result.Instructions)
}

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

func TestWorkflowManifestHint_DocumentsLibrary(t *testing.T) {
	for _, want := range []string{
		"@lib/", `name "@lib"`, "scope", `"shared"`, "lib root", "private then shared", "scriptFile", "orchestrationFile",
	} {
		assert.Contains(t, adaptmcp.WorkflowManifestHint, want)
	}
}

func TestWorkflowManifestHint_DocumentsSpawnInputs(t *testing.T) {
	assert.Equal(t, 0, strings.Count(adaptmcp.ParallelGroupGuidance, "spawn_workflow"), "spawn guidance is its own const, not parallel-group prose")
	for _, want := range []string{
		"spawn_workflow", "config.inputs", "Mapped inputs win", "fails closed", "fully mapped",
		"no model_profile", "never LLM-filled", "inputSources",
	} {
		assert.Contains(t, adaptmcp.SpawnInputsGuidance, want)
		assert.Contains(t, adaptmcp.WorkflowManifestHint, want)
	}
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

// A group gives every task its own copy of the parent's working area and
// refuses, before any task starts, a start variable holding an absolute path
// inside the parent's {{workspace}} (engine.ErrGroupSharedPath). The standard
// work_dir convention is exactly such a path, so the group guidance must tell
// authors to override it per task with a {{workspace}}-relative input.
func TestParallelGroupGuidance_WarnsThatAbsoluteWorkDirIsRefusedUnlessOverriddenPerTask(t *testing.T) {
	for name, text := range map[string]string{
		"ParallelGroupGuidance": adaptmcp.ParallelGroupGuidance,
		"WorkflowManifestHint":  adaptmcp.WorkflowManifestHint,
	} {
		assert.Contains(t, text, "work_dir", "%s must name the work_dir convention", name)
		assert.Contains(t, text, "{{workspace}}/", "%s must show the task-local path form", name)
		assert.Contains(t, text, "refused before any task starts", "%s must say the launch is refused", name)
	}
}
