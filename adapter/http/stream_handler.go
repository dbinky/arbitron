package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// inboundMessage represents a JSON message received from the WebSocket client.
type inboundMessage struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
}

// interactiveErrorEvent is sent back to the client when an interactive
// operation fails.
type interactiveErrorEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		Message string `json:"message"`
	} `json:"data"`
}

// StreamHandler handles WebSocket connections for streaming workflow events.
type StreamHandler struct {
	instances           instance.InstanceRepository
	nodeExecs           instance.NodeExecutionRepository
	eventBus            engine.EventBus
	conversationHandler engine.ConversationHandler
}

// NewStreamHandler creates a new StreamHandler with its dependencies.
// nodeExecs and conversationHandler may be nil if interactive messaging is not needed.
func NewStreamHandler(instances instance.InstanceRepository, nodeExecs instance.NodeExecutionRepository, eventBus engine.EventBus, conversationHandler engine.ConversationHandler) *StreamHandler {
	return &StreamHandler{
		instances:           instances,
		nodeExecs:           nodeExecs,
		eventBus:            eventBus,
		conversationHandler: conversationHandler,
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
	case engine.EventTypeInstanceCompleted, engine.EventTypeInstanceFailed, engine.EventTypeInstanceCancelled:
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

	// History hydration: if the instance is in interactive wait, send
	// the full conversation history as the first WebSocket event.
	h.sendInteractiveHistory(ctx, conn, inst)

	// writeMu coordinates WebSocket writes between the outbound event loop
	// and the read goroutine (which may send interactive.error events).
	var writeMu sync.Mutex

	// Start a goroutine to read and route inbound messages.
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			h.handleInbound(ctx, conn, &writeMu, id, data)
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
			writeMu.Lock()
			err := h.writeEvent(ctx, conn, evt)
			writeMu.Unlock()
			if err != nil {
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

// handleInbound parses an inbound JSON message and routes it to the appropriate
// handler. Unknown message types are silently ignored for forward compatibility.
func (h *StreamHandler) handleInbound(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, instanceID uuid.UUID, data []byte) {
	var msg inboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("stream: ignoring malformed inbound message", "error", err)
		return
	}

	if h.conversationHandler == nil {
		// No conversation handler wired — ignore interactive messages.
		return
	}

	var handlerErr error
	switch msg.Type {
	case "interactive.send":
		handlerErr = h.conversationHandler.HandleMessage(ctx, instanceID, msg.Content)
	case "interactive.end":
		handlerErr = h.conversationHandler.EndSession(ctx, instanceID)
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}

	if handlerErr != nil {
		errEvt := interactiveErrorEvent{
			Type:      engine.EventTypeInteractiveError,
			Timestamp: time.Now().UTC(),
		}
		errEvt.Data.Message = handlerErr.Error()

		errData, err := json.Marshal(errEvt)
		if err != nil {
			slog.Error("stream: failed to marshal interactive.error", "error", err)
			return
		}

		writeMu.Lock()
		writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
		writeErr := conn.Write(writeCtx, websocket.MessageText, errData)
		cancel()
		writeMu.Unlock()

		if writeErr != nil {
			slog.Error("stream: failed to write interactive.error", "error", writeErr)
		}
	}
}

// sendInteractiveHistory checks if the instance is in interactive wait state
// and, if so, sends an interactive.history event with the full conversation.
func (h *StreamHandler) sendInteractiveHistory(ctx context.Context, conn *websocket.Conn, inst *instance.Instance) {
	if inst.State != instance.StateWaiting {
		return
	}
	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return
	}
	if h.nodeExecs == nil {
		return
	}

	nodeID := *inst.CurrentNodeID

	// Check if the current node is interactive by parsing the workflow snapshot.
	if !isInteractiveNode(inst.WorkflowSnapshot, nodeID) {
		return
	}

	// Load the node execution record.
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, inst.ID, nodeID)
	if err != nil {
		slog.Warn("stream: failed to load node execution for history hydration", "instanceId", inst.ID, "nodeId", nodeID, "error", err)
		return
	}

	// Extract messages from llm_conversation.
	var messages any
	if nodeExec.LLMConversation != nil {
		if msgs, ok := nodeExec.LLMConversation["messages"]; ok {
			messages = msgs
		}
	}
	if messages == nil {
		messages = []any{}
	}

	historyEvt := engine.Event{
		Type:      engine.EventTypeInteractiveHistory,
		Timestamp: time.Now().UTC(),
		Data: map[string]any{
			"nodeId":   nodeID,
			"messages": messages,
		},
	}
	if err := h.writeEvent(ctx, conn, historyEvt); err != nil {
		slog.Warn("stream: failed to send interactive.history", "instanceId", inst.ID, "error", err)
	}
}

// isInteractiveNode checks if the specified node in the workflow snapshot is
// of type "interactive".
func isInteractiveNode(snapshot map[string]any, nodeID string) bool {
	if snapshot == nil {
		return false
	}
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return false
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return false
	}
	for _, node := range wf.Nodes {
		if node.ID == nodeID {
			return node.Type == workflow.NodeTypeInteractive
		}
	}
	return false
}

// writeEvent serializes an event as JSON and sends it as a WebSocket text message
// with a write deadline to prevent goroutine leaks from slow/frozen clients.
// Callers must hold writeMu if concurrent writes are possible.
func (h *StreamHandler) writeEvent(ctx context.Context, conn *websocket.Conn, evt engine.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
