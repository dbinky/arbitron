package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An interactive session whose opening turn is done but which the human has
// not finished has a succeeded record with NO output (output is written only
// when the session ends). Re-entering the node in that state — a run paused
// while it waited for input and then resumed through the run loop — must put
// the run back to waiting for the human. Treating the record as a finished
// session would skip the conversation and advance with empty output.
func TestInteractiveExecutor_InProgressSessionReentry_ReturnsToWaiting(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"nonce": "n"}, StartedAt: time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	assert.True(t, result.IsWait, "the session is still open: the run must wait for the human")
	assert.Empty(t, llmProvider.getCapturedReqs(), "the opening turn must not be replayed")
	got, err := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, got.State)
}

// A run that is no longer running at re-entry (paused or cancelled meanwhile)
// is not moved to waiting: that would revive it.
func TestInteractiveExecutor_InProgressSessionReentry_NonRunningRunNotRevived(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	require.NoError(t, instRepo.UpdateState(context.Background(), instanceID, instance.StateCancelled))
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		LLMConversation: map[string]any{"nonce": "n"}, StartedAt: time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")

	require.NoError(t, err)
	assert.True(t, result.IsWait)
	got, err := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateCancelled, got.State)
}
