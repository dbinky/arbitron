// adapter/sqlite/drain_item_repo.go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
)

// DrainItemRepo implements runner.DrainStore over drain_items (044).
type DrainItemRepo struct{ db *sql.DB }

func NewDrainItemRepo(db *sql.DB) *DrainItemRepo { return &DrainItemRepo{db: db} }

func (r *DrainItemRepo) Upsert(ctx context.Context, it runner.DrainItem) error {
	const q = `INSERT INTO drain_items (runner_id, kind, run_id, group_key, state, reason, updated_at)
	           VALUES (?, ?, ?, ?, ?, ?, ?)
	           ON CONFLICT (runner_id, kind, run_id) DO UPDATE SET
	             group_key = excluded.group_key, state = excluded.state, reason = excluded.reason, updated_at = excluded.updated_at`
	if _, err := r.db.ExecContext(ctx, q, string(it.RunnerID), it.Kind, uuidToString(it.RunID), it.GroupKey, string(it.State), it.Reason, timeToString(it.UpdatedAt)); err != nil {
		return fmt.Errorf("upserting drain item: %w", err)
	}
	return nil
}

func (r *DrainItemRepo) List(ctx context.Context, id runner.RunnerID) ([]runner.DrainItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT runner_id, kind, run_id, group_key, state, reason, updated_at
	                                     FROM drain_items WHERE runner_id = ? ORDER BY kind, run_id`, string(id))
	if err != nil {
		return nil, fmt.Errorf("listing drain items: %w", err)
	}
	defer rows.Close()
	var out []runner.DrainItem
	for rows.Next() {
		var rid, runID, state, updated string
		var it runner.DrainItem
		if err := rows.Scan(&rid, &it.Kind, &runID, &it.GroupKey, &state, &it.Reason, &updated); err != nil {
			return nil, fmt.Errorf("scanning drain item: %w", err)
		}
		if it.RunID, err = parseUUID(runID); err != nil {
			return nil, err
		}
		if it.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		it.RunnerID, it.State = runner.RunnerID(rid), runner.DrainItemState(state)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *DrainItemRepo) Clear(ctx context.Context, id runner.RunnerID) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM drain_items WHERE runner_id = ?`, string(id)); err != nil {
		return fmt.Errorf("clearing drain items: %w", err)
	}
	return nil
}
