package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentInteractiveNode() workflow.Node {
	n := interactiveNode()
	n.Type = workflow.NodeTypeInteractiveAgent
	return n
}
func wiredExecutor(t *testing.T, exec *engine.InteractiveExecutor, f ckFixture) *memIntents {
	t.Helper()
	intents := &memIntents{}
	f.committer.finish = intents.committedByTransaction
	exec.SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute)
	exec.SetTurnIntents(intents)
	exec.SetSessionCheckpoints(f.deps())
	return intents
}
func TestInteractiveExecutor_FirstTurnCheckpointCreatesRecordThroughCommit(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, id := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("o")
	files.files["test-wf/template.md"] = []byte("t")
	llm.streamCallFn = receiptStream("builtin", true, "thread-1", "hi")
	exec := engine.NewInteractiveExecutor(instRepo, nodeExecRepo, streamSelector(llm), bindingResolver{b: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: interactiveChatModel}}, files, bus, queue)
	f := newCkFixture()
	intents := wiredExecutor(t, exec, f)
	vars := variable.NewStore()
	vars.SetReserved(engine.ReservedVarWorkspace, "/srv/ws/run-1")
	ctx := engine.ContextWithStepIdentity(engine.ContextWithInstanceID(context.Background(), id), engine.StepIdentity{RunID: id, NodeID: "interact", InvocationID: uuid.New(), OperationID: "interact", Kind: engine.OperationNode, Attempt: 1})
	outer := engine.NewStepCompletionSink()
	ctx = engine.ContextWithStepCompletionSink(ctx, outer)
	_, err := exec.Execute(ctx, agentInteractiveNode(), vars, "test-wf")
	require.NoError(t, err)
	assert.Equal(t, []string{"export:thread-1", "put", "stage-workspace", "commit", "prune"}, f.log.all())
	assert.Equal(t, "claude-code", f.exporter.reqs[0].Backend)
	assert.Equal(t, 1, f.store.keys[0].TurnIndex)
	require.Len(t, f.committer.commits, 1)
	c := f.committer.commits[0]
	assert.True(t, c.Create)
	assert.Empty(t, outer.Completions(), "CommitTurn owns the stream receipt; the engine must not commit it again as a suspension")
	assert.Equal(t, intents.persisted[0].ID, c.IntentID)
	_, direct := nodeExecRepo.GetByInstanceAndNode(context.Background(), id, "interact")
	assert.Error(t, direct, "only CommitTurn writes the record")
	inst, err := instRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateWaiting, inst.State)
}
func TestInteractiveExecutor_FirstTurnExportFailureFailsStep(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, id := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("o")
	files.files["test-wf/template.md"] = []byte("t")
	llm.streamCallFn = receiptStream("builtin", false, "sess-1", "hi")
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	f := newCkFixture()
	f.exporter.err = errors.New("transcript not found")
	wiredExecutor(t, exec, f)
	ctx := engine.ContextWithStepIdentity(engine.ContextWithInstanceID(context.Background(), id), engine.StepIdentity{RunID: id, NodeID: "interact", InvocationID: uuid.New(), OperationID: "interact", Kind: engine.OperationNode, Attempt: 1})
	_, err := exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checkpoint first turn")
	assert.Empty(t, f.committer.commits)
	inst, gerr := instRepo.Get(context.Background(), id)
	require.NoError(t, gerr)
	assert.Equal(t, instance.StateRunning, inst.State)
	assert.Zero(t, assistantEvents(bus, id))
}
func TestInteractiveExecutor_SetSessionCheckpointsRejectsIncompleteDeps(t *testing.T) {
	instRepo, nodeExecRepo, llm, files, bus, queue, _ := interactiveTestSetup()
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queue)
	assert.Panics(t, func() { exec.SetSessionCheckpoints(engine.SessionCheckpointDeps{}) })
}
