package instance

import (
	"time"

	"github.com/google/uuid"
)

// WorkspaceMove is one durable change of a run's workspace home, projected at
// read time. TargetJobID identifies the exact execution placement; maintenance
// transfers and initial acquisitions have no executing target job.
type WorkspaceMove struct {
	From           string     `json:"from"`
	To             string     `json:"to"`
	Reason         string     `json:"reason"`
	Bytes          int64      `json:"bytes"`
	Generation     int64      `json:"generation"`
	At             time.Time  `json:"at"`
	TargetJobID    *uuid.UUID `json:"targetJobId,omitempty"`
	InvocationID   *uuid.UUID `json:"invocationId,omitempty"`
	NodeID         string     `json:"nodeId"`
	OperationID    string     `json:"operationId"`
	FromGeneration int64      `json:"fromGeneration"`
	ToGeneration   int64      `json:"toGeneration"`
}
