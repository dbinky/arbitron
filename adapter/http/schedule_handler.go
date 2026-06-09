package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/schedule"
)

// ScheduleHandler exposes the recurring-schedule management surface (REST). It
// is a thin translator over schedule.Service: decode → call service → map
// sentinels to status. ALL authz + validation lives in the service, shared with
// the MCP surface, so the two never drift.
type ScheduleHandler struct {
	svc *schedule.Service
}

// NewScheduleHandler constructs a ScheduleHandler.
func NewScheduleHandler(svc *schedule.Service) *ScheduleHandler {
	return &ScheduleHandler{svc: svc}
}

// scheduleCreateRequest is the POST /schedules body. CreatorID is deliberately
// absent: the creator is always the request identity (service-set).
type scheduleCreateRequest struct {
	WorkflowName string         `json:"workflowName"`
	CronExpr     string         `json:"cronExpr"`
	Timezone     string         `json:"timezone,omitempty"`
	Department   string         `json:"department,omitempty"`
	Variables    map[string]any `json:"variables,omitempty"`
	Enabled      *bool          `json:"enabled,omitempty"`
}

// scheduleUpdateRequest is the PUT /schedules/{id} body. Nil fields are
// unchanged.
type scheduleUpdateRequest struct {
	WorkflowName *string        `json:"workflowName,omitempty"`
	CronExpr     *string        `json:"cronExpr,omitempty"`
	Timezone     *string        `json:"timezone,omitempty"`
	Department   *string        `json:"department,omitempty"`
	Variables    map[string]any `json:"variables,omitempty"`
	Enabled      *bool          `json:"enabled,omitempty"`
}

// scheduleRunNowRequest is the POST /schedules/{id}/run body. The whole body is
// optional; an empty body leaves force=false, so run-now COALESCES against an
// active run by default. Set force to fire regardless of an active run.
type scheduleRunNowRequest struct {
	Force bool `json:"force,omitempty"`
}

// writeScheduleError maps schedule sentinels to HTTP status.
func writeScheduleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, schedule.ErrExists):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, schedule.ErrCreatorUnresolved):
		WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, schedule.ErrRunNowUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
	case errors.Is(err, schedule.ErrForbidden), errors.Is(err, schedule.ErrNotAuthorizedWorkflow):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, schedule.ErrInvalidCron),
		errors.Is(err, schedule.ErrInvalidTimezone),
		errors.Is(err, schedule.ErrEmptyWorkflow),
		errors.Is(err, schedule.ErrDepartmentNotMember),
		errors.Is(err, schedule.ErrQuotaExceeded),
		errors.Is(err, schedule.ErrIntervalTooFrequent):
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func decodeScheduleBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return false
	}
	return true
}

func scheduleIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid schedule id")
		return uuid.Nil, false
	}
	return id, true
}

// Create handles POST /schedules.
func (h *ScheduleHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req scheduleCreateRequest
	if !decodeScheduleBody(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	s, err := h.svc.Create(r.Context(), schedule.CreateInput{
		WorkflowName: req.WorkflowName,
		CronExpr:     req.CronExpr,
		Timezone:     req.Timezone,
		Department:   req.Department,
		Variables:    req.Variables,
		Enabled:      enabled,
	})
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, s.WireMap())
}

// List handles GET /schedules. An optional ?workflowName= query narrows the
// result to one workflow (read-only dashboard surface). The creator scoping is
// applied by the service from the request identity; the workflow filter only
// shrinks that already-scoped set, so it is safe to take from the query.
func (h *ScheduleHandler) List(w http.ResponseWriter, r *http.Request) {
	opts := schedule.ListOptions{
		WorkflowName: r.URL.Query().Get("workflowName"),
	}
	schedules, err := h.svc.List(r.Context(), opts)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(schedules))
	for _, s := range schedules {
		out = append(out, s.WireMap())
	}
	WriteJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

// Get handles GET /schedules/{id}.
func (h *ScheduleHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	s, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.WireMap())
}

// Update handles PUT /schedules/{id}.
func (h *ScheduleHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	var req scheduleUpdateRequest
	if !decodeScheduleBody(w, r, &req) {
		return
	}
	s, err := h.svc.Update(r.Context(), id, schedule.UpdateInput{
		WorkflowName: req.WorkflowName,
		CronExpr:     req.CronExpr,
		Timezone:     req.Timezone,
		Department:   req.Department,
		Variables:    req.Variables,
		Enabled:      req.Enabled,
	})
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.WireMap())
}

// Enable handles POST /schedules/{id}/enable.
func (h *ScheduleHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

// Disable handles POST /schedules/{id}/disable.
func (h *ScheduleHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *ScheduleHandler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	s, err := h.svc.SetEnabled(r.Context(), id, enabled)
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, s.WireMap())
}

// Delete handles DELETE /schedules/{id}.
func (h *ScheduleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeScheduleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunNow handles POST /schedules/{id}/run. Body {"force": bool} is optional
// (default false ⇒ coalesce against an active run). On a started run it returns
// 202 with {instanceId, fired:true} and a Location header pointing at the
// created instance; when skipped-because-busy it returns 200 with {fired:false}.
func (h *ScheduleHandler) RunNow(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleIDParam(w, r)
	if !ok {
		return
	}
	var req scheduleRunNowRequest
	// The body is optional. An empty body must not be a 400, so decode only when
	// there is content; reuse decodeScheduleBody (which 400s on malformed JSON).
	if r.ContentLength != 0 {
		if !decodeScheduleBody(w, r, &req) {
			return
		}
	}
	res, err := h.svc.RunNow(r.Context(), id, schedule.RunNowInput{Force: req.Force})
	if err != nil {
		writeScheduleError(w, err)
		return
	}
	if !res.Fired {
		WriteJSON(w, http.StatusOK, map[string]any{"fired": false})
		return
	}
	// Point the caller at the created instance (HTTP-transport detail; the MCP
	// surface has no equivalent). The path mirrors the instances route group.
	w.Header().Set("Location", "/api/v1/instances/"+res.InstanceID.String())
	WriteJSON(w, http.StatusAccepted, map[string]any{
		"instanceId": res.InstanceID.String(),
		"fired":      true,
	})
}
