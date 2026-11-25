package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rcFixture struct {
	ctx     context.Context
	db      *sql.DB
	leases  *ResourceLeaseRepo
	commits *ResourceCommitRepo
	run     uuid.UUID
	key     string
}

func newRCFixture(t *testing.T) *rcFixture {
	t.Helper()
	db := setupTestDB(t).DB
	run := uuid.New()
	return &rcFixture{ctx: context.Background(), db: db, leases: NewResourceLeaseRepo(db, "boot-1"),
		commits: NewResourceCommitRepo(db), run: run, key: "run:" + run.String()}
}

func (f *rcFixture) staged(version int64, base runner.CheckpointRef, claim engine.ResourceFenceRef) runner.StagedCheckpoint {
	h := strings.Repeat(fmt.Sprintf("%x", version%16), 64)
	return runner.StagedCheckpoint{RunID: f.run, Base: base, Version: version,
		Manifest: runner.Manifest{Version: version, Hash: h}, ArchivePath: fmt.Sprintf("/ckpt/v%d.tar.gz", version),
		SizeBytes: 42, Resource: claim}
}

func (f *rcFixture) home(t *testing.T) (owner string, version int64, dirty bool, reason string) {
	t.Helper()
	require.NoError(t, f.db.QueryRow(`SELECT runner_id, checkpoint_version, dirty, non_portable_reason FROM workspace_homes WHERE run_id = ?`,
		f.run.String()).Scan(&owner, &version, &dirty, &reason))
	return
}

func (f *rcFixture) rows(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ?`, f.run.String()).Scan(&n))
	return n
}

func TestResourceCommit_InsertsCommittedRowAdvancesHomeAndReleasesTheClaim(t *testing.T) {
	f := newRCFixture(t)
	_, err := f.db.Exec(`INSERT INTO workspace_homes(run_id, runner_id, generation, dirty, non_portable_reason, updated_at)
		VALUES (?, 'builtin', 1, 1, 'partial clone', '2026-11-24T00:00:00Z')`, f.run.String())
	require.NoError(t, err)
	claim, err := f.leases.Claim(f.ctx, f.key, "group:group-gather/run:"+f.run.String(), runner.BuiltInRunnerID)
	require.NoError(t, err)

	require.NoError(t, f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, claim)))

	var state, resKey, resOwner, resToken, baseRef string
	var jobID sql.NullString
	var resGen int64
	require.NoError(t, f.db.QueryRow(`SELECT state, job_id, base_ref, resource_key, resource_owner, resource_generation, resource_token
		FROM workspace_checkpoints WHERE run_id = ? AND version = 1`, f.run.String()).
		Scan(&state, &jobID, &baseRef, &resKey, &resOwner, &resGen, &resToken))
	assert.Equal(t, "committed", state, "SINK: a group mutation is committed directly, never prepared")
	assert.False(t, jobID.Valid)
	assert.Equal(t, "", baseRef)
	assert.Equal(t, []any{claim.Key, claim.Owner, claim.Generation, claim.Token.String()}, []any{resKey, resOwner, resGen, resToken})
	owner, version, dirty, reason := f.home(t)
	assert.Equal(t, "builtin", owner)
	assert.Equal(t, int64(1), version)
	assert.False(t, dirty)
	assert.Empty(t, reason, "a portable artifact clears the RR-08 marker, as a step commit does")
	assert.Equal(t, 0, activeLeases(t, f.db, f.key), "the claim is released in the same transaction")
	latest, _, ok, err := NewCheckpointCatalogRepo(f.db).Latest(f.ctx, f.run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), latest.Version)
}

func TestResourceCommit_StaleBaseIsAConflictAndChangesNothing(t *testing.T) {
	f := newRCFixture(t)
	claim, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
	require.NoError(t, err)
	require.NoError(t, f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, claim)))
	claim2, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
	require.NoError(t, err)

	err = f.commits.CommitResource(f.ctx, claim2, f.staged(2, runner.CheckpointRef{}, claim2)) // based on "no checkpoint", but v1 is committed
	require.ErrorIs(t, err, runner.ErrCheckpointConflict)
	assert.Equal(t, 1, f.rows(t))
	_, version, _, _ := f.home(t)
	assert.Equal(t, int64(1), version)
	assert.Equal(t, 1, activeLeases(t, f.db, f.key), "a refused commit keeps the claim for the caller's deferred Release")
}

func TestResourceCommit_RefusesAStaleOrJobClaim(t *testing.T) {
	t.Run("released claim", func(t *testing.T) {
		f := newRCFixture(t)
		claim, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
		require.NoError(t, err)
		require.NoError(t, f.leases.Release(f.ctx, claim))
		err = f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, claim))
		require.ErrorIs(t, err, runner.ErrResourceFenced)
		assert.Equal(t, 0, f.rows(t))
	})
	t.Run("job claim", func(t *testing.T) {
		f := newRCFixture(t)
		j := leaseJob(t, f.db, f.run, runner.BuiltInRunnerID, "running")
		claim, err := f.leases.ClaimJob(f.ctx, f.key, runner.JobHolder(j), j.Fence())
		require.NoError(t, err)
		err = f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, claim))
		require.ErrorIs(t, err, runner.ErrResourceFenced, "a job's claim commits through StepCommitRepo, never here")
		assert.Equal(t, 0, f.rows(t))
	})
	t.Run("staged under another claim", func(t *testing.T) {
		f := newRCFixture(t)
		claim, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
		require.NoError(t, err)
		other := claim
		other.Token = uuid.New()
		err = f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, other))
		require.ErrorIs(t, err, runner.ErrResourceFenced)
		assert.Equal(t, 0, f.rows(t))
	})
}

func TestResourceCommit_RefusesWhenTheHomeMovedAwayFromTheClaimOwner(t *testing.T) {
	f := newRCFixture(t)
	_, err := f.db.Exec(`INSERT INTO workspace_homes(run_id, runner_id, generation, updated_at) VALUES (?, 'mac-1', 2, '2026-11-24T00:00:00Z')`, f.run.String())
	require.NoError(t, err)
	claim, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
	require.NoError(t, err)

	err = f.commits.CommitResource(f.ctx, claim, f.staged(1, runner.CheckpointRef{}, claim))
	require.ErrorIs(t, err, runner.ErrHomeOwned, "a server write never bypasses the home's owner")
	assert.Equal(t, 0, f.rows(t))
}

func TestResourceCommit_MalformedStagedIsRejected(t *testing.T) {
	f := newRCFixture(t)
	claim, err := f.leases.Claim(f.ctx, f.key, "group:g/run:"+f.run.String(), runner.BuiltInRunnerID)
	require.NoError(t, err)
	for name, mut := range map[string]func(*runner.StagedCheckpoint){
		"other run":  func(s *runner.StagedCheckpoint) { s.RunID = uuid.New() },
		"no archive": func(s *runner.StagedCheckpoint) { s.ArchivePath = "" },
		"bad hash":   func(s *runner.StagedCheckpoint) { s.Manifest.Hash = "zz" },
		"no version": func(s *runner.StagedCheckpoint) { s.Version = 0 },
	} {
		s := f.staged(1, runner.CheckpointRef{}, claim)
		mut(&s)
		err := f.commits.CommitResource(f.ctx, claim, s)
		require.ErrorIs(t, err, runner.ErrInvalidCandidate, name)
	}
	assert.Equal(t, 0, f.rows(t))
	assert.Equal(t, 1, activeLeases(t, f.db, f.key))
}
