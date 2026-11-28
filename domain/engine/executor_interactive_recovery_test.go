// domain/engine/executor_interactive_recovery_test.go
package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rrExec struct {
	exec    *engine.InteractiveExecutor
	execs   *mockNodeExecutionRepo
	llm     *mockStreamingChatProvider
	bus     *mockEventBus
	ctx     context.Context
	mover   *rrMover
	intents *rrIntents
}

func newRRExec(t *testing.T, withMover bool) *rrExec {
	t.Helper()
	instRepo, nodeExecRepo, llm, files, bus, queueMgr, id := interactiveTestSetup()
	files.files["test-wf/orchestration.md"] = []byte("ORCH")
	files.files["test-wf/template.md"] = []byte("TPL")
	exec := buildInteractiveExecutor(instRepo, nodeExecRepo, llm, files, bus, queueMgr)
	leases := &rrLeaseSpy{TurnLeaseStore: engine.NewMemoryTurnLeaseStore()}
	exec.SetTurnLeases(leases, time.Minute)
	intents := newRRIntents()
	exec.SetTurnIntents(intents)
	mover := &rrMover{store: leases}
	if withMover {
		exec.SetSessionMover(mover, 2)
	}
	return &rrExec{exec: exec, execs: nodeExecRepo, llm: llm, bus: bus, mover: mover, intents: intents,
		ctx: engine.ContextWithStepIdentity(engine.ContextWithInstanceID(context.Background(), id), engine.StepIdentity{
			RunID: id, NodeID: "interact", InvocationID: uuid.New(), OperationID: "node", Kind: engine.OperationNode, Attempt: 1})}
}

func TestInteractiveExecutor_FirstTurnExhaustionRerunsWithFreshSessionSamePromptAndNonce(t *testing.T) {
	r := newRRExec(t, true)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"first reply"}, "")
	node := interactiveNode()

	res, err := r.exec.Execute(r.ctx, node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	require.True(t, res.IsWait)

	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 2)
	assert.Equal(t, reqs[0].Prompt, reqs[1].Prompt, "the step repeats with the SAME rendered prompt (same nonce)")
	assert.Equal(t, reqs[0].SystemPrompt, reqs[1].SystemPrompt)
	assert.Equal(t, "", reqs[1].SessionID, "fresh session: no bundle exists before the first success")

	require.Len(t, r.mover.reqs, 1)
	assert.True(t, r.mover.reqs[0].FirstTurn)
	assert.Equal(t, 1, r.mover.reqs[0].TurnIndex)
	assert.Equal(t, "exhausted", r.mover.reqs[0].Reason)
	assert.Equal(t, uuid.Nil, r.mover.reqs[0].ExecutionID, "no session record exists yet")

	id, _ := engine.InstanceIDFromContext(r.ctx)
	restarted := r.bus.eventsOfType(id, engine.EventTypeInteractiveTurnRestarted)
	require.Len(t, restarted, 1)
	assert.Equal(t, 1, restarted[0].Data.(map[string]any)["turnIndex"])

	rec, err := r.execs.GetByInstanceAndNode(context.Background(), id, node.ID)
	require.NoError(t, err)
	nonce, _ := rec.LLMConversation["nonce"].(string)
	require.NotEmpty(t, nonce)
	assert.True(t, strings.Contains(reqs[1].Prompt, nonce), "the persisted nonce is the one in the re-run prompt")
	msgs := extractMessages(t, rec.LLMConversation)
	assert.Equal(t, "first reply", msgs[1]["content"], "the partial first turn is discarded")
	assert.Equal(t, instance.NodeStatusSucceeded, rec.Status)
}

func TestInteractiveExecutor_FirstTurnRecoveryCommitsOnlySuccessfulReceiptAndIdentity(t *testing.T) {
	r := newRRExec(t, true)
	f := newCkFixture()
	f.committer.apply = func(exec *instance.NodeExecution) error { return r.execs.Create(context.Background(), exec) }
	f.committer.finish = func(id uuid.UUID) { require.NoError(t, r.intents.Complete(context.Background(), id)) }
	r.exec.SetSessionCheckpoints(f.deps())
	r.exec.SetSessionMover(receiptMove{rrMover: r.mover, importReceipt: engine.StepCompletion{
		JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: "import-only", Generation: 1},
	}}, 2)
	inv := uuid.New()
	id, _ := engine.InstanceIDFromContext(r.ctx)
	r.ctx = engine.ContextWithStepIdentity(r.ctx, engine.StepIdentity{RunID: id, NodeID: interactiveNode().ID,
		InvocationID: inv, OperationID: "node", Kind: engine.OperationNode, Attempt: 1})
	calls := 0
	r.llm.streamCallFn = func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		calls++
		if calls == 1 {
			sink, ok := engine.StepCompletionSinkFromContext(ctx)
			require.True(t, ok)
			require.NoError(t, sink.Record(engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: "failed", Generation: 1}}))
			return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonExhausted}
		}
		return receiptStream("mac-b", false, "sid-first", "successful first reply")(ctx, req)
	}
	result, err := r.exec.Execute(r.ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	require.True(t, result.IsWait)
	require.Len(t, f.committer.commits, 1)
	commit := f.committer.commits[0]
	require.Len(t, commit.Completions, 1)
	assert.Equal(t, "mac-b", commit.Completions[0].JobFence.RunnerID)
	assert.Equal(t, int64(2), commit.SessionGeneration)
	events := r.bus.eventsOfType(id, engine.EventTypeInteractiveMessage)
	require.Len(t, events, 1, "the rendered prompt is never published as a user message")
	data := events[0].Data.(map[string]any)
	assert.Equal(t, "successful first reply", data["content"])
	assert.Equal(t, interactiveNode().ID, data["nodeId"])
	assert.Equal(t, inv.String(), data["invocationId"])
	assert.Equal(t, commit.RequestID.String(), data["requestId"])
	assert.Equal(t, 1, data["turnIndex"])
	assert.Equal(t, int64(2), data["generation"])
}

func TestInteractiveExecutor_FirstTurnNoTargetKeepsIntentPending(t *testing.T) {
	r := newRRExec(t, true)
	r.mover.errs = []error{&engine.SessionWaitError{Kind: "capacity", Group: "anthropic_subscription:anthropic", Model: "m"}}
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"x"}, "")
	_, err := r.exec.Execute(r.ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrNoSessionTarget))
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1, "the rendered first-turn intent is kept for the re-run")
	assert.Equal(t, r.llm.getCapturedReqs()[0].Prompt, pending[0].Prompt)
}

func TestInteractiveExecutor_FirstTurnMoveBudgetKeepsExactPendingIntent(t *testing.T) {
	r := newRRExec(t, true)
	r.llm.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
		return nil, &engine.SessionMoveRequiredError{From: "mac-a", Reason: engine.MoveReasonExhausted}
	}
	_, err := r.exec.Execute(r.ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.ErrorIs(t, err, engine.ErrRecoveryBudgetExhausted)
	assert.NotErrorIs(t, err, engine.ErrSessionRestoreFailed)
	assert.Len(t, r.mover.reqs, 2)
	reqs := r.llm.getCapturedReqs()
	require.Len(t, reqs, 3)
	pending, err := r.intents.Pending(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, reqs[0].Prompt, pending[0].Prompt)
	assert.Equal(t, reqs[0].Prompt, reqs[2].Prompt)
	assert.Zero(t, assistantEvents(r.bus, pending[0].RunID))
}

func TestInteractiveExecutor_FirstTurnNoMoverInterruptionFails(t *testing.T) {
	r := newRRExec(t, false)
	r.llm.streamCallFn = interruptedThen("exhausted", []string{"x"}, "")
	node := interactiveNode()
	_, err := r.exec.Execute(r.ctx, node, variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.True(t, errors.Is(err, engine.ErrSessionMoveRequired))
	id, _ := engine.InstanceIDFromContext(r.ctx)
	_, gerr := r.execs.GetByInstanceAndNode(context.Background(), id, node.ID)
	assert.Error(t, gerr, "nothing persisted for an interrupted first turn")
}

func TestInteractiveExecutor_FirstTurnMoveFailureSurfaces(t *testing.T) {
	r := newRRExec(t, true)
	r.mover.errs = []error{errors.New("restore failed")}
	r.llm.streamCallFn = interruptedThen("runner_lost", []string{"x"}, "")
	_, err := r.exec.Execute(r.ctx, interactiveNode(), variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore failed")
	assert.Len(t, r.llm.getCapturedReqs(), 1, "no re-run without a recovered owner")
}
