package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// WorkflowService defines the engine methods needed by workflow handlers.
type WorkflowService interface {
	Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
}

// workflowStatsReader is a consumer-side interface for reading workflow
// execution statistics. It avoids importing the full InstanceRepository and
// keeps the dependency surface minimal (accept interfaces, return structs).
type workflowStatsReader interface {
	Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error)
}

// WorkflowHandler handles REST API requests for workflow operations.
type WorkflowHandler struct {
	repo       workflow.WorkflowRepository
	service    WorkflowService
	fileReader engine.FileReader
	stats      workflowStatsReader
}

// NewWorkflowHandler creates a new WorkflowHandler with its dependencies.
func NewWorkflowHandler(repo workflow.WorkflowRepository, service WorkflowService, fileReader engine.FileReader, stats workflowStatsReader) *WorkflowHandler {
	return &WorkflowHandler{
		repo:       repo,
		service:    service,
		fileReader: fileReader,
		stats:      stats,
	}
}

// triggerRequest is the JSON body for triggering a workflow.
type triggerRequest struct {
	Variables map[string]any `json:"variables"`
	Priority  string         `json:"priority"`
}

// safeWorkflowName checks that a workflow name is a plain filename component
// (no path separators, no dot-dot) to prevent directory traversal via the URL.
func safeWorkflowName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && filepath.Clean(name) == name && name != ".."
}

// List handles GET /workflows and returns a JSON array of workflow summaries.
func (h *WorkflowHandler) List(w http.ResponseWriter, r *http.Request) {
	summaries, err := h.repo.List(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list workflows")
		return
	}
	if summaries == nil {
		summaries = []workflow.WorkflowSummary{}
	}
	WriteJSON(w, http.StatusOK, summaries)
}

// Get handles GET /workflows/{name} and returns the full workflow definition.
func (h *WorkflowHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	wf, err := h.repo.Load(r.Context(), name)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load workflow")
		return
	}
	WriteJSON(w, http.StatusOK, wf)
}

// Validate handles POST /workflows/{name}/validate and returns validation results.
func (h *WorkflowHandler) Validate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	wf, err := h.repo.Load(r.Context(), name)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load workflow")
		return
	}

	errs := workflow.Validate(wf, h.fileReader)
	valid := len(errs) == 0
	if errs == nil {
		errs = []workflow.ValidationError{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"valid":  valid,
		"errors": errs,
	})
}

const maxTriggerBodyBytes = 1 << 20 // 1 MiB

// Trigger handles POST /workflows/{name}/trigger and creates a new workflow instance.
func (h *WorkflowHandler) Trigger(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	var req triggerRequest
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
			return
		}
	}

	if req.Priority == "" {
		req.Priority = "normal"
	}

	id, started, err := h.service.Trigger(name, req.Variables, req.Priority)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		if strings.Contains(err.Error(), "missing required input") {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	status := http.StatusAccepted
	state := "queued"
	if started {
		status = http.StatusCreated
		state = "running"
	}

	WriteJSON(w, status, map[string]any{
		"instanceId": id.String(),
		"state":      state,
	})
}

// saveLayoutRequest is the JSON body for saving node positions.
type saveLayoutRequest struct {
	Positions map[string]workflow.NodePosition `json:"positions"`
}

// SaveLayout handles PUT /workflows/{name}/layout and persists node positions.
func (h *WorkflowHandler) SaveLayout(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var req saveLayoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if len(req.Positions) == 0 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "positions must not be empty")
		return
	}

	if err := h.repo.SaveNodePositions(r.Context(), name, req.Positions); err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to save layout")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// Stats handles GET /workflows/{name}/stats and returns execution statistics.
func (h *WorkflowHandler) Stats(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	if h.stats == nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stats not available")
		return
	}

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30)
	to := now

	if v := r.URL.Query().Get("from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid from parameter: "+err.Error())
			return
		}
		from = parsed
	}
	if v := r.URL.Query().Get("to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid to parameter: "+err.Error())
			return
		}
		to = parsed
	}

	stats, err := h.stats.Stats(r.Context(), name, from, to)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to fetch stats")
		return
	}

	WriteJSON(w, http.StatusOK, stats)
}
