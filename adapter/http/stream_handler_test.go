package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newStreamTestServer(instRepo *mockInstanceRepo, bus engine.EventBus) *httptest.Server {
	sh := adapthttp.NewStreamHandler(instRepo, bus)

	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)

	return httptest.NewServer(r)
}

// ---------------------------------------------------------------------------
// Tests: Connect to running instance and receive events
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToRunningInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Publish an event after connection is established.
	evt := engine.Event{
		Type:      "node_started",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "A"},
	}

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(inst.ID, evt)

	// Read the event from WebSocket.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "node_started", received.Type)
}

// ---------------------------------------------------------------------------
// Tests: Connect to completed instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToCompletedInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateCompleted)
	now := time.Now()
	inst.CompletedAt = &now
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Should receive a state_changed event for the terminal state.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to failed instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToFailedInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateFailed)
	now := time.Now()
	inst.CompletedAt = &now
	failedNode := "B"
	inst.FailedNodeID = &failedNode
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Should receive a state_changed event for the terminal state.
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to cancelled instance gets state_changed and closes
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToCancelledInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateCancelled)
	now := time.Now()
	inst.CompletedAt = &now
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)

	var received engine.Event
	err = json.Unmarshal(msg, &received)
	require.NoError(t, err)
	assert.Equal(t, "state_changed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal event")
}

// ---------------------------------------------------------------------------
// Tests: Connect to nonexistent instance returns 404
// ---------------------------------------------------------------------------

func TestStreamHandler_ConnectToNonexistentInstance(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := uuid.New()
	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + id.String() + "/ws"

	// WebSocket dial should fail because server returns 404 before upgrade.
	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Tests: Invalid instance ID returns 400
// ---------------------------------------------------------------------------

func TestStreamHandler_InvalidInstanceID(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/not-a-uuid/ws"

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Tests: Client disconnect triggers clean unsubscribe
// ---------------------------------------------------------------------------

func TestStreamHandler_ClientDisconnect(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Client disconnects.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Wait a moment, then publish. Should not panic.
	time.Sleep(50 * time.Millisecond)
	assert.NotPanics(t, func() {
		bus.Publish(inst.ID, engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-disconnect",
		})
	})
}

// ---------------------------------------------------------------------------
// Tests: Terminal state_changed event during streaming closes connection
// ---------------------------------------------------------------------------

func TestStreamHandler_TerminalEventDuringStreaming(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Send a non-terminal event first.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt1 engine.Event
	require.NoError(t, json.Unmarshal(msg, &evt1))
	assert.Equal(t, "node_started", evt1.Type)

	// Now send a terminal state_changed event.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      map[string]any{"state": "completed"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var evt2 engine.Event
	require.NoError(t, json.Unmarshal(msg, &evt2))
	assert.Equal(t, "state_changed", evt2.Type)

	// Connection should close after terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after terminal state_changed")
}

// ---------------------------------------------------------------------------
// Tests: Engine domain event types close the connection
// ---------------------------------------------------------------------------

func TestStreamHandler_EngineCompletedEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Publish the engine's real completed event type.
	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.completed",
		Timestamp: time.Now(),
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.completed", received.Type)

	// Connection should close after the terminal event.
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.completed")
}

func TestStreamHandler_EngineFailedEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.failed",
		Timestamp: time.Now(),
		Data:      map[string]any{"failedNodeId": "B", "error": "node B exploded"},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.failed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.failed")
}

func TestStreamHandler_EngineCancelledEventClosesConnection(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	bus.Publish(inst.ID, engine.Event{
		Type:      "instance.cancelled",
		Timestamp: time.Now(),
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "instance.cancelled", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should be closed after instance.cancelled")
}

// ---------------------------------------------------------------------------
// Tests: Multiple events received in order
// ---------------------------------------------------------------------------

func TestStreamHandler_MultipleEventsInOrder(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// Publish 3 events.
	for i := 0; i < 3; i++ {
		bus.Publish(inst.ID, engine.Event{
			Type:      "node_completed",
			Timestamp: time.Now(),
			Data:      map[string]any{"index": float64(i)},
		})
	}

	// Read 3 events.
	for i := 0; i < 3; i++ {
		_, msg, err := conn.Read(ctx)
		require.NoError(t, err, "reading event %d", i)
		var evt engine.Event
		require.NoError(t, json.Unmarshal(msg, &evt))
		assert.Equal(t, "node_completed", evt.Type)
	}
}

// ---------------------------------------------------------------------------
// Tests: isTerminalEvent edge cases
// ---------------------------------------------------------------------------

func TestStreamHandler_StateChangedStringTerminalState(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed with a string "completed" should be treated as terminal.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      "completed",
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "connection should close after state_changed(completed)")
}

func TestStreamHandler_StateChangedStringNonTerminal(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed with "running" is NOT terminal — connection should stay open.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      "running",
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	// Send a follow-up event to confirm connection is still open.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "A"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var follow engine.Event
	require.NoError(t, json.Unmarshal(msg, &follow))
	assert.Equal(t, "node_started", follow.Type)
}

func TestStreamHandler_StateChangedMapNoStateKey(t *testing.T) {
	instRepo := newMockInstanceRepo()
	bus := eventbus.New()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	srv := newStreamTestServer(instRepo, bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + inst.ID.String() + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	// state_changed map without "state" key should NOT be terminal.
	bus.Publish(inst.ID, engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      map[string]any{"progress": float64(50)},
	})

	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var received engine.Event
	require.NoError(t, json.Unmarshal(msg, &received))
	assert.Equal(t, "state_changed", received.Type)

	// Connection should still be open — send another event to confirm.
	bus.Publish(inst.ID, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": "B"},
	})

	_, msg, err = conn.Read(ctx)
	require.NoError(t, err)
	var follow engine.Event
	require.NoError(t, json.Unmarshal(msg, &follow))
	assert.Equal(t, "node_started", follow.Type)
}

// ---------------------------------------------------------------------------
// Tests: Instance repo internal error returns 500 before upgrade
// ---------------------------------------------------------------------------

func TestStreamHandler_InstanceRepoError(t *testing.T) {
	bus := eventbus.New()

	sh := adapthttp.NewStreamHandler(&errInstanceRepo{err: fmt.Errorf("db down")}, bus)
	r := chi.NewRouter()
	r.Get("/instances/{id}/ws", sh.HandleWebSocket)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	id := uuid.New()
	wsURL := "ws" + srv.URL[len("http"):] + "/instances/" + id.String() + "/ws"

	_, resp, err := websocket.Dial(ctx, wsURL, nil)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	}
}
