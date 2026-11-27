package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHistory struct {
	byRun map[uuid.UUID][]instance.WorkspaceMove
}

func (f fakeHistory) WorkspaceHistory(_ context.Context, id uuid.UUID) ([]instance.WorkspaceMove, error) {
	return f.byRun[id], nil
}

func TestWorkspaceHistory_OnInstanceAndTheStepThatMoved(t *testing.T) {
	f := newTestFixtures()
	ctx := context.Background()
	run, jobA, jobB, invA, invB := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, f.instanceRepo.Create(ctx, &instance.Instance{ID: run, WorkflowName: "w", State: instance.StateRunning, CreatedAt: at, UpdatedAt: at}))
	// Deliberately overlapping timestamps: only exact placement evidence can
	// distinguish parallel executions and attach the correct move.
	for node, job := range map[string]uuid.UUID{"A": jobA, "B": jobB} {
		require.NoError(t, f.nodeExecRepo.Create(ctx, &instance.NodeExecution{ID: uuid.New(), InstanceID: run, NodeID: node, Attempt: 1,
			Status: instance.NodeStatusSucceeded, StartedAt: at, PlacementHistory: []instance.PlacementRecord{{JobID: job.String()}}}))
	}
	moves := []instance.WorkspaceMove{
		{From: "", To: "mac-1", Reason: "acquire", Generation: 1, At: at}, // no execution yet
		{From: "mac-1", To: "mac-2", Reason: "must_move: exhausted", Bytes: 2048, Generation: 2, At: at,
			TargetJobID: &jobA, InvocationID: &invA, NodeID: "A", OperationID: "agent", FromGeneration: 1, ToGeneration: 2},
		{From: "mac-2", To: "mac-1", Reason: "must_move", Generation: 3, At: at,
			TargetJobID: &jobB, InvocationID: &invB, NodeID: "B", OperationID: "process", FromGeneration: 2, ToGeneration: 3},
		{From: "mac-1", To: "mac-2", Reason: "drain", Generation: 4, At: at, OperationID: "pull:run@4/mac-2", FromGeneration: 3, ToGeneration: 4},
	}
	f.engine.SetWorkspaceHistorySource(fakeHistory{byRun: map[uuid.UUID][]instance.WorkspaceMove{run: moves}})
	inst, err := f.engine.GetInstance(bgAdmin(), run)
	require.NoError(t, err)
	assert.Equal(t, moves, inst.WorkspaceMoves, "instance includes initial acquisitions and maintenance moves")
	trace, err := f.engine.GetTrace(bgAdmin(), run)
	require.NoError(t, err)
	require.Len(t, trace, 2)
	for _, ex := range trace {
		want := moves[1:2]
		if ex.NodeID == "B" {
			want = moves[2:3]
		}
		assert.Equal(t, want, ex.WorkspaceMoves)
		node, err := f.engine.GetNodeTrace(bgAdmin(), run, ex.NodeID)
		require.NoError(t, err)
		assert.Equal(t, want, node.WorkspaceMoves, "node trace has the same exact operation association")
	}
}

func TestWorkspaceHistory_RetryLoopAndParallelCorrelation(t *testing.T) {
	f := newTestFixtures()
	ctx := context.Background()
	run := uuid.New()
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, f.instanceRepo.Create(ctx, &instance.Instance{ID: run, WorkflowName: "w", State: instance.StateRunning}))
	var moves []instance.WorkspaceMove
	want := map[uuid.UUID][]instance.WorkspaceMove{}
	for i, tc := range []struct {
		node    string
		attempt int
	}{{"loop", 1}, {"loop", 2}, {"loop", 1}, {"parallel", 1}} {
		job, inv, execID := uuid.New(), uuid.New(), uuid.New()
		move := instance.WorkspaceMove{From: "mac-1", To: "mac-2", Reason: "must_move", Generation: int64(i + 1), At: at,
			TargetJobID: &job, InvocationID: &inv, NodeID: tc.node, OperationID: "agent"}
		moves = append(moves, move)
		want[execID] = []instance.WorkspaceMove{move}
		require.NoError(t, f.nodeExecRepo.Create(ctx, &instance.NodeExecution{ID: execID, InstanceID: run, NodeID: tc.node, Attempt: tc.attempt,
			StartedAt: at.Add(time.Hour), Status: instance.NodeStatusSucceeded, PlacementHistory: []instance.PlacementRecord{{JobID: job.String()}}}))
	}
	// A legacy attempt without placement evidence never borrows another
	// visit's move merely because node, attempt and timestamps overlap.
	legacy := uuid.New()
	require.NoError(t, f.nodeExecRepo.Create(ctx, &instance.NodeExecution{ID: legacy, InstanceID: run, NodeID: "loop", Attempt: 1, StartedAt: at}))
	f.engine.SetWorkspaceHistorySource(fakeHistory{byRun: map[uuid.UUID][]instance.WorkspaceMove{run: moves}})
	trace, err := f.engine.GetTrace(bgAdmin(), run)
	require.NoError(t, err)
	require.Len(t, trace, 5)
	for _, ex := range trace {
		assert.Equal(t, want[ex.ID], ex.WorkspaceMoves, "execution %s", ex.ID)
	}
}
