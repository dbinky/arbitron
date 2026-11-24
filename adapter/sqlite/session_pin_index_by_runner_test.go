package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPinIndex_ListByRunnerReportsQueryErrors(t *testing.T) {
	raw, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	got, err := NewSessionPinIndex(raw).ListByRunner(context.Background(), "mac-a")
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "listing sessions pinned to mac-a")
}

func TestSessionPinIndex_ListByRunnerUsesOnlyTheNewestRecord(t *testing.T) {
	db := setupTestDB(t)
	run, _ := seedPinned(t, db, "waiting", "talk", 2, "mac-a", "s")
	idx := NewSessionPinIndex(db.DB)
	old, err := idx.ListByRunner(context.Background(), "older-runner")
	require.NoError(t, err)
	assert.Empty(t, old, "a superseded record's pin is not a pin")
	cur, err := idx.ListByRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	require.Len(t, cur, 1)
	assert.Equal(t, run, cur[0].RunID)
}

func TestRunnerSessions_NewInvocationWinsOverOlderHigherAttempt(t *testing.T) {
	db := setupTestDB(t)
	runID := seedInstance(t, db, localUserID(t), "waiting")
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = 'talk' WHERE id = ?`, uuidToString(runID))
	require.NoError(t, err)
	seedPinnedVisit(t, db, runID, "talk", false, 3, 5, "old-runner", "old-runner/anthropic_subscription/-")
	seedPinnedVisit(t, db, runID, "talk", true, 1, 1, "mac-a", "mac-a/anthropic_subscription/-")
	idx := NewSessionPinIndex(db.DB)
	old, err := idx.ListByRunner(context.Background(), "old-runner")
	require.NoError(t, err)
	assert.Empty(t, old, "a closed visit's pin is not a pin, whatever its attempt")
	cur, err := idx.ListByRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, []runner.RunnerSession{{RunID: runID, NodeID: "talk", Generation: 1}}, cur)
}

func TestSessionPinIndex_ListByRunnerIgnoresNodesTheRunLeft(t *testing.T) {
	db := setupTestDB(t)
	run, _ := seedPinned(t, db, "waiting", "talk", 1, "mac-a", "s")
	_, err := db.DB.Exec(`UPDATE instances SET current_node_id = 'next' WHERE id = ?`, uuidToString(run))
	require.NoError(t, err)
	got, err := NewSessionPinIndex(db.DB).ListByRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func setPinGeneration(t *testing.T, db testDB, execID uuid.UUID, gen int64) {
	t.Helper()
	_, err := db.DB.Exec(`UPDATE node_executions SET session_generation = ? WHERE id = ?`, gen, uuidToString(execID))
	require.NoError(t, err)
}

func TestSessionPinIndex_ListByRunnerListsLiveSessionsPinnedThere(t *testing.T) {
	db := setupTestDB(t)
	waiting, waitingExec := seedPinned(t, db, "waiting", "talk", 1, "mac-a", "mac-a/anthropic_subscription/-")
	setPinGeneration(t, db, waitingExec, 4)
	running, _ := seedPinned(t, db, "running", "chat", 1, "mac-a", "mac-a/anthropic_subscription/-")
	seedPinned(t, db, "waiting", "talk", 1, "mac-b", "mac-b/anthropic_subscription/-")
	seedPinned(t, db, "completed", "talk", 1, "mac-a", "mac-a/anthropic_subscription/-")
	seedPinned(t, db, "waiting", "talk", 1, "", "")

	got, err := NewSessionPinIndex(db.DB).ListByRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.ElementsMatch(t, []runner.RunnerSession{
		{RunID: waiting, NodeID: "talk", Generation: 4},
		{RunID: running, NodeID: "chat", Generation: 1},
	}, got)
	other, err := NewSessionPinIndex(db.DB).ListByRunner(context.Background(), "mac-b")
	require.NoError(t, err)
	assert.Len(t, other, 1, "each runner lists only its own pins")
}
