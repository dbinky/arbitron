package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TestRouter_InstanceList_EnricherProvenanceProbe drives the REAL composition
// path: a concrete *engine.Engine with the enricher wired, seeded with an
// instance whose submitted title is "ProbeTitle". The runName reaching the
// wire MUST be "ProbeTitle — probe-wf" — NOT the short-id fallback. If the
// enricher is unwired (or its instance-repo input is nil), the runName
// degrades to "probe-wf <short-id>" and this test FAILS, proving the wiring
// is load-bearing.
func TestRouter_InstanceList_EnricherProvenanceProbe(t *testing.T) {
	owner := uuid.New()
	nodeID := "n1"
	now := time.Now()
	wf := workflow.Workflow{
		Name:   "probe-wf",
		Inputs: []workflow.InputField{{Name: "title", Type: "string", Required: true}},
		Nodes:  []workflow.Node{{ID: nodeID, Label: "Step"}},
	}
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "probe-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:            instance.StateRunning,
		Priority:         instance.PriorityNormal,
		CurrentNodeID:    &nodeID,
		Variables:        map[string]any{"title": "ProbeTitle"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}

	// Real engine with the enricher wired — the composition-root path.
	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		&probeQueueManager{}, &mockQueueRepoForRouter{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil))

	// Inject an admin identity so the list is unconstrained (the instance is
	// visible regardless of owner). The auth middleware is nil here; we pass
	// the identity via context on the request directly.
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "ProbeTitle — probe-wf", resp[0]["runName"], "enricher must derive the run name from the submitted title, not the short-id fallback")
	assert.Equal(t, "Step", resp[0]["friendlyStep"], "enricher must resolve the current node label from the snapshot")
	assert.Equal(t, "alice", resp[0]["submitterName"], "enricher must resolve the owner display name")
}

// TestRouter_InstanceList_ModelRetryProvenanceProbe drives the REAL composition
// path and asserts that model, backend, and retryCount reach the wire when a
// node execution exists. If the enricher's node-exec aggregate is nil/empty,
// model is "" and retryCount is 0 — this test FAILS, proving the wiring is
// load-bearing.
func TestRouter_InstanceList_ModelRetryProvenanceProbe(t *testing.T) {
	owner := uuid.New()
	nodeID := "n1"
	now := time.Now()
	wf := workflow.Workflow{
		Name:   "model-wf",
		Inputs: []workflow.InputField{{Name: "title", Type: "string", Required: true}},
		Nodes:  []workflow.Node{{ID: nodeID, Label: "Step", Config: workflow.NodeConfig{Backend: "claude-code"}}},
	}
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "model-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:            instance.StateRunning,
		Priority:         instance.PriorityNormal,
		CurrentNodeID:    &nodeID,
		Variables:        map[string]any{"title": "ModelProbe"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo := newMockNodeExecRepo()
	model := "gpt-4o"
	nodeExecRepo.executions = append(nodeExecRepo.executions, &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: nodeID, Attempt: 3, LLMModel: &model, StartedAt: now,
	})
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}

	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		&probeQueueManager{}, &mockQueueRepoForRouter{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil))

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "gpt-4o", resp[0]["model"], "enricher must populate model from node execution aggregate")
	assert.Equal(t, "claude-code", resp[0]["backend"], "enricher must populate backend from node config")
	assert.Equal(t, float64(3), resp[0]["retryCount"], "enricher must populate retryCount from node execution aggregate")
}

// TestRouter_InstanceList_ModelRetryAbsentForQueuedInstance confirms that a
// queued instance with no node executions yields empty model and 0 retryCount
// on the wire — the negative control for the model/retry probe above.
func TestRouter_InstanceList_ModelRetryAbsentForQueuedInstance(t *testing.T) {
	owner := uuid.New()
	now := time.Now()
	wf := workflow.Workflow{Name: "noexec-wf", Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "noexec-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}

	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		&probeQueueManager{}, &mockQueueRepoForRouter{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil))

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	model, hasModel := resp[0]["model"]
	if hasModel {
		assert.Equal(t, "", model, "queued instance with no executions -> model must be empty")
	}
	retry, hasRetry := resp[0]["retryCount"]
	if hasRetry {
		assert.Equal(t, float64(0), retry, "queued instance with no executions -> retryCount must be 0")
	}
}

// TestRouter_InstanceList_EnricherAbsent_DegradesToShortID confirms that
// WITHOUT the enricher wired, the runName is the short-id fallback (proving
// the probe above is load-bearing — it fails when the wiring is inert).
func TestRouter_InstanceList_EnricherAbsent_DegradesToShortID(t *testing.T) {
	owner := uuid.New()
	wf := workflow.Workflow{
		Name:   "probe-wf",
		Inputs: []workflow.InputField{{Name: "title", Type: "string"}},
		Nodes:  []workflow.Node{{ID: "n1", Label: "Step"}},
	}
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "probe-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:            instance.StateRunning,
		Variables:        map[string]any{"title": "ProbeTitle"},
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	instRepo := newMockInstanceRepo()
	instRepo.instances[inst.ID] = inst

	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(),
		&probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}},
		map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		&probeQueueManager{}, &mockQueueRepoForRouter{},
	)
	// No SetInstanceEnricher — the enricher is unwired (the degraded path).

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: newMockNodeExecRepo(),
		Snapshots: newMockSnapshotRepo(),
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.NotEqual(t, "ProbeTitle — probe-wf", resp[0]["runName"], "unwired enricher must NOT derive the title-based name")
	assert.Contains(t, resp[0]["runName"], "probe-wf", "degraded run name still includes the workflow name")
}

// --- helpers ---

func snapshotMapForRouter(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// probeUserRepo is a minimal UserRepository for the router probe (only GetByID
// is exercised by the enricher's batch lookup).
type probeUserRepo struct {
	users map[uuid.UUID]instance.User
}

func (r *probeUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return &u, nil
}
func (r *probeUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *probeUserRepo) GetBySubject(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *probeUserRepo) GetByEmail(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *probeUserRepo) Create(context.Context, *instance.User) error            { return nil }
func (r *probeUserRepo) UpdateUsername(context.Context, uuid.UUID, string) error { return nil }
func (r *probeUserRepo) List(context.Context) ([]instance.User, error)           { return nil, nil }
func (r *probeUserRepo) UpdateRole(context.Context, uuid.UUID, string) error     { return nil }
func (r *probeUserRepo) CountAdmins(context.Context) (int, error)                { return 0, nil }
func (r *probeUserRepo) ListDepartments(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (r *probeUserRepo) ReplaceDepartments(context.Context, uuid.UUID, []string) error { return nil }

var _ instance.UserRepository = (*probeUserRepo)(nil)

// probeQueueManager satisfies engine.QueueManager (Enqueue + NotifyCapacityFreed)
// for the router probe. It is distinct from mockQueueManagerForRouter, which
// satisfies the HTTP-layer QueueManagerPort (UpdateConfig + Evaluate).
type probeQueueManager struct{}

func (probeQueueManager) Enqueue(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (probeQueueManager) NotifyCapacityFreed(context.Context) {}

var _ engine.QueueManager = (*probeQueueManager)(nil)
