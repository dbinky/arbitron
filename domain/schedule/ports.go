package schedule

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ListFilters controls which schedules List returns. It mirrors the nil=admin
// convention of notification.ListFilters / instance.ListFilters.
type ListFilters struct {
	// CreatorID, when non-nil, restricts results to schedules owned by that
	// user. nil => admin (no owner restriction). SECURITY: set by the
	// management service from the request identity (non-admin => own UserID);
	// REST/MCP handlers MUST NOT set it from request input.
	CreatorID    *uuid.UUID
	WorkflowName *string
	Enabled      *bool
}

// ScheduleRepository persists and queries Schedule entities. Implementations
// must wrap the domain sentinels (ErrNotFound, ErrExists) via fmt.Errorf %w so
// the adapter error mapping works.
type ScheduleRepository interface {
	Create(ctx context.Context, s *Schedule) error
	Get(ctx context.Context, id uuid.UUID) (*Schedule, error)
	List(ctx context.Context, filters ListFilters) ([]Schedule, error)
	ListEnabled(ctx context.Context) ([]Schedule, error)
	Update(ctx context.Context, s *Schedule) error
	Delete(ctx context.Context, id uuid.UUID) error
	SetLastFired(ctx context.Context, id uuid.UUID, at time.Time) error
}
