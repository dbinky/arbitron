package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Mocks for InstanceHandler
// ---------------------------------------------------------------------------

// errInstanceRepo always returns the stored error from List and Get.
type errInstanceRepo struct{ err error }

func (r *errInstanceRepo) Create(_ context.Context, _ *instance.Instance) error { return r.err }
func (r *errInstanceRepo) Get(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
	return nil, r.err
}
func (r *errInstanceRepo) List(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
	return nil, r.err
}
func (r *errInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error    { return r.err }
func (r *errInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error { return r.err }
func (r *errInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error       { return r.err }
func (r *errInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error               { return r.err }
func (r *errInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error                       { return r.err }
func (r *errInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error)            { return nil, r.err }
func (r *errInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error { return r.err }
func (r *errInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error          { return r.err }
func (r *errInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error { return r.err }
func (r *errInstanceRepo) SetWebhookReceived(_ context.Context, _ uuid.UUID, _ time.Time) error    { return r.err }
func (r *errInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) { return nil, r.err }

// errNodeExecRepo always returns the stored error.
type errNodeExecRepo struct{ err error }

func (r *errNodeExecRepo) Create(_ context.Context, _ *instance.NodeExecution) error { return r.err }
func (r *errNodeExecRepo) Update(_ context.Context, _ *instance.NodeExecution) error { return r.err }
func (r *errNodeExecRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
	return nil, r.err
}
func (r *errNodeExecRepo) GetByInstanceAndNode(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
	return nil, r.err
}

// errSnapshotRepo always returns the stored error.
type errSnapshotRepo struct{ err error }

func (r *errSnapshotRepo) Create(_ context.Context, _ *instance.VariableSnapshot) error { return r.err }
func (r *errSnapshotRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.VariableSnapshot, error) {
	return nil, r.err
}

type mockInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockInstanceRepo) Create(_ context.Context, inst *instance.Instance) error {
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	cp := *inst
	if inst.Variables != nil {
		cp.Variables = make(map[string]any, len(inst.Variables))
		for k, v := range inst.Variables {
			cp.Variables[k] = v
		}
	}
	if inst.OriginalVariables != nil {
		cp.OriginalVariables = make(map[string]any, len(inst.OriginalVariables))
		for k, v := range inst.OriginalVariables {
			cp.OriginalVariables[k] = v
		}
	}
	return &cp, nil
}

func (r *mockInstanceRepo) List(_ context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	var summaries []instance.InstanceSummary
	for _, inst := range r.instances {
		if filters.State != nil && inst.State != *filters.State {
			continue
		}
		if filters.WorkflowName != nil && inst.WorkflowName != *filters.WorkflowName {
			continue
		}
		if filters.Since != nil && !inst.CreatedAt.After(*filters.Since) {
			continue
		}
		summaries = append(summaries, instance.InstanceSummary{
			ID:           inst.ID,
			WorkflowName: inst.WorkflowName,
			State:        inst.State,
			Priority:     inst.Priority,
			CreatedAt:    inst.CreatedAt,
			UpdatedAt:    inst.UpdatedAt,
		})
	}
	// Apply limit/offset
	if filters.Offset > 0 && filters.Offset < len(summaries) {
		summaries = summaries[filters.Offset:]
	} else if filters.Offset >= len(summaries) {
		summaries = nil
	}
	if filters.Limit > 0 && filters.Limit < len(summaries) {
		summaries = summaries[:filters.Limit]
	}
	return summaries, nil
}

func (r *mockInstanceRepo) UpdateState(_ context.Context, id uuid.UUID, state instance.State) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = state
	return nil
}

func (r *mockInstanceRepo) UpdateVariables(_ context.Context, id uuid.UUID, variables map[string]any) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.Variables = variables
	return nil
}

func (r *mockInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (r *mockInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (r *mockInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error {
	return nil
}

func (r *mockInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}

func (r *mockInstanceRepo) UpdatePriority(_ context.Context, id uuid.UUID, priority instance.Priority) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.Priority = priority
	return nil
}

func (r *mockInstanceRepo) SetWaiting(_ context.Context, id uuid.UUID, waitingUntil *time.Time) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	if waitingUntil != nil {
		inst.State = instance.StateWaiting
	}
	inst.WaitingUntil = waitingUntil
	return nil
}

func (r *mockInstanceRepo) SetWebhookFields(_ context.Context, id uuid.UUID, token *string, secretHash *string) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookToken = token
	inst.WebhookSecretHash = secretHash
	return nil
}

func (r *mockInstanceRepo) SetWebhookReceived(_ context.Context, id uuid.UUID, receivedAt time.Time) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookReceivedAt = &receivedAt
	return nil
}

func (r *mockInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return &instance.WorkflowStats{}, nil
}

// mockNodeExecutionRepo for InstanceHandler
type mockNodeExecRepo struct {
	executions []*instance.NodeExecution
}

func newMockNodeExecRepo() *mockNodeExecRepo {
	return &mockNodeExecRepo{}
}

func (r *mockNodeExecRepo) Create(_ context.Context, exec *instance.NodeExecution) error {
	r.executions = append(r.executions, exec)
	return nil
}

func (r *mockNodeExecRepo) Update(_ context.Context, _ *instance.NodeExecution) error {
	return nil
}

func (r *mockNodeExecRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	var result []instance.NodeExecution
	for _, e := range r.executions {
		if e.InstanceID == instanceID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (r *mockNodeExecRepo) GetByInstanceAndNode(_ context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	for _, e := range r.executions {
		if e.InstanceID == instanceID && e.NodeID == nodeID {
			return e, nil
		}
	}
	return nil, fmt.Errorf("node execution instance=%s node=%s: %w", instanceID, nodeID, instance.ErrNotFound)
}

// mockSnapshotRepo for InstanceHandler
type mockSnapshotRepo struct {
	snapshots []*instance.VariableSnapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{}
}

func (r *mockSnapshotRepo) Create(_ context.Context, snap *instance.VariableSnapshot) error {
	r.snapshots = append(r.snapshots, snap)
	return nil
}

func (r *mockSnapshotRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
	var result []instance.VariableSnapshot
	for _, s := range r.snapshots {
		if s.InstanceID == instanceID {
			result = append(result, *s)
		}
	}
	return result, nil
}

// mockInstanceService implements adapthttp.InstanceService
type mockInstanceService struct {
	listFn           func(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	getFn            func(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	traceFn          func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	nodeTraceFn      func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	variablesFn      func(ctx context.Context, id uuid.UUID) (map[string]any, error)
	variableHistFn   func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	pauseFn          func(id uuid.UUID) error
	resumeFn         func(id uuid.UUID) error
	cancelFn         func(id uuid.UUID) error
	retryFn          func(id uuid.UUID, variableEdits map[string]any) error
	rerunFn          func(id uuid.UUID) (uuid.UUID, error)
	setPriorityFn    func(id uuid.UUID, priority string) error
}

func (m *mockInstanceService) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filters)
	}
	return nil, nil
}

func (m *mockInstanceService) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return nil, instance.ErrNotFound
}

func (m *mockInstanceService) GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if m.traceFn != nil {
		return m.traceFn(ctx, id)
	}
	return nil, nil
}

func (m *mockInstanceService) GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if m.nodeTraceFn != nil {
		return m.nodeTraceFn(ctx, id, nodeID)
	}
	return nil, instance.ErrNotFound
}

func (m *mockInstanceService) GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	if m.variablesFn != nil {
		return m.variablesFn(ctx, id)
	}
	return nil, instance.ErrNotFound
}

func (m *mockInstanceService) GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if m.variableHistFn != nil {
		return m.variableHistFn(ctx, id)
	}
	return nil, nil
}

func (m *mockInstanceService) Pause(id uuid.UUID) error {
	if m.pauseFn != nil {
		return m.pauseFn(id)
	}
	return nil
}

func (m *mockInstanceService) Resume(id uuid.UUID) error {
	if m.resumeFn != nil {
		return m.resumeFn(id)
	}
	return nil
}

func (m *mockInstanceService) Cancel(id uuid.UUID) error {
	if m.cancelFn != nil {
		return m.cancelFn(id)
	}
	return nil
}

func (m *mockInstanceService) Retry(id uuid.UUID, variableEdits map[string]any) error {
	if m.retryFn != nil {
		return m.retryFn(id, variableEdits)
	}
	return nil
}

func (m *mockInstanceService) Rerun(id uuid.UUID) (uuid.UUID, error) {
	if m.rerunFn != nil {
		return m.rerunFn(id)
	}
	return uuid.New(), nil
}

func (m *mockInstanceService) SetPriority(id uuid.UUID, priority string) error {
	if m.setPriorityFn != nil {
		return m.setPriorityFn(id, priority)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

func testInstance(state instance.State) *instance.Instance {
	now := time.Now()
	return &instance.Instance{
		ID:           uuid.New(),
		OwnerID:      uuid.New(),
		WorkflowName: "test-wf",
		State:        state,
		Priority:     instance.PriorityNormal,
		Variables:    map[string]any{"key": "value"},
		OriginalVariables: map[string]any{"key": "value"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func newInstanceHandler() (*adapthttp.InstanceHandler, *mockInstanceRepo, *mockNodeExecRepo, *mockSnapshotRepo, *mockInstanceService) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	svc := &mockInstanceService{}
	// By default the scoped read/list services delegate to the repos
	// (admin-equivalent, unconstrained scope) so existing handler tests that seed
	// the repos keep passing. Scoping itself is unit-tested in the engine; these
	// tests cover the handler wiring (delegation to the service) and parsing.
	svc.listFn = instRepo.List
	svc.getFn = instRepo.Get
	svc.traceFn = func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
		if _, err := instRepo.Get(ctx, id); err != nil {
			return nil, err
		}
		return nodeExecRepo.ListByInstance(ctx, id)
	}
	svc.nodeTraceFn = func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
		if _, err := instRepo.Get(ctx, id); err != nil {
			return nil, err
		}
		return nodeExecRepo.GetByInstanceAndNode(ctx, id, nodeID)
	}
	svc.variablesFn = func(ctx context.Context, id uuid.UUID) (map[string]any, error) {
		inst, err := instRepo.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return inst.Variables, nil
	}
	svc.variableHistFn = func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
		if _, err := instRepo.Get(ctx, id); err != nil {
			return nil, err
		}
		return snapRepo.ListByInstance(ctx, id)
	}
	h := adapthttp.NewInstanceHandler(instRepo, nodeExecRepo, snapRepo, svc)
	return h, instRepo, nodeExecRepo, snapRepo, svc
}

// serviceOverRepos builds a mockInstanceService whose read methods delegate to
// the given repos (unconstrained scope), so the pre-existing internal-error
// path tests — which seed error repos and expect the handler to surface 500 —
// keep exercising the same repo error through the service seam.
func serviceOverRepos(instRepo instance.InstanceRepository, nodeExecRepo instance.NodeExecutionRepository, snapRepo instance.VariableSnapshotRepository) *mockInstanceService {
	return &mockInstanceService{
		getFn: instRepo.Get,
		traceFn: func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
			if _, err := instRepo.Get(ctx, id); err != nil {
				return nil, err
			}
			return nodeExecRepo.ListByInstance(ctx, id)
		},
		nodeTraceFn: func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
			if _, err := instRepo.Get(ctx, id); err != nil {
				return nil, err
			}
			return nodeExecRepo.GetByInstanceAndNode(ctx, id, nodeID)
		},
		variablesFn: func(ctx context.Context, id uuid.UUID) (map[string]any, error) {
			inst, err := instRepo.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			return inst.Variables, nil
		},
		variableHistFn: func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
			if _, err := instRepo.Get(ctx, id); err != nil {
				return nil, err
			}
			return snapRepo.ListByInstance(ctx, id)
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: List instances
// ---------------------------------------------------------------------------

func TestInstanceHandler_List(t *testing.T) {
	t.Run("returns empty array when no instances", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []instance.InstanceSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Empty(t, resp)
	})

	t.Run("returns instances", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateRunning)
		instRepo.instances[inst.ID] = inst

		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []instance.InstanceSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 1)
	})

	t.Run("filters by state", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		running := testInstance(instance.StateRunning)
		completed := testInstance(instance.StateCompleted)
		instRepo.instances[running.ID] = running
		instRepo.instances[completed.ID] = completed

		req := httptest.NewRequest(http.MethodGet, "/instances?state=running", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []instance.InstanceSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 1)
		assert.Equal(t, instance.StateRunning, resp[0].State)
	})

	t.Run("filters by workflowName", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst1 := testInstance(instance.StateRunning)
		inst1.WorkflowName = "wf-a"
		inst2 := testInstance(instance.StateRunning)
		inst2.WorkflowName = "wf-b"
		instRepo.instances[inst1.ID] = inst1
		instRepo.instances[inst2.ID] = inst2

		req := httptest.NewRequest(http.MethodGet, "/instances?workflowName=wf-a", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []instance.InstanceSummary
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 1)
		assert.Equal(t, "wf-a", resp[0].WorkflowName)
	})

	t.Run("uses default limit of 50", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		// Just verify it doesn't error with no limit param
		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		w := httptest.NewRecorder()
		h.List(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("delegates to the scoped list service (not the repo)", func(t *testing.T) {
		h, instRepo, nodeExecRepo, snapRepo, svc := newInstanceHandler()
		// Seed the repo with an instance the handler must NOT see if it bypasses
		// the service. The service returns its own fixed set.
		hidden := testInstance(instance.StateRunning)
		instRepo.instances[hidden.ID] = hidden

		called := false
		fixed := []instance.InstanceSummary{{ID: uuid.New(), WorkflowName: "scoped"}}
		svc.listFn = func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
			called = true
			return fixed, nil
		}
		h = adapthttp.NewInstanceHandler(instRepo, nodeExecRepo, snapRepo, svc)

		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, called, "List must delegate to the scoped service so the owner constraint is enforced")
		var resp []instance.InstanceSummary
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp, 1)
		assert.Equal(t, "scoped", resp[0].WorkflowName)
	})

	t.Run("does not read an owner query param", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		var got instance.ListFilters
		svc.listFn = func(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
			got = f
			return nil, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/instances?ownerId="+uuid.New().String(), nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Nil(t, got.OwnerID, "handler must not let a caller set the owner filter from query input")
	})
}

// ---------------------------------------------------------------------------
// Tests: Get instance
// ---------------------------------------------------------------------------

func TestInstanceHandler_Get(t *testing.T) {
	t.Run("returns instance", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateRunning)
		instRepo.instances[inst.ID] = inst

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, inst.ID.String(), resp["id"])
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodGet, "/instances/not-a-uuid", nil)
		req = withChiURLParam(req, "id", "not-a-uuid")
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String(), nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Pause
// ---------------------------------------------------------------------------

func TestInstanceHandler_Pause(t *testing.T) {
	t.Run("pauses running instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.pauseFn = func(gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Pause(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("returns 409 for wrong state", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.pauseFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s is not running (state: completed): %w", gotID, instance.ErrNotRunning)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Pause(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.pauseFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("failed to get instance: %w", instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Pause(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/pause", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.Pause(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Resume
// ---------------------------------------------------------------------------

func TestInstanceHandler_Resume(t *testing.T) {
	t.Run("resumes paused instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.resumeFn = func(gotID uuid.UUID) error {
			assert.Equal(t, id, gotID)
			return nil
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Resume(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("returns 409 for wrong state", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.resumeFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s is not paused (state: running): %w", gotID, instance.ErrNotPaused)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Resume(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/resume", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.Resume(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.resumeFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("failed to get instance: %w", instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Resume(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Cancel
// ---------------------------------------------------------------------------

func TestInstanceHandler_Cancel(t *testing.T) {
	t.Run("cancels running instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.cancelFn = func(gotID uuid.UUID) error {
			return nil
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("returns 409 for wrong state", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.cancelFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s cannot be cancelled (state: completed): %w", gotID, instance.ErrCannotCancel)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/cancel", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.Cancel(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.cancelFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("failed to get instance: %w", instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Retry
// ---------------------------------------------------------------------------

func TestInstanceHandler_Retry(t *testing.T) {
	t.Run("retries failed instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, variableEdits map[string]any) error {
			assert.Equal(t, id, gotID)
			return nil
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("retries with variable edits", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, variableEdits map[string]any) error {
			assert.Equal(t, "new-value", variableEdits["key"])
			return nil
		}

		body := `{"variables":{"key":"new-value"}}`
		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", strings.NewReader(body))
		req = withChiURLParam(req, "id", id.String())
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("returns 409 for wrong state", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, variableEdits map[string]any) error {
			return fmt.Errorf("instance %s is not failed (state: running): %w", gotID, instance.ErrNotFailed)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/retry", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.Retry(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 400 for malformed JSON body", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		id := uuid.New()
		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", strings.NewReader("{not-json}"))
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = 10
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, variableEdits map[string]any) error {
			return fmt.Errorf("failed to get instance: %w", instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: Rerun
// ---------------------------------------------------------------------------

func TestInstanceHandler_Rerun(t *testing.T) {
	t.Run("reruns completed instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		newID := uuid.New()
		svc.rerunFn = func(gotID uuid.UUID) (uuid.UUID, error) {
			assert.Equal(t, id, gotID)
			return newID, nil
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Rerun(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, newID.String(), resp["instanceId"])
	})

	t.Run("returns 409 for wrong state", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.rerunFn = func(gotID uuid.UUID) (uuid.UUID, error) {
			return uuid.Nil, fmt.Errorf("instance %s cannot be rerun (state: running): %w", gotID, instance.ErrCannotRerun)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Rerun(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.rerunFn = func(gotID uuid.UUID) (uuid.UUID, error) {
			return uuid.Nil, fmt.Errorf("failed to get instance: %w", instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Rerun(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodPost, "/instances/bad-uuid/rerun", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.Rerun(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: GetTrace
// ---------------------------------------------------------------------------

func TestInstanceHandler_GetTrace(t *testing.T) {
	t.Run("returns ordered trace records", func(t *testing.T) {
		h, instRepo, nodeExecRepo, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateCompleted)
		instRepo.instances[inst.ID] = inst

		now := time.Now()
		nodeExecRepo.executions = []*instance.NodeExecution{
			{
				ID:         uuid.New(),
				InstanceID: inst.ID,
				NodeID:     "A",
				Attempt:    1,
				Status:     instance.NodeStatusSucceeded,
				StartedAt:  now,
			},
			{
				ID:         uuid.New(),
				InstanceID: inst.ID,
				NodeID:     "B",
				Attempt:    1,
				Status:     instance.NodeStatusSucceeded,
				StartedAt:  now.Add(time.Second),
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		w := httptest.NewRecorder()
		h.GetTrace(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 2)
	})

	t.Run("non-owner gets 404 identical to a missing instance (no existence leak)", func(t *testing.T) {
		// The scoped service returns ErrNotFound for a non-owner; the handler must
		// surface a 404 whose body is identical to a genuinely missing instance.
		id := uuid.New()
		notFoundSvc := &mockInstanceService{
			traceFn: func(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
				return nil, instance.ErrNotFound
			},
		}
		h := adapthttp.NewInstanceHandler(newMockInstanceRepo(), newMockNodeExecRepo(), newMockSnapshotRepo(), notFoundSvc)

		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/trace", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetTrace(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodGet, "/instances/bad-uuid/trace", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.GetTrace(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/trace", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetTrace(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: GetNodeTrace
// ---------------------------------------------------------------------------

func TestInstanceHandler_GetNodeTrace(t *testing.T) {
	t.Run("returns single node trace", func(t *testing.T) {
		h, instRepo, nodeExecRepo, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateCompleted)
		instRepo.instances[inst.ID] = inst

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

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace/A", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		req = withChiURLParam(req, "nodeId", "A")
		w := httptest.NewRecorder()
		h.GetNodeTrace(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "A", resp["nodeId"])
	})

	t.Run("returns 404 for unknown node", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateCompleted)
		instRepo.instances[inst.ID] = inst

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace/Z", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		req = withChiURLParam(req, "nodeId", "Z")
		w := httptest.NewRecorder()
		h.GetNodeTrace(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodGet, "/instances/bad-uuid/trace/A", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		req = withChiURLParam(req, "nodeId", "A")
		w := httptest.NewRecorder()
		h.GetNodeTrace(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: GetVariables
// ---------------------------------------------------------------------------

func TestInstanceHandler_GetVariables(t *testing.T) {
	t.Run("returns instance variables", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateRunning)
		inst.Variables = map[string]any{"foo": "bar", "count": float64(42)}
		instRepo.instances[inst.ID] = inst

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/variables", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		w := httptest.NewRecorder()
		h.GetVariables(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "bar", resp["foo"])
		assert.Equal(t, float64(42), resp["count"])
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/variables", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetVariables(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()
		req := httptest.NewRequest(http.MethodGet, "/instances/bad-uuid/variables", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.GetVariables(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: GetVariableHistory
// ---------------------------------------------------------------------------

func TestInstanceHandler_GetVariableHistory(t *testing.T) {
	t.Run("returns variable snapshots", func(t *testing.T) {
		h, instRepo, _, snapRepo, _ := newInstanceHandler()
		inst := testInstance(instance.StateCompleted)
		instRepo.instances[inst.ID] = inst

		now := time.Now()
		snapRepo.snapshots = []*instance.VariableSnapshot{
			{
				ID:         uuid.New(),
				InstanceID: inst.ID,
				NodeID:     "A",
				Variables:  map[string]any{"x": float64(1)},
				CapturedAt: now,
			},
			{
				ID:         uuid.New(),
				InstanceID: inst.ID,
				NodeID:     "B",
				Variables:  map[string]any{"x": float64(2)},
				CapturedAt: now.Add(time.Second),
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/variables/history", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		w := httptest.NewRecorder()
		h.GetVariableHistory(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var resp []map[string]any
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Len(t, resp, 2)
	})

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodGet, "/instances/bad-uuid/variables/history", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.GetVariableHistory(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns 404 for unknown instance", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/variables/history", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetVariableHistory(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("returns 500 when snapshots repo errors", func(t *testing.T) {
		instRepo := newMockInstanceRepo()
		inst := testInstance(instance.StateCompleted)
		instRepo.instances[inst.ID] = inst
		nodeRepo := newMockNodeExecRepo()
		snapRepo := &errSnapshotRepo{err: fmt.Errorf("db down")}
		h := adapthttp.NewInstanceHandler(instRepo, nodeRepo, snapRepo, serviceOverRepos(instRepo, nodeRepo, snapRepo))

		req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/variables/history", nil)
		req = withChiURLParam(req, "id", inst.ID.String())
		w := httptest.NewRecorder()
		h.GetVariableHistory(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Tests: internal/error paths for all handlers
// ---------------------------------------------------------------------------

func TestInstanceHandler_List_InternalError(t *testing.T) {
	svc := &mockInstanceService{listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
		return nil, fmt.Errorf("db down")
	}}
	h := adapthttp.NewInstanceHandler(&errInstanceRepo{err: fmt.Errorf("db down")}, newMockNodeExecRepo(), newMockSnapshotRepo(), svc)

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "INTERNAL_ERROR", resp.Code)
}

func TestInstanceHandler_Get_InternalError(t *testing.T) {
	instRepo := &errInstanceRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), serviceOverRepos(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo()))

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String(), nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_Pause_InternalError(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.pauseFn = func(_ uuid.UUID) error { return fmt.Errorf("unexpected db error") }

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Pause(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_Resume_InternalError(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.resumeFn = func(_ uuid.UUID) error { return fmt.Errorf("unexpected db error") }

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Resume(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_Cancel_InternalError(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.cancelFn = func(_ uuid.UUID) error { return fmt.Errorf("unexpected db error") }

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Cancel(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_Retry_InternalError(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.retryFn = func(_ uuid.UUID, _ map[string]any) error { return fmt.Errorf("unexpected db error") }

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Retry(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_Rerun_InternalError(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.rerunFn = func(_ uuid.UUID) (uuid.UUID, error) {
		return uuid.Nil, fmt.Errorf("unexpected db error")
	}

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Rerun(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_GetTrace_NodeExecsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	inst := testInstance(instance.StateCompleted)
	instRepo.instances[inst.ID] = inst
	nodeRepo := &errNodeExecRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, nodeRepo, newMockSnapshotRepo(), serviceOverRepos(instRepo, nodeRepo, newMockSnapshotRepo()))

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace", nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.GetTrace(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_GetNodeTrace_InternalError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst
	nodeRepo := &errNodeExecRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, nodeRepo, newMockSnapshotRepo(), serviceOverRepos(instRepo, nodeRepo, newMockSnapshotRepo()))

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String()+"/trace/A", nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	req = withChiURLParam(req, "nodeId", "A")
	w := httptest.NewRecorder()
	h.GetNodeTrace(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_GetNodeTrace_InstanceNotFound(t *testing.T) {
	instRepo := newMockInstanceRepo()
	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), serviceOverRepos(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo()))

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/trace/A", nil)
	req = withChiURLParam(req, "id", id.String())
	req = withChiURLParam(req, "nodeId", "A")
	w := httptest.NewRecorder()
	h.GetNodeTrace(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestInstanceHandler_GetNodeTrace_InstanceRepoError(t *testing.T) {
	instRepo := &errInstanceRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), serviceOverRepos(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo()))

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/trace/A", nil)
	req = withChiURLParam(req, "id", id.String())
	req = withChiURLParam(req, "nodeId", "A")
	w := httptest.NewRecorder()
	h.GetNodeTrace(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_GetVariables_InternalError(t *testing.T) {
	instRepo := &errInstanceRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), serviceOverRepos(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo()))

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/variables", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.GetVariables(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestInstanceHandler_GetVariableHistory_InstanceError(t *testing.T) {
	instRepo := &errInstanceRepo{err: fmt.Errorf("db down")}
	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), serviceOverRepos(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo()))

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/variables/history", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.GetVariableHistory(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: edge cases
// ---------------------------------------------------------------------------

func TestInstanceHandler_List_LimitAndOffset(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	for i := 0; i < 5; i++ {
		inst := testInstance(instance.StateRunning)
		instRepo.instances[inst.ID] = inst
	}

	req := httptest.NewRequest(http.MethodGet, "/instances?limit=2&offset=1", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []instance.InstanceSummary
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Len(t, resp, 2)
}

func TestInstanceHandler_Get_IncludesCompletedAt(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateCompleted)
	completedAt := time.Now()
	inst.CompletedAt = &completedAt
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp["completedAt"])
}

func TestInstanceHandler_Get_IncludesQueueFields(t *testing.T) {
	// Verifies that queuedAt, waitingUntil, webhookUrl, and webhookReceived
	// are correctly mapped in the instance response.
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateWaiting)
	now := time.Now()
	future := now.Add(time.Hour)
	token := "test-webhook-token"
	receivedAt := now.Add(-time.Second)

	inst.QueuedAt = &now
	inst.WaitingUntil = &future
	inst.WebhookToken = &token
	inst.WebhookReceivedAt = &receivedAt
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotNil(t, resp["queuedAt"], "queuedAt should be present")
	assert.NotNil(t, resp["waitingUntil"], "waitingUntil should be present")
	require.NotNil(t, resp["webhookUrl"], "webhookUrl should be present when webhook token is set")
	assert.Contains(t, resp["webhookUrl"].(string), token, "webhookUrl should embed the token")
	assert.Equal(t, true, resp["webhookReceived"], "webhookReceived should be true when WebhookReceivedAt is set")
}

func TestInstanceHandler_Get_NoWebhookFields_WhenTokenAbsent(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Nil(t, resp["webhookUrl"], "webhookUrl should be absent when no webhook token")
	// webhookReceived omitempty — absent when false
	_, present := resp["webhookReceived"]
	assert.False(t, present, "webhookReceived should be omitted when false")
}

// ---------------------------------------------------------------------------
// Tests: SetPriority endpoint
// ---------------------------------------------------------------------------

func TestInstanceHandler_SetPriority_HappyPath(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	var gotID uuid.UUID
	var gotPriority string
	svc.setPriorityFn = func(id uuid.UUID, priority string) error {
		gotID = id
		gotPriority = priority
		return nil
	}

	instanceID := uuid.New()
	body := `{"priority":"high"}`
	req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", strings.NewReader(body))
	req = withChiURLParam(req, "id", instanceID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetPriority(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, instanceID, gotID)
	assert.Equal(t, "high", gotPriority)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "priority_updated", resp["status"])
}

func TestInstanceHandler_SetPriority_MissingBody_400(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()

	instanceID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", nil)
	req = withChiURLParam(req, "id", instanceID.String())
	w := httptest.NewRecorder()
	h.SetPriority(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestInstanceHandler_SetPriority_InvalidPriority_400(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()

	instanceID := uuid.New()
	body := `{"priority":"urgent"}`
	req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", strings.NewReader(body))
	req = withChiURLParam(req, "id", instanceID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetPriority(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Contains(t, resp.Error, "invalid priority")
}

func TestInstanceHandler_SetPriority_AllValidValues(t *testing.T) {
	for _, priority := range []string{"high", "normal", "low"} {
		t.Run(priority, func(t *testing.T) {
			h, _, _, _, svc := newInstanceHandler()
			var got string
			svc.setPriorityFn = func(_ uuid.UUID, p string) error {
				got = p
				return nil
			}

			instanceID := uuid.New()
			body := `{"priority":"` + priority + `"}`
			req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", strings.NewReader(body))
			req = withChiURLParam(req, "id", instanceID.String())
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.SetPriority(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, priority, got)
		})
	}
}

func TestInstanceHandler_SetPriority_ServiceError_Conflict(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	svc.setPriorityFn = func(_ uuid.UUID, _ string) error {
		return fmt.Errorf("instance is not queued: %w", instance.ErrNotQueued)
	}

	instanceID := uuid.New()
	body := `{"priority":"high"}`
	req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", strings.NewReader(body))
	req = withChiURLParam(req, "id", instanceID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetPriority(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
}

// ---------------------------------------------------------------------------
// Tests: List with since filter
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Tests: Instance detail interactive state fields
// ---------------------------------------------------------------------------

func TestInstanceHandler_Get_InteractiveWait_HasInteractiveFields(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()

	nodeID := "review-design"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "review-design",
				"type": "interactive_llm",
				"config": map[string]any{
					"systemPrompt": "You are a reviewer.",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "review-design", resp["interactiveNodeId"])
	assert.Equal(t, true, resp["interactiveActive"])
}

func TestInstanceHandler_Get_Running_NoInteractiveFields(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()

	inst := testInstance(instance.StateRunning)
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Nil(t, resp["interactiveNodeId"])
	assert.Equal(t, false, resp["interactiveActive"])
}

func TestInstanceHandler_Get_WaitingNonInteractive_NoInteractiveFields(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()

	nodeID := "timer-node"
	inst := testInstance(instance.StateWaiting)
	inst.CurrentNodeID = &nodeID
	inst.WorkflowSnapshot = map[string]any{
		"name": "test-wf",
		"nodes": []any{
			map[string]any{
				"id":   "timer-node",
				"type": "timer_delay",
				"config": map[string]any{
					"durationSeconds": "30",
				},
			},
		},
	}
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Nil(t, resp["interactiveNodeId"])
	assert.Equal(t, false, resp["interactiveActive"])
}

func TestInstanceHandler_List_SinceFilter(t *testing.T) {
	t.Run("returns only instances created after since timestamp", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		cutoff := time.Now().Add(-time.Hour)

		old := testInstance(instance.StateRunning)
		old.CreatedAt = cutoff.Add(-time.Minute) // before cutoff
		recent := testInstance(instance.StateRunning)
		recent.CreatedAt = cutoff.Add(time.Minute) // after cutoff

		instRepo.instances[old.ID] = old
		instRepo.instances[recent.ID] = recent

		req := httptest.NewRequest(http.MethodGet, "/instances?since="+cutoff.UTC().Format(time.RFC3339), nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []instance.InstanceSummary
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp, 1)
		assert.Equal(t, recent.ID, resp[0].ID)
	})

	t.Run("malformed since param is silently ignored and returns all instances", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		inst := testInstance(instance.StateRunning)
		instRepo.instances[inst.ID] = inst

		req := httptest.NewRequest(http.MethodGet, "/instances?since=not-a-timestamp", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []instance.InstanceSummary
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Len(t, resp, 1, "malformed since must not filter out instances")
	})

	t.Run("since exactly at instance creation time excludes that instance", func(t *testing.T) {
		h, instRepo, _, _, _ := newInstanceHandler()
		exact := time.Now().UTC().Truncate(time.Second)

		inst := testInstance(instance.StateRunning)
		inst.CreatedAt = exact
		instRepo.instances[inst.ID] = inst

		// since == createdAt: the instance was created AT the cutoff, not AFTER it.
		req := httptest.NewRequest(http.MethodGet, "/instances?since="+exact.Format(time.RFC3339), nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []instance.InstanceSummary
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp, "instance created AT since boundary should be excluded (strictly after)")
	})
}
