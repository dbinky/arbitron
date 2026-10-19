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
	"github.com/arbitron/arbitron/domain/identity"
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
func (r *errInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return r.err
}
func (r *errInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return r.err
}
func (r *errInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return r.err
}
func (r *errInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error { return r.err }
func (r *errInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error        { return r.err }
func (r *errInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, r.err
}
func (r *errInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return r.err
}
func (r *errInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return r.err
}
func (r *errInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error {
	return r.err
}
func (r *errInstanceRepo) SetWebhookReceived(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return r.err
}
func (r *errInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return nil, r.err
}

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
func (r *errNodeExecRepo) AggregateByInstances(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
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
			ID:               inst.ID,
			OwnerID:          inst.OwnerID,
			WorkflowName:     inst.WorkflowName,
			State:            inst.State,
			Priority:         inst.Priority,
			Department:       inst.Department,
			Visibility:       inst.Visibility,
			CurrentNodeID:    inst.CurrentNodeID,
			FailedNodeID:     inst.FailedNodeID,
			Variables:        inst.Variables,
			WorkflowSnapshot: inst.WorkflowSnapshot,
			TriggerSource:    inst.TriggerSource,
			ScheduleID:       inst.ScheduleID,
			CreatedAt:        inst.CreatedAt,
			UpdatedAt:        inst.UpdatedAt,
			CompletedAt:      inst.CompletedAt,
			QueuedAt:         inst.QueuedAt,
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

func (r *mockNodeExecRepo) AggregateByInstances(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	out := make(map[uuid.UUID]instance.NodeExecutionAggregate, len(ids))
	for _, id := range ids {
		for _, e := range r.executions {
			if e.InstanceID == id {
				agg, ok := out[id]
				if !ok {
					agg = instance.NodeExecutionAggregate{InstanceID: id}
				}
				if e.Attempt > agg.MaxAttempt {
					agg.MaxAttempt = e.Attempt
				}
				out[id] = agg
			}
		}
	}
	return out, nil
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
	listFn          func(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error)
	listEnrichedFn  func(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error)
	getFn           func(ctx context.Context, id uuid.UUID) (*instance.Instance, error)
	traceFn         func(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error)
	nodeTraceFn     func(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error)
	variablesFn     func(ctx context.Context, id uuid.UUID) (map[string]any, error)
	variableHistFn  func(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error)
	interventionsFn func(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error)
	pauseFn         func(id uuid.UUID) error
	resumeFn        func(id uuid.UUID) error
	cancelFn        func(id uuid.UUID) error
	retryFn         func(id uuid.UUID, variableEdits map[string]any) error
	rerunFn         func(id uuid.UUID) (uuid.UUID, error)
	setPriorityFn   func(id uuid.UUID, priority string) error
}

func (m *mockInstanceService) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if m.listFn != nil {
		return m.listFn(ctx, filters)
	}
	return nil, nil
}

func (m *mockInstanceService) ListEnriched(ctx context.Context, filters instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
	if m.listEnrichedFn != nil {
		return m.listEnrichedFn(ctx, filters)
	}
	summaries, err := m.ListInstances(ctx, filters)
	if err != nil {
		return nil, err
	}
	out := make([]instance.EnrichedInstanceSummary, len(summaries))
	for i, s := range summaries {
		out[i] = instance.EnrichedInstanceSummary{InstanceSummary: s}
	}
	return out, nil
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

func (m *mockInstanceService) GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error) {
	if m.interventionsFn != nil {
		return m.interventionsFn(ctx, id)
	}
	return nil, nil
}

func (m *mockInstanceService) Pause(_ context.Context, id uuid.UUID) error {
	if m.pauseFn != nil {
		return m.pauseFn(id)
	}
	return nil
}

func (m *mockInstanceService) Resume(_ context.Context, id uuid.UUID) error {
	if m.resumeFn != nil {
		return m.resumeFn(id)
	}
	return nil
}

func (m *mockInstanceService) Cancel(_ context.Context, id uuid.UUID) error {
	if m.cancelFn != nil {
		return m.cancelFn(id)
	}
	return nil
}

func (m *mockInstanceService) Retry(_ context.Context, id uuid.UUID, variableEdits map[string]any) error {
	if m.retryFn != nil {
		return m.retryFn(id, variableEdits)
	}
	return nil
}

func (m *mockInstanceService) Rerun(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if m.rerunFn != nil {
		return m.rerunFn(id)
	}
	return uuid.New(), nil
}

func (m *mockInstanceService) SetPriority(_ context.Context, id uuid.UUID, priority string) error {
	if m.setPriorityFn != nil {
		return m.setPriorityFn(id, priority)
	}
	return nil
}

// TestInstanceHandler_GetInterventions covers the audit-trail endpoint: a viewer
// (owner/admin) gets 200 with the rows; a non-viewer gets 404 (the service
// returns ErrNotFound), identical to a missing instance.
func TestInstanceHandler_GetInterventions(t *testing.T) {
	t.Run("viewer gets 200 with rows", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		detail := "high"
		svc.interventionsFn = func(_ context.Context, gotID uuid.UUID) ([]instance.Intervention, error) {
			assert.Equal(t, id, gotID)
			return []instance.Intervention{
				{ID: uuid.New(), InstanceID: id, ActorUserID: uuid.New(), Action: instance.ActionSetPriority, Detail: &detail, CreatedAt: time.Now()},
			}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/interventions", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetInterventions(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		require.Len(t, resp, 1)
		assert.Equal(t, "set_priority", resp[0]["action"])
		assert.Equal(t, "high", resp[0]["detail"])
	})

	t.Run("non-viewer gets 404", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.interventionsFn = func(_ context.Context, _ uuid.UUID) ([]instance.Intervention, error) {
			return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String()+"/interventions", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.GetInterventions(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

func testInstance(state instance.State) *instance.Instance {
	now := time.Now()
	return &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           uuid.New(),
		WorkflowName:      "test-wf",
		State:             state,
		Priority:          instance.PriorityNormal,
		Variables:         map[string]any{"key": "value"},
		OriginalVariables: map[string]any{"key": "value"},
		CreatedAt:         now,
		UpdatedAt:         now,
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

	t.Run("no-identity defence-in-depth: ErrNotFound from service returns 200 empty array", func(t *testing.T) {
		// When auth middleware is somehow bypassed the scoped engine returns
		// ErrNotFound (no identity = reveal nothing). The List handler must map
		// this to HTTP 200 with an empty array — not 404 or 500 — so a probe
		// cannot distinguish "no instances" from "access denied".
		svc := &mockInstanceService{
			listFn: func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
				return nil, instance.ErrNotFound
			},
		}
		h := adapthttp.NewInstanceHandler(newMockInstanceRepo(), newMockNodeExecRepo(), newMockSnapshotRepo(), svc)

		req := httptest.NewRequest(http.MethodGet, "/instances", nil)
		w := httptest.NewRecorder()
		h.List(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp []instance.InstanceSummary
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Empty(t, resp, "unauthenticated caller must see an empty list, not an error")
	})
}

// Manual run: triggerSource="manual" is always present; scheduleId is ABSENT
// (omitempty) when ScheduleID is nil. Tests that the omitempty tag works and
// that the zero-value TriggerSource is never silently dropped.
func TestInstanceHandler_List_ManualOrigin_ScheduleIDAbsent(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	svc.listFn = func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
		now := time.Now()
		return []instance.InstanceSummary{{
			ID:            uuid.New(),
			WorkflowName:  "wf",
			State:         instance.StateQueued,
			Priority:      instance.PriorityNormal,
			TriggerSource: instance.OriginManual,
			ScheduleID:    nil,
			CreatedAt:     now,
			UpdatedAt:     now,
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "manual", resp[0]["triggerSource"])
	_, hasScheduleID := resp[0]["scheduleId"]
	assert.False(t, hasScheduleID, "scheduleId must be absent (omitempty) for manual runs")
}

// Origin fields: triggerSource + scheduleId appear in the List JSON response.
// The summaryDTO maps OriginRunNow -> "run_now" and ScheduleID -> scheduleId.
func TestInstanceHandler_List_OriginFields(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	sid := uuid.New()
	svc.listFn = func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
		now := time.Now()
		return []instance.InstanceSummary{{
			ID:            uuid.New(),
			WorkflowName:  "wf",
			State:         instance.StateQueued,
			Priority:      instance.PriorityNormal,
			TriggerSource: instance.OriginRunNow,
			ScheduleID:    &sid,
			CreatedAt:     now,
			UpdatedAt:     now,
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "run_now", resp[0]["triggerSource"], "triggerSource must be in JSON")
	assert.Equal(t, sid.String(), resp[0]["scheduleId"], "scheduleId must be in JSON")
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

	t.Run("non-owner gets 404 identical to a missing instance (no existence leak)", func(t *testing.T) {
		// The scoped engine returns ErrNotFound for a non-owner; the handler must
		// surface a 404 whose body is identical to a genuinely missing instance so
		// a probe cannot confirm the instance exists just because someone else owns it.
		id := uuid.New()
		notFoundSvc := &mockInstanceService{
			getFn: func(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
				return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
			},
		}
		h := adapthttp.NewInstanceHandler(newMockInstanceRepo(), newMockNodeExecRepo(), newMockSnapshotRepo(), notFoundSvc)

		req := httptest.NewRequest(http.MethodGet, "/instances/"+id.String(), nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Get(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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

	t.Run("non-owner gets 404 (intervention gate enforced in the service)", func(t *testing.T) {
		// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
		// the handler maps it to 404, identical to a missing instance.
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.pauseFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Pause(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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

	t.Run("non-owner gets 404 (intervention gate enforced in the service)", func(t *testing.T) {
		// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
		// the handler maps it to 404, identical to a missing instance.
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.resumeFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/resume", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Resume(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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

	cancelBody := func(t *testing.T, state instance.State, getErr error) map[string]string {
		t.Helper()
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.cancelFn = func(uuid.UUID) error { return nil }
		svc.getFn = func(context.Context, uuid.UUID) (*instance.Instance, error) {
			if getErr != nil {
				return nil, getErr
			}
			return &instance.Instance{ID: id, State: state}, nil
		}
		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]string
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body
	}

	t.Run("body reports an actually cancelled instance as cancelled", func(t *testing.T) {
		body := cancelBody(t, instance.StateCancelled, nil)
		assert.Equal(t, "cancelled", body["status"])
		assert.Equal(t, "cancelled", body["state"])
	})

	t.Run("body does not claim a completed parent was cancelled", func(t *testing.T) {
		body := cancelBody(t, instance.StateCompleted, nil)
		assert.Equal(t, "cancel_requested", body["status"])
		assert.Equal(t, "completed", body["state"])
	})

	t.Run("body stays honest when the state cannot be read back", func(t *testing.T) {
		body := cancelBody(t, "", instance.ErrNotFound)
		assert.Equal(t, "cancel_requested", body["status"])
		assert.NotContains(t, body, "state")
	})

	t.Run("non-owner gets 404 (intervention gate enforced in the service)", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.cancelFn = func(gotID uuid.UUID) error {
			return fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Cancel(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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
	t.Run("returns 409 when a terminal group task cannot be retried", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, _ map[string]any) error {
			assert.Equal(t, id, gotID)
			return fmt.Errorf("group task %s has an immutable terminal outcome: %w", gotID, instance.ErrCannotRetry)
		}
		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)
		assert.Equal(t, http.StatusConflict, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "CONFLICT", resp.Code)
	})

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

	t.Run("non-owner gets 404 (intervention gate enforced in the service)", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.retryFn = func(gotID uuid.UUID, _ map[string]any) error {
			return fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/retry", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Retry(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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

	t.Run("non-owner gets 404 (intervention gate enforced in the service)", func(t *testing.T) {
		h, _, _, _, svc := newInstanceHandler()
		id := uuid.New()
		svc.rerunFn = func(gotID uuid.UUID) (uuid.UUID, error) {
			return uuid.Nil, fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
		}

		req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/rerun", nil)
		req = withChiURLParam(req, "id", id.String())
		w := httptest.NewRecorder()
		h.Rerun(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var resp adapthttp.ErrorResponse
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "NOT_FOUND", resp.Code)
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

func TestInstanceHandler_SetPriority_NonOwner_404(t *testing.T) {
	// The engine's authorizeIntervention returns ErrNotFound for a non-owner;
	// the handler maps it to 404, identical to a missing instance.
	h, _, _, _, svc := newInstanceHandler()
	svc.setPriorityFn = func(gotID uuid.UUID, _ string) error {
		return fmt.Errorf("instance %s: %w", gotID, instance.ErrNotFound)
	}

	instanceID := uuid.New()
	body := `{"priority":"high"}`
	req := httptest.NewRequest(http.MethodPost, "/instances/"+instanceID.String()+"/priority", strings.NewReader(body))
	req = withChiURLParam(req, "id", instanceID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.SetPriority(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "NOT_FOUND", resp.Code)
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

// ---------------------------------------------------------------------------
// FA #19: Instance adapter surface — department+visibility in responses,
//         ErrForbidden→403 propagation
// ---------------------------------------------------------------------------

// Success: GET /instances/:id response body includes department and visibility
// when the instance carries those fields. This verifies that the JSON projection
// (instanceResponse struct) surfaces both columns added in migration 012.
func TestInstanceHandler_Get_DepartmentAndVisibilityInResponse(t *testing.T) {
	h, instRepo, _, _, _ := newInstanceHandler()
	inst := testInstance(instance.StateRunning)
	inst.Department = "engineering"
	inst.Visibility = instance.VisibilityDepartment
	instRepo.instances[inst.ID] = inst

	req := httptest.NewRequest(http.MethodGet, "/instances/"+inst.ID.String(), nil)
	req = withChiURLParam(req, "id", inst.ID.String())
	w := httptest.NewRecorder()
	h.Get(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "engineering", resp["department"],
		"department must appear in the GET response JSON")
	assert.Equal(t, "department", resp["visibility"],
		"visibility must appear in the GET response JSON")
}

// Success: ErrForbidden from the engine (department peer attempting an
// intervention) must map to HTTP 403 — not 404 or 500. This preserves the
// 404-vs-403 split defined in canView/canIntervene.
func TestInstanceHandler_Pause_ErrForbidden_Returns403(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.pauseFn = func(_ uuid.UUID) error {
		return fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	}

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/pause", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Pause(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code,
		"ErrForbidden from the engine must produce HTTP 403, not 404 or 500")
	var resp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "FORBIDDEN", resp.Code)
}

// FA #19 Pass 2: robustness

// List response includes department and visibility in each InstanceSummary.
// The mock repo.List doesn't copy those fields, so we inject them via listFn.
func TestInstanceHandler_List_DepartmentAndVisibilityInSummary(t *testing.T) {
	_, _, _, _, svc := newInstanceHandler()
	svc.listFn = func(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
		return []instance.InstanceSummary{
			{
				ID:           uuid.New(),
				WorkflowName: "wf",
				State:        instance.StateRunning,
				Priority:     instance.PriorityNormal,
				Department:   "sales",
				Visibility:   instance.VisibilityPublic,
			},
		}, nil
	}
	h := adapthttp.NewInstanceHandler(newMockInstanceRepo(), newMockNodeExecRepo(), newMockSnapshotRepo(), svc)

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "sales", resp[0]["department"],
		"department must appear in List summary JSON")
	assert.Equal(t, "public", resp[0]["visibility"],
		"visibility must appear in List summary JSON")
}

// Cancel with ErrForbidden also returns 403 (same mapEngineError path as Pause).
// This spot-checks that all intervention endpoints share the 403 mapping rather
// than special-casing Pause.
func TestInstanceHandler_Cancel_ErrForbidden_Returns403(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	id := uuid.New()
	svc.cancelFn = func(_ uuid.UUID) error {
		return fmt.Errorf("instance %s: %w", id, instance.ErrForbidden)
	}

	req := httptest.NewRequest(http.MethodPost, "/instances/"+id.String()+"/cancel", nil)
	req = withChiURLParam(req, "id", id.String())
	w := httptest.NewRecorder()
	h.Cancel(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestInstanceHandler_List_EnrichedFields(t *testing.T) {
	h, instRepo, _, _, svc := newInstanceHandler()
	owner := uuid.New()
	nodeID := "draft-node"
	now := time.Now()
	inst := &instance.Instance{
		ID:            uuid.New(),
		OwnerID:       owner,
		WorkflowName:  "feature-pipeline",
		State:         instance.StateRunning,
		Priority:      instance.PriorityNormal,
		CurrentNodeID: &nodeID,
		Variables:     map[string]any{"title": "Refactor"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	inst.WorkflowSnapshot = map[string]any{
		"name":  "feature-pipeline",
		"nodes": []any{map[string]any{"id": nodeID, "label": "Generate draft", "config": map[string]any{"backend": "claude-code"}}},
	}
	instRepo.instances[inst.ID] = inst

	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: inst.ID, WorkflowName: "feature-pipeline", State: instance.StateRunning, Priority: instance.PriorityNormal, CurrentNodeID: &nodeID, CreatedAt: now, UpdatedAt: now},
			RunName:         "Refactor — feature-pipeline",
			Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
			FriendlyStep:    "Generate draft",
			Model:           "gpt-4o",
			Backend:         "claude-code",
			RetryCount:      2,
			QueuePosition:   0,
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	row := resp[0]
	assert.Equal(t, "Refactor — feature-pipeline", row["runName"])
	assert.Equal(t, "Generate draft", row["friendlyStep"])
	assert.Equal(t, "gpt-4o", row["model"])
	assert.Equal(t, "claude-code", row["backend"])
	assert.Equal(t, float64(2), row["retryCount"])
	assert.Equal(t, "alice", row["submitterName"])
	// Security: with no identity in context, the viewer is not the owner and not
	// an admin, so the account identity (email) must be withheld.
	_, hasIdentity := row["submitterIdentity"]
	assert.False(t, hasIdentity, "non-owner/non-admin must NOT see submitterIdentity")
	assert.Equal(t, false, row["dismissed"], "dismissed decoration preserved")
}

func TestInstanceHandler_List_SubmitterIdentity_RevealedToOwner(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	owner := uuid.New()
	now := time.Now()
	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now},
			RunName:         "run — wf",
			Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser}))
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "alice@example.com", resp[0]["submitterIdentity"], "owner sees the account identity")
}

func TestInstanceHandler_List_SubmitterIdentity_RevealedToAdmin(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	owner := uuid.New()
	now := time.Now()
	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now},
			RunName:         "run — wf",
			Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: uuid.New(), Username: "admin", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "alice@example.com", resp[0]["submitterIdentity"], "admin sees the account identity")
}

func TestInstanceHandler_List_SubmitterIdentity_WithheldFromPeer(t *testing.T) {
	h, _, _, _, svc := newInstanceHandler()
	owner := uuid.New()
	now := time.Now()
	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return []instance.EnrichedInstanceSummary{{
			InstanceSummary: instance.InstanceSummary{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", State: instance.StateRunning, Priority: instance.PriorityNormal, CreatedAt: now, UpdatedAt: now},
			RunName:         "run — wf",
			Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
		}}, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	// A non-owning, non-admin peer (e.g. department/public viewer).
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: uuid.New(), Username: "bob", Role: identity.RoleUser}))
	w := httptest.NewRecorder()
	h.List(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	_, has := resp[0]["submitterIdentity"]
	assert.False(t, has, "a non-owner peer must NOT see another user's account identity")
}

func TestInstanceHandler_List_EnrichedProvenanceProbe(t *testing.T) {
	// Provenance probe: when the service returns NO enriched rows (empty),
	// the handler still returns 200 + []. When the service is unwired
	// (listEnrichedFn nil), the handler falls back to the plain service list
	// so the endpoint never breaks.
	h, _, _, _, svc := newInstanceHandler()
	svc.listEnrichedFn = func(_ context.Context, _ instance.ListFilters) ([]instance.EnrichedInstanceSummary, error) {
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	w := httptest.NewRecorder()
	h.List(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp)
}
