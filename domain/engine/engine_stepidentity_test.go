package engine_test

import (
	"context"
	"errors"
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

// identityRecorder captures what a dispatched operation would see: the step
// identity and placement sink on the executor's context.
type identityRecorder struct {
	mu    sync.Mutex
	ids   []engine.StepIdentity
	sinks []*engine.PlacementSink
}

func (r *identityRecorder) record(t *testing.T, ctx context.Context) {
	id, ok := engine.StepIdentityFromContext(ctx)
	assert.True(t, ok, "executor ctx must carry a step identity")
	sink, ok := engine.PlacementSinkFromContext(ctx)
	assert.True(t, ok, "executor ctx must carry a placement sink")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	r.sinks = append(r.sinks, sink)
}

func (r *identityRecorder) all() ([]engine.StepIdentity, []*engine.PlacementSink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.StepIdentity(nil), r.ids...), append([]*engine.PlacementSink(nil), r.sinks...)
}

type failingInvocationStore struct {
	engine.InvocationStore
	beginErr  error
	commitErr error
}

func (s failingInvocationStore) Begin(ctx context.Context, run uuid.UUID, node string) (engine.Invocation, error) {
	if s.beginErr != nil {
		return engine.Invocation{}, s.beginErr
	}
	return s.InvocationStore.Begin(ctx, run, node)
}

func (s failingInvocationStore) Commit(ctx context.Context, id uuid.UUID) error {
	if s.commitErr != nil {
		return s.commitErr
	}
	return s.InvocationStore.Commit(ctx, id)
}

func TestEngine_StampsNodeIdentityPerAttemptUnderOneInvocation(t *testing.T) {
	f := newTestFixtures()
	store := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(store)
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 1}
	f.workflowRepo.add(wf)

	rec := &identityRecorder{}
	calls := 0
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		rec.record(t, ctx)
		calls++
		if calls == 1 {
			return nil, errors.New("transient adapter failure")
		}
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	ids, sinks := rec.all()
	require.Len(t, ids, 2)
	for i, sid := range ids {
		assert.True(t, sid.Valid(), "attempt %d identity must be valid", i+1)
		assert.Equal(t, id, sid.RunID)
		assert.Equal(t, "A", sid.NodeID)
		assert.Equal(t, "node", sid.OperationID)
		assert.Equal(t, engine.OperationNode, sid.Kind)
		assert.Equal(t, i+1, sid.Attempt)
	}
	assert.Equal(t, ids[0].InvocationID, ids[1].InvocationID, "retries are attempts of ONE visit")
	assert.NotSame(t, sinks[0], sinks[1], "each attempt gets a fresh placement sink")

	inv, err := store.Get(context.Background(), ids[0].InvocationID)
	require.NoError(t, err)
	assert.NotNil(t, inv.CommittedAt, "the visit is committed once the engine advanced")
}

func TestEngine_BackEdgeRevisitGetsNewInvocation(t *testing.T) {
	f := newTestFixtures()
	store := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(store)
	f.workflowRepo.add(&workflow.Workflow{
		Name: "loop-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeConditional},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "A", Output: "again"},
			{Source: "B", Target: "C", Output: "done"},
		},
	})
	rec := &identityRecorder{}
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		rec.record(t, ctx)
		return &engine.NodeResult{Output: map[string]any{}}, nil
	})
	bCalls := 0
	f.executor.onNode("B", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		bCalls++
		if bCalls == 1 {
			return &engine.NodeResult{OutputPort: "again"}, nil
		}
		return &engine.NodeResult{OutputPort: "done"}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "loop-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	ids, _ := rec.all()
	require.Len(t, ids, 2, "A runs once per loop visit")
	assert.NotEqual(t, ids[0].InvocationID, ids[1].InvocationID, "a back-edge revisit is a new invocation")
	assert.Equal(t, 1, ids[0].Attempt)
	assert.Equal(t, 1, ids[1].Attempt, "attempt resets every visit")
	first, err := store.Get(context.Background(), ids[0].InvocationID)
	require.NoError(t, err)
	second, err := store.Get(context.Background(), ids[1].InvocationID)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Visit)
	assert.Equal(t, 2, second.Visit)
	assert.NotNil(t, first.CommittedAt)
	assert.NotNil(t, second.CommittedAt)
}

func TestEngine_WaitedVisitReusesOpenInvocation(t *testing.T) {
	f := newTestFixtures()
	store := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(store)
	f.workflowRepo.add(singleNodeWorkflow())

	rec := &identityRecorder{}
	calls := 0
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		rec.record(t, ctx)
		calls++
		if calls == 1 {
			runID, _ := engine.InstanceIDFromContext(ctx)
			_ = f.instanceRepo.UpdateState(ctx, runID, instance.StateWaiting)
			return &engine.NodeResult{IsWait: true}, nil
		}
		return &engine.NodeResult{Output: map[string]any{"done": true}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)
	ids, _ := rec.all()
	require.Len(t, ids, 1)
	open, err := store.Get(context.Background(), ids[0].InvocationID)
	require.NoError(t, err)
	assert.Nil(t, open.CommittedAt, "a waiting visit stays open")

	// Re-run from the current node (what a wake or a restart does).
	require.NoError(t, f.engine.StartInstance(context.Background(), id))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	ids, _ = rec.all()
	require.Len(t, ids, 2)
	assert.Equal(t, ids[0].InvocationID, ids[1].InvocationID, "the resumed execution reuses the open invocation")
	done, err := store.Get(context.Background(), ids[0].InvocationID)
	require.NoError(t, err)
	assert.NotNil(t, done.CommittedAt)
}

func TestEngine_InvocationBeginFailureFailsStepBeforeExecution(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetInvocationStore(failingInvocationStore{InvocationStore: engine.NewMemoryInvocationStore(), beginErr: errors.New("disk full")})
	f.workflowRepo.add(singleNodeWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Empty(t, f.executor.getCalls(), "nothing executes without a durable invocation")
}

func TestEngine_InvocationCommitFailureFailsRun(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetInvocationStore(failingInvocationStore{InvocationStore: engine.NewMemoryInvocationStore(), commitErr: errors.New("disk full")})
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	calls := f.executor.getCalls()
	require.Len(t, calls, 1, "the run stops before B: a later visit must never reuse an uncommitted invocation")
	assert.Equal(t, "A", calls[0].NodeID)
}

func TestEngine_NodeExecutionPersistenceFailureStopsBeforeNextNode(t *testing.T) {
	f := newTestFixtures()
	store := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(store)
	f.nodeExecRepo.createErr = errors.New("disk full")
	wf := linearWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 2}
	f.workflowRepo.add(wf)
	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	require.Len(t, f.executor.getCalls(), 1)
	inv, err := store.Begin(context.Background(), id, "A")
	require.NoError(t, err)
	assert.Equal(t, 1, inv.Visit)
	assert.Nil(t, inv.CommittedAt)
}

func TestEngine_SetInvocationStoreNilFallsBackToMemory(t *testing.T) {
	f := newTestFixtures()
	f.engine.SetInvocationStore(nil)
	require.NotNil(t, f.engine.InvocationStore())
	f.workflowRepo.add(singleNodeWorkflow())
	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
}
