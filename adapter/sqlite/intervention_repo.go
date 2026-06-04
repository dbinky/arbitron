package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// InterventionRepo implements instance.InterventionRepository backed by SQLite.
// It is a hand-written-SQL repo (no sqlc) modeled on NotificationRepo.
type InterventionRepo struct {
	db *sql.DB
}

// NewInterventionRepo creates an InterventionRepo using the given database connection.
func NewInterventionRepo(db *sql.DB) *InterventionRepo {
	return &InterventionRepo{db: db}
}

func (r *InterventionRepo) Create(ctx context.Context, iv *instance.Intervention) error {
	const q = `INSERT INTO instance_interventions (id, instance_id, actor_user_id, action, detail) VALUES (?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, q,
		uuidToString(iv.ID), uuidToString(iv.InstanceID), uuidToString(iv.ActorUserID),
		iv.Action, ptrToNullString(iv.Detail))
	if err != nil {
		return fmt.Errorf("creating intervention: %w", err)
	}
	return nil
}

func (r *InterventionRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.Intervention, error) {
	const q = `SELECT id, instance_id, actor_user_id, action, detail, created_at
	           FROM instance_interventions WHERE instance_id = ? ORDER BY created_at ASC`
	rows, err := r.db.QueryContext(ctx, q, uuidToString(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing interventions: %w", err)
	}
	defer rows.Close()

	interventions := make([]instance.Intervention, 0)
	for rows.Next() {
		var idStr, instIDStr, actorStr, action, createdStr string
		var detail sql.NullString
		if err := rows.Scan(&idStr, &instIDStr, &actorStr, &action, &detail, &createdStr); err != nil {
			return nil, fmt.Errorf("scanning intervention row: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("converting intervention row: %w", err)
		}
		instID, err := parseUUID(instIDStr)
		if err != nil {
			return nil, fmt.Errorf("converting intervention row: %w", err)
		}
		actor, err := parseUUID(actorStr)
		if err != nil {
			return nil, fmt.Errorf("converting intervention row: %w", err)
		}
		createdAt, err := parseTime(createdStr)
		if err != nil {
			return nil, fmt.Errorf("converting intervention row: %w", err)
		}
		interventions = append(interventions, instance.Intervention{
			ID:          id,
			InstanceID:  instID,
			ActorUserID: actor,
			Action:      action,
			Detail:      nullStringToPtr(detail),
			CreatedAt:   createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating intervention rows: %w", err)
	}
	return interventions, nil
}
