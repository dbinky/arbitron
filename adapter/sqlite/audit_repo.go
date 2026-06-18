package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
)

// Compile-time: satisfies the audit event store port.
var _ audit.EventStore = (*AuditRepo)(nil)

// AuditRepo implements audit.EventStore over SQLite. Hand-written SQL (no sqlc),
// modeled on InterventionRepo.
type AuditRepo struct {
	db *sql.DB
}

// NewAuditRepo creates an AuditRepo using the given database connection.
func NewAuditRepo(db *sql.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

func (r *AuditRepo) Append(ctx context.Context, ev audit.Event) error {
	detail := ev.Detail
	if detail == "" {
		detail = "{}"
	}
	const q = `INSERT INTO audit_events (id, occurred_at, actor_id, action, target, detail)
	           VALUES (?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		uuidToString(ev.ID), timeToString(ev.OccurredAt), ev.ActorID, ev.Action, ev.Target, detail)
	if err != nil {
		return fmt.Errorf("appending audit event: %w", err)
	}
	return nil
}

func (r *AuditRepo) ListByActor(ctx context.Context, actorID string, limit int) ([]audit.Event, error) {
	const q = `SELECT id, occurred_at, actor_id, action, target, detail
	           FROM audit_events WHERE actor_id = ? ORDER BY occurred_at DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, q, actorID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}
	defer rows.Close()
	return scanAuditEvents(rows)
}

// ListByTarget returns every event recorded against target (e.g. a workflow
// name), newest first. Hand-written query (no sqlc), mirroring ListByActor but
// keyed on the target column with no limit — a single workflow's audit trail is
// bounded by its lifecycle ops, not by retention.
func (r *AuditRepo) ListByTarget(ctx context.Context, target string) ([]audit.Event, error) {
	const q = `SELECT id, occurred_at, actor_id, action, target, detail
	           FROM audit_events WHERE target = ? ORDER BY occurred_at DESC`
	rows, err := r.db.QueryContext(ctx, q, target)
	if err != nil {
		return nil, fmt.Errorf("listing audit events by target: %w", err)
	}
	defer rows.Close()
	return scanAuditEvents(rows)
}

// scanAuditEvents materializes the shared SELECT row shape into events.
func scanAuditEvents(rows *sql.Rows) ([]audit.Event, error) {
	out := make([]audit.Event, 0)
	for rows.Next() {
		var idStr, occurredStr, actor, action, target, detail string
		if err := rows.Scan(&idStr, &occurredStr, &actor, &action, &target, &detail); err != nil {
			return nil, fmt.Errorf("scanning audit row: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("parsing audit id: %w", err)
		}
		occurred, err := parseTime(occurredStr)
		if err != nil {
			return nil, fmt.Errorf("parsing audit occurred_at: %w", err)
		}
		out = append(out, audit.Event{
			ID: id, OccurredAt: occurred, ActorID: actor,
			Action: action, Target: target, Detail: detail,
		})
	}
	return out, rows.Err()
}
