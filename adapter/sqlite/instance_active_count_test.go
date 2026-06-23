package sqlite

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedInstanceWithWorkflowID inserts an instance in state carrying workflowID
// (the stable id added by migration 017). Returns the instance id.
func seedInstanceWithWorkflowID(t *testing.T, db testDB, ownerID, workflowID uuid.UUID, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state, workflow_id)
		 VALUES (?, ?, 'wf', '{}', ?, ?)`,
		uuidToString(id), uuidToString(ownerID), state, uuidToString(workflowID))
	require.NoError(t, err)
	return id
}

// Happy/success: non-terminal instances carrying the id are counted; a different
// id is not.
func TestInstanceRepo_CountActiveByWorkflowID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)
	wfID := uuid.New()
	other := uuid.New()

	seedInstanceWithWorkflowID(t, db, owner, wfID, "running")
	seedInstanceWithWorkflowID(t, db, owner, wfID, "queued")
	seedInstanceWithWorkflowID(t, db, owner, other, "running")

	n, err := repo.CountActiveByWorkflowID(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "both non-terminal instances of wfID counted")

	n, err = repo.CountActiveByWorkflowID(ctx, other)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// Failure boundary: terminal-state instances are not counted as active.
func TestInstanceRepo_CountActiveByWorkflowID_TerminalNotCounted(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)
	wfID := uuid.New()

	for _, st := range []string{"completed", "failed", "cancelled"} {
		seedInstanceWithWorkflowID(t, db, owner, wfID, st)
	}

	n, err := repo.CountActiveByWorkflowID(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "terminal-state instances must not count as active")
}

// Edge: queued/waiting/paused all count as active (non-terminal).
func TestInstanceRepo_CountActiveByWorkflowID_NonTerminalStates(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)
	wfID := uuid.New()

	for _, st := range []string{"queued", "waiting", "paused", "running"} {
		seedInstanceWithWorkflowID(t, db, owner, wfID, st)
	}

	n, err := repo.CountActiveByWorkflowID(ctx, wfID)
	require.NoError(t, err)
	assert.Equal(t, 4, n)
}

// Edge: an unknown id (no instances) counts zero.
func TestInstanceRepo_CountActiveByWorkflowID_Unknown(t *testing.T) {
	repo := NewInstanceRepo(setupTestDB(t).DB)
	n, err := repo.CountActiveByWorkflowID(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
