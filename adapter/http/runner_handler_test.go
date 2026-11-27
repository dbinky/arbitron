package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunnerService records load-bearing arguments and enforces the admin gate
// the real service applies, so routing + error mapping are exercised.
type fakeRunnerService struct {
	views    []runner.RunnerView
	detail   runner.RunnerDetail
	events   []runner.PoolEvent
	gotID    runner.RunnerID
	gotSrc   runner.SourceID
	gotLim   int
	gotPat   runner.RunnerConfigPatch
	cordons  []bool
	drain    runner.DrainProgress
	drainErr error
}

func fakeAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.ErrUnauthenticated
	}
	if !id.IsAdmin() {
		return identity.ErrForbidden
	}
	return nil
}

func (f *fakeRunnerService) List(context.Context) ([]runner.RunnerView, error) { return f.views, nil }
func (f *fakeRunnerService) Get(_ context.Context, id runner.RunnerID) (runner.RunnerDetail, error) {
	f.gotID = id
	if id != runner.BuiltInRunnerID {
		return runner.RunnerDetail{}, runner.ErrRunnerNotFound
	}
	return f.detail, nil
}
func (f *fakeRunnerService) Events(_ context.Context, limit int) ([]runner.PoolEvent, error) {
	f.gotLim = limit
	return f.events, nil
}
func (f *fakeRunnerService) Update(ctx context.Context, id runner.RunnerID, p runner.RunnerConfigPatch) (runner.Runner, error) {
	if err := fakeAdmin(ctx); err != nil {
		return runner.Runner{}, err
	}
	if p.StepLimit != nil && !runner.ValidStepLimit(*p.StepLimit) {
		return runner.Runner{}, runner.ErrInvalidStepLimit
	}
	f.gotID, f.gotPat = id, p
	return runner.Runner{ID: id, StepLimit: *p.StepLimit}, nil
}
func (f *fakeRunnerService) Cordon(ctx context.Context, id runner.RunnerID) error {
	if err := fakeAdmin(ctx); err != nil {
		return err
	}
	f.gotID, f.cordons = id, append(f.cordons, true)
	return nil
}
func (f *fakeRunnerService) Uncordon(ctx context.Context, id runner.RunnerID) error {
	if err := fakeAdmin(ctx); err != nil {
		return err
	}
	f.gotID, f.cordons = id, append(f.cordons, false)
	return nil
}
func (f *fakeRunnerService) ProbeSource(ctx context.Context, id runner.SourceID) (runner.SourceHealth, error) {
	if err := fakeAdmin(ctx); err != nil {
		return runner.SourceHealth{}, err
	}
	f.gotSrc = id
	return runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}, nil
}

func (f *fakeRunnerService) Drain(ctx context.Context, id runner.RunnerID) (runner.DrainProgress, error) {
	if err := fakeAdmin(ctx); err != nil {
		return runner.DrainProgress{}, err
	}
	f.gotID = id
	return f.drain, f.drainErr
}

func sampleRunnerFake() *fakeRunnerService {
	reset := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	src := runner.Source{ID: "builtin/anthropic_subscription/-", RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription,
		Families: []modelidentity.Family{"anthropic"}, Backends: []string{"", "claude-code"},
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &reset}}
	view := runner.RunnerView{Runner: runner.Runner{ID: runner.BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: 8}, Sources: []runner.Source{src}, SlotsUsed: 3, Queued: 1}
	vis := uuid.New()
	return &fakeRunnerService{
		views: []runner.RunnerView{view},
		detail: runner.RunnerDetail{RunnerView: view, Assignments: []runner.AssignmentView{
			{JobID: uuid.New(), RunID: &vis, NodeID: "n1", State: runner.JobRunning, SourceID: src.ID},
			{JobID: uuid.New(), State: runner.JobRunning, SourceID: src.ID, Hidden: true},
		}},
		events: []runner.PoolEvent{{ID: uuid.New(), Kind: runner.EventSourceLoggedOut, RunnerID: runner.BuiltInRunnerID, SourceID: src.ID}},
	}
}

func runnerRouter(svc RunnerService, role identity.Role) http.Handler {
	r := NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Runners: svc})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := identity.WithIdentity(req.Context(), identity.Identity{UserID: uuid.New(), Username: "u", Role: role})
		r.ServeHTTP(w, req.WithContext(ctx))
	})
}

func doRunner(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRunnerHandler_ListDTO(t *testing.T) {
	w := doRunner(t, runnerRouter(sampleRunnerFake(), identity.RoleUser), http.MethodGet, "/api/v1/runners", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body struct {
		Runners []map[string]any `json:"runners"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Runners, 1)
	r := body.Runners[0]
	assert.Equal(t, "builtin", r["id"])
	assert.Equal(t, true, r["builtIn"])
	assert.EqualValues(t, 8, r["stepLimit"])
	assert.EqualValues(t, 3, r["slotsUsed"])
	assert.EqualValues(t, 1, r["queued"])
	assert.Nil(t, r["telemetry"])
	assert.Nil(t, r["drain"])
	assert.Equal(t, map[string]any{}, r["labels"])
	srcs := r["sources"].([]any)
	s0 := srcs[0].(map[string]any)
	assert.Equal(t, "anthropic_subscription", s0["kind"])
	assert.Equal(t, []any{"anthropic"}, s0["families"])
	h := s0["health"].(map[string]any)
	assert.Equal(t, "exhausted", h["capacity"])
	assert.Equal(t, "2026-11-20T15:00:00Z", h["resetAt"])
	assert.Nil(t, h["headroom"], "unknown headroom is null, never 0")
}

func TestRunnerHandler_GetHidesRunIDs(t *testing.T) {
	fake := sampleRunnerFake()
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code)
	var d map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	as := d["assignments"].([]any)
	require.Len(t, as, 2)
	hidden := as[1].(map[string]any)
	assert.Equal(t, true, hidden["hidden"])
	assert.Nil(t, hidden["runId"])
	assert.Equal(t, "", hidden["nodeId"])
	assert.Equal(t, []any{}, d["sessions"])
	assert.Equal(t, []any{}, d["homes"])

	w = doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/laptop", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRunnerHandler_Events(t *testing.T) {
	fake := sampleRunnerFake()
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/events?limit=25", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 25, fake.gotLim)
	assert.Contains(t, w.Body.String(), `"events":[`)
	assert.Contains(t, w.Body.String(), `"kind":"source.logged_out"`)
	w = doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/events?limit=abc", "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRunnerHandler_WritesAdminOnly(t *testing.T) {
	fake := sampleRunnerFake()
	user := runnerRouter(fake, identity.RoleUser)
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodPatch, "/api/v1/runners/builtin", `{"stepLimit":12}`},
		{http.MethodPost, "/api/v1/runners/builtin/cordon", ""},
		{http.MethodPost, "/api/v1/runners/builtin/uncordon", ""},
		{http.MethodPost, "/api/v1/runners/sources/builtin%2Fanthropic_subscription%2F-/probe", ""},
	} {
		w := doRunner(t, user, tc.method, tc.target, tc.body)
		assert.Equal(t, http.StatusForbidden, w.Code, tc.target)
	}
	assert.Empty(t, fake.cordons)
}

func TestRunnerHandler_AdminWrites(t *testing.T) {
	fake := sampleRunnerFake()
	admin := runnerRouter(fake, identity.RoleAdmin)
	w := doRunner(t, admin, http.MethodPatch, "/api/v1/runners/builtin", `{"stepLimit":12,"labels":{"site":"hq"}}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, fake.gotPat.StepLimit)
	assert.Equal(t, 12, *fake.gotPat.StepLimit)
	assert.Equal(t, map[string]string{"site": "hq"}, fake.gotPat.Labels)

	w = doRunner(t, admin, http.MethodPatch, "/api/v1/runners/builtin", `{"stepLimit":0}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = doRunner(t, admin, http.MethodPatch, "/api/v1/runners/builtin", `{nope`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	require.Equal(t, http.StatusNoContent, doRunner(t, admin, http.MethodPost, "/api/v1/runners/builtin/cordon", "").Code)
	require.Equal(t, http.StatusNoContent, doRunner(t, admin, http.MethodPost, "/api/v1/runners/builtin/uncordon", "").Code)
	assert.Equal(t, []bool{true, false}, fake.cordons)

	w = doRunner(t, admin, http.MethodPost, "/api/v1/runners/sources/builtin%2Fanthropic_subscription%2F-/probe", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, runner.SourceID("builtin/anthropic_subscription/-"), fake.gotSrc, "the escaped source id is decoded")
	assert.Contains(t, w.Body.String(), `"auth":"ok"`)
}

func TestRunnerHandler_RoutesAbsentWithoutService(t *testing.T) {
	r := NewRouterFromDeps(RouterDeps{Config: &config.Config{}})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/runners", nil))
	assert.NotEqual(t, http.StatusOK, w.Code)
}
