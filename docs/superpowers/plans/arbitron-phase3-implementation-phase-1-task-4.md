# Phase 3.1 Task 4: Backend — System WebSocket & Instance List Since Filter

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the system-wide WebSocket endpoint (`GET /api/v1/ws`) and the `since` query parameter for instance listing so the dashboard can receive live updates and filter by time range.

**Architecture:** The `EventBus` interface gains a `SubscribeAll` method. The `InProcessEventBus` fans out to all-subscribers on every `Publish`. A new `SystemStreamHandler` uses `SubscribeAll` to broadcast to all connected WebSocket clients. The instance list query adds optional `since` filtering.

**Tech Stack:** Go, chi, nhooyr.io/websocket, sqlc

---

### Task 4.1: EventBus SubscribeAll

**Files:**
- Modify: `domain/engine/ports.go`
- Modify: `adapter/eventbus/eventbus.go`
- Modify: `adapter/eventbus/noop.go`
- Create: `adapter/eventbus/eventbus_subscribe_all_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/eventbus/eventbus_subscribe_all_test.go`:

```go
package eventbus_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscribeAll_ReceivesEventsFromAnyInstance(t *testing.T) {
	bus := eventbus.New()
	ch, unsub := bus.SubscribeAll()
	defer unsub()

	id1 := uuid.New()
	id2 := uuid.New()

	bus.Publish(id1, engine.Event{Type: "test.one", Timestamp: time.Now()})
	bus.Publish(id2, engine.Event{Type: "test.two", Timestamp: time.Now()})

	var events []engine.Event
	timeout := time.After(time.Second)
	for i := 0; i < 2; i++ {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-timeout:
			t.Fatal("timed out waiting for events")
		}
	}

	require.Len(t, events, 2)
	assert.Equal(t, "test.one", events[0].Type)
	assert.Equal(t, "test.two", events[1].Type)
}

func TestSubscribeAll_UnsubStopsDelivery(t *testing.T) {
	bus := eventbus.New()
	ch, unsub := bus.SubscribeAll()
	unsub()

	bus.Publish(uuid.New(), engine.Event{Type: "after.unsub", Timestamp: time.Now()})

	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(100 * time.Millisecond):
		// ok — no event received
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./adapter/eventbus/ -run TestSubscribeAll -v
```

Expected: compilation error — `SubscribeAll` does not exist.

- [ ] **Step 3: Add SubscribeAll to EventBus interface**

In `domain/engine/ports.go`, add to the `EventBus` interface:

```go
SubscribeAll() (<-chan Event, func())
```

- [ ] **Step 4: Implement SubscribeAll in InProcessEventBus**

In `adapter/eventbus/eventbus.go`, add a new field and method:

Add field to struct:
```go
allSubscribers []*subscriber
```

Add method:
```go
func (b *InProcessEventBus) SubscribeAll() (<-chan Event, func()) {
	ch := make(chan Event, bufferSize)
	s := &subscriber{ch: ch}

	b.mu.Lock()
	b.allSubscribers = append(b.allSubscribers, s)
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			s.closed = true

			for i, sub := range b.allSubscribers {
				if sub == s {
					b.allSubscribers[i] = b.allSubscribers[len(b.allSubscribers)-1]
					b.allSubscribers[len(b.allSubscribers)-1] = nil
					b.allSubscribers = b.allSubscribers[:len(b.allSubscribers)-1]
					break
				}
			}

			close(ch)
		})
	}

	return ch, unsub
}
```

Modify the `Publish` method to also fan out to all-subscribers:

After the existing per-instance fan-out loop, add:
```go
for _, s := range b.allSubscribers {
	if s.closed {
		continue
	}
	select {
	case s.ch <- event:
	default:
		slog.Warn("event bus dropped event for slow all-subscriber",
			"instanceID", instanceID,
			"eventType", event.Type,
		)
	}
}
```

- [ ] **Step 5: Add SubscribeAll to NoopEventBus**

In `adapter/eventbus/noop.go`, add:

```go
func (n *NoopEventBus) SubscribeAll() (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}
```

- [ ] **Step 6: Run test to verify it passes**

```bash
go test ./adapter/eventbus/ -run TestSubscribeAll -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add domain/engine/ports.go adapter/eventbus/eventbus.go adapter/eventbus/noop.go adapter/eventbus/eventbus_subscribe_all_test.go
git commit -m "feat: add SubscribeAll to EventBus for system-wide event streaming"
```

---

### Task 4.2: System-wide WebSocket handler

**Files:**
- Create: `adapter/http/system_stream_handler.go`
- Create: `adapter/http/system_stream_handler_test.go`
- Modify: `adapter/http/server.go` (add route)

- [ ] **Step 1: Write the failing test**

Create `adapter/http/system_stream_handler_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
)

func TestSystemStreamHandler_ReceivesEvents(t *testing.T) {
	bus := eventbus.New()
	handler := adapthttp.NewSystemStreamHandler(bus)

	r := chi.NewRouter()
	r.Get("/api/v1/ws", handler.HandleWebSocket)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[4:] + "/api/v1/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the subscription time to register.
	time.Sleep(50 * time.Millisecond)

	bus.Publish(uuid.New(), engine.Event{
		Type:      "instance.completed",
		Timestamp: time.Now(),
		Data:      map[string]any{"state": "completed"},
	})

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var evt engine.Event
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != "instance.completed" {
		t.Errorf("expected instance.completed, got %s", evt.Type)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./adapter/http/ -run TestSystemStreamHandler -v
```

Expected: compilation error — `NewSystemStreamHandler` does not exist.

- [ ] **Step 3: Implement SystemStreamHandler**

Create `adapter/http/system_stream_handler.go`:

```go
package http

import (
	"context"
	"encoding/json"
	"net/http"

	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
)

type SystemStreamHandler struct {
	eventBus engine.EventBus
}

func NewSystemStreamHandler(eventBus engine.EventBus) *SystemStreamHandler {
	return &SystemStreamHandler{eventBus: eventBus}
}

func (h *SystemStreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	ctx := r.Context()

	ch, unsub := h.eventBus.SubscribeAll()
	defer unsub()

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, _, err := conn.Read(ctx)
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		case <-clientDone:
			return
		case <-ctx.Done():
			conn.Close(websocket.StatusGoingAway, "server shutting down")
			return
		}
	}
}
```

- [ ] **Step 4: Register route in server.go**

In `adapter/http/server.go`, in the `NewRouter` function, add a `SystemStreamHandler` creation and route registration. Add the handler creation after `streamHandler`:

```go
sysStreamHandler := NewSystemStreamHandler(eventBus)
```

Inside the `/api/v1` route group, add:

```go
r.Get("/ws", sysStreamHandler.HandleWebSocket)
```

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./adapter/http/ -run TestSystemStreamHandler -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add adapter/http/system_stream_handler.go adapter/http/system_stream_handler_test.go adapter/http/server.go
git commit -m "feat: add system-wide WebSocket endpoint for dashboard live updates"
```

---

### Task 4.3: Instance list `since` filter

**Files:**
- Modify: `domain/instance/instance.go` (add Since to ListFilters)
- Modify: `adapter/postgres/queries/instances.sql` (add since filter)
- Modify: `adapter/postgres/instance_repo.go` (pass since param)
- Modify: `adapter/http/instance_handler.go` (parse since query param)

- [ ] **Step 1: Add Since to ListFilters**

In `domain/instance/instance.go`, add to `ListFilters`:

```go
Since *time.Time
```

- [ ] **Step 2: Update the sqlc query**

In `adapter/postgres/queries/instances.sql`, modify the `ListInstances` query to add a since filter:

```sql
-- name: ListInstances :many
SELECT id, workflow_name, state, priority, current_node_id, created_at, updated_at, completed_at, queued_at
FROM instances
WHERE (sqlc.narg('state')::instance_state IS NULL OR state = sqlc.narg('state')::instance_state)
AND (sqlc.narg('workflow_name')::text IS NULL OR workflow_name = sqlc.narg('workflow_name')::text)
AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since')::timestamptz)
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;
```

- [ ] **Step 3: Regenerate sqlc**

```bash
cd adapter/postgres && sqlc generate
```

- [ ] **Step 4: Update instance_repo.go to pass Since**

In the `List` method of `InstanceRepo`, add the `Since` parameter to the sqlc params struct. The exact field name depends on sqlc output — pass `toPgTimestamptzPtr(filters.Since)` for the `Since` parameter.

- [ ] **Step 5: Parse since query param in instance_handler.go**

In the `List` method of `InstanceHandler`, add parsing after the existing query parameter parsing:

```go
if s := r.URL.Query().Get("since"); s != "" {
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		filters.Since = &t
	}
}
```

- [ ] **Step 6: Run existing tests to verify nothing is broken**

```bash
go test ./adapter/postgres/ -v
go test ./adapter/http/ -v
```

Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add domain/instance/ adapter/postgres/ adapter/http/instance_handler.go
git commit -m "feat: add since filter to instance list endpoint"
```
