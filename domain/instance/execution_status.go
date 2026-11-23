package instance

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionState is the placement status of one dispatched operation of a run.
// It never replaces the run's InstanceState (Remote Runners D6).
type ExecutionState string

const (
	ExecQueuedSlot      ExecutionState = "queued_slot"
	ExecWaitingCapacity ExecutionState = "waiting_capacity"
	ExecRunning         ExecutionState = "running"
	ExecMovingWorkspace ExecutionState = "moving_workspace" // Phase 3
	ExecRecovering      ExecutionState = "recovering"       // Phase 3
)

// ExecutionStatus is one active operation's placement status.
type ExecutionStatus struct {
	RunID              uuid.UUID      `json:"runId"`
	NodeID             string         `json:"nodeId"`
	OperationID        string         `json:"operationId"`
	JobID              uuid.UUID      `json:"jobId"`
	State              ExecutionState `json:"state"`
	RunnerID           string         `json:"runnerId,omitempty"`
	SourceID           string         `json:"sourceId,omitempty"`
	Group              string         `json:"group,omitempty"`
	Model              string         `json:"model,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	NextReset          *time.Time     `json:"nextReset,omitempty"`
	Progress           *MoveProgress  `json:"progress,omitempty"`           // P3
	HomeRunnerID       string         `json:"homeRunnerId,omitempty"`       // P3
	SessionPinRunnerID string         `json:"sessionPinRunnerId,omitempty"` // P4
}

// MoveProgress reports a workspace move (Phase 3).
type MoveProgress struct {
	BytesDone  int64 `json:"bytesDone"`
	BytesTotal int64 `json:"bytesTotal"`
}
