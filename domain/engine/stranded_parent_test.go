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

func newStrandFixtures() *testFixtures {
	f := newTestFixtures()
	sw := newFakeSpawnWaits()
	sw.instances = f.instanceRepo
	f.engine.SetSpawnWaits(sw)
	return f
}

func seedRunAt(t *testing.T, f *testFixtures, state instance.State, node string) uuid.UUID {
	t.Helper()
	n := node
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: state, Priority: instance.PriorityNormal, CurrentNodeID: &n,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), inst))
	return inst.ID
}

func stateNow(t *testing.T, f *testFixtures, id uuid.UUID) instance.State {
	t.Helper()
	inst, err := f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	return inst.State
}

func TestAdoptStrandedParent_NoWalker_SuspendsToWaiting(t *testing.T) {
	f := newStrandFixtures()
	id := seedRunAt(t, f, instance.StateRunning, "spawn-1")

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "spawn-1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, instance.StateWaiting, stateNow(t, f, id))
}

func TestAdoptStrandedParent_NotRunning_NoOp(t *testing.T) {
	f := newStrandFixtures()
	id := seedRunAt(t, f, instance.StatePaused, "spawn-1")

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "spawn-1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, instance.StatePaused, stateNow(t, f, id))
}

// A parent whose walker is live is never adopted, even when it sits at the
// node the sweep names: adopting it would hand one run to two walkers.
func TestAdoptStrandedParent_LiveWalker_NoOp(t *testing.T) {
	f := newStrandFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name:  "strand-live-wf",
		Nodes: []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
	})
	started := make(chan struct{})
	release := make(chan struct{})
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		close(started)
		<-release
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "strand-live-wf", nil, "normal")
	require.NoError(t, err)
	<-started

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "A")
	require.NoError(t, err)
	assert.False(t, ok, "a run with a live walker must never be adopted")
	assert.Equal(t, instance.StateRunning, stateNow(t, f, id))

	close(release)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

// StartInstance writes `running` and registers the walker. If those two steps
// are not atomic under the walker lock, a sweep landing between them sees a
// running run with no walker and adopts a run that is merely starting.
//
// The hazard is every WOKEN parent: one re-queued from `waiting` at its spawn
// node goes through StartInstance with current_node_id = that node and a
// terminal child, which is exactly what ListRunningOnTerminalChild returns. The
// run is therefore seeded as a queued parent at a real spawn node ("A"); the
// SQL-faithful fake would never let a nil-node row be adopted.
func TestStartInstance_RegistersWalkerBeforeRunningIsVisible(t *testing.T) {
	f := newStrandFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name:  "strand-start-wf",
		Nodes: []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
	})
	release := make(chan struct{})
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		<-release
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})

	// Trigger without starting: the run stays queued, then is moved to the
	// awaited spawn node exactly as a woken parent is.
	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
	f.queueManager.mu.Unlock()
	id, _, err := f.engine.Trigger(context.Background(), "strand-start-wf", nil, "normal")
	require.NoError(t, err)
	require.NoError(t, f.instanceRepo.UpdateCurrentNode(context.Background(), id, "A"))
	require.Equal(t, instance.StateQueued, stateNow(t, f, id))

	adopted := make(chan bool, 1)
	var fired bool
	f.instanceRepo.onUpdateState = func(id uuid.UUID, st instance.State) {
		if st != instance.StateRunning || fired {
			return
		}
		fired = true
		go func() {
			ok, _ := f.engine.AdoptStrandedParent(context.Background(), id, "A")
			adopted <- ok
		}()
		// Give the adopter every chance to land before the walker registers.
		time.Sleep(50 * time.Millisecond)
	}

	require.NoError(t, f.engine.StartInstance(context.Background(), id))

	select {
	case ok := <-adopted:
		assert.False(t, ok, "a run being started must never look stranded")
	case <-time.After(5 * time.Second):
		t.Fatal("adopter never returned")
	}
	close(release)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

// Adoption needs the spawn-wait port; an engine without it must say so rather
// than silently adopt nothing.
func TestAdoptStrandedParent_WithoutSpawnWaits_Errors(t *testing.T) {
	f := newTestFixtures()
	id := seedRunAt(t, f, instance.StateRunning, "spawn-1")

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "spawn-1")
	require.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, instance.StateRunning, stateNow(t, f, id))
}

func TestSetSpawnWaits_PanicsOnNil(t *testing.T) {
	f := newTestFixtures()
	assert.Panics(t, func() { f.engine.SetSpawnWaits(nil) })
}

// A nil current node never matches the SQL predicate, so the fake the adoption
// tests rely on must not match it either.
func TestAdoptStrandedParent_NilCurrentNode_NoOp(t *testing.T) {
	f := newStrandFixtures()
	inst := &instance.Instance{
		ID: uuid.New(), OwnerID: uuid.New(), WorkflowName: "parent",
		State: instance.StateRunning, Priority: instance.PriorityNormal,
	}
	require.NoError(t, f.instanceRepo.Create(context.Background(), inst))

	ok, err := f.engine.AdoptStrandedParent(context.Background(), inst.ID, "spawn-1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, instance.StateRunning, stateNow(t, f, inst.ID))
}

func TestAdoptStrandedParent_MismatchedCurrentNode_NoOp(t *testing.T) {
	f := newStrandFixtures()
	id := seedRunAt(t, f, instance.StateRunning, "spawn-2")

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "spawn-1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, instance.StateRunning, stateNow(t, f, id))
}

// A finishing walker's exit must remove only ITS OWN registration. Here the run
// fails and is retried before the failed walker's exit defers run: the retry
// registers a fresh walker, and the old walker's unconditional delete then
// removed it, so the live retry looked stranded and could be adopted (handed to
// a second walker).
func TestWalkerExit_DoesNotDeregisterSuccessor(t *testing.T) {
	f := newStrandFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name:  "strand-successor-wf",
		Nodes: []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
	})
	var calls int
	retryStarted := make(chan struct{})
	release := make(chan struct{})
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		calls++
		if calls == 1 {
			return nil, assert.AnError
		}
		close(retryStarted)
		<-release
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})
	var retried bool
	f.instanceRepo.onSetFailed = func(id uuid.UUID) {
		if retried {
			return
		}
		retried = true
		require.NoError(t, f.engine.Retry(bgAdmin(), id, nil))
	}

	id, _, err := f.engine.Trigger(context.Background(), "strand-successor-wf", nil, "normal")
	require.NoError(t, err)
	<-retryStarted
	// Best-effort settle so the failed walker's exit has usually run. The
	// ownership invariant itself is pinned deterministically, without any
	// scheduling dependence, by TestWalkerRegistration_* (internal package).
	time.Sleep(50 * time.Millisecond)

	ok, err := f.engine.AdoptStrandedParent(context.Background(), id, "A")
	require.NoError(t, err)
	assert.False(t, ok, "the retry's live walker must still be registered")

	close(release)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted, instance.StateFailed)
	assert.Equal(t, instance.StateCompleted, inst.State)
}
