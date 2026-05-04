# Phase 3 Task 2: EventBus & WebSocket Streaming

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the in-process event bus and WebSocket streaming handler so clients can observe running workflow instances in real time.

**Architecture:** The EventBus port is defined in `domain/engine/ports.go`. The in-process implementation lives in `adapter/eventbus/`. The WebSocket handler in `adapter/http/` subscribes to the event bus and sends events to connected clients.

**Tech Stack:** Go 1.23+, nhooyr.io/websocket

---

### Task 1: In-Process Event Bus

**Files:**
- Create: `adapter/eventbus/eventbus.go`
- Create: `adapter/eventbus/eventbus_test.go`

- [ ] **Step 1: Install websocket library**

```bash
go get nhooyr.io/websocket
```

- [ ] **Step 2: Write failing tests for the event bus**

Create `adapter/eventbus/eventbus_test.go`:

```go
package eventbus_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
)

func TestEventBus_PublishNoSubscribers(t *testing.T) {
	bus := eventbus.New()
	// Should not panic
	bus.Publish(uuid.New(), engine.Event{Type: "test", Timestamp: time.Now()})
}

func TestEventBus_SubscribeReceivesEvents(t *testing.T) {
	bus := eventbus.New()
	id := uuid.New()

	ch, unsub := bus.Subscribe(id)
	defer unsub()

	bus.Publish(id, engine.Event{Type: "node_started", Timestamp: time.Now()})

	select {
	case evt := <-ch:
		if evt.Type != "node_started" {
			t.Errorf("expected node_started, got %s", evt.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := eventbus.New()
	id := uuid.New()

	ch1, unsub1 := bus.Subscribe(id)
	defer unsub1()
	ch2, unsub2 := bus.Subscribe(id)
	defer unsub2()

	bus.Publish(id, engine.Event{Type: "test", Timestamp: time.Now()})

	for _, ch := range []<-chan engine.Event{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Type != "test" {
				t.Errorf("expected test, got %s", evt.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestEventBus_UnsubscribeStopsEvents(t *testing.T) {
	bus := eventbus.New()
	id := uuid.New()

	ch, unsub := bus.Subscribe(id)
	unsub()

	bus.Publish(id, engine.Event{Type: "test", Timestamp: time.Now()})

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("should not receive events after unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		// Expected — channel closed, no events
	}
}

func TestEventBus_DifferentInstancesIsolated(t *testing.T) {
	bus := eventbus.New()
	id1 := uuid.New()
	id2 := uuid.New()

	ch1, unsub1 := bus.Subscribe(id1)
	defer unsub1()

	bus.Publish(id2, engine.Event{Type: "other_instance", Timestamp: time.Now()})

	select {
	case <-ch1:
		t.Error("should not receive events for different instance")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestEventBus_SlowConsumerDropsEvents(t *testing.T) {
	bus := eventbus.New()
	id := uuid.New()

	ch, unsub := bus.Subscribe(id)
	defer unsub()

	// Publish more events than the buffer size (256)
	for i := 0; i < 300; i++ {
		bus.Publish(id, engine.Event{Type: "flood", Timestamp: time.Now()})
	}

	// Should not deadlock — slow consumer events are dropped
	count := 0
	for {
		select {
		case <-ch:
			count++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	if count > 256 {
		t.Errorf("expected at most 256 events (buffer size), got %d", count)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./adapter/eventbus/ -v
```

Expected: FAIL

- [ ] **Step 4: Implement the in-process event bus**

Create `adapter/eventbus/eventbus.go`:

```go
package eventbus

import (
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
)

const bufferSize = 256

type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[uuid.UUID][]chan engine.Event
}

func New() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[uuid.UUID][]chan engine.Event),
	}
}

func (b *InProcessEventBus) Publish(instanceID uuid.UUID, event engine.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs := b.subscribers[instanceID]
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// Buffer full — drop event for slow consumer
		}
	}
}

func (b *InProcessEventBus) Subscribe(instanceID uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event, bufferSize)

	b.mu.Lock()
	b.subscribers[instanceID] = append(b.subscribers[instanceID], ch)
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		subs := b.subscribers[instanceID]
		for i, sub := range subs {
			if sub == ch {
				b.subscribers[instanceID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}

	return ch, unsub
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./adapter/eventbus/ -v
```

Expected: all PASS

- [ ] **Step 6: Remove the no-op event bus file**

Delete `adapter/eventbus/noop.go` since we now have a real implementation.

- [ ] **Step 7: Commit**

```bash
git add adapter/eventbus/
git commit -m "feat: in-process event bus with buffered channels"
```

---

### Task 2: WebSocket Handler

**Files:**
- Create: `adapter/http/stream_handler.go`
- Create: `adapter/http/stream_handler_test.go`

- [ ] **Step 8: Write failing tests for WebSocket handler**

Create `adapter/http/stream_handler_test.go`. Tests using `httptest.NewServer` and `nhooyr.io/websocket`:

- Connect to running instance → receives events published to event bus
- Connect to completed instance → receives state_changed event, connection closes
- Connect to nonexistent instance → 404 (before upgrade)
- Multiple clients → all receive same events
- Client disconnect → unsubscribe called (no resource leak)

- [ ] **Step 9: Implement WebSocket handler**

Create `adapter/http/stream_handler.go`:

```go
package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

type StreamHandler struct {
	instances instance.InstanceRepository
	eventBus  engine.EventBus
}

func NewStreamHandler(instances instance.InstanceRepository, eventBus engine.EventBus) *StreamHandler {
	return &StreamHandler{instances: instances, eventBus: eventBus}
}

func (h *StreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		WriteError(w, 400, "BAD_REQUEST", "invalid instance ID")
		return
	}

	inst, err := h.instances.Get(id)
	if err != nil {
		WriteError(w, 404, "NOT_FOUND", "instance not found")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()

	// Terminal state — send current state and close
	if inst.State == instance.StateCompleted || inst.State == instance.StateFailed || inst.State == instance.StateCancelled {
		data, _ := json.Marshal(engine.Event{
			Type: "state_changed",
			Data: map[string]any{"newState": string(inst.State)},
		})
		conn.Write(ctx, websocket.MessageText, data)
		conn.Close(websocket.StatusNormalClosure, "instance is terminal")
		return
	}

	ch, unsub := h.eventBus.Subscribe(id)
	defer unsub()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "stream ended")
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
			// Close after terminal state
			if evt.Type == "state_changed" {
				if d, ok := evt.Data.(map[string]any); ok {
					if s, ok := d["newState"].(string); ok {
						if s == "completed" || s == "failed" || s == "cancelled" {
							conn.Close(websocket.StatusNormalClosure, "instance terminal")
							return
						}
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 10: Add WebSocket route to router**

Update `adapter/http/server.go` to add the WebSocket route:

```go
sh := NewStreamHandler(instances, eventBus)

// Inside the instances route group:
r.Get("/{id}/ws", sh.HandleWebSocket)
```

- [ ] **Step 11: Run tests**

```bash
go test ./adapter/http/ -v -run TestStream
```

Expected: all PASS

- [ ] **Step 12: Commit**

```bash
git add adapter/http/stream_handler* adapter/http/server.go
git commit -m "feat: WebSocket streaming handler"
```
