package http_test

import (
	"context"
	"encoding/json"
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
	"github.com/google/uuid"
)

func newSystemStreamTestServer(bus engine.EventBus) *httptest.Server {
	sh := adapthttp.NewSystemStreamHandler(bus)

	r := chi.NewRouter()
	r.Get("/ws", sh.HandleWebSocket)

	return httptest.NewServer(r)
}

func TestSystemStreamHandler_ReceivesEventsFromAnyInstance(t *testing.T) {
	bus := eventbus.New()

	srv := newSystemStreamTestServer(bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	id1 := uuid.New()
	id2 := uuid.New()

	bus.Publish(id1, engine.Event{
		Type:      "node_started",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "A"},
	})

	bus.Publish(id2, engine.Event{
		Type:      "node_completed",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
		Data:      map[string]any{"nodeId": "B"},
	})

	// Read first event.
	_, msg1, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt1 engine.Event
	require.NoError(t, json.Unmarshal(msg1, &evt1))
	assert.Equal(t, "node_started", evt1.Type)

	// Read second event.
	_, msg2, err := conn.Read(ctx)
	require.NoError(t, err)
	var evt2 engine.Event
	require.NoError(t, json.Unmarshal(msg2, &evt2))
	assert.Equal(t, "node_completed", evt2.Type)
}

func TestSystemStreamHandler_ClientDisconnect(t *testing.T) {
	bus := eventbus.New()

	srv := newSystemStreamTestServer(bus)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)

	// Give the handler a moment to subscribe.
	time.Sleep(50 * time.Millisecond)

	// Client disconnects.
	conn.Close(websocket.StatusNormalClosure, "bye")

	// Wait a moment, then publish. Should not panic.
	time.Sleep(50 * time.Millisecond)
	assert.NotPanics(t, func() {
		bus.Publish(uuid.New(), engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-disconnect",
		})
	})
}
