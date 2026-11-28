package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dmUnavailableRunner struct{ err error }

func (s dmUnavailableRunner) Run(_ context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	if a.Job.RunnerID != runner.BuiltInRunnerID || job.Session == nil || job.Session.Bundle == nil ||
		string(job.Session.Bundle.Data) != "bundle" || job.Session.WorkDir != "/ws" {
		return runner.StepOutput{}, errors.New("wrong import input")
	}
	return runner.StepOutput{}, s.err
}

func TestSessionMover_ImportTransportFailureStaysRetryableAndReleasesSlot(t *testing.T) {
	f := newDmFixture(t)
	unavailable := errors.New("transfer unavailable")
	ctx := context.Background()
	_, err := f.mover(func(d *runner.MoverDeps) {
		d.Runners = dmRunners{runner.BuiltInRunnerID: dmUnavailableRunner{unavailable}}
	}).Move(ctx, f.lease, f.request())
	require.ErrorIs(t, err, unavailable)
	assert.Zero(t, f.lf.slots(t))
	assert.Equal(t, "mac-a", *f.storedExecution(t).SessionPinRunner)
	in := f.moveIntent(t)
	st, found, err := f.moves.MoveForIntent(ctx, in.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, st.State.Open(), "disk/transport failure must not publish a rebuilt pin or fail the move")
	assert.Empty(t, f.storedExecution(t).SessionMoves)
	require.NoError(t, f.mover(nil).ResumeMove(engine.ContextWithTurnLease(ctx, f.held(t)), in))
	got := f.storedExecution(t)
	assert.Equal(t, string(runner.BuiltInRunnerID), *got.SessionPinRunner)
	assert.EqualValues(t, 2, got.SessionGeneration)
	assert.Len(t, got.SessionMoves, 1)
	assert.Zero(t, f.lf.slots(t))
	pending, err := f.intents.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	jobs, err := f.lf.ledger.ListByRun(ctx, f.inst.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, runner.JobCompleted, jobs[0].State)
	attempts, err := f.lf.ledger.Attempts(ctx, jobs[0].ID)
	require.NoError(t, err)
	assert.Len(t, attempts, 2, "the failed transfer released its slot before the retry")
}

func TestSessionMover_PendingIntentIsTheAcceptedBindingSource(t *testing.T) {
	f := newDmFixture(t)
	ctx := context.Background()
	in := engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), LeaseKey: f.lease.Key,
		RunID: f.inst.ID, InvocationID: f.inv, NodeID: "talk", TurnIndex: 2, Kind: engine.TurnIntentMessage,
		Message: "accepted message", PriorCheckpointRef: "ckpt-1", SessionGeneration: 1, OperationID: "turn/2",
		Binding: engine.AcceptedTurnBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"}}
	require.NoError(t, f.intents.Persist(ctx, in))
	req := f.request()
	req.Binding.Model, req.Requirements.Model = "claude-opus-unaccepted", "claude-opus-unaccepted"
	req.Binding.Backend, req.Requirements.Backend = "opencode", "opencode"
	res, err := f.mover(nil).Move(ctx, f.lease, req)
	require.NoError(t, err)
	assert.Equal(t, "sid-builtin", res.SessionID)
	assert.Equal(t, "sid-builtin", f.storedExecution(t).LLMConversation[engine.ConversationKeySessionID])
	jobs, err := f.lf.ledger.ListByRun(ctx, f.inst.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, in.Binding.Model, jobs[0].Model)
	pending, err := f.intents.PendingFor(ctx, f.lease.Key)
	require.NoError(t, err)
	require.NotNil(t, pending)
	assert.Equal(t, in.RequestID, pending.RequestID)
	assert.Equal(t, in.Binding, pending.Binding)
}

func (f *dmFixture) storedExecution(t *testing.T) *instance.NodeExecution {
	t.Helper()
	e, err := f.execs.GetByInstanceAndNode(context.Background(), f.inst.ID, "talk")
	require.NoError(t, err)
	return e
}
