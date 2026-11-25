package sqlite

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Decision D6: a reconnect hello that omits a ledger-live job is durable stop
// evidence for that exact job generation, and only for it.
func TestResourceLease_SupersedeReconnectAbsentNeedsTheAbsenceReport(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	inbox := NewRemoteInboxRepo(f.db)
	other := leaseJob(t, f.db, f.job.Key.RunID, "mac-1", "running")
	require.NoError(t, inbox.RecordAbsent(f.ctx, other.Fence()), "another job's report is not evidence for this claim")
	f.fenceOldJob(t)

	_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeReconnectAbsent), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "no absence report for this job generation")
	f.requireUnchanged(t)
}

func TestResourceLease_SupersedeReconnectAbsentTakesOverTheReportedJob(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	inbox := NewRemoteInboxRepo(f.db)
	require.NoError(t, inbox.RecordAbsent(f.ctx, f.job.Fence()), "reported while the job is still ledger-live")
	require.NoError(t, inbox.RecordAbsent(f.ctx, f.job.Fence()), "a repeated report is idempotent")

	_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeReconnectAbsent), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "the job must be fenced first")
	f.requireUnchanged(t)

	f.fenceOldJob(t)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeReconnectAbsent), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeReconnectAbsent, runner.BuiltInRunnerID)
}

func TestRemoteInbox_RecordAbsentOnlyForALiveFence(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	inbox := NewRemoteInboxRepo(f.db)
	stale := f.job.Fence()
	stale.Generation++
	require.ErrorIs(t, inbox.RecordAbsent(f.ctx, stale), runner.ErrFenced, "a generation the ledger never issued")
	wrong := f.job.Fence()
	wrong.RunnerID = "mac-2"
	require.ErrorIs(t, inbox.RecordAbsent(f.ctx, wrong), runner.ErrFenced, "another runner cannot report this job")
	zero := f.job.Fence()
	zero.Generation = 0
	require.ErrorIs(t, inbox.RecordAbsent(f.ctx, zero), runner.ErrMalformedMessage)
	f.fenceOldJob(t)
	require.ErrorIs(t, inbox.RecordAbsent(f.ctx, f.job.Fence()), runner.ErrFenced, "a fenced job gains no evidence after the fact")
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM runner_job_absences`).Scan(&n))
	assert.Zero(t, n)
}

// Edge: the kind is remote-only; a built-in claim never accepts it even when
// a row names the job.
func TestResourceLease_SupersedeReconnectAbsentNeverAppliesToBuiltIn(t *testing.T) {
	f := newSupersedeFixture(t, runner.BuiltInRunnerID)
	_, err := f.db.Exec(`INSERT INTO runner_job_absences(job_id, generation, runner_id, reported_at) VALUES (?, ?, ?, '2026-11-25T00:00:00Z')`,
		f.job.ID.String(), f.job.Generation, string(runner.BuiltInRunnerID))
	require.NoError(t, err)
	f.fenceOldJob(t)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeReconnectAbsent), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven)
	f.requireUnchanged(t)
}

// Exercise the real schema upgrade with a populated pre-047 receipt table.
func TestMigration047_PreservesPopulatedReceiptTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	before, err := migrationsFS.ReadFile("migrations/044_workspace_mobility.up.sql")
	require.NoError(t, err)
	_, schema, found := strings.Cut(string(before), "CREATE TABLE workspace_lease_supersessions (")
	require.True(t, found)
	_, err = db.Exec("CREATE TABLE workspace_lease_supersessions (" + schema)
	require.NoError(t, err)
	const insert = `INSERT INTO workspace_lease_supersessions VALUES ('run:x', 1, 'token', 2, ?, 'job-1', 7, '2026-11-25T00:00:00Z')`
	_, err = db.Exec(insert, "stopped")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO workspace_lease_supersessions VALUES ('run:y', 3, 'other', 4, 'reconnect_absent', 'job-2', 8, 't')`)
	require.Error(t, err, "the old CHECK refuses D6")
	upgrade, err := migrationsFS.ReadFile("migrations/047_runner_job_absences.up.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(upgrade))
	require.NoError(t, err)
	var key, token, kind, job, at string
	var oldGen, newGen, jobGen int
	require.NoError(t, db.QueryRow(`SELECT * FROM workspace_lease_supersessions`).Scan(&key, &oldGen, &token, &newGen, &kind, &job, &jobGen, &at))
	assert.Equal(t, []any{"run:x", 1, "token", 2, "stopped", "job-1", 7, "2026-11-25T00:00:00Z"}, []any{key, oldGen, token, newGen, kind, job, jobGen, at})
	_, err = db.Exec(`INSERT INTO workspace_lease_supersessions VALUES ('run:y', 3, 'other', 4, 'reconnect_absent', 'job-2', 8, 't')`)
	require.NoError(t, err, "the upgraded CHECK admits D6")
}

// Migration 047 widens the receipt kinds without losing earlier receipts.
func TestMigration047_KeepsSupersessionReceipts(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	f.fenceOldJob(t)
	_, err := f.db.Exec(`UPDATE runners SET liveness = 'lost' WHERE id = 'mac-1'`)
	require.NoError(t, err)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeRunnerLost, runner.BuiltInRunnerID)
	_, err = f.db.Exec(`INSERT INTO workspace_lease_supersessions(resource_key, old_generation, old_token, new_generation, kind, superseded_at)
		VALUES ('run:x', 1, 't', 2, 'stale_guess', '2026-11-25T00:00:00Z')`)
	require.Error(t, err, "unknown kinds are still refused")
}
