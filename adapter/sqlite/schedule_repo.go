package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
)

// ScheduleRepo implements schedule.ScheduleRepository backed by SQLite.
//
// NOTE on sqlc: the project normally generates query code via sqlc, but sqlc is
// not available in this build environment. This repo is therefore hand-written
// with direct database/sql calls, consistent with adapter/sqlite/notification_repo.go
// (which is also hand-written). The SQL matches the queries documented in the
// Phase-3 Task-2 plan / queries/schedules.sql so a later sqlc regeneration is a
// drop-in. JSON variables and the nullable last_fired_at column are (de)serialized
// here so the domain entity stays driver-agnostic.
type ScheduleRepo struct {
	db *sql.DB
}

// Compile-time assertion that ScheduleRepo satisfies the domain port.
var _ schedule.ScheduleRepository = (*ScheduleRepo)(nil)

// NewScheduleRepo creates a ScheduleRepo using the given database connection.
func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

const scheduleColumns = `id, workflow_id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables, last_fired_at, created_at, updated_at`

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *ScheduleRepo) Create(ctx context.Context, s *schedule.Schedule) error {
	varsJSON, err := toJSONString(s.Variables)
	if err != nil {
		return fmt.Errorf("marshalling schedule variables: %w", err)
	}
	const q = `INSERT INTO schedules (id, workflow_id, workflow_name, cron_expr, timezone, creator_id, department, enabled, variables)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = r.db.ExecContext(ctx, q,
		uuidToString(s.ID),
		workflowIDToNullString(s.WorkflowID),
		s.WorkflowName,
		s.CronExpr,
		schedule.NormalizeTimezone(s.Timezone),
		uuidToString(s.CreatorID),
		s.Department,
		boolToInt(s.Enabled),
		varsJSON,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("schedule %q: %w", s.ID, schedule.ErrExists)
		}
		return fmt.Errorf("creating schedule %q: %w", s.ID, err)
	}
	return nil
}

func (r *ScheduleRepo) Get(ctx context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	const q = `SELECT ` + scheduleColumns + ` FROM schedules WHERE id = ?`
	row := r.db.QueryRowContext(ctx, q, uuidToString(id))
	s, err := r.scanSchedule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("schedule %q: %w", id, schedule.ErrNotFound)
		}
		return nil, fmt.Errorf("getting schedule %q: %w", id, err)
	}
	return s, nil
}

func (r *ScheduleRepo) List(ctx context.Context, filters schedule.ListFilters) ([]schedule.Schedule, error) {
	q := `SELECT ` + scheduleColumns + ` FROM schedules WHERE 1=1`
	var args []any
	if filters.CreatorID != nil {
		q += ` AND creator_id = ?`
		args = append(args, uuidToString(*filters.CreatorID))
	}
	if filters.WorkflowName != nil {
		q += ` AND workflow_name = ?`
		args = append(args, *filters.WorkflowName)
	}
	if filters.Enabled != nil {
		q += ` AND enabled = ?`
		args = append(args, boolToInt(*filters.Enabled))
	}
	q += ` ORDER BY created_at DESC, id`
	return r.queryList(ctx, q, args...)
}

func (r *ScheduleRepo) ListEnabled(ctx context.Context) ([]schedule.Schedule, error) {
	const q = `SELECT ` + scheduleColumns + ` FROM schedules WHERE enabled = 1 ORDER BY id`
	return r.queryList(ctx, q)
}

func (r *ScheduleRepo) Update(ctx context.Context, s *schedule.Schedule) error {
	// Pre-check existence so a missing id maps to the sentinel rather than a
	// silent zero-row update (mirrors connection_repo.go Update).
	if _, err := r.Get(ctx, s.ID); err != nil {
		return err
	}
	varsJSON, err := toJSONString(s.Variables)
	if err != nil {
		return fmt.Errorf("marshalling schedule variables: %w", err)
	}
	const q = `UPDATE schedules
SET workflow_id = ?, workflow_name = ?, cron_expr = ?, timezone = ?, department = ?, enabled = ?, variables = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
WHERE id = ?`
	_, err = r.db.ExecContext(ctx, q,
		workflowIDToNullString(s.WorkflowID),
		s.WorkflowName,
		s.CronExpr,
		schedule.NormalizeTimezone(s.Timezone),
		s.Department,
		boolToInt(s.Enabled),
		varsJSON,
		uuidToString(s.ID),
	)
	if err != nil {
		return fmt.Errorf("updating schedule %q: %w", s.ID, err)
	}
	return nil
}

func (r *ScheduleRepo) SetLastFired(ctx context.Context, id uuid.UUID, at time.Time) error {
	const q = `UPDATE schedules SET last_fired_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ') WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, timeToString(at), uuidToString(id))
	if err != nil {
		return fmt.Errorf("setting last_fired for schedule %q: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("schedule %q: %w", id, schedule.ErrNotFound)
	}
	return nil
}

func (r *ScheduleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	const q = `DELETE FROM schedules WHERE id = ?`
	if _, err := r.db.ExecContext(ctx, q, uuidToString(id)); err != nil {
		return fmt.Errorf("deleting schedule %q: %w", id, err)
	}
	return nil
}

func (r *ScheduleRepo) queryList(ctx context.Context, q string, args ...any) ([]schedule.Schedule, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	defer rows.Close()

	out := make([]schedule.Schedule, 0)
	for rows.Next() {
		s, err := r.scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning schedule row: %w", err)
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating schedule rows: %w", err)
	}
	return out, nil
}

// scanner abstracts *sql.Row and *sql.Rows for scanSchedule.
type scanner interface {
	Scan(dest ...any) error
}

func (r *ScheduleRepo) scanSchedule(row scanner) (*schedule.Schedule, error) {
	var (
		idStr, workflowName, cronExpr, timezone, creatorIDStr, department string
		enabledInt                                                        int
		varsJSON, createdStr, updatedStr                                  string
		workflowID, lastFired                                             sql.NullString
	)
	if err := row.Scan(&idStr, &workflowID, &workflowName, &cronExpr, &timezone, &creatorIDStr, &department,
		&enabledInt, &varsJSON, &lastFired, &createdStr, &updatedStr); err != nil {
		return nil, err
	}
	id, err := parseUUID(idStr)
	if err != nil {
		return nil, err
	}
	wfID, err := nullStringToWorkflowID(workflowID)
	if err != nil {
		return nil, err
	}
	creatorID, err := parseUUID(creatorIDStr)
	if err != nil {
		return nil, err
	}
	vars, err := fromJSONString(varsJSON)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling schedule variables: %w", err)
	}
	if vars == nil {
		vars = map[string]any{}
	}
	lastFiredAt, err := nullStringToTimePtr(lastFired)
	if err != nil {
		return nil, err
	}
	createdAt, err := parseTime(createdStr)
	if err != nil {
		return nil, err
	}
	updatedAt, err := parseTime(updatedStr)
	if err != nil {
		return nil, err
	}
	return &schedule.Schedule{
		ID:           id,
		WorkflowID:   wfID,
		WorkflowName: workflowName,
		CronExpr:     cronExpr,
		Timezone:     timezone,
		CreatorID:    creatorID,
		Department:   department,
		Enabled:      enabledInt != 0,
		Variables:    vars,
		LastFiredAt:  lastFiredAt,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}
