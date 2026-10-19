package http

import (
	"errors"
	"log/slog"
	"net/http"

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

	output, err := h.conversationHandler.EndSession(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
		case errors.Is(err, instance.ErrNotInteractiveWait):
			WriteError(w, http.StatusConflict, "CONFLICT", "no active interactive session")
		default:
			slog.Error("interactive: end session failed", "instanceID", id, "error", err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to end session")
		}
		return
	}

	if output == nil {
		output = map[string]any{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"output": output})
}

// AbandonSession handles POST /instances/{id}/interactive/abandon.
// Ordinary interactive sessions are ended, never abandoned.
func (h *InteractiveHandler) AbandonSession(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}
	if err := h.conversationHandler.AbandonSession(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, instance.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found")
		case errors.Is(err, instance.ErrForbidden):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", "not allowed to abandon this session")
		case errors.Is(err, engine.ErrNotResolutionSession):
			WriteError(w, http.StatusConflict, "CONFLICT", "abandon is only available for gather conflict-resolution sessions; end an interactive session instead")
		case errors.Is(err, instance.ErrNotInteractiveWait):
			WriteError(w, http.StatusConflict, "CONFLICT", "no active interactive session")
		case errors.Is(err, instance.ErrInteractiveTurnInProgress):
			WriteError(w, http.StatusConflict, "CONFLICT", "a turn is in progress; wait for the current response")
		default:
			slog.Error("interactive: abandon session failed", "instanceID", id, "error", err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to abandon session")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"status": "abandoned", "instanceId": id.String()})
}
