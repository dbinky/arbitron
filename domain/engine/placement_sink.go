package engine

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PlacementFact is one placement attempt of a dispatched operation, recorded
// by the runner dispatcher and persisted on the NodeExecution (P1 T14).
type PlacementFact struct {
	JobID      uuid.UUID `json:"jobId"`
	RunnerID   string    `json:"runnerId"`
	SourceID   string    `json:"sourceId,omitempty"`
	Reason     string    `json:"reason"`
	Generation int64     `json:"generation"`
	Outcome    string    `json:"outcome"` // "completed" | "exhausted" | "logged_out" | "transient" | "runner_lost" | "other" | "fenced"
	At         time.Time `json:"at"`
}

// PlacementSink collects the placement facts of one execution attempt. The
// engine installs a fresh sink per attempt; the dispatcher records into it.
type PlacementSink struct {
	mu    sync.Mutex
	facts []PlacementFact
}

// NewPlacementSink returns an empty sink.
func NewPlacementSink() *PlacementSink { return &PlacementSink{} }

// Record appends f.
func (s *PlacementSink) Record(f PlacementFact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.facts = append(s.facts, f)
}

// Facts returns a copy of the recorded facts in order.
func (s *PlacementSink) Facts() []PlacementFact {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PlacementFact, len(s.facts))
	copy(out, s.facts)
	return out
}

type placementSinkKey struct{}

// ContextWithPlacementSink attaches s to ctx.
func ContextWithPlacementSink(ctx context.Context, s *PlacementSink) context.Context {
	return context.WithValue(ctx, placementSinkKey{}, s)
}

// PlacementSinkFromContext returns the sink installed on ctx.
func PlacementSinkFromContext(ctx context.Context) (*PlacementSink, bool) {
	s, ok := ctx.Value(placementSinkKey{}).(*PlacementSink)
	return s, ok && s != nil
}
