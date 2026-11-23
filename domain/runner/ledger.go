package runner

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// JobState is a job's lifecycle state.
type JobState string

const (
	JobQueued    JobState = "queued"
	JobOffered   JobState = "offered"
	JobRunning   JobState = "running"
	JobCompleted JobState = "completed"
	JobFailed    JobState = "failed"
	JobFenced    JobState = "fenced"
)

// Active reports whether the job is not terminal.
func (s JobState) Active() bool { return s == JobQueued || s == JobOffered || s == JobRunning }

// HoldsSlot reports whether the job occupies a runner slot.
func (s JobState) HoldsSlot() bool { return s == JobOffered || s == JobRunning }

// JobKey is the idempotency key of a dispatched operation.
type JobKey struct {
	RunID        uuid.UUID
	InvocationID uuid.UUID
	OperationID  string
	Attempt      int
}

// Valid reports whether every key component is present.
func (k JobKey) Valid() bool {
	return k.RunID != uuid.Nil && k.InvocationID != uuid.Nil && k.OperationID != "" && k.Attempt >= 1
}

// Job is one durable ledger row.
type Job struct {
	ID            uuid.UUID
	Key           JobKey
	NodeID        string
	Op            OpType
	Group         ModelGroup
	Model         string
	RunnerID      RunnerID
	SourceID      SourceID
	Generation    int64
	State         JobState
	LeaseUntil    *time.Time
	Reason        string
	Wait          *WaitReason
	ResultRef     string
	CheckpointRef string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Fence identifies one assignment generation of a job.
type Fence struct {
	JobID      uuid.UUID
	RunnerID   RunnerID
	Generation int64
}

// Fence returns the job's current fence.
func (j Job) Fence() Fence { return Fence{JobID: j.ID, RunnerID: j.RunnerID, Generation: j.Generation} }

// Attempt is one assignment generation's history.
type Attempt struct {
	JobID      uuid.UUID
	Generation int64
	RunnerID   RunnerID
	SourceID   SourceID
	Reason     string
	Outcome    string
	StartedAt  time.Time
	EndedAt    *time.Time
}

// Ledger is the durable job ledger.
type Ledger interface {
	EnsureIntent(ctx context.Context, key JobKey, nodeID string, req ExecRequirements) (Job, error) // idempotent on key; writes queued
	SetWait(ctx context.Context, jobID uuid.UUID, w *WaitReason) error
	// Reserve: one transaction — job must be queued; counts offered+running on runner; if count >= limit => ErrSlotTaken;
	// sets runner/source/reason, generation+1, state offered, lease. Returns the fenced job.
	Reserve(ctx context.Context, jobID uuid.UUID, d Decision, limit int, leaseUntil time.Time) (Job, error)
	MarkRunning(ctx context.Context, f Fence) error
	Complete(ctx context.Context, f Fence, resultRef string) error
	Fail(ctx context.Context, f Fence, class OutcomeClass) error           // terminal failure of this attempt (slot released)
	Requeue(ctx context.Context, f Fence, class OutcomeClass) (Job, error) // back to queued, slot released, history appended
	FenceCheck(ctx context.Context, f Fence) error                         // THE single fence; ErrFenced when stale/terminal
	ExtendLease(ctx context.Context, f Fence, until time.Time) error
	Get(ctx context.Context, jobID uuid.UUID) (Job, error)
	FindByKey(ctx context.Context, key JobKey) (Job, bool, error)
	ListByRun(ctx context.Context, runID uuid.UUID) ([]Job, error)
	ListActive(ctx context.Context) ([]Job, error) // queued, offered, running
	ListActiveByRunner(ctx context.Context, id RunnerID) ([]Job, error)
	Attempts(ctx context.Context, jobID uuid.UUID) ([]Attempt, error)
}

var (
	ErrSlotTaken   = errors.New("runner: runner has no free slot")
	ErrFenced      = errors.New("runner: stale or terminal job fence")
	ErrJobNotFound = errors.New("runner: job not found")
)
