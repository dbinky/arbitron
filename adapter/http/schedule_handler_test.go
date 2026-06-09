package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- in-test schedule repo + canTrigger ---

type schedTestRepo struct {
	m map[uuid.UUID]*schedule.Schedule
}

func newSchedTestRepo() *schedTestRepo { return &schedTestRepo{m: map[uuid.UUID]*schedule.Schedule{}} }

func (r *schedTestRepo) Create(_ context.Context, s *schedule.Schedule) error {
	r.m[s.ID] = s
	return nil
}
func (r *schedTestRepo) Get(_ context.Context, id uuid.UUID) (*schedule.Schedule, error) {
	s, ok := r.m[id]
	if !ok {
		return nil, schedule.ErrNotFound
	}
	cp := *s
	return &cp, nil
}
func (r *schedTestRepo) List(_ context.Context, f schedule.ListFilters) ([]schedule.Schedule, error) {
	var out []schedule.Schedule
	for _, s := range r.m {
		if f.CreatorID != nil && s.CreatorID != *f.CreatorID {
			continue
		}
		out = append(out, *s)
	}
	return out, nil
}
func (r *schedTestRepo) ListEnabled(_ context.Context) ([]schedule.Schedule, error) { return nil, nil }
func (r *schedTestRepo) Update(_ context.Context, s *schedule.Schedule) error {
	if _, ok := r.m[s.ID]; !ok {
		return schedule.ErrNotFound
	}
	r.m[s.ID] = s
	return nil
}
func (r *schedTestRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := r.m[id]; !ok {
		return schedule.ErrNotFound
	}
	delete(r.m, id)
	return nil
}
func (r *schedTestRepo) SetLastFired(context.Context, uuid.UUID, time.Time) error { return nil }

type allowCanTrigger struct{ allow bool }

func (a allowCanTrigger) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	return a.allow, nil
}

// schedRouter builds a chi router with the schedule routes and an identity
// middleware that injects the given identity, mirroring production wiring.
func schedRouter(t *testing.T, repo schedule.ScheduleRepository, allow bool, id identity.Identity) http.Handler {
	t.Helper()
	svc := schedule.NewService(repo, allowCanTrigger{allow: allow})
	h := adapthttp.NewScheduleHandler(svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), id)))
		})
	})
	r.Route("/schedules", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Get("/{id}", h.Get)
		r.Put("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)
		r.Post("/{id}/enable", h.Enable)
		r.Post("/{id}/disable", h.Disable)
	})
	return r
}

func testUser() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser}
}

func do(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Buffer
	if body != "" {
		rdr = bytes.NewBufferString(body)
	} else {
		rdr = bytes.NewBufferString("")
	}
	req := httptest.NewRequest(method, path, rdr)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// --- Happy-path ---

func TestScheduleHandler_Create_201(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), true, testUser())
	w := do(t, router, http.MethodPost, "/schedules", `{"workflowName":"report","cronExpr":"0 9 * * *","timezone":"UTC"}`)
	require.Equal(t, http.StatusCreated, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "report", body["workflowName"])
	assert.NotEmpty(t, body["id"])
}

func TestScheduleHandler_List_200(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	repo.m[uuid.New()] = &schedule.Schedule{ID: uuid.New(), CreatorID: u.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	router := schedRouter(t, repo, true, u)
	w := do(t, router, http.MethodGet, "/schedules", "")
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Len(t, body["schedules"], 1)
}

// --- Input-validation ---

func TestScheduleHandler_Create_InvalidCron_400(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), true, testUser())
	w := do(t, router, http.MethodPost, "/schedules", `{"workflowName":"report","cronExpr":"nope"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestScheduleHandler_Create_BadBody_400(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), true, testUser())
	w := do(t, router, http.MethodPost, "/schedules", `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// --- Error-handling / authz ---

func TestScheduleHandler_Create_CannotTrigger_403(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), false, testUser())
	w := do(t, router, http.MethodPost, "/schedules", `{"workflowName":"report","cronExpr":"0 9 * * *"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestScheduleHandler_Get_NotFound_404(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), true, testUser())
	w := do(t, router, http.MethodGet, "/schedules/"+uuid.New().String(), "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// --- Edge-cases / boundary ---

// error — PUT /{id} for a schedule owned by another user must return 403.
func TestScheduleHandler_Update_OtherUser_403(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo.m[s.ID] = s
	requester := testUser() // different user
	router := schedRouter(t, repo, true, requester)
	w := do(t, router, http.MethodPut, "/schedules/"+s.ID.String(), `{"cronExpr":"0 10 * * *"}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// error — DELETE /{id} for a schedule owned by another user must return 403.
func TestScheduleHandler_Delete_OtherUser_403(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo.m[s.ID] = s
	requester := testUser() // different user
	router := schedRouter(t, repo, true, requester)
	w := do(t, router, http.MethodDelete, "/schedules/"+s.ID.String(), "")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Len(t, repo.m, 1, "schedule must not be deleted on forbidden")
}

// error — GET /{id} for a schedule owned by another user must return 403.
func TestScheduleHandler_Get_OtherUser_403(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: owner.UserID, WorkflowName: "report", CronExpr: "0 9 * * *"}
	repo.m[s.ID] = s
	requester := testUser() // different user
	router := schedRouter(t, repo, true, requester)
	w := do(t, router, http.MethodGet, "/schedules/"+s.ID.String(), "")
	assert.Equal(t, http.StatusForbidden, w.Code)
}

// error — POST /{id}/enable on a non-existent schedule returns 404.
func TestScheduleHandler_Enable_NotFound_404(t *testing.T) {
	router := schedRouter(t, newSchedTestRepo(), true, testUser())
	w := do(t, router, http.MethodPost, "/schedules/"+uuid.New().String()+"/enable", "")
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestScheduleHandler_Enable_StaticRouteNotShadowedById(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := &schedule.Schedule{ID: uuid.New(), CreatorID: u.UserID, WorkflowName: "report", CronExpr: "0 9 * * *", Enabled: false}
	repo.m[s.ID] = s
	router := schedRouter(t, repo, true, u)

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/enable", "")
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["enabled"], "POST /{id}/enable must resolve to SetEnabled, not GET /{id}")
}

// --- Integration (full lifecycle over chi) ---

func TestScheduleHandler_FullLifecycle(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	router := schedRouter(t, repo, true, u)

	// Create
	w := do(t, router, http.MethodPost, "/schedules", `{"workflowName":"report","cronExpr":"0 9 * * *","timezone":"UTC"}`)
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created["id"].(string)

	// List sees it
	w = do(t, router, http.MethodGet, "/schedules", "")
	require.Equal(t, http.StatusOK, w.Code)

	// Get
	w = do(t, router, http.MethodGet, "/schedules/"+id, "")
	require.Equal(t, http.StatusOK, w.Code)

	// Disable
	w = do(t, router, http.MethodPost, "/schedules/"+id+"/disable", "")
	require.Equal(t, http.StatusOK, w.Code)

	// Update
	w = do(t, router, http.MethodPut, "/schedules/"+id, `{"cronExpr":"0 10 * * *"}`)
	require.Equal(t, http.StatusOK, w.Code)
	var updated map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &updated))
	assert.Equal(t, "0 10 * * *", updated["cronExpr"])

	// Delete
	w = do(t, router, http.MethodDelete, "/schedules/"+id, "")
	require.Equal(t, http.StatusNoContent, w.Code)

	// Get -> 404
	w = do(t, router, http.MethodGet, "/schedules/"+id, "")
	require.Equal(t, http.StatusNotFound, w.Code)
}
