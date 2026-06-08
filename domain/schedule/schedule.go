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
	ID           uuid.UUID
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
