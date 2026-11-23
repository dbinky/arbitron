package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/go-chi/chi/v5"
)

// RunnerService is the pool service surface (*runner.Service satisfies it).
type RunnerService interface {
	List(ctx context.Context) ([]runner.RunnerView, error)
	Get(ctx context.Context, id runner.RunnerID) (runner.RunnerDetail, error)
	Events(ctx context.Context, limit int) ([]runner.PoolEvent, error)
	Update(ctx context.Context, id runner.RunnerID, p runner.RunnerConfigPatch) (runner.Runner, error)
	Cordon(ctx context.Context, id runner.RunnerID) error
	Uncordon(ctx context.Context, id runner.RunnerID) error
	ProbeSource(ctx context.Context, id runner.SourceID) (runner.SourceHealth, error)
}

const maxRunnerBodyBytes = 64 << 10

// RunnerHandler serves /api/v1/runners. Authz, visibility and audit live in
// the service; this layer only decodes, maps errors and renders DTOs.
type RunnerHandler struct{ svc RunnerService }

func NewRunnerHandler(svc RunnerService) *RunnerHandler {
	if svc == nil {
		panic("http: NewRunnerHandler requires a runner service")
	}
	return &RunnerHandler{svc: svc}
}

type updateRunnerRequest struct {
	StepLimit *int              `json:"stepLimit"`
	Labels    map[string]string `json:"labels"`
}

func (h *RunnerHandler) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List(r.Context())
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	out := make([]runnerViewResponse, 0, len(views))
	for _, v := range views {
		out = append(out, toRunnerViewResponse(v))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runners": out})
}

func (h *RunnerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := runnerParam(w, r, "id")
	if !ok {
		return
	}
	d, err := h.svc.Get(r.Context(), runner.RunnerID(id))
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRunnerDetailResponse(d))
}

func (h *RunnerHandler) Events(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	evs, err := h.svc.Events(r.Context(), limit)
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"events": toPoolEventViews(evs)})
}

func (h *RunnerHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := runnerParam(w, r, "id")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRunnerBodyBytes)
	var req updateRunnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	rn, err := h.svc.Update(r.Context(), runner.RunnerID(id), runner.RunnerConfigPatch{StepLimit: req.StepLimit, Labels: req.Labels})
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, toRunnerViewResponse(runner.RunnerView{Runner: rn}))
}

func (h *RunnerHandler) Cordon(w http.ResponseWriter, r *http.Request)   { h.cordon(w, r, true) }
func (h *RunnerHandler) Uncordon(w http.ResponseWriter, r *http.Request) { h.cordon(w, r, false) }

func (h *RunnerHandler) cordon(w http.ResponseWriter, r *http.Request, on bool) {
	id, ok := runnerParam(w, r, "id")
	if !ok {
		return
	}
	var err error
	if on {
		err = h.svc.Cordon(r.Context(), runner.RunnerID(id))
	} else {
		err = h.svc.Uncordon(r.Context(), runner.RunnerID(id))
	}
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RunnerHandler) ProbeSource(w http.ResponseWriter, r *http.Request) {
	id, ok := runnerParam(w, r, "sourceId")
	if !ok {
		return
	}
	hl, err := h.svc.ProbeSource(r.Context(), runner.SourceID(id))
	if err != nil {
		writeRunnerError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, toSourceHealthResponse(hl))
}

// runnerParam reads a URL-escaped path parameter (source IDs contain "/",
// sent as %2F; chi matches on the raw path, so the value arrives escaped).
func runnerParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v, err := url.PathUnescape(chi.URLParam(r, name))
	if err != nil || v == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid "+name)
		return "", false
	}
	return v, true
}

func writeRunnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, identity.ErrForbidden):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "admin only")
	case errors.Is(err, runner.ErrRunnerNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "runner not found")
	case errors.Is(err, runner.ErrSourceNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "source not found")
	case errors.Is(err, runner.ErrInvalidStepLimit):
		WriteError(w, http.StatusBadRequest, "INVALID_STEP_LIMIT", "stepLimit must be between 1 and 256")
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
