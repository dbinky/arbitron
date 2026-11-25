package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// ResourceCommitRepo publishes group mutations using the shared lease/catalog DB.
type ResourceCommitRepo struct {
	db  *sql.DB
	now func() time.Time
}

var _ runner.ResourceCommitter = (*ResourceCommitRepo)(nil)

func NewResourceCommitRepo(db *sql.DB) *ResourceCommitRepo {
	return &ResourceCommitRepo{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// CommitResource preserves both the committed pointer and the claim on failure.
func (r *ResourceCommitRepo) CommitResource(ctx context.Context, claim engine.ResourceFenceRef, s runner.StagedCheckpoint) error {
	if s.RunID == uuid.Nil || claim.Key != runResourceKey(s.RunID) {
		return fmt.Errorf("%w: staged artifact of run %s does not belong to claim %s", runner.ErrInvalidCandidate, s.RunID, claim.Key)
	}
	ref := runner.CheckpointRef{RunID: s.RunID, Version: s.Version, ManifestHash: s.Manifest.Hash}
	if err := (runner.CheckpointCandidate{Ref: ref, ArchivePath: s.ArchivePath, SizeBytes: s.SizeBytes}).ValidateBootstrap(); err != nil {
		return err
	}
	if !s.Base.IsZero() && (s.Base.RunID != s.RunID || s.Version <= s.Base.Version) {
		return fmt.Errorf("%w: %s does not follow base %s", runner.ErrInvalidCandidate, ref, s.Base)
	}
	if s.Resource != (engine.ResourceFenceRef{}) && s.Resource != claim {
		return fmt.Errorf("%w: artifact %s was staged under another claim", runner.ErrResourceFenced, ref)
	}
	run := uuidToString(s.RunID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("resource commit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire the write lock before reading the fence or committed base.
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_leases SET active = active WHERE resource_key = ?`, claim.Key); err != nil {
		return fmt.Errorf("resource commit: lock: %w", err)
	}
	if err := checkResourceFenceTx(ctx, tx, claim); err != nil {
		return err
	}
	rec, ok, err := readHolder(ctx, tx, claim)
	if err != nil {
		return err
	}
	if !ok || rec.Job != nil {
		return fmt.Errorf("%w: %s generation %d is not a server-side claim", runner.ErrResourceFenced, claim.Key, claim.Generation)
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT runner_id FROM workspace_homes WHERE run_id = ?`, run).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resource commit: read home: %w", err)
	}
	if owner != claim.Owner && !(owner == string(runner.ServerHome) && claim.Owner == string(runner.BuiltInRunnerID)) {
		return fmt.Errorf("%w: run %s is homed on %q, claim owner is %q", runner.ErrHomeOwned, s.RunID, owner, claim.Owner)
	}
	cur, err := committedRefTx(ctx, tx, s.RunID)
	if err != nil {
		return err
	}
	if cur != s.Base.String() {
		return fmt.Errorf("%w: run %s artifact is based on %q, but the committed checkpoint is %q", runner.ErrCheckpointConflict, s.RunID, s.Base, cur)
	}
	now := timeToString(r.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoints
  (run_id, version, manifest_hash, archive_path, size_bytes, job_id, job_generation, state, base_ref,
   resource_key, resource_owner, resource_generation, resource_token, created_at)
  VALUES (?, ?, ?, ?, ?, NULL, NULL, 'committed', ?, ?, ?, ?, ?, ?)`,
		run, ref.Version, ref.ManifestHash, s.ArchivePath, s.SizeBytes, s.Base.String(),
		claim.Key, claim.Owner, claim.Generation, claim.Token.String(), now); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: run %s v%d already registered", runner.ErrCheckpointConflict, s.RunID, ref.Version)
		}
		return fmt.Errorf("resource commit: insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_homes (run_id, updated_at) VALUES (?, ?) ON CONFLICT(run_id) DO NOTHING`, run, now); err != nil {
		return fmt.Errorf("resource commit: home row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET checkpoint_version = ?, dirty = 0, non_portable_reason = '', updated_at = ? WHERE run_id = ?`, ref.Version, now, run); err != nil {
		return fmt.Errorf("resource commit: advance home pointer: %w", err)
	}
	if err := releaseResourceTx(ctx, tx, claim); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("resource commit: commit: %w", err)
	}
	return nil
}
