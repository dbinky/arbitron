package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type receiptMove struct {
	*rrMover
	importReceipt engine.StepCompletion
}

func (m receiptMove) MoveSession(ctx context.Context, lease engine.TurnLease, req engine.SessionMoveRequest) (engine.SessionMoveResult, error) {
	// Import work is a separate operation, never the successful turn receipt.
	if sink, ok := engine.StepCompletionSinkFromContext(ctx); ok {
		if err := sink.Record(m.importReceipt); err != nil {
			return engine.SessionMoveResult{}, err
		}
	}
	return m.rrMover.MoveSession(ctx, lease, req)
}

func TestTurnRecovery_ResendsExactMessageUnderNewGeneration(t *testing.T) {
	r := newRRHarness(t, nil, true)
	f := newCkFixture()
	f.committer.apply = func(exec *instance.NodeExecution) error { return r.execs.Update(context.Background(), exec) }
	f.committer.finish = func(id uuid.UUID) { require.NoError(t, r.intents.Complete(context.Background(), id)) }
	r.h.(sessionCheckpointSetter).SetSessionCheckpoints(f.deps())
	r.h.(interface {
		SetSessionMover(engine.SessionMover, int)
	}).SetSessionMover(receiptMove{rrMover: r.mover,
		importReceipt: engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: "mac-b", Generation: 1}}}, 2)
	var leases []engine.TurnLease
	var identities []engine.StepIdentity
	var attempts []*engine.StepCompletionSink
	failed := engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: "mac-a", Generation: 1}}
	r.llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		lease, ok := engine.TurnLeaseFromContext(ctx)
		require.True(t, ok)
		leases = append(leases, lease)
		identity, ok := engine.StepIdentityFromContext(ctx)
		require.True(t, ok)
		identities = append(identities, identity)
		sink, ok := engine.StepCompletionSinkFromContext(ctx)
		require.True(t, ok)
		attempts = append(attempts, sink)
		assert.Empty(t, sink.Completions(), "a fresh sink on every attempt")
		if len(leases) == 1 {
			require.NoError(t, sink.Record(failed))
			return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonExhausted}
		}
		assert.Equal(t, "exact accepted message", req.Prompt)
		assert.Equal(t, "sid-b", req.SessionID)
		return receiptStream("mac-b", false, "sid-b", "committed reply")(ctx, req)
	}
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "exact accepted message"))
	require.Len(t, leases, 2)
	assert.Equal(t, leases[0].Token, leases[1].Token)
	assert.Equal(t, int64(2), leases[1].Generation)
	assert.NotSame(t, attempts[0], attempts[1])
	assert.NotEqual(t, identities[0].OperationID, identities[1].OperationID)
	require.Len(t, f.committer.commits, 1)
	committed := f.committer.commits[0]
	assert.Equal(t, leases[1].Generation, committed.SessionGeneration)
	require.Len(t, committed.Completions, 1, "neither failed nor import receipts may be committed")
	assert.Equal(t, "mac-b", committed.Completions[0].JobFence.RunnerID)
	assert.NotEqual(t, failed.JobFence.JobID, committed.Completions[0].JobFence.JobID)
	assert.Equal(t, committed.Completions[0].JobFence, f.exporter.reqs[0].JobFence)
	assert.Equal(t, 1, assistantEvents(r.bus, r.id))
	for _, event := range r.bus.eventsOfType(r.id, engine.EventTypeInteractiveMessage) {
		data := event.Data.(map[string]any)
		assert.Equal(t, "interact", data["nodeId"])
		assert.Equal(t, committed.InvocationID.String(), data["invocationId"])
		assert.Equal(t, committed.RequestID.String(), data["requestId"])
		assert.Equal(t, 2, data["turnIndex"])
		if data["role"] == "assistant" {
			assert.Equal(t, int64(2), data["generation"])
		}
	}
}

type skippedGenerationMove struct{ *rrMover }

func (m skippedGenerationMove) MoveSession(ctx context.Context, lease engine.TurnLease, req engine.SessionMoveRequest) (engine.SessionMoveResult, error) {
	res, err := m.rrMover.MoveSession(ctx, lease, req)
	if err != nil {
		return res, err
	}
	// Reserve another generation as a previously abandoned attempt would.
	lease.Generation = res.Generation
	reserved, err := m.store.BumpGeneration(ctx, lease)
	res.Generation = reserved.Generation
	return res, err
}

func TestTurnRecovery_AdoptRefreshesLeaseInAttemptContext(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.h.(interface {
		SetSessionMover(engine.SessionMover, int)
	}).SetSessionMover(skippedGenerationMove{r.mover}, 2)
	var leases []engine.TurnLease
	r.llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		lease, ok := engine.TurnLeaseFromContext(ctx)
		require.True(t, ok)
		leases = append(leases, lease)
		if len(leases) == 1 {
			return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonRunnerLost}
		}
		assert.Equal(t, int64(3), lease.Generation, "use the reservation, not old+1")
		return cannedStreamWithSession([]string{"reply"}, "sid-b")(ctx, req)
	}
	require.NoError(t, r.h.HandleMessage(convoCtx(), r.id, "same message"))
	require.Len(t, leases, 2)
	assert.Equal(t, leases[0].Token, leases[1].Token)
	for _, kind := range []string{engine.EventTypeInteractiveToken, engine.EventTypeInteractiveTurnRestarted} {
		events := r.bus.eventsOfType(r.id, kind)
		require.Len(t, events, 1)
		assert.Equal(t, int64(3), events[0].Data.(map[string]any)["generation"])
	}
}

func TestTurnRecovery_MissingSuccessfulReceiptFailsClosed(t *testing.T) {
	r := newRRHarness(t, nil, true)
	f := newCkFixture()
	r.h.(sessionCheckpointSetter).SetSessionCheckpoints(f.deps())
	r.llm.streamCallFn = interruptedThen(engine.MoveReasonExhausted, []string{"unreceipted reply"}, "")
	require.ErrorIs(t, r.h.HandleMessage(convoCtx(), r.id, "not committed"), engine.ErrMissingTurnReceipt)
	assert.Empty(t, f.committer.commits)
	assert.Zero(t, assistantEvents(r.bus, r.id))
	got, err := r.execs.GetByInstanceAndNode(context.Background(), r.id, "interact")
	require.NoError(t, err)
	assert.Len(t, extractMessages(t, got.LLMConversation), 2)
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "not committed", pending[0].Message)
}

func TestTurnRecovery_CommitFailureCannotPublishAssistantSuccess(t *testing.T) {
	r := newRRHarness(t, nil, true)
	f := newCkFixture()
	f.committer.err = errors.New("durable commit failed")
	r.h.(sessionCheckpointSetter).SetSessionCheckpoints(f.deps())
	r.llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		if req.SessionID != "sid-b" {
			return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonRunnerLost}
		}
		return receiptStream("mac-b", false, "sid-b", "not committed")(ctx, req)
	}
	err := r.h.HandleMessage(convoCtx(), r.id, "keep pending")
	require.ErrorContains(t, err, "durable commit failed")
	assert.Zero(t, assistantEvents(r.bus, r.id))
	assert.Empty(t, f.committer.commits)
	assert.Empty(t, f.store.pruned)
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "keep pending", pending[0].Message)
}

func TestTurnRecovery_DryPoolKeepsIntentForWorker(t *testing.T) {
	r := newRRHarness(t, nil, true)
	r.mover.errs = []error{&engine.SessionWaitError{Kind: "capacity", Group: "anthropic_subscription:anthropic", Model: "m"}}
	r.llm.streamCallFn = interruptedThen(engine.MoveReasonExhausted, []string{"never"}, "late")
	require.ErrorIs(t, r.h.HandleMessage(convoCtx(), r.id, "pending"), engine.ErrNoSessionTarget)
	assert.Zero(t, assistantEvents(r.bus, r.id))
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0].Message)
}
