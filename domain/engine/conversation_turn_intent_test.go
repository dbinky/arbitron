package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type turnIntentSetter interface{ SetTurnIntents(engine.TurnIntentStore) }

func fileIntentRepo(t *testing.T, path string) *sqlite.TurnIntentRepo {
	t.Helper()
	db, err := sqlite.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	return sqlite.NewTurnIntentRepo(db)
}

func TestConversationHandler_Intent_PersistedBeforeDispatchAndSurvivesRestart(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	path := filepath.Join(t.TempDir(), "arbitron.db")
	repo := fileIntentRepo(t, path)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	h.(turnIntentSetter).SetTurnIntents(repo)
	h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute) // turnLeaseSetter: P4 T2 test file

	const exact = "Please refactor `pkg/x` — keep the API ✓"
	var seenAtDispatch *engine.TurnIntent
	ctx, crash := context.WithCancel(convoCtx())
	llm.streamCallFn = func(c context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		in, err := repo.PendingFor(context.Background(), engine.RunLeaseKey(instanceID))
		require.NoError(t, err)
		seenAtDispatch = in
		ch := make(chan engine.ChatChunk, 1)
		ch <- engine.ChatChunk{Token: "partial "}
		go func() { crash(); <-c.Done(); close(ch) }() // the server dies mid-turn
		return ch, nil
	}
	err := h.HandleMessage(ctx, instanceID, exact)
	require.Error(t, err)
	require.NotNil(t, seenAtDispatch, "the intent is durable BEFORE dispatch")
	assert.Equal(t, exact, seenAtDispatch.Message)

	// Restart: a new process opens the same database.
	restarted := fileIntentRepo(t, path)
	pending, err := restarted.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	in := pending[0]
	assert.Equal(t, exact, in.Message, "the exact message survives the restart")
	assert.Equal(t, engine.TurnIntentMessage, in.Kind)
	assert.Equal(t, engine.RunLeaseKey(instanceID), in.LeaseKey)
	assert.Equal(t, instanceID, in.RunID)
	assert.Equal(t, "interact", in.NodeID)
	assert.Equal(t, 2, in.TurnIndex)
	assert.Equal(t, "turn/2", in.OperationID)
	assert.Equal(t, interactiveChatModel, in.Model)
	assert.EqualValues(t, 1, in.SessionGeneration)
	exec, err := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "interact")
	require.NoError(t, err)
	assert.Len(t, extractMessages(t, exec.LLMConversation), 2, "the interrupted turn was not persisted")
}

func TestConversationHandler_Intent_CompletedOnSuccessAndOnDefinitiveFailure(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "a.db"))
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	h.(turnIntentSetter).SetTurnIntents(repo)
	h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute) // turnLeaseSetter: P4 T2 test file

	llm.streamCallFn = cannedStream([]string{"fine"}, 1, 1)
	require.NoError(t, h.HandleMessage(convoCtx(), instanceID, "ok turn"))
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "a successful turn completes its intent")

	llm.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		return nil, errors.New("provider refused")
	}
	require.Error(t, h.HandleMessage(convoCtx(), instanceID, "refused turn"))
	pending, err = repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "a definitive failure the user saw is never auto-resent")
}

func TestConversationHandler_Intent_EndSessionPersistsAndCompletes(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, instanceID := conversationTestSetup(t)
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "b.db"))
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	h.(turnIntentSetter).SetTurnIntents(repo)
	h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute) // turnLeaseSetter: P4 T2 test file
	var sawEnd bool
	queue.onNotify = func() { // capacity is freed after the end transition, before EndSession returns
		in, err := repo.PendingFor(context.Background(), engine.RunLeaseKey(instanceID))
		require.NoError(t, err)
		sawEnd = in != nil && in.Kind == engine.TurnIntentEnd
	}
	_, err := h.EndSession(convoCtx(), instanceID)
	require.NoError(t, err)
	assert.True(t, sawEnd, "the end intent is durable while the end operation runs")
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestConversationHandler_Intent_ResolveFailurePersistsNothing(t *testing.T) {
	instRepo, nodeExecRepo, _, bus, queue, instanceID := conversationTestSetup(t)
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "c.db"))
	h := engine.NewConversationHandler(instRepo, nodeExecRepo, streamSelector(newMockStreamingChatProvider()), newFakeResolver(), bus, queue)
	h.(turnIntentSetter).SetTurnIntents(repo)
	h.(turnLeaseSetter).SetTurnLeases(engine.NewMemoryTurnLeaseStore(), time.Minute) // turnLeaseSetter: P4 T2 test file
	require.Error(t, h.HandleMessage(convoCtx(), instanceID, "unknown profile"))
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "nothing accepted, nothing to resend")
	assert.Empty(t, bus.eventsOfType(instanceID, engine.EventTypeInteractiveMessage), "no acknowledgement for a refused submission")
}

func TestConversationHandler_SetTurnIntentsRejectsNil(t *testing.T) {
	instRepo, nodeExecRepo, llm, bus, queue, _ := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llm, bus, queue)
	assert.Panics(t, func() { h.(turnIntentSetter).SetTurnIntents(nil) })
}

func TestTurnIntent_Turn1NeverMatchesTurn10(t *testing.T) {
	assert.True(t, engine.TurnOperationMatches("turn/1", "turn/1"))
	assert.True(t, engine.TurnOperationMatches("turn/1/stream", "turn/1"))
	assert.False(t, engine.TurnOperationMatches("turn/10", "turn/1"))
	assert.False(t, engine.TurnOperationMatches("turn/10/stream", "turn/1"))
	assert.False(t, engine.TurnOperationMatches("turn/1", ""))

	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "pending.db"))
	run := uuid.New()
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), RunID: run,
		InvocationID: uuid.New(), LeaseKey: engine.RunLeaseKey(run), NodeID: "chat",
		TurnIndex: 10, OperationID: "turn/10", Kind: engine.TurnIntentMessage,
		Message: "pending tenth turn", SessionGeneration: 1,
		Binding: engine.AcceptedTurnBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: "claude-sonnet-4-5"}}
	require.NoError(t, repo.Persist(context.Background(), in))
	pending, err := repo.PendingFor(context.Background(), in.LeaseKey)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, in.RequestID, pending.RequestID)
	assert.False(t, engine.TurnOperationMatches(pending.OperationID, "turn/1"))
}
