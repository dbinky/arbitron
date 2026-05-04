package engine_test

import (
	"context"
	"encoding/json"
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
// Mock implementations
// ---------------------------------------------------------------------------

// mockWorkflowRepo implements workflow.WorkflowRepository
type mockWorkflowRepo struct {
	mu        sync.Mutex
	workflows map[string]*workflow.Workflow
}

func newMockWorkflowRepo() *mockWorkflowRepo {
	return &mockWorkflowRepo{workflows: make(map[string]*workflow.Workflow)}
}

func (r *mockWorkflowRepo) Load(name string) (*workflow.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wf, ok := r.workflows[name]
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", name)
	}
	return wf, nil
}

func (r *mockWorkflowRepo) List() ([]workflow.WorkflowSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var summaries []workflow.WorkflowSummary
	for _, wf := range r.workflows {
		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
		})
	}
	return summaries, nil
}

func (r *mockWorkflowRepo) add(wf *workflow.Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflows[wf.Name] = wf
}

// mockInstanceRepo implements instance.InstanceRepository
type mockInstanceRepo struct {
	mu        sync.Mutex
	instances map[uuid.UUID]*instance.Instance
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockInstanceRepo) Create(inst *instance.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockInstanceRepo) Get(id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance not found: %s", id)
	}
	// Return a copy to avoid data races
	cp := *inst
	if inst.Variables != nil {
		cp.Variables = make(map[string]any, len(inst.Variables))
		for k, v := range inst.Variables {
			cp.Variables[k] = v
		}
	}
	if inst.OriginalVariables != nil {
		cp.OriginalVariables = make(map[string]any, len(inst.OriginalVariables))
		for k, v := range inst.OriginalVariables {
			cp.OriginalVariables[k] = v
		}
	}
	if inst.WorkflowSnapshot != nil {
		cp.WorkflowSnapshot = make(map[string]any, len(inst.WorkflowSnapshot))
		for k, v := range inst.WorkflowSnapshot {
			cp.WorkflowSnapshot[k] = v
		}
	}
	if inst.CurrentNodeID != nil {
		n := *inst.CurrentNodeID
		cp.CurrentNodeID = &n
	}
	if inst.FailedNodeID != nil {
		n := *inst.FailedNodeID
		cp.FailedNodeID = &n
	}
	return &cp, nil
}

func (r *mockInstanceRepo) List(filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var summaries []instance.InstanceSummary
	for _, inst := range r.instances {
		if filters.State != nil && inst.State != *filters.State {
			continue
		}
		summaries = append(summaries, instance.InstanceSummary{
			ID:           inst.ID,
			WorkflowName: inst.WorkflowName,
			State:        inst.State,
			Priority:     inst.Priority,
		})
	}
	return summaries, nil
}

func (r *mockInstanceRepo) UpdateState(id uuid.UUID, state instance.State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = state
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) UpdateVariables(id uuid.UUID, variables map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.Variables = variables
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) UpdateCurrentNode(id uuid.UUID, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.CurrentNodeID = &nodeID
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) SetFailed(id uuid.UUID, failedNodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = instance.StateFailed
	inst.FailedNodeID = &failedNodeID
	now := time.Now()
	inst.CompletedAt = &now
	inst.UpdatedAt = now
	return nil
}

func (r *mockInstanceRepo) SetCompleted(id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = instance.StateCompleted
	now := time.Now()
	inst.CompletedAt = &now
	inst.UpdatedAt = now
	return nil
}

func (r *mockInstanceRepo) ListRunning() ([]instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var running []instance.Instance
	for _, inst := range r.instances {
		if inst.State == instance.StateRunning {
			running = append(running, *inst)
		}
	}
	return running, nil
}

func (r *mockInstanceRepo) get(id uuid.UUID) *instance.Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instances[id]
}

// mockNodeExecutionRepo implements instance.NodeExecutionRepository
type mockNodeExecutionRepo struct {
	mu         sync.Mutex
	executions []*instance.NodeExecution
}

func newMockNodeExecutionRepo() *mockNodeExecutionRepo {
	return &mockNodeExecutionRepo{}
}

func (r *mockNodeExecutionRepo) Create(exec *instance.NodeExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executions = append(r.executions, exec)
	return nil
}

func (r *mockNodeExecutionRepo) Update(exec *instance.NodeExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.executions {
		if e.ID == exec.ID {
			r.executions[i] = exec
			return nil
		}
	}
	return fmt.Errorf("execution not found: %s", exec.ID)
}

func (r *mockNodeExecutionRepo) ListByInstance(instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []instance.NodeExecution
	for _, e := range r.executions {
		if e.InstanceID == instanceID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (r *mockNodeExecutionRepo) GetByInstanceAndNode(instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.executions {
		if e.InstanceID == instanceID && e.NodeID == nodeID {
			return e, nil
		}
	}
	return nil, fmt.Errorf("execution not found for instance %s node %s", instanceID, nodeID)
}

// mockSnapshotRepo implements instance.VariableSnapshotRepository
type mockSnapshotRepo struct {
	mu        sync.Mutex
	snapshots []*instance.VariableSnapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{}
}

func (r *mockSnapshotRepo) Create(snap *instance.VariableSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snap)
	return nil
}

func (r *mockSnapshotRepo) ListByInstance(instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []instance.VariableSnapshot
	for _, s := range r.snapshots {
		if s.InstanceID == instanceID {
			result = append(result, *s)
		}
	}
	return result, nil
}

// mockUserRepo implements instance.UserRepository
type mockUserRepo struct {
	defaultUserID uuid.UUID
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{defaultUserID: uuid.New()}
}

func (r *mockUserRepo) GetDefaultUser() (uuid.UUID, error) {
	return r.defaultUserID, nil
}

// mockEventBus implements engine.EventBus
type mockEventBus struct {
	mu     sync.Mutex
	events map[uuid.UUID][]engine.Event
}

func newMockEventBus() *mockEventBus {
	return &mockEventBus{events: make(map[uuid.UUID][]engine.Event)}
}

func (b *mockEventBus) Publish(instanceID uuid.UUID, event engine.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events[instanceID] = append(b.events[instanceID], event)
}

func (b *mockEventBus) Subscribe(instanceID uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}

func (b *mockEventBus) eventsOfType(instanceID uuid.UUID, eventType string) []engine.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var result []engine.Event
	for _, e := range b.events[instanceID] {
		if e.Type == eventType {
			result = append(result, e)
		}
	}
	return result
}

// mockExecutor implements engine.NodeExecutor with configurable behavior
type mockExecutor struct {
	mu       sync.Mutex
	calls    []mockExecCall
	handlers map[string]func(ctx context.Context, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error)
	// defaultResult is returned when no handler is registered for a node
	defaultResult *engine.NodeResult
	defaultErr    error
}

type mockExecCall struct {
	NodeID       string
	WorkflowName string
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		handlers: make(map[string]func(ctx context.Context, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error)),
		defaultResult: &engine.NodeResult{
			Output: map[string]any{"result": "ok"},
		},
	}
}

func (e *mockExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*engine.NodeResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, mockExecCall{NodeID: node.ID, WorkflowName: workflowName})
	handler, hasHandler := e.handlers[node.ID]
	e.mu.Unlock()

	if hasHandler {
		return handler(ctx, node, vars)
	}
	return e.defaultResult, e.defaultErr
}

func (e *mockExecutor) onNode(nodeID string, handler func(ctx context.Context, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[nodeID] = handler
}

func (e *mockExecutor) getCalls() []mockExecCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]mockExecCall, len(e.calls))
	copy(cp, e.calls)
	return cp
}

// ---------------------------------------------------------------------------
// Helper to build test fixtures
// ---------------------------------------------------------------------------

type testFixtures struct {
	workflowRepo *mockWorkflowRepo
	instanceRepo *mockInstanceRepo
	nodeExecRepo *mockNodeExecutionRepo
	snapshotRepo *mockSnapshotRepo
	userRepo     *mockUserRepo
	eventBus     *mockEventBus
	executor     *mockExecutor
	engine       *engine.Engine
}

func newTestFixtures() *testFixtures {
	wfRepo := newMockWorkflowRepo()
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := newMockUserRepo()
	eventBus := newMockEventBus()
	exec := newMockExecutor()

	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:     exec,
		workflow.NodeTypeScript:      exec,
		workflow.NodeTypeToolCall:    exec,
		workflow.NodeTypeConditional: exec,
		workflow.NodeTypeTransform:   exec,
	}

	eng := engine.NewEngine(wfRepo, instRepo, nodeExecRepo, snapRepo, userRepo, executors, eventBus)

	return &testFixtures{
		workflowRepo: wfRepo,
		instanceRepo: instRepo,
		nodeExecRepo: nodeExecRepo,
		snapshotRepo: snapRepo,
		userRepo:     userRepo,
		eventBus:     eventBus,
		executor:     exec,
		engine:       eng,
	}
}

// waitForInstance waits until the instance reaches one of the given terminal states.
func waitForInstance(t *testing.T, repo *mockInstanceRepo, id uuid.UUID, timeout time.Duration, states ...instance.State) *instance.Instance {
	t.Helper()
	deadline := time.After(timeout)
	stateSet := make(map[instance.State]bool)
	for _, s := range states {
		stateSet[s] = true
	}
	for {
		select {
		case <-deadline:
			inst, _ := repo.Get(id)
			if inst != nil {
				t.Fatalf("timed out waiting for instance %s to reach %v; current state: %s", id, states, inst.State)
			}
			t.Fatalf("timed out waiting for instance %s to reach %v; instance not found", id, states)
		default:
			inst, err := repo.Get(id)
			if err == nil && stateSet[inst.State] {
				return inst
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// linearWorkflow creates a workflow with nodes A→B→C.
func linearWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "linear-wf",
		Description: "A linear workflow",
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
}

// conditionalWorkflow creates a workflow with a conditional branch.
// A (conditional) --high--> B
//                  --low-->  C
func conditionalWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "conditional-wf",
		Description: "A conditional workflow",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeConditional, Entrypoint: true, Config: workflow.NodeConfig{
				Conditions: []workflow.Condition{
					{Field: "score", Operator: "gt", Value: float64(80), Output: "high"},
				},
				DefaultOutput: "low",
			}},
			{ID: "B", Type: workflow.NodeTypeScript},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B", Output: "high"},
			{Source: "A", Target: "C", Output: "low"},
		},
	}
}

// singleNodeWorkflow creates a workflow with a single node.
func singleNodeWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "single-wf",
		Description: "A single node workflow",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
		},
	}
}

// workflowWithInputs creates a workflow with required and optional inputs.
func workflowWithInputs() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "input-wf",
		Description: "A workflow with inputs",
		Inputs: []workflow.InputField{
			{Name: "name", Type: "string", Required: true},
			{Name: "count", Type: "number", Required: false, Default: float64(10)},
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
		},
	}
}

// errorWorkflow creates a workflow where node B will fail.
// Error behavior can be customized per test.
func errorWorkflow(errorBehavior workflow.ErrorBehavior) *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "error-wf",
		Description: "An error workflow",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript, ErrorBehavior: errorBehavior},
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: Graph walker
// ---------------------------------------------------------------------------

func TestEngine_LinearWorkflow(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// All three nodes should have been executed
	calls := f.executor.getCalls()
	assert.Len(t, calls, 3)
	assert.Equal(t, "A", calls[0].NodeID)
	assert.Equal(t, "B", calls[1].NodeID)
	assert.Equal(t, "C", calls[2].NodeID)
}

func TestEngine_ConditionalBranch_High(t *testing.T) {
	f := newTestFixtures()
	wf := conditionalWorkflow()
	f.workflowRepo.add(wf)

	// The conditional executor checks "score > 80", so provide score=90.
	// We register a handler for the conditional node that mimics the real conditional executor.
	f.executor.onNode("A", func(_ context.Context, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		// Evaluate conditions like the real conditional executor would.
		scoreVal, _ := vars.Get("score")
		score, _ := scoreVal.(float64)
		if score > 80 {
			return &engine.NodeResult{OutputPort: "high"}, nil
		}
		return &engine.NodeResult{OutputPort: "low"}, nil
	})

	id, err := f.engine.Trigger("conditional-wf", map[string]any{"score": float64(90)}, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	calls := f.executor.getCalls()
	// Should execute A and B (high branch), not C
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "A")
	assert.Contains(t, nodeIDs, "B")
	assert.NotContains(t, nodeIDs, "C")
}

func TestEngine_ConditionalBranch_DefaultOutput(t *testing.T) {
	f := newTestFixtures()
	wf := conditionalWorkflow()
	f.workflowRepo.add(wf)

	// Score=50 won't match "gt 80", so should take the default "low" branch to C.
	f.executor.onNode("A", func(_ context.Context, node workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		scoreVal, _ := vars.Get("score")
		score, _ := scoreVal.(float64)
		if score > 80 {
			return &engine.NodeResult{OutputPort: "high"}, nil
		}
		return &engine.NodeResult{OutputPort: "low"}, nil
	})

	id, err := f.engine.Trigger("conditional-wf", map[string]any{"score": float64(50)}, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "A")
	assert.Contains(t, nodeIDs, "C")
	assert.NotContains(t, nodeIDs, "B")
}

func TestEngine_SingleNode(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	calls := f.executor.getCalls()
	assert.Len(t, calls, 1)
	assert.Equal(t, "A", calls[0].NodeID)
}

func TestEngine_ErrorHalt(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "halt"})
	f.workflowRepo.add(wf)

	// Node B fails
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "B", *inst.FailedNodeID)

	// C should not have been executed
	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "A")
	assert.Contains(t, nodeIDs, "B")
	assert.NotContains(t, nodeIDs, "C")
}

func TestEngine_ErrorSkip(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{
		Type:          "skip",
		DefaultOutput: "skipped-value",
	})
	f.workflowRepo.add(wf)

	// Node B fails
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// All three nodes should have been called (A executes, B fails then skipped, C executes)
	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "A")
	assert.Contains(t, nodeIDs, "B")
	assert.Contains(t, nodeIDs, "C")
}

func TestEngine_ErrorRoute(t *testing.T) {
	f := newTestFixtures()
	wf := &workflow.Workflow{
		Name:        "error-route-wf",
		Description: "Error routing workflow",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript, ErrorBehavior: workflow.ErrorBehavior{
				Type:        "route",
				ErrorNodeID: "E",
			}},
			{ID: "C", Type: workflow.NodeTypeScript},
			{ID: "E", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
			{Source: "E", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	// Node B fails
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-route-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// Should have executed A, B (failed), E (error route), C
	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "A")
	assert.Contains(t, nodeIDs, "B")
	assert.Contains(t, nodeIDs, "E")
	assert.Contains(t, nodeIDs, "C")
}

func TestEngine_RetrySucceedsOnSecondAttempt(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{
		Type:              "retry",
		Retries:           2,
		RetryDelaySeconds: 0,
	})
	f.workflowRepo.add(wf)

	callCount := 0
	var mu sync.Mutex
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		callCount++
		c := callCount
		mu.Unlock()
		if c == 1 {
			return nil, fmt.Errorf("node B exploded on first attempt")
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// B should have been called twice (first fails, second succeeds)
	calls := f.executor.getCalls()
	bCalls := 0
	for _, c := range calls {
		if c.NodeID == "B" {
			bCalls++
		}
	}
	assert.Equal(t, 2, bCalls)
}

func TestEngine_RetryExhausted(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{
		Type:              "retry",
		Retries:           2,
		RetryDelaySeconds: 0,
	})
	f.workflowRepo.add(wf)

	// Always fails
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B always explodes")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
	require.NotNil(t, inst.FailedNodeID)
	assert.Equal(t, "B", *inst.FailedNodeID)

	// B should have been called 3 times (1 initial + 2 retries)
	calls := f.executor.getCalls()
	bCalls := 0
	for _, c := range calls {
		if c.NodeID == "B" {
			bCalls++
		}
	}
	assert.Equal(t, 3, bCalls)
}

func TestEngine_DefaultErrorBehaviorFromWorkflow(t *testing.T) {
	f := newTestFixtures()
	// Node has no error behavior override → uses workflow default
	wf := &workflow.Workflow{
		Name:        "default-err-wf",
		Description: "Workflow with default error behavior",
		DefaultErrorBehavior: workflow.ErrorBehavior{
			Type:          "skip",
			DefaultOutput: "default-skip-value",
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript}, // No ErrorBehavior set
			{ID: "C", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
			{Source: "B", Target: "C"},
		},
	}
	f.workflowRepo.add(wf)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("default-err-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	// B was skipped, C should still have executed
	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	assert.Contains(t, nodeIDs, "C")
}

// ---------------------------------------------------------------------------
// Tests: Trigger and input validation
// ---------------------------------------------------------------------------

func TestEngine_TriggerWithValidInputs(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(workflowWithInputs())

	id, err := f.engine.Trigger("input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_TriggerWithMissingRequiredInput(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(workflowWithInputs())

	// "name" is required but not provided
	_, err := f.engine.Trigger("input-wf", map[string]any{}, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestEngine_TriggerWithDefaultInput(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(workflowWithInputs())

	id, err := f.engine.Trigger("input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	// The "count" variable should have been set to its default value (10)
	assert.Equal(t, float64(10), inst.Variables["count"])
}

func TestEngine_TriggerWorkflowNotFound(t *testing.T) {
	f := newTestFixtures()
	_, err := f.engine.Trigger("nonexistent", nil, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ---------------------------------------------------------------------------
// Tests: Instance lifecycle
// ---------------------------------------------------------------------------

func TestEngine_PauseRunning(t *testing.T) {
	f := newTestFixtures()

	// Use a workflow with a node that blocks until cancelled
	wf := &workflow.Workflow{
		Name: "pause-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
		},
	}
	f.workflowRepo.add(wf)

	// Block on node B so we can pause while it's running
	bStarted := make(chan struct{})
	bDone := make(chan struct{})
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		close(bStarted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-bDone:
			return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
		}
	})

	id, err := f.engine.Trigger("pause-wf", nil, "normal")
	require.NoError(t, err)

	// Wait for B to start
	<-bStarted

	err = f.engine.Pause(id)
	require.NoError(t, err)

	// Release B so the run loop can check the pause
	close(bDone)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)
	assert.Equal(t, instance.StatePaused, inst.State)
}

func TestEngine_ResumeFromPaused(t *testing.T) {
	f := newTestFixtures()

	wf := &workflow.Workflow{
		Name: "resume-wf",
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

	// Block on node B; use a counter to handle multiple invocations (initial + resume)
	var bCallCount int
	var bMu sync.Mutex
	bStarted := make(chan struct{}, 1)
	bDone := make(chan struct{})
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		bMu.Lock()
		bCallCount++
		call := bCallCount
		bMu.Unlock()

		if call == 1 {
			bStarted <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-bDone:
				return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
			}
		}
		// On resume, just succeed immediately
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, err := f.engine.Trigger("resume-wf", nil, "normal")
	require.NoError(t, err)

	<-bStarted

	err = f.engine.Pause(id)
	require.NoError(t, err)
	close(bDone)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Now resume
	err = f.engine.Resume(id)
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_CancelRunning(t *testing.T) {
	f := newTestFixtures()

	wf := &workflow.Workflow{
		Name: "cancel-wf",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
		},
	}
	f.workflowRepo.add(wf)

	bStarted := make(chan struct{})
	f.executor.onNode("B", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		close(bStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	id, err := f.engine.Trigger("cancel-wf", nil, "normal")
	require.NoError(t, err)

	<-bStarted

	err = f.engine.Cancel(id)
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCancelled)
	assert.Equal(t, instance.StateCancelled, inst.State)
}

func TestEngine_RetryFailed(t *testing.T) {
	f := newTestFixtures()
	wf := linearWorkflow()
	f.workflowRepo.add(wf)

	// B fails on first run
	firstRun := true
	var mu sync.Mutex
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		isFirst := firstRun
		firstRun = false
		mu.Unlock()
		if isFirst {
			return nil, fmt.Errorf("node B exploded")
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Retry the failed instance
	err = f.engine.Retry(id, nil)
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_RerunCompleted(t *testing.T) {
	f := newTestFixtures()
	wf := workflowWithInputs()
	f.workflowRepo.add(wf)

	origID, err := f.engine.Trigger("input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, origID, 5*time.Second, instance.StateCompleted)

	newID, err := f.engine.Rerun(origID)
	require.NoError(t, err)
	assert.NotEqual(t, origID, newID)

	inst := waitForInstance(t, f.instanceRepo, newID, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
	// Should use original_variables from the original instance
	assert.Equal(t, "Alice", inst.Variables["name"])
}

func TestEngine_RecoverCrashedInstances(t *testing.T) {
	f := newTestFixtures()

	// Directly create a "running" instance in the repo (simulating a crash)
	crashedID := uuid.New()
	err := f.instanceRepo.Create(&instance.Instance{
		ID:           crashedID,
		WorkflowName: "some-wf",
		State:        instance.StateRunning,
		Variables:    map[string]any{},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	require.NoError(t, err)

	err = f.engine.RecoverCrashedInstances()
	require.NoError(t, err)

	inst, err := f.instanceRepo.Get(crashedID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, inst.State)
}

// ---------------------------------------------------------------------------
// Tests: Workflow snapshot stored on instance
// ---------------------------------------------------------------------------

func TestEngine_WorkflowSnapshotStored(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	inst, err := f.instanceRepo.Get(id)
	require.NoError(t, err)
	require.NotNil(t, inst.WorkflowSnapshot)

	// The snapshot should be a JSON-serializable representation of the workflow
	snapshotBytes, err := json.Marshal(inst.WorkflowSnapshot)
	require.NoError(t, err)
	assert.Contains(t, string(snapshotBytes), "linear-wf")
}

// ---------------------------------------------------------------------------
// Tests: Variable snapshots created per node
// ---------------------------------------------------------------------------

func TestEngine_VariableSnapshotsCreated(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	snaps, err := f.snapshotRepo.ListByInstance(id)
	require.NoError(t, err)
	// One snapshot per node (A, B, C)
	assert.Len(t, snaps, 3)
}

// ---------------------------------------------------------------------------
// Tests: Node execution records created
// ---------------------------------------------------------------------------

func TestEngine_NodeExecutionRecords(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(id)
	require.NoError(t, err)
	assert.Len(t, execs, 3)
}

func TestEngine_NodeExecutionHasCorrectTiming(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	before := time.Now()
	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	after := time.Now()

	execs, err := f.nodeExecRepo.ListByInstance(id)
	require.NoError(t, err)
	require.Len(t, execs, 1)

	exec := execs[0]
	assert.True(t, exec.StartedAt.After(before) || exec.StartedAt.Equal(before), "StartedAt should be after trigger time")
	require.NotNil(t, exec.CompletedAt, "CompletedAt should be set")
	assert.True(t, exec.CompletedAt.Before(after) || exec.CompletedAt.Equal(after), "CompletedAt should be before now")
	assert.True(t, !exec.CompletedAt.Before(exec.StartedAt), "CompletedAt should not be before StartedAt")
}

func TestEngine_RetryCreatesPerAttemptRecords(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{
		Type:              "retry",
		Retries:           2,
		RetryDelaySeconds: 0,
	})
	f.workflowRepo.add(wf)

	callCount := 0
	var mu sync.Mutex
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		callCount++
		c := callCount
		mu.Unlock()
		if c < 3 {
			return nil, fmt.Errorf("attempt %d failed", c)
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(id)
	require.NoError(t, err)

	// Count records for node B — should be 3 (attempt 1, 2, 3).
	var bExecs []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "B" {
			bExecs = append(bExecs, e)
		}
	}
	require.Len(t, bExecs, 3, "expect one record per attempt")

	// Sort by attempt number to verify ordering.
	for i, e := range bExecs {
		assert.Equal(t, i+1, e.Attempt, "attempt number should match record index")
	}

	// First two attempts should be failed, last should be succeeded.
	assert.Equal(t, instance.NodeStatusFailed, bExecs[0].Status)
	assert.Equal(t, instance.NodeStatusFailed, bExecs[1].Status)
	assert.Equal(t, instance.NodeStatusSucceeded, bExecs[2].Status)
}

func TestEngine_RetryExhaustedAttemptNumbers(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{
		Type:              "retry",
		Retries:           2,
		RetryDelaySeconds: 0,
	})
	f.workflowRepo.add(wf)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("always fails")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	execs, err := f.nodeExecRepo.ListByInstance(id)
	require.NoError(t, err)

	var bExecs []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "B" {
			bExecs = append(bExecs, e)
		}
	}
	require.Len(t, bExecs, 3, "expect three records: initial + 2 retries")

	for i, e := range bExecs {
		assert.Equal(t, i+1, e.Attempt)
		assert.Equal(t, instance.NodeStatusFailed, e.Status)
		assert.NotNil(t, e.CompletedAt)
	}
}

func TestEngine_SkipStatusPersistedAsSkipped(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "skip"})
	f.workflowRepo.add(wf)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(id)
	require.NoError(t, err)

	var bExec *instance.NodeExecution
	for i := range execs {
		if execs[i].NodeID == "B" {
			bExec = &execs[i]
		}
	}
	require.NotNil(t, bExec, "should have a record for node B")
	assert.Equal(t, instance.NodeStatusSkipped, bExec.Status, "skipped node should be recorded as skipped, not failed")
}

// ---------------------------------------------------------------------------
// Tests: Node-level events published to event bus
// ---------------------------------------------------------------------------

func TestEngine_PublishesNodeStartedEvents(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	started := f.eventBus.eventsOfType(id, "node.started")
	// A, B, C should each emit node.started
	assert.Len(t, started, 3)
	nodeIDs := make([]string, 0, 3)
	for _, e := range started {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok, "node.started Data should be a map")
		nodeID, _ := d["nodeId"].(string)
		nodeIDs = append(nodeIDs, nodeID)
	}
	assert.ElementsMatch(t, []string{"A", "B", "C"}, nodeIDs)
}

func TestEngine_PublishesNodeCompletedEvents(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, err := f.engine.Trigger("linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	completed := f.eventBus.eventsOfType(id, "node.completed")
	assert.Len(t, completed, 3)

	for _, e := range completed {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok)
		assert.NotEmpty(t, d["nodeId"])
		assert.NotEmpty(t, d["nodeType"])
	}
}

func TestEngine_PublishesNodeFailedEvent(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "halt"})
	f.workflowRepo.add(wf)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	failed := f.eventBus.eventsOfType(id, "node.failed")
	require.Len(t, failed, 1)
	d, ok := failed[0].Data.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "B", d["nodeId"])
	assert.Contains(t, d["error"], "node B exploded")
}

func TestEngine_PublishesNodeFailedEventOnSkip(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "skip"})
	f.workflowRepo.add(wf)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, err := f.engine.Trigger("error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// node.failed is published even when behavior is skip, because the node DID fail
	failed := f.eventBus.eventsOfType(id, "node.failed")
	require.Len(t, failed, 1)
	assert.Equal(t, "B", failed[0].Data.(map[string]any)["nodeId"])

	// node.started and node.completed for A and C, but NOT node.completed for B (it failed)
	completed := f.eventBus.eventsOfType(id, "node.completed")
	completedIDs := make([]string, 0, len(completed))
	for _, e := range completed {
		completedIDs = append(completedIDs, e.Data.(map[string]any)["nodeId"].(string))
	}
	assert.Contains(t, completedIDs, "A")
	assert.Contains(t, completedIDs, "C")
	assert.NotContains(t, completedIDs, "B")
}

// ---------------------------------------------------------------------------
// Tests: Lifecycle state conflict errors
// ---------------------------------------------------------------------------

func TestEngine_PauseNonRunning(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Pause(id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestEngine_ResumeNonPaused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Resume(id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not paused")
}

func TestEngine_CancelTerminalState(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Cancel(id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be cancelled")
}

func TestEngine_RetryNonFailed(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, err := f.engine.Trigger("single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Retry(id, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not failed")
}

// ---------------------------------------------------------------------------
// Tests: Edge cases
// ---------------------------------------------------------------------------

func TestEngine_UnregisteredNodeType(t *testing.T) {
	f := newTestFixtures()
	wf := &workflow.Workflow{
		Name: "unknown-type-wf",
		Nodes: []workflow.Node{
			// NodeType "foobar" has no registered executor
			{ID: "A", Type: "foobar", Entrypoint: true},
		},
	}
	f.workflowRepo.add(wf)

	// Remove all executors so the unknown type definitely isn't handled
	eng := engine.NewEngine(
		f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo,
		map[workflow.NodeType]engine.NodeExecutor{},
		f.eventBus,
	)

	id, err := eng.Trigger("unknown-type-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
}

func TestEngine_RetryWithVariableEdits(t *testing.T) {
	f := newTestFixtures()
	wf := linearWorkflow()
	f.workflowRepo.add(wf)

	// B reads "name" from variables; on first run name is "wrong", on retry it's "Alice"
	firstRun := true
	var mu sync.Mutex
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		mu.Lock()
		isFirst := firstRun
		firstRun = false
		mu.Unlock()
		if isFirst {
			return nil, fmt.Errorf("node B exploded")
		}
		nameVal, _ := vars.Get("name")
		return &engine.NodeResult{Output: map[string]any{"name": nameVal}}, nil
	})

	id, err := f.engine.Trigger("linear-wf", map[string]any{"name": "wrong"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Retry with corrected variable
	err = f.engine.Retry(id, map[string]any{"name": "Alice"})
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
	// Variable should be updated
	assert.Equal(t, "Alice", inst.Variables["name"])
}
