package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Compile-time: satisfies the pool event log port.
var _ runner.EventLog = (*PoolEventRepo)(nil)

// defaultPoolEventLimit applies when List is called with limit <= 0.
const defaultPoolEventLimit = 100

// PoolEventRepo implements runner.EventLog over SQLite (migration 041).
type PoolEventRepo struct {
	db *sql.DB
}

// NewPoolEventRepo creates a PoolEventRepo.
func NewPoolEventRepo(db *sql.DB) *PoolEventRepo { return &PoolEventRepo{db: db} }

func (r *PoolEventRepo) Append(ctx context.Context, ev runner.PoolEvent) error {
	if ev.ID == uuid.Nil || ev.Kind == "" {
		return errors.New("pool events: id and kind are required")
	}
	detail, err := toJSONString(ev.Detail)
	if err != nil {
		return fmt.Errorf("encoding pool event detail: %w", err)
	}
	var run sql.NullString
	if ev.RunID != nil {
		run = sql.NullString{String: uuidToString(*ev.RunID), Valid: true}
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO pool_events (id, at, kind, runner_id, source_id, run_id, cause_kind, cause_actor, cause_outcome, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuidToString(ev.ID), timeToString(ev.At), ev.Kind, string(ev.RunnerID), string(ev.SourceID), run,
		ev.Cause.Kind, ev.Cause.Actor, string(ev.Cause.Outcome), detail)
	if err != nil {
		return fmt.Errorf("appending pool event: %w", err)
	}
	return nil
}

func (r *PoolEventRepo) List(ctx context.Context, limit int) ([]runner.PoolEvent, error) {
	if limit <= 0 {
		limit = defaultPoolEventLimit
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, at, kind, runner_id, source_id, run_id, cause_kind, cause_actor, cause_outcome, detail
		FROM pool_events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing pool events: %w", err)
	}
	defer rows.Close()
	var out []runner.PoolEvent
	for rows.Next() {
		var ev runner.PoolEvent
		var id, at, rid, sid, outcome, detail string
		var run sql.NullString
		if err := rows.Scan(&id, &at, &ev.Kind, &rid, &sid, &run, &ev.Cause.Kind, &ev.Cause.Actor, &outcome, &detail); err != nil {
			return nil, fmt.Errorf("scanning pool event: %w", err)
		}
		if ev.ID, err = parseUUID(id); err != nil {
			return nil, err
		}
		if ev.At, err = parseTime(at); err != nil {
			return nil, err
		}
		ev.RunnerID, ev.SourceID, ev.Cause.Outcome = runner.RunnerID(rid), runner.SourceID(sid), runner.OutcomeClass(outcome)
		if run.Valid {
			u, err := parseUUID(run.String)
			if err != nil {
				return nil, err
			}
			ev.RunID = &u
		}
		if ev.Detail, err = fromJSONString(detail); err != nil {
			return nil, fmt.Errorf("decoding pool event detail: %w", err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Prune keeps only the newest keep events.
func (r *PoolEventRepo) Prune(ctx context.Context, keep int) error {
	if keep < 0 {
		return fmt.Errorf("pool events: keep must be >= 0, got %d", keep)
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM pool_events WHERE seq NOT IN (SELECT seq FROM pool_events ORDER BY seq DESC LIMIT ?)`, keep); err != nil {
		return fmt.Errorf("pruning pool events: %w", err)
	}
	return nil
}
