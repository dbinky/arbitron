package notification

import (
	"time"

	"github.com/google/uuid"
)

// Type represents the kind of notification.
type Type string

const (
	TypeNeedsInteraction Type = "needs_interaction"
	TypeFailed           Type = "failed"
	TypeCompleted        Type = "completed"
)

// Notification represents a user-facing notification tied to a workflow instance.
type Notification struct {
	ID           uuid.UUID
	InstanceID   uuid.UUID
	WorkflowName string
	Type         Type
	Read         bool
	Dismissed    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
