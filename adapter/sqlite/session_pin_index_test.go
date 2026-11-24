package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPinned opens one visit; retry generations within that visit supersede older pins.
func seedPinned(t *testing.T, db testDB, state, node string, attempt int, pin, source string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	runID := seedInstance(t, db, localUserID(t), state)
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = ? WHERE id = ?`, node, uuidToString(runID))
	require.NoError(t, err)
	inv, err := NewInvocationRepo(db.DB).Begin(context.Background(), runID, node)
	require.NoError(t, err)
	var last uuid.UUID
	for a := 1; a <= attempt; a++ {
		p, src := pin, source
		if a < attempt {
			p, src = "older-runner", "older-runner/anthropic_subscription/-"
		}
		last = seedPinnedSessionExec(t, db, runID, node, inv.ID, a, int64(a), p, src)
	}
	return runID, last
}

func seedPinnedVisit(t *testing.T, db testDB, runID uuid.UUID, node string, commitPrevious bool, attempt int, gen int64, pin, source string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	repo := NewInvocationRepo(db.DB)
	inv, err := repo.Begin(ctx, runID, node)
	require.NoError(t, err)
	if commitPrevious {
		require.NoError(t, repo.Commit(ctx, inv.ID))
		inv, err = repo.Begin(ctx, runID, node)
		require.NoError(t, err)
	}
	return inv.ID, seedPinnedSessionExec(t, db, runID, node, inv.ID, attempt, gen, pin, source)
}

func seedPinnedSessionExec(t *testing.T, db testDB, runID uuid.UUID, node string, invocation uuid.UUID, attempt int, gen int64, pin, source string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	repo := NewNodeExecutionRepo(db.DB)
	exec := &instance.NodeExecution{ID: uuid.New(), InstanceID: runID, NodeID: node, Attempt: attempt,
		Status: instance.NodeStatusSucceeded, LLMConversation: map[string]any{"messages": []any{}}, StartedAt: time.Now().UTC()}
	require.NoError(t, repo.Create(ctx, exec))
	if pin != "" {
		p := pin
		require.NoError(t, repo.SetSessionState(ctx, exec.ID, instance.SessionState{InvocationID: invocation, PinRunner: &p, Generation: gen}))
	}
	_, err := db.DB.Exec(`UPDATE node_executions SET source_id = ? WHERE id = ?`, source, uuidToString(exec.ID))
	require.NoError(t, err)
	return exec.ID
}

func TestSessionPinIndex_ListsWaitingPinnedSessionsAtCurrentNode(t *testing.T) {
	db := setupTestDB(t)
	waiting, waitingExec := seedPinned(t, db, "waiting", "talk", 2, "mac-a", "mac-a/anthropic_subscription/-")
	seedPinned(t, db, "running", "talk", 1, "mac-a", "mac-a/anthropic_subscription/-")
	seedPinned(t, db, "waiting", "talk", 1, "", "")
	got, err := NewSessionPinIndex(db.DB).ListPinned(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, runner.PinnedSession{RunID: waiting, NodeID: "talk", ExecutionID: waitingExec,
		RunnerID: "mac-a", SourceID: "mac-a/anthropic_subscription/-"}, got[0],
		"inside the open visit the highest session generation wins; the older retry's pin is superseded")
}

func TestSessionPinIndex_IgnoresRecordsOfOtherNodes(t *testing.T) {
	db := setupTestDB(t)
	runID, _ := seedPinned(t, db, "waiting", "talk", 1, "mac-a", "mac-a/anthropic_subscription/-")
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = 'next' WHERE id = ?`, uuidToString(runID))
	require.NoError(t, err)
	got, err := NewSessionPinIndex(db.DB).ListPinned(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got, "a session the run has left is not pinned any more")
}

func TestSessionPinIndex_NewInvocationWinsOverOlderHigherAttempt(t *testing.T) {
	db := setupTestDB(t)
	runID := seedInstance(t, db, localUserID(t), "waiting")
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = 'talk' WHERE id = ?`, uuidToString(runID))
	require.NoError(t, err)
	_, old := seedPinnedVisit(t, db, runID, "talk", false, 3, 5, "old-runner", "old-runner/anthropic_subscription/-")
	_, cur := seedPinnedVisit(t, db, runID, "talk", true, 1, 1, "mac-a", "mac-a/anthropic_subscription/-")
	_, err = db.DB.Exec(`UPDATE node_executions SET started_at = ? WHERE id = ?`, timeToString(time.Now().Add(-time.Hour)), uuidToString(cur))
	require.NoError(t, err)
	got, err := NewSessionPinIndex(db.DB).ListPinned(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, cur, got[0].ExecutionID, "the open visit's session, never MAX(attempt)")
	assert.NotEqual(t, old, got[0].ExecutionID)
	assert.Equal(t, runner.RunnerID("mac-a"), got[0].RunnerID)
}

func TestSessionPinIndex_PinWithoutSessionInvocationIsNotListed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	runID := seedInstance(t, db, localUserID(t), "waiting")
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = 'talk' WHERE id = ?`, uuidToString(runID))
	require.NoError(t, err)
	seedPinnedSessionExec(t, db, runID, "talk", uuid.Nil, 1, 1, "mac-a", "mac-a/anthropic_subscription/-")
	other := seedInstance(t, db, localUserID(t), "waiting")
	_, err = db.DB.Exec(`UPDATE instances SET current_node_id = 'talk' WHERE id = ?`, uuidToString(other))
	require.NoError(t, err)
	inv, _ := seedPinnedVisit(t, db, other, "talk", false, 1, 1, "mac-a", "mac-a/anthropic_subscription/-")
	require.NoError(t, NewInvocationRepo(db.DB).Commit(ctx, inv))
	got, err := NewSessionPinIndex(db.DB).ListPinned(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSessionRecovery_MoveRefreshesSourceAndInvocation(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	runID, execID := seedPinned(t, db, "waiting", "talk", 1, "mac-a", "mac-a/anthropic_subscription/-")
	inv, err := NewInvocationRepo(db.DB).Begin(ctx, runID, "talk")
	require.NoError(t, err)
	require.NoError(t, NewNodeExecutionRepo(db.DB).RecordSessionMove(ctx, execID, instance.SessionMoveRecord{
		RunID: runID, InvocationID: inv.ID, NodeID: "talk", FromRunner: "mac-a", ToRunner: "mac-b",
		Reason: "exhausted", Generation: 2}))
	got, err := NewSessionPinIndex(db.DB).ListPinned(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, runner.RunnerID("mac-b"), got[0].RunnerID)
	assert.Equal(t, execID, got[0].ExecutionID)
	assert.Equal(t, runner.SourceID(""), got[0].SourceID, "mac-a's source is not mac-b's: never report a stale source")
	_, err = db.DB.Exec(`UPDATE node_executions SET source_id = 'mac-b/anthropic_subscription/-' WHERE id = ?`, uuidToString(execID))
	require.NoError(t, err)
	got, err = NewSessionPinIndex(db.DB).ListPinned(ctx)
	require.NoError(t, err)
	assert.Equal(t, runner.SourceID("mac-b/anthropic_subscription/-"), got[0].SourceID)
}
