package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// StreamHandler handles WebSocket connections for streaming workflow events.
type StreamHandler struct {
	instances instance.InstanceRepository
	eventBus  engine.EventBus
}

// NewStreamHandler creates a new StreamHandler with its dependencies.
func NewStreamHandler(instances instance.InstanceRepository, eventBus engine.EventBus) *StreamHandler {
	return &StreamHandler{
		instances: instances,
		eventBus:  eventBus,
	}
}

// isTerminalState returns true if the given state is a final state.
func isTerminalState(state instance.State) bool {
	switch state {
	case instance.StateCompleted, instance.StateFailed, instance.StateCancelled:
		return true
	default:
		return false
	}
}

// isTerminalEvent returns true if the event represents a transition to a terminal state.
// Recognises both the engine's domain events (instance.completed, instance.failed,
// instance.cancelled) and the generic state_changed event shape.
func isTerminalEvent(evt engine.Event) bool {
	switch evt.Type {
	case "instance.completed", "instance.failed", "instance.cancelled":
		return true
	}

	if evt.Type != "state_changed" {
		return false
	}

	// Data may be a string state or a map with a "state" key.
	switch d := evt.Data.(type) {
	case string:
		return isTerminalState(instance.State(d))
	case map[string]any:
		if s, ok := d["state"].(string); ok {
			return isTerminalState(instance.State(s))
		}
	}
	return false
}

// HandleWebSocket handles GET /instances/{id}/ws to stream events over WebSocket.
func (h *StreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Parse instance ID.
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+idStr)
		return
	}

	// Verify instance exists before upgrading.
	inst, err := h.instances.Get(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	// Upgrade to WebSocket.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept writes its own HTTP error response on failure.
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	ctx := r.Context()

	// If instance is already in a terminal state, send one state_changed event and close.
	if isTerminalState(inst.State) {
		terminalEvt := engine.Event{
			Type:      "state_changed",
			Timestamp: time.Now().UTC(),
			Data:      map[string]any{"state": string(inst.State)},
		}
		h.writeEvent(ctx, conn, terminalEvt)
		conn.Close(websocket.StatusNormalClosure, "instance is terminal")
		return
	}

	// Subscribe to events for this instance.
	ch, unsub := h.eventBus.Subscribe(id)
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
				// Channel closed (e.g. bus shutdown).
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			if err := h.writeEvent(ctx, conn, evt); err != nil {
				return
			}
			// If this was a terminal state change, close the connection.
			if isTerminalEvent(evt) {
				conn.Close(websocket.StatusNormalClosure, "instance reached terminal state")
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

// writeEvent serializes an event as JSON and sends it as a WebSocket text message
// with a write deadline to prevent goroutine leaks from slow/frozen clients.
func (h *StreamHandler) writeEvent(ctx context.Context, conn *websocket.Conn, evt engine.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
