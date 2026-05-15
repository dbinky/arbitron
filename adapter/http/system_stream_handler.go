package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
)

// wsWriteTimeout is the maximum time allowed for a single WebSocket write.
// This prevents goroutine leaks from frozen or slow clients.
const wsWriteTimeout = 10 * time.Second

// SystemStreamHandler handles WebSocket connections for streaming all workflow events
// system-wide, regardless of instance.
type SystemStreamHandler struct {
	eventBus engine.EventBus
}

// NewSystemStreamHandler creates a new SystemStreamHandler with its dependencies.
func NewSystemStreamHandler(eventBus engine.EventBus) *SystemStreamHandler {
	return &SystemStreamHandler{
		eventBus: eventBus,
	}
}

// HandleWebSocket handles GET /ws to stream all events over WebSocket.
func (h *SystemStreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept writes its own HTTP error response on failure.
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	ctx := r.Context()

	ch, unsub := h.eventBus.SubscribeAll()
	defer unsub()

	// Start a goroutine to detect client disconnect via reading.
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

	// Stream events to the client.
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			if err := writeWSEvent(ctx, conn, evt); err != nil {
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

// writeWSEvent serializes an event as JSON and sends it as a WebSocket text
// message with a write deadline to prevent goroutine leaks from slow/frozen
// clients. Shared by StreamHandler and SystemStreamHandler.
func writeWSEvent(ctx context.Context, conn *websocket.Conn, evt engine.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
