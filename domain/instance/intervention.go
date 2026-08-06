package instance

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Intervention action constants. The engine and the interactive handler
// reference the same strings so the audit log is consistent across surfaces.
const (
	ActionPause            = "pause"
	ActionResume           = "resume"
	ActionCancel           = "cancel"
	ActionSetPriority      = "set_priority"
	ActionRetry            = "retry"
	ActionRerun            = "rerun"
	ActionInteractiveMsg   = "interactive.message"
	ActionInteractiveEnded = "interactive.ended"
)

// Intervention records a non-owner action on an instance for attribution. A row
// is written only when the acting identity differs from the instance owner;
// owner self-actions are not recorded.
type Intervention struct {
	ID          uuid.UUID
	InstanceID  uuid.UUID
	ActorUserID uuid.UUID
	Action      string
	Detail      *string
	CreatedAt   time.Time
}

// InterventionRepository persists and queries the intervention audit trail.
type InterventionRepository interface {
	Create(ctx context.Context, iv *Intervention) error
	ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]Intervention, error)
}
