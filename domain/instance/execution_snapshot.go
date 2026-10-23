package instance

import (
	"context"

	"github.com/google/uuid"
)

// ExecutionSnapshot is the workflow definition a run executes from, frozen at
// trigger. Its node overrides are routes the run still takes even after the
// workflow on disk is edited, renamed or deleted.
type ExecutionSnapshot struct {
	InstanceID       uuid.UUID
	WorkflowSnapshot map[string]any
}

// ExecutionSnapshotSource is the trusted (caller-independent) read of every run
// that can still execute a node on a live-resolved model: a legacy NULL-pin run
// that is queued, running, waiting, paused, or failed and therefore retryable.
// A pinned run executes only its pins, and a completed or cancelled run never
// executes again, so neither consumes an advanced profile model. A row
// with corrupt pins must be isolated as failed before being omitted; it must
// never be treated as legacy. Other unreadable inputs fail the read.
type ExecutionSnapshotSource interface {
	ListExecutionSnapshots(context.Context) ([]ExecutionSnapshot, error)
}
