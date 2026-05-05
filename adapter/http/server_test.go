package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Full router integration mocks
// ---------------------------------------------------------------------------

// routerMockService implements both WorkflowService and InstanceService.
type routerMockService struct {
	triggerFn     func(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
	pauseFn       func(id uuid.UUID) error
	resumeFn      func(id uuid.UUID) error
	cancelFn      func(id uuid.UUID) error
	retryFn       func(id uuid.UUID, variableEdits map[string]any) error
	rerunFn       func(id uuid.UUID) (uuid.UUID, error)
	setPriorityFn func(id uuid.UUID, priority string) error
}

func (m *routerMockService) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	if m.triggerFn != nil {
		return m.triggerFn(workflowName, variables, priority)
	}
	return uuid.New(), true, nil
}

func (m *routerMockService) Pause(id uuid.UUID) error {
	if m.pauseFn != nil {
		return m.pauseFn(id)
	}
	return nil
}

func (m *routerMockService) Resume(id uuid.UUID) error {
	if m.resumeFn != nil {
		return m.resumeFn(id)
	}
	return nil
}

func (m *routerMockService) Cancel(id uuid.UUID) error {
	if m.cancelFn != nil {
		return m.cancelFn(id)
	}
	return nil
}

func (m *routerMockService) Retry(id uuid.UUID, variableEdits map[string]any) error {
	if m.retryFn != nil {
		return m.retryFn(id, variableEdits)
	}
	return nil
}

func (m *routerMockService) Rerun(id uuid.UUID) (uuid.UUID, error) {
	if m.rerunFn != nil {
		return m.rerunFn(id)
	}
	return uuid.New(), nil
}

func (m *routerMockService) SetPriority(id uuid.UUID, priority string) error {
	if m.setPriorityFn != nil {
		return m.setPriorityFn(id, priority)
	}
	return nil
}

// mockQueueManagerForRouter implements adapthttp.QueueManagerPort
type mockQueueManagerForRouter struct{}

func (m *mockQueueManagerForRouter) UpdateConfig(_, _ int)                    {}
func (m *mockQueueManagerForRouter) Evaluate(_ context.Context) error         { return nil }

// mockQueueRepoForRouter implements instance.QueueRepository
type mockQueueRepoForRouter struct{}

func (r *mockQueueRepoForRouter) CountRunning(_ context.Context) (int, error)                              { return 0, nil }
func (r *mockQueueRepoForRouter) CountRunningByOwner(_ context.Context, _ uuid.UUID) (int, error)         { return 0, nil }
func (r *mockQueueRepoForRouter) CountQueued(_ context.Context) (int, error)                               { return 0, nil }
func (r *mockQueueRepoForRouter) CountWaiting(_ context.Context) (int, error)                              { return 0, nil }
func (r *mockQueueRepoForRouter) NextEligible(_ context.Context, _ int, _ int) (*instance.Instance, error) { return nil, nil }
func (r *mockQueueRepoForRouter) SetQueued(_ context.Context, _ uuid.UUID, _ time.Time) error              { return nil }
func (r *mockQueueRepoForRouter) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error)     { return nil, nil }
func (r *mockQueueRepoForRouter) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error)   { return nil, nil }

func newRouterTestDeps() (*mockWorkflowRepo, *mockInstanceRepo, *mockNodeExecRepo, *mockSnapshotRepo, *routerMockService, *mockFileReader, engine.EventBus, *config.Config, adapthttp.QueueManagerPort, adapthttp.QueueRepoPort) {
	wfRepo := newMockWorkflowRepo()
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	svc := &routerMockService{}
	fileReader := newMockFileReader()
	bus := eventbus.NewNoOp()
	cfg := &config.Config{
		Server:   config.ServerConfig{Host: "localhost", Port: 8080},
		Database: config.DatabaseConfig{URL: "postgres://user:pass@localhost/arbitron"},
		LLM:      config.LLMConfig{Provider: "anthropic", APIKey: "sk-secret-key-12345"},
		Logging:  config.LoggingConfig{Level: "info"},
		Queue:    config.QueueConfig{GlobalCap: 20, PerUserCap: 5},
	}
	qm := &mockQueueManagerForRouter{}
	qr := &mockQueueRepoForRouter{}
	return wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr
}

// ---------------------------------------------------------------------------
// Tests: Health endpoint
// ---------------------------------------------------------------------------

func TestRouter_Health(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]string
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "ok", resp["status"])
}

// ---------------------------------------------------------------------------
// Tests: System config endpoint
// ---------------------------------------------------------------------------

func TestRouter_SystemConfig(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err)

	// API key should be redacted
	llm, ok := resp["llm"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "**REDACTED**", llm["apiKey"])
	assert.Equal(t, "anthropic", llm["provider"])

	// Server config should be present
	server, ok := resp["server"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "localhost", server["host"])
}

// ---------------------------------------------------------------------------
// Tests: Workflow routes wired correctly
// ---------------------------------------------------------------------------

func TestRouter_WorkflowRoutes(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	wfRepo.workflows["my-wf"] = &workflow.Workflow{
		Name:        "my-wf",
		Description: "Test workflow",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
		},
	}
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr)

	t.Run("GET /api/v1/workflows", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /api/v1/workflows/{name}", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/my-wf", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/workflows/{name}/validate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/my-wf/validate", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/workflows/{name}/trigger", func(t *testing.T) {
		body := `{"variables":{}}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/my-wf/trigger", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Instance routes wired correctly
// ---------------------------------------------------------------------------

func TestRouter_InstanceRoutes(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	inst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           uuid.New(),
		WorkflowName:      "test-wf",
		State:             instance.StateRunning,
		Priority:          instance.PriorityNormal,
		Variables:         map[string]any{"x": float64(1)},
		OriginalVariables: map[string]any{"x": float64(1)},
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	instRepo.instances[inst.ID] = inst
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr)

	t.Run("GET /api/v1/instances", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /api/v1/instances/{id}", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String(), nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/instances/{id}/pause", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/pause", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/instances/{id}/resume", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/resume", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/instances/{id}/cancel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/cancel", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/instances/{id}/retry", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/retry", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("POST /api/v1/instances/{id}/rerun", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/rerun", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("GET /api/v1/instances/{id}/trace", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String()+"/trace", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /api/v1/instances/{id}/trace/{nodeId}", func(t *testing.T) {
		nodeExecRepo.executions = []*instance.NodeExecution{
			{
				ID:         uuid.New(),
				InstanceID: inst.ID,
				NodeID:     "A",
				Attempt:    1,
				Status:     instance.NodeStatusSucceeded,
				StartedAt:  time.Now(),
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String()+"/trace/A", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /api/v1/instances/{id}/variables", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String()+"/variables", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET /api/v1/instances/{id}/variables/history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String()+"/variables/history", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: 404 for unknown routes
// ---------------------------------------------------------------------------

func TestRouter_NotFound(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
