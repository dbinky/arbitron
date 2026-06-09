package http_test

// Vertical slice: POST /schedules/{id}/run → Handler → real Service.RunNow →
// real Runner.Fire → real SQLite (schedule lookup + user identity resolve) →
// fake Trigger.
//
// This is the "run-now over a real REST round-trip" integration path from the
// final-org-gaps review. It complements the unit tests in schedule_handler_test.go
// by exercising the full HTTP→Service→Runner→SQLite chain with no mocks at the
// service or repository layers.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	domschedule "github.com/arbitron/arbitron/domain/schedule"
)

// --- local adapters for the slice ---

// sliceTrigger records TriggerWithDepartment calls and returns a fixed instanceID.
type sliceTrigger struct {
	calls  []sliceTriggerCall
	instID uuid.UUID
}

type sliceTriggerCall struct {
	ctx          context.Context
	workflowName string
	variables    map[string]any
	department   string
}

func (f *sliceTrigger) TriggerWithDepartment(ctx context.Context, name string, vars map[string]any, _, dept string) (uuid.UUID, bool, error) {
	f.calls = append(f.calls, sliceTriggerCall{ctx: ctx, workflowName: name, variables: vars, department: dept})
	if f.instID == uuid.Nil {
		f.instID = uuid.New()
	}
	return f.instID, true, nil
}

// openCanTrigger always permits scheduling (no published-workflow check).
type openCanTrigger struct{}

func (openCanTrigger) CanTrigger(_ context.Context, _ identity.Identity, _ string) (bool, error) {
	return true, nil
}

// sqliteIdentResolver implements schedule.IdentityResolver using the real user repo.
type sqliteIdentResolver struct {
	users *sqlite.UserRepo
}

func (r *sqliteIdentResolver) IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error) {
	u, err := r.users.GetByID(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	depts, err := r.users.ListDepartments(ctx, userID)
	if err != nil {
		return identity.Identity{}, err
	}
	email := ""
	subject := ""
	if u.Email != nil {
		email = *u.Email
	}
	if u.Subject != nil {
		subject = *u.Subject
	}
	return identity.BuildIdentity(identity.UserIdentityInput{
		UserID:            u.ID,
		Username:          u.Username,
		Role:              u.Role,
		Subject:           subject,
		Email:             email,
		StoredDepartments: depts,
	}, identity.GroupConfig{}), nil
}

// --- slice environment ---

// runNowSliceEnv holds the real components for run-now integration tests.
type runNowSliceEnv struct {
	trig     *sliceTrigger
	userRepo *sqlite.UserRepo
	svc      *domschedule.Service
	router   func(callerID identity.Identity) http.Handler
}

// newRunNowSliceEnv builds the real HTTP→Service→Runner→SQLite stack.
func newRunNowSliceEnv(t *testing.T) *runNowSliceEnv {
	t.Helper()

	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))

	userRepo := sqlite.NewUserRepo(db)
	schedRepo := sqlite.NewScheduleRepo(db)
	instRepo := sqlite.NewInstanceRepo(db)

	trig := &sliceTrigger{}
	idents := &sqliteIdentResolver{users: userRepo}
	runner := domschedule.NewRunner(trig, idents, instRepo)
	svc := domschedule.NewService(schedRepo, openCanTrigger{}, domschedule.WithFirer(runner))
	h := adapthttp.NewScheduleHandler(svc)

	routerFn := func(caller identity.Identity) http.Handler {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(identity.WithIdentity(req.Context(), caller)))
			})
		})
		r.Post("/{id}/run", h.RunNow)
		return r
	}

	return &runNowSliceEnv{trig: trig, userRepo: userRepo, svc: svc, router: routerFn}
}

// newSliceUser inserts a user directly into the DB and returns an Identity.
func newSliceUser(t *testing.T, repo *sqlite.UserRepo, username string, role identity.Role) identity.Identity {
	t.Helper()
	u := &instance.User{ID: uuid.New(), Username: username, Role: role}
	require.NoError(t, repo.Create(context.Background(), u))
	return identity.Identity{UserID: u.ID, Username: u.Username, Role: u.Role, Departments: []string{"default"}}
}

// newSliceSchedule creates a schedule via the service using the caller's identity.
func newSliceSchedule(t *testing.T, svc *domschedule.Service, caller identity.Identity, wfName, dept string) domschedule.Schedule {
	t.Helper()
	ctx := identity.WithIdentity(context.Background(), caller)
	s, err := svc.Create(ctx, domschedule.CreateInput{
		WorkflowName: wfName,
		CronExpr:     "0 0 * * *",
		Department:   dept,
		Variables:    map[string]any{"k": "v"},
	})
	require.NoError(t, err)
	return s
}

// --- happy path ---

// TestRunNowSlice_HappyPath_202 exercises the full REST→Service→Runner→SQLite
// chain: the owner calls POST /schedules/{id}/run, the runner fetches the
// schedule from SQLite, resolves the creator identity via SQLite, injects
// OriginRunNow into the trigger context, and the handler returns 202 + instanceId.
func TestRunNowSlice_HappyPath_202(t *testing.T) {
	env := newRunNowSliceEnv(t)
	caller := newSliceUser(t, env.userRepo, "alice@slice.test", identity.RoleUser)
	sched := newSliceSchedule(t, env.svc, caller, "my-workflow", "default")

	body, _ := json.Marshal(map[string]any{"skipWhenBusy": false})
	req := httptest.NewRequest(http.MethodPost, "/"+sched.ID.String()+"/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router(caller).ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, true, resp["fired"])
	assert.NotEmpty(t, resp["instanceId"], "instanceId must be present on a fired run")

	require.Len(t, env.trig.calls, 1, "trigger must be called exactly once")
	call := env.trig.calls[0]
	assert.Equal(t, "my-workflow", call.workflowName)
	assert.Equal(t, map[string]any{"k": "v"}, call.variables,
		"runner must forward the schedule's stored variables to the trigger")

	// Verify the run origin injected into the trigger context.
	origin, ok := instance.OriginFromContext(call.ctx)
	require.True(t, ok, "trigger context must carry an origin")
	assert.Equal(t, instance.OriginRunNow, origin.Source,
		"trigger context must carry OriginRunNow — not manual or schedule")
	require.NotNil(t, origin.ScheduleID,
		"trigger context must carry the schedule ID for run_now origin")
	assert.Equal(t, sched.ID, *origin.ScheduleID)
}

// --- success: skip-when-busy with no active run fires normally ---

// TestRunNowSlice_SkipWhenBusy_NoActiveRun_202 verifies that skipWhenBusy=true
// with no active run in the real SQLite instances table still fires (the real
// InstanceRepo.HasActiveRun is exercised here — it returns false for an empty
// DB).
func TestRunNowSlice_SkipWhenBusy_NoActiveRun_202(t *testing.T) {
	env := newRunNowSliceEnv(t)
	caller := newSliceUser(t, env.userRepo, "bob@slice.test", identity.RoleUser)
	sched := newSliceSchedule(t, env.svc, caller, "fire-anyway", "default")

	body, _ := json.Marshal(map[string]any{"skipWhenBusy": true})
	req := httptest.NewRequest(http.MethodPost, "/"+sched.ID.String()+"/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.router(caller).ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, true, resp["fired"], "no active run in DB → skipWhenBusy fires normally")
	assert.Len(t, env.trig.calls, 1)
}

// --- error: non-owner is forbidden ---

// TestRunNowSlice_NonOwner_403 verifies the owner-or-admin authz gate through
// the real service and SQLite — the schedule is owned by one user, another user
// is rejected.
func TestRunNowSlice_NonOwner_403(t *testing.T) {
	env := newRunNowSliceEnv(t)
	owner := newSliceUser(t, env.userRepo, "owner@slice.test", identity.RoleUser)
	other := newSliceUser(t, env.userRepo, "other@slice.test", identity.RoleUser)

	sched := newSliceSchedule(t, env.svc, owner, "owner-workflow", "default")

	req := httptest.NewRequest(http.MethodPost, "/"+sched.ID.String()+"/run", http.NoBody)
	rec := httptest.NewRecorder()
	env.router(other).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "non-owner must be rejected 403")
	assert.Empty(t, env.trig.calls, "trigger must not fire on forbidden request")
}

// --- edge: unknown schedule ---

// TestRunNowSlice_NotFound_404 verifies that a non-existent schedule ID returns
// 404 through the real service→SQLite path.
func TestRunNowSlice_NotFound_404(t *testing.T) {
	env := newRunNowSliceEnv(t)
	caller := newSliceUser(t, env.userRepo, "carol@slice.test", identity.RoleUser)

	phantomID := uuid.New().String()
	req := httptest.NewRequest(http.MethodPost, "/"+phantomID+"/run", http.NoBody)
	rec := httptest.NewRecorder()
	env.router(caller).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "phantom schedule must return 404")
	assert.Empty(t, env.trig.calls)
}
