package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// InstanceService defines the engine methods needed by instance handlers.
type InstanceService interface {
	ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error)
	GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	Pause(id uuid.UUID) error
	Resume(id uuid.UUID) error
	Cancel(id uuid.UUID) error
	Retry(id uuid.UUID, variableEdits map[string]any) error
	Rerun(id uuid.UUID) (uuid.UUID, error)
	SetPriority(id uuid.UUID, priority string) error
}

// InstanceHandler handles REST API requests for instance operations.
type InstanceHandler struct {
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	snapshots  instance.VariableSnapshotRepository
	service    InstanceService
}

// NewInstanceHandler creates a new InstanceHandler with its dependencies.
func NewInstanceHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	service InstanceService,
) *InstanceHandler {
	return &InstanceHandler{
		instances: instances,
		nodeExecs: nodeExecs,
		snapshots: snapshots,
		service:   service,
	}
}

// instanceResponse is a JSON-friendly representation of an instance.
type instanceResponse struct {
	ID                string         `json:"id"`
	OwnerID           string         `json:"ownerId"`
	WorkflowName      string         `json:"workflowName"`
	State             string         `json:"state"`
	Priority          string         `json:"priority"`
	Variables         map[string]any `json:"variables"`
	OriginalVariables map[string]any `json:"originalVariables"`
	CurrentNodeID     *string        `json:"currentNodeId,omitempty"`
	FailedNodeID      *string        `json:"failedNodeId,omitempty"`
	CreatedAt         string         `json:"createdAt"`
	UpdatedAt         string         `json:"updatedAt"`
	CompletedAt       *string        `json:"completedAt,omitempty"`
	QueuedAt          *string        `json:"queuedAt,omitempty"`
	WaitingUntil      *string        `json:"waitingUntil,omitempty"`
	WebhookURL        *string        `json:"webhookUrl,omitempty"`
	WebhookReceived   bool           `json:"webhookReceived,omitempty"`
	WorkflowSnapshot  map[string]any `json:"workflowSnapshot,omitempty"`
	InteractiveNodeID *string        `json:"interactiveNodeId"`
	InteractiveActive bool           `json:"interactiveActive"`
}

func toInstanceResponse(inst *instance.Instance) instanceResponse {
	resp := instanceResponse{
		ID:                inst.ID.String(),
		OwnerID:           inst.OwnerID.String(),
		WorkflowName:      inst.WorkflowName,
		State:             string(inst.State),
		Priority:          string(inst.Priority),
		Variables:         inst.Variables,
		OriginalVariables: inst.OriginalVariables,
		CurrentNodeID:     inst.CurrentNodeID,
		FailedNodeID:      inst.FailedNodeID,
		CreatedAt:         inst.CreatedAt.Format(APITimeFormat),
		UpdatedAt:         inst.UpdatedAt.Format(APITimeFormat),
	}
	resp.WorkflowSnapshot = inst.WorkflowSnapshot
	if inst.CompletedAt != nil {
		s := inst.CompletedAt.Format(APITimeFormat)
		resp.CompletedAt = &s
	}
	if inst.QueuedAt != nil {
		s := inst.QueuedAt.Format(APITimeFormat)
		resp.QueuedAt = &s
	}
	if inst.WaitingUntil != nil {
		s := inst.WaitingUntil.Format(APITimeFormat)
		resp.WaitingUntil = &s
	}
	if inst.WebhookToken != nil {
		url := fmt.Sprintf("/api/v1/webhooks/wait/%s", *inst.WebhookToken)
		resp.WebhookURL = &url
	}
	if inst.WebhookReceivedAt != nil {
		resp.WebhookReceived = true
	}

	// Derive interactive state fields.
	if inst.State == instance.StateWaiting && inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		if isInteractiveNode(inst.WorkflowSnapshot, *inst.CurrentNodeID) {
			nodeID := *inst.CurrentNodeID
			resp.InteractiveNodeID = &nodeID
			resp.InteractiveActive = true
		}
	}

	return resp
}

// isInteractiveNode checks if the specified node in the workflow snapshot is
// of type "interactive". Uses the shared engine.FindNodeByID helper.
func isInteractiveNode(snapshot map[string]any, nodeID string) bool {
	node, err := engine.FindNodeByID(snapshot, nodeID)
	if err != nil {
		return false
	}
	return workflow.IsInteractive(node.Type)
}

// retryRequest is the optional JSON body for retrying an instance.
type retryRequest struct {
	Variables map[string]any `json:"variables"`
}

// parseInstanceID extracts and validates the instance UUID from the URL.
func parseInstanceID(r *http.Request) (uuid.UUID, error) {
	idStr := chi.URLParam(r, "id")
	return uuid.Parse(idStr)
}

// mapEngineError maps an engine error to the appropriate HTTP status code.
func mapEngineError(err error) int {
	if errors.Is(err, instance.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, instance.ErrNotRunning) ||
		errors.Is(err, instance.ErrNotPaused) ||
		errors.Is(err, instance.ErrNotFailed) ||
		errors.Is(err, instance.ErrNotQueued) ||
		errors.Is(err, instance.ErrNotWaiting) ||
		errors.Is(err, instance.ErrCannotCancel) ||
		errors.Is(err, instance.ErrCannotRerun) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// mapEngineErrorCode maps an engine error to an error code string.
func mapEngineErrorCode(err error) string {
	status := mapEngineError(err)
	switch status {
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	default:
		return "INTERNAL_ERROR"
	}
}

const (
	defaultListLimit   = 50
	maxListLimit       = 500
	maxRetryBodyBytes  = 1 << 20 // 1 MiB
)

// List handles GET /instances with optional query params for filtering.
func (h *InstanceHandler) List(w http.ResponseWriter, r *http.Request) {
	filters := instance.ListFilters{
		Limit: defaultListLimit,
	}

	if s := r.URL.Query().Get("state"); s != "" {
		state := instance.State(s)
		filters.State = &state
	}
	if wn := r.URL.Query().Get("workflowName"); wn != "" {
		filters.WorkflowName = &wn
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			if parsed > maxListLimit {
				parsed = maxListLimit
			}
			filters.Limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			filters.Offset = parsed
		}
	}
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			filters.Since = &t
		}
	}

	summaries, err := h.service.ListInstances(r.Context(), filters)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			// No identity in context (defence-in-depth); reveal nothing.
			WriteJSON(w, http.StatusOK, []instance.InstanceSummary{})
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}
	if summaries == nil {
		summaries = []instance.InstanceSummary{}
	}
	WriteJSON(w, http.StatusOK, summaries)
}

// Get handles GET /instances/{id} and returns the full instance.
func (h *InstanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID: "+chi.URLParam(r, "id"))
		return
	}

	inst, err := h.service.GetInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	WriteJSON(w, http.StatusOK, toInstanceResponse(inst))
}

// Pause handles POST /instances/{id}/pause.
func (h *InstanceHandler) Pause(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Pause(id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// Resume handles POST /instances/{id}/resume.
func (h *InstanceHandler) Resume(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Resume(id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// Cancel handles POST /instances/{id}/cancel.
func (h *InstanceHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	if err := h.service.Cancel(id); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// Retry handles POST /instances/{id}/retry with optional variable edits.
func (h *InstanceHandler) Retry(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	var req retryRequest
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
			return
		}
	}

	if err := h.service.Retry(id, req.Variables); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "retrying"})
}

// Rerun handles POST /instances/{id}/rerun.
func (h *InstanceHandler) Rerun(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	newID, err := h.service.Rerun(id)
	if err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"instanceId": newID.String(),
		"state":      "running",
	})
}

// nodeExecutionResponse is a JSON-friendly representation of a node execution.
type nodeExecutionResponse struct {
	ID              string         `json:"id"`
	InstanceID      string         `json:"instanceId"`
	NodeID          string         `json:"nodeId"`
	Attempt         int            `json:"attempt"`
	Status          string         `json:"status"`
	ResolvedInputs  map[string]any `json:"resolvedInputs,omitempty"`
	Output          map[string]any `json:"output,omitempty"`
	Error           map[string]any `json:"error,omitempty"`
	LLMConversation map[string]any `json:"llmConversation,omitempty"`
	LLMModel        *string        `json:"llmModel,omitempty"`
	LLMTokensIn     *int           `json:"llmTokensIn,omitempty"`
	LLMTokensOut    *int           `json:"llmTokensOut,omitempty"`
	ScriptStdout    *string        `json:"scriptStdout,omitempty"`
	ScriptStderr    *string        `json:"scriptStderr,omitempty"`
	ScriptExitCode  *int           `json:"scriptExitCode,omitempty"`
	StartedAt       string         `json:"startedAt"`
	CompletedAt     *string        `json:"completedAt,omitempty"`
}

func toNodeExecutionResponse(exec *instance.NodeExecution) nodeExecutionResponse {
	resp := nodeExecutionResponse{
		ID:              exec.ID.String(),
		InstanceID:      exec.InstanceID.String(),
		NodeID:          exec.NodeID,
		Attempt:         exec.Attempt,
		Status:          string(exec.Status),
		ResolvedInputs:  exec.ResolvedInputs,
		Output:          exec.Output,
		Error:           exec.Error,
		LLMConversation: exec.LLMConversation,
		LLMModel:        exec.LLMModel,
		LLMTokensIn:     exec.LLMTokensIn,
		LLMTokensOut:    exec.LLMTokensOut,
		ScriptStdout:    exec.ScriptStdout,
		ScriptStderr:    exec.ScriptStderr,
		ScriptExitCode:  exec.ScriptExitCode,
		StartedAt:       exec.StartedAt.Format(APITimeFormat),
	}
	if exec.CompletedAt != nil {
		s := exec.CompletedAt.Format(APITimeFormat)
		resp.CompletedAt = &s
	}
	return resp
}

// GetTrace handles GET /instances/{id}/trace and returns ordered execution records.
func (h *InstanceHandler) GetTrace(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	execs, err := h.service.GetTrace(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list trace records")
		return
	}

	resp := make([]nodeExecutionResponse, len(execs))
	for i, exec := range execs {
		resp[i] = toNodeExecutionResponse(&exec)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// GetNodeTrace handles GET /instances/{id}/trace/{nodeId} and returns a single node execution.
func (h *InstanceHandler) GetNodeTrace(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	exec, err := h.service.GetNodeTrace(r.Context(), id, nodeID)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			// Covers both an unauthorized/absent instance and a missing node
			// execution; the message stays node-oriented to match prior behavior.
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "node execution not found: "+nodeID)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get node execution")
		return
	}

	WriteJSON(w, http.StatusOK, toNodeExecutionResponse(exec))
}

// variableSnapshotResponse is a JSON-friendly representation of a variable snapshot.
type variableSnapshotResponse struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instanceId"`
	NodeID     string         `json:"nodeId"`
	Variables  map[string]any `json:"variables"`
	CapturedAt string         `json:"capturedAt"`
}

func toVariableSnapshotResponse(snap *instance.VariableSnapshot) variableSnapshotResponse {
	return variableSnapshotResponse{
		ID:         snap.ID.String(),
		InstanceID: snap.InstanceID.String(),
		NodeID:     snap.NodeID,
		Variables:  snap.Variables,
		CapturedAt: snap.CapturedAt.Format(APITimeFormat),
	}
}

// GetVariables handles GET /instances/{id}/variables and returns current instance variables.
func (h *InstanceHandler) GetVariables(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	vars, err := h.service.GetVariables(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}

	WriteJSON(w, http.StatusOK, vars)
}

// GetVariableHistory handles GET /instances/{id}/variables/history and returns
// the ordered history of variable snapshots.
func (h *InstanceHandler) GetVariableHistory(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	snaps, err := h.service.GetVariableHistory(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list variable history")
		return
	}

	resp := make([]variableSnapshotResponse, len(snaps))
	for i, snap := range snaps {
		resp[i] = toVariableSnapshotResponse(&snap)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// priorityRequest is the JSON body for changing instance priority.
type priorityRequest struct {
	Priority string `json:"priority"`
}

// SetPriority handles POST /instances/{id}/priority.
func (h *InstanceHandler) SetPriority(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}

	var req priorityRequest
	if r.Body == nil || r.ContentLength == 0 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "request body required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRetryBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	// Validate priority value.
	switch req.Priority {
	case "high", "normal", "low":
		// valid
	default:
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid priority: must be high, normal, or low")
		return
	}

	if err := h.service.SetPriority(id, req.Priority); err != nil {
		status := mapEngineError(err)
		WriteError(w, status, mapEngineErrorCode(err), err.Error())
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "priority_updated"})
}
