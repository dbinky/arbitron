package runner

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ServerHome denotes a workspace held only in the server checkpoint.
const ServerHome RunnerID = ""

// NonPortablePromisorReason explains why a partial clone must remain local.
const NonPortablePromisorReason = "partial clone: required promisor objects missing locally; use a full clone or run git fetch --refetch"

// HomeMoveCause identifies the operation causing a location change.
type HomeMoveCause struct {
	TargetJobID, InvocationID    *uuid.UUID
	NodeID, OperationID          string
	FromGeneration, ToGeneration int64
}

// HomeMove is durable location history, not an execution writer lease.
type HomeMove struct {
	RunID          uuid.UUID  `json:"runId"`
	From           RunnerID   `json:"from"`
	To             RunnerID   `json:"to"`
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

// HomeStore records durable workspace location and handoff history.
type HomeStore interface {
	GetHome(context.Context, uuid.UUID) (Home, error)
	AcquireResource(context.Context, uuid.UUID, RunnerID, int64) (Home, error)
	HandOff(context.Context, uuid.UUID, RunnerID, string, int64, int64, HomeMoveCause) (Home, error)
	SetCheckpoint(context.Context, uuid.UUID, int64) error
	MarkDirty(context.Context, uuid.UUID, bool) error
	SetNonPortable(context.Context, uuid.UUID, string) error
	// BootstrapLocalCheckpoint installs version 1 only without an active writer.
	BootstrapLocalCheckpoint(context.Context, uuid.UUID, string) error
	History(context.Context, uuid.UUID) ([]HomeMove, error)
	ListByRunner(context.Context, RunnerID) ([]Home, error)
	Release(context.Context, uuid.UUID, int64) error
}

var (
	ErrHomeGenerationConflict = errors.New("runner: workspace home generation changed (another writer won)")
	ErrHomeOwned              = errors.New("runner: workspace home is owned by another runner; hand it off first")
)
