package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// RunnerCopyRepo implements runner.CopyInventory over runner_copies (044).
type RunnerCopyRepo struct {
	db  *sql.DB
	now func() time.Time
}

func NewRunnerCopyRepo(db *sql.DB) *RunnerCopyRepo { return &RunnerCopyRepo{db: db, now: time.Now} }

func (r *RunnerCopyRepo) RecordHeld(ctx context.Context, runID uuid.UUID, id runner.RunnerID, gen int64) error {
	const q = `INSERT INTO runner_copies (run_id, runner_id, home_generation, state, updated_at)
	           VALUES (?, ?, ?, 'active', ?)
	           ON CONFLICT (run_id, runner_id) DO UPDATE SET
	             home_generation = excluded.home_generation, state = 'active', updated_at = excluded.updated_at`
	if _, err := r.db.ExecContext(ctx, q, uuidToString(runID), string(id), gen, timeToString(r.now())); err != nil {
		return fmt.Errorf("recording runner copy: %w", err)
	}
	return nil
}

func (r *RunnerCopyRepo) MarkForDeletion(ctx context.Context, runID uuid.UUID, id runner.RunnerID, upTo int64, status runner.CopyStatus) error {
	if status != runner.CopyStale && status != runner.CopyDeletePending {
		return fmt.Errorf("%w: %q", runner.ErrInvalidCopyStatus, status)
	}
	const q = `INSERT INTO runner_copies (run_id, runner_id, home_generation, state, updated_at)
	           VALUES (?, ?, ?, ?, ?)
	           ON CONFLICT (run_id, runner_id) DO UPDATE SET
	             home_generation = excluded.home_generation, state = excluded.state, updated_at = excluded.updated_at
	           WHERE NOT (runner_copies.state = 'active' AND runner_copies.home_generation > excluded.home_generation)`
	if _, err := r.db.ExecContext(ctx, q, uuidToString(runID), string(id), upTo, string(status), timeToString(r.now())); err != nil {
		return fmt.Errorf("marking runner copy for deletion: %w", err)
	}
	return nil
}

func (r *RunnerCopyRepo) MarkDeleted(ctx context.Context, runID uuid.UUID, id runner.RunnerID) error {
	const q = `UPDATE runner_copies SET state = 'deleted', updated_at = ?
	           WHERE run_id = ? AND runner_id = ? AND state IN ('stale', 'delete_pending')`
	if _, err := r.db.ExecContext(ctx, q, timeToString(r.now()), uuidToString(runID), string(id)); err != nil {
		return fmt.Errorf("marking runner copy deleted: %w", err)
	}
	return nil
}

func (r *RunnerCopyRepo) PendingDeletes(ctx context.Context, id runner.RunnerID) ([]runner.RunnerCopy, error) {
	return r.query(ctx, `SELECT run_id, runner_id, home_generation, state, updated_at FROM runner_copies
	                     WHERE runner_id = ? AND state IN ('stale', 'delete_pending') ORDER BY run_id`, string(id))
}

func (r *RunnerCopyRepo) ListByRun(ctx context.Context, runID uuid.UUID) ([]runner.RunnerCopy, error) {
	return r.query(ctx, `SELECT run_id, runner_id, home_generation, state, updated_at FROM runner_copies
	                     WHERE run_id = ? AND state != 'deleted' ORDER BY runner_id`, uuidToString(runID))
}

func (r *RunnerCopyRepo) query(ctx context.Context, q string, arg any) ([]runner.RunnerCopy, error) {
	rows, err := r.db.QueryContext(ctx, q, arg)
	if err != nil {
		return nil, fmt.Errorf("listing runner copies: %w", err)
	}
	defer rows.Close()
	var out []runner.RunnerCopy
	for rows.Next() {
		var runID, rid, state, updated string
		var c runner.RunnerCopy
		if err := rows.Scan(&runID, &rid, &c.HomeGeneration, &state, &updated); err != nil {
			return nil, fmt.Errorf("scanning runner copy: %w", err)
		}
		if c.RunID, err = parseUUID(runID); err != nil {
			return nil, err
		}
		if c.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		c.RunnerID, c.Status = runner.RunnerID(rid), runner.CopyStatus(state)
		out = append(out, c)
	}
	return out, rows.Err()
}
