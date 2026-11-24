// adapter/sqlite/checkpoint_catalog_repo.go
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// bootstrapResourceOwner marks the provenance-only resource columns of a
// bootstrap baseline row (044 requires them). It is never an authorization:
// prepared rows carry the exact P3 T3 claim instead.
const bootstrapResourceOwner = "bootstrap"

// CheckpointCatalogRepo indexes immutable checkpoint artifacts
// (workspace_checkpoints, migration 044). The committed pointer is
// workspace_homes.checkpoint_version; only Bootstrap here and the P3 T5/P3 T13/
// P4 T6 transactions move it.
type CheckpointCatalogRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewCheckpointCatalogRepo creates the repo.
func NewCheckpointCatalogRepo(db *sql.DB) *CheckpointCatalogRepo {
	return &CheckpointCatalogRepo{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func runResourceKey(runID uuid.UUID) string { return "run:" + runID.String() }

// committedRef reads the committed pointer and its row inside q. A pointer to a
// missing or non-committed row is corruption, never "no checkpoint".
func committedRef(ctx context.Context, q groupQueryer, runID uuid.UUID) (runner.CheckpointRef, string, bool, error) {
	var version int64
	var hash, path, state sql.NullString
	err := q.QueryRowContext(ctx, `SELECT h.checkpoint_version, c.manifest_hash, c.archive_path, c.state
		FROM workspace_homes h
		LEFT JOIN workspace_checkpoints c ON c.run_id = h.run_id AND c.version = h.checkpoint_version
		WHERE h.run_id = ?`, uuidToString(runID)).Scan(&version, &hash, &path, &state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version == 0) {
		return runner.CheckpointRef{}, "", false, nil
	}
	if err != nil {
		return runner.CheckpointRef{}, "", false, fmt.Errorf("checkpoint catalog: committed pointer: %w", err)
	}
	if !hash.Valid || state.String != "committed" {
		return runner.CheckpointRef{}, "", false, fmt.Errorf("%w: run %s committed pointer v%d names no committed checkpoint",
			runner.ErrCorruptCheckpoint, runID, version)
	}
	return runner.CheckpointRef{RunID: runID, Version: version, ManifestHash: hash.String}, path.String, true, nil
}

// Latest returns the COMMITTED checkpoint named by the home pointer. Prepared
// rows, however new, are never returned.
func (r *CheckpointCatalogRepo) Latest(ctx context.Context, runID uuid.UUID) (runner.CheckpointRef, string, bool, error) {
	return committedRef(ctx, r.db, runID)
}

// Get returns one COMMITTED checkpoint; prepared candidates are not found.
func (r *CheckpointCatalogRepo) Get(ctx context.Context, runID uuid.UUID, version int64) (runner.CheckpointRef, string, error) {
	var hash, path string
	err := r.db.QueryRowContext(ctx, `SELECT manifest_hash, archive_path FROM workspace_checkpoints
		WHERE run_id = ? AND version = ? AND state = 'committed'`, uuidToString(runID), version).Scan(&hash, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.CheckpointRef{}, "", fmt.Errorf("%w: run %s v%d", runner.ErrCheckpointNotFound, runID, version)
	}
	if err != nil {
		return runner.CheckpointRef{}, "", fmt.Errorf("checkpoint catalog: get: %w", err)
	}
	return runner.CheckpointRef{RunID: runID, Version: version, ManifestHash: hash}, path, nil
}

// Bootstrap records the run's first committed baseline. In one transaction it
// rejects prior committed progress, an active run:<uuid> lease and an active
// job of the run, inserts the committed row and sets the home pointer.
func (r *CheckpointCatalogRepo) Bootstrap(ctx context.Context, c runner.CheckpointCandidate) error {
	if err := c.ValidateBootstrap(); err != nil {
		return err
	}
	run := uuidToString(c.Ref.RunID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("checkpoint catalog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// First statement takes the SQLite write lock before anything is read.
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET updated_at = updated_at WHERE run_id = ?`, run); err != nil {
		return fmt.Errorf("checkpoint catalog: lock: %w", err)
	}
	var pointer, committed, leases, jobs int64
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?), 0),
		(SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ? AND state = 'committed'),
		(SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1),
		(SELECT COUNT(*) FROM runner_jobs WHERE run_id = ? AND state IN ('offered', 'running'))`,
		run, run, runResourceKey(c.Ref.RunID), run).Scan(&pointer, &committed, &leases, &jobs); err != nil {
		return fmt.Errorf("checkpoint catalog: bootstrap preconditions: %w", err)
	}
	switch {
	case pointer != 0 || committed != 0:
		return fmt.Errorf("%w: run %s already has committed progress", runner.ErrCheckpointConflict, c.Ref.RunID)
	case leases != 0:
		return fmt.Errorf("%w: run %s has an active resource writer", runner.ErrCheckpointConflict, c.Ref.RunID)
	case jobs != 0:
		return fmt.Errorf("%w: run %s has an active job", runner.ErrCheckpointConflict, c.Ref.RunID)
	}
	now := timeToString(r.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_checkpoints
		(run_id, version, manifest_hash, archive_path, size_bytes, job_id, job_generation, state, base_ref,
		 resource_key, resource_owner, resource_generation, resource_token, created_at)
		VALUES (?, ?, ?, ?, ?, NULL, NULL, 'committed', '', ?, ?, 1, ?, ?)`,
		run, c.Ref.Version, c.Ref.ManifestHash, c.ArchivePath, c.SizeBytes,
		runResourceKey(c.Ref.RunID), bootstrapResourceOwner, uuid.NewString(), now); err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: run %s v%d already registered", runner.ErrCheckpointConflict, c.Ref.RunID, c.Ref.Version)
		}
		return fmt.Errorf("checkpoint catalog: insert baseline: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_homes(run_id, checkpoint_version, updated_at)
		VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(run_id) DO UPDATE SET checkpoint_version = excluded.checkpoint_version, updated_at = excluded.updated_at`,
		run, c.Ref.Version); err != nil {
		return fmt.Errorf("checkpoint catalog: set baseline pointer: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("checkpoint catalog: commit: %w", err)
	}
	return nil
}

// DeleteRun removes every row of a run and clears its committed pointer, so
// Latest reports "no checkpoint" rather than a dangling pointer.
func (r *CheckpointCatalogRepo) DeleteRun(ctx context.Context, runID uuid.UUID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("checkpoint catalog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_checkpoints WHERE run_id = ?`, uuidToString(runID)); err != nil {
		return fmt.Errorf("checkpoint catalog: delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET checkpoint_version = 0 WHERE run_id = ?`, uuidToString(runID)); err != nil {
		return fmt.Errorf("checkpoint catalog: clear pointer: %w", err)
	}
	return tx.Commit()
}

// appended to adapter/sqlite/checkpoint_catalog_repo.go
// Retention reports every row version of the run, its committed pointer and
// whether a writer is active (an active run:<uuid> lease or an offered/running
// job), for the store's Prune.
func (r *CheckpointCatalogRepo) Retention(ctx context.Context, runID uuid.UUID) (runner.CheckpointRetention, error) {
	run := uuidToString(runID)
	ret := runner.CheckpointRetention{Versions: map[int64]bool{}}
	rows, err := r.db.QueryContext(ctx, `SELECT version FROM workspace_checkpoints WHERE run_id = ?`, run)
	if err != nil {
		return runner.CheckpointRetention{}, fmt.Errorf("checkpoint catalog: retention: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return runner.CheckpointRetention{}, fmt.Errorf("checkpoint catalog: retention: %w", err)
		}
		ret.Versions[v] = true
	}
	if err := rows.Err(); err != nil {
		return runner.CheckpointRetention{}, fmt.Errorf("checkpoint catalog: retention: %w", err)
	}
	var writers int64
	if err := r.db.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT checkpoint_version FROM workspace_homes WHERE run_id = ?), 0),
		(SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1) +
		(SELECT COUNT(*) FROM runner_jobs WHERE run_id = ? AND state IN ('offered', 'running'))`,
		run, runResourceKey(runID), run).Scan(&ret.Committed, &writers); err != nil {
		return runner.CheckpointRetention{}, fmt.Errorf("checkpoint catalog: retention: %w", err)
	}
	ret.Held = writers > 0
	return ret, nil
}
