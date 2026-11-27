// domain/engine/recovery_test.go
package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedPolicy struct {
	mu      sync.Mutex
	actions map[uuid.UUID]engine.RecoveryAction
	errs    map[uuid.UUID]error
	seen    []uuid.UUID
}

func (p *scriptedPolicy) PrepareRun(_ context.Context, inst *instance.Instance) (engine.RecoveryAction, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, inst.ID)
	if err := p.errs[inst.ID]; err != nil {
		return "", err
	}
	if a, ok := p.actions[inst.ID]; ok {
		return a, nil
	}
	return engine.RecoveryResume, nil
}

func runningAt(t *testing.T, f *testFixtures, wf *workflow.Workflow, node string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	n := node
	require.NoError(t, f.instanceRepo.Create(context.Background(), &instance.Instance{
		ID: id, WorkflowName: wf.Name, WorkflowSnapshot: snapshotOf(t, wf), State: instance.StateRunning,
		CurrentNodeID: &n, Variables: map[string]any{}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))
	return id
}

func eventTypes(f *testFixtures, id uuid.UUID) []string {
	f.eventBus.mu.Lock()
	defer f.eventBus.mu.Unlock()
	var out []string
	for _, e := range f.eventBus.events[id] {
		out = append(out, e.Type)
	}
	return out
}

func recoveredData(f *testFixtures, id uuid.UUID) map[string]any {
	f.eventBus.mu.Lock()
	defer f.eventBus.mu.Unlock()
	for _, e := range f.eventBus.events[id] {
		if e.Type == engine.EventTypeInstanceRecovered {
			d, _ := e.Data.(map[string]any)
			return d
		}
	}
	return nil
}

func TestRecoverRunningInstances_ResumesAtTheCurrentStep(t *testing.T) {
	f := newTestFixtures()
	id := runningAt(t, f, linearWorkflow(), "B")

	require.NoError(t, f.engine.RecoverRunningInstances(context.Background(), &scriptedPolicy{}))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	var nodes []string
	for _, c := range f.executor.getCalls() {
		nodes = append(nodes, c.NodeID)
	}
	assert.Equal(t, []string{"B", "C"}, nodes, "completed step A is not repeated; the interrupted step B re-enters")
	d := recoveredData(f, id)
	require.NotNil(t, d)
	assert.Equal(t, "restart", d["reason"])
	assert.Equal(t, string(engine.RecoveryResume), d["action"])
	assert.Equal(t, instance.RecoveryNoticeText, d["notice"])
}

func TestRecoverRunningInstances_ReplayCommittedAlsoReenters(t *testing.T) {
	f := newTestFixtures()
	id := runningAt(t, f, linearWorkflow(), "C")
	pol := &scriptedPolicy{actions: map[uuid.UUID]engine.RecoveryAction{}}
	pol.actions[id] = engine.RecoveryReplayCommitted
	require.NoError(t, f.engine.RecoverRunningInstances(context.Background(), pol))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, string(engine.RecoveryReplayCommitted), recoveredData(f, id)["action"])
}

func TestRecoverRunningInstances_FailActionAndPolicyErrorFailOnlyThatRun(t *testing.T) {
	f := newTestFixtures()
	failed := runningAt(t, f, linearWorkflow(), "B")
	broken := runningAt(t, f, linearWorkflow(), "B")
	healthy := runningAt(t, f, linearWorkflow(), "C")
	pol := &scriptedPolicy{
		actions: map[uuid.UUID]engine.RecoveryAction{failed: engine.RecoveryFail},
		errs:    map[uuid.UUID]error{broken: errors.New("restore failed")},
	}
	require.NoError(t, f.engine.RecoverRunningInstances(context.Background(), pol))

	for _, id := range []uuid.UUID{failed, broken} {
		inst, err := f.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, instance.StateFailed, inst.State, "failed synchronously")
		require.NotNil(t, inst.FailedNodeID)
		assert.Equal(t, "B", *inst.FailedNodeID)
		assert.Equal(t, string(engine.RecoveryFail), recoveredData(f, id)["action"])
	}
	waitForInstance(t, f.instanceRepo, healthy, 5*time.Second, instance.StateCompleted)
	assert.ElementsMatch(t, []uuid.UUID{failed, broken, healthy}, pol.seen)
}

func TestRecoverRunningInstances_NonResumableStepsAndBadSnapshotsFail(t *testing.T) {
	f := newTestFixtures()
	interactive := &workflow.Workflow{Name: "chat-wf", Nodes: []workflow.Node{{ID: "talk", Type: workflow.NodeTypeInteractiveLLM, Entrypoint: true}}}
	group := &workflow.Workflow{Name: "group-wf", Nodes: []workflow.Node{{ID: "fan", Type: workflow.NodeTypeParallelGroup, Entrypoint: true}}}
	spawn := &workflow.Workflow{Name: "spawn-wf", Nodes: []workflow.Node{{ID: "kid", Type: workflow.NodeTypeSpawnWorkflow, Entrypoint: true}}}
	ids := []uuid.UUID{runningAt(t, f, interactive, "talk"), runningAt(t, f, group, "fan"), runningAt(t, f, spawn, "kid")}
	bad := uuid.New()
	require.NoError(t, f.instanceRepo.Create(context.Background(), &instance.Instance{ID: bad, WorkflowName: "x", State: instance.StateRunning,
		Variables: map[string]any{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	pol := &scriptedPolicy{}

	require.NoError(t, f.engine.RecoverRunningInstances(context.Background(), pol))
	for _, id := range append(ids, bad) {
		inst, err := f.instanceRepo.Get(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, instance.StateFailed, inst.State)
		assert.Contains(t, eventTypes(f, id), engine.EventTypeInstanceRecovered)
	}
	assert.Empty(t, pol.seen, "the pool policy is only consulted for runs that will re-enter")
	assert.Empty(t, f.executor.getCalls())
}

func TestRecoverRunningInstances_RequiresAPolicyAndHandlesCorruptPins(t *testing.T) {
	f := newTestFixtures()
	require.ErrorIs(t, f.engine.RecoverRunningInstances(context.Background(), nil), engine.ErrNoRecoveryPolicy)

	corrupt := runningAt(t, f, linearWorkflow(), "B")
	f.instanceRepo.corruptRunning = map[uuid.UUID]bool{corrupt: true}
	require.NoError(t, f.engine.RecoverRunningInstances(context.Background(), &scriptedPolicy{}))
	inst, err := f.instanceRepo.Get(context.Background(), corrupt)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.Contains(t, eventTypes(f, corrupt), engine.EventTypeInstanceFailed)
}
