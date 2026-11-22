package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildInputPreparer_StampsDistinctChildInputOperation(t *testing.T) {
	var got engine.StepIdentity
	var stamped bool
	prov := &mockChatProvider{callFn: func(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
		got, stamped = engine.StepIdentityFromContext(ctx)
		return &engine.ChatResponse{Output: `{"repo":"r","branch":"b"}`, Model: req.Model}, nil
	}}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))
	parent := engine.StepIdentity{RunID: uuid.New(), NodeID: "spawn-1", InvocationID: uuid.New(), OperationID: "node", Kind: engine.OperationNode, Attempt: 2}
	ctx := engine.ContextWithStepIdentity(context.Background(), parent)

	_, err := p.Prepare(ctx, engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "child", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	require.True(t, stamped)
	assert.Equal(t, "node/child-input", got.OperationID)
	assert.Equal(t, engine.OperationChildInput, got.Kind)
	assert.Equal(t, parent.InvocationID, got.InvocationID)
	assert.Equal(t, parent.Attempt, got.Attempt)
	assert.NotEqual(t, parent.OperationID, got.OperationID, "child preparation never shares the node's durable operation id")
}
