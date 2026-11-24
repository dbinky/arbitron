package runner

import (
	"context"

	"github.com/google/uuid"
)

// PinnedSession is a waiting interactive session pinned in its open node visit.
type PinnedSession struct {
	RunID       uuid.UUID
	NodeID      string
	ExecutionID uuid.UUID
	RunnerID    RunnerID
	SourceID    SourceID // Empty when the source is unknown or belongs to an old pin.
}

// PinnedSessionIndex lists every pinned waiting session.
type PinnedSessionIndex interface {
	ListPinned(ctx context.Context) ([]PinnedSession, error)
}
