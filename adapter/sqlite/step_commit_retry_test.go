package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openStepDB opens (or reopens) a file-backed database the way the server does.
func openStepDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	return db
}

// seedOpenInvocation creates a running instance at node "A" and A's open visit.
func seedOpenInvocation(t *testing.T, db *sql.DB) (run, inv uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	owner, err := NewUserRepo(db).GetDefaultUser(ctx)
	require.NoError(t, err)
	inst := newTestInstance(t, owner)
	start := "A"
	inst.CurrentNodeID = &start
	require.NoError(t, NewInstanceRepo(db).Create(ctx, inst))
	got, err := NewInvocationRepo(db).Begin(ctx, inst.ID, "A")
	require.NoError(t, err)
	return inst.ID, got.ID
}

func failedAttempt(run uuid.UUID, node string, attempt int) *instance.NodeExecution {
	now := time.Now().UTC().Truncate(time.Second)
	return &instance.NodeExecution{ID: uuid.New(), InstanceID: run, NodeID: node, Attempt: attempt,
		Status: instance.NodeStatusFailed, Error: map[string]any{"error": "provider 500"}, StartedAt: now, CompletedAt: &now}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestRetryProgress_SecondAttemptAndDelaySurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db := openStepDB(t, path)
	run, inv := seedOpenInvocation(t, db)
	repo := NewStepCommitRepo(db)

	p, err := repo.LoadRetryProgress(ctx, inv)
	require.NoError(t, err)
	assert.Equal(t, engine.RetryProgress{InvocationID: inv, Attempt: 1}, p, "an open invocation starts at attempt 1")

	retryAt := time.Now().Add(90 * time.Second).UTC().Truncate(time.Second)
	first := failedAttempt(run, "A", 1)
	p, err = repo.CommitRetryFailure(ctx, inv, 1, first, &retryAt)
	require.NoError(t, err)
	assert.Equal(t, 2, p.Attempt)
	require.NoError(t, db.Close()) // the process is gone; only the file survives

	db2 := openStepDB(t, path)
	t.Cleanup(func() { _ = db2.Close() })
	got, err := NewStepCommitRepo(db2).LoadRetryProgress(ctx, inv)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Attempt, "attempt 2 is the live attempt after a restart")
	require.NotNil(t, got.RetryNotBefore)
	assert.True(t, retryAt.Equal(*got.RetryNotBefore), "the retry delay survives: %v vs %v", retryAt, *got.RetryNotBefore)
	assert.False(t, got.Exhausted)
	require.NotNil(t, got.LastExecution)
	assert.Equal(t, first.ID, got.LastExecution.ID)
	assert.Equal(t, instance.NodeStatusFailed, got.LastExecution.Status)
	execs, err := NewNodeExecutionRepo(db2).ListByInstance(ctx, run)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, first.ID, execs[0].ID)
}

func TestRetryProgress_FailureAndCounterCommitAtomically(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	run, inv := seedOpenInvocation(t, db)
	repo := NewStepCommitRepo(db)
	_, err := db.Exec(`CREATE TRIGGER refuse_retry_failure BEFORE INSERT ON node_retry_failures BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)

	at := time.Now().Add(time.Minute)
	x := failedAttempt(run, "A", 1)
	_, err = repo.CommitRetryFailure(ctx, inv, 1, x, &at)
	require.ErrorContains(t, err, "injected")

	assert.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM node_executions WHERE id = ?`, x.ID.String()), "no failed trace without its progress")
	assert.Zero(t, countRows(t, db, `SELECT COUNT(*) FROM node_retry_progress WHERE invocation_id = ?`, inv.String()))
	p, err := repo.LoadRetryProgress(ctx, inv)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Attempt)
}

func TestRetryProgress_ConflictingReplayRejected(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	run, inv := seedOpenInvocation(t, db)
	repo := NewStepCommitRepo(db)
	at := time.Now().Add(time.Minute).UTC().Truncate(time.Second)

	x1 := failedAttempt(run, "A", 1)
	p1, err := repo.CommitRetryFailure(ctx, inv, 1, x1, &at)
	require.NoError(t, err)
	again, err := repo.CommitRetryFailure(ctx, inv, 1, x1, &at)
	require.NoError(t, err, "an identical replay returns the same receipt")
	assert.Equal(t, p1.Attempt, again.Attempt)

	succeeded := failedAttempt(run, "A", 2)
	succeeded.Status = instance.NodeStatusSucceeded
	for name, call := range map[string]func() error{
		"another execution for a journaled attempt": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 1, failedAttempt(run, "A", 1), &at)
			return err
		},
		"the same attempt with a different outcome": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 1, x1, nil)
			return err
		},
		"an attempt ahead of the journal": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 3, failedAttempt(run, "A", 3), &at)
			return err
		},
		"an execution of another node": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 2, failedAttempt(run, "B", 2), &at)
			return err
		},
		"an execution of another run": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 2, failedAttempt(uuid.New(), "A", 2), &at)
			return err
		},
		"a succeeded execution": func() error {
			_, err := repo.CommitRetryFailure(ctx, inv, 2, succeeded, &at)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, call(), engine.ErrRetryProgressConflict)
		})
	}
	assert.Equal(t, 1, countRows(t, db, `SELECT COUNT(*) FROM node_executions WHERE instance_id = ?`, run.String()), "no partial rows on conflict")

	x2 := failedAttempt(run, "A", 2)
	p2, err := repo.CommitRetryFailure(ctx, inv, 2, x2, nil)
	require.NoError(t, err)
	assert.True(t, p2.Exhausted)
	assert.Equal(t, 2, p2.Attempt)
	_, err = repo.CommitRetryFailure(ctx, inv, 2, x2, nil)
	require.NoError(t, err, "replaying the exhausting failure is idempotent")
	_, err = repo.CommitRetryFailure(ctx, inv, 2, failedAttempt(run, "A", 2), nil)
	require.ErrorIs(t, err, engine.ErrRetryProgressConflict, "an exhausted budget accepts nothing new")

	_, err = repo.CommitRetryFailure(ctx, uuid.New(), 1, failedAttempt(run, "A", 1), nil)
	require.ErrorIs(t, err, engine.ErrInvocationNotFound)
}
