package runner

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// Event kinds (bus Event.Type == PoolEvent.Kind). runner./source./pool. kinds
// published with uuid.Nil reach every authenticated subscriber.
const (
	EventRunnerUpdated       = "runner.updated"
	EventRunnerCordoned      = "runner.cordoned"
	EventRunnerUncordoned    = "runner.uncordoned"
	EventSourceHealthChanged = "source.health_changed"
	EventSourceLoggedOut     = "source.logged_out"
	EventSourceRecovered     = "source.recovered"
	EventJobRerouted         = "job.rerouted"     // run-scoped
	EventExecutionStatus     = "execution.status" // run-scoped
)

// PoolEventView is the JSON payload of a pool event on the bus.
type PoolEventView struct {
	ID       uuid.UUID      `json:"id"`
	At       time.Time      `json:"at"`
	Kind     string         `json:"kind"`
	RunnerID string         `json:"runnerId,omitempty"`
	SourceID string         `json:"sourceId,omitempty"`
	RunID    *uuid.UUID     `json:"runId,omitempty"`
	Cause    CauseView      `json:"cause"`
	Detail   map[string]any `json:"detail,omitempty"`
}

// CauseView is the JSON form of a Cause.
type CauseView struct {
	Kind    string `json:"kind"`
	Actor   string `json:"actor,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// NewPoolEventView projects a PoolEvent for the wire.
func NewPoolEventView(ev PoolEvent) PoolEventView {
	return PoolEventView{ID: ev.ID, At: ev.At, Kind: ev.Kind, RunnerID: string(ev.RunnerID), SourceID: string(ev.SourceID),
		RunID: ev.RunID, Cause: CauseView{Kind: ev.Cause.Kind, Actor: ev.Cause.Actor, Outcome: string(ev.Cause.Outcome)}, Detail: ev.Detail}
}

// EventRecorder is the single path every pool event takes: append to the
// bounded log, prune to retention, publish on the bus.
type EventRecorder struct {
	log       EventLog
	bus       engine.EventBus
	retention int
	now       func() time.Time
}

// NewEventRecorder builds a recorder. log and bus are required; retention must
// be >= 1; a nil now uses time.Now.
func NewEventRecorder(log EventLog, bus engine.EventBus, retention int, now func() time.Time) *EventRecorder {
	if log == nil || bus == nil {
		panic("runner: NewEventRecorder requires an EventLog and an EventBus")
	}
	if retention < 1 {
		panic("runner: NewEventRecorder requires retention >= 1")
	}
	if now == nil {
		now = time.Now
	}
	return &EventRecorder{log: log, bus: bus, retention: retention, now: now}
}

// Bus returns the bus events are published on.
func (r *EventRecorder) Bus() engine.EventBus { return r.bus }

// Record persists and publishes ev. Persistence errors are logged, never
// returned: an event-log failure must not fail a step or an operator action.
func (r *EventRecorder) Record(ctx context.Context, ev PoolEvent) {
	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}
	if ev.At.IsZero() {
		ev.At = r.now().UTC()
	}
	if err := r.log.Append(ctx, ev); err != nil {
		slog.Error("runner: pool event not persisted", "kind", ev.Kind, "runner", ev.RunnerID, "source", ev.SourceID, "error", err)
	} else if err := r.log.Prune(ctx, r.retention); err != nil {
		slog.Warn("runner: pool event prune failed", "error", err)
	}
	scope := uuid.Nil
	if ev.RunID != nil {
		scope = *ev.RunID
	}
	r.bus.Publish(scope, engine.Event{Type: ev.Kind, Timestamp: ev.At, Data: NewPoolEventView(ev)})
}
