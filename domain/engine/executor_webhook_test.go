package engine_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock webhook registry for executor tests
// ---------------------------------------------------------------------------

type mockWebhookRegistrar struct {
	mu             sync.Mutex
	registerCalls  []registerCall
	deactivateCalls []string
}

type registerCall struct {
	Token      string
	InstanceID uuid.UUID
	NodeID     string
	SecretHash *string
}

func (r *mockWebhookRegistrar) Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerCalls = append(r.registerCalls, registerCall{
		Token:      token,
		InstanceID: instanceID,
		NodeID:     nodeID,
		SecretHash: secretHash,
	})
}

func (r *mockWebhookRegistrar) Deactivate(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deactivateCalls = append(r.deactivateCalls, token)
}

func (r *mockWebhookRegistrar) getRegisterCalls() []registerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]registerCall, len(r.registerCalls))
	copy(cp, r.registerCalls)
	return cp
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWebhookWaitExecutor_GeneratesURLSafeToken(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	// Verify token is valid base64url and decodes to 32 bytes.
	calls := registry.getRegisterCalls()
	require.Len(t, calls, 1)
	token := calls[0].Token
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	assert.Len(t, decoded, 32)
}

func TestWebhookWaitExecutor_SecretHashing(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
		Config: workflow.NodeConfig{
			Secret: "my-secret",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := registry.getRegisterCalls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].SecretHash)

	// Verify the hash matches HMAC-SHA256(key=token, message=secret).
	token := calls[0].Token
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("my-secret"))
	expectedHash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	assert.Equal(t, expectedHash, *calls[0].SecretHash)
}

func TestWebhookWaitExecutor_NoSecret_NoHash(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := registry.getRegisterCalls()
	require.Len(t, calls, 1)
	assert.Nil(t, calls[0].SecretHash)
}

func TestWebhookWaitExecutor_TimeoutParsing(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	vars.SetInput("timeout", "60")

	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
		Config: workflow.NodeConfig{
			WaitTimeoutSeconds: "{{timeout}}",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	before := time.Now()
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, instanceID, calls[0].InstanceID)
	assert.Equal(t, engine.TimerTypeWebhook, calls[0].TimerType)
	expected := before.Add(60 * time.Second)
	assert.WithinDuration(t, expected, calls[0].WaitingUntil, 2*time.Second)
}

func TestWebhookWaitExecutor_ExceedsMaxTimeout_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()

	// 8 days in seconds exceeds the 7-day maximum.
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
		Config: workflow.NodeConfig{
			WaitTimeoutSeconds: "691200",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestWebhookWaitExecutor_NonNumericTimeout_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	vars.SetInput("timeout", "abc")

	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
		Config: workflow.NodeConfig{
			WaitTimeoutSeconds: "{{timeout}}",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not numeric")
}

func TestWebhookWaitExecutor_NoTimeout_NoScheduler(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := scheduler.getScheduleCalls()
	assert.Empty(t, calls)
}

func TestWebhookWaitExecutor_WritesNodeExecution(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)
	assert.Equal(t, instance.NodeStatusSucceeded, execs[0].Status)
	assert.Equal(t, "webhook-node", execs[0].NodeID)
	assert.NotNil(t, execs[0].CompletedAt)
	// Check webhookUrl in output
	webhookURL, ok := execs[0].Output["webhookUrl"]
	require.True(t, ok)
	assert.Contains(t, webhookURL.(string), "/api/v1/webhooks/wait/")
}

func TestWebhookWaitExecutor_TransitionsToWaiting(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
	assert.NotNil(t, inst.WebhookToken)
}

func TestWebhookWaitExecutor_RegistersWithRegistry(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := registry.getRegisterCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, instanceID, calls[0].InstanceID)
	assert.Equal(t, "webhook-node", calls[0].NodeID)
}

func TestWebhookWaitExecutor_NotifiesQueueManager(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	select {
	case <-qm.notifyCapacityFreedCh:
		// success
	case <-time.After(time.Second):
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

func TestWebhookWaitExecutor_ReturnsIsWait(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)
}

// ---------------------------------------------------------------------------
// Tests: Engine Cancel/Pause/Resume for webhook-waiting instances
// ---------------------------------------------------------------------------

func buildWebhookEngine(t *testing.T, f *testFixtures, scheduler *mockTimerScheduler, registry *mockWebhookRegistrar) *engine.Engine {
	t.Helper()
	webhookExec := engine.NewWebhookWaitExecutor(f.instanceRepo, f.nodeExecRepo, registry, scheduler, f.queueManager)
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     f.executor,
		workflow.NodeTypeScript:      f.executor,
		workflow.NodeTypeToolCall:    f.executor,
		workflow.NodeTypeConditional: f.executor,
		workflow.NodeTypeTransform:   f.executor,
		workflow.NodeTypeTimerDelay:  engine.NewTimerDelayExecutor(f.instanceRepo, f.nodeExecRepo, scheduler, f.queueManager),
		workflow.NodeTypeWebhookWait: webhookExec,
	}
	eng := engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, &mockQueueRepo{instRepo: f.instanceRepo})
	eng.SetScheduler(scheduler)
	eng.SetWebhookRegistry(registry)

	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	f.queueManager.mu.Unlock()
	return eng
}

func webhookWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "webhook-ops-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "webhook"},
			{Source: "webhook", Target: "C"},
		},
	}
}

func TestCancel_WebhookWaiting_DeactivatesEndpoint(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(webhookWorkflow())

	id, _, err := eng.Trigger(context.Background(), "webhook-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Get the token that was registered.
	registry.mu.Lock()
	require.Len(t, registry.registerCalls, 1)
	token := registry.registerCalls[0].Token
	registry.mu.Unlock()

	err = eng.Cancel(id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)

	// Token should be deactivated.
	registry.mu.Lock()
	assert.Contains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()
}

func TestCancel_WebhookWaiting_CancelsTimeout(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(webhookWorkflow())

	id, _, err := eng.Trigger(context.Background(), "webhook-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	err = eng.Cancel(id)
	require.NoError(t, err)

	// Scheduler cancel should have been called.
	scheduler.mu.Lock()
	assert.Contains(t, scheduler.cancelCalls, id)
	scheduler.mu.Unlock()
}

func TestPause_WebhookWaiting_EndpointStaysActive(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(webhookWorkflow())

	id, _, err := eng.Trigger(context.Background(), "webhook-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	err = eng.Pause(id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StatePaused, inst.State)

	// Webhook should NOT be deactivated (stays active during pause).
	registry.mu.Lock()
	assert.Empty(t, registry.deactivateCalls)
	registry.mu.Unlock()
}

func TestResume_WebhookPaused_ReceivedWhilePaused_GoesToQueued(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(webhookWorkflow())

	id, _, err := eng.Trigger(context.Background(), "webhook-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Pause the instance.
	err = eng.Pause(id)
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Simulate webhook received while paused (set WebhookReceivedAt directly).
	f.instanceRepo.mu.Lock()
	now := time.Now()
	f.instanceRepo.instances[id].WebhookReceivedAt = &now
	f.instanceRepo.mu.Unlock()

	// Resume — should go to queued since webhook was received.
	err = eng.Resume(id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestResume_WebhookPaused_NotReceived_GoesToWaiting(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(webhookWorkflow())

	id, _, err := eng.Trigger(context.Background(), "webhook-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Pause the instance.
	err = eng.Pause(id)
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Resume without webhook received — should go back to waiting.
	err = eng.Resume(id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

// ---------------------------------------------------------------------------
// Tests: WebhookWaitExecutor error cases
// ---------------------------------------------------------------------------

func TestWebhookWaitExecutor_NoInstanceIDInContext_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	_, err := executor.Execute(context.Background(), node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestWebhookWaitExecutor_SetWebhookFieldsFails_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	instRepo.setWebhookFieldsErr = fmt.Errorf("db error")
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting webhook fields")

	// Instance should NOT be in waiting state (SetWebhookFields fails before SetWaiting).
	inst := instRepo.get(instanceID)
	assert.Equal(t, instance.StateRunning, inst.State)

	// Registry should NOT have been called.
	assert.Empty(t, registry.getRegisterCalls())
}

func TestWebhookWaitExecutor_SetWaitingFails_RollsBackWebhookFields(t *testing.T) {
	instRepo := newMockInstanceRepo()
	instRepo.setWaitingErr = fmt.Errorf("db error")
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
		Config: workflow.NodeConfig{
			WaitTimeoutSeconds: "60",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting waiting state")

	// Webhook fields should have been rolled back (token cleared).
	inst := instRepo.get(instanceID)
	assert.Nil(t, inst.WebhookToken, "webhook token should be cleared on rollback")
	assert.Equal(t, instance.StateRunning, inst.State)
}

// TestWebhookWaitExecutor_NodeExecCreateFails_ReturnsError verifies that a
// nodeExecs.Create failure propagates as an error. Unlike the timer executor,
// this happens before SetWaiting, so the instance remains running and the
// engine can safely call SetFailed.
func TestWebhookWaitExecutor_NodeExecCreateFails_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	nodeExecRepo.createErr = fmt.Errorf("db write failed")
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()
	registry := &mockWebhookRegistrar{}

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewWebhookWaitExecutor(instRepo, nodeExecRepo, registry, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "webhook-node",
		Type: workflow.NodeTypeWebhookWait,
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating node execution")

	// Instance must still be running — the engine will call SetFailed.
	inst := instRepo.get(instanceID)
	assert.Equal(t, instance.StateRunning, inst.State)

	// Registry and scheduler must not have been called.
	assert.Empty(t, registry.getRegisterCalls())
	assert.Empty(t, scheduler.getScheduleCalls())
}
