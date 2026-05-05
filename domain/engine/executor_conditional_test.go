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

func conditionalNode(conditions []workflow.Condition, defaultOutput string) workflow.Node {
	return workflow.Node{
		ID:   "cond1",
		Type: workflow.NodeTypeConditional,
		Config: workflow.NodeConfig{
			Conditions:    conditions,
			DefaultOutput: defaultOutput,
		},
	}
}

func TestConditionalExecutor_EqMatch(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("status", "active")

	node := conditionalNode([]workflow.Condition{
		{Field: "status", Operator: "eq", Value: "active", Output: "is_active"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "is_active", result.OutputPort)
	assert.Nil(t, result.Output)
}

func TestConditionalExecutor_EqNoMatch(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("status", "inactive")

	node := conditionalNode([]workflow.Condition{
		{Field: "status", Operator: "eq", Value: "active", Output: "is_active"},
	}, "fallback")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "fallback", result.OutputPort)
}

func TestConditionalExecutor_Neq(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("status", "inactive")

	node := conditionalNode([]workflow.Condition{
		{Field: "status", Operator: "neq", Value: "active", Output: "not_active"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "not_active", result.OutputPort)
}

func TestConditionalExecutor_GtNumeric(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(85))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "gt", Value: float64(80), Output: "high"},
	}, "low")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "high", result.OutputPort)
}

func TestConditionalExecutor_GteNumeric(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(80))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "gte", Value: float64(80), Output: "pass"},
	}, "fail")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "pass", result.OutputPort)
}

func TestConditionalExecutor_LtNumeric(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(50))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "lt", Value: float64(60), Output: "low"},
	}, "ok")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "low", result.OutputPort)
}

func TestConditionalExecutor_LteNumeric(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(60))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "lte", Value: float64(60), Output: "low_or_equal"},
	}, "high")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "low_or_equal", result.OutputPort)
}

func TestConditionalExecutor_GtNonNumericSkips(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("name", "alice")

	node := conditionalNode([]workflow.Condition{
		{Field: "name", Operator: "gt", Value: float64(10), Output: "shouldnt_match"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "default", result.OutputPort)
}

func TestConditionalExecutor_ContainsString(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("message", "hello world")

	node := conditionalNode([]workflow.Condition{
		{Field: "message", Operator: "contains", Value: "world", Output: "found"},
	}, "not_found")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "found", result.OutputPort)
}

func TestConditionalExecutor_ContainsArray(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("tags", []any{"go", "rust", "python"})

	node := conditionalNode([]workflow.Condition{
		{Field: "tags", Operator: "contains", Value: "rust", Output: "has_rust"},
	}, "no_rust")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "has_rust", result.OutputPort)
}

func TestConditionalExecutor_ExistsTrue(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("email", "user@example.com")

	node := conditionalNode([]workflow.Condition{
		{Field: "email", Operator: "exists", Value: nil, Output: "has_email"},
	}, "no_email")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "has_email", result.OutputPort)
}

func TestConditionalExecutor_ExistsFalse(t *testing.T) {
	vars := variable.NewStore()

	node := conditionalNode([]workflow.Condition{
		{Field: "email", Operator: "exists", Value: nil, Output: "has_email"},
	}, "no_email")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "no_email", result.OutputPort)
}

func TestConditionalExecutor_ExistsNull(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("email", nil)

	node := conditionalNode([]workflow.Condition{
		{Field: "email", Operator: "exists", Value: nil, Output: "has_email"},
	}, "no_email")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "no_email", result.OutputPort)
}

func TestConditionalExecutor_FirstMatchWins(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(90))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "gt", Value: float64(80), Output: "first"},
		{Field: "score", Operator: "gt", Value: float64(70), Output: "second"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "first", result.OutputPort)
}

func TestConditionalExecutor_DefaultOutput(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("score", float64(50))

	node := conditionalNode([]workflow.Condition{
		{Field: "score", Operator: "gt", Value: float64(80), Output: "high"},
		{Field: "score", Operator: "gt", Value: float64(70), Output: "medium"},
	}, "low")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "low", result.OutputPort)
}

func TestConditionalExecutor_EmptyConditions(t *testing.T) {
	vars := variable.NewStore()

	node := conditionalNode(nil, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "default", result.OutputPort)
}

func TestConditionalExecutor_NeqMissingField(t *testing.T) {
	// When the field doesn't exist, neq should be true (absence is not equal).
	vars := variable.NewStore()

	node := conditionalNode([]workflow.Condition{
		{Field: "email", Operator: "neq", Value: "x@x.com", Output: "different"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "different", result.OutputPort)
}

func TestConditionalExecutor_ContainsStringNotFound(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("message", "hello world")

	node := conditionalNode([]workflow.Condition{
		{Field: "message", Operator: "contains", Value: "xyz", Output: "found"},
	}, "not_found")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "not_found", result.OutputPort)
}

func TestConditionalExecutor_ContainsNonStringNonArray(t *testing.T) {
	// contains on a numeric field → false, routes to default.
	vars := variable.NewStore()
	vars.SetInput("count", float64(42))

	node := conditionalNode([]workflow.Condition{
		{Field: "count", Operator: "contains", Value: "42", Output: "found"},
	}, "not_found")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "not_found", result.OutputPort)
}

func TestConditionalExecutor_UnknownOperator(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("x", "y")

	node := conditionalNode([]workflow.Condition{
		{Field: "x", Operator: "bogus", Value: "y", Output: "matched"},
	}, "default")

	exec := engine.NewConditionalExecutor()
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")

	require.NoError(t, err)
	assert.Equal(t, "default", result.OutputPort)
}
