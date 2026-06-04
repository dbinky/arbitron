package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// wsWriteTimeout is the maximum time allowed for a single WebSocket write.
// This prevents goroutine leaks from frozen or slow clients.
const wsWriteTimeout = 10 * time.Second

// SystemStreamHandler handles WebSocket connections for streaming all workflow events
// system-wide, regardless of instance.
type SystemStreamHandler struct {
	eventBus  engine.EventBus
	instances instance.InstanceRepository
}

// NewSystemStreamHandler creates a new SystemStreamHandler with its dependencies.
// instances is used to resolve per-event instance ownership so a non-admin only
// receives events for instances they own.
func NewSystemStreamHandler(eventBus engine.EventBus, instances instance.InstanceRepository) *SystemStreamHandler {
	return &SystemStreamHandler{
		eventBus:  eventBus,
		instances: instances,
	}
}

// HandleWebSocket handles GET /ws to stream all events over WebSocket, scoped to
// the caller's visible set: admins receive every event; a non-admin receives
// only events for instances they own.
func (h *SystemStreamHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept writes its own HTTP error response on failure.
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")

	ctx := r.Context()

	ch, unsub := h.eventBus.SubscribeAll()
	defer unsub()

	// Per-connection ownership cache: instanceID -> visible? Resolved lazily so
	// each instance ID is looked up at most once per connection. An ownership
	// change mid-connection is not observed until the client reconnects.
	visible := make(map[uuid.UUID]bool)

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
		case sev, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			if !h.canSee(ctx, id, sev.InstanceID, visible) {
				continue // drop events for instances this caller may not see
			}
			if err := writeWSEvent(ctx, conn, sev.Event); err != nil {
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

// canSee reports whether the identity may receive events for instanceID,
// memoizing the per-connection result. Admins see all. The lookup fails closed:
// if ownership cannot be resolved, the event is dropped.
func (h *SystemStreamHandler) canSee(ctx context.Context, id identity.Identity, instanceID uuid.UUID, cache map[uuid.UUID]bool) bool {
	if id.IsAdmin() {
		return true
	}
	if v, ok := cache[instanceID]; ok {
		return v
	}
	inst, err := h.instances.Get(ctx, instanceID)
	v := err == nil && inst != nil && inst.OwnerID == id.UserID
	cache[instanceID] = v
	return v
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
