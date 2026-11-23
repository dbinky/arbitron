package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedSessionExec(t *testing.T, db testDB, nodeID string, attempt int, started time.Time) (*instance.Instance, *instance.NodeExecution) {
	t.Helper()
	ctx := context.Background()
	ownerID, err := NewUserRepo(db.DB).GetDefaultUser(ctx)
	require.NoError(t, err)
	inst := newTestInstance(t, ownerID)
	require.NoError(t, NewInstanceRepo(db.DB).Create(ctx, inst))
	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: nodeID, Attempt: attempt,
		Status: instance.NodeStatusSucceeded, StartedAt: started,
		LLMConversation: map[string]any{"sessionId": "s1", "nonce": "n", "messages": []any{}}}
	require.NoError(t, NewNodeExecutionRepo(db.DB).Create(ctx, exec))
	return inst, exec
}

func TestNodeExecutionSession_SetAndReadBack(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	inst, exec := seedSessionExec(t, db, "chat", 1, time.Now().UTC().Truncate(time.Second))

	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.Nil(t, got.SessionCheckpointRef, "a fresh record has no session state")
	assert.Nil(t, got.SessionPinRunner)
	assert.Zero(t, got.SessionGeneration)

	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{
		CheckpointRef: strp("session:ref-1"), PinRunner: strp("builtin"), Generation: 3}))

	got, err = repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	require.NotNil(t, got.SessionCheckpointRef)
	assert.Equal(t, "session:ref-1", *got.SessionCheckpointRef)
	assert.Equal(t, "builtin", *got.SessionPinRunner)
	assert.EqualValues(t, 3, got.SessionGeneration)

	list, err := repo.ListByInstance(ctx, inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "session:ref-1", *list[0].SessionCheckpointRef)

	st, ok, err := repo.LatestSessionState(ctx, inst.ID, "chat")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "builtin", *st.PinRunner)
	assert.EqualValues(t, 3, st.Generation)

	_, ok, err = repo.LatestSessionState(ctx, inst.ID, "nope")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestNodeExecutionSession_StaleGenerationRejected(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	_, exec := seedSessionExec(t, db, "chat", 1, time.Now().UTC())

	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{CheckpointRef: strp("a"), PinRunner: strp("r1"), Generation: 5}))
	// Same generation: a later turn under the same lease generation is allowed.
	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{CheckpointRef: strp("b"), PinRunner: strp("r1"), Generation: 5}))
	// Older generation: a superseded owner's late write is refused.
	err := repo.SetSessionState(ctx, exec.ID, instance.SessionState{CheckpointRef: strp("late"), PinRunner: strp("r0"), Generation: 4})
	assert.ErrorIs(t, err, instance.ErrStaleSessionGeneration)

	err = repo.SetSessionState(ctx, uuid.New(), instance.SessionState{Generation: 9})
	assert.ErrorIs(t, err, instance.ErrNotFound)

	st, _, err := repo.LatestSessionState(ctx, exec.InstanceID, "chat")
	require.NoError(t, err)
	assert.Equal(t, "b", *st.CheckpointRef, "the stale write changed nothing")
}

func TestNodeExecutionSession_LegacyUpdateNeverClobbersSessionColumns(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	inst, exec := seedSessionExec(t, db, "chat", 1, time.Now().UTC())
	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{CheckpointRef: strp("keep"), PinRunner: strp("r9"), Generation: 2}))

	// A legacy writer holding a record read before the session state was set.
	exec.Output = map[string]any{"x": 1}
	require.NoError(t, repo.Update(ctx, exec))
	ok, err := repo.UpdateIfSessionOpen(ctx, exec)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.Equal(t, "keep", *got.SessionCheckpointRef)
	assert.Equal(t, "r9", *got.SessionPinRunner)
	assert.EqualValues(t, 2, got.SessionGeneration)
}

func TestNodeExecutionSession_RecordMoveIsAtomicAndAttached(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	started := time.Now().UTC().Truncate(time.Second)
	inst, exec := seedSessionExec(t, db, "chat", 1, started)
	require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{CheckpointRef: strp("c1"), PinRunner: strp("r1"), Generation: 1}))

	move := instance.SessionMoveRecord{RunID: inst.ID, InvocationID: uuid.New(), NodeID: "chat", FromRunner: "r1", ToRunner: "r2",
		Reason: "exhausted", Lossy: true, Notice: "tool details and reasoning may be lost", Generation: 2, At: started.Add(5 * time.Second)}
	require.NoError(t, repo.RecordSessionMove(ctx, exec.ID, move))

	// Replaying the same generation is stale: the move already committed.
	assert.ErrorIs(t, repo.RecordSessionMove(ctx, exec.ID, move), instance.ErrStaleSessionGeneration)

	got, err := repo.GetByInstanceAndNode(ctx, inst.ID, "chat")
	require.NoError(t, err)
	assert.Equal(t, "r2", *got.SessionPinRunner)
	assert.EqualValues(t, 2, got.SessionGeneration)
	assert.Equal(t, "c1", *got.SessionCheckpointRef, "a move keeps the committed checkpoint ref")
	require.Len(t, got.SessionMoves, 1)
	m := got.SessionMoves[0]
	assert.NotEqual(t, uuid.Nil, m.ID)
	assert.Equal(t, "r1", m.FromRunner)
	assert.Equal(t, "r2", m.ToRunner)
	assert.Equal(t, "exhausted", m.Reason)
	assert.True(t, m.Lossy)
	assert.Equal(t, "tool details and reasoning may be lost", m.Notice)
	assert.EqualValues(t, 2, m.Generation)
	assert.True(t, m.At.Equal(move.At))

	var moves int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM session_moves").Scan(&moves))
	assert.Equal(t, 1, moves, "the refused replay inserted nothing")
}

func TestNodeExecutionSession_MovesBelongToTheirVisit(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	t0 := time.Now().UTC().Truncate(time.Second)
	inst, first := seedSessionExec(t, db, "chat", 1, t0)
	second := &instance.NodeExecution{ID: uuid.New(), InstanceID: inst.ID, NodeID: "chat", Attempt: 2,
		Status: instance.NodeStatusSucceeded, StartedAt: t0.Add(time.Minute)}
	require.NoError(t, repo.Create(ctx, second))

	require.NoError(t, repo.RecordSessionMove(ctx, first.ID, instance.SessionMoveRecord{RunID: inst.ID, InvocationID: uuid.New(),
		NodeID: "chat", FromRunner: "a", ToRunner: "b", Reason: "drain", Generation: 2, At: t0.Add(10 * time.Second)}))
	require.NoError(t, repo.RecordSessionMove(ctx, second.ID, instance.SessionMoveRecord{RunID: inst.ID, InvocationID: uuid.New(),
		NodeID: "chat", FromRunner: "b", ToRunner: "c", Reason: "runner_lost", Generation: 2, At: t0.Add(2 * time.Minute)}))

	list, err := repo.ListByInstance(ctx, inst.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Len(t, list[0].SessionMoves, 1)
	assert.Equal(t, "drain", list[0].SessionMoves[0].Reason)
	require.Len(t, list[1].SessionMoves, 1)
	assert.Equal(t, "runner_lost", list[1].SessionMoves[0].Reason)
}
