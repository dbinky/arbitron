package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSessionState_NewInvocationWinsOverOlderHigherAttempt(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	t0 := time.Now().UTC().Truncate(time.Second)
	inst, old := seedSessionExec(t, db, "chat", 9, t0)
	oldInv, newInv := uuid.New(), uuid.New()
	require.NoError(t, repo.SetSessionState(ctx, old.ID, instance.SessionState{InvocationID: oldInv, CheckpointRef: strp("old"), Generation: 4}))
	newer := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "chat", Attempt: 1, StartedAt: t0.Add(time.Minute), Status: instance.NodeStatusSucceeded}
	require.NoError(t, repo.Create(ctx, newer))
	require.NoError(t, repo.SetSessionState(ctx, newer.ID, instance.SessionState{InvocationID: newInv, CheckpointRef: strp("new"), Generation: 1}))
	st, ok, err := repo.LatestSessionState(ctx, inst.ID, "chat")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, newInv, st.InvocationID)
	require.Equal(t, "new", *st.CheckpointRef)
	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	require.Equal(t, newer.ID, got.ID)
	require.Equal(t, newInv, got.SessionInvocationID)
	// A delayed move belongs to the old invocation, not the newer wall-clock visit.
	require.NoError(t, repo.RecordSessionMove(ctx, old.ID, instance.SessionMoveRecord{RunID: inst.ID, InvocationID: oldInv, NodeID: "chat", FromRunner: "a", ToRunner: "b", Reason: "drain", Generation: 5, At: t0.Add(2 * time.Minute)}))
	list, err := repo.ListByInstance(ctx, inst.ID)
	require.NoError(t, err)
	for _, exec := range list {
		if exec.ID == old.ID {
			require.Len(t, exec.SessionMoves, 1)
		} else {
			require.Empty(t, exec.SessionMoves)
		}
	}
}

func TestSessionState_ClosedDatabasePropagatesReadError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewNodeExecutionRepo(db.DB)
	require.NoError(t, db.DB.Close())
	_, _, err := repo.LatestSessionState(context.Background(), uuid.New(), "chat")
	require.Error(t, err)
}
