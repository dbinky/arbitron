package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mark3labs/mcp-go/server"

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

func newRouterTestDeps() (*mockWorkflowRepo, *mockInstanceRepo, *mockNodeExecRepo, *mockSnapshotRepo, *routerMockService, *mockFileReader, engine.EventBus, *config.Config, adapthttp.QueueManagerPort, instance.QueueRepository) {
	wfRepo := newMockWorkflowRepo()
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	svc := &routerMockService{}
	fileReader := newMockFileReader()
	bus := eventbus.NewNoOp()
	cfg := &config.Config{
		Server:   config.ServerConfig{Host: "localhost", Port: 8080},
		Database: config.DatabaseConfig{Path: "./arbitron.db"},
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
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

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
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

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
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

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
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

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
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: Queue config endpoints
// ---------------------------------------------------------------------------

func TestRouter_GetConfig_IncludesQueueSection(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, _ := newRouterTestDeps()
	// Use a queue repo that returns non-zero counts.
	qr := &mockCountingQueueRepo{running: 3, queued: 5, waiting: 2}
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, &mockQueueManagerForRouter{}, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	queue, ok := resp["queue"].(map[string]any)
	require.True(t, ok, "queue section must be present")
	assert.Equal(t, float64(20), queue["globalCap"])
	assert.Equal(t, float64(5), queue["perUserCap"])
	assert.Equal(t, float64(3), queue["runningCount"])
	assert.Equal(t, float64(5), queue["queuedCount"])
	assert.Equal(t, float64(2), queue["waitingCount"])
}

func TestRouter_UpdateQueueConfig_UpdatesBothCaps(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, qr := newRouterTestDeps()
	qm := &mockCapturingQueueManager{}
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"globalCap":30,"perUserCap":10}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, float64(30), resp["globalCap"])
	assert.Equal(t, float64(10), resp["perUserCap"])

	qm.mu.Lock()
	assert.Equal(t, 30, qm.lastGlobalCap)
	assert.Equal(t, 10, qm.lastPerUserCap)
	qm.mu.Unlock()
}

func TestRouter_UpdateQueueConfig_CapRaised_TriggersEvaluate(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, qr := newRouterTestDeps()
	evaluateCh := make(chan struct{}, 1)
	qm := &mockCapturingQueueManager{evaluateCh: evaluateCh}
	cfg.Queue.GlobalCap = 20 // old cap
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	// Raise cap from 20 → 30.
	body := `{"globalCap":30}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case <-evaluateCh:
		// Evaluate was called — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("Evaluate was not called after cap was raised")
	}
}

func TestRouter_UpdateQueueConfig_NegativeCap_400(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"globalCap":-1}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRouter_UpdateQueueConfig_InvalidBody_400(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{not json`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRouter_InstanceRoutes_SetPriority(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	inst := &instance.Instance{
		ID:           uuid.New(),
		State:        instance.StateQueued,
		Priority:     instance.PriorityNormal,
		Variables:    map[string]any{},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	instRepo.instances[inst.ID] = inst
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"priority":"high"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+inst.ID.String()+"/priority", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "priority_updated", resp["status"])
}

// ---------------------------------------------------------------------------
// Additional mocks for queue config tests
// ---------------------------------------------------------------------------

type mockCountingQueueRepo struct {
	mockQueueRepoForRouter
	running int
	queued  int
	waiting int
}

func (r *mockCountingQueueRepo) CountRunning(_ context.Context) (int, error)  { return r.running, nil }
func (r *mockCountingQueueRepo) CountQueued(_ context.Context) (int, error)   { return r.queued, nil }
func (r *mockCountingQueueRepo) CountWaiting(_ context.Context) (int, error)  { return r.waiting, nil }

type mockCapturingQueueManager struct {
	mu             sync.Mutex
	lastGlobalCap  int
	lastPerUserCap int
	evaluateCh     chan struct{}
}

func (m *mockCapturingQueueManager) UpdateConfig(globalCap, perUserCap int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGlobalCap = globalCap
	m.lastPerUserCap = perUserCap
}

func (m *mockCapturingQueueManager) Evaluate(_ context.Context) error {
	if m.evaluateCh != nil {
		select {
		case m.evaluateCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestRouter_UpdateQueueConfig_OnlyPerUserCap_PartialUpdate(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, qr := newRouterTestDeps()
	qm := &mockCapturingQueueManager{}
	cfg.Queue.GlobalCap = 20
	cfg.Queue.PerUserCap = 5
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	// Update only perUserCap — globalCap should stay at 20.
	body := `{"perUserCap":15}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, float64(20), resp["globalCap"], "globalCap should remain unchanged")
	assert.Equal(t, float64(15), resp["perUserCap"])

	qm.mu.Lock()
	assert.Equal(t, 20, qm.lastGlobalCap, "globalCap passed to UpdateConfig must be unchanged")
	assert.Equal(t, 15, qm.lastPerUserCap)
	qm.mu.Unlock()
}

func TestRouter_UpdateQueueConfig_CapLowered_NoEvaluate(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, qr := newRouterTestDeps()
	evaluateCh := make(chan struct{}, 1)
	qm := &mockCapturingQueueManager{evaluateCh: evaluateCh}
	cfg.Queue.GlobalCap = 30 // old cap — higher than new
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	// Lower cap from 30 → 10 — should NOT trigger Evaluate.
	body := `{"globalCap":10}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Give any goroutine a brief window to fire (it shouldn't).
	time.Sleep(50 * time.Millisecond)
	select {
	case <-evaluateCh:
		t.Fatal("Evaluate must NOT be called when cap is lowered")
	default:
		// Correct: evaluate was not called.
	}
}

// ---------------------------------------------------------------------------
// Tests: MCP endpoint mounted at /mcp
// ---------------------------------------------------------------------------

func TestRouter_MCPEndpoint_NotMounted_WhenNil(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// With no MCP handler and no dashboard fallthrough, chi returns 404.
	assert.Equal(t, http.StatusNotFound, w.Code, "POST /mcp should 404 when MCP handler is nil")
}

func TestRouter_MCPEndpoint_Mounted(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()

	// Use a minimal stub handler that proves the route is mounted.
	stubMCP := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0"}`))
	})

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
		MCPHandler:   stubMCP,
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "POST /mcp should be handled by the mounted MCP handler")
}

func TestRouter_MCPEndpoint_RealHandler(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()

	// Use the real mcp-go StreamableHTTPServer to verify integration.
	mcpServer := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	mcpHTTP := server.NewStreamableHTTPServer(mcpServer)

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
		MCPHandler:   mcpHTTP,
	})

	// Send a valid JSON-RPC initialize request.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.0.1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The MCP server should return 200 with a JSON-RPC response (not 404 or 405).
	assert.Equal(t, http.StatusOK, w.Code, "MCP initialize should succeed")
	assert.Contains(t, w.Body.String(), `"jsonrpc"`, "response should be valid JSON-RPC")
}

func TestRouter_MCPEndpoint_SecurityHeaders(t *testing.T) {
	// Edge case: security headers middleware is applied via r.Use before any
	// route, so it must wrap the mounted /mcp handler too. A missing header
	// here means the middleware chain doesn't cover mounted sub-routers.
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	stubMCP := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
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
		MCPHandler:   stubMCP,
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), "security header must be set on /mcp responses")
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.NotEmpty(t, w.Header().Get("Referrer-Policy"))
}

func TestRouter_GetConfig_ReflectsUpdatedQueueCaps(t *testing.T) {
	// PUT /system/config/queue must update the values returned by GET /system/config.
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, _, qr := newRouterTestDeps()
	qm := &mockCapturingQueueManager{}
	cfg.Queue.GlobalCap = 20
	cfg.Queue.PerUserCap = 5
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	// Confirm initial GET values.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	getW := httptest.NewRecorder()
	router.ServeHTTP(getW, getReq)
	require.Equal(t, http.StatusOK, getW.Code)
	var before map[string]any
	require.NoError(t, json.NewDecoder(getW.Body).Decode(&before))
	assert.Equal(t, float64(20), before["queue"].(map[string]any)["globalCap"])
	assert.Equal(t, float64(5), before["queue"].(map[string]any)["perUserCap"])

	// Update both caps.
	putBody := `{"globalCap":50,"perUserCap":15}`
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/queue", strings.NewReader(putBody))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	router.ServeHTTP(putW, putReq)
	require.Equal(t, http.StatusOK, putW.Code)

	// GET must now return the updated caps.
	getReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	getW2 := httptest.NewRecorder()
	router.ServeHTTP(getW2, getReq2)
	require.Equal(t, http.StatusOK, getW2.Code)
	var after map[string]any
	require.NoError(t, json.NewDecoder(getW2.Body).Decode(&after))
	assert.Equal(t, float64(50), after["queue"].(map[string]any)["globalCap"], "globalCap must reflect PUT update")
	assert.Equal(t, float64(15), after["queue"].(map[string]any)["perUserCap"], "perUserCap must reflect PUT update")
}

// Success: GET /system/config exposes redacted teams config + publicBaseUrl.
func TestRouter_SystemConfig_TeamsRedactedAndBaseURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Server.PublicBaseURL = "http://localhost:8080"
	cfg.Notifications.Teams.WebhookURL = "https://example.com/hook"
	cfg.Notifications.Teams.Enabled = true
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	teams, ok := resp["teams"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "**REDACTED**", teams["webhookUrl"])
	assert.Equal(t, true, teams["enabled"])

	server, ok := resp["server"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "http://localhost:8080", server["publicBaseUrl"])
}

// Edge-case: unset webhook reads back as "" with enabled=false.
func TestRouter_SystemConfig_TeamsUnset(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	teams := resp["teams"].(map[string]any)
	assert.Equal(t, "", teams["webhookUrl"])
	assert.Equal(t, false, teams["enabled"])
}

// Success: PUT /system/config/teams updates URL + toggle; GET reflects it.
func TestRouter_UpdateTeamsConfig_Success(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":"https://example.com/hook","enabled":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "https://example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
}

// Edge-case: a toggle-only PUT preserves the existing webhook URL.
func TestRouter_UpdateTeamsConfig_PartialPreservesURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://kept.example.com/hook"
	cfg.Notifications.Teams.Enabled = true
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"enabled":false}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "https://kept.example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Failure: a non-http(s) webhook URL is rejected with 400, config unchanged.
func TestRouter_UpdateTeamsConfig_InvalidURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://old.example.com/hook"
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":"ftp://nope"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}

// Error: a malformed JSON body is rejected with 400.
func TestRouter_UpdateTeamsConfig_MalformedJSON(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Error: a body larger than the MaxBytesReader limit (1 MiB) is rejected with
// 400 and leaves the stored config unchanged. This is the Phase 1 design's
// "oversize body" error category, distinct from the malformed-JSON error above.
func TestRouter_UpdateTeamsConfig_OversizeBody(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://old.example.com/hook"
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	// A valid-JSON body whose webhookUrl value pushes the payload past 1 MiB.
	body := `{"webhookUrl":"https://example.com/` + strings.Repeat("a", (1<<20)+16) + `"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}

// Edge-case: clearing the URL with an explicit empty string is allowed.
func TestRouter_UpdateTeamsConfig_ClearURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://old.example.com/hook"
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", cfg.Notifications.Teams.WebhookURL)
}
