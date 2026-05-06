package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NotificationRepo implements notification.Repository using raw SQL queries
// against a pgxpool.Pool (same pattern as Stats in instance_repo.go).
//
// Raw SQL is used instead of sqlc because the List query builds its WHERE
// clause and parameter indices dynamically based on optional filters
// (dismissed, limit, offset). sqlc requires static queries, so hand-rolled
// SQL with careful argIdx tracking is the pragmatic choice here. If this
// grows beyond a few filter combinations, consider a query-builder library.
type NotificationRepo struct {
	pool *pgxpool.Pool
}

// NewNotificationRepo creates a new NotificationRepo backed by the given pool.
func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo {
	return &NotificationRepo{pool: pool}
}

// Create inserts a new notification row.
func (r *NotificationRepo) Create(ctx context.Context, n *notification.Notification) error {
	query := `INSERT INTO notifications (id, instance_id, type) VALUES ($1, $2, $3)`
	_, err := r.pool.Exec(ctx, query, n.ID, n.InstanceID, string(n.Type))
	if err != nil {
		return fmt.Errorf("creating notification: %w", err)
	}
	return nil
}

// List returns notifications ordered by created_at DESC, optionally filtering
// by dismissed status. It JOINs on instances to populate WorkflowName.
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
	argIdx := 1

	if filters.Dismissed != nil {
		query += fmt.Sprintf(" WHERE n.dismissed = $%d", argIdx)
		args = append(args, *filters.Dismissed)
		argIdx++
	}

	query += " ORDER BY n.created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, filters.Offset)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing notifications: %w", err)
	}
	defer rows.Close()

	var notifications []notification.Notification
	for rows.Next() {
		var n notification.Notification
		var nType string
		if err := rows.Scan(&n.ID, &n.InstanceID, &n.WorkflowName, &nType, &n.Read, &n.Dismissed, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning notification row: %w", err)
		}
		n.Type = notification.Type(nType)
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating notification rows: %w", err)
	}
	return notifications, nil
}

// CountUnread returns the number of notifications that are neither read nor dismissed.
func (r *NotificationRepo) CountUnread(ctx context.Context) (int, error) {
	query := `SELECT COUNT(*) FROM notifications WHERE read = false AND dismissed = false`
	var count int
	err := r.pool.QueryRow(ctx, query).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting unread notifications: %w", err)
	}
	return count, nil
}

// MarkAllRead sets read=true for all unread notifications.
func (r *NotificationRepo) MarkAllRead(ctx context.Context) error {
	query := `UPDATE notifications SET read = true, updated_at = $1 WHERE read = false`
	_, err := r.pool.Exec(ctx, query, time.Now())
	if err != nil {
		return fmt.Errorf("marking all notifications read: %w", err)
	}
	return nil
}

// Dismiss sets dismissed=true for a single notification. Returns
// notification.ErrNotFound if no row matches the given ID.
func (r *NotificationRepo) Dismiss(ctx context.Context, id uuid.UUID) error {
	query := `UPDATE notifications SET dismissed = true, updated_at = $1 WHERE id = $2`
	tag, err := r.pool.Exec(ctx, query, time.Now(), id)
	if err != nil {
		return fmt.Errorf("dismissing notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notification.ErrNotFound
	}
	return nil
}
