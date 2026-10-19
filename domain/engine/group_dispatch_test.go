package engine_test

import (
	"context"
	"encoding/json"
	"errors"
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

// The callback runs without the recorder's lock so it can re-enter the engine.
type recordingGroupHandler struct {
	mu     sync.Mutex
	calls  []uuid.UUID
	onCall func(context.Context, uuid.UUID)
}

func (h *recordingGroupHandler) OnTaskTerminal(ctx context.Context, childID uuid.UUID) {
	h.mu.Lock()
	h.calls = append(h.calls, childID)
	hook := h.onCall
	h.mu.Unlock()
	if hook != nil {
		hook(ctx, childID)
	}
}

func (h *recordingGroupHandler) called() []uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uuid.UUID(nil), h.calls...)
}

var _ engine.GroupTerminalHandler = (*recordingGroupHandler)(nil)

type groupDispatchFixture struct {
	f        *testFixtures
	handler  *recordingGroupHandler
	owner    uuid.UUID
	parentID uuid.UUID
	runID    uuid.UUID
}

func newGroupDispatchFixture(t *testing.T, wireHandler bool) *groupDispatchFixture {
	t.Helper()
	f := newTestFixtures()
	t.Cleanup(f.engine.Shutdown)
	owner := uuid.New()
	node := "group-1"
	parent := &instance.Instance{
		ID: uuid.New(), OwnerID: owner, WorkflowName: "parent",
		State: instance.StateWaiting, Priority: instance.PriorityNormal, CurrentNodeID: &node,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), parent))
	h := &recordingGroupHandler{}
	if wireHandler {
		f.engine.SetGroupTerminalHandler(h)
	}
	return &groupDispatchFixture{f: f, handler: h, owner: owner, parentID: parent.ID, runID: uuid.New()}
}

func (g *groupDispatchFixture) task(t *testing.T, key string, state instance.State, snapshot map[string]any) uuid.UUID {
	t.Helper()
	pid, node, rid, k := g.parentID, "group-1", g.runID, key
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: g.owner, WorkflowName: "parent", WorkflowSnapshot: snapshot,
		State: state, Priority: instance.PriorityNormal, Variables: map[string]any{},
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: instance.SpawnModeGroupTask, SpawnDepth: 1,
		GroupRunID: &rid, GroupTaskKey: &k, GroupTaskKind: instance.GroupTaskKindBranch,
	}
	require.NoError(t, g.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func (g *groupDispatchFixture) legacyChild(t *testing.T, mode instance.SpawnMode, state instance.State) uuid.UUID {
	t.Helper()
	pid, node := g.parentID, "group-1"
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: g.owner, WorkflowName: "child",
		State: state, Priority: instance.PriorityNormal,
		ParentInstanceID: &pid, ParentNodeID: &node, SpawnMode: mode, SpawnDepth: 1,
	}
	require.NoError(t, g.f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func (g *groupDispatchFixture) stateOf(t *testing.T, id uuid.UUID) instance.State {
	t.Helper()
	got, err := g.f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return got.State
}

func snapshotOf(t *testing.T, wf *workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// Even a parent waiting at the matching node may only be woken by the coordinator.
func TestGroupDispatch_TerminalGroupTaskRoutesToHandlerNotLegacyWake(t *testing.T) {
	for _, st := range []instance.State{instance.StateCompleted, instance.StateFailed, instance.StateCancelled} {
		g := newGroupDispatchFixture(t, true)
		child := g.task(t, "a", st, nil)
		g.f.engine.OnChildTerminal(context.Background(), child)
		assert.Equal(t, []uuid.UUID{child}, g.handler.called(), "state %s", st)
		assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID), "state %s", st)
		assert.Equal(t, 0, g.f.queueManager.notifyCount(), "state %s", st)
	}
}

func TestGroupDispatch_RunCompletionReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateQueued, snapshotOf(t, singleNodeWorkflow()))
	require.NoError(t, g.f.engine.StartInstance(context.Background(), child))
	waitForInstance(t, g.f.instanceRepo, child, 5*time.Second, instance.StateCompleted)
	require.Eventually(t, func() bool { return len(g.handler.called()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
}

func TestGroupDispatch_RunFailureReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	g.f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, errors.New("boom")
	})
	child := g.task(t, "a", instance.StateQueued, snapshotOf(t, singleNodeWorkflow()))
	require.NoError(t, g.f.engine.StartInstance(context.Background(), child))
	waitForInstance(t, g.f.instanceRepo, child, 5*time.Second, instance.StateFailed)
	require.Eventually(t, func() bool { return len(g.handler.called()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

func TestGroupDispatch_CancelReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateRunning, nil)
	require.NoError(t, g.f.engine.Cancel(bgAdmin(), child))
	assert.Equal(t, instance.StateCancelled, g.stateOf(t, child))
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

// Exercise the real queue's terminal hook, not a call that bypasses the producer.
func TestGroupDispatch_WebhookTimeoutFromQueueReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateWaiting, nil)
	token := "tok"
	require.NoError(t, g.f.instanceRepo.SetWebhookFields(context.Background(), child, &token, nil))
	mgr := queue.NewManager(g.f.queueRepo, g.f.instanceRepo, &config.Config{}, g.f.engine)
	mgr.SetChildTerminalHook(g.f.engine.OnChildTerminal)
	require.NoError(t, mgr.OnTimerFired(context.Background(), child, engine.TimerTypeWebhook))
	assert.Equal(t, instance.StateFailed, g.stateOf(t, child))
	assert.Equal(t, []uuid.UUID{child}, g.handler.called())
}

func TestGroupDispatch_LegacyWaitForChildStillWakesWithHandlerWired(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.legacyChild(t, instance.SpawnModeWaitForChild, instance.StateCompleted)
	g.f.engine.OnChildTerminal(context.Background(), child)
	assert.Equal(t, instance.StateQueued, g.stateOf(t, g.parentID))
	assert.GreaterOrEqual(t, g.f.queueManager.notifyCount(), 1)
	assert.Empty(t, g.handler.called())
}

func TestGroupDispatch_FireAndForgetChildNeverReachesHandler(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.legacyChild(t, instance.SpawnModeFireAndForget, instance.StateCompleted)
	g.f.engine.OnChildTerminal(context.Background(), child)
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
	assert.Empty(t, g.handler.called())
}

func TestGroupDispatch_NonTerminalGroupTaskIsNotDispatched(t *testing.T) {
	for _, st := range []instance.State{instance.StateQueued, instance.StateRunning, instance.StateWaiting, instance.StatePaused} {
		g := newGroupDispatchFixture(t, true)
		child := g.task(t, "a", st, nil)
		g.f.engine.OnChildTerminal(context.Background(), child)
		assert.Empty(t, g.handler.called(), "state %s", st)
	}
}

func TestGroupDispatch_UnreadableChildIsNotDispatched(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateCompleted, nil)
	g.f.instanceRepo.mu.Lock()
	g.f.instanceRepo.getErr = errors.New("db down")
	g.f.instanceRepo.mu.Unlock()
	g.f.engine.OnChildTerminal(context.Background(), child)
	assert.Empty(t, g.handler.called())
}

func TestSetGroupTerminalHandler_PanicsOnNil(t *testing.T) {
	f := newTestFixtures()
	t.Cleanup(f.engine.Shutdown)
	assert.Panics(t, func() { f.engine.SetGroupTerminalHandler(nil) })
}

func TestGroupDispatch_UnsetHandlerIsALoggedNoOp(t *testing.T) {
	g := newGroupDispatchFixture(t, false)
	child := g.task(t, "a", instance.StateCompleted, nil)
	require.NotPanics(t, func() { g.f.engine.OnChildTerminal(context.Background(), child) })
	assert.Empty(t, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
	assert.Equal(t, 0, g.f.queueManager.notifyCount())
}

func TestGroupDispatch_HandlerMayBeReenteredThroughCancel(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	a := g.task(t, "a", instance.StateRunning, nil)
	b := g.task(t, "b", instance.StateRunning, nil)
	g.handler.onCall = func(_ context.Context, childID uuid.UUID) {
		if childID == a {
			_ = g.f.engine.Cancel(bgAdmin(), b)
		}
	}
	done := make(chan error, 1)
	go func() { done <- g.f.engine.Cancel(bgAdmin(), a) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch deadlocked when the handler re-entered the engine through Cancel")
	}
	assert.Equal(t, []uuid.UUID{a, b}, g.handler.called())
	assert.Equal(t, instance.StateCancelled, g.stateOf(t, a))
	assert.Equal(t, instance.StateCancelled, g.stateOf(t, b))
}

func TestRecoverCrashedInstances_DoesNotDispatchGroupTasks(t *testing.T) {
	g := newGroupDispatchFixture(t, true)
	child := g.task(t, "a", instance.StateRunning, nil)
	require.NoError(t, g.f.engine.RecoverCrashedInstances())
	assert.Equal(t, instance.StateFailed, g.stateOf(t, child))
	assert.Empty(t, g.handler.called())
	assert.Equal(t, instance.StateWaiting, g.stateOf(t, g.parentID))
}
