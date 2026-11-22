package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validIdentity() engine.StepIdentity {
	return engine.StepIdentity{
		RunID: uuid.New(), NodeID: "n1", InvocationID: uuid.New(),
		OperationID: "node", Kind: engine.OperationNode, Attempt: 1,
	}
}

func TestStepIdentity_Valid(t *testing.T) {
	assert.True(t, validIdentity().Valid())
	for name, mut := range map[string]func(*engine.StepIdentity){
		"nil run":        func(s *engine.StepIdentity) { s.RunID = uuid.Nil },
		"empty node":     func(s *engine.StepIdentity) { s.NodeID = "" },
		"nil invocation": func(s *engine.StepIdentity) { s.InvocationID = uuid.Nil },
		"empty op":       func(s *engine.StepIdentity) { s.OperationID = "" },
		"attempt zero":   func(s *engine.StepIdentity) { s.Attempt = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			id := validIdentity()
			mut(&id)
			assert.False(t, id.Valid())
		})
	}
}

func TestStepIdentity_ContextRoundTrip(t *testing.T) {
	_, ok := engine.StepIdentityFromContext(context.Background())
	assert.False(t, ok, "a bare context carries no identity")

	id := validIdentity()
	got, ok := engine.StepIdentityFromContext(engine.ContextWithStepIdentity(context.Background(), id))
	require.True(t, ok)
	assert.Equal(t, id, got)
}

func TestWithSubOperation_DerivesDistinctOperationUnderSameInvocation(t *testing.T) {
	parent := validIdentity()
	ctx := engine.ContextWithStepIdentity(context.Background(), parent)

	child, ok := engine.StepIdentityFromContext(engine.WithSubOperation(ctx, "child-input", engine.OperationChildInput))
	require.True(t, ok)
	assert.Equal(t, "node/child-input", child.OperationID)
	assert.Equal(t, engine.OperationChildInput, child.Kind)
	assert.Equal(t, parent.InvocationID, child.InvocationID)
	assert.Equal(t, parent.RunID, child.RunID)
	assert.Equal(t, parent.Attempt, child.Attempt)
	assert.NotEqual(t, parent.OperationID, child.OperationID, "a sub-operation never shares its parent's durable operation id")

	still, _ := engine.StepIdentityFromContext(ctx)
	assert.Equal(t, parent, still, "deriving a child does not mutate the parent context")
}

func TestWithSubOperation_WithoutParentStaysUnstamped(t *testing.T) {
	ctx := engine.WithSubOperation(context.Background(), "child-input", engine.OperationChildInput)
	_, ok := engine.StepIdentityFromContext(ctx)
	assert.False(t, ok, "no parent identity => still none, so the dispatcher fails closed")
}

func TestErrMissingStepIdentity_IsStable(t *testing.T) {
	assert.EqualError(t, engine.ErrMissingStepIdentity, "engine: dispatched operation has no step identity")
}
