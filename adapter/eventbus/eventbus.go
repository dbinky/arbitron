package eventbus

import (
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

const bufferSize = 256

// InProcessEventBus is a thread-safe, in-process event bus that fans out events
// to all subscribers of a given instance. It uses non-blocking sends so that a
// slow consumer does not block other subscribers or the publisher.
type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[uuid.UUID][]*subscriber
}

// subscriber holds the channel. The closed field is only accessed under the
// parent InProcessEventBus mutex.
type subscriber struct {
	ch     chan engine.Event
	closed bool
}

// New creates a new InProcessEventBus.
func New() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[uuid.UUID][]*subscriber),
	}
}

// Publish sends an event to all subscribers of the given instance.
// The send is non-blocking: if a subscriber's buffer is full, the event
// is dropped for that subscriber. This prevents slow consumers from
// causing deadlocks.
//
// The read lock is held for the entire fan-out. Since sends are non-blocking
// (select with default), this cannot deadlock and prevents races with
// concurrent unsubscribe (which needs a write lock to close channels).
func (b *InProcessEventBus) Publish(instanceID uuid.UUID, event engine.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, s := range b.subscribers[instanceID] {
		if s.closed {
			continue
		}
		// Non-blocking send: drop event if buffer is full.
		select {
		case s.ch <- event:
		default:
		}
	}
}

// Subscribe creates a buffered channel for the given instance and returns it
// along with an unsubscribe function. The unsubscribe function closes the channel
// and removes the subscriber from the list. It is safe to call the unsubscribe
// function multiple times.
func (b *InProcessEventBus) Subscribe(instanceID uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event, bufferSize)
	s := &subscriber{ch: ch}

	b.mu.Lock()
	b.subscribers[instanceID] = append(b.subscribers[instanceID], s)
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			s.closed = true

			subs := b.subscribers[instanceID]
			for i, sub := range subs {
				if sub == s {
					subs[i] = subs[len(subs)-1]
					subs[len(subs)-1] = nil
					b.subscribers[instanceID] = subs[:len(subs)-1]
					break
				}
			}
			if len(b.subscribers[instanceID]) == 0 {
				delete(b.subscribers, instanceID)
			}

			close(ch)
		})
	}

	return ch, unsub
}
