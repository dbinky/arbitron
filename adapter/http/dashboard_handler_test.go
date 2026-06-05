package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/dashboard"
)

// fakeStatsService captures the parsed Request and returns canned Stats.
type fakeStatsService struct {
	got dashboard.Request
	out dashboard.Stats
	err error
}

func (f *fakeStatsService) GetStats(_ context.Context, req dashboard.Request) (dashboard.Stats, error) {
	f.got = req
	return f.out, f.err
}

func TestDashboardHandler_ParsesParamsAndSerializesEnvelope(t *testing.T) {
	svc := &fakeStatsService{out: dashboard.Stats{
		Departments: []string{"engineering"},
		Queue:       dashboard.QueueHealth{Running: 2, FailedNeedingAttention: 1},
		Outcomes:    dashboard.Outcomes{TotalRuns: 5, SuccessRate: 0.8},
	}}
	h := adapthttp.NewDashboardHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats?departments=engineering,sales&window=6h", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"engineering", "sales"}, svc.got.Departments, "CSV split into slice")
	assert.Equal(t, 6*time.Hour, svc.got.Window, "Go duration parsed")

	var body dashboard.Stats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 2, body.Queue.Running)
	assert.Equal(t, 1, body.Queue.FailedNeedingAttention)
	assert.InDelta(t, 0.8, body.Outcomes.SuccessRate, 0.001)
}

func TestDashboardHandler_InvalidWindowIs400(t *testing.T) {
	svc := &fakeStatsService{}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats?window=notaduration", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardHandler_OmittedDepartmentsAreNil(t *testing.T) {
	svc := &fakeStatsService{}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, svc.got.Departments, "absent param ⇒ nil ⇒ service applies union default")
	assert.Zero(t, svc.got.Window, "absent window ⇒ zero ⇒ service applies DefaultWindow")
}

func TestDashboardHandler_EmptyResultsSerializeAsArrays(t *testing.T) {
	svc := &fakeStatsService{out: dashboard.Stats{}} // all nil slices
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"throughput":[]`)
	assert.Contains(t, body, `"perWorkflow":[]`)
	assert.Contains(t, body, `"departments":[]`)
}

func TestDashboardHandler_ServiceErrorIs500(t *testing.T) {
	svc := &fakeStatsService{err: context.DeadlineExceeded}
	h := adapthttp.NewDashboardHandler(svc)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// ---------------------------------------------------------------------------
// FA #22 Pass 1: route wiring via the full router
// ---------------------------------------------------------------------------

// TestDashboardStatsRoute_WiredWhenServiceNonNil verifies that the /api/v1/dashboard/stats
// route is registered and returns 200 when DashboardStats is set in RouterDeps.
// Composition test: proves the handler is mounted in the router, not just that
// the handler logic works.
func TestDashboardStatsRoute_WiredWhenServiceNonNil(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	statsSvc := &fakeStatsService{out: dashboard.Stats{
		Departments: []string{"engineering"},
	}}
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:        svc,
		Workflows:      wfRepo,
		Instances:      instRepo,
		NodeExecs:      nodeExecRepo,
		Snapshots:      snapRepo,
		FileReader:     fileReader,
		EventBus:       bus,
		Config:         cfg,
		QueueManager:   qm,
		QueueRepo:      qr,
		DashboardStats: statsSvc,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "GET /api/v1/dashboard/stats must be 200 when DashboardStats is set")

	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	_, ok := body["departments"]
	assert.True(t, ok, "response must contain 'departments' field")
}

// TestDashboardStatsRoute_AbsentWhenServiceNil verifies that GET /api/v1/dashboard/stats
// returns 404 when DashboardStats is nil — the route must not be mounted at all
// (no stale handler, correct nil-guard in NewRouterFromDeps).
func TestDashboardStatsRoute_AbsentWhenServiceNil(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:      svc,
		Workflows:    wfRepo,
		Instances:    instRepo,
		NodeExecs:    nodeExecRepo,
		Snapshots:    snapRepo,
		FileReader:   fileReader,
		EventBus:     bus,
		Config:       cfg,
		QueueManager: qm,
		QueueRepo:    qr,
		// DashboardStats intentionally nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code, "route must 404 when DashboardStats is nil")
}

// ---------------------------------------------------------------------------
// FA #22 Pass 2: parseDepartments robustness
// ---------------------------------------------------------------------------

// TestDashboardHandler_BlankOnlyCSV_TreatedAsNil verifies that a departments
// param containing only whitespace or empty segments yields nil, so the service
// applies the union-all default rather than filtering to zero departments (which
// would produce empty stats for the caller).
func TestDashboardHandler_BlankOnlyCSV_TreatedAsNil(t *testing.T) {
	for _, raw := range []string{" ", "  ", ",", " , , "} {
		svc := &fakeStatsService{}
		h := adapthttp.NewDashboardHandler(svc)
		u := "/api/v1/dashboard/stats?departments=" + url.QueryEscape(raw)
		req := httptest.NewRequest(http.MethodGet, u, nil)
		rec := httptest.NewRecorder()
		h.Stats(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Nil(t, svc.got.Departments,
			"blank-only param %q must yield nil departments (union default)", raw)
	}
}

// TestDashboardHandler_SpacesAroundDepartmentNames_Trimmed verifies that spaces
// around individual department names in the CSV are trimmed before forwarding
// to the service, so "engineering , sales" is equivalent to "engineering,sales".
func TestDashboardHandler_SpacesAroundDepartmentNames_Trimmed(t *testing.T) {
	svc := &fakeStatsService{}
	h := adapthttp.NewDashboardHandler(svc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stats?departments="+url.QueryEscape("engineering , sales"), nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"engineering", "sales"}, svc.got.Departments,
		"spaces around department names must be trimmed before forwarding to service")
}
