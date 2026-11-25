package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

func (r *WorkspaceHomeRepo) BootstrapLocalCheckpoint(ctx context.Context, runID uuid.UUID, ref string) error {
	parsed, err := runner.ParseLocalCheckpointRef(ref)
	if err != nil {
		return err
	}
	if parsed.RunID != runID || parsed.Version != 1 {
		return fmt.Errorf("sqlite: local baseline %s for run %s: %w", ref, runID, runner.ErrCheckpointConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var cur, reason string
	err = tx.QueryRowContext(ctx,
		`SELECT local_checkpoint, non_portable_reason FROM workspace_homes WHERE run_id = ?`, runID.String()).Scan(&cur, &reason)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && reason == "") {
		return fmt.Errorf("sqlite: local baseline for run %s: home is portable", runID)
	}
	if err != nil {
		return err
	}
	if cur == ref {
		return tx.Commit()
	}
	if cur != "" {
		return fmt.Errorf("sqlite: local baseline for run %s already %s: %w", runID, cur, runner.ErrCheckpointConflict)
	}
	var active int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1`, "run:"+runID.String()).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return fmt.Errorf("sqlite: local baseline for run %s: an active writer holds the workspace: %w", runID, runner.ErrCheckpointConflict)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_homes SET local_checkpoint = ?, dirty = 0 WHERE run_id = ?`, ref, runID.String()); err != nil {
		return err
	}
	return tx.Commit()
}
