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
		if f.WorkflowName != nil && s.WorkflowName != *f.WorkflowName {
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

// stubHTTPFirer is an in-test schedule.Firer for the REST run-now tests.
type stubHTTPFirer struct {
	inst  uuid.UUID
	fired bool
	err   error
	calls int
}

func (f *stubHTTPFirer) Fire(_ context.Context, _ schedule.Schedule, _ bool) (uuid.UUID, bool, error) {
	f.calls++
	return f.inst, f.fired, f.err
}

// schedRouterWithFirer mirrors schedRouter but wires a Firer so the run-now
// route is exercised. It registers the /{id}/run route.
func schedRouterWithFirer(t *testing.T, repo schedule.ScheduleRepository, allow bool, id identity.Identity, firer schedule.Firer) http.Handler {
	t.Helper()
	svc := schedule.NewService(repo, allowCanTrigger{allow: allow}, schedule.WithFirer(firer))
	h := adapthttp.NewScheduleHandler(svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), id)))
		})
	})
	r.Route("/schedules", func(r chi.Router) {
		r.Post("/{id}/run", h.RunNow)
		r.Get("/{id}", h.Get)
	})
	return r
}

// seedSchedule inserts an enabled schedule owned by owner into repo and returns it.
func seedSchedule(repo *schedTestRepo, owner uuid.UUID) schedule.Schedule {
	s := schedule.Schedule{
		ID:           uuid.New(),
		WorkflowName: "report",
		CronExpr:     "0 9 * * *",
		CreatorID:    owner,
		Enabled:      true,
	}
	repo.m[s.ID] = &s
	return s
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

// TestScheduleHandler_ScheduleList_WorkflowFilter covers the optional
// ?workflowName= narrowing on GET /schedules. Creator scoping stays service-set
// from the request identity; the workflow filter only shrinks that scoped set.
func TestScheduleHandler_ScheduleList_WorkflowFilter(t *testing.T) {
	newRepo := func(owner uuid.UUID) *schedTestRepo {
		repo := newSchedTestRepo()
		a := &schedule.Schedule{ID: uuid.New(), CreatorID: owner, WorkflowName: "wf-a", CronExpr: "0 9 * * *"}
		b := &schedule.Schedule{ID: uuid.New(), CreatorID: owner, WorkflowName: "wf-b", CronExpr: "0 9 * * *"}
		repo.m[a.ID] = a
		repo.m[b.ID] = b
		return repo
	}

	t.Run("workflowName filters to that workflow only", func(t *testing.T) {
		u := testUser()
		router := schedRouter(t, newRepo(u.UserID), true, u)
		w := do(t, router, http.MethodGet, "/schedules?workflowName=wf-a", "")
		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Schedules []map[string]any `json:"schedules"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.Len(t, body.Schedules, 1)
		assert.Equal(t, "wf-a", body.Schedules[0]["workflowName"])
	})

	t.Run("no query returns the full owner-scoped list", func(t *testing.T) {
		u := testUser()
		router := schedRouter(t, newRepo(u.UserID), true, u)
		w := do(t, router, http.MethodGet, "/schedules", "")
		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Schedules []map[string]any `json:"schedules"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Len(t, body.Schedules, 2)
	})

	t.Run("empty workflowName is treated as no filter", func(t *testing.T) {
		u := testUser()
		router := schedRouter(t, newRepo(u.UserID), true, u)
		w := do(t, router, http.MethodGet, "/schedules?workflowName=", "")
		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Schedules []map[string]any `json:"schedules"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Len(t, body.Schedules, 2)
	})

	t.Run("unknown workflow is an empty list, not an error", func(t *testing.T) {
		u := testUser()
		router := schedRouter(t, newRepo(u.UserID), true, u)
		w := do(t, router, http.MethodGet, "/schedules?workflowName=does-not-exist", "")
		require.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Schedules []map[string]any `json:"schedules"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Empty(t, body.Schedules)
	})
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

// --- run-now (Capability A) ---

// Happy path: owner fires => 202 Accepted, body {instanceId, fired:true}.
func TestScheduleHandler_RunNow_202(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	inst := uuid.New()
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: inst, fired: true})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{"skipWhenBusy":false}`)

	require.Equal(t, http.StatusAccepted, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, inst.String(), body["instanceId"])
	assert.Equal(t, true, body["fired"])
}

// Empty body is allowed (skipWhenBusy defaults to false) => still 202.
func TestScheduleHandler_RunNow_EmptyBody_202(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.New(), fired: true})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", "")

	require.Equal(t, http.StatusAccepted, w.Code)
}

// Skipped (busy + skipWhenBusy) => 200 OK, body {fired:false}, no instanceId.
func TestScheduleHandler_RunNow_Skipped_200(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.Nil, fired: false})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{"skipWhenBusy":true}`)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["fired"])
}

// Input validation: malformed UUID in path => 400.
func TestScheduleHandler_RunNow_BadID_400(t *testing.T) {
	router := schedRouterWithFirer(t, newSchedTestRepo(), true, testUser(), &stubHTTPFirer{})
	w := do(t, router, http.MethodPost, "/schedules/not-a-uuid/run", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Input validation: malformed JSON body => 400.
func TestScheduleHandler_RunNow_BadBody_400(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{inst: uuid.New(), fired: true})
	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{not json`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Authorization: a non-owner non-admin => 403, firer never called.
func TestScheduleHandler_RunNow_NonOwner_403(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := seedSchedule(repo, owner.UserID)
	firer := &stubHTTPFirer{inst: uuid.New(), fired: true}
	stranger := testUser() // different UserID, RoleUser
	router := schedRouterWithFirer(t, repo, true, stranger, firer)

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, firer.calls)
}

// Authorization: admin can run any user's schedule (owner-or-admin gate).
func TestScheduleHandler_RunNow_Admin_202(t *testing.T) {
	repo := newSchedTestRepo()
	owner := testUser()
	s := seedSchedule(repo, owner.UserID)
	inst := uuid.New()
	firer := &stubHTTPFirer{inst: inst, fired: true}
	admin := identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}
	router := schedRouterWithFirer(t, repo, true, admin, firer)

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, 1, firer.calls)
}

// Not found => 404.
func TestScheduleHandler_RunNow_NotFound_404(t *testing.T) {
	router := schedRouterWithFirer(t, newSchedTestRepo(), true, testUser(), &stubHTTPFirer{})
	w := do(t, router, http.MethodPost, "/schedules/"+uuid.New().String()+"/run", `{}`)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Failure: creator unresolved => 409 Conflict.
func TestScheduleHandler_RunNow_CreatorUnresolved_409(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	router := schedRouterWithFirer(t, repo, true, u, &stubHTTPFirer{err: schedule.ErrCreatorUnresolved})

	w := do(t, router, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusConflict, w.Code)
}

// Failure: firer unwired => 503 Service Unavailable. Build a router WITHOUT a
// firer but WITH the /run route.
func TestScheduleHandler_RunNow_Unwired_503(t *testing.T) {
	repo := newSchedTestRepo()
	u := testUser()
	s := seedSchedule(repo, u.UserID)
	svc := schedule.NewService(repo, allowCanTrigger{allow: true}) // no WithFirer
	h := adapthttp.NewScheduleHandler(svc)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), u)))
		})
	})
	r.Route("/schedules", func(r chi.Router) { r.Post("/{id}/run", h.RunNow) })

	w := do(t, r, http.MethodPost, "/schedules/"+s.ID.String()+"/run", `{}`)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
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
