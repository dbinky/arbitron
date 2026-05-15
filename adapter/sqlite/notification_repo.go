package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
)

// NotificationRepo implements notification.Repository backed by SQLite.
type NotificationRepo struct {
	db *sql.DB
}

// NewNotificationRepo creates a NotificationRepo using the given database connection.
func NewNotificationRepo(db *sql.DB) *NotificationRepo {
	return &NotificationRepo{db: db}
}

func (r *NotificationRepo) Create(ctx context.Context, n *notification.Notification) error {
	query := `INSERT INTO notifications (id, instance_id, type) VALUES (?, ?, ?)`
	_, err := r.db.ExecContext(ctx, query, uuidToString(n.ID), uuidToString(n.InstanceID), string(n.Type))
	if err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	return nil
}

func (r *NotificationRepo) List(ctx context.Context, filters notification.ListFilters) ([]notification.Notification, error) {
	limit := filters.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	query := `
SELECT n.id, n.instance_id, COALESCE(i.workflow_name, ''), n.type, n.read, n.dismissed, n.created_at, n.updated_at
FROM notifications n
LEFT JOIN instances i ON i.id = n.instance_id`

	args := []any{}

	if filters.Dismissed != nil {
		dismissed := 0
		if *filters.Dismissed {
			dismissed = 1
		}
		query += " WHERE n.dismissed = ?"
		args = append(args, dismissed)
	}

	query += " ORDER BY n.created_at DESC"
	query += " LIMIT ? OFFSET ?"
	args = append(args, limit, filters.Offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}
	defer rows.Close()

	notifications := make([]notification.Notification, 0)
	for rows.Next() {
		var idStr, instIDStr, wfName, nType, createdStr, updatedStr string
		var readInt, dismissedInt int
		if err := rows.Scan(&idStr, &instIDStr, &wfName, &nType, &readInt, &dismissedInt, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("scanning notification row: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("converting notification row: %w", err)
		}
		instanceID, err := parseUUID(instIDStr)
		if err != nil {
			return nil, fmt.Errorf("converting notification row: %w", err)
		}
		createdAt, err := parseTime(createdStr)
		if err != nil {
			return nil, fmt.Errorf("converting notification row: %w", err)
		}
		updatedAt, err := parseTime(updatedStr)
		if err != nil {
			return nil, fmt.Errorf("converting notification row: %w", err)
		}
		notifications = append(notifications, notification.Notification{
			ID:           id,
			InstanceID:   instanceID,
			WorkflowName: wfName,
			Type:         notification.Type(nType),
			Read:         readInt != 0,
			Dismissed:    dismissedInt != 0,
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating notification rows: %w", err)
	}
	return notifications, nil
}

func (r *NotificationRepo) CountUnread(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM notifications WHERE read = 0 AND dismissed = 0`
	var count int
	err := r.db.QueryRowContext(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting unread notifications: %w", err)
	}
	return count, nil
}

func (r *NotificationRepo) MarkAllRead(ctx context.Context) error {
	now := timeToString(time.Now().UTC())
	query := `UPDATE notifications SET read = 1, updated_at = ? WHERE read = 0`
	_, err := r.db.ExecContext(ctx, query, now)
	if err != nil {
		return fmt.Errorf("marking all notifications read: %w", err)
	}
	return nil
}

func (r *NotificationRepo) Dismiss(ctx context.Context, id uuid.UUID) error {
	now := timeToString(time.Now().UTC())
	query := `UPDATE notifications SET dismissed = 1, updated_at = ? WHERE id = ?`
	result, err := r.db.ExecContext(ctx, query, now, uuidToString(id))
	if err != nil {
		return fmt.Errorf("dismissing notification: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if affected == 0 {
		return notification.ErrNotFound
	}
	return nil
}
