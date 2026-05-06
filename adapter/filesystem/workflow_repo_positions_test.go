package filesystem_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTempWorkflow creates a temporary workflow directory with the given JSON
// content and returns the base path (parent of the workflow dir).
func newTempWorkflow(t *testing.T, name string, content []byte) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), content, 0o644))
	return base
}

func TestSaveNodePositions_WritesCorrectly(t *testing.T) {
	wfJSON := []byte(`{
  "name": "pos-test",
  "description": "Position test workflow",
  "nodes": [
    {"id": "A", "type": "transform", "label": "Node A", "config": {}},
    {"id": "B", "type": "script", "label": "Node B", "config": {}}
  ],
  "edges": []
}
`)

	base := newTempWorkflow(t, "pos-test", wfJSON)
	repo := filesystem.NewWorkflowRepo(base)

	positions := map[string]workflow.NodePosition{
		"A": {X: 100, Y: 200},
		"B": {X: 300.5, Y: 400.75},
	}

	err := repo.SaveNodePositions(context.Background(), "pos-test", positions)
	require.NoError(t, err)

	// Re-read the file and verify positions were written.
	data, err := os.ReadFile(filepath.Join(base, "pos-test", "workflow.json"))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	nodes := raw["nodes"].([]any)
	require.Len(t, nodes, 2)

	nodeA := nodes[0].(map[string]any)
	posA := nodeA["position"].(map[string]any)
	assert.Equal(t, float64(100), posA["x"])
	assert.Equal(t, float64(200), posA["y"])

	nodeB := nodes[1].(map[string]any)
	posB := nodeB["position"].(map[string]any)
	assert.Equal(t, 300.5, posB["x"])
	assert.Equal(t, 400.75, posB["y"])
}

func TestSaveNodePositions_PreservesExistingFields(t *testing.T) {
	wfJSON := []byte(`{
  "name": "preserve-test",
  "description": "A workflow with extra fields",
  "author": "tester",
  "customField": "should survive",
  "nodes": [
    {
      "id": "N1",
      "type": "llm_call",
      "label": "LLM",
      "description": "An LLM node",
      "config": {
        "model": "claude-sonnet-4-6",
        "maxTokens": 1000
      },
      "extraNodeField": "also survives"
    }
  ],
  "edges": [{"source": "N1", "target": "N1"}]
}
`)

	base := newTempWorkflow(t, "preserve-test", wfJSON)
	repo := filesystem.NewWorkflowRepo(base)

	positions := map[string]workflow.NodePosition{
		"N1": {X: 42, Y: 84},
	}

	err := repo.SaveNodePositions(context.Background(), "preserve-test", positions)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(base, "preserve-test", "workflow.json"))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	// Top-level fields preserved.
	assert.Equal(t, "preserve-test", raw["name"])
	assert.Equal(t, "A workflow with extra fields", raw["description"])
	assert.Equal(t, "tester", raw["author"])
	assert.Equal(t, "should survive", raw["customField"])

	// Edges preserved.
	edges := raw["edges"].([]any)
	require.Len(t, edges, 1)

	// Node fields preserved.
	node := raw["nodes"].([]any)[0].(map[string]any)
	assert.Equal(t, "N1", node["id"])
	assert.Equal(t, "llm_call", node["type"])
	assert.Equal(t, "LLM", node["label"])
	assert.Equal(t, "An LLM node", node["description"])
	assert.Equal(t, "also survives", node["extraNodeField"])

	// Config preserved.
	cfg := node["config"].(map[string]any)
	assert.Equal(t, "claude-sonnet-4-6", cfg["model"])
	assert.Equal(t, float64(1000), cfg["maxTokens"])

	// Position was set.
	pos := node["position"].(map[string]any)
	assert.Equal(t, float64(42), pos["x"])
	assert.Equal(t, float64(84), pos["y"])
}

func TestSaveNodePositions_WorkflowNotFound(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)

	err := repo.SaveNodePositions(context.Background(), "nonexistent", map[string]workflow.NodePosition{
		"A": {X: 1, Y: 2},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSaveNodePositions_PartialMatch(t *testing.T) {
	// Only some nodes have positions — others should be untouched.
	wfJSON := []byte(`{
  "name": "partial-test",
  "nodes": [
    {"id": "A", "type": "transform", "label": "A", "config": {}},
    {"id": "B", "type": "transform", "label": "B", "config": {}},
    {"id": "C", "type": "transform", "label": "C", "config": {}}
  ]
}
`)

	base := newTempWorkflow(t, "partial-test", wfJSON)
	repo := filesystem.NewWorkflowRepo(base)

	// Only save position for A and C; B should have no position.
	positions := map[string]workflow.NodePosition{
		"A": {X: 10, Y: 20},
		"C": {X: 50, Y: 60},
	}

	err := repo.SaveNodePositions(context.Background(), "partial-test", positions)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(base, "partial-test", "workflow.json"))
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	nodes := raw["nodes"].([]any)
	require.Len(t, nodes, 3)

	nodeA := nodes[0].(map[string]any)
	_, hasA := nodeA["position"]
	assert.True(t, hasA, "Node A should have a position")

	nodeB := nodes[1].(map[string]any)
	_, hasB := nodeB["position"]
	assert.False(t, hasB, "Node B should NOT have a position")

	nodeC := nodes[2].(map[string]any)
	_, hasC := nodeC["position"]
	assert.True(t, hasC, "Node C should have a position")
}
