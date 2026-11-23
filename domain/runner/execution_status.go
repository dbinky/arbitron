package runner

import (
	"context"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ExecutionStatusProjector derives per-run execution status from active ledger
// jobs and publishes execution.status events.
type ExecutionStatusProjector struct {
	ledger Ledger
	mu     sync.RWMutex
	bus    engine.EventBus
	now    func() time.Time
}

// NewExecutionStatusProjector builds a projector over ledger (required).
func NewExecutionStatusProjector(ledger Ledger) *ExecutionStatusProjector {
	if ledger == nil {
		panic("runner: NewExecutionStatusProjector requires a Ledger")
	}
	return &ExecutionStatusProjector{ledger: ledger, now: time.Now}
}

// AttachBus sets the bus execution.status events are published on.
func (p *ExecutionStatusProjector) AttachBus(bus engine.EventBus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bus = bus
}

func (p *ExecutionStatusProjector) attachedBus() engine.EventBus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bus
}

// StatusFromJob projects one job in the given state.
func StatusFromJob(j Job, state instance.ExecutionState) instance.ExecutionStatus {
	s := instance.ExecutionStatus{RunID: j.Key.RunID, NodeID: j.NodeID, OperationID: j.Key.OperationID, JobID: j.ID, State: state,
		RunnerID: string(j.RunnerID), SourceID: string(j.SourceID), Group: j.Group.String(), Model: j.Model, Reason: j.Reason}
	if j.Wait != nil && (state == instance.ExecQueuedSlot || state == instance.ExecWaitingCapacity) {
		s.Reason, s.NextReset, s.Group = j.Wait.Detail, j.Wait.NextReset, j.Wait.Group.String()
	}
	return s
}

func stateOf(j Job) (instance.ExecutionState, bool) {
	switch j.State {
	case JobQueued:
		if j.Wait != nil && j.Wait.Kind == WaitCapacity {
			return instance.ExecWaitingCapacity, true
		}
		return instance.ExecQueuedSlot, true
	case JobOffered, JobRunning:
		return instance.ExecRunning, true
	}
	return "", false
}

// ExecutionStatuses implements engine.ExecutionStatusProvider: one ledger read
// for the whole (already authorized) page, filtered to runIDs.
func (p *ExecutionStatusProjector) ExecutionStatuses(ctx context.Context, runIDs []uuid.UUID) (map[uuid.UUID][]instance.ExecutionStatus, error) {
	out := map[uuid.UUID][]instance.ExecutionStatus{}
	if len(runIDs) == 0 {
		return out, nil
	}
	want := make(map[uuid.UUID]bool, len(runIDs))
	for _, id := range runIDs {
		want[id] = true
	}
	jobs, err := p.ledger.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if !want[j.Key.RunID] {
			continue
		}
		if st, ok := stateOf(j); ok {
			out[j.Key.RunID] = append(out[j.Key.RunID], StatusFromJob(j, st))
		}
	}
	return out, nil
}

// Publish emits execution.status for job, scoped to its run. Without an
// attached bus it is a no-op.
func (p *ExecutionStatusProjector) Publish(_ context.Context, job Job, state instance.ExecutionState) {
	bus := p.attachedBus()
	if bus == nil {
		return
	}
	bus.Publish(job.Key.RunID, engine.Event{Type: EventExecutionStatus, Timestamp: p.now().UTC(), Data: StatusFromJob(job, state)})
}
