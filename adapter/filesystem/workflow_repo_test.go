package filesystem_test

import (
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

	wf, err := repo.Load("valid-workflow")
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

	wf, err := repo.Load("nonexistent-workflow")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-workflow")
}

func TestWorkflowRepo_Load_MissingManifest(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load("empty-dir")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "workflow.json")
}

func TestWorkflowRepo_Load_MalformedJSON(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load("malformed-json")
	assert.Nil(t, wf)
	assert.Error(t, err)
}

func TestWorkflowRepo_List(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	summaries, err := repo.List()
	require.NoError(t, err)

	// Should find valid-workflow but skip empty-dir (no workflow.json)
	// and malformed-json (invalid JSON)
	require.Len(t, summaries, 1)
	assert.Equal(t, "valid-workflow", summaries[0].Name)
	assert.Equal(t, "A test workflow with one transform node", summaries[0].Description)
	assert.Equal(t, "test-author", summaries[0].Author)
}

func TestWorkflowRepo_List_EmptyWorkspace(t *testing.T) {
	// Create a temporary empty directory
	tmpDir, err := os.MkdirTemp("", "arbitron-test-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	repo := filesystem.NewWorkflowRepo(tmpDir)

	summaries, err := repo.List()
	require.NoError(t, err)
	assert.Empty(t, summaries)
}
