package engine_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartResolution_StampsResolutionIdentityUnderGroupNodeVisit(t *testing.T) {
	f := newBridgeFixture(t)
	store := engine.NewMemoryInvocationStore()
	f.bridge.SetInvocationStore(store)
	assert.Same(t, store, f.bridge.InvocationStore())
	open, err := store.Begin(context.Background(), f.parentID, "g1")
	require.NoError(t, err)

	var got engine.StepIdentity
	var stamped bool
	f.agent.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		got, stamped = engine.StepIdentityFromContext(ctx)
		return cannedStreamWithSession([]string{"Resolved."}, "agent-sess-1")(ctx, req)
	}

	_, err = f.start(1)
	require.NoError(t, err)
	require.True(t, stamped, "the resolution stream call must be stamped")
	assert.True(t, got.Valid())
	assert.Equal(t, f.parentID, got.RunID)
	assert.Equal(t, "g1", got.NodeID)
	assert.Equal(t, open.ID, got.InvocationID)
	assert.Equal(t, engine.OperationResolution, got.Kind)
	assert.Equal(t, fmt.Sprintf("resolution/%s:1:1", f.runID), got.OperationID)
	assert.Equal(t, 1, got.Attempt)
}

func TestStartResolution_InvocationFailureRefusesBeforeStream(t *testing.T) {
	f := newBridgeFixture(t)
	f.bridge.SetInvocationStore(failingInvocationStore{InvocationStore: engine.NewMemoryInvocationStore(), beginErr: fmt.Errorf("db locked")})
	f.agent.streamCallFn = cannedStreamWithSession([]string{"x"}, "s")
	_, err := f.start(1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin invocation")
	assert.Empty(t, f.agent.getCapturedReqs())
}
