package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// callerOwner returns the authenticated caller's user ID from context. The
// Phase 1 auth middleware guarantees an identity is present on every request
// reaching these handlers.
func callerOwner(ctx context.Context) uuid.UUID {
	return identity.MustFromContext(ctx).UserID
}

// callerPrivate returns the caller's private storage scope.
func callerPrivate(ctx context.Context) workflow.Scope {
	return workflow.Private(callerOwner(ctx))
}

// ownerBoundChecker adapts a scope-aware engine.FileReader to the owner-blind
// workflow.FileChecker by binding the caller's owner, so validation searches
// the caller's private workspace first then shared.
type ownerBoundChecker struct {
	fr    engine.FileReader
	owner uuid.UUID
}

func (c ownerBoundChecker) FileExists(workflowName, relativePath string) bool {
	return c.fr.FileExists(c.owner, workflowName, relativePath)
}

// writeSharedReadOnlyOrNotFound maps a write-path error to the right HTTP
// status: shared-read-only → 409, not-found → 404; returns false if the error
// is neither (caller handles the remaining cases).
func writeSharedReadOnlyOrNotFound(w http.ResponseWriter, name string, err error) bool {
	switch {
	case errors.Is(err, workflow.ErrSharedReadOnly):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
		return true
	case errors.Is(err, workflow.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return true
	}
	return false
}

// TriggerService defines the engine trigger method needed by workflow handlers.
// The request context carries the triggering identity (injected by the auth
// middleware) so the engine resolves the caller's visible scopes and stamps the
// triggering user as the instance owner.
type TriggerService interface {
	Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
}

// WorkflowService is an alias retained for the EngineService composition in
// server.go; it carries the Trigger method the engine satisfies.
type WorkflowService = TriggerService

// workflowStatsReader is a consumer-side interface for reading workflow
// execution statistics. It avoids importing the full InstanceRepository and
// keeps the dependency surface minimal (accept interfaces, return structs).
type workflowStatsReader interface {
	Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error)
}

// WorkflowHandler handles REST API requests for workflow operations.
type WorkflowHandler struct {
	repo           workflow.WorkflowRepository
	wfSvc          *workflow.WorkflowService
	service        TriggerService
	fileReader     engine.FileReader
	stats          workflowStatsReader
	profileChecker workflow.ProfileChecker
	connChecker    workflow.ConnectionChecker
}

// SetConnectionChecker injects the workflow.ConnectionChecker used to validate
// that a profile's non-empty ConnectionRef names an existing connection. When
// unset (nil), the connection-reference validation pass is skipped.
func (h *WorkflowHandler) SetConnectionChecker(cc workflow.ConnectionChecker) {
	h.connChecker = cc
}

// NewWorkflowHandler creates a new WorkflowHandler with its dependencies.
// An optional workflow.ProfileChecker may be supplied as the final argument to
// enable model_profile validation; when omitted (or nil), the model_profile
// validation pass is skipped.
func NewWorkflowHandler(repo workflow.WorkflowRepository, service TriggerService, fileReader engine.FileReader, stats workflowStatsReader, profileChecker ...workflow.ProfileChecker) *WorkflowHandler {
	var pc workflow.ProfileChecker
	if len(profileChecker) > 0 {
		pc = profileChecker[0]
	}
	return &WorkflowHandler{
		repo:           repo,
		wfSvc:          workflow.NewWorkflowService(repo, nil),
		service:        service,
		fileReader:     fileReader,
		stats:          stats,
		profileChecker: pc,
	}
}

// SetWorkflowService overrides the internally-constructed WorkflowService, e.g.
// to wire a real PublisherLookup once the published_workflows repo exists.
func (h *WorkflowHandler) SetWorkflowService(svc *workflow.WorkflowService) {
	if svc != nil {
		h.wfSvc = svc
	}
}

// triggerRequest is the JSON body for triggering a workflow.
type triggerRequest struct {
	Variables map[string]any `json:"variables"`
	Priority  string         `json:"priority"`
}

// safeWorkflowName checks that a workflow name is a plain filename component
// (no path separators, no dot-only names) to prevent directory traversal via
// the URL. "." is rejected because filepath.Join(base, ".") resolves to base
// itself, the same trap as ".." but not caught by the name != ".." check.
func safeWorkflowName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && filepath.Clean(name) == name && name != ".." && name != "."
}

// List handles GET /workflows and returns a JSON array of workflow summaries.
func (h *WorkflowHandler) List(w http.ResponseWriter, r *http.Request) {
	summaries, err := h.wfSvc.List(r.Context())
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
	wf, err := h.wfSvc.Load(r.Context(), name)
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
	wf, err := h.wfSvc.Load(r.Context(), name)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load workflow")
		return
	}

	checker := ownerBoundChecker{fr: h.fileReader, owner: callerOwner(r.Context())}
	errs := workflow.Validate(wf, checker, h.profileChecker, h.connChecker)
	valid := len(errs) == 0
	if errs == nil {
		errs = []workflow.ValidationError{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"valid":  valid,
		"errors": errs,
	})
}

const maxTriggerBodyBytes = 1 << 20  // 1 MiB
const maxWriteFileBytes = 10 << 20   // 10 MiB

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

	id, started, err := h.service.Trigger(r.Context(), name, req.Variables, req.Priority)
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

// publishRequest is the JSON body for publishing a private workflow.
type publishRequest struct {
	PublishedName string `json:"publishedName"`
	Visibility    string `json:"visibility"` // "department" | "public"
	Department    string `json:"department,omitempty"`
}

// visibilityRequest is the JSON body for changing a shared workflow's level.
type visibilityRequest struct {
	Visibility string `json:"visibility"`
	Department string `json:"department,omitempty"`
}

// copyRequest is the JSON body for copying a shared workflow.
type copyRequest struct {
	NewName string `json:"newName"`
}

// Publish handles POST /workflows/{name}/publish and copies the caller's
// private workflow into the shared workspace under publishedName.
func (h *WorkflowHandler) Publish(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if !safeWorkflowName(req.PublishedName) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid published name: "+req.PublishedName)
		return
	}

	// Default empty→public to preserve the old single-arg publish contract for
	// existing clients; the service enforces the role gate for the level.
	vis := req.Visibility
	if vis == "" {
		vis = "public"
	}
	if err := h.wfSvc.Publish(r.Context(), name, req.PublishedName, workflow.Visibility(vis), req.Department); err != nil {
		switch {
		case errors.Is(err, workflow.ErrInvalidVisibility):
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		case errors.Is(err, workflow.ErrUnauthorized):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		case errors.Is(err, workflow.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		case errors.Is(err, workflow.ErrAlreadyExists):
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
		default:
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to publish workflow")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "published", "name": req.PublishedName})
}

// SetVisibility handles PUT /workflows/{name}/visibility and changes an existing
// shared workflow's sharing level. {name} is the published name.
func (h *WorkflowHandler) SetVisibility(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var req visibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if err := h.wfSvc.SetVisibility(r.Context(), name, workflow.Visibility(req.Visibility), req.Department); err != nil {
		switch {
		case errors.Is(err, workflow.ErrInvalidVisibility):
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		case errors.Is(err, workflow.ErrUnauthorized):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		case errors.Is(err, workflow.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		default:
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to set visibility")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "visibility_updated", "name": name})
}

// Copy handles POST /workflows/{name}/copy and copies a shared workflow into
// the caller's private workspace under newName.
func (h *WorkflowHandler) Copy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var req copyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if !safeWorkflowName(req.NewName) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid new name: "+req.NewName)
		return
	}

	if err := h.wfSvc.Copy(r.Context(), name, req.NewName); err != nil {
		switch {
		case errors.Is(err, workflow.ErrAlreadyExists):
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
		case errors.Is(err, workflow.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		default:
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to copy workflow")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "copied", "name": req.NewName})
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

	if err := h.wfSvc.SaveNodePositions(r.Context(), name, req.Positions); err != nil {
		if writeSharedReadOnlyOrNotFound(w, name, err) {
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

// Create handles POST /workflows and creates a new workflow definition.
func (h *WorkflowHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var wf workflow.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if !safeWorkflowName(wf.Name) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid workflow name: "+wf.Name)
		return
	}

	if err := h.wfSvc.Create(r.Context(), wf.Name, &wf); err != nil {
		if errors.Is(err, workflow.ErrAlreadyExists) {
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create workflow")
		return
	}
	WriteJSON(w, http.StatusCreated, wf)
}

// Update handles PUT /workflows/{name} and updates an existing workflow definition.
func (h *WorkflowHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var wf workflow.Workflow
	if err := json.NewDecoder(r.Body).Decode(&wf); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	// URL path is authoritative for the workflow name; overwrite whatever the
	// body contained so workflow.json stays consistent with its directory name.
	wf.Name = name

	if err := h.wfSvc.Update(r.Context(), name, &wf); err != nil {
		if writeSharedReadOnlyOrNotFound(w, name, err) {
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update workflow")
		return
	}
	WriteJSON(w, http.StatusOK, wf)
}

// Delete handles DELETE /workflows/{name} and removes a workflow definition.
func (h *WorkflowHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	if err := h.wfSvc.Delete(r.Context(), name); err != nil {
		if writeSharedReadOnlyOrNotFound(w, name, err) {
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete workflow")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListFiles handles GET /workflows/{name}/files and returns the list of files
// in a workflow directory.
func (h *WorkflowHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	files, err := h.wfSvc.ListFiles(r.Context(), name)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list files")
		return
	}
	if files == nil {
		files = []string{}
	}
	WriteJSON(w, http.StatusOK, files)
}

// ReadFile handles GET /workflows/{name}/files/* and returns the raw file contents.
func (h *WorkflowHandler) ReadFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	data, err := h.fileReader.ReadFile(callerOwner(r.Context()), name, filePath)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) || errors.Is(err, workflow.ErrFileNotFound) || errors.Is(err, os.ErrNotExist) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "file not found: "+filePath)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to read file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// WriteFile handles PUT /workflows/{name}/files/* and writes content to a file.
func (h *WorkflowHandler) WriteFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWriteFileBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read request body: "+err.Error())
		return
	}

	if err := h.wfSvc.WriteFile(r.Context(), name, filePath, data); err != nil {
		if errors.Is(err, workflow.ErrInvalidFilePath) {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		if writeSharedReadOnlyOrNotFound(w, name, err) {
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to write file")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "written"})
}

// DeleteFile handles DELETE /workflows/{name}/files/* and removes a file.
func (h *WorkflowHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}

	filePath := chi.URLParam(r, "*")
	if filePath == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "file path is required")
		return
	}

	if err := h.wfSvc.DeleteFile(r.Context(), name, filePath); err != nil {
		if errors.Is(err, workflow.ErrInvalidFilePath) {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		if errors.Is(err, workflow.ErrSharedReadOnly) {
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
			return
		}
		if errors.Is(err, workflow.ErrNotFound) || errors.Is(err, workflow.ErrFileNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "file not found: "+filePath)
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to delete file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
