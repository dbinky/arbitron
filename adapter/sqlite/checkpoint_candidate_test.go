// adapter/sqlite/checkpoint_candidate_test.go
package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ckClaim takes the run's resource claim for job through the P3 T3 lease repo.
func ckClaim(t *testing.T, db *sql.DB, job runner.Job) engine.ResourceFenceRef {
	t.Helper()
	ref, err := NewResourceLeaseRepo(db, "boot-test").ClaimJob(context.Background(), "run:"+job.Key.RunID.String(), runner.JobHolder(job), job.Fence())
	require.NoError(t, err)
	return ref
}

func ckPrepared(run uuid.UUID, version int64, hash string, base runner.CheckpointRef, job runner.Job, res engine.ResourceFenceRef) runner.CheckpointCandidate {
	f := job.Fence()
	return runner.CheckpointCandidate{Ref: runner.CheckpointRef{RunID: run, Version: version, ManifestHash: hash},
		Base: base, Job: &f, Resource: res, ArchivePath: "/ck/" + run.String() + "/v" + hash[:4], SizeBytes: 42}
}

// ckPromote is the test's validated publisher: what P3 T5's transaction does
// to the catalog (promote the exact row and the pointer together).
func ckPromote(t *testing.T, db *sql.DB, ref runner.CheckpointRef) {
	t.Helper()
	tx, err := db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE workspace_checkpoints SET state = 'committed' WHERE run_id = ? AND version = ? AND manifest_hash = ? AND state = 'prepared'`,
		ref.RunID.String(), ref.Version, ref.ManifestHash)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE workspace_homes SET checkpoint_version = ? WHERE run_id = ?`, ref.Version, ref.RunID.String())
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func TestCheckpointCandidate_PreparedIsNotLatest(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "arbitron.db")
	storeDir := t.TempDir()
	db := ckFileDB(t, dbPath)
	store := checkpoint.NewStore(storeDir, checkpoint.WithCatalog(NewCheckpointCatalogRepo(db)))
	run := uuid.New()
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte("baseline"), 0o644))
	base, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)

	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte("step edit"), 0o644))
	prepared, _, err := store.StageBuiltIn(ctx, run, ws, job.Fence(), res)
	require.NoError(t, err)
	assert.Greater(t, prepared.Version, base.Version)

	// Restart: new DB handle, catalog and store over the same files.
	require.NoError(t, db.Close())
	db = ckFileDB(t, dbPath)
	store = checkpoint.NewStore(storeDir, checkpoint.WithCatalog(NewCheckpointCatalogRepo(db)))
	latest, ok, err := store.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, base, latest, "a prepared candidate never becomes the recovery base")
	assert.Equal(t, base.Version, ckPointer(t, db, run))
	dst := t.TempDir()
	require.NoError(t, store.Restore(ctx, latest, dst))
	b, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "baseline", string(b))
	_, err = store.Manifest(ctx, prepared)
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)

	var state, jobID, key, token, baseRef string
	var jobGen, resGen int64
	require.NoError(t, db.QueryRow(`SELECT state, job_id, job_generation, resource_key, resource_generation, resource_token, base_ref
		FROM workspace_checkpoints WHERE run_id = ? AND version = ?`, run.String(), prepared.Version).
		Scan(&state, &jobID, &jobGen, &key, &resGen, &token, &baseRef))
	assert.Equal(t, "prepared", state)
	assert.Equal(t, job.ID.String(), jobID)
	assert.Equal(t, job.Generation, jobGen)
	assert.Equal(t, res.Key, key)
	assert.Equal(t, res.Generation, resGen)
	assert.Equal(t, res.Token.String(), token)
	assert.Equal(t, base.String(), baseRef)

	ckPromote(t, db, prepared)
	latest, _, err = store.Latest(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, prepared, latest, "only the validated transaction moves Latest")
}

func TestCheckpointCandidate_BuiltInHasExactJobAndResourceFence(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)

	missingJob := ckPrepared(run, 2, ckHash("b"), base, job, res)
	missingJob.Job = nil
	require.ErrorIs(t, cat.Publish(ctx, missingJob), runner.ErrInvalidCandidate, "missing job is never a wildcard")
	missingRes := ckPrepared(run, 2, ckHash("b"), base, job, res)
	missingRes.Resource = engine.ResourceFenceRef{}
	require.ErrorIs(t, cat.Publish(ctx, missingRes), runner.ErrInvalidCandidate, "missing resource is never a wildcard")

	wrongRunner := ckPrepared(run, 2, ckHash("b"), base, job, res)
	wrongRunner.Job.RunnerID = "mac-1"
	require.ErrorIs(t, cat.Publish(ctx, wrongRunner), runner.ErrFenced)
	wrongGen := ckPrepared(run, 2, ckHash("b"), base, job, res)
	wrongGen.Job.Generation++
	require.ErrorIs(t, cat.Publish(ctx, wrongGen), runner.ErrFenced)
	unknown := ckPrepared(run, 2, ckHash("b"), base, job, res)
	unknown.Job.JobID = uuid.New()
	require.ErrorIs(t, cat.Publish(ctx, unknown), runner.ErrFenced)

	otherRun := uuid.New()
	otherJob := ckReserveJob(t, db, otherRun)
	otherRes := ckClaim(t, db, otherJob)
	borrowed := ckPrepared(run, 2, ckHash("b"), base, job, otherRes)
	require.ErrorIs(t, cat.Publish(ctx, borrowed), runner.ErrResourceFenced, "another job's claim never authorizes this job")
	assert.Equal(t, 1, ckRows(t, db, run), "no rejected candidate left a row")

	good := ckPrepared(run, 2, ckHash("b"), base, job, res)
	require.NoError(t, cat.Publish(ctx, good))
	var jobID, owner, token string
	var jobGen, resGen int64
	require.NoError(t, db.QueryRow(`SELECT job_id, job_generation, resource_owner, resource_generation, resource_token
		FROM workspace_checkpoints WHERE run_id = ? AND version = 2 AND state = 'prepared'`, run.String()).
		Scan(&jobID, &jobGen, &owner, &resGen, &token))
	assert.Equal(t, []any{job.ID.String(), job.Generation, res.Owner, res.Generation, res.Token.String()},
		[]any{jobID, jobGen, owner, resGen, token})
	assert.Equal(t, int64(1), ckPointer(t, db, run), "preparing never moves the committed pointer")
}

func TestCheckpointCandidate_StaleResourceCannotPrepare(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)

	forged := res
	forged.Token = uuid.New()
	require.ErrorIs(t, cat.Publish(ctx, ckPrepared(run, 2, ckHash("b"), base, job, forged)), runner.ErrResourceFenced)
	otherGen := res
	otherGen.Generation++
	require.ErrorIs(t, cat.Publish(ctx, ckPrepared(run, 2, ckHash("b"), base, job, otherGen)), runner.ErrResourceFenced)

	require.NoError(t, NewResourceLeaseRepo(db, "boot-test").Release(ctx, res))
	require.ErrorIs(t, cat.Publish(ctx, ckPrepared(run, 2, ckHash("b"), base, job, res)), runner.ErrResourceFenced,
		"a released claim cannot prepare")

	job2 := ckReserveJob(t, db, uuid.New())
	_, err = NewRunnerLedgerRepo(db).Requeue(ctx, job2.Fence(), runner.OutcomeRunnerLost)
	require.NoError(t, err)
	require.ErrorIs(t, cat.Publish(ctx, ckPrepared(job2.Key.RunID, 1, ckHash("c"), runner.CheckpointRef{}, job2, res)), runner.ErrFenced,
		"a requeued job's fence is stale")

	assert.Equal(t, 1, ckRows(t, db, run))
	ref, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, base, ref)
}

func TestCheckpointCandidate_ConcurrentVersionAllocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // a self-deadlock fails, not hangs
	defer cancel()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)

	hashes := []string{"b", "c", "d", "e", "f", "0", "1", "2"}
	errs := make([]error, len(hashes))
	var wg sync.WaitGroup
	for i, h := range hashes {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			errs[i] = cat.Publish(ctx, ckPrepared(run, 2, ckHash(h), base, job, res))
		}(i, h)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			require.ErrorIs(t, err, runner.ErrCheckpointConflict)
		}
	}
	assert.Equal(t, 1, won, "exactly one candidate owns a version")

	for i := range hashes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = cat.Publish(ctx, ckPrepared(run, int64(3+i), ckHash(hashes[i]), base, job, res))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1+1+len(hashes), ckRows(t, db, run))
	assert.Equal(t, int64(1), ckPointer(t, db, run))
}

func TestCheckpointCandidate_IdempotentPreparation(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)

	c := ckPrepared(run, 2, ckHash("b"), base, job, res)
	require.NoError(t, cat.Publish(ctx, c))
	require.NoError(t, cat.Publish(ctx, c), "an identical retried registration returns the same candidate")
	assert.Equal(t, 2, ckRows(t, db, run))

	otherHash := c
	otherHash.Ref.ManifestHash = ckHash("c")
	require.ErrorIs(t, cat.Publish(ctx, otherHash), runner.ErrCheckpointConflict)
	otherPath := c
	otherPath.ArchivePath = "/elsewhere"
	require.ErrorIs(t, cat.Publish(ctx, otherPath), runner.ErrCheckpointConflict)

	zeroBase := ckPrepared(run, 3, ckHash("d"), runner.CheckpointRef{}, job, res)
	require.ErrorIs(t, cat.Publish(ctx, zeroBase), runner.ErrCheckpointConflict, "the base must be the committed pointer")
	ckPromote(t, db, c.Ref)
	stale := ckPrepared(run, 3, ckHash("d"), base, job, res)
	require.ErrorIs(t, cat.Publish(ctx, stale), runner.ErrCheckpointConflict, "a candidate built on a superseded base is refused")
	require.NoError(t, cat.Publish(ctx, ckPrepared(run, 3, ckHash("d"), c.Ref, job, res)))

	fresh := uuid.New()
	freshJob := ckReserveJob(t, db, fresh)
	freshRes := ckClaim(t, db, freshJob)
	require.NoError(t, cat.Publish(ctx, ckPrepared(fresh, 1, ckHash("e"), runner.CheckpointRef{}, freshJob, freshRes)),
		"a run with no committed catalog checkpoint (non-portable until now) prepares on the zero base")
}

func TestCheckpointCandidate_PreparedForFindsExactCandidateAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "arbitron.db")
	db := ckFileDB(t, dbPath)
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)
	c := ckPrepared(run, 2, ckHash("b"), base, job, res)
	require.NoError(t, cat.Publish(ctx, c))

	// Restart: a new handle and repo over the same file (adopted remote job).
	require.NoError(t, db.Close())
	db = ckFileDB(t, dbPath)
	cat = NewCheckpointCatalogRepo(db)
	got, ok, err := cat.PreparedFor(ctx, run, job.Fence(), res)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, c.Ref, got, "the exact candidate of this job and claim")
}

func TestCheckpointCandidate_PreparedForIgnoresOtherFencesAndCommittedRows(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)
	c := ckPrepared(run, 2, ckHash("b"), base, job, res)
	require.NoError(t, cat.Publish(ctx, c))

	otherGen := job.Fence()
	otherGen.Generation++
	otherRunner := job.Fence()
	otherRunner.RunnerID = "mac-1"
	otherToken := res
	otherToken.Token = uuid.New()
	otherResGen := res
	otherResGen.Generation++
	for name, q := range map[string]struct {
		job runner.Fence
		res engine.ResourceFenceRef
	}{
		"other job generation":      {otherGen, res},
		"other job runner":          {otherRunner, res},
		"other job":                 {runner.Fence{JobID: uuid.New(), RunnerID: job.RunnerID, Generation: job.Generation}, res},
		"other resource token":      {job.Fence(), otherToken},
		"other resource generation": {job.Fence(), otherResGen},
	} {
		_, ok, err := cat.PreparedFor(ctx, run, q.job, q.res)
		require.NoError(t, err, name)
		assert.False(t, ok, name)
	}
	_, ok, err := cat.PreparedFor(ctx, uuid.New(), job.Fence(), res)
	require.NoError(t, err)
	assert.False(t, ok, "another run")

	ckPromote(t, db, c.Ref)
	_, ok, err = cat.PreparedFor(ctx, run, job.Fence(), res)
	require.NoError(t, err)
	assert.False(t, ok, "a committed row is no longer a prepared candidate")
}

func TestCheckpointCandidate_PreparedForRefusesToGuessBetweenCandidates(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	base, _, _, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)
	require.NoError(t, cat.Publish(ctx, ckPrepared(run, 2, ckHash("b"), base, job, res)))
	require.NoError(t, cat.Publish(ctx, ckPrepared(run, 3, ckHash("c"), base, job, res)))
	_, ok, err := cat.PreparedFor(ctx, run, job.Fence(), res)
	require.ErrorIs(t, err, runner.ErrCheckpointConflict, "two candidates of one operation: never pick the newest")
	assert.False(t, ok)
}
