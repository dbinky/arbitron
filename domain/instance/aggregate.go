package instance

import (
	"time"

	"github.com/google/uuid"
)

// NodeExecutionAggregate is the per-instance collapse of all NodeExecution
// rows for a list page. It carries the latest model used, the max attempt
// (retry count), and the earliest started_at (first-started). All fields are
// zero-valued when the instance has no node executions (a queued run).
type NodeExecutionAggregate struct {
	InstanceID   uuid.UUID
	LatestModel  string
	MaxAttempt   int
	FirstStarted time.Time
}
