package http_test

import (
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

type mockInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockInstanceRepo) Create(inst *instance.Instance) error {
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockInstanceRepo) Get(id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance not found: %s", id)
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

func (r *mockInstanceRepo) List(filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	var summaries []instance.InstanceSummary
	for _, inst := range r.instances {
		if filters.State != nil && inst.State != *filters.State {
			continue
		}
		if filters.WorkflowName != nil && inst.WorkflowName != *filters.WorkflowName {
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

func (r *mockInstanceRepo) UpdateState(id uuid.UUID, state instance.State) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = state
	return nil
}

func (r *mockInstanceRepo) UpdateVariables(id uuid.UUID, variables map[string]any) error {
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.Variables = variables
	return nil
}

func (r *mockInstanceRepo) UpdateCurrentNode(id uuid.UUID, nodeID string) error {
	return nil
}

func (r *mockInstanceRepo) SetFailed(id uuid.UUID, failedNodeID string) error {
	return nil
}

func (r *mockInstanceRepo) SetCompleted(id uuid.UUID) error {
	return nil
}

func (r *mockInstanceRepo) ListRunning() ([]instance.Instance, error) {
	return nil, nil
}

// mockNodeExecutionRepo for InstanceHandler
type mockNodeExecRepo struct {
	executions []*instance.NodeExecution
}

func newMockNodeExecRepo() *mockNodeExecRepo {
	return &mockNodeExecRepo{}
}

func (r *mockNodeExecRepo) Create(exec *instance.NodeExecution) error {
	r.executions = append(r.executions, exec)
	return nil
}

func (r *mockNodeExecRepo) Update(exec *instance.NodeExecution) error {
	return nil
}

func (r *mockNodeExecRepo) ListByInstance(instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	var result []instance.NodeExecution
	for _, e := range r.executions {
		if e.InstanceID == instanceID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (r *mockNodeExecRepo) GetByInstanceAndNode(instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	for _, e := range r.executions {
		if e.InstanceID == instanceID && e.NodeID == nodeID {
			return e, nil
		}
	}
	return nil, fmt.Errorf("node execution not found for instance %s node %s", instanceID, nodeID)
}

// mockSnapshotRepo for InstanceHandler
type mockSnapshotRepo struct {
	snapshots []*instance.VariableSnapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{}
}

func (r *mockSnapshotRepo) Create(snap *instance.VariableSnapshot) error {
	r.snapshots = append(r.snapshots, snap)
	return nil
}

func (r *mockSnapshotRepo) ListByInstance(instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
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
	pauseFn  func(id uuid.UUID) error
	resumeFn func(id uuid.UUID) error
	cancelFn func(id uuid.UUID) error
	retryFn  func(id uuid.UUID, variableEdits map[string]any) error
	rerunFn  func(id uuid.UUID) (uuid.UUID, error)
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
	h := adapthttp.NewInstanceHandler(instRepo, nodeExecRepo, snapRepo, svc)
	return h, instRepo, nodeExecRepo, snapRepo, svc
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
			return fmt.Errorf("instance %s is not running (state: completed)", gotID)
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
			return fmt.Errorf("failed to get instance: instance not found: %s", gotID)
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
			return fmt.Errorf("instance %s is not paused (state: running)", gotID)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Resume(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
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
			return fmt.Errorf("instance %s cannot be cancelled (state: completed)", gotID)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
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
			return fmt.Errorf("instance %s is not failed (state: running)", gotID)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)
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
			return uuid.Nil, fmt.Errorf("instance %s cannot be rerun (state: running)", gotID)
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
			return uuid.Nil, fmt.Errorf("failed to get instance: instance not found: %s", gotID)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Rerun(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
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

	t.Run("returns 400 for bad UUID", func(t *testing.T) {
		h, _, _, _, _ := newInstanceHandler()

		req := httptest.NewRequest(http.MethodGet, "/instances/bad-uuid/trace", nil)
		req = withChiURLParam(req, "id", "bad-uuid")
		w := httptest.NewRecorder()
		h.GetTrace(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
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
}
