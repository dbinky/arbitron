package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tsRes(run uuid.UUID, owner string) engine.ResourceFenceRef {
	return engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: owner, Generation: 3,
		Token: uuid.MustParse("6b6b6b6b-6b6b-4b6b-8b6b-6b6b6b6b6b6b")}
}

func TestTransferSessionRepo_RoundTripPartsCommitAndExpiry(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTransferSessionRepo(db.DB)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	run := uuid.New()
	base := runner.CheckpointRef{RunID: run, Version: 4, ManifestHash: strings.Repeat("a", 64)}
	s := runner.TransferSession{
		TokenHash: strings.Repeat("c", 64), JobID: uuid.New(), Generation: 2, RunnerID: "mac-1", RunID: run,
		Direction: runner.TransferPush, BaseRef: base.String(), Resource: tsRes(run, "mac-1"),
		ExpiresAt: now.Add(10 * time.Minute), Parts: map[int]string{}, CreatedAt: now,
	}
	require.NoError(t, repo.Create(ctx, s))
	assert.Error(t, repo.Create(ctx, s), "token hashes are unique")

	got, err := repo.GetByHash(ctx, s.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, s.JobID, got.JobID)
	assert.Equal(t, int64(2), got.Generation)
	assert.Equal(t, runner.RunnerID("mac-1"), got.RunnerID)
	assert.Equal(t, s.RunID, got.RunID)
	assert.Equal(t, runner.TransferPush, got.Direction)
	assert.Equal(t, base.String(), got.BaseRef, "the pinned base round-trips")
	assert.Empty(t, got.BundleHash)
	assert.Empty(t, got.MaintenanceOperationID)
	assert.Equal(t, s.Resource, got.Resource, "the exact claim round-trips")
	assert.True(t, got.ExpiresAt.Equal(s.ExpiresAt))
	assert.Empty(t, got.Parts)

	require.NoError(t, repo.RecordPart(ctx, s.TokenHash, 7, strings.Repeat("7", 64)))
	require.NoError(t, repo.RecordPart(ctx, s.TokenHash, 0, strings.Repeat("0", 64)))
	require.NoError(t, repo.RecordPart(ctx, s.TokenHash, 7, strings.Repeat("8", 64)), "re-sending a part replaces it")
	require.NoError(t, repo.SetCheckpointRef(ctx, s.TokenHash, "run@2:hash"))
	got, err = repo.GetByHash(ctx, s.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, map[int]string{0: strings.Repeat("0", 64), 7: strings.Repeat("8", 64)}, got.Parts)
	assert.Equal(t, "run@2:hash", got.CheckpointRef)

	_, err = repo.GetByHash(ctx, strings.Repeat("d", 64))
	assert.ErrorIs(t, err, runner.ErrTransferTokenInvalid)
	assert.ErrorIs(t, repo.RecordPart(ctx, strings.Repeat("d", 64), 0, strings.Repeat("0", 64)), runner.ErrTransferTokenInvalid)

	n, err := repo.DeleteExpired(ctx, now.Add(5*time.Minute))
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = repo.DeleteExpired(ctx, now.Add(10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	_, err = repo.GetByHash(ctx, s.TokenHash)
	assert.ErrorIs(t, err, runner.ErrTransferTokenInvalid)
}

// A maintenance-scope bundle session (P3 T12) has no job: job_id/generation
// are NULL, and the operation id, bundle hash and claim round-trip.
func TestTransferSessionRepo_MaintenanceBundleScopeRoundTrips(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTransferSessionRepo(db.DB)
	now := time.Now().UTC().Truncate(time.Second)
	run := uuid.New()
	s := runner.TransferSession{TokenHash: strings.Repeat("e", 64), MaintenanceOperationID: "drain-1", RunnerID: "mac-2", RunID: run,
		Direction: runner.TransferBundle, BundleHash: strings.Repeat("f", 64), Resource: tsRes(run, "mac-2"),
		ExpiresAt: now.Add(time.Minute), Parts: map[int]string{}, CreatedAt: now}
	require.NoError(t, repo.Create(ctx, s))
	got, err := repo.GetByHash(ctx, s.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.JobID)
	assert.Zero(t, got.Generation)
	assert.Equal(t, "drain-1", got.MaintenanceOperationID)
	assert.Equal(t, strings.Repeat("f", 64), got.BundleHash)
	assert.Empty(t, got.BaseRef)
	assert.Equal(t, s.Resource, got.Resource)
}

// The schema refuses a session without a live claim; the repo never invents one.
func TestTransferSessionRepo_SessionWithoutClaimIsRefused(t *testing.T) {
	db := setupTestDB(t)
	s := runner.TransferSession{TokenHash: strings.Repeat("9", 64), JobID: uuid.New(), Generation: 1, RunnerID: "mac-1",
		RunID: uuid.New(), Direction: runner.TransferPull, ExpiresAt: time.Now().Add(time.Minute), Parts: map[int]string{}, CreatedAt: time.Now()}
	assert.Error(t, NewTransferSessionRepo(db.DB).Create(context.Background(), s))
}

func transferService(t *testing.T) (*runner.TransferTokenService, *ResourceLeaseRepo, *RunnerLedgerRepo, runner.Job, engine.ResourceFenceRef, runner.TransferScope) {
	t.Helper()
	db := ckFileDB(t, filepath.Join(t.TempDir(), "arbitron.db"))
	run := uuid.New()
	job := ckReserveJob(t, db, run)
	res := ckClaim(t, db, job)
	leases := NewResourceLeaseRepo(db, "boot-test")
	ledger := NewRunnerLedgerRepo(db)
	svc := runner.NewTransferTokenService(NewTransferSessionRepo(db), ledger, leases, time.Now)
	scope := runner.TransferScope{Fence: job.Fence(), RunID: run, Direction: runner.TransferPull,
		BaseRef: runner.CheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("a", 64)}.String(), Resource: res}
	return svc, leases, ledger, job, res, scope
}

func TestTransferSessionRepo_ServesTheTokenService(t *testing.T) {
	ctx := context.Background()
	svc, _, ledger, job, _, scope := transferService(t)
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	got, err := svc.Validate(ctx, tok.Token, job.Fence().RunnerID)
	require.NoError(t, err)
	assert.Equal(t, scope, got.Scope(), "the pinned scope survives the SQLite round trip")

	_, err = ledger.Requeue(ctx, job.Fence(), runner.OutcomeRunnerLost)
	require.NoError(t, err)
	_, err = svc.Validate(ctx, tok.Token, job.Fence().RunnerID)
	assert.ErrorIs(t, err, runner.ErrFenced, "a re-queued job's token is refused")
}

func TestTransferSessionRepo_ReleasedClaimRefusesToken(t *testing.T) {
	ctx := context.Background()
	svc, leases, _, job, res, scope := transferService(t)
	tok, err := svc.Issue(ctx, scope, time.Minute)
	require.NoError(t, err)
	require.NoError(t, leases.Release(ctx, res))
	_, err = svc.Validate(ctx, tok.Token, job.Fence().RunnerID)
	assert.ErrorIs(t, err, runner.ErrResourceFenced, "a token outlives neither its job nor its claim")
	_, err = svc.Issue(ctx, scope, time.Minute)
	assert.ErrorIs(t, err, runner.ErrResourceFenced, "no new token for a released claim")
}
