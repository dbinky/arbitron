package notification

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a notification does not exist.
var ErrNotFound = errors.New("notification not found")

// ErrAlreadyExists means Create found an existing notification with this ID.
// Callers retrying a durable notification may treat it as success.
var ErrAlreadyExists = errors.New("notification already exists")

// Type represents the kind of notification.
type Type string

const (
	TypeNeedsInteraction Type = "needs_interaction"
	TypeFailed           Type = "failed"
	TypeCompleted        Type = "completed"
	// TypeScheduleSkipped / TypeScheduleMissed are raised by the schedule
	// evaluator (org-readiness Phase 3). They have NO instance: a scheduled run
	// did not happen (collision skip, or downtime miss). The notifications table
	// stores them with a NULL instance_id and the workflow name denormalized.
	TypeScheduleSkipped Type = "schedule_skipped"
	TypeScheduleMissed  Type = "schedule_missed"
	// TypeConflictResolution indicates a group gather needs human conflict resolution.
	// It is tied to the parent instance.
	TypeConflictResolution Type = "conflict_resolution"
	// TypeProfileRefusesTriggers is raised by the boot pin preflight: a
	// persisted fixed-mode profile names no fixed model version on its routes,
	// so triggers of workflows using it refuse. It has no instance; the
	// denormalized WorkflowName carries "profile <alias>".
	TypeProfileRefusesTriggers Type = "profile_refuses_triggers"
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
