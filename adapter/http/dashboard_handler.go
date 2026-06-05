package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/dashboard"
)

// dashboardStatsService is the consumer-side interface the handler depends on
// (accept interfaces, return structs). The concrete *dashboard.Service from the
// domain satisfies it; tests inject a fake.
type dashboardStatsService interface {
	GetStats(ctx context.Context, req dashboard.Request) (dashboard.Stats, error)
}

// DashboardHandler serves the department-scoped operational dashboard stats.
type DashboardHandler struct {
	svc dashboardStatsService
}

// NewDashboardHandler builds the handler over the stats service.
func NewDashboardHandler(svc dashboardStatsService) *DashboardHandler {
	return &DashboardHandler{svc: svc}
}

// Stats handles GET /dashboard/stats. It parses the (unvalidated) query params
// and delegates to the service, which clamps departments to the caller's
// allowed set and the window to a sane range. The identity is read inside the
// service via context; the auth middleware guarantees it is present.
func (h *DashboardHandler) Stats(w http.ResponseWriter, r *http.Request) {
	req := dashboard.Request{
		Departments: parseDepartments(r.URL.Query().Get("departments")),
	}

	if raw := r.URL.Query().Get("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid window parameter: "+err.Error())
			return
		}
		req.Window = d
	}

	stats, err := h.svc.GetStats(r.Context(), req)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load dashboard stats")
		return
	}

	// Ensure JSON arrays serialize as [] not null for empty results.
	if stats.Throughput == nil {
		stats.Throughput = []dashboard.ThroughputBucket{}
	}
	if stats.PerWorkflow == nil {
		stats.PerWorkflow = []dashboard.WorkflowBreakdown{}
	}
	if stats.Departments == nil {
		stats.Departments = []string{}
	}

	WriteJSON(w, http.StatusOK, stats)
}

// parseDepartments splits a comma-separated department list, trimming blanks.
// An empty/absent value yields nil so the service applies the union default.
func parseDepartments(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
