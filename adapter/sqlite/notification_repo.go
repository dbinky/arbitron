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
	// Schedule alerts (schedule_skipped/schedule_missed) have no instance, so a
	// nil InstanceID is persisted as SQL NULL (the FK would reject the zero
	// UUID) and the workflow name is carried in the denormalized column instead
	// of via the instance join.
	query := `INSERT INTO notifications (id, instance_id, type, workflow_name) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`
	var instanceID any
	if n.InstanceID != uuid.Nil {
		instanceID = uuidToString(n.InstanceID)
	}
	result, err := r.db.ExecContext(ctx, query, uuidToString(n.ID), instanceID, string(n.Type), n.WorkflowName)
	if err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking created notification: %w", err)
	}
	if affected == 0 {
		return notification.ErrAlreadyExists
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

	// COALESCE the joined instance workflow_name first, falling back to the
	// notification's denormalized workflow_name (set for instance-less schedule
	// alerts where the join yields no row).
	query := `
SELECT n.id, COALESCE(n.instance_id, ''), COALESCE(NULLIF(i.workflow_name, ''), n.workflow_name, ''), n.type, n.read, n.dismissed, n.created_at, n.updated_at
FROM notifications n
LEFT JOIN instances i ON i.id = n.instance_id`

	args := []any{}
	var conditions []string

	if filters.OwnerID != nil {
		conditions = append(conditions, "i.owner_id = ?")
		args = append(args, uuidToString(*filters.OwnerID))
	}

	if filters.Dismissed != nil {
		dismissed := 0
		if *filters.Dismissed {
			dismissed = 1
		}
		conditions = append(conditions, "n.dismissed = ?")
		args = append(args, dismissed)
	}

	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for _, c := range conditions[1:] {
			query += " AND " + c
		}
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
		var instanceID uuid.UUID
		if instIDStr != "" {
			instanceID, err = parseUUID(instIDStr)
			if err != nil {
				return nil, fmt.Errorf("converting notification row: %w", err)
			}
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

func (r *NotificationRepo) CountUnread(ctx context.Context, ownerID *uuid.UUID) (int, error) {
	var count int
	var err error
	if ownerID == nil {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM notifications WHERE read = 0 AND dismissed = 0`,
		).Scan(&count)
	} else {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM notifications n
			 JOIN instances i ON i.id = n.instance_id
			 WHERE n.read = 0 AND n.dismissed = 0 AND i.owner_id = ?`,
			uuidToString(*ownerID),
		).Scan(&count)
	}
	if err != nil {
		return 0, fmt.Errorf("counting unread notifications: %w", err)
	}
	return count, nil
}

func (r *NotificationRepo) MarkAllRead(ctx context.Context, ownerID *uuid.UUID) error {
	now := timeToString(time.Now().UTC())
	var err error
	if ownerID == nil {
		_, err = r.db.ExecContext(ctx,
			`UPDATE notifications SET read = 1, updated_at = ? WHERE read = 0`,
			now,
		)
	} else {
		_, err = r.db.ExecContext(ctx,
			`UPDATE notifications SET read = 1, updated_at = ?
			 WHERE read = 0 AND instance_id IN (SELECT id FROM instances WHERE owner_id = ?)`,
			now, uuidToString(*ownerID),
		)
	}
	if err != nil {
		return fmt.Errorf("marking all notifications read: %w", err)
	}
	return nil
}

func (r *NotificationRepo) Dismiss(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error {
	now := timeToString(time.Now().UTC())
	var result sql.Result
	var err error
	if ownerID == nil {
		result, err = r.db.ExecContext(ctx,
			`UPDATE notifications SET dismissed = 1, updated_at = ? WHERE id = ?`,
			now, uuidToString(id),
		)
	} else {
		// Fail closed: treat a notification owned by another user as not found.
		result, err = r.db.ExecContext(ctx,
			`UPDATE notifications SET dismissed = 1, updated_at = ?
			 WHERE id = ? AND instance_id IN (SELECT id FROM instances WHERE owner_id = ?)`,
			now, uuidToString(id), uuidToString(*ownerID),
		)
	}
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
