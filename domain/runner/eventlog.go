package runner

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Cause.Kind vocabulary.
const (
	CauseOutcome   = "outcome"
	CauseOperator  = "operator"
	CausePlacement = "placement"
	CauseLiveness  = "liveness"
	CauseSystem    = "system"
)

// PoolEvent is one persisted pool event. Detail never carries prompts,
// provider prose or credentials.
type PoolEvent struct {
	ID       uuid.UUID
	At       time.Time
	Kind     string
	RunnerID RunnerID
	SourceID SourceID
	RunID    *uuid.UUID // non-nil => run-scoped (visibility filtered)
	Cause    Cause
	Detail   map[string]any
}

// Cause names why an event happened.
type Cause struct {
	Kind    string // "outcome"|"operator"|"placement"|"liveness"|"system"
	Actor   string
	Outcome OutcomeClass
}

// EventLog is the bounded persisted pool event log.
type EventLog interface {
	Append(ctx context.Context, ev PoolEvent) error
	List(ctx context.Context, limit int) ([]PoolEvent, error) // newest first; limit <= 0 => 100
	Prune(ctx context.Context, keep int) error
}
