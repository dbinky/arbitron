package runner

import "sync"

// PoolNotifier signals "pool state changed" (health, config, slots) to
// waiting dispatches. Each subscriber has a 1-buffered channel, so Notify never
// blocks and bursts coalesce into one pending wake.
type PoolNotifier struct {
	mu   sync.Mutex
	next int
	subs map[int]chan struct{}
}

// NewPoolNotifier returns a notifier with no subscribers.
func NewPoolNotifier() *PoolNotifier { return &PoolNotifier{subs: map[int]chan struct{}{}} }

// Notify wakes every subscriber without blocking.
func (n *PoolNotifier) Notify() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a wake channel and its cancel function.
func (n *PoolNotifier) Subscribe() (<-chan struct{}, func()) {
	n.mu.Lock()
	defer n.mu.Unlock()
	id := n.next
	n.next++
	ch := make(chan struct{}, 1)
	n.subs[id] = ch
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		delete(n.subs, id)
	}
}
