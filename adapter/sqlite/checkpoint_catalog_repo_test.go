// adapter/sqlite/checkpoint_catalog_repo_test.go
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ckHash(c string) string { return strings.Repeat(c, 64) }

// ckFileDB opens a file-backed, migrated database so tests can close and
// reopen it (restart).
func ckFileDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	t.Cleanup(func() { db.Close() })
	return db
}

// ckReserveJob creates a real ledger job for run reserved on the built-in
// runner through the P1 ledger, returning it with its exact fence.
func ckReserveJob(t *testing.T, db *sql.DB, run uuid.UUID) runner.Job {
	t.Helper()
	ctx := context.Background()
	reg := NewRunnerRegistryRepo(db)
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: 64, Liveness: runner.LivenessOnline}))
	src := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "")
	require.NoError(t, reg.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{{ID: src, RunnerID: runner.BuiltInRunnerID,
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{"claude-code"},
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}}))
	led := NewRunnerLedgerRepo(db)
	key := runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	job, err := led.EnsureIntent(ctx, key, "n", anthropicRequirements(run))
	require.NoError(t, err)
	job, err = led.Reserve(ctx, job.ID, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Source: src, Reason: "test"}, 64, time.Now().Add(time.Hour))
	require.NoError(t, err)
	return job
}

func ckBootstrap(run uuid.UUID, version int64, hash string) runner.CheckpointCandidate {
	return runner.CheckpointCandidate{Ref: runner.CheckpointRef{RunID: run, Version: version, ManifestHash: hash},
		ArchivePath: "/ck/" + run.String() + "/v" + hash[:4], SizeBytes: 10}
}

func ckPointer(t *testing.T, db *sql.DB, run uuid.UUID) int64 {
	t.Helper()
	var v int64
	err := db.QueryRow(`SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?`, run.String()).Scan(&v)
	if err == sql.ErrNoRows {
		return 0
	}
	require.NoError(t, err)
	return v
}

func ckRows(t *testing.T, db *sql.DB, run uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ?`, run.String()).Scan(&n))
	return n
}

func TestCheckpointCatalog_BootstrapRecordsCommittedBaseline(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	_, _, ok, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	assert.False(t, ok)

	c := ckBootstrap(run, 1, ckHash("a"))
	require.NoError(t, cat.Bootstrap(ctx, c))
	ref, path, ok, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, c.Ref, ref)
	assert.Equal(t, c.ArchivePath, path)
	assert.Equal(t, int64(1), ckPointer(t, db, run), "the committed pointer is the home row")
	var state, owner string
	var job sql.NullString
	require.NoError(t, db.QueryRow(`SELECT state, job_id, resource_owner FROM workspace_checkpoints WHERE run_id = ? AND version = 1`,
		run.String()).Scan(&state, &job, &owner))
	assert.Equal(t, "committed", state)
	assert.False(t, job.Valid, "a baseline has no job")
	assert.Equal(t, "bootstrap", owner, "provenance only, never an authorization")
	got, gotPath, err := cat.Get(ctx, run, 1)
	require.NoError(t, err)
	assert.Equal(t, c.Ref, got)
	assert.Equal(t, c.ArchivePath, gotPath)
	_, _, err = cat.Get(ctx, run, 9)
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)

	require.ErrorIs(t, cat.Bootstrap(ctx, ckBootstrap(run, 2, ckHash("b"))), runner.ErrCheckpointConflict,
		"a run with committed progress is never re-baselined")
	withJob := ckBootstrap(uuid.New(), 1, ckHash("c"))
	withJob.Job = &runner.Fence{JobID: uuid.New(), RunnerID: runner.BuiltInRunnerID, Generation: 1}
	require.ErrorIs(t, cat.Bootstrap(ctx, withJob), runner.ErrInvalidCandidate)
	assert.Equal(t, 1, ckRows(t, db, run))
}

func TestCheckpointCandidate_BootstrapRejectsActiveWriter(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)

	leased := uuid.New()
	_, err := db.Exec(`INSERT INTO workspace_leases(resource_key, holder, owner, generation, token, active) VALUES (?, 'job:x', 'builtin', 1, ?, 1)`,
		"run:"+leased.String(), uuid.NewString())
	require.NoError(t, err)
	require.ErrorIs(t, cat.Bootstrap(ctx, ckBootstrap(leased, 1, ckHash("a"))), runner.ErrCheckpointConflict, "an active resource claim blocks bootstrap")
	assert.Zero(t, ckRows(t, db, leased))
	assert.Zero(t, ckPointer(t, db, leased))
	_, err = db.Exec(`UPDATE workspace_leases SET active = 0 WHERE resource_key = ?`, "run:"+leased.String())
	require.NoError(t, err)
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(leased, 1, ckHash("a"))), "a released claim does not")

	busy := uuid.New()
	job := ckReserveJob(t, db, busy)
	require.ErrorIs(t, cat.Bootstrap(ctx, ckBootstrap(busy, 1, ckHash("b"))), runner.ErrCheckpointConflict, "an active job blocks bootstrap")
	assert.Zero(t, ckRows(t, db, busy))
	_, err = NewRunnerLedgerRepo(db).Requeue(ctx, job.Fence(), runner.OutcomeRunnerLost)
	require.NoError(t, err)
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(busy, 1, ckHash("b"))))
	assert.Equal(t, int64(1), ckPointer(t, db, busy))
}

// ckInsertPrepared writes a schema-valid prepared row directly (the shape
// Publish writes in cycle 4), standing in for a candidate.
func ckInsertPrepared(t *testing.T, db *sql.DB, run uuid.UUID, version int64, hash, state string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO workspace_checkpoints (run_id, version, manifest_hash, archive_path, size_bytes, job_id, job_generation,
		state, base_ref, resource_key, resource_owner, resource_generation, resource_token, created_at)
		VALUES (?, ?, ?, '/ck/x', 1, ?, 1, ?, '', ?, 'builtin', 1, ?, '2026-11-23T00:00:00Z')`,
		run.String(), version, hash, uuid.NewString(), state, "run:"+run.String(), uuid.NewString())
	require.NoError(t, err)
}

func TestCheckpointCatalog_LatestReadsOnlyCommittedPointer(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	ckInsertPrepared(t, db, run, 2, ckHash("b"), "prepared")
	ckInsertPrepared(t, db, run, 3, ckHash("c"), "committed")

	ref, _, ok, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), ref.Version, "neither a newer prepared row nor a newer committed row moves Latest; only the pointer does")
	_, _, err = cat.Get(ctx, run, 2)
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound, "prepared candidates are not served")

	_, err = db.Exec(`UPDATE workspace_homes SET checkpoint_version = 2 WHERE run_id = ?`, run.String())
	require.NoError(t, err)
	_, _, _, err = cat.Latest(ctx, run)
	require.ErrorIs(t, err, runner.ErrCorruptCheckpoint, "a pointer to a prepared row is corruption, never progress")

	// The validated publisher (P3 T5) promotes row and pointer together.
	_, err = db.Exec(`UPDATE workspace_checkpoints SET state = 'committed' WHERE run_id = ? AND version = 2`, run.String())
	require.NoError(t, err)
	ref, _, ok, err = cat.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, runner.CheckpointRef{RunID: run, Version: 2, ManifestHash: ckHash("b")}, ref)
}

func TestCheckpointCatalog_DeleteRunClearsRowsAndPointer(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run, other := uuid.New(), uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	ckInsertPrepared(t, db, run, 2, ckHash("b"), "prepared")
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(other, 1, ckHash("c"))))

	require.NoError(t, cat.DeleteRun(ctx, run))
	_, _, ok, err := cat.Latest(ctx, run)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Zero(t, ckRows(t, db, run))
	assert.Zero(t, ckPointer(t, db, run))
	_, _, ok, err = cat.Latest(ctx, other)
	require.NoError(t, err)
	assert.True(t, ok, "another run is untouched")
}

func TestCheckpointCatalog_SurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "arbitron.db")
	db := ckFileDB(t, path)
	run := uuid.New()
	require.NoError(t, NewCheckpointCatalogRepo(db).Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	require.NoError(t, db.Close())
	reopened := ckFileDB(t, path)
	ref, _, ok, err := NewCheckpointCatalogRepo(reopened).Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), ref.Version)
}

// appended to adapter/sqlite/checkpoint_catalog_repo_test.go
func TestCheckpointCatalog_RetentionReportsRowsPointerAndWriters(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t).DB
	cat := NewCheckpointCatalogRepo(db)
	run := uuid.New()
	require.NoError(t, cat.Bootstrap(ctx, ckBootstrap(run, 1, ckHash("a"))))
	ckInsertPrepared(t, db, run, 3, ckHash("b"), "prepared")

	ret, err := cat.Retention(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.CheckpointRetention{Versions: map[int64]bool{1: true, 3: true}, Committed: 1}, ret)

	_, err = db.Exec(`INSERT INTO workspace_leases(resource_key, holder, owner, generation, token, active) VALUES (?, 'group:op', 'builtin', 1, ?, 1)`,
		"run:"+run.String(), uuid.NewString())
	require.NoError(t, err)
	ret, err = cat.Retention(ctx, run)
	require.NoError(t, err)
	assert.True(t, ret.Held, "an active claim holds the run")
	_, err = db.Exec(`UPDATE workspace_leases SET active = 0`)
	require.NoError(t, err)

	ckReserveJob(t, db, run)
	ret, err = cat.Retention(ctx, run)
	require.NoError(t, err)
	assert.True(t, ret.Held, "an active job holds the run")

	empty, err := cat.Retention(ctx, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, runner.CheckpointRetention{Versions: map[int64]bool{}}, empty)
}
