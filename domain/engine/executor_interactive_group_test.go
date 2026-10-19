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

// WHY: InteractiveExecutor short-circuits on "a succeeded record exists for
// this node" (executor_interactive.go:72-75). Group sessions and the group's
// wait trace are also succeeded records — but on the parallel_group node,
// which the engine never dispatches to InteractiveExecutor (it is registered
// only for interactive_llm / interactive_agent, cmd/arbitron/main.go:1001-1003).
// The guard below is defense in depth: a group-resolution record must never
// be mistaken for a completed interactive session, even under a node id reused
// across snapshot versions. Inside the group, an EARLIER merge's/attempt's
// session is excluded by the identity match against GatherReport.ActiveSession
// (conversation gate) and by the gatherer verifying the CURRENT merge's latest
// attempt (P3-T6).
func TestInteractiveExecutor_GroupResolutionRecordIsNotACompletedSession(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStream([]string{"fresh turn"}, 1, 1)
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output:          map[string]any{"stale": true},
		LLMConversation: map[string]any{"kind": "group_resolution", "groupRunId": uuid.NewString(), "mergeIndex": 0, "attempt": 1},
		StartedAt:       time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.True(t, result.IsWait, "a new conversation was started")
	assert.Len(t, llmProvider.getCapturedReqs(), 1)
	assert.Nil(t, result.Output)
}

func TestInteractiveExecutor_OrdinaryCompletedRecordStillShortCircuits(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"summary": "done"}, LLMConversation: map[string]any{"nonce": "n"}, StartedAt: time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"summary": "done"}, result.Output)
	assert.Empty(t, llmProvider.getCapturedReqs())
}
