package workflow_test

import (
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
