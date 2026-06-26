package instance

import (
	"github.com/google/uuid"
)

// NodeExecutionAggregate is the per-instance collapse of all NodeExecution
// rows for a list page. It carries the latest model used and the max attempt
// (retry count). All fields are zero-valued when the instance has no node
// executions (a queued run).
type NodeExecutionAggregate struct {
	InstanceID  uuid.UUID
	LatestModel string
	MaxAttempt  int
}
