package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type invocationSettable interface {
	SetInvocationStore(engine.InvocationStore)
	InvocationStore() engine.InvocationStore
}

func TestHandleMessage_StampsTurnIdentityUnderOpenNodeInvocation(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, qm, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, qm)
	store := engine.NewMemoryInvocationStore()
	settable, ok := h.(invocationSettable)
	require.True(t, ok, "the conversation handler must accept the durable invocation store")
	settable.SetInvocationStore(store)
	assert.Same(t, store, settable.InvocationStore())

	// The interactive node's visit was opened by executeWithRetry and left open by IsWait.
	open, err := store.Begin(context.Background(), id, "interact")
	require.NoError(t, err)

	var mu sync.Mutex
	var got []engine.StepIdentity
	llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		sid, ok := engine.StepIdentityFromContext(ctx)
		assert.True(t, ok, "the routed stream call must be stamped")
		mu.Lock()
		got = append(got, sid)
		mu.Unlock()
		return cannedStream([]string{"noted"}, 1, 1)(ctx, req)
	}

	require.NoError(t, h.HandleMessage(convoCtx(), id, "first follow-up"))
	require.NoError(t, h.HandleMessage(convoCtx(), id, "second follow-up"))

	require.Len(t, got, 2)
	for _, sid := range got {
		assert.True(t, sid.Valid())
		assert.Equal(t, id, sid.RunID)
		assert.Equal(t, "interact", sid.NodeID)
		assert.Equal(t, open.ID, sid.InvocationID, "turns live under the node's open visit")
		assert.Equal(t, engine.OperationTurn, sid.Kind)
		assert.Equal(t, 1, sid.Attempt)
	}
	// History already holds one user message (the orchestration prompt).
	assert.Equal(t, "turn/2", got[0].OperationID)
	assert.Equal(t, "turn/3", got[1].OperationID, "each turn is a distinct durable operation")
}

func TestHandleMessage_InvocationFailureRefusesTurnBeforeProvider(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, qm, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, qm)
	h.(invocationSettable).SetInvocationStore(failingInvocationStore{InvocationStore: engine.NewMemoryInvocationStore(), beginErr: errors.New("db locked")})
	llm.streamCallFn = cannedStream([]string{"x"}, 1, 1)

	err := h.HandleMessage(convoCtx(), id, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "begin invocation")
	assert.Empty(t, llm.getCapturedReqs(), "no provider call without a durable identity")
}

func TestHandleMessage_DefaultStoreStillStamps(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, qm, id := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, qm)
	var sid engine.StepIdentity
	llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		sid, _ = engine.StepIdentityFromContext(ctx)
		return cannedStream([]string{"x"}, 1, 1)(ctx, req)
	}
	require.NoError(t, h.HandleMessage(convoCtx(), id, "hello"))
	assert.True(t, sid.Valid())
	assert.NotEqual(t, uuid.Nil, sid.InvocationID)
}
