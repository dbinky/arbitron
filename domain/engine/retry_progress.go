package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// RetryProgress is the durable workflow-retry position of one invocation.
// Source reroutes advance job generations, not workflow attempts.
type RetryProgress struct {
	InvocationID   uuid.UUID
	Attempt        int
	RetryNotBefore *time.Time
	Exhausted      bool
	LastExecution  *instance.NodeExecution
}

// RetryProgressStore journals a failed execution and its retry position atomically.
// An initial load for an open invocation returns attempt 1.
type RetryProgressStore interface {
	LoadRetryProgress(context.Context, uuid.UUID) (RetryProgress, error)
	CommitRetryFailure(ctx context.Context, invocationID uuid.UUID, attempt int,
		execution *instance.NodeExecution, retryAt *time.Time) (RetryProgress, error)
}

// ErrRetryProgressConflict signals a stale, duplicate, or exhausted attempt.
var ErrRetryProgressConflict = errors.New("engine: retry progress conflict")

// SetRetryProgressStore installs the durable retry journal.
func (e *Engine) SetRetryProgressStore(s RetryProgressStore) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retryProgress = s
}

func (e *Engine) retryStore() RetryProgressStore {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.retryProgress
}

// exhaustedRetryError replays the halt without another provider call.
func exhaustedRetryError(nodeID string, p RetryProgress) error {
	msg := "retry budget exhausted"
	if p.LastExecution != nil {
		if s, ok := p.LastExecution.Error["error"].(string); ok && s != "" {
			msg = s
		}
	}
	return fmt.Errorf("node %q (replayed after restart): %s", nodeID, msg)
}
