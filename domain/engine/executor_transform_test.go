package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func transformNode(ops []workflow.Operation) workflow.Node {
	return workflow.Node{
		ID:   "transform1",
		Type: workflow.NodeTypeTransform,
		Config: workflow.NodeConfig{
			Operations: ops,
		},
	}
}

func TestTransformExecutor_Set(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "greeting", Operation: "set", Value: "hello"},
	})

	exec := engine.NewTransformExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Nil(t, result.Output)

	val, ok := vars.Get("greeting")
	require.True(t, ok)
	assert.Equal(t, "hello", val)
}

func TestTransformExecutor_SetWithSubstitution(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("name", "Alice")

	node := transformNode([]workflow.Operation{
		{Variable: "greeting", Operation: "set", Value: "Hello, {{name}}!"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("greeting")
	require.True(t, ok)
	assert.Equal(t, "Hello, Alice!", val)
}

func TestTransformExecutor_Increment(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", float64(5))

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "increment", Value: float64(3)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("counter")
	require.True(t, ok)
	assert.Equal(t, float64(8), val)
}

func TestTransformExecutor_Decrement(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", float64(10))

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "decrement", Value: float64(4)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("counter")
	require.True(t, ok)
	assert.Equal(t, float64(6), val)
}

func TestTransformExecutor_IncrementNonNumericError(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", "not_a_number")

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "increment", Value: float64(1)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-numeric")
}

func TestTransformExecutor_DecrementNonNumericError(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", "text")

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "decrement", Value: float64(1)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-numeric")
}

func TestTransformExecutor_IncrementMissingError(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "increment", Value: float64(1)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTransformExecutor_DecrementMissingError(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "decrement", Value: float64(1)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestTransformExecutor_AppendToExisting(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("items", []any{"a", "b"})

	node := transformNode([]workflow.Operation{
		{Variable: "items", Operation: "append", Value: "c"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("items")
	require.True(t, ok)
	assert.Equal(t, []any{"a", "b", "c"}, val)
}

func TestTransformExecutor_AppendToMissingCreatesArray(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "items", Operation: "append", Value: "first"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("items")
	require.True(t, ok)
	assert.Equal(t, []any{"first"}, val)
}

func TestTransformExecutor_AppendToNonArrayError(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("items", "not_an_array")

	node := transformNode([]workflow.Operation{
		{Variable: "items", Operation: "append", Value: "x"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an array")
}

func TestTransformExecutor_Delete(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("temp", "value")

	node := transformNode([]workflow.Operation{
		{Variable: "temp", Operation: "delete"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	_, ok := vars.Get("temp")
	assert.False(t, ok)
}

func TestTransformExecutor_DeleteMissingNoError(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "nonexistent", Operation: "delete"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
}

func TestTransformExecutor_UnknownOperation(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "x", Operation: "bogus", Value: "y"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation")
}

func TestTransformExecutor_IncrementNonNumericDelta(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", float64(5))

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "increment", Value: "not_a_number"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-numeric")
}

func TestTransformExecutor_MultipleOpsSequential(t *testing.T) {
	vars := variable.NewStore()

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "set", Value: float64(10)},
		{Variable: "counter", Operation: "increment", Value: float64(5)},
		{Variable: "counter", Operation: "decrement", Value: float64(3)},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)

	val, ok := vars.Get("counter")
	require.True(t, ok)
	assert.Equal(t, float64(12), val)
}


func TestTransformExecutor_EmptyVariableNameReturnsError(t *testing.T) {
	vars := variable.NewStore()
	node := transformNode([]workflow.Operation{
		{Variable: "", Operation: "set", Value: "value"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "variable name is required")
}

// Edge case: decrement with a non-numeric delta returns an error, mirroring
// the equivalent increment test (same code path via applyIncrement).
func TestTransformExecutor_DecrementNonNumericDelta(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("counter", float64(10))

	node := transformNode([]workflow.Operation{
		{Variable: "counter", Operation: "decrement", Value: "not_a_number"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-numeric")
}

func TestTransformExecutor_RejectsReservedWorkspaceVariable(t *testing.T) {
	// SF12 / PR #8 thread 3291944671 fix #3: a transform that tries to
	// rewrite the engine-seeded "workspace" variable (which becomes the
	// agent's WorkDir) fails the node instead of silently rerouting the
	// agent out of its sandbox.
	vars := variable.NewStore()
	vars.SetReserved("workspace", "/seeded/path")

	node := transformNode([]workflow.Operation{
		{Variable: "workspace", Operation: "set", Value: "/etc"},
	})

	exec := engine.NewTransformExecutor()
	_, err := exec.Execute(context.Background(), node, vars, "test-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, variable.ErrReservedInputName)

	// Original value must be preserved.
	val, ok := vars.Get("workspace")
	require.True(t, ok)
	assert.Equal(t, "/seeded/path", val)
}
