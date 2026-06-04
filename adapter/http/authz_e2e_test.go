package http_test

// authz_e2e_test.go — end-to-end adapter-level authorization tests for the
// HTTP surface.
//
// These tests wire the REAL engine (not stubs) so that domain-layer scoping
// rules are exercised through the HTTP handler exactly as they would be in
// production. A non-admin identity is injected into the request context (as
// the auth middleware would in org mode) and each endpoint must respect the
// owner-based isolation rules.
//
// Covered: GET /instances, GET /instances/{id}, GET /instances/{id}/trace,
//          GET /instances/{id}/variables, POST /instances/{id}/pause.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ---------------------------------------------------------------------------
// Minimal in-memory repos for E2E authz tests
// ---------------------------------------------------------------------------

type httpAuthzInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newHTTPAuthzInstanceRepo(insts ...*instance.Instance) *httpAuthzInstanceRepo {
	r := &httpAuthzInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
	for _, i := range insts {
		r.instances[i.ID] = i
	}
	return r
}

func (r *httpAuthzInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}

func (r *httpAuthzInstanceRepo) List(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
	var out []instance.InstanceSummary
	for _, inst := range r.instances {
		if f.OwnerID != nil && inst.OwnerID != *f.OwnerID {
			continue
		}
		out = append(out, instance.InstanceSummary{
			ID:           inst.ID,
			WorkflowName: inst.WorkflowName,
			State:        inst.State,
		})
	}
	return out, nil
}

func (r *httpAuthzInstanceRepo) Create(_ context.Context, _ *instance.Instance) error { return nil }
func (r *httpAuthzInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return nil
}
func (r *httpAuthzInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}
func (r *httpAuthzInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (r *httpAuthzInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (r *httpAuthzInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error         { return nil }
func (r *httpAuthzInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *httpAuthzInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return nil
}
func (r *httpAuthzInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return nil
}
func (r *httpAuthzInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error {
	return nil
}
func (r *httpAuthzInstanceRepo) SetWebhookReceived(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (r *httpAuthzInstanceRepo) Stats(_ context.Context, _ string, _, _ time.Time) (*instance.WorkflowStats, error) {
	return nil, nil
}

var _ instance.InstanceRepository = (*httpAuthzInstanceRepo)(nil)

type httpAuthzNodeExecRepo struct{}

func (r *httpAuthzNodeExecRepo) Create(_ context.Context, _ *instance.NodeExecution) error { return nil }
func (r *httpAuthzNodeExecRepo) Update(_ context.Context, _ *instance.NodeExecution) error { return nil }
func (r *httpAuthzNodeExecRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
	return []instance.NodeExecution{}, nil
}
func (r *httpAuthzNodeExecRepo) GetByInstanceAndNode(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
	return nil, instance.ErrNotFound
}

var _ instance.NodeExecutionRepository = (*httpAuthzNodeExecRepo)(nil)

// newHTTPAuthzInstance creates a test instance with the given owner.
func newHTTPAuthzInstance(ownerID uuid.UUID, wfName string) *instance.Instance {
	return &instance.Instance{
		ID:           uuid.New(),
		OwnerID:      ownerID,
		WorkflowName: wfName,
		State:        instance.StateRunning,
		Variables:    map[string]any{"k": "v"},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// newHTTPAuthzHandler builds a real engine + handler for E2E authz testing.
func newHTTPAuthzHandler(repo *httpAuthzInstanceRepo) *adapthttp.InstanceHandler {
	eng := engine.NewEngine(
		nil, repo, &httpAuthzNodeExecRepo{}, nil, nil,
		map[workflow.NodeType]engine.NodeExecutor{},
		nil, nil, nil,
	)
	return adapthttp.NewInstanceHandler(repo, &httpAuthzNodeExecRepo{}, nil, eng)
}

// withIDAndIdentity sets both the chi URL param "id" and the caller's identity
// on the request context — exactly what the router + auth middleware provide in
// production.
func withIDAndIdentity(r *http.Request, id uuid.UUID, id2 identity.Identity) *http.Request {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add("id", id.String())
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = identity.WithIdentity(ctx, id2)
	return r.WithContext(ctx)
}

func httpNonAdmin(userID uuid.UUID) identity.Identity {
	return identity.Identity{UserID: userID, Username: "alice", Role: identity.RoleUser}
}

func httpAdmin() identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}
}

// ---------------------------------------------------------------------------
// GET /instances — list scoped to caller
// ---------------------------------------------------------------------------

func TestHTTPAuthzE2E_InstanceList(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newHTTPAuthzInstance(ownerA, "wf-a")
	instB := newHTTPAuthzInstance(ownerB, "wf-b")

	repo := newHTTPAuthzInstanceRepo(instA, instB)
	h := newHTTPAuthzHandler(repo)

	t.Run("non-admin sees only own instances", func(t *testing.T) {
		ctx := identity.WithIdentity(context.Background(), httpNonAdmin(ownerA))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		h.List(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, instA.ID.String(), "ownerA must see their own instance")
		assert.NotContains(t, body, instB.ID.String(), "ownerA must not see ownerB's instance")
	})

	t.Run("admin sees all instances", func(t *testing.T) {
		ctx := identity.WithIdentity(context.Background(), httpAdmin())
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		h.List(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		body := w.Body.String()
		assert.Contains(t, body, instA.ID.String())
		assert.Contains(t, body, instB.ID.String())
	})
}

// ---------------------------------------------------------------------------
// GET /instances/{id} — get scoped to owner
// ---------------------------------------------------------------------------

func TestHTTPAuthzE2E_InstanceGet(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newHTTPAuthzInstance(ownerA, "wf-a")

	repo := newHTTPAuthzInstanceRepo(instA)
	h := newHTTPAuthzHandler(repo)

	t.Run("owner gets 200 for their own instance", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String(), nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerA))
		w := httptest.NewRecorder()
		h.Get(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("non-owner gets 404 (existence not leaked)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String(), nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerB))
		w := httptest.NewRecorder()
		h.Get(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("admin gets 200 for any instance", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String(), nil)
		req = withIDAndIdentity(req, instA.ID, httpAdmin())
		w := httptest.NewRecorder()
		h.Get(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// GET /instances/{id}/trace — trace scoped to owner
// ---------------------------------------------------------------------------

func TestHTTPAuthzE2E_InstanceTrace(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newHTTPAuthzInstance(ownerA, "wf-a")

	repo := newHTTPAuthzInstanceRepo(instA)
	h := newHTTPAuthzHandler(repo)

	t.Run("owner gets 200 for their own trace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/trace", nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerA))
		w := httptest.NewRecorder()
		h.GetTrace(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("non-owner gets 404 for another user's trace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/trace", nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerB))
		w := httptest.NewRecorder()
		h.GetTrace(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("admin gets 200 for any trace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/trace", nil)
		req = withIDAndIdentity(req, instA.ID, httpAdmin())
		w := httptest.NewRecorder()
		h.GetTrace(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// GET /instances/{id}/variables — variables scoped to owner
// ---------------------------------------------------------------------------

func TestHTTPAuthzE2E_InstanceVariables(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newHTTPAuthzInstance(ownerA, "wf-a")

	repo := newHTTPAuthzInstanceRepo(instA)
	h := newHTTPAuthzHandler(repo)

	t.Run("owner gets 200 for their own variables", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/variables", nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerA))
		w := httptest.NewRecorder()
		h.GetVariables(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("non-owner gets 404 for another user's variables", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/variables", nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerB))
		w := httptest.NewRecorder()
		h.GetVariables(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("admin gets 200 for any variables", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+instA.ID.String()+"/variables", nil)
		req = withIDAndIdentity(req, instA.ID, httpAdmin())
		w := httptest.NewRecorder()
		h.GetVariables(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// POST /instances/{id}/pause — intervention scoped to owner-or-admin
// ---------------------------------------------------------------------------

func TestHTTPAuthzE2E_InstancePause(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newHTTPAuthzInstance(ownerA, "wf-a")

	repo := newHTTPAuthzInstanceRepo(instA)
	h := newHTTPAuthzHandler(repo)

	t.Run("non-owner gets 404 for another user's pause", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+instA.ID.String()+"/pause", nil)
		req = withIDAndIdentity(req, instA.ID, httpNonAdmin(ownerB))
		w := httptest.NewRecorder()
		h.Pause(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
