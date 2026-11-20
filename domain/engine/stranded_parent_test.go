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

// strandLinks is a SpawnLinkRepository whose SuspendStrandedAt is a real
// conditional write over the mock instance repo: running -> waiting, and only
// at nodeID when the row records a current node.
type strandLinks struct {
	engine.NoopSpawnLinks
	repo *mockInstanceRepo
}

func (s strandLinks) SuspendStrandedAt(_ context.Context, id uuid.UUID, nodeID string) (bool, error) {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	inst, ok := s.repo.instances[id]
	if !ok || inst.State != instance.StateRunning {
		return false, nil
	}
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != nodeID {
		return false, nil
	}
	inst.State = instance.StateWaiting
	return true, nil
}

func newStrandFixtures() *testFixtures {
	f := newTestFixtures()
	f.engine.SetSpawnLinks(strandLinks{repo: f.instanceRepo})
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
// are not atomic under the engine lock, a sweep landing between them sees a
// running run with no walker and adopts a run that is merely starting.
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

	id, _, err := f.engine.Trigger(context.Background(), "strand-start-wf", nil, "normal")
	require.NoError(t, err)

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
