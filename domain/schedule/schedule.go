// Package schedule is the recurring-trigger domain core. It owns the Schedule
// entity, the ScheduleRepository port, cron/timezone validation, and the
// in-process evaluator that starts workflows on a clock. It depends only on the
// stdlib, github.com/google/uuid, github.com/robfig/cron/v3 (parse + next-fire
// math only), and sibling domain ports (identity, notification, workflow). It
// imports nothing from adapter/* and does not import domain/engine; the engine
// satisfies the evaluator's narrow Trigger port at the composition root.
package schedule

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors. These map cleanly to transport status at the adapter seam
// (ErrNotFound => 404; the validation errors => 400).
var (
	ErrNotFound        = errors.New("schedule not found")
	ErrExists          = errors.New("schedule already exists")
	ErrInvalidCron     = errors.New("schedule: invalid cron expression")
	ErrInvalidTimezone = errors.New("schedule: invalid timezone")
	ErrEmptyWorkflow   = errors.New("schedule: workflow name must not be empty")
)

// Schedule is a recurring trigger: it starts WorkflowName on a cron + timezone,
// attributed to CreatorID (the run's owner + department source).
type Schedule struct {
	ID uuid.UUID
	// WorkflowID is the stable identity of the scheduled workflow (resolved
	// name->id at create time via the workflow registry). It is uuid.Nil for a
	// schedule created before id-references existed, or when no registry is wired
	// (local mode). At fire time the evaluator resolves WorkflowID->current name
	// so a rename cannot break a running schedule; WorkflowName is RETAINED as the
	// human-facing display label and the fall-back when WorkflowID is Nil.
	WorkflowID   uuid.UUID
	WorkflowName string
	CronExpr     string    // standard 5-field cron, validated via robfig/cron
	Timezone     string    // IANA name; "" normalizes to "UTC"
	CreatorID    uuid.UUID // the user the run is attributed to (owner + department source)
	Department   string    // department override to stamp the run under ("" => publication default)
	Enabled      bool
	Variables    map[string]any // inputs passed to the workflow at fire time
	LastFiredAt  *time.Time     // nil until first fire; drives no-backfill recovery
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WireMap is the single canonical wire projection of a Schedule. Both the REST
// handler and the MCP tools render schedules through it so the two surfaces
// cannot drift on field names, casing, or time formatting. Timestamps are
// emitted in UTC; LastFiredAt is nil when the schedule has never fired.
func (s Schedule) WireMap() map[string]any {
	var lastFired any
	if s.LastFiredAt != nil {
		lastFired = s.LastFiredAt.UTC()
	}
	return map[string]any{
		"id":           s.ID.String(),
		"workflowId":   s.WorkflowID.String(),
		"workflowName": s.WorkflowName,
		"cronExpr":     s.CronExpr,
		"timezone":     s.Timezone,
		"creatorId":    s.CreatorID.String(),
		"department":   s.Department,
		"enabled":      s.Enabled,
		"variables":    s.Variables,
		"lastFiredAt":  lastFired,
		"createdAt":    s.CreatedAt.UTC(),
		"updatedAt":    s.UpdatedAt.UTC(),
	}
}
