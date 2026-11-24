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

var errSimulatedCrash = errors.New("simulated process death")

// memRetryStore is the in-memory RetryProgressStore: attempt CAS, failed
// execution written with the progress, exhaustion sticky.
type memRetryStore struct {
	mu        sync.Mutex
	execs     *mockNodeExecutionRepo
	progress  map[uuid.UUID]engine.RetryProgress
	loadedFor []uuid.UUID
}

func newMemRetryStore(execs *mockNodeExecutionRepo) *memRetryStore {
	return &memRetryStore{execs: execs, progress: map[uuid.UUID]engine.RetryProgress{}}
}

func (s *memRetryStore) LoadRetryProgress(_ context.Context, inv uuid.UUID) (engine.RetryProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadedFor = append(s.loadedFor, inv)
	if p, ok := s.progress[inv]; ok {
		return p, nil
	}
	return engine.RetryProgress{InvocationID: inv, Attempt: 1}, nil
}

func (s *memRetryStore) CommitRetryFailure(ctx context.Context, inv uuid.UUID, attempt int, x *instance.NodeExecution, at *time.Time) (engine.RetryProgress, error) {
	s.mu.Lock()
	p, ok := s.progress[inv]
	if !ok {
		p = engine.RetryProgress{InvocationID: inv, Attempt: 1}
	}
	if p.Exhausted || p.Attempt != attempt || x == nil || x.Attempt != attempt {
		s.mu.Unlock()
		return engine.RetryProgress{}, engine.ErrRetryProgressConflict
	}
	s.mu.Unlock()
	if err := s.execs.Create(ctx, x); err != nil {
		return engine.RetryProgress{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at != nil {
		p.Attempt, p.RetryNotBefore = attempt+1, at
	} else {
		p.Exhausted, p.RetryNotBefore = true, nil
	}
	p.LastExecution = x
	s.progress[inv] = p
	return p, nil
}

func (s *memRetryStore) loads() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.loadedFor...)
}

// crashAfterRetryCommit stands in for a process that died right after the
// retry journal committed attempt N's failure.
type crashAfterRetryCommit struct {
	engine.RetryProgressStore
	attempt int
	crashed chan struct{}
	release chan struct{}
	once    sync.Once
}

func crashAfterRetry(t *testing.T, inner engine.RetryProgressStore, attempt int) *crashAfterRetryCommit {
	c := &crashAfterRetryCommit{RetryProgressStore: inner, attempt: attempt, crashed: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(c.release) })
	return c
}

func (c *crashAfterRetryCommit) CommitRetryFailure(ctx context.Context, inv uuid.UUID, attempt int, x *instance.NodeExecution, at *time.Time) (engine.RetryProgress, error) {
	p, err := c.RetryProgressStore.CommitRetryFailure(ctx, inv, attempt, x, at)
	if err != nil || attempt != c.attempt {
		return p, err
	}
	c.once.Do(func() { close(c.crashed) })
	<-c.release
	return p, errSimulatedCrash
}

func (c *crashAfterRetryCommit) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.crashed:
	case <-time.After(10 * time.Second):
		t.Fatal("the retry journal never committed the expected attempt")
	}
}

// restartedEngine is a fresh engine over the same durable stores: the server
// after a restart, with its own executors so calls are attributable.
func restartedEngine(f *testFixtures, executors map[workflow.NodeType]engine.NodeExecutor, invs engine.InvocationStore) *engine.Engine {
	eng := wireModelPins(engine.NewEngine(f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo, executors, f.eventBus, f.queueManager, f.queueRepo, ""))
	eng.SetInvocationStore(invs)
	return eng
}

func TestEngine_RestartResumesLiveSecondAttempt(t *testing.T) {
	ctx := context.Background()
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 1, RetryDelaySeconds: 1}
	f.workflowRepo.add(wf)
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	store := newMemRetryStore(f.nodeExecRepo)
	crash := crashAfterRetry(t, store, 1)
	f.engine.SetRetryProgressStore(crash)
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, errors.New("provider 500")
	})

	id, _, err := f.engine.Trigger(ctx, "single-wf", nil, "normal")
	require.NoError(t, err)
	crash.wait(t)
	inv, err := invs.Begin(ctx, id, "A") // the open visit
	require.NoError(t, err)
	persisted, err := store.LoadRetryProgress(ctx, inv.ID)
	require.NoError(t, err)
	require.Equal(t, 2, persisted.Attempt)
	require.NotNil(t, persisted.RetryNotBefore)

	var mu sync.Mutex
	var seen []engine.StepIdentity
	var startedAt []time.Time
	exec2 := newMockExecutor()
	exec2.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		sid, _ := engine.StepIdentityFromContext(ctx)
		mu.Lock()
		seen, startedAt = append(seen, sid), append(startedAt, time.Now())
		mu.Unlock()
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})
	eng2 := restartedEngine(f, map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec2}, invs)
	eng2.SetRetryProgressStore(store)
	require.NoError(t, eng2.StartInstance(ctx, id))
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1, "the restarted engine runs exactly one attempt")
	assert.Equal(t, 2, seen[0].Attempt, "it resumes attempt 2, never attempt 1")
	assert.Equal(t, inv.ID, seen[0].InvocationID)
	assert.False(t, startedAt[0].Before(persisted.RetryNotBefore.Add(-50*time.Millisecond)), "the remaining retry delay is honoured")
	assert.Len(t, f.executor.getCalls(), 1, "the first engine ran attempt 1 only")
}

func TestEngine_RestartDoesNotResetExhaustedRetryBudget(t *testing.T) {
	ctx := context.Background()
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "retry", Retries: 1}
	f.workflowRepo.add(wf)
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	store := newMemRetryStore(f.nodeExecRepo)
	crash := crashAfterRetry(t, store, 2) // the exhausting failure
	f.engine.SetRetryProgressStore(crash)
	f.executor.onNode("A", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, errors.New("provider 500")
	})

	id, _, err := f.engine.Trigger(ctx, "single-wf", nil, "normal")
	require.NoError(t, err)
	crash.wait(t)

	exec2 := newMockExecutor()
	eng2 := restartedEngine(f, map[workflow.NodeType]engine.NodeExecutor{workflow.NodeTypeScript: exec2}, invs)
	eng2.SetRetryProgressStore(store)
	require.NoError(t, eng2.StartInstance(ctx, id))
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed, instance.StateCompleted)

	require.Equal(t, instance.StateFailed, inst.State, "an exhausted budget replays the halt")
	assert.Equal(t, "A", *inst.FailedNodeID)
	assert.Empty(t, exec2.getCalls(), "no provider call after the budget is exhausted")
	assert.Len(t, f.executor.getCalls(), 2)
	f.nodeExecRepo.mu.Lock()
	defer f.nodeExecRepo.mu.Unlock()
	assert.Len(t, f.nodeExecRepo.executions, 2, "the replay writes no new failed execution")
}

func TestEngine_NewVisitResetsRetryProgress(t *testing.T) {
	ctx := context.Background()
	f := newTestFixtures()
	f.workflowRepo.add(&workflow.Workflow{
		Name: "loop-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true, ErrorBehavior: workflow.ErrorBehavior{Type: "retry", Retries: 1}},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{{Source: "A", Target: "B"}, {Source: "B", Target: "A", Output: "again"}},
	})
	invs := engine.NewMemoryInvocationStore()
	f.engine.SetInvocationStore(invs)
	store := newMemRetryStore(f.nodeExecRepo)
	f.engine.SetRetryProgressStore(store)

	var mu sync.Mutex
	var seen []engine.StepIdentity
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		sid, _ := engine.StepIdentityFromContext(ctx)
		mu.Lock()
		seen = append(seen, sid)
		n := len(seen)
		mu.Unlock()
		if n == 1 {
			return nil, errors.New("transient")
		}
		return &engine.NodeResult{Output: map[string]any{"ok": true}}, nil
	})
	bVisits := 0
	f.executor.onNode("B", func(context.Context, workflow.Node, *variable.VariableStore) (*engine.NodeResult, error) {
		bVisits++
		if bVisits == 1 {
			return &engine.NodeResult{OutputPort: "again"}, nil
		}
		return &engine.NodeResult{OutputPort: "done"}, nil
	})

	id, _, err := f.engine.Trigger(ctx, "loop-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 3)
	assert.Equal(t, []int{1, 2, 1}, []int{seen[0].Attempt, seen[1].Attempt, seen[2].Attempt}, "a back-edge visit starts at attempt 1")
	assert.Equal(t, seen[0].InvocationID, seen[1].InvocationID)
	assert.NotEqual(t, seen[0].InvocationID, seen[2].InvocationID, "a new graph visit gets a new invocation")
	assert.Equal(t, []uuid.UUID{seen[0].InvocationID, seen[2].InvocationID}, store.loads(),
		"the engine loads the retry position of each visit's own invocation")
}
