package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testdataPath(t *testing.T) string {
	t.Helper()
	// testdata is relative to this test file
	dir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	return dir
}

func TestWorkflowRepo_Load_ValidWorkflow(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"valid-workflow")
	require.NoError(t, err)
	require.NotNil(t, wf)

	// Top-level fields
	assert.Equal(t, "valid-workflow", wf.Name)
	assert.Equal(t, "A test workflow with one transform node", wf.Description)
	assert.Equal(t, "test-author", wf.Author)

	// Default error behavior
	assert.Equal(t, "retry", wf.DefaultErrorBehavior.Type)
	assert.Equal(t, 3, wf.DefaultErrorBehavior.Retries)
	assert.Equal(t, 5, wf.DefaultErrorBehavior.RetryDelaySeconds)

	// Inputs
	require.Len(t, wf.Inputs, 1)
	assert.Equal(t, "inputText", wf.Inputs[0].Name)
	assert.Equal(t, "string", wf.Inputs[0].Type)
	assert.Equal(t, "The text to transform", wf.Inputs[0].Description)
	assert.True(t, wf.Inputs[0].Required)

	// Nodes
	require.Len(t, wf.Nodes, 1)
	node := wf.Nodes[0]
	assert.Equal(t, "transform-1", node.ID)
	assert.Equal(t, workflow.NodeTypeTransform, node.Type)
	assert.Equal(t, "Uppercase Transform", node.Label)
	assert.Equal(t, "Transforms input to uppercase", node.Description)
	assert.True(t, node.Entrypoint)

	// Node config operations
	require.Len(t, node.Config.Operations, 1)
	op := node.Config.Operations[0]
	assert.Equal(t, "outputText", op.Variable)
	assert.Equal(t, "uppercase", op.Operation)
	assert.Equal(t, "{{ inputText }}", op.Value)

	// Edges
	assert.Empty(t, wf.Edges)
}

func TestWorkflowRepo_Load_MissingDirectory(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"nonexistent-workflow")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-workflow")
}

func TestWorkflowRepo_Load_MissingManifest(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"empty-dir")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty-dir")
}

func TestWorkflowRepo_Load_MalformedJSON(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"malformed-json")
	assert.Nil(t, wf)
	assert.Error(t, err)
}

func TestWorkflowRepo_List(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	summaries, err := repo.List(context.Background())
	require.NoError(t, err)

	// Should find all-node-types and valid-workflow; skip empty-dir and malformed-json.
	require.Len(t, summaries, 2)

	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	assert.Contains(t, names, "valid-workflow")
	assert.Contains(t, names, "all-node-types")
}

func TestWorkflowRepo_Load_AllNodeTypes(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"all-node-types")
	require.NoError(t, err)
	require.NotNil(t, wf)
	require.Len(t, wf.Nodes, 5)

	byID := make(map[string]workflow.Node, 5)
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}

	// LLM node
	llm := byID["llm-node"]
	assert.Equal(t, workflow.NodeTypeLLMCall, llm.Type)
	assert.Equal(t, "prompt.md", llm.Config.TemplateFile)
	assert.Equal(t, "claude-sonnet-4-6", llm.Config.Model)
	assert.Equal(t, 1000, llm.Config.MaxTokens)
	assert.Equal(t, 0.7, llm.Config.Temperature)
	assert.NotNil(t, llm.Config.OutputSchema)

	// Script node
	script := byID["script-node"]
	assert.Equal(t, workflow.NodeTypeScript, script.Type)
	assert.Equal(t, "run.sh", script.Config.ScriptFile)
	assert.Equal(t, "bash", script.Config.Interpreter)
	assert.Equal(t, []string{"--input", "{{llm-node.result}}"}, script.Config.Args)
	assert.Equal(t, 30, script.Config.TimeoutSeconds)

	// Tool call node
	tool := byID["tool-node"]
	assert.Equal(t, workflow.NodeTypeToolCall, tool.Type)
	assert.Equal(t, "git", tool.Config.Command)
	assert.Equal(t, []string{"status"}, tool.Config.Args)
	assert.Equal(t, 10, tool.Config.TimeoutSeconds)

	// Conditional node
	cond := byID["cond-node"]
	assert.Equal(t, workflow.NodeTypeConditional, cond.Type)
	require.Len(t, cond.Config.Conditions, 1)
	assert.Equal(t, "score", cond.Config.Conditions[0].Field)
	assert.Equal(t, "gt", cond.Config.Conditions[0].Operator)
	assert.Equal(t, "high", cond.Config.Conditions[0].Output)
	assert.Equal(t, "low", cond.Config.DefaultOutput)

	// Transform node
	transform := byID["transform-node"]
	assert.Equal(t, workflow.NodeTypeTransform, transform.Type)
	require.Len(t, transform.Config.Operations, 1)
	assert.Equal(t, "result", transform.Config.Operations[0].Variable)
	assert.Equal(t, "set", transform.Config.Operations[0].Operation)
}

func TestWorkflowRepo_List_EmptyWorkspace(t *testing.T) {
	// Create a temporary empty directory
	tmpDir, err := os.MkdirTemp("", "arbitron-test-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	repo := filesystem.NewWorkflowRepo(tmpDir)

	summaries, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestWorkflowRepo_Load_PathTraversalRejected(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	// A name like "../sibling" could escape the workspace root.
	_, err := repo.Load(context.Background(),"../sibling")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
