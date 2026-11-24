package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leaseJob writes a real runner_jobs row in state on runnerID (the P1 ledger
// table fenceErr reads) and returns it with its exact fence.
func leaseJob(t *testing.T, db *sql.DB, run uuid.UUID, runnerID runner.RunnerID, state string) runner.Job {
	t.Helper()
	id := uuid.New()
	_, err := db.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind, runner_id, generation, state, created_at, updated_at)
		VALUES (?, ?, ?, 'node', 1, 'n', 'process', 'none', ?, 1, ?, '2026-11-23T00:00:00Z', '2026-11-23T00:00:00Z')`,
		id.String(), run.String(), uuid.NewString(), string(runnerID), state)
	require.NoError(t, err)
	j, err := getJob(context.Background(), db, id)
	require.NoError(t, err)
	return j
}

// leaseFileDBs opens two independent handles (two connections) on one
// file-backed database, as two server paths would.
func leaseFileDBs(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	t.Cleanup(func() { db.Close() })
	other, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { other.Close() })
	return db, other
}

func activeLeases(t *testing.T, db *sql.DB, key string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1`, key).Scan(&n))
	return n
}

func TestResourceLease_SameRunnerWritersCannotOverlap(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	leases := NewResourceLeaseRepo(db, "boot-1")
	run := uuid.New()
	key := "run:" + run.String()
	a := leaseJob(t, db, run, runner.BuiltInRunnerID, "running")
	b := leaseJob(t, db, run, runner.BuiltInRunnerID, "running")

	refA, err := leases.ClaimJob(ctx, key, runner.JobHolder(a), a.Fence())
	require.NoError(t, err)
	assert.Equal(t, engine.ResourceFenceRef{Key: key, Owner: "builtin", Generation: 1, Token: refA.Token}, refA)
	_, err = leases.ClaimJob(ctx, key, runner.JobHolder(b), b.Fence())
	require.ErrorIs(t, err, runner.ErrResourceBusy, "a second job on the SAME runner never overlaps")
	_, err = leases.Claim(ctx, key, "group:mutation-1", runner.BuiltInRunnerID)
	require.ErrorIs(t, err, runner.ErrResourceBusy, "a server-side holder never overlaps either")
	require.NoError(t, leases.Check(ctx, refA))

	rec, ok, err := leases.Active(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, runner.JobHolder(a), rec.Holder)
	require.NotNil(t, rec.Job)
	assert.Equal(t, a.Fence(), *rec.Job)
	assert.Empty(t, rec.BootID)

	require.NoError(t, leases.Release(ctx, refA))
	refB, err := leases.ClaimJob(ctx, key, runner.JobHolder(b), b.Fence())
	require.NoError(t, err)
	assert.Equal(t, int64(2), refB.Generation, "each new claim bumps the generation")
	assert.NotEqual(t, refA.Token, refB.Token)
	require.ErrorIs(t, leases.Check(ctx, refA), runner.ErrResourceFenced)
}

func TestResourceLease_ClaimJobAdoptsOnlyTheExactSameJob(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	leases := NewResourceLeaseRepo(db, "boot-1")
	run := uuid.New()
	key := "run:" + run.String()
	a := leaseJob(t, db, run, runner.BuiltInRunnerID, "offered")

	ref, err := leases.ClaimJob(ctx, key, runner.JobHolder(a), a.Fence())
	require.NoError(t, err)
	again, err := leases.ClaimJob(ctx, key, runner.JobHolder(a), a.Fence())
	require.NoError(t, err)
	assert.Equal(t, ref, again, "an idempotent retry adopts the existing claim")
	_, err = leases.ClaimJob(ctx, key, "job:someone-else", a.Fence())
	require.ErrorIs(t, err, runner.ErrResourceBusy, "the same fence under another holder is not the same operation")

	stale := a.Fence()
	stale.Generation++
	_, err = leases.ClaimJob(ctx, key, runner.JobHolder(a), stale)
	require.ErrorIs(t, err, runner.ErrFenced)

	done := leaseJob(t, db, uuid.New(), runner.BuiltInRunnerID, "completed")
	_, err = leases.ClaimJob(ctx, "run:"+done.Key.RunID.String(), runner.JobHolder(done), done.Fence())
	require.ErrorIs(t, err, runner.ErrFenced, "a terminal job cannot claim")
	_, err = leases.ClaimJob(ctx, "run:x", "job:ghost", runner.Fence{JobID: uuid.New(), RunnerID: "builtin", Generation: 1})
	require.ErrorIs(t, err, runner.ErrFenced, "an unknown job cannot claim")
	assert.Zero(t, activeLeases(t, db, "run:x"))
}

func TestResourceLease_ConcurrentClaimHasOneWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, other := leaseFileDBs(t)
	run := uuid.New()
	key := "run:" + run.String()
	const n = 12
	jobs := make([]runner.Job, n)
	for i := range jobs {
		jobs[i] = leaseJob(t, db, run, runner.BuiltInRunnerID, "running")
	}
	refs := make([]engine.ResourceFenceRef, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handle := db
			if i%2 == 1 {
				handle = other // half the claimants race through a second connection
			}
			refs[i], errs[i] = NewResourceLeaseRepo(handle, "boot-1").ClaimJob(ctx, key, runner.JobHolder(jobs[i]), jobs[i].Fence())
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := range errs {
		if errs[i] == nil {
			winners++
			require.NoError(t, NewResourceLeaseRepo(db, "boot-1").Check(ctx, refs[i]))
		} else {
			require.ErrorIs(t, errs[i], runner.ErrResourceBusy)
		}
	}
	assert.Equal(t, 1, winners)
	assert.Equal(t, 1, activeLeases(t, db, key))
	var holders int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workspace_lease_holders WHERE resource_key = ?`, key).Scan(&holders))
	assert.Equal(t, 1, holders, "losers leave no holder rows")
}

func TestResourceLease_StaleReleaseCannotClearNewOwner(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	leases := NewResourceLeaseRepo(db, "boot-1")
	key := "run:" + uuid.NewString()
	first, err := leases.Claim(ctx, key, "group:op-1", runner.BuiltInRunnerID)
	require.NoError(t, err)
	require.NoError(t, leases.Release(ctx, first))
	second, err := leases.Claim(ctx, key, "group:op-2", runner.BuiltInRunnerID)
	require.NoError(t, err)

	require.ErrorIs(t, leases.Release(ctx, first), runner.ErrResourceFenced, "the old generation cannot release the new owner")
	forged := second
	forged.Token = uuid.New()
	require.ErrorIs(t, leases.Release(ctx, forged), runner.ErrResourceFenced)
	wrongOwner := second
	wrongOwner.Owner = "mac-1"
	require.ErrorIs(t, leases.Release(ctx, wrongOwner), runner.ErrResourceFenced)
	require.ErrorIs(t, leases.Release(ctx, engine.ResourceFenceRef{Key: key}), runner.ErrResourceFenced, "incomplete evidence")
	require.NoError(t, leases.Check(ctx, second), "the new owner still holds it")
	rec, ok, err := leases.Active(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "group:op-2", rec.Holder)
	assert.Equal(t, "boot-1", rec.BootID)
	assert.Nil(t, rec.Job)
	require.NoError(t, leases.Release(ctx, second))
	require.ErrorIs(t, leases.Release(ctx, second), runner.ErrResourceFenced, "release is exact and single")
	_, ok, err = leases.Active(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestResourceLease_StorageErrorNeverGrantsOwnership(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	leases := NewResourceLeaseRepo(db, "boot-1")
	key := "run:" + uuid.NewString()
	_, err := db.Exec(`CREATE TRIGGER fail_holder BEFORE INSERT ON workspace_lease_holders BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`)
	require.NoError(t, err)

	ref, err := leases.Claim(ctx, key, "group:op-1", runner.BuiltInRunnerID)
	require.Error(t, err)
	assert.Equal(t, engine.ResourceFenceRef{}, ref, "no reference is handed out")
	assert.Zero(t, activeLeases(t, db, key), "the half-written claim rolled back")
	_, ok, err := leases.Active(ctx, key)
	require.NoError(t, err)
	assert.False(t, ok)

	_, err = db.Exec(`DROP TRIGGER fail_holder`)
	require.NoError(t, err)
	ref, err = leases.Claim(ctx, key, "group:op-1", runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ref.Generation, "the failed attempt consumed no generation")

	_, err = NewResourceLeaseRepo(db, "").Claim(ctx, "run:other", "group:op", runner.BuiltInRunnerID)
	require.Error(t, err, "a server-side claim without a boot ID is refused")
	require.NoError(t, db.Close())
	_, err = leases.Claim(ctx, "run:closed", "group:op", runner.BuiltInRunnerID)
	require.Error(t, err)
	require.Error(t, leases.Check(ctx, ref), "an unreadable store never confirms ownership")
}

func TestResourceLease_TransactionHelpersUseTheCallersTx(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // self-deadlock fails, not hangs
	defer cancel()
	db := setupTestDB(t).DB
	leases := NewResourceLeaseRepo(db, "boot-1")
	key := "run:" + uuid.NewString()
	ref, err := leases.Claim(ctx, key, "group:op-1", runner.BuiltInRunnerID)
	require.NoError(t, err)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, checkResourceFenceTx(ctx, tx, ref))
	require.NoError(t, releaseResourceTx(ctx, tx, ref))
	require.ErrorIs(t, checkResourceFenceTx(ctx, tx, ref), runner.ErrResourceFenced, "visible inside the same tx")
	require.NoError(t, tx.Rollback())
	require.NoError(t, leases.Check(ctx, ref), "a rolled-back release changes nothing")
}
