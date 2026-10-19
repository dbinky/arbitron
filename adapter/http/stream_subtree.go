package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// SubtreeInactiveEventType is the final frame of a subtree stream: the whole
// visible subtree is inactive, the server closes normally, and the client
// refetches the final aggregate state.
const SubtreeInactiveEventType = "subtree.inactive"

// defaultSubtreeRecheck bounds how long a dropped final event (the bus drops on
// a full buffer) or a transition with no emitter can keep a stream open.
const defaultSubtreeRecheck = 15 * time.Second

// SubtreeSubscriber is the authorized, ancestry-routed event source behind
// ?subtree=1 (engine.SubtreeEventBridge).
type SubtreeSubscriber interface {
	SubscribeSubtree(ctx context.Context, rootID uuid.UUID) (<-chan engine.ScopedEvent, func(), error)
	InSubtree(ctx context.Context, rootID, id uuid.UUID) bool
	EventVisible(ctx context.Context, sourceID uuid.UUID, evt engine.Event) bool
}

// subtreeWSFrame is the subtree-mode wire frame: the engine.Event fields plus
// the instance that emitted it.
type subtreeWSFrame struct {
	Type             string    `json:"type"`
	Timestamp        time.Time `json:"timestamp"`
	Data             any       `json:"data"`
	SourceInstanceID uuid.UUID `json:"sourceInstanceId"`
}

func subtreeFrame(source uuid.UUID, evt engine.Event) subtreeWSFrame {
	return subtreeWSFrame{Type: evt.Type, Timestamp: evt.Timestamp, Data: evt.Data, SourceInstanceID: source}
}

// SetSubtreeStreaming wires subtree mode. activity supplies GraphState.Active
// for the subscribed root (the same authorized activity the HTTP read
// returns). recheck <= 0 uses defaultSubtreeRecheck.
func (h *StreamHandler) SetSubtreeStreaming(sub SubtreeSubscriber, activity GraphStateService, recheck time.Duration) {
	if recheck <= 0 {
		recheck = defaultSubtreeRecheck
	}
	h.subtree = sub
	h.activity = activity
	h.subtreeRecheck = recheck
}

// handleSubtree serves ?subtree=1 for an already-authorized root.
func (h *StreamHandler) handleSubtree(w http.ResponseWriter, r *http.Request, root *instance.Instance) {
	if h.subtree == nil || h.activity == nil {
		WriteError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "subtree streaming is not available")
		return
	}
	ctx := r.Context()

	// Subscribe BEFORE the first activity check and before upgrading, so no
	// transition between the check and the subscription can be missed.
	ch, unsub, err := h.subtree.SubscribeSubtree(ctx, root.ID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to subscribe")
		return
	}
	defer unsub()

	// Decide connect-time activity BEFORE upgrading: once the client sees the
	// handshake it may act (and the subtree may change), and every change after
	// this point reaches the already-open subscription.
	activeAtConnect, rootState, connectState := h.subtreeActivityState(ctx, root.ID)

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept writes its own HTTP error response on failure.
		return
	}
	defer conn.Close(websocket.StatusInternalError, "unexpected close")
	conn.SetReadLimit(wsMaxReadMessageSize)

	// writeMu coordinates writes between the outbound loop and the read
	// goroutine (which may send interactive.error frames).
	var writeMu sync.Mutex
	write := func(f subtreeWSFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeWSValue(ctx, conn, f)
	}

	if !activeAtConnect {
		h.closeSubtreeInactive(conn, write, root.ID, rootState)
		return
	}

	for _, f := range h.subtreeHistory(ctx, root, connectState) {
		if err := write(f); err != nil {
			return
		}
	}

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			h.handleSubtreeInbound(ctx, conn, &writeMu, root.ID, data)
		}
	}()

	ticker := time.NewTicker(h.subtreeRecheck)
	defer ticker.Stop()
	for {
		select {
		case sev, ok := <-ch:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "event stream ended")
				return
			}
			if err := write(subtreeFrame(sev.InstanceID, sev.Event)); err != nil {
				return
			}
			if !isTerminalEvent(sev.Event) {
				continue
			}
			// A terminal descendant — or a terminal root with active
			// descendants — never ends the stream on its own.
			if active, state := h.subtreeActivity(ctx, root.ID); !active {
				h.closeSubtreeInactive(conn, write, root.ID, state)
				return
			}
		case <-ticker.C:
			if active, state := h.subtreeActivity(ctx, root.ID); !active {
				h.closeSubtreeInactive(conn, write, root.ID, state)
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

// subtreeActivity reports whether any visible context of rootID's subtree is
// still active, plus the root's state for the final frame. A root that became
// unviewable or vanished is inactive; any other read failure keeps the stream
// open (it carries only authorized events) until the next check.
func (h *StreamHandler) subtreeActivity(ctx context.Context, rootID uuid.UUID) (bool, string) {
	active, state, _ := h.subtreeActivityState(ctx, rootID)
	return active, state
}

// subtreeActivityState is subtreeActivity plus the aggregate it read (nil when
// unavailable), so connect-time hydration reuses the same walk.
func (h *StreamHandler) subtreeActivityState(ctx context.Context, rootID uuid.UUID) (bool, string, *engine.GraphState) {
	gs, err := h.activity.GraphState(ctx, rootID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			return false, "", nil
		}
		slog.Warn("stream: subtree activity check failed; keeping the stream open", "instanceId", rootID, "error", err)
		return true, "", nil
	}
	// An empty aggregate has no root observation and cannot prove inactivity.
	// In particular, its zero-value Active must not end a live subscription.
	if gs == nil || len(gs.Contexts) == 0 {
		return true, "", nil
	}
	return gs.Active, gs.Contexts[0].State, gs
}

// subtreeHistory hydrates every open session in the subtree, root first, so a
// socket opened on an ancestor (or reconnecting mid-session) receives the
// history of a descendant's interactive or resolution session before its
// live tokens. Each descendant is re-read under the caller's identity, must
// still be inside the subtree, and its frame passes the same EventVisible
// boundary as live events. Without an aggregate only the root is hydrated.
func (h *StreamHandler) subtreeHistory(ctx context.Context, root *instance.Instance, gs *engine.GraphState) []subtreeWSFrame {
	var frames []subtreeWSFrame
	add := func(inst *instance.Instance) {
		if evt, ok := h.interactiveHistoryEvent(ctx, inst); ok && h.subtree.EventVisible(ctx, inst.ID, evt) {
			frames = append(frames, subtreeFrame(inst.ID, evt))
		}
	}
	add(root)
	if gs == nil {
		return frames
	}
	seen := map[uuid.UUID]bool{root.ID: true}
	for _, c := range gs.Contexts {
		if c.Redacted || c.InstanceID == uuid.Nil || seen[c.InstanceID] || c.State != string(instance.StateWaiting) {
			continue
		}
		seen[c.InstanceID] = true
		if !h.subtree.InSubtree(ctx, root.ID, c.InstanceID) {
			continue
		}
		inst, err := h.service.GetInstance(ctx, c.InstanceID)
		if err != nil || inst == nil {
			continue
		}
		add(inst)
	}
	return frames
}

func (h *StreamHandler) closeSubtreeInactive(conn *websocket.Conn, write func(subtreeWSFrame) error, rootID uuid.UUID, state string) {
	frame := subtreeWSFrame{
		Type:             SubtreeInactiveEventType,
		Timestamp:        time.Now().UTC(),
		SourceInstanceID: rootID,
		Data:             map[string]any{"rootInstanceId": rootID.String(), "state": state},
	}
	if err := write(frame); err != nil {
		return
	}
	conn.Close(websocket.StatusNormalClosure, "subtree inactive")
}

// handleSubtreeInbound routes an interactive control by its EXPLICIT target
// instance — never by whichever descendant emitted last. The target must be
// the root or inside its subtree AND viewable by the caller; the conversation
// handler then applies its own intervention checks.
func (h *StreamHandler) handleSubtreeInbound(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, rootID uuid.UUID, data []byte) {
	var msg inboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		slog.Warn("stream: ignoring malformed inbound message", "error", err)
		return
	}
	if h.conversationHandler == nil {
		return
	}
	switch msg.Type {
	case "interactive.send", "interactive.end", "interactive.abandon":
	default:
		// Unknown type — silently ignore for forward compatibility.
		return
	}
	target, err := uuid.Parse(msg.InstanceID)
	if err != nil {
		h.writeInteractiveError(ctx, conn, writeMu, "instanceId is required on a subtree stream", &rootID)
		return
	}
	if !h.subtreeTargetAllowed(ctx, rootID, target) {
		// Outside the subtree, unviewable and unknown all read the same.
		h.writeInteractiveError(ctx, conn, writeMu, "Session not found", &target)
		return
	}
	h.routeInteractive(ctx, conn, writeMu, target, msg, &target)
}

func (h *StreamHandler) subtreeTargetAllowed(ctx context.Context, rootID, target uuid.UUID) bool {
	if target != rootID && !h.subtree.InSubtree(ctx, rootID, target) {
		return false
	}
	_, err := h.service.GetInstance(ctx, target)
	return err == nil
}
