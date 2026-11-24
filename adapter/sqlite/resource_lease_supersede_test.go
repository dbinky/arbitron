package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type supersedeFixture struct {
	ctx    context.Context
	db     *sql.DB
	leases *ResourceLeaseRepo
	ledger *RunnerLedgerRepo
	reg    *RunnerRegistryRepo
	key    string
	job    runner.Job
	ref    engine.ResourceFenceRef
}

// newSupersedeFixture registers runnerID, gives it a running job on a fresh run
// and lets that job claim the run's resource.
func newSupersedeFixture(t *testing.T, runnerID runner.RunnerID) supersedeFixture {
	t.Helper()
	ctx := context.Background()
	db := setupTestDB(t).DB
	reg := NewRunnerRegistryRepo(db)
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: runnerID, Name: string(runnerID), BuiltIn: runnerID == runner.BuiltInRunnerID,
		StepLimit: 8, Liveness: runner.LivenessOnline}))
	run := uuid.New()
	job := leaseJob(t, db, run, runnerID, "running")
	leases := NewResourceLeaseRepo(db, "boot-1")
	key := "run:" + run.String()
	ref, err := leases.ClaimJob(ctx, key, runner.JobHolder(job), job.Fence())
	require.NoError(t, err)
	return supersedeFixture{ctx: ctx, db: db, leases: leases, ledger: NewRunnerLedgerRepo(db), reg: reg, key: key, job: job, ref: ref}
}

func (f supersedeFixture) proof(kind runner.SupersedeKind) runner.SupersedeProof {
	fence := f.job.Fence()
	return runner.SupersedeProof{Kind: kind, OldJob: &fence}
}

// fenceOldJob is the caller's first recovery step (P3 T10): the ledger no
// longer authorizes the old job fence.
func (f supersedeFixture) fenceOldJob(t *testing.T) {
	t.Helper()
	require.NoError(t, f.ledger.Fail(f.ctx, f.job.Fence(), runner.OutcomeRunnerLost))
}

func (f supersedeFixture) receipts(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM workspace_lease_supersessions WHERE resource_key = ?`, f.key).Scan(&n))
	return n
}

// requireUnchanged asserts a refused Supersede left the old claim in force.
func (f supersedeFixture) requireUnchanged(t *testing.T) {
	t.Helper()
	require.NoError(t, f.leases.Check(f.ctx, f.ref), "the old claim still holds")
	assert.Zero(t, f.receipts(t))
	var holders int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM workspace_lease_holders WHERE resource_key = ?`, f.key).Scan(&holders))
	assert.Equal(t, 1, holders, "no new holder recorded")
}

// requireSuperseded asserts the takeover receipt, the new server-side holder
// and that the old reference is fenced.
func (f supersedeFixture) requireSuperseded(t *testing.T, next engine.ResourceFenceRef, kind runner.SupersedeKind, owner runner.RunnerID) {
	t.Helper()
	assert.Equal(t, f.ref.Generation+1, next.Generation)
	assert.Equal(t, string(owner), next.Owner)
	assert.NotEqual(t, f.ref.Token, next.Token)
	require.ErrorIs(t, f.leases.Check(f.ctx, f.ref), runner.ErrResourceFenced, "the old writer is fenced")
	require.NoError(t, f.leases.Check(f.ctx, next))
	var gotKind, oldToken string
	var oldGen, newGen int64
	var jobID sql.NullString
	require.NoError(t, f.db.QueryRow(`SELECT kind, old_generation, old_token, new_generation, job_id FROM workspace_lease_supersessions WHERE resource_key = ?`, f.key).
		Scan(&gotKind, &oldGen, &oldToken, &newGen, &jobID))
	assert.Equal(t, []any{string(kind), f.ref.Generation, f.ref.Token.String(), next.Generation}, []any{gotKind, oldGen, oldToken, newGen})
	if f.job.ID != uuid.Nil && jobID.Valid {
		assert.Equal(t, f.job.ID.String(), jobID.String)
	}
	rec, ok, err := f.leases.Active(f.ctx, f.key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "recovery:restore", rec.Holder)
	assert.Nil(t, rec.Job, "the recovery holder is server-side")
	assert.Equal(t, "boot-1", rec.BootID)
}

func TestResourceLease_SuspectDoesNotAuthorizeSupersede(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	f.fenceOldJob(t)
	for _, l := range []runner.Liveness{runner.LivenessSuspect, runner.LivenessOffline, runner.LivenessOnline} {
		require.NoError(t, f.reg.SetLiveness(f.ctx, "mac-1", l, time.Now()))
		for _, kind := range []runner.SupersedeKind{runner.SupersedeRunnerLost, runner.SupersedeRevoked, runner.SupersedeStopped, runner.SupersedeServerRestart, "suspect"} {
			_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(kind), "recovery:restore", runner.BuiltInRunnerID)
			require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "%s while %s", kind, l)
		}
	}
	f.requireUnchanged(t)

	require.NoError(t, f.reg.SetLiveness(f.ctx, "mac-1", runner.LivenessLost, time.Now()))
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err, "only confirmed loss does")
	f.requireSuperseded(t, next, runner.SupersedeRunnerLost, runner.BuiltInRunnerID)
}

func TestResourceLease_SupersedeRequiresTheOldJobFenced(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	require.NoError(t, f.reg.SetLiveness(f.ctx, "mac-1", runner.LivenessLost, time.Now()))
	_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "the ledger still authorizes the old job")
	f.requireUnchanged(t)
	f.fenceOldJob(t)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeRunnerLost, runner.BuiltInRunnerID)
}

func TestResourceLease_SupersedeRejectsAWrongOldJob(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	require.NoError(t, f.reg.SetLiveness(f.ctx, "mac-1", runner.LivenessLost, time.Now()))
	f.fenceOldJob(t)
	wrongGen := f.job.Fence()
	wrongGen.Generation++
	other := runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 1}
	for name, p := range map[string]runner.SupersedeProof{
		"nil job":          {Kind: runner.SupersedeRunnerLost},
		"other generation": {Kind: runner.SupersedeRunnerLost, OldJob: &wrongGen},
		"other job":        {Kind: runner.SupersedeRunnerLost, OldJob: &other},
	} {
		_, err := f.leases.Supersede(f.ctx, f.ref, p, "recovery:restore", runner.BuiltInRunnerID)
		require.ErrorIs(t, err, runner.ErrSupersedeUnproven, name)
	}
	f.requireUnchanged(t)
}

func TestResourceLease_SupersedeOnRevokedKey(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	f.fenceOldJob(t)
	_, err := f.db.Exec(`INSERT INTO runner_keys(id, name, runner_id, key_hash, prefix, created_at) VALUES (?, 'mac-1-key', 'mac-1', 'hash-1', 'arb_', '2026-11-23T00:00:00Z')`, uuid.NewString())
	require.NoError(t, err)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRevoked), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "an unrevoked key proves nothing")
	f.requireUnchanged(t)
	_, err = f.db.Exec(`UPDATE runner_keys SET revoked_at = '2026-11-23T01:00:00Z' WHERE runner_id = 'mac-1'`)
	require.NoError(t, err)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRevoked), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeRevoked, runner.BuiltInRunnerID)
}

func TestResourceLease_SupersedeBuiltInNeedsConfirmedStop(t *testing.T) {
	f := newSupersedeFixture(t, runner.BuiltInRunnerID)
	f.fenceOldJob(t)
	for _, kind := range []runner.SupersedeKind{runner.SupersedeRunnerLost, runner.SupersedeRevoked, runner.SupersedeServerRestart} {
		_, err := f.leases.Supersede(f.ctx, f.ref, f.proof(kind), "recovery:restore", runner.BuiltInRunnerID)
		require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "%s never applies to a built-in job claim", kind)
	}
	_, err := f.db.Exec(`INSERT INTO builtin_processes(job_id, generation, guardian_pid, process_start_marker, state) VALUES (?, ?, 4242, 'marker', 'running')`,
		f.job.ID.String(), f.job.Generation)
	require.NoError(t, err)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "a process that may still write blocks takeover")
	f.requireUnchanged(t)
	_, err = f.db.Exec(`UPDATE builtin_processes SET state = 'stopped' WHERE job_id = ?`, f.job.ID.String())
	require.NoError(t, err)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeStopped, runner.BuiltInRunnerID)
}

func TestResourceLease_SupersedeRemoteNeedsReceivedResult(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	f.fenceOldJob(t)
	_, err := f.db.Exec(`INSERT INTO runner_inbox_cursors(job_id, generation, runner_id, result_received) VALUES (?, ?, 'mac-1', 0)`, f.job.ID.String(), f.job.Generation)
	require.NoError(t, err)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "no terminal result received yet")
	f.requireUnchanged(t)
	_, err = f.db.Exec(`UPDATE runner_inbox_cursors SET result_received = 1 WHERE job_id = ?`, f.job.ID.String())
	require.NoError(t, err)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeStopped), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeStopped, runner.BuiltInRunnerID)
}

func TestResourceLease_SupersedeServerRestartNeedsAnEarlierBoot(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	key := "run:" + uuid.NewString()
	before := NewResourceLeaseRepo(db, "boot-1")
	ref, err := before.Claim(ctx, key, "group:op-1", runner.BuiltInRunnerID)
	require.NoError(t, err)
	_, err = before.Supersede(ctx, ref, runner.SupersedeProof{Kind: runner.SupersedeServerRestart}, "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "the same boot is still running that holder")
	job := runner.Fence{JobID: uuid.New(), RunnerID: "builtin", Generation: 1}
	after := NewResourceLeaseRepo(db, "boot-2")
	_, err = after.Supersede(ctx, ref, runner.SupersedeProof{Kind: runner.SupersedeServerRestart, OldJob: &job}, "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven, "a job-less claim takes no job proof")
	next, err := after.Supersede(ctx, ref, runner.SupersedeProof{Kind: runner.SupersedeServerRestart}, "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), next.Generation)
	require.ErrorIs(t, after.Check(ctx, ref), runner.ErrResourceFenced)
	rec, ok, err := after.Active(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "boot-2", rec.BootID, "the new holder belongs to the current boot")
	var kind string
	require.NoError(t, db.QueryRow(`SELECT kind FROM workspace_lease_supersessions WHERE resource_key = ?`, key).Scan(&kind))
	assert.Equal(t, "server_restart", kind)
}

func TestResourceLease_SupersedeFailureChangesNothing(t *testing.T) {
	f := newSupersedeFixture(t, "mac-1")
	require.NoError(t, f.reg.SetLiveness(f.ctx, "mac-1", runner.LivenessLost, time.Now()))
	f.fenceOldJob(t)
	stale := f.ref
	stale.Token = uuid.New()
	_, err := f.leases.Supersede(f.ctx, stale, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrResourceFenced, "only the exact active claim can be superseded")
	_, err = f.db.Exec(`CREATE TRIGGER fail_receipt BEFORE INSERT ON workspace_lease_supersessions BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	require.NoError(t, err)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.Error(t, err)
	f.requireUnchanged(t)
	_, err = f.db.Exec(`DROP TRIGGER fail_receipt`)
	require.NoError(t, err)
	next, err := f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.NoError(t, err)
	f.requireSuperseded(t, next, runner.SupersedeRunnerLost, runner.BuiltInRunnerID)
	_, err = f.leases.Supersede(f.ctx, f.ref, f.proof(runner.SupersedeRunnerLost), "recovery:restore", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrResourceFenced, "a superseded claim cannot be superseded twice")
}
