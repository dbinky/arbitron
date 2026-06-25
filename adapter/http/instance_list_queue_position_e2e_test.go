package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_InstanceList_QueuePositionProvenanceProbe drives the REAL
// composition path: a concrete *engine.Engine + *queue.Manager with the
// enricher wired AND the manager set as the QueuePositionProvider. A queued
// instance MUST receive a positive queuePosition on the wire. If the provider
// is unwired, position stays 0 and this test FAILS — proving the wiring is
// load-bearing.
func TestRouter_InstanceList_QueuePositionProvenanceProbe(t *testing.T) {
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	wf := workflow.Workflow{Name: "pos-wf", Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	queuedInst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "pos-wf",
		WorkflowSnapshot: snapshotMapForRouter(t, wf),
		State:            instance.StateQueued,
		Priority:         instance.PriorityHigh,
		QueuedAt:         &now,
		Variables:        map[string]any{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	instRepo := newMockInstanceRepo()
	instRepo.instances[queuedInst.ID] = queuedInst
	nodeExecRepo := newMockNodeExecRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}
	queueRepo := &probeQueueRepo{queued: []instance.Instance{*queuedInst}}

	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, nodeExecRepo, snapRepo,
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		nil, queueRepo,
	)
	enricher := engine.NewInstanceEnricher(instRepo, nodeExecRepo, userRepo, nil, "")
	eng.SetInstanceEnricher(enricher)

	// Construct the queue manager and wire it as the position provider — the
	// composition-root path. The manager needs a starter; a nil starter is fine
	// because QueuePositions never starts work (read-only).
	mgr := queue.NewManager(queueRepo, instRepo, &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}}, nil)
	enricher.SetQueuePositionProvider(mgr)

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: nodeExecRepo,
		Snapshots: snapRepo,
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	pos, ok := resp[0]["queuePosition"].(float64)
	require.True(t, ok, "queuePosition must be present on the wire")
	assert.Equal(t, float64(1), pos, "single queued item -> position 1")
}

// TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired confirms
// that WITHOUT the position provider wired, queuePosition is 0 (absent) —
// proving the probe above is load-bearing.
func TestRouter_InstanceList_QueuePositionAbsentWhenProviderUnwired(t *testing.T) {
	owner := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	wf := workflow.Workflow{Name: "pos-wf", Nodes: []workflow.Node{{ID: "n1", Label: "Step"}}}
	queuedInst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "pos-wf", WorkflowSnapshot: snapshotMapForRouter(t, wf), State: instance.StateQueued, Priority: instance.PriorityHigh, QueuedAt: &now, Variables: map[string]any{}, CreatedAt: now, UpdatedAt: now}

	instRepo := newMockInstanceRepo()
	instRepo.instances[queuedInst.ID] = queuedInst

	eng := engine.NewEngine(
		newMockWorkflowRepo(), instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(),
		&probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}},
		map[workflow.NodeType]engine.NodeExecutor{}, eventbus.NewNoOp(),
		nil, &probeQueueRepo{queued: []instance.Instance{*queuedInst}},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(instRepo, newMockNodeExecRepo(), &probeUserRepo{users: map[uuid.UUID]instance.User{owner: {ID: owner, Username: "alice"}}}, nil, ""))
	// No SetQueuePositionProvider — position stays 0.

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:   eng,
		Instances: instRepo,
		NodeExecs: newMockNodeExecRepo(),
		Snapshots: newMockSnapshotRepo(),
		Config:    &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 0}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleAdmin}))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp, 1)
	// omitempty elides a zero position, so absence IS the unwired signal.
	// A wired provider would serialize a positive integer here. Asserting
	// the field is absent (or zero) proves the probe above is load-bearing:
	// without the provider, the position never reaches the wire.
	pos, exists := resp[0]["queuePosition"]
	if exists {
		assert.Equal(t, float64(0), pos.(float64), "unwired provider -> position 0")
	} else {
		t.Log("queuePosition absent (omitempty on zero) — unwired provider confirmed")
	}
	// Belt-and-suspenders: the unwired path must NOT report a positive position.
	if p, ok := pos.(float64); ok {
		assert.LessOrEqual(t, p, float64(0), "unwired provider must not report a positive position")
	}
}

// probeQueueRepo is a minimal QueueRepository for the position probe. It
// satisfies the interface including ListQueued (Phase 2 Task 1).
type probeQueueRepo struct {
	queued []instance.Instance
}

func (r *probeQueueRepo) CountRunning(context.Context) (int, error) { return 0, nil }
func (r *probeQueueRepo) CountRunningByOwner(context.Context, uuid.UUID) (int, error) {
	return 0, nil
}
func (r *probeQueueRepo) CountQueued(context.Context) (int, error)  { return len(r.queued), nil }
func (r *probeQueueRepo) CountWaiting(context.Context) (int, error) { return 0, nil }
func (r *probeQueueRepo) NextEligible(context.Context, int, int) (*instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) SetQueued(context.Context, uuid.UUID, time.Time) error { return nil }
func (r *probeQueueRepo) ListWaitingWithTimer(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) ListWaitingWithWebhook(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *probeQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	return r.queued, nil
}

var _ instance.QueueRepository = (*probeQueueRepo)(nil)
