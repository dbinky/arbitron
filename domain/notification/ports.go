package notification

import (
	"context"

	"github.com/google/uuid"
)

// ListFilters controls which notifications are returned by List.
// OwnerID nil means admin (no owner restriction); non-nil filters to that user.
type ListFilters struct {
	OwnerID   *uuid.UUID
	Dismissed *bool
	Limit     int
	Offset    int
}

// Repository defines the persistence operations for notifications.
// ownerID parameters follow the same nil=admin convention as ListFilters.OwnerID.
type Repository interface {
	// Create returns ErrAlreadyExists for a duplicate ID, without changing the
	// existing row (including its read/dismissed state).
	Create(ctx context.Context, n *Notification) error
	List(ctx context.Context, filters ListFilters) ([]Notification, error)
	CountUnread(ctx context.Context, ownerID *uuid.UUID) (int, error)
	MarkAllRead(ctx context.Context, ownerID *uuid.UUID) error
	Dismiss(ctx context.Context, id uuid.UUID, ownerID *uuid.UUID) error
}
