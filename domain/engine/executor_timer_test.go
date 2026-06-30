package engine_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mocks for timer delay executor tests
// ---------------------------------------------------------------------------

type mockTimerScheduler struct {
	mu             sync.Mutex
	scheduleCalls  []scheduleCall
	cancelCalls    []uuid.UUID
}

type scheduleCall struct {
	InstanceID   uuid.UUID
	WaitingUntil time.Time
	TimerType    string
}

func (s *mockTimerScheduler) Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleCalls = append(s.scheduleCalls, scheduleCall{
		InstanceID:   instanceID,
		WaitingUntil: waitingUntil,
		TimerType:    timerType,
	})
}

func (s *mockTimerScheduler) Cancel(instanceID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCalls = append(s.cancelCalls, instanceID)
}

func (s *mockTimerScheduler) getScheduleCalls() []scheduleCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]scheduleCall, len(s.scheduleCalls))
	copy(cp, s.scheduleCalls)
	return cp
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestTimerDelayExecutor_ResolvesSubstitution(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()
	vars.SetInput("delay", "30")

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "{{delay}}",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	before := time.Now()
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, instanceID, calls[0].InstanceID)
	// 30 seconds from now (with some tolerance)
	expected := before.Add(30 * time.Second)
	assert.WithinDuration(t, expected, calls[0].WaitingUntil, 2*time.Second)
}

func TestTimerDelayExecutor_NonNumericDuration_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()
	vars.SetInput("delay", "abc")

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "{{delay}}",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not numeric")
}

func TestTimerDelayExecutor_ExceedsMaxDuration_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	// 8 days in seconds exceeds the 7-day maximum.
	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "691200",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

func TestTimerDelayExecutor_ZeroDuration_ImmediateEligibility(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "0",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	before := time.Now()
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	// waitingUntil should be now or in the past (immediately eligible)
	assert.True(t, !calls[0].WaitingUntil.After(before.Add(time.Second)))
}

func TestTimerDelayExecutor_NegativeDuration_ImmediateEligibility(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "-5",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	before := time.Now()
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	assert.True(t, !calls[0].WaitingUntil.After(before.Add(time.Second)))
}

func TestTimerDelayExecutor_SetsWaitingState(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "10",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	inst := instRepo.get(instanceID)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
	assert.NotNil(t, inst.WaitingUntil)
}

func TestTimerDelayExecutor_RegistersWithScheduler(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "5",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, instanceID, calls[0].InstanceID)
	assert.Equal(t, engine.TimerTypeDelay, calls[0].TimerType)
}

func TestTimerDelayExecutor_NotifiesQueueManager(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "5",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	// Check that capacity freed was notified
	select {
	case <-qm.notifyCapacityFreedCh:
		// success
	case <-time.After(time.Second):
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

func TestTimerDelayExecutor_WritesNodeExecution(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "5",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)

	execs, _ := nodeExecRepo.ListByInstance(context.Background(), instanceID)
	require.Len(t, execs, 1)
	assert.Equal(t, instance.NodeStatusSucceeded, execs[0].Status)
	assert.Equal(t, "timer-node", execs[0].NodeID)
	assert.NotNil(t, execs[0].CompletedAt)
}

func TestTimerDelayExecutor_ReturnsIsWait(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()

	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "10",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, node, vars, "test-wf")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)
}

// ---------------------------------------------------------------------------
// Tests: Engine run() loop handling of WaitResult
// ---------------------------------------------------------------------------

func TestRun_WaitResult_ExitsCleanly(t *testing.T) {
	f := newTestFixtures()

	scheduler := &mockTimerScheduler{}
	f.engine.SetScheduler(scheduler)

	timerExec := engine.NewTimerDelayExecutor(f.instanceRepo, f.nodeExecRepo, scheduler, f.queueManager)

	// Register the timer executor alongside the standard mock executor.
	wf := &workflow.Workflow{
		Name: "timer-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "60"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	// Build a new engine with the timer executor registered.
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     f.executor,
		workflow.NodeTypeScript:      f.executor,
		workflow.NodeTypeToolCall:    f.executor,
		workflow.NodeTypeConditional: f.executor,
		workflow.NodeTypeTransform:   f.executor,
		workflow.NodeTypeTimerDelay:  timerExec,
	}
	eng := engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, &mockQueueRepo{instRepo: f.instanceRepo}, "")
	eng.SetScheduler(scheduler)

	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	f.queueManager.mu.Unlock()

	id, _, err := eng.Trigger(context.Background(), "timer-wf", nil, "normal")
	require.NoError(t, err)

	// Wait for the instance to reach waiting state.
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)
	assert.Equal(t, instance.StateWaiting, inst.State)

	// Ensure it did NOT complete or fail.
	assert.Nil(t, inst.CompletedAt)
}

func TestRun_WaitResult_DoesNotFollowEdges(t *testing.T) {
	f := newTestFixtures()

	scheduler := &mockTimerScheduler{}
	f.engine.SetScheduler(scheduler)

	timerExec := engine.NewTimerDelayExecutor(f.instanceRepo, f.nodeExecRepo, scheduler, f.queueManager)

	wf := &workflow.Workflow{
		Name: "timer-no-follow-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "60"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     f.executor,
		workflow.NodeTypeScript:      f.executor,
		workflow.NodeTypeToolCall:    f.executor,
		workflow.NodeTypeConditional: f.executor,
		workflow.NodeTypeTransform:   f.executor,
		workflow.NodeTypeTimerDelay:  timerExec,
	}
	eng := engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, &mockQueueRepo{instRepo: f.instanceRepo}, "")
	eng.SetScheduler(scheduler)

	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	f.queueManager.mu.Unlock()

	id, _, err := eng.Trigger(context.Background(), "timer-no-follow-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Allow some time for any errant follow-up execution.
	time.Sleep(100 * time.Millisecond)

	// Node C should NOT have been called.
	calls := f.executor.getCalls()
	for _, c := range calls {
		assert.NotEqual(t, "C", c.NodeID, "node C should not execute after timer delay")
	}
}

// ---------------------------------------------------------------------------
// Tests: Waiting state operations (cancel/pause/resume)
// ---------------------------------------------------------------------------

func buildTimerEngine(t *testing.T, f *testFixtures, scheduler *mockTimerScheduler) *engine.Engine {
	t.Helper()
	timerExec := engine.NewTimerDelayExecutor(f.instanceRepo, f.nodeExecRepo, scheduler, f.queueManager)
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     f.executor,
		workflow.NodeTypeScript:      f.executor,
		workflow.NodeTypeToolCall:    f.executor,
		workflow.NodeTypeConditional: f.executor,
		workflow.NodeTypeTransform:   f.executor,
		workflow.NodeTypeTimerDelay:  timerExec,
	}
	eng := engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, &mockQueueRepo{instRepo: f.instanceRepo}, "")
	eng.SetScheduler(scheduler)

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

func timerWorkflow(durationSeconds string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "timer-ops-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: durationSeconds}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
}

func TestCancel_WaitingInstance_CancelsTimer(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(timerWorkflow("60"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	err = eng.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)

	// Scheduler cancel should have been called.
	scheduler.mu.Lock()
	assert.Contains(t, scheduler.cancelCalls, id)
	scheduler.mu.Unlock()
}

func TestCancel_WaitingInstance_NotifiesQueueManager(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(timerWorkflow("60"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Drain any capacity freed notifications from the timer setup.
	for len(f.queueManager.notifyCapacityFreedCh) > 0 {
		<-f.queueManager.notifyCapacityFreedCh
	}

	err = eng.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	select {
	case <-f.queueManager.notifyCapacityFreedCh:
		// success
	case <-time.After(time.Second):
		t.Fatal("expected NotifyCapacityFreed to be called")
	}
}

func TestPause_WaitingInstance_TransitionsToPaused(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(timerWorkflow("60"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	err = eng.Pause(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StatePaused, inst.State)
}

func TestPause_WaitingInstance_DoesNotCancelTimer(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(timerWorkflow("60"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	err = eng.Pause(bgAdmin(), id)
	require.NoError(t, err)

	// Scheduler cancel should NOT have been called.
	scheduler.mu.Lock()
	assert.NotContains(t, scheduler.cancelCalls, id)
	scheduler.mu.Unlock()
}

func TestResume_PausedFromWaiting_TimerExpired_GoesToQueued(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	// Use a very short timer so it expires quickly.
	f.workflowRepo.add(timerWorkflow("0"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Pause it.
	err = eng.Pause(bgAdmin(), id)
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// waitingUntil was set to "now" (0 duration), so it's already expired.
	// Resume should go to queued.
	err = eng.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestResume_PausedFromWaiting_TimerNotExpired_GoesToWaiting(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(timerWorkflow("3600")) // 1 hour — definitely not expired

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Pause it.
	err = eng.Pause(bgAdmin(), id)
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Resume — timer not expired, go back to waiting.
	err = eng.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestResume_PausedFromRunning_StartsInstance(t *testing.T) {
	// This test verifies that normal resume (not from waiting) still works.
	// We need 3 nodes so that pausing during B still has a next node (C)
	// for the pause check to catch at the iteration boundary.
	f := newTestFixtures()

	wf := &workflow.Workflow{
		Name: "normal-resume-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	// Block on node B so we can pause while it's running.
	bStarted := make(chan struct{})
	bDone := make(chan struct{})
	var callCount int
	var callMu sync.Mutex
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		callMu.Lock()
		callCount++
		c := callCount
		callMu.Unlock()
		if c == 1 {
			close(bStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-bDone:
				return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
			}
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "normal-resume-wf", nil, "normal")
	require.NoError(t, err)

	<-bStarted
	err = f.engine.Pause(bgAdmin(), id)
	require.NoError(t, err)
	close(bDone)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// WaitingUntil should be nil for a paused-from-running instance.
	inst := f.instanceRepo.get(id)
	assert.Nil(t, inst.WaitingUntil)

	// Resume should restart from current node and eventually complete.
	err = f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	finalInst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, finalInst.State)
}

// ---------------------------------------------------------------------------
// Integration-style lifecycle tests: full timer delay flow with real scheduler
// ---------------------------------------------------------------------------

// buildFullTimerEngine creates an engine with a real Scheduler (not mocked)
// for end-to-end lifecycle testing.
func buildFullTimerEngine(t *testing.T, f *testFixtures) (*engine.Engine, *queue.Scheduler) {
	t.Helper()

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	qr := &mockQueueRepo{instRepo: f.instanceRepo}

	// Build a real scheduler with the queue manager as handler.
	mgr := queue.NewManager(qr, f.instanceRepo, cfg, nil) // starter set later
	scheduler := queue.NewScheduler(mgr)

	timerExec := engine.NewTimerDelayExecutor(f.instanceRepo, f.nodeExecRepo, scheduler, f.queueManager)
	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     f.executor,
		workflow.NodeTypeScript:      f.executor,
		workflow.NodeTypeToolCall:    f.executor,
		workflow.NodeTypeConditional: f.executor,
		workflow.NodeTypeTransform:   f.executor,
		workflow.NodeTypeTimerDelay:  timerExec,
	}
	eng := engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, qr, "")
	eng.SetScheduler(scheduler)

	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	f.queueManager.mu.Unlock()

	return eng, scheduler
}

func TestTimerDelay_FullFlow(t *testing.T) {
	f := newTestFixtures()
	eng, scheduler := buildFullTimerEngine(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	// Create workflow: entry -> timer_delay(0.1s) -> script_node
	wf := &workflow.Workflow{
		Name: "full-flow-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "0.1"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	id, _, err := eng.Trigger(context.Background(), "full-flow-wf", nil, "normal")
	require.NoError(t, err)

	// Instance should go to waiting after executing A and reaching timer.
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Verify waitingUntil is set.
	inst := f.instanceRepo.get(id)
	assert.NotNil(t, inst.WaitingUntil)

	// Wait for the timer to fire and the instance to be re-queued.
	// The mock queueManager's NotifyCapacityFreed is a no-op for Evaluate,
	// but the real scheduler will call OnTimerFired which calls SetQueued on the mockQueueRepo.
	time.Sleep(300 * time.Millisecond)

	// After the timer fires, the instance should be in queued state
	// (the real scheduler called OnTimerFired -> SetQueued on mockQueueRepo).
	inst = f.instanceRepo.get(id)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestTimerDelay_WithCancel(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	wf := &workflow.Workflow{
		Name: "cancel-timer-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "60"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	id, _, err := eng.Trigger(context.Background(), "cancel-timer-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Cancel the waiting instance.
	err = eng.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)

	// Wait briefly to make sure timer doesn't fire.
	time.Sleep(100 * time.Millisecond)
	inst = f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)
}

func TestTimerDelay_ZeroDuration(t *testing.T) {
	f := newTestFixtures()
	eng, scheduler := buildFullTimerEngine(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	wf := &workflow.Workflow{
		Name: "zero-dur-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "0"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	id, _, err := eng.Trigger(context.Background(), "zero-dur-wf", nil, "normal")
	require.NoError(t, err)

	// Zero duration: instance goes to waiting, then immediately eligible.
	// The scheduler should fire very quickly.
	time.Sleep(200 * time.Millisecond)

	inst := f.instanceRepo.get(id)
	// Should have transitioned to queued (scheduler fired immediately).
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestTimerDelay_SubstitutionVariable(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	wf := &workflow.Workflow{
		Name: "subst-timer-wf",
		Inputs: []workflow.InputField{
			{Name: "delay", Type: "number", Required: true},
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "{{delay}}"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	id, _, err := eng.Trigger(context.Background(), "subst-timer-wf", map[string]any{"delay": float64(5)}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Verify the scheduler was called with approximately 5 seconds.
	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	expectedDuration := 5 * time.Second
	actualDuration := time.Until(calls[0].WaitingUntil)
	assert.InDelta(t, expectedDuration.Seconds(), actualDuration.Seconds(), 2)
}

func TestTimerDelay_NonNumericDuration_FailsInstance(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	wf := &workflow.Workflow{
		Name: "bad-dur-wf",
		Inputs: []workflow.InputField{
			{Name: "delay", Type: "string", Required: true},
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "{{delay}}"}},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "timer"},
			{Source: "timer", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	id, _, err := eng.Trigger(context.Background(), "bad-dur-wf", map[string]any{"delay": "abc"}, "normal")
	require.NoError(t, err)

	// The node should fail, and with default "halt" error behavior, instance should fail.
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "timer", *inst.FailedNodeID)
}

// ---------------------------------------------------------------------------
// Tests: TimerDelayExecutor error cases
// ---------------------------------------------------------------------------

func TestTimerDelayExecutor_NoInstanceIDInContext_Fails(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "10",
		},
	}

	// No instance ID embedded in context.
	_, err := executor.Execute(context.Background(), node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance ID not found in context")
}

func TestTimerDelayExecutor_SetWaitingFails_ReturnsError(t *testing.T) {
	instRepo := newMockInstanceRepo()
	instRepo.setWaitingErr = fmt.Errorf("db error")
	nodeExecRepo := newMockNodeExecutionRepo()
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "10",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	_, err := executor.Execute(ctx, node, vars, "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to set waiting state")

	// Scheduler should NOT have been called (we never reached that step).
	assert.Empty(t, scheduler.getScheduleCalls())
}

// TestTimerDelayExecutor_NodeExecCreateFails_StillSchedules verifies that if
// writing the node execution record fails, the executor still succeeds: SetWaiting
// already committed, so the scheduler and queue notification must still proceed.
func TestTimerDelayExecutor_NodeExecCreateFails_StillSchedules(t *testing.T) {
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	nodeExecRepo.createErr = fmt.Errorf("db write failed")
	scheduler := &mockTimerScheduler{}
	qm := newMockQueueManager()

	instanceID := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateRunning,
	}
	instRepo.mu.Unlock()

	executor := engine.NewTimerDelayExecutor(instRepo, nodeExecRepo, scheduler, qm)

	vars := variable.NewStore()
	node := workflow.Node{
		ID:   "timer-node",
		Type: workflow.NodeTypeTimerDelay,
		Config: workflow.NodeConfig{
			DurationSeconds: "10",
		},
	}

	ctx := engine.ContextWithInstanceID(context.Background(), instanceID)
	result, err := executor.Execute(ctx, node, vars, "test-wf")

	// The error must NOT propagate: SetWaiting already committed.
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsWait)

	// Scheduler must still be called.
	calls := scheduler.getScheduleCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, instanceID, calls[0].InstanceID)

	// Instance state should be waiting.
	inst := instRepo.get(instanceID)
	assert.Equal(t, instance.StateWaiting, inst.State)
}
