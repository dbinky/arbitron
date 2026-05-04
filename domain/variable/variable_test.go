package variable

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore(t *testing.T) {
	store := NewStore()
	assert.NotNil(t, store)
}

func TestGetFromInputs_SimpleKey(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	val, ok := store.Get("name")
	assert.True(t, ok)
	assert.Equal(t, "alice", val)
}

func TestGetFromNodeOutputs_DottedPath(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("validate", map[string]any{"score": 85})

	val, ok := store.Get("validate.score")
	assert.True(t, ok)
	assert.Equal(t, 85, val)
}

func TestGetDeepNestedPath(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("analyze", map[string]any{
		"result": map[string]any{
			"items": []any{
				map[string]any{"name": "first"},
				map[string]any{"name": "second"},
			},
		},
	})

	val, ok := store.Get("analyze.result.items.0.name")
	assert.True(t, ok)
	assert.Equal(t, "first", val)
}

func TestGetArrayIndex(t *testing.T) {
	store := NewStore()
	store.SetInput("tags", []any{"go", "rust", "python"})

	val, ok := store.Get("tags.1")
	assert.True(t, ok)
	assert.Equal(t, "rust", val)
}

func TestGetMissingPath(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	val, ok := store.Get("missing")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestGetMissingNestedPath(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node", map[string]any{"a": 1})

	val, ok := store.Get("node.b")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestGetOutOfBoundsArray(t *testing.T) {
	store := NewStore()
	store.SetInput("tags", []any{"go", "rust"})

	val, ok := store.Get("tags.5")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestGetNegativeArrayIndex(t *testing.T) {
	store := NewStore()
	store.SetInput("tags", []any{"go", "rust"})

	// "-1" is not a valid non-negative integer, so it should fail to index
	val, ok := store.Get("tags.-1")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestGetPathIntoNonObject(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")

	val, ok := store.Get("name.first")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestGetNullValue(t *testing.T) {
	store := NewStore()
	store.SetInput("empty", nil)

	val, ok := store.Get("empty")
	assert.True(t, ok)
	assert.Nil(t, val)
}

func TestGetNullNestedValue(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node", map[string]any{"result": nil})

	val, ok := store.Get("node.result")
	assert.True(t, ok)
	assert.Nil(t, val)
}

func TestGetInputsTakePriorityOverNodeOutputs(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "from-inputs")
	store.SetNodeOutput("name", map[string]any{"value": "from-outputs"})

	// First segment "name" found in inputs, should return input value.
	val, ok := store.Get("name")
	assert.True(t, ok)
	assert.Equal(t, "from-inputs", val)
}

func TestGetFallsBackToNodeOutputs(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node", map[string]any{"value": 42})

	val, ok := store.Get("node.value")
	assert.True(t, ok)
	assert.Equal(t, 42, val)
}

func TestSetInput(t *testing.T) {
	store := NewStore()
	store.SetInput("key", "value1")

	val, ok := store.Get("key")
	assert.True(t, ok)
	assert.Equal(t, "value1", val)

	// Overwrite
	store.SetInput("key", "value2")
	val, ok = store.Get("key")
	assert.True(t, ok)
	assert.Equal(t, "value2", val)
}

func TestSetNodeOutput(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node1", map[string]any{"result": "ok"})

	val, ok := store.Get("node1.result")
	assert.True(t, ok)
	assert.Equal(t, "ok", val)
}

func TestDeleteExistingKey(t *testing.T) {
	store := NewStore()
	store.SetInput("key", "value")
	store.DeleteInput("key")

	val, ok := store.Get("key")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestDeleteMissingKey(t *testing.T) {
	store := NewStore()
	// Should not panic
	store.DeleteInput("nonexistent")
}

func TestSnapshot(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")
	store.SetInput("count", 42)
	store.SetNodeOutput("validate", map[string]any{"score": 85})

	snap := store.Snapshot()
	assert.Equal(t, "alice", snap["name"])
	assert.Equal(t, 42, snap["count"])
	assert.Equal(t, map[string]any{"score": 85}, snap["validate"])
}

func TestSnapshotNodeOutputsOverwriteInputs(t *testing.T) {
	store := NewStore()
	store.SetInput("key", "input-value")
	store.SetNodeOutput("key", map[string]any{"nested": true})

	snap := store.Snapshot()
	// nodeOutputs should overwrite inputs for same key in the merged view.
	assert.Equal(t, map[string]any{"nested": true}, snap["key"])
}

func TestSnapshotEmpty(t *testing.T) {
	store := NewStore()
	snap := store.Snapshot()
	assert.NotNil(t, snap)
	assert.Empty(t, snap)
}

func TestFromSnapshotRoundTrip(t *testing.T) {
	store := NewStore()
	store.SetInput("name", "alice")
	store.SetInput("count", 42)
	store.SetNodeOutput("validate", map[string]any{"score": 85})

	snap := store.Snapshot()
	restored := FromSnapshot(snap)

	// Simple values go to inputs
	val, ok := restored.Get("name")
	assert.True(t, ok)
	assert.Equal(t, "alice", val)

	val, ok = restored.Get("count")
	assert.True(t, ok)
	assert.Equal(t, 42, val)

	// Map values go to nodeOutputs
	val, ok = restored.Get("validate.score")
	assert.True(t, ok)
	assert.Equal(t, 85, val)
}

func TestFromSnapshotNil(t *testing.T) {
	restored := FromSnapshot(nil)
	require.NotNil(t, restored)
	snap := restored.Snapshot()
	assert.Empty(t, snap)
}

func TestFromSnapshotEmpty(t *testing.T) {
	restored := FromSnapshot(map[string]any{})
	require.NotNil(t, restored)
	snap := restored.Snapshot()
	assert.Empty(t, snap)
}

func TestFromSnapshotReconstructsCorrectly(t *testing.T) {
	data := map[string]any{
		"stringVal": "hello",
		"intVal":    99,
		"boolVal":   true,
		"nodeData":  map[string]any{"result": "ok", "code": 200},
	}

	store := FromSnapshot(data)

	// Non-map values → inputs
	val, ok := store.Get("stringVal")
	assert.True(t, ok)
	assert.Equal(t, "hello", val)

	val, ok = store.Get("intVal")
	assert.True(t, ok)
	assert.Equal(t, 99, val)

	val, ok = store.Get("boolVal")
	assert.True(t, ok)
	assert.Equal(t, true, val)

	// Map values → nodeOutputs, accessible via dot-path
	val, ok = store.Get("nodeData.result")
	assert.True(t, ok)
	assert.Equal(t, "ok", val)

	val, ok = store.Get("nodeData.code")
	assert.True(t, ok)
	assert.Equal(t, 200, val)
}

func TestFromSnapshotWithNodeIDs_ObjectInputNotMisclassified(t *testing.T) {
	// "config" is an object-valued workflow input, NOT a node output.
	// "validate" is a node ID whose output happens to be a map.
	data := map[string]any{
		"name":     "Alice",
		"config":   map[string]any{"retries": 3},
		"validate": map[string]any{"score": 85},
	}
	nodeIDs := map[string]bool{"validate": true}

	store := FromSnapshotWithNodeIDs(data, nodeIDs)

	// "name" is a plain input.
	val, ok := store.Get("name")
	require.True(t, ok)
	assert.Equal(t, "Alice", val)

	// "config" is an object input — must land in inputs so DeleteInput works.
	val, ok = store.Get("config")
	require.True(t, ok)
	assert.Equal(t, map[string]any{"retries": 3}, val)

	store.DeleteInput("config")
	_, ok = store.Get("config")
	assert.False(t, ok, "DeleteInput should remove object-valued input")

	// "validate" is a node output — accessible via dot-path.
	val, ok = store.Get("validate.score")
	require.True(t, ok)
	assert.Equal(t, 85, val)
}

func TestGetEmptyPath(t *testing.T) {
	store := NewStore()
	store.SetInput("", "should-not-matter")

	// An empty dot-path resolves the empty-string key — unusual but handled.
	val, ok := store.Get("")
	assert.True(t, ok)
	assert.Equal(t, "should-not-matter", val)
}

func TestGetThroughNilIntermediate(t *testing.T) {
	store := NewStore()
	store.SetNodeOutput("node", map[string]any{"result": nil})

	// Traversing deeper through a nil intermediate must not panic.
	val, ok := store.Get("node.result.deeper")
	assert.False(t, ok)
	assert.Nil(t, val)
}

func TestFromSnapshotWithNodeIDs_NilNodeIDsFallsBackToHeuristic(t *testing.T) {
	data := map[string]any{
		"scalar": "hello",
		"mapped": map[string]any{"key": "val"},
	}

	store := FromSnapshotWithNodeIDs(data, nil)

	// Same behaviour as FromSnapshot: map → nodeOutputs heuristic.
	val, ok := store.Get("scalar")
	require.True(t, ok)
	assert.Equal(t, "hello", val)

	val, ok = store.Get("mapped.key")
	require.True(t, ok)
	assert.Equal(t, "val", val)
}
