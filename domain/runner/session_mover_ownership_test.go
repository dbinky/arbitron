package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMover_FirstTurnDoesNotRestoreOverAnActiveOwner(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	busy := engine.ResourceFenceRef{Key: "run:" + f.runID.String(), Owner: "mac-a", Generation: 4, Token: uuid.New()}
	f.resources.active[busy.Key], f.resources.holders[busy.Key] = busy, "job:old-turn"
	req := f.request(engine.MoveReasonRunnerLost, "claude-code")
	req.FirstTurn, req.ExecutionID = true, uuid.Nil
	_, err := f.mover.Move(context.Background(), heldLease, req)
	require.ErrorIs(t, err, runner.ErrResourceBusy)
	assert.Empty(t, f.restorer.restored)
	assert.Empty(t, f.macB.got)
	assert.Equal(t, busy, f.resources.active[busy.Key])
	assert.Empty(t, f.moves.recs)
}

func TestSessionMover_ImportRetryRetainsAnUnreleasedClaim(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	transport, release := errors.New("transport unavailable"), errors.New("release failed")
	f.macB.importErr, f.resources.releaseErr = transport, release
	_, err := f.mover.Move(context.Background(), heldLease, f.request(engine.MoveReasonRunnerLost, "claude-code"))
	require.ErrorIs(t, err, transport)
	require.ErrorIs(t, err, release)
	st := f.moves.only(t)
	require.NotNil(t, st.NewResource, "a failed release cannot discard durable ownership")
	assert.Equal(t, *st.NewResource, f.resources.active[st.NewResource.Key])
	assert.True(t, st.State.Open())
	assert.Equal(t, runner.JobQueued, f.ledger.jobs[st.JobID].State)
	assert.Empty(t, f.moves.recs)

	f.macB.importErr, f.resources.releaseErr = nil, nil
	_, err = f.mover.Move(context.Background(), reservedLease(), f.request(engine.MoveReasonRunnerLost, "claude-code"))
	require.NoError(t, err)
	assert.Len(t, f.moves.recs, 1)
	assert.Empty(t, f.resources.active)
}

func TestSessionMover_FencesOnlyTheExactSessionInvocation(t *testing.T) {
	f := newMvFixture(t, "claude-code")
	req := f.request(engine.MoveReasonExhausted, "claude-code")
	exact := f.ledger.seedTurn(f.runID, "talk", "mac-a", req.InvocationID)
	foreign := f.ledger.seedTurn(f.runID, "talk", "mac-a", uuid.New())
	_, err := f.mover.Move(context.Background(), heldLease, req)
	require.NoError(t, err)
	assert.ErrorIs(t, f.ledger.FenceCheck(context.Background(), exact), runner.ErrFenced)
	assert.NoError(t, f.ledger.FenceCheck(context.Background(), foreign), "another visit's running job is not this session")
}
