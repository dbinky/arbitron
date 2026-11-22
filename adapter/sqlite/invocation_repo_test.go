package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationRepo_BeginReusesOpenAcrossRepoInstances(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	run := seedInstance(t, db, localUserID(t), "running")

	first, err := NewInvocationRepo(db.DB).Begin(ctx, run, "n1")
	require.NoError(t, err)
	assert.Equal(t, 1, first.Visit)
	assert.Equal(t, run, first.RunID)
	assert.Nil(t, first.CommittedAt)

	// A fresh repo over the same DB stands in for a server restart.
	again, err := NewInvocationRepo(db.DB).Begin(ctx, run, "n1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "restart reuses the open invocation")

	var rows int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM node_invocations WHERE run_id = ?`, run.String()).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestInvocationRepo_CommitThenBeginIsNewVisit(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewInvocationRepo(db.DB)
	run := seedInstance(t, db, localUserID(t), "running")

	first, err := repo.Begin(ctx, run, "n1")
	require.NoError(t, err)
	require.NoError(t, repo.Commit(ctx, first.ID))
	require.NoError(t, repo.Commit(ctx, first.ID), "commit is idempotent")

	got, err := repo.Get(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CommittedAt)

	second, err := repo.Begin(ctx, run, "n1")
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, 2, second.Visit)

	other, err := repo.Begin(ctx, run, "n2")
	require.NoError(t, err)
	assert.Equal(t, 1, other.Visit)
}

func TestInvocationRepo_Errors(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewInvocationRepo(db.DB)
	_, err := repo.Get(ctx, uuid.New())
	assert.ErrorIs(t, err, engine.ErrInvocationNotFound)
	assert.ErrorIs(t, repo.Commit(ctx, uuid.New()), engine.ErrInvocationNotFound)
	_, err = repo.Begin(ctx, uuid.Nil, "n1")
	assert.Error(t, err)
	_, err = repo.Begin(ctx, uuid.New(), "n1")
	assert.Error(t, err, "the run must exist (foreign key)")
}

func TestInvocationRepo_SchemaForbidsTwoOpenVisits(t *testing.T) {
	db := setupTestDB(t)
	run := seedInstance(t, db, localUserID(t), "running")
	_, err := db.DB.Exec(`INSERT INTO node_invocations (id, run_id, node_id, visit, created_at) VALUES (?, ?, 'n1', 1, '2026-11-20T00:00:00Z')`, uuid.NewString(), run.String())
	require.NoError(t, err)
	_, err = db.DB.Exec(`INSERT INTO node_invocations (id, run_id, node_id, visit, created_at) VALUES (?, ?, 'n1', 2, '2026-11-20T00:00:00Z')`, uuid.NewString(), run.String())
	assert.Error(t, err, "the partial unique index allows one open visit per (run,node)")
}

var _ engine.InvocationStore = (*InvocationRepo)(nil)
