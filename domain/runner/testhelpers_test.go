package runner_test

import (
	"context"
	"errors"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// recordingBus is an engine.EventBus that records every publish with its scope.
type recordingBus struct {
	mu     sync.Mutex
	events []engine.ScopedEvent
}

func (b *recordingBus) Publish(id uuid.UUID, ev engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, engine.ScopedEvent{InstanceID: id, Event: ev})
}
func (b *recordingBus) Subscribe(uuid.UUID) (<-chan engine.Event, func()) { return nil, func() {} }
func (b *recordingBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) { return nil, func() {} }

func (b *recordingBus) ofType(typ string) []engine.ScopedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []engine.ScopedEvent
	for _, e := range b.events {
		if e.Event.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// memoryEventLog is a runner.EventLog that honours its arguments.
type memoryEventLog struct {
	mu        sync.Mutex
	events    []runner.PoolEvent
	prunes    []int
	appendErr error
}

func (l *memoryEventLog) Append(_ context.Context, ev runner.PoolEvent) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.appendErr != nil {
		return l.appendErr
	}
	l.events = append(l.events, ev)
	return nil
}

func (l *memoryEventLog) List(_ context.Context, limit int) ([]runner.PoolEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []runner.PoolEvent
	for i := len(l.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.events[i])
	}
	return out, nil
}

func (l *memoryEventLog) Prune(_ context.Context, keep int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prunes = append(l.prunes, keep)
	if len(l.events) > keep {
		l.events = append([]runner.PoolEvent(nil), l.events[len(l.events)-keep:]...)
	}
	return nil
}

func (l *memoryEventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.events {
		out = append(out, e.Kind)
	}
	return out
}

var errDisk = errors.New("disk full")
