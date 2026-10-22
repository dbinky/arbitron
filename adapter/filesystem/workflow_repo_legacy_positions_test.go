package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
)

func TestWorkflowRepo_HasNoSaveNodePositions(t *testing.T) {
	_, ok := reflect.TypeOf(&filesystem.WorkflowRepo{}).MethodByName("SaveNodePositions")
	assert.False(t, ok, "the filesystem repo must not keep a position-patching write path")
}

// Pre-feature and hand-edited manifest positions remain readable fallback data.
func TestWorkflowRepo_Load_ParsesLegacyNodePositions(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	wfDir := filepath.Join(scopeRootFor(t, dir, testScope()), "legacy-pos")
	require.NoError(t, os.MkdirAll(wfDir, 0o700))
	manifest := `{
  "name": "legacy-pos",
  "nodes": [
    {"id": "A", "type": "transform", "entrypoint": true, "config": {}, "position": {"x": 120.5, "y": -40}},
    {"id": "B", "type": "transform", "config": {}}
  ],
  "edges": [{"source": "A", "target": "B"}]
}`
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "workflow.json"), []byte(manifest), 0o644))
	loaded, err := repo.Load(context.Background(), testScope(), "legacy-pos")
	require.NoError(t, err)
	require.Len(t, loaded.Nodes, 2)
	require.NotNil(t, loaded.Nodes[0].Position)
	assert.Equal(t, 120.5, loaded.Nodes[0].Position.X)
	assert.Equal(t, -40.0, loaded.Nodes[0].Position.Y)
	assert.Nil(t, loaded.Nodes[1].Position)
}

// Normal authoring still persists supplied presentation data verbatim.
func TestWorkflowRepo_Update_PreservesManifestPositions(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()
	wf := sampleWorkflow("pos-roundtrip")
	require.NoError(t, repo.Create(ctx, testScope(), "pos-roundtrip", wf))
	wf.Nodes[0].Position = &workflow.NodePosition{X: 7, Y: 9}
	require.NoError(t, repo.Update(ctx, testScope(), "pos-roundtrip", wf))
	loaded, err := repo.Load(ctx, testScope(), "pos-roundtrip")
	require.NoError(t, err)
	require.NotNil(t, loaded.Nodes[0].Position)
	assert.Equal(t, workflow.NodePosition{X: 7, Y: 9}, *loaded.Nodes[0].Position)
}
