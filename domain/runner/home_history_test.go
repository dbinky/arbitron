package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ engine.WorkspaceHistorySource = (*runner.WorkspaceHistorySource)(nil)

func TestWorkspaceHistorySource_MapsHomeMoves(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	run := r.pool.SeedRun(t)
	h, err := r.repo.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	_, err = r.repo.HandOff(ctx, run, "mac-2", "drain: must_move", 4096, h.Generation, runner.HomeMoveCause{OperationID: "pull:run@1/mac-2", FromGeneration: 1, ToGeneration: 2})
	require.NoError(t, err)
	got, err := runner.NewWorkspaceHistorySource(r.repo).WorkspaceHistory(ctx, run)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "mac-1", got[1].From)
	assert.Equal(t, "mac-2", got[1].To)
	assert.Equal(t, "drain: must_move", got[1].Reason)
	assert.Equal(t, int64(4096), got[1].Bytes)
	assert.Equal(t, int64(2), got[1].Generation)
	assert.Equal(t, "pull:run@1/mac-2", got[1].OperationID)
	assert.Nil(t, got[1].TargetJobID)
}

func TestHomeMove_ExactOperationSurvivesHistoryRead(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	run, job, inv := r.pool.SeedRun(t), uuid.New(), uuid.New()
	cause := runner.HomeMoveCause{TargetJobID: &job, InvocationID: &inv, NodeID: "edit", OperationID: "agent", FromGeneration: 0, ToGeneration: 1}
	_, err := r.repo.HandOff(ctx, run, "mac-1", "placement", 128, 0, cause)
	require.NoError(t, err)
	history, err := runner.NewWorkspaceHistorySource(r.repo).WorkspaceHistory(ctx, run)
	require.NoError(t, err)
	require.Len(t, history, 1)
	move := history[0]
	assert.Equal(t, &job, move.TargetJobID)
	assert.Equal(t, &inv, move.InvocationID)
	assert.Equal(t, "edit", move.NodeID)
	assert.Equal(t, "agent", move.OperationID)
	assert.Equal(t, int64(0), move.FromGeneration)
	assert.Equal(t, int64(1), move.ToGeneration)
	stored, err := r.repo.History(ctx, run)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, stored[0].At, move.At, "the SQL move timestamp is preserved")
}
