package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/workflow"
)

// WorkflowService defines the engine methods needed by workflow handlers.
type WorkflowService interface {
	Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, error)
}

// WorkflowHandler handles REST API requests for workflow operations.
type WorkflowHandler struct {
	repo       workflow.WorkflowRepository
	service    WorkflowService
	fileReader engine.FileReader
}

// NewWorkflowHandler creates a new WorkflowHandler with its dependencies.
func NewWorkflowHandler(repo workflow.WorkflowRepository, service WorkflowService, fileReader engine.FileReader) *WorkflowHandler {
	return &WorkflowHandler{
		repo:       repo,
		service:    service,
		fileReader: fileReader,
	}
}

// triggerRequest is the JSON body for triggering a workflow.
type triggerRequest struct {
	Variables map[string]any `json:"variables"`
	Priority  string         `json:"priority"`
}

// List handles GET /workflows and returns a JSON array of workflow summaries.
func (h *WorkflowHandler) List(w http.ResponseWriter, r *http.Request) {
	summaries, err := h.repo.List()
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
	wf, err := h.repo.Load(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
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
	wf, err := h.repo.Load(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
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

// Trigger handles POST /workflows/{name}/trigger and creates a new workflow instance.
func (h *WorkflowHandler) Trigger(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var req triggerRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
			return
		}
	}

	if req.Priority == "" {
		req.Priority = "normal"
	}

	id, err := h.service.Trigger(name, req.Variables, req.Priority)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
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

	WriteJSON(w, http.StatusCreated, map[string]any{
		"instanceId": id.String(),
		"state":      "running",
	})
}
