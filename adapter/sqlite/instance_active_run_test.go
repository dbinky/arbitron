package sqlite

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedNamedInstance(t *testing.T, db testDB, ownerID uuid.UUID, workflow, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state)
		 VALUES (?, ?, ?, '{}', ?)`,
		uuidToString(id), uuidToString(ownerID), workflow, state)
	require.NoError(t, err)
	return id
}

func TestInstanceRepo_HasActiveRun(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)
	other := seedUser(t, db, "other-"+uuid.NewString()[:8])

	// Running instance => active.
	seedNamedInstance(t, db, owner, "report", "running")
	has, err := repo.HasActiveRun(ctx, "report", owner)
	require.NoError(t, err)
	assert.True(t, has)

	// Different workflow => not active.
	has, err = repo.HasActiveRun(ctx, "cleanup", owner)
	require.NoError(t, err)
	assert.False(t, has)

	// Different owner => not active.
	has, err = repo.HasActiveRun(ctx, "report", other)
	require.NoError(t, err)
	assert.False(t, has)
}

func TestInstanceRepo_HasActiveRun_TerminalStatesNotActive(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	owner := localUserID(t)

	for _, st := range []string{"completed", "failed", "cancelled"} {
		seedNamedInstance(t, db, owner, "terminalwf", st)
	}
	has, err := repo.HasActiveRun(ctx, "terminalwf", owner)
	require.NoError(t, err)
	assert.False(t, has, "terminal-state instances must not count as active")
}

func TestInstanceRepo_HasActiveRun_QueuedWaitingPaused(t *testing.T) {
	repo := NewInstanceRepo(setupTestDB(t).DB)
	ctx := context.Background()
	for _, st := range []string{"queued", "waiting", "paused"} {
		db := setupTestDB(t)
		repo = NewInstanceRepo(db.DB)
		owner := localUserID(t)
		seedNamedInstance(t, db, owner, "activewf", st)
		has, err := repo.HasActiveRun(ctx, "activewf", owner)
		require.NoError(t, err)
		assert.Truef(t, has, "state %q must count as active", st)
	}
}
