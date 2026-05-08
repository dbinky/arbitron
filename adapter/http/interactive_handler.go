package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// InteractiveHandler handles REST API requests for interactive session operations.
type InteractiveHandler struct {
	conversationHandler engine.ConversationHandler
	instances           instance.InstanceRepository
	nodeExecs           instance.NodeExecutionRepository
}

// NewInteractiveHandler creates a new InteractiveHandler with its dependencies.
func NewInteractiveHandler(
	conversationHandler engine.ConversationHandler,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
) *InteractiveHandler {
	return &InteractiveHandler{
		conversationHandler: conversationHandler,
		instances:           instances,
		nodeExecs:           nodeExecs,
	}
}

// EndSession handles POST /instances/{id}/interact/end.
// It ends the interactive session and returns the captured output.
func (h *InteractiveHandler) EndSession(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.conversationHandler.EndSession(r.Context(), id); err != nil {
		if errors.Is(err, instance.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		if strings.Contains(err.Error(), "not in interactive wait state") {
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to end session")
		return
	}

	// Load the instance to get the current node ID, then load the node
	// execution to retrieve the output.
	inst, err := h.instances.Get(r.Context(), id)
	if err != nil {
		// EndSession succeeded but we failed to load the output — return
		// success with an empty output rather than confusing the client.
		WriteJSON(w, http.StatusOK, map[string]any{"output": map[string]any{}})
		return
	}

	// After EndSession, the instance state has transitioned to "queued"
	// and CurrentNodeID still points to the interactive node. Load its
	// execution record for the output.
	var output map[string]any
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		nodeExec, err := h.nodeExecs.GetByInstanceAndNode(r.Context(), id, *inst.CurrentNodeID)
		if err == nil && nodeExec.Output != nil {
			output = nodeExec.Output
		}
	}
	if output == nil {
		output = map[string]any{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"output": output})
}
