package engine_test

import (
	"context"
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

// These tests cover what happens AFTER a wait node's wait is over: the timer
// fired, or the webhook arrived. The wake paths (queue.Manager.OnTimerFired,
// the webhook handler, Resume from paused, startup recovery) only re-queue the
// instance; the run then restarts at current_node_id, which is the wait node.
// The run must complete that node and move on, not arm a fresh wait.

// wakeInstance mirrors the production wake paths: waiting -> queued, then the
// queue starts the run.
func wakeInstance(t *testing.T, f *testFixtures, eng *engine.Engine, id uuid.UUID) {
	t.Helper()
	q := &mockQueueRepo{instRepo: f.instanceRepo}
	require.NoError(t, q.SetQueued(context.Background(), id, time.Now()))
	require.NoError(t, eng.StartInstance(context.Background(), id))
}

func countExecs(t *testing.T, f *testFixtures, id uuid.UUID, nodeID string) int {
	t.Helper()
	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	n := 0
	for _, e := range execs {
		if e.NodeID == nodeID {
			n++
		}
	}
	return n
}

func TestTimerDelay_AfterTimerFires_AdvancesToNextNode(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)
	f.workflowRepo.add(timerWorkflow("0"))

	id, _, err := eng.Trigger(context.Background(), "timer-ops-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	wakeInstance(t, f, eng, id)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateWaiting, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State, "a fired timer must advance past the timer node, not re-arm")
	assert.Equal(t, 1, countExecs(t, f, id, "timer"), "the timer node must run exactly once")
	assert.Nil(t, inst.WaitingUntil, "the consumed timer deadline must be cleared")

	called := false
	for _, c := range f.executor.getCalls() {
		if c.NodeID == "C" {
			called = true
		}
	}
	assert.True(t, called, "the node after the timer must run")

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	assert.Len(t, scheduler.scheduleCalls, 1, "the timer must be scheduled once, not re-armed on wake")
}

func TestTimerDelay_InLoop_ArmsAFreshWaitOnEachPass(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	eng := buildTimerEngine(t, f, scheduler)

	f.workflowRepo.add(&workflow.Workflow{
		Name: "timer-loop-wf",
		Nodes: []workflow.Node{
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Entrypoint: true, Config: workflow.NodeConfig{DurationSeconds: "0"}},
			{ID: "work", Type: workflow.NodeTypeScript},
			{ID: "again", Type: workflow.NodeTypeConditional},
			{ID: "done", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "timer", Target: "work"},
			{Source: "work", Target: "again"},
			{Source: "again", Target: "timer", Output: "loop"},
			{Source: "again", Target: "done", Output: "exit"},
		},
	})
	passes := 0
	f.executor.onNode("again", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		passes++
		if passes == 1 {
			return &engine.NodeResult{OutputPort: "loop"}, nil
		}
		return &engine.NodeResult{OutputPort: "exit"}, nil
	})

	id, _, err := eng.Trigger(context.Background(), "timer-loop-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// First fire: one pass of the body, then the back-edge revisits the timer,
	// which must arm a NEW wait rather than fall straight through.
	wakeInstance(t, f, eng, id)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateWaiting, inst.State, "the loop revisit must wait again")
	assert.Equal(t, 2, countExecs(t, f, id, "timer"))
	assert.NotNil(t, inst.WaitingUntil, "the revisit must set a new deadline")

	// Second fire: exit the loop.
	wakeInstance(t, f, eng, id)
	inst = waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, 2, passes)
	assert.Equal(t, 2, countExecs(t, f, id, "timer"))
}

func TestWebhookWait_AfterDelivery_AdvancesWithPayload(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	eng := buildWebhookEngine(t, f, scheduler, registry)

	f.workflowRepo.add(&workflow.Workflow{
		Name: "hook-wf",
		Nodes: []workflow.Node{
			{ID: "hook", Type: workflow.NodeTypeWebhookWait, Entrypoint: true},
			{ID: "after", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{{Source: "hook", Target: "after"}},
	})
	var seen any
	f.executor.onNode("after", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		seen = variable.Resolve("{{hook.answer}}", vars)
		return &engine.NodeResult{}, nil
	})

	id, _, err := eng.Trigger(context.Background(), "hook-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	// Simulate the webhook handler: store the payload on the node's record,
	// stamp receipt, then wake.
	ctx := context.Background()
	rec, err := f.nodeExecRepo.GetByInstanceAndNode(ctx, id, "hook")
	require.NoError(t, err)
	rec.Output = map[string]any{"answer": "42"}
	require.NoError(t, f.nodeExecRepo.Update(ctx, rec))
	require.NoError(t, f.instanceRepo.SetWebhookReceived(ctx, id, time.Now()))
	wakeInstance(t, f, eng, id)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateWaiting, instance.StateFailed)
	require.Equal(t, instance.StateCompleted, inst.State, "a delivered webhook must advance past the wait node, not re-arm")
	assert.Equal(t, 1, countExecs(t, f, id, "hook"), "the webhook node must run exactly once")
	assert.Equal(t, "42", seen, "the payload must reach downstream nodes")
	assert.Nil(t, inst.WebhookToken, "the consumed token must be cleared")
	assert.Nil(t, inst.WebhookReceivedAt, "the consumed receipt must be cleared so a later wait is not pre-satisfied")

	registry.mu.Lock()
	defer registry.mu.Unlock()
	assert.Len(t, registry.registerCalls, 1, "no second token may be issued")
}
