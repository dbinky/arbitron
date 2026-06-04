package mcp

import (
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
)

// SubscriptionManager manages EventBus subscriptions for an MCP session.
// It tracks per-instance and system-wide subscriptions, forwarding events
// through the OnEvent callback. All methods are safe for concurrent use.
type SubscriptionManager struct {
	bus     engine.EventBus
	mu      sync.Mutex
	subs    map[uuid.UUID]func() // instance ID → unsubscribe
	sysSub  func()               // system-wide unsubscribe
	OnEvent func(instanceID uuid.UUID, event engine.Event)
}

// NewSubscriptionManager creates a SubscriptionManager wired to the given EventBus.
func NewSubscriptionManager(bus engine.EventBus) *SubscriptionManager {
	return &SubscriptionManager{
		bus:  bus,
		subs: make(map[uuid.UUID]func()),
	}
}

// SubscribeInstance subscribes to events for the given instance ID. If already
// subscribed, the old subscription is replaced. Events are forwarded via OnEvent.
func (m *SubscriptionManager) SubscribeInstance(id uuid.UUID) error {
	ch, unsub := m.bus.Subscribe(id)

	m.mu.Lock()
	if oldUnsub, ok := m.subs[id]; ok {
		oldUnsub()
	}
	m.subs[id] = unsub
	m.mu.Unlock()

	go m.forward(id, ch)

	return nil
}

// UnsubscribeInstance removes the subscription for the given instance ID.
// It is a no-op if the instance is not subscribed.
func (m *SubscriptionManager) UnsubscribeInstance(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if unsub, ok := m.subs[id]; ok {
		unsub()
		delete(m.subs, id)
	}
}

// SubscribeSystem subscribes to all events (system-wide). If already subscribed,
// the old subscription is replaced. Events are forwarded via OnEvent with a nil
// instance ID (uuid.Nil) since the channel receives events for all instances.
func (m *SubscriptionManager) SubscribeSystem() error {
	ch, unsub := m.bus.SubscribeAll()

	m.mu.Lock()
	if m.sysSub != nil {
		m.sysSub()
	}
	m.sysSub = unsub
	m.mu.Unlock()

	go m.forwardScoped(ch)

	return nil
}

// UnsubscribeSystem removes the system-wide subscription. It is a no-op if
// not currently subscribed.
func (m *SubscriptionManager) UnsubscribeSystem() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sysSub != nil {
		m.sysSub()
		m.sysSub = nil
	}
}

// CleanupAll unsubscribes everything — all per-instance subscriptions and the
// system-wide subscription. Called on connection close.
func (m *SubscriptionManager) CleanupAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, unsub := range m.subs {
		unsub()
		delete(m.subs, id)
	}

	if m.sysSub != nil {
		m.sysSub()
		m.sysSub = nil
	}
}

// ActiveCount returns the number of active subscriptions, including the
// system-wide subscription if active.
func (m *SubscriptionManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := len(m.subs)
	if m.sysSub != nil {
		count++
	}
	return count
}

// forward reads events from the channel and calls OnEvent for each one.
// It exits when the channel is closed (i.e., the subscription was cancelled).
func (m *SubscriptionManager) forward(instanceID uuid.UUID, ch <-chan engine.Event) {
	for evt := range ch {
		m.mu.Lock()
		cb := m.OnEvent
		m.mu.Unlock()

		if cb != nil {
			cb(instanceID, evt)
		}
	}
}

// forwardScoped reads ScopedEvents from the system-wide channel and calls
// OnEvent with the event's real instance ID (rather than uuid.Nil), so MCP
// system-stream consumers can attribute each event to its instance. It exits
// when the channel is closed.
func (m *SubscriptionManager) forwardScoped(ch <-chan engine.ScopedEvent) {
	for sev := range ch {
		m.mu.Lock()
		cb := m.OnEvent
		m.mu.Unlock()

		if cb != nil {
			cb(sev.InstanceID, sev.Event)
		}
	}
}
