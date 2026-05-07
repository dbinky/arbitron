package notification

import (
	"context"

	"github.com/google/uuid"
)

// ListFilters controls which notifications are returned by List.
type ListFilters struct {
	Dismissed *bool
	Limit     int
	Offset    int
}

// Repository defines the persistence operations for notifications.
type Repository interface {
	Create(ctx context.Context, n *Notification) error
	List(ctx context.Context, filters ListFilters) ([]Notification, error)
	CountUnread(ctx context.Context) (int, error)
	MarkAllRead(ctx context.Context) error
	Dismiss(ctx context.Context, id uuid.UUID) error
}
