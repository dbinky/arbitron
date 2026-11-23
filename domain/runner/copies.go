package runner

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// CopyStatus is the inventory state of one runner's copy of a run's workspace.
type CopyStatus string

const (
	CopyActive        CopyStatus = "active"         // held at the recorded home generation
	CopyStale         CopyStatus = "stale"          // reported on reconnect for a generation the runner no longer owns; delete sent
	CopyDeletePending CopyStatus = "delete_pending" // deletion ordered (planned move or workspace removal); not yet confirmed
	CopyDeleted       CopyStatus = "deleted"        // a later hello no longer lists it
)

var ErrInvalidCopyStatus = errors.New("runner: invalid copy status for this operation")

type RunnerCopy struct {
	RunID          uuid.UUID
	RunnerID       RunnerID
	HomeGeneration int64
	Status         CopyStatus
	UpdatedAt      time.Time
}

// CopyInventory records runner-held workspace copies (runner_copies).
type CopyInventory interface {
	RecordHeld(ctx context.Context, runID uuid.UUID, id RunnerID, gen int64) error
	// MarkForDeletion records that copies at or below upTo must go. It never
	// touches an active row whose generation is newer than upTo.
	MarkForDeletion(ctx context.Context, runID uuid.UUID, id RunnerID, upTo int64, status CopyStatus) error
	MarkDeleted(ctx context.Context, runID uuid.UUID, id RunnerID) error
	PendingDeletes(ctx context.Context, id RunnerID) ([]RunnerCopy, error)
	ListByRun(ctx context.Context, runID uuid.UUID) ([]RunnerCopy, error)
}

// MsgDeleteCopy tells a runner to delete its copy of a run's workspace.
const MsgDeleteCopy = "delete_copy"

// DeleteCopyPayload: delete the local copy of RunID if its home generation is
// at or below HomeGeneration (a newer copy is never deleted).
type DeleteCopyPayload struct {
	RunID          uuid.UUID
	HomeGeneration int64
	Reason         string
}

// HeldCopyInfo is one copy a runner reports in hello.
type HeldCopyInfo struct {
	RunID          uuid.UUID
	HomeGeneration int64
}

// CopyReconciler decides which reported copies a reconnecting runner must delete.
type CopyReconciler interface {
	ReconcileCopies(ctx context.Context, id RunnerID, held []HeldCopyInfo) ([]DeleteCopyPayload, error)
}

// CopyDeleter sends a delete to a connected runner; false when it is offline.
type CopyDeleter interface {
	DeleteCopy(ctx context.Context, id RunnerID, p DeleteCopyPayload) bool
}
