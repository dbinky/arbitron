package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type refusedIntentStore struct{ engine.TurnIntentStore }

func (refusedIntentStore) Persist(context.Context, engine.TurnIntent) error {
	return errors.New("durable intent write failed")
}

func TestTurnIntent_PersistenceOrCommitErrorNeverAcknowledgesSuccess(t *testing.T) {
	instances, executions, llm, bus, queue, run := conversationTestSetup(t)
	h := buildConversationHandler(instances, executions, llm, bus, queue)
	h.(turnIntentSetter).SetTurnIntents(refusedIntentStore{})
	require.ErrorContains(t, h.HandleMessage(convoCtx(), run, "must not be accepted"), "durable intent write failed")
	assert.Empty(t, llm.getCapturedReqs())
	assert.Empty(t, bus.eventsOfType(run, engine.EventTypeInteractiveMessage))
}

func TestTurnIntent_AcceptsAndReplaysExactMessageAndBinding(t *testing.T) {
	instances, executions, llm, bus, queue, run := conversationTestSetup(t)
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "accepted.db"))
	h := buildConversationHandler(instances, executions, llm, bus, queue)
	h.(turnIntentSetter).SetTurnIntents(repo)
	llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		in, err := repo.PendingFor(ctx, engine.RunLeaseKey(run))
		require.NoError(t, err)
		require.NotNil(t, in)
		assert.NotEqual(t, uuid.Nil, in.RequestID)
		assert.NotEqual(t, uuid.Nil, in.InvocationID)
		assert.Equal(t, "exact accepted message ✓", in.Message)
		accepted, err := engine.CaptureAcceptedTurnBinding(provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: fakeResolvedProvider, Model: interactiveChatModel})
		require.NoError(t, err)
		assert.Equal(t, accepted, in.Binding)
		identity, ok := engine.TurnIdentityFromContext(ctx)
		require.True(t, ok)
		assert.Equal(t, engine.TurnIdentityFromIntent(*in), identity)
		changed := *in
		changed.Message = "different payload"
		require.Error(t, repo.Persist(ctx, changed), "request payload is immutable")
		return nil, engine.ErrSessionMoveRequired
	}
	require.ErrorIs(t, h.HandleMessage(convoCtx(), run, "exact accepted message ✓"), engine.ErrSessionMoveRequired)
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1, "a recoverable error preserves accepted intent")
}

func TestInteractiveExecutor_Intent_OlderVisitIntentIsNotReused(t *testing.T) {
	instances, executions, llm, files, bus, queue, run := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("CURRENT ORCH")
	files.files["test-wf/template.md"] = []byte("CURRENT TEMPLATE")
	repo := fileIntentRepo(t, filepath.Join(t.TempDir(), "visits.db"))
	old := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), InvocationID: uuid.New(), LeaseKey: engine.RunLeaseKey(run), RunID: run, NodeID: "interact", TurnIndex: 1, Kind: engine.TurnIntentFirst, Prompt: "OLD VISIT", Nonce: "old", Model: interactiveChatModel, SessionGeneration: 1, OperationID: "node"}
	require.NoError(t, repo.Persist(context.Background(), old))
	exec := buildInteractiveExecutor(instances, executions, llm, files, bus, queue)
	exec.SetTurnIntents(repo)
	llm.streamCallFn = cannedStream([]string{"hello"}, 1, 1)
	ctx := engine.ContextWithInstanceID(context.Background(), run)
	currentVisit := uuid.New()
	ctx = engine.ContextWithStepIdentity(ctx, engine.StepIdentity{RunID: run, NodeID: "interact", InvocationID: currentVisit, OperationID: "node"})
	_, err := exec.Execute(ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.ErrorIs(t, err, engine.ErrInteractiveTurnInProgress, "one unresolved operation per session: the new visit is refused, not stacked")
	for _, req := range llm.getCapturedReqs() {
		assert.NotContains(t, req.Prompt, "OLD VISIT", "an older visit's intent is never reused")
	}
	assert.Empty(t, llm.getCapturedReqs(), "nothing is dispatched while another visit's intent is unresolved")
	pending, err := repo.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, old.RequestID, pending[0].RequestID, "older visit remains for recovery")
	assert.NotEqual(t, currentVisit, pending[0].InvocationID)
}
