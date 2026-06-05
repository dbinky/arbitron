package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
