package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

const setGatherReportSQL = `UPDATE group_runs
SET gather_json = ?, updated_at = ?
WHERE id = ? AND state != 'done'`

const setTaskFinalizedRefSQL = `UPDATE group_tasks SET finalized_ref = ?
WHERE group_run_id = ? AND task_key = ?
AND (finalized_ref IS NULL OR finalized_ref = '' OR finalized_ref = ?)`

const selectGatherJSONSQL = `SELECT gather_json FROM group_runs WHERE id = ?`

const selectFinalizedRefsSQL = `SELECT task_key, finalized_ref FROM group_tasks
WHERE group_run_id = ? AND finalized_ref IS NOT NULL`

// SetGatherReport replaces a validated, normalized report unless the run is
// already done. SetFinalOutput freezes it when preparing the continuation.
func (r *GroupRunRepo) SetGatherReport(ctx context.Context, runID uuid.UUID, report instance.GatherReport) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	if err := report.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(report.Normalized())
	if err != nil {
		return fmt.Errorf("marshalling gather report: %w", err)
	}
	res, err := r.db.ExecContext(ctx, setGatherReportSQL, string(raw), groupTime(r.nowUTC()), uuidToString(runID))
	if err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set gather report: %w", err)
	}
	if n == 1 {
		return nil
	}
	exists, err := r.groupRunExists(ctx, runID)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
	}
	return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
}

// ReplaceGatherReport replaces the report in one transaction only while the
// stored report still equals expected (nil: none stored) and the run is not
// done. A mismatch returns ErrGatherReportStale and writes nothing.
func (r *GroupRunRepo) ReplaceGatherReport(ctx context.Context, runID uuid.UUID, expected *instance.GatherReport, report instance.GatherReport) error {
	if err := report.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(report.Normalized())
	if err != nil {
		return fmt.Errorf("marshalling gather report: %w", err)
	}
	return r.withTx(ctx, "replace gather report", func(tx *sql.Tx) error {
		var state string
		var stored sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT state, gather_json FROM group_runs WHERE id = ?`, uuidToString(runID)).Scan(&state, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
		}
		if err != nil {
			return fmt.Errorf("replace gather report: %w", err)
		}
		if state == string(instance.GroupRunDone) {
			return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
		}
		var current *instance.GatherReport
		if stored.Valid && stored.String != "" {
			current = &instance.GatherReport{}
			if err := json.Unmarshal([]byte(stored.String), current); err != nil {
				return fmt.Errorf("decode gather report for %s: %w", runID, err)
			}
		}
		same, err := instance.SameGatherReport(current, expected)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportStale)
		}
		if _, err := tx.ExecContext(ctx, setGatherReportSQL, string(raw), groupTime(r.nowUTC()), uuidToString(runID)); err != nil {
			return fmt.Errorf("replace gather report: %w", err)
		}
		return nil
	})
}

// SetTaskFinalizedRef records a finalized ref addressed by both run and key.
// The first ref is immutable; retrying with that same ref is idempotent.
func (r *GroupRunRepo) SetTaskFinalizedRef(ctx context.Context, runID uuid.UUID, key, ref string) error {
	if err := r.ready(); err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	if key == "" || ref == "" {
		return errors.New("set task finalized ref: task key and ref are required")
	}
	res, err := r.db.ExecContext(ctx, setTaskFinalizedRefSQL, ref, uuidToString(runID), key, ref)
	if err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set task finalized ref: %w", err)
	}
	if n == 0 {
		var one int
		err := r.db.QueryRowContext(ctx, `SELECT 1 FROM group_tasks WHERE group_run_id = ? AND task_key = ?`, uuidToString(runID), key).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupRunNotFound)
		}
		if err != nil {
			return fmt.Errorf("set task finalized ref: %w", err)
		}
		return fmt.Errorf("group run %s task %q: finalized ref is immutable", runID, key)
	}
	return nil
}

// hydrateGather uses the read's own transaction for a consistent snapshot.
// Calling r.db while holding that transaction would deadlock the single connection.
func hydrateGather(ctx context.Context, q groupQueryer, run *instance.GroupRun) error {
	if run == nil {
		return nil
	}
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, selectGatherJSONSQL, uuidToString(run.ID)).Scan(&raw); err != nil {
		return fmt.Errorf("load gather report for %s: %w", run.ID, err)
	}
	run.Gather = nil
	if raw.Valid && raw.String != "" {
		var report instance.GatherReport
		if err := json.Unmarshal([]byte(raw.String), &report); err != nil {
			return fmt.Errorf("decode gather report for %s: %w", run.ID, err)
		}
		run.Gather = &report
	}
	rows, err := q.QueryContext(ctx, selectFinalizedRefsSQL, uuidToString(run.ID))
	if err != nil {
		return fmt.Errorf("load finalized refs for %s: %w", run.ID, err)
	}
	defer rows.Close()
	refs := map[string]string{}
	for rows.Next() {
		var key, ref string
		if err := rows.Scan(&key, &ref); err != nil {
			return fmt.Errorf("scan finalized ref for %s: %w", run.ID, err)
		}
		refs[key] = ref
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load finalized refs for %s: %w", run.ID, err)
	}
	for i := range run.Tasks {
		run.Tasks[i].FinalizedRef = refs[run.Tasks[i].Key]
	}
	return nil
}

func (r *GroupRunRepo) groupRunExists(ctx context.Context, runID uuid.UUID) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM group_runs WHERE id = ?`, uuidToString(runID)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check group run %s: %w", runID, err)
	}
	return true, nil
}

// ListResultsResourceRuns inventories completed results tasks independently of
// ListUnsettled and snapshot presence. Their immutable roster remains the
// recovery record even after launch released the snapshot or a prior cleanup
// removed some siblings. Missing directories make repeated cleanup a no-op.
func (r *GroupRunRepo) ListResultsResourceRuns(ctx context.Context) ([]uuid.UUID, error) {
	if err := r.ready(); err != nil {
		return nil, fmt.Errorf("list results resources: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.id FROM group_runs r
 WHERE r.gather_mode = 'results' AND r.state = 'done'
 AND EXISTS (SELECT 1 FROM group_tasks t JOIN instances i ON i.id = t.child_instance_id
             WHERE t.group_run_id = r.id AND i.state = 'completed')
 ORDER BY r.created_at, r.id`)
	if err != nil {
		return nil, fmt.Errorf("list results resources: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("list results resources: %w", err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("list results resources: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
