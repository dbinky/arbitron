package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// processRow reads the raw 044 row; state "" means no row.
func processRow(t *testing.T, db *sql.DB, f runner.Fence) (pid int, marker, state string) {
	t.Helper()
	err := db.QueryRow(`SELECT guardian_pid, process_start_marker, state FROM builtin_processes WHERE job_id = ? AND generation = ?`,
		f.JobID.String(), f.Generation).Scan(&pid, &marker, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ""
	}
	require.NoError(t, err)
	return pid, marker, state
}

func TestBuiltInProcessRepo_PermitRecordStopLifecycle(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	repo := NewBuiltInProcessRepo(db)
	f := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "running").Fence()

	require.NoError(t, repo.Permit(ctx, f))
	pid, marker, state := processRow(t, db, f)
	assert.Equal(t, []any{0, "", "permitted"}, []any{pid, marker, state}, "the permit is durable before any guardian exists")

	require.NoError(t, repo.Record(ctx, f, 4242, "darwin:1.000002"))
	pid, marker, state = processRow(t, db, f)
	assert.Equal(t, []any{4242, "darwin:1.000002", "running"}, []any{pid, marker, state})
	got, ok, err := repo.Get(ctx, f)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, runner.BuiltInProcess{Fence: f, GuardianPID: 4242, StartMarker: "darwin:1.000002", State: runner.ProcessRunning}, got)

	require.NoError(t, repo.MarkStopped(ctx, f, runner.ProcessRunning))
	_, _, state = processRow(t, db, f)
	assert.Equal(t, "stopped", state, "exactly the string ResourceLeaseRepo.Supersede(kind stopped) reads")
}

func TestBuiltInProcessRepo_PermitRequiresALiveBuiltInFence(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	repo := NewBuiltInProcessRepo(db)
	failed := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "failed").Fence()
	remote := leaseJob(t, db, uuid.New(), "mac-1", "running").Fence()
	stale := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "running").Fence()
	stale.Generation = 2

	for name, f := range map[string]runner.Fence{"fenced job": failed, "remote runner": remote, "stale generation": stale, "zero fence": {}} {
		require.ErrorIs(t, repo.Permit(ctx, f), runner.ErrFenced, name)
	}
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM builtin_processes`).Scan(&n))
	assert.Zero(t, n, "no permit is written for a fence the ledger does not authorize")
}

func TestBuiltInProcessRepo_OneLiveProcessPerJobGeneration(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	repo := NewBuiltInProcessRepo(db)
	f := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "running").Fence()

	require.NoError(t, repo.Permit(ctx, f))
	require.ErrorIs(t, repo.Permit(ctx, f), runner.ErrProcessActive)
	require.NoError(t, repo.Record(ctx, f, 4242, "m"))
	require.ErrorIs(t, repo.Permit(ctx, f), runner.ErrProcessActive)
	require.NoError(t, repo.MarkStopped(ctx, f, runner.ProcessRunning))
	require.NoError(t, repo.Permit(ctx, f), "a later sequential launch of the same live job reopens a confirmed-stopped row")
	pid, marker, state := processRow(t, db, f)
	assert.Equal(t, []any{0, "", "permitted"}, []any{pid, marker, state})
}

func TestBuiltInProcessRepo_StaleTransitionsAreRejected(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	repo := NewBuiltInProcessRepo(db)
	f := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "running").Fence()

	require.ErrorIs(t, repo.Record(ctx, f, 4242, "m"), runner.ErrProcessStateChanged, "no guardian is recorded without a permit")
	require.NoError(t, repo.Permit(ctx, f))
	require.ErrorIs(t, repo.MarkStopped(ctx, f, runner.ProcessRunning), runner.ErrProcessStateChanged)
	_, _, state := processRow(t, db, f)
	assert.Equal(t, "permitted", state)
	require.ErrorIs(t, repo.MarkStopped(ctx, f, runner.ProcessAbsent), runner.ErrProcessStateChanged, "an existing row is never blindly overwritten")
	require.Error(t, repo.Record(ctx, f, 1, "m"), "pid 1 (and the kill(-1) broadcast) is never a guardian")
	require.Error(t, repo.Record(ctx, f, 4242, ""), "a guardian needs a start marker")

	never := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "failed").Fence()
	require.NoError(t, repo.MarkStopped(ctx, never, runner.ProcessAbsent))
	pid, marker, state := processRow(t, db, never)
	assert.Equal(t, []any{0, "", "stopped"}, []any{pid, marker, state}, "a job that never launched is durably stopped")
}

func TestBuiltInProcessRepo_StoppedRowIsTheSupersedeEvidence(t *testing.T) {
	f := newSupersedeFixture(t, runner.BuiltInRunnerID)
	repo := NewBuiltInProcessRepo(f.db)
	fence := f.job.Fence()
	require.NoError(t, repo.Permit(f.ctx, fence))
	require.NoError(t, repo.Record(f.ctx, fence, 4242, "m"))
	f.fenceOldJob(t)

	_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "a running guardian blocks takeover")
	f.requireUnchanged(t)
	require.ErrorIs(t, repo.Permit(f.ctx, fence), runner.ErrFenced, "a fenced job can never be re-permitted")

	require.NoError(t, repo.MarkStopped(f.ctx, fence, runner.ProcessRunning))
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeStopped, runner.BuiltInRunnerID)
}
