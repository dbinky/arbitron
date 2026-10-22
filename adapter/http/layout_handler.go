package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// maxLayoutRetryBodyBytes bounds the tiny {"expectedRevision": "..."} body.
const maxLayoutRetryBodyBytes = 1 << 16

// LayoutQueryService is the consumer-owned port onto the authorised layout
// query/retry seam (implemented by *layout.QueryService). Authorization lives
// behind it; this adapter only parses and maps errors.
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}

// LayoutHandler serves arrangement status and retry for workflows and instances.
type LayoutHandler struct {
	layouts LayoutQueryService
}

// NewLayoutHandler builds the handler. A nil service makes every route answer
// 503 so a mis-wired deployment is visible rather than silently layout-less.
func NewLayoutHandler(layouts LayoutQueryService) *LayoutHandler {
	return &LayoutHandler{layouts: layouts}
}

type layoutRetryRequest struct {
	ExpectedRevision string `json:"expectedRevision"`
}

func (h *LayoutHandler) configured(w http.ResponseWriter) bool {
	if h.layouts == nil {
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "layout service not configured")
		return false
	}
	return true
}

// GetWorkflowLayout handles GET /workflows/{name}/layout.
func (h *LayoutHandler) GetWorkflowLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	if !h.configured(w) {
		return
	}
	view, err := h.layouts.WorkflowLayout(r.Context(), name)
	if err != nil {
		writeLayoutError(w, err, "workflow not found: "+name)
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// RetryWorkflowLayout handles POST /workflows/{name}/layout/retry.
func (h *LayoutHandler) RetryWorkflowLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	if !h.configured(w) {
		return
	}
	expected, ok := decodeLayoutRetry(w, r)
	if !ok {
		return
	}
	ack, err := h.layouts.RetryWorkflow(r.Context(), name, expected)
	if err != nil {
		writeLayoutError(w, err, "workflow not found: "+name)
		return
	}
	WriteJSON(w, http.StatusAccepted, ack)
}

// GetInstanceLayout handles GET /instances/{id}/layout.
func (h *LayoutHandler) GetInstanceLayout(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if !h.configured(w) {
		return
	}
	view, err := h.layouts.InstanceLayout(r.Context(), id)
	if err != nil {
		writeLayoutError(w, err, "instance not found: "+id.String())
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// RetryInstanceLayout handles POST /instances/{id}/layout/retry.
func (h *LayoutHandler) RetryInstanceLayout(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}
	if !h.configured(w) {
		return
	}
	expected, ok := decodeLayoutRetry(w, r)
	if !ok {
		return
	}
	ack, err := h.layouts.RetryInstance(r.Context(), id, expected)
	if err != nil {
		writeLayoutError(w, err, "instance not found: "+id.String())
		return
	}
	WriteJSON(w, http.StatusAccepted, ack)
}

// decodeLayoutRetry parses the retry body, writing a 400 and returning false
// when it is malformed or carries no expectedRevision.
func decodeLayoutRetry(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLayoutRetryBodyBytes)
	var req layoutRetryRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return "", false
	}
	if req.ExpectedRevision == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "expectedRevision is required")
		return "", false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: expected one JSON object")
		return "", false
	}
	return req.ExpectedRevision, true
}

// writeMutationPending exposes only the fixed recovery reason, never storage details.
func writeMutationPending(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, workflow.ErrMutationPending) {
		return false
	}
	WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{Error: "workflow destination is awaiting durable mutation recovery; retry shortly", Code: "UNAVAILABLE"})
	return true
}

// writeLayoutError maps sentinels in precedence order, concealing not-found details.
func writeLayoutError(w http.ResponseWriter, err error, notFoundMessage string) {
	switch {
	case errors.Is(err, workflow.ErrNotFound), errors.Is(err, instance.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", notFoundMessage)
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, workflow.ErrUnauthorized):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, workflow.ErrSharedReadOnly),
		errors.Is(err, layout.ErrRevisionConflict),
		errors.Is(err, layout.ErrRetryConflict),
		errors.Is(err, layout.ErrLegacyRevision):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, workflow.ErrMutationPending):
		writeMutationPending(w, err)
	case errors.Is(err, layout.ErrStoreUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}
