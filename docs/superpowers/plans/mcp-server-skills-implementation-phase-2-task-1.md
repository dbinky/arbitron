# Phase 2, Task 1: MCP Event Streaming

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add real-time event streaming to the MCP server by subscribing to the domain EventBus and forwarding events as MCP notifications. Implement subscribe/unsubscribe tools.

**Architecture:** The MCP adapter subscribes to `EventBus.Subscribe(instanceID)` or `EventBus.SubscribeAll()` — same methods the WebSocket stream handler uses. A subscription manager tracks active subscriptions per MCP session. When the session closes, all subscriptions are cleaned up. Domain events are translated to MCP notifications.

**Tech Stack:** Go, github.com/mark3labs/mcp-go, sync

---

## Prerequisites

- Phase 1 complete (MCP server with all request-response tools)

## Files to Create/Modify

### Create: `adapter/mcp/subscriptions.go`

Subscription manager that tracks active EventBus subscriptions per session. Handles:
- Per-instance subscriptions (`instance_subscribe` / `instance_unsubscribe`)
- System-wide subscriptions (`system_subscribe` / `system_unsubscribe`)
- Cleanup on session close
- Event-to-notification translation

### Create: `adapter/mcp/subscriptions_test.go`

### Create: `adapter/mcp/tools_streaming.go`

Four new MCP tools: `instance_subscribe`, `instance_unsubscribe`, `system_subscribe`, `system_unsubscribe`.

### Create: `adapter/mcp/tools_streaming_test.go`

### Modify: `adapter/mcp/server.go`

Add subscription manager to the Server struct. Wire cleanup on connection close.

### Modify: `adapter/mcp/register.go`

Add `RegisterStreamingTools(srv)` call.

---

### Task 1.1: Subscription Manager

- [ ] **Step 1: Write failing tests**

```go
func TestSubscriptionManager_Subscribe_HappyPath(t *testing.T) {
	bus := &mockEventBus{}
	mgr := NewSubscriptionManager(bus)

	err := mgr.SubscribeInstance(testInstanceID)
	require.NoError(t, err)
	assert.Equal(t, 1, mgr.ActiveCount())
}

func TestSubscriptionManager_Unsubscribe(t *testing.T) {
	bus := &mockEventBus{}
	mgr := NewSubscriptionManager(bus)
	require.NoError(t, mgr.SubscribeInstance(testInstanceID))

	mgr.UnsubscribeInstance(testInstanceID)
	assert.Equal(t, 0, mgr.ActiveCount())
}

func TestSubscriptionManager_UnsubscribeNonExistent(t *testing.T) {
	bus := &mockEventBus{}
	mgr := NewSubscriptionManager(bus)

	// Should not panic or error
	mgr.UnsubscribeInstance(uuid.New())
	assert.Equal(t, 0, mgr.ActiveCount())
}

func TestSubscriptionManager_CleanupAll(t *testing.T) {
	bus := &mockEventBus{}
	mgr := NewSubscriptionManager(bus)
	require.NoError(t, mgr.SubscribeInstance(uuid.New()))
	require.NoError(t, mgr.SubscribeInstance(uuid.New()))
	require.NoError(t, mgr.SubscribeSystem())

	mgr.CleanupAll()
	assert.Equal(t, 0, mgr.ActiveCount())
}

func TestSubscriptionManager_DuplicateSubscribe(t *testing.T) {
	bus := &mockEventBus{}
	mgr := NewSubscriptionManager(bus)
	require.NoError(t, mgr.SubscribeInstance(testInstanceID))

	// Second subscribe to same instance — should replace, not duplicate
	require.NoError(t, mgr.SubscribeInstance(testInstanceID))
	assert.Equal(t, 1, mgr.ActiveCount())
}

func TestSubscriptionManager_EventForwarding(t *testing.T) {
	bus := eventbus.New()
	mgr := NewSubscriptionManager(bus)

	received := make(chan engine.Event, 10)
	mgr.OnEvent = func(instanceID uuid.UUID, event engine.Event) {
		received <- event
	}

	require.NoError(t, mgr.SubscribeInstance(testInstanceID))

	bus.Publish(testInstanceID, engine.Event{
		Type:      engine.EventTypeNodeStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "step1"},
	})

	select {
	case evt := <-received:
		assert.Equal(t, engine.EventTypeNodeStarted, evt.Type)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
```

- [ ] **Step 2: Implement SubscriptionManager**

```go
package mcp

import (
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

type SubscriptionManager struct {
	bus     engine.EventBus
	mu      sync.Mutex
	subs    map[uuid.UUID]func() // instance ID → unsubscribe function
	sysSub  func()               // system-wide unsubscribe
	OnEvent func(instanceID uuid.UUID, event engine.Event)
}

func NewSubscriptionManager(bus engine.EventBus) *SubscriptionManager {
	return &SubscriptionManager{
		bus:  bus,
		subs: make(map[uuid.UUID]func()),
	}
}

func (m *SubscriptionManager) SubscribeInstance(id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Unsubscribe existing subscription to this instance
	if unsub, exists := m.subs[id]; exists {
		unsub()
	}

	ch, unsub := m.bus.Subscribe(id)
	m.subs[id] = unsub

	go m.forwardEvents(id, ch)
	return nil
}

func (m *SubscriptionManager) UnsubscribeInstance(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if unsub, exists := m.subs[id]; exists {
		unsub()
		delete(m.subs, id)
	}
}

func (m *SubscriptionManager) SubscribeSystem() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sysSub != nil {
		m.sysSub()
	}

	ch, unsub := m.bus.SubscribeAll()
	m.sysSub = unsub

	go m.forwardSystemEvents(ch)
	return nil
}

func (m *SubscriptionManager) UnsubscribeSystem() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sysSub != nil {
		m.sysSub()
		m.sysSub = nil
	}
}

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

func (m *SubscriptionManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := len(m.subs)
	if m.sysSub != nil {
		count++
	}
	return count
}

func (m *SubscriptionManager) forwardEvents(id uuid.UUID, ch <-chan engine.Event) {
	for event := range ch {
		if m.OnEvent != nil {
			m.OnEvent(id, event)
		}
	}
}

func (m *SubscriptionManager) forwardSystemEvents(ch <-chan engine.Event) {
	for event := range ch {
		if m.OnEvent != nil {
			m.OnEvent(uuid.Nil, event)
		}
	}
}
```

- [ ] **Step 3: Run tests, verify pass**
- [ ] **Step 4: Commit**

---

### Task 1.2: Streaming Tools

- [ ] **Step 1: Write tests for subscribe/unsubscribe tools**
- [ ] **Step 2: Implement tools in `tools_streaming.go`**

Register 4 tools: `instance_subscribe`, `instance_unsubscribe`, `system_subscribe`, `system_unsubscribe`. Each tool calls the corresponding `SubscriptionManager` method.

- [ ] **Step 3: Wire OnEvent to MCP notifications**

In the `Server`, when `OnEvent` fires, translate the domain event to an MCP notification and send it to the client via the mcp-go notification mechanism.

- [ ] **Step 4: Update `register.go`**: add `RegisterStreamingTools(srv)`
- [ ] **Step 5: Run all tests, verify pass**
- [ ] **Step 6: Commit**

---

## Acceptance Criteria

- `instance_subscribe` starts receiving events for a specific instance
- `instance_unsubscribe` stops event delivery
- `system_subscribe` / `system_unsubscribe` for system-wide events
- Duplicate subscribes replace existing subscriptions (no leak)
- `CleanupAll` releases all subscriptions
- Domain events are forwarded as MCP notifications
- `node.completed` events include variable state
- All tests pass
