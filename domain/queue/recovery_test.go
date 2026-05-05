package queue_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tests: RecoverTimers
// ---------------------------------------------------------------------------

func TestRecoverTimers_PastDue_TransitionsToQueued(t *testing.T) {
	id := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WaitingUntil: &pastDue},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, id)
	qr.mu.Unlock()
}

func TestRecoverTimers_FuturePending_RegistersWithScheduler(t *testing.T) {
	id := uuid.New()
	future := time.Now().Add(1 * time.Hour)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WaitingUntil: &future},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// The scheduler should have the timer registered.
	assert.Equal(t, 1, scheduler.Pending())
}

func TestRecoverTimers_MultipleMixed(t *testing.T) {
	pastID := uuid.New()
	futureID := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)
	future := time.Now().Add(1 * time.Hour)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: pastID, State: instance.StateWaiting, WaitingUntil: &pastDue},
		{ID: futureID, State: instance.StateWaiting, WaitingUntil: &future},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Past-due should be queued.
	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, pastID)
	assert.NotContains(t, qr.setQueuedCalls, futureID)
	qr.mu.Unlock()

	// Future should be scheduled.
	assert.Equal(t, 1, scheduler.Pending())
}

func TestRecoverTimers_NoWaitingInstances(t *testing.T) {
	qr := newMockQueueRepo()
	// Empty — no waiting instances.

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)
}

func TestRecoverTimers_TriggersEvaluation(t *testing.T) {
	pastID := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)

	// Set up a queued eligible instance that should be started during evaluation.
	eligibleID := uuid.New()
	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: pastID, State: instance.StateWaiting, WaitingUntil: &pastDue},
	}
	qr.eligible = []*instance.Instance{
		{ID: eligibleID, State: instance.StateQueued},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Evaluate should have tried to start the eligible instance.
	calls := starter.getCalls()
	assert.Contains(t, calls, eligibleID)
}

// ---------------------------------------------------------------------------
// Tests: RecoverWebhooks
// ---------------------------------------------------------------------------

// mockNodeExecRepoForRecovery implements instance.NodeExecutionRepository for recovery tests.
type mockNodeExecRepoForRecovery struct {
	executions map[string]*instance.NodeExecution // keyed by instanceID:nodeID
}

func newMockNodeExecRepoForRecovery() *mockNodeExecRepoForRecovery {
	return &mockNodeExecRepoForRecovery{executions: make(map[string]*instance.NodeExecution)}
}

func (r *mockNodeExecRepoForRecovery) Create(_ context.Context, exec *instance.NodeExecution) error {
	key := exec.InstanceID.String() + ":" + exec.NodeID
	r.executions[key] = exec
	return nil
}

func (r *mockNodeExecRepoForRecovery) Update(_ context.Context, exec *instance.NodeExecution) error {
	key := exec.InstanceID.String() + ":" + exec.NodeID
	r.executions[key] = exec
	return nil
}

func (r *mockNodeExecRepoForRecovery) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	var result []instance.NodeExecution
	for _, e := range r.executions {
		if e.InstanceID == instanceID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (r *mockNodeExecRepoForRecovery) GetByInstanceAndNode(_ context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	key := instanceID.String() + ":" + nodeID
	exec, ok := r.executions[key]
	if !ok {
		return nil, nil
	}
	return exec, nil
}

func makeWebhookSnapshot(nodeID string, timeoutSeconds string) map[string]any {
	wf := workflow.Workflow{
		Name: "test-wf",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: nodeID, Type: workflow.NodeTypeWebhookWait, Config: workflow.NodeConfig{WaitTimeoutSeconds: timeoutSeconds}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: nodeID},
			{Source: nodeID, Target: "next"},
		},
	}
	wfBytes, _ := json.Marshal(wf)
	var snapshot map[string]any
	_ = json.Unmarshal(wfBytes, &snapshot)
	return snapshot
}

func TestRecoverWebhooks_ReRegistersToken(t *testing.T) {
	id := uuid.New()
	token := "recover-token-1"
	nodeID := "webhook-node"

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:               id,
			State:            instance.StateWaiting,
			WebhookToken:     &token,
			CurrentNodeID:    &nodeID,
			WorkflowSnapshot: makeWebhookSnapshot(nodeID, ""),
		},
	}

	instRepo := newMockInstanceRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}
	nodeExecRepo := newMockNodeExecRepoForRecovery()

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler, nodeExecRepo)
	require.NoError(t, err)

	// Token should be re-registered.
	registry.mu.Lock()
	assert.Contains(t, registry.registerCalls, token)
	registry.mu.Unlock()
}

func TestRecoverWebhooks_TimedOut_FailsInstance(t *testing.T) {
	id := uuid.New()
	token := "recover-token-timeout"
	nodeID := "webhook-node"

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:               id,
			State:            instance.StateWaiting,
			WebhookToken:     &token,
			CurrentNodeID:    &nodeID,
			WorkflowSnapshot: makeWebhookSnapshot(nodeID, "60"), // 60 second timeout
		},
	}

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StateWaiting,
		WebhookToken: &token,
	}
	instRepo.mu.Unlock()

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}
	nodeExecRepo := newMockNodeExecRepoForRecovery()

	// Node execution started 2 minutes ago (timeout of 60s => expired).
	startedAt := time.Now().Add(-2 * time.Minute)
	nodeExecRepo.executions[id.String()+":"+nodeID] = &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: id,
		NodeID:     nodeID,
		StartedAt:  startedAt,
	}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler, nodeExecRepo)
	require.NoError(t, err)

	// Token should be deactivated.
	registry.mu.Lock()
	assert.Contains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()

	// Instance should be failed.
	instRepo.mu.Lock()
	inst := instRepo.instances[id]
	assert.Equal(t, instance.StateFailed, inst.State)
	instRepo.mu.Unlock()
}

func TestRecoverWebhooks_TimeoutPending_RegistersWithScheduler(t *testing.T) {
	id := uuid.New()
	token := "recover-token-pending"
	nodeID := "webhook-node"

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:               id,
			State:            instance.StateWaiting,
			WebhookToken:     &token,
			CurrentNodeID:    &nodeID,
			WorkflowSnapshot: makeWebhookSnapshot(nodeID, "3600"), // 1 hour timeout
		},
	}

	instRepo := newMockInstanceRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}
	nodeExecRepo := newMockNodeExecRepoForRecovery()

	// Node execution started 5 seconds ago (timeout of 3600s => still pending).
	startedAt := time.Now().Add(-5 * time.Second)
	nodeExecRepo.executions[id.String()+":"+nodeID] = &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: id,
		NodeID:     nodeID,
		StartedAt:  startedAt,
	}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler, nodeExecRepo)
	require.NoError(t, err)

	// Token should be re-registered (not deactivated).
	registry.mu.Lock()
	assert.Contains(t, registry.registerCalls, token)
	assert.NotContains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()

	// Scheduler should have the timeout registered.
	assert.Equal(t, 1, scheduler.Pending())
}
