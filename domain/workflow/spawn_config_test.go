package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSpawnConfig_HappyPath(t *testing.T) {
	got, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		ChildWorkflow: "child", SpawnMode: workflow.SpawnModeWaitForChild,
	})
	require.NoError(t, err)
	assert.Equal(t, "child", got.ChildWorkflow)
	assert.Equal(t, workflow.SpawnModeWaitForChild, got.Mode)
}

// The EMPTY mode is invalid, not an inheritance request: the two modes have
// opposite control-flow consequences, so an unset mode is an authoring error.
func TestParseSpawnConfig_EmptyModeIsInvalid(t *testing.T) {
	_, err := workflow.ParseSpawnConfig(workflow.NodeConfig{ChildWorkflow: "child"})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSpawnModeInvalid)
}

func TestParseSpawnConfig_UnrecognizedModeIsInvalid(t *testing.T) {
	_, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		ChildWorkflow: "child", SpawnMode: "WAIT_FOR_CHILD",
	})
	assert.ErrorIs(t, err, workflow.ErrSpawnModeInvalid, "the allowed set is exact-match lowercase")
}

func TestParseSpawnConfig_MissingChildWorkflow(t *testing.T) {
	_, err := workflow.ParseSpawnConfig(workflow.NodeConfig{SpawnMode: workflow.SpawnModeFireAndForget})
	assert.ErrorIs(t, err, workflow.ErrSpawnChildWorkflowMissing)
}

// EVERY problem is reported, not just the first: an author fixing a spawn node
// should see both in one pass.
func TestParseSpawnConfig_ReportsEveryProblem(t *testing.T) {
	_, err := workflow.ParseSpawnConfig(workflow.NodeConfig{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrSpawnChildWorkflowMissing))
	assert.True(t, errors.Is(err, workflow.ErrSpawnModeInvalid))
}

// A partial result is never returned alongside an error — a caller must not be
// able to spawn from half a contract.
func TestParseSpawnConfig_NoPartialResultOnError(t *testing.T) {
	got, err := workflow.ParseSpawnConfig(workflow.NodeConfig{ChildWorkflow: "child"})
	require.Error(t, err)
	assert.Equal(t, workflow.SpawnConfig{}, got)
}

func TestParseSpawnConfig_CarriesInputsAsDeepCopy(t *testing.T) {
	node := workflow.NodeConfig{
		ChildWorkflow: "child",
		SpawnMode:     workflow.SpawnModeFireAndForget,
		Inputs: map[string]any{
			"branch": "{{intake.branch}}",
			"paths":  []any{"a"},
			"opts":   map[string]any{"x": 1.0},
		},
	}
	cfg, err := workflow.ParseSpawnConfig(node)
	require.NoError(t, err)
	assert.Equal(t, node.Inputs, cfg.Inputs)

	cfg.Inputs["paths"].([]any)[0] = "mutated"
	cfg.Inputs["opts"].(map[string]any)["x"] = 99.0
	assert.Equal(t, "a", node.Inputs["paths"].([]any)[0], "mutating the parsed copy must not touch the node")
	assert.Equal(t, 1.0, node.Inputs["opts"].(map[string]any)["x"])

	node.Inputs["paths"].([]any)[0] = "again"
	node.Inputs["opts"].(map[string]any)["x"] = 7.0
	assert.Equal(t, "mutated", cfg.Inputs["paths"].([]any)[0], "mutating the node must not touch the parsed copy")
	assert.Equal(t, 99.0, cfg.Inputs["opts"].(map[string]any)["x"])
}

func TestParseSpawnConfig_NilInputsStayNil(t *testing.T) {
	cfg, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		ChildWorkflow: "child", SpawnMode: workflow.SpawnModeWaitForChild,
	})
	require.NoError(t, err)
	assert.Nil(t, cfg.Inputs)
}

func TestParseSpawnConfig_ReservedWorkspaceInput(t *testing.T) {
	cfg, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		ChildWorkflow: "child",
		SpawnMode:     workflow.SpawnModeFireAndForget,
		Inputs:        map[string]any{"workspace": "/x"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSpawnReservedInput)
	assert.Contains(t, err.Error(), "workspace")
	assert.Equal(t, workflow.SpawnConfig{}, cfg)
}

func TestParseSpawnConfig_ReservedInputJoinedWithOtherProblems(t *testing.T) {
	_, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		SpawnMode: "bogus",
		Inputs:    map[string]any{"workspace": "/x"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrSpawnChildWorkflowMissing))
	assert.True(t, errors.Is(err, workflow.ErrSpawnModeInvalid))
	assert.True(t, errors.Is(err, workflow.ErrSpawnReservedInput))
}

func TestParseSpawnConfig_EmptyInputsMapIsAccepted(t *testing.T) {
	cfg, err := workflow.ParseSpawnConfig(workflow.NodeConfig{
		ChildWorkflow: "child", SpawnMode: workflow.SpawnModeFireAndForget,
		Inputs: map[string]any{},
	})
	require.NoError(t, err)
	assert.Empty(t, cfg.Inputs)
}

func TestNodeConfig_InputsJSONRoundTrip(t *testing.T) {
	var cfg workflow.NodeConfig
	require.NoError(t, json.Unmarshal([]byte(
		`{"childWorkflow":"c","spawnMode":"fire_and_forget","inputs":{"n":3,"b":true}}`), &cfg))
	assert.Equal(t, 3.0, cfg.Inputs["n"])
	assert.Equal(t, true, cfg.Inputs["b"])

	out, err := json.Marshal(workflow.NodeConfig{})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "inputs")
}
