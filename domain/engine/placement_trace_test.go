package engine_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchFact simulates what runner.StepDispatcher writes into the attempt sink.
func dispatchFact(t *testing.T, ctx context.Context, source, reason, outcome string, gen int64) {
	t.Helper()
	sink, ok := engine.PlacementSinkFromContext(ctx)
	require.True(t, ok, "executeWithRetry must install a sink per attempt")
	sink.Record(engine.PlacementFact{JobID: uuid.New(), RunnerID: "builtin", SourceID: source, Reason: reason, Generation: gen, Outcome: outcome, At: time.Now()})
}

func placementExecs(f *testFixtures, id uuid.UUID, node string) []*instance.NodeExecution {
	f.nodeExecRepo.mu.Lock()
	defer f.nodeExecRepo.mu.Unlock()
	var out []*instance.NodeExecution
	for _, e := range f.nodeExecRepo.executions {
		if e.InstanceID == id && e.NodeID == node {
			out = append(out, e)
		}
	}
	return out
}

// Every attempt — the failed one included — records its own facts; the last
// fact of the attempt sets RunnerID/SourceID/PlacementReason.
func TestEngine_PlacementCopiedOnEveryAttempt(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(errorWorkflow(workflow.ErrorBehavior{Type: "retry", Retries: 1}))
	var calls atomic.Int32
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		if calls.Add(1) == 1 {
			dispatchFact(t, ctx, "builtin/anthropic_subscription/-", "preferred", "exhausted", 1)
			dispatchFact(t, ctx, "builtin/lunaroute/lr", "rerouted", "other", 2)
			return nil, fmt.Errorf("step failed")
		}
		dispatchFact(t, ctx, "builtin/lunaroute/lr", "first fit", "completed", 1)
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	b := placementExecs(f, id, "B")
	require.Len(t, b, 2)
	first, second := b[0], b[1]
	assert.Equal(t, 1, first.Attempt)
	assert.Equal(t, instance.NodeStatusFailed, first.Status)
	require.NotNil(t, first.RunnerID)
	assert.Equal(t, "builtin", *first.RunnerID)
	assert.Equal(t, "builtin/lunaroute/lr", *first.SourceID)
	assert.Equal(t, "rerouted", *first.PlacementReason)
	require.Len(t, first.PlacementHistory, 2)
	assert.Equal(t, "exhausted", first.PlacementHistory[0].Outcome)
	assert.Equal(t, "builtin/anthropic_subscription/-", first.PlacementHistory[0].SourceID)

	assert.Equal(t, 2, second.Attempt)
	require.Len(t, second.PlacementHistory, 1, "a fresh sink per attempt")
	assert.Equal(t, "completed", second.PlacementHistory[0].Outcome)
	assert.Equal(t, "first fit", *second.PlacementReason)

	a := placementExecs(f, id, "A")
	require.Len(t, a, 1)
	assert.Nil(t, a[0].RunnerID, "a node that never dispatched has no placement")
	assert.Nil(t, a[0].PlacementHistory)
}

// Edge: a fact without a source (process slot on CredNone) leaves SourceID nil.
func TestEngine_PlacementWithoutSource(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		dispatchFact(t, ctx, "", "first fit", "completed", 1)
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})
	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	a := placementExecs(f, id, "A")
	require.Len(t, a, 1)
	require.NotNil(t, a[0].RunnerID)
	assert.Nil(t, a[0].SourceID)
}

// The interactive executor writes its own first-turn record; it must copy the
// sink facts of the attempt context it runs in.
func TestInteractiveExecutor_FirstTurnRecordCarriesPlacement(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	fileReader.files["test-wf/orchestration.md"] = []byte("o")
	fileReader.files["test-wf/template.md"] = []byte("t")
	llmProvider.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		dispatchFact(t, ctx, "builtin/anthropic_subscription/-", "first fit", "completed", 1)
		return cannedStream([]string{"hi"}, 1, 1)(ctx, req)
	}
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)
	sink := engine.NewPlacementSink()
	ctx := engine.ContextWithPlacementSink(engine.ContextWithInstanceID(context.Background(), instanceID), sink)
	_, err := executor.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)

	nodeExecRepo.mu.Lock()
	defer nodeExecRepo.mu.Unlock()
	require.Len(t, nodeExecRepo.executions, 1)
	rec := nodeExecRepo.executions[0]
	require.NotNil(t, rec.RunnerID)
	assert.Equal(t, "builtin", *rec.RunnerID)
	assert.Equal(t, "builtin/anthropic_subscription/-", *rec.SourceID)
	assert.Len(t, rec.PlacementHistory, 1)
}
