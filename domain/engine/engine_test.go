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
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockWorkflowRepo implements workflow.Repository
type mockWorkflowRepo struct {
	mu        sync.Mutex
	workflows map[string]*workflow.Workflow
}

func newMockWorkflowRepo() *mockWorkflowRepo {
	return &mockWorkflowRepo{workflows: make(map[string]*workflow.Workflow)}
}

func (r *mockWorkflowRepo) Load(_ context.Context, _ workflow.Scope, name string) (*workflow.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wf, ok := r.workflows[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return wf, nil
}

func (r *mockWorkflowRepo) List(_ context.Context, _ workflow.Scope) ([]workflow.WorkflowSummary, error) {
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

func (r *mockWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}

func (r *mockWorkflowRepo) Create(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (r *mockWorkflowRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (r *mockWorkflowRepo) Delete(_ context.Context, _ workflow.Scope, _ string) error { return nil }
func (r *mockWorkflowRepo) WriteFile(_ context.Context, _ workflow.Scope, _ string, _ string, _ []byte) error {
	return nil
}
func (r *mockWorkflowRepo) DeleteFile(_ context.Context, _ workflow.Scope, _ string, _ string) error {
	return nil
}
func (r *mockWorkflowRepo) ListFiles(_ context.Context, _ workflow.Scope, _ string) ([]string, error) {
	return nil, nil
}
func (r *mockWorkflowRepo) Exists(_ context.Context, _ workflow.Scope, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.workflows[name]
	return ok, nil
}
func (r *mockWorkflowRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return nil
}

func (r *mockWorkflowRepo) add(wf *workflow.Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflows[wf.Name] = wf
}

// mockInstanceRepo implements instance.InstanceRepository
type mockInstanceRepo struct {
	mu                  sync.Mutex
	instances           map[uuid.UUID]*instance.Instance
	getErr              error
	setWaitingErr       error
	setWebhookFieldsErr error
	// updateStateErr, when set, makes UpdateState fail. Used to exercise the
	// half-suspended failure path.
	updateStateErr error
	// onUpdateState, when set, is invoked from UpdateState AFTER the state
	// mutation is applied. It is a test-only hook (not part of the production
	// InstanceRepository port) used to snapshot cross-mock ordering — e.g. the
	// number of events published at the moment the instance transitions to
	// waiting — so a test can assert event-emission happened at-or-after the
	// transition.
	onUpdateState func(id uuid.UUID, state instance.State)
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockInstanceRepo) Create(_ context.Context, inst *instance.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
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

func (r *mockInstanceRepo) List(_ context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var summaries []instance.InstanceSummary
	for _, inst := range r.instances {
		if filters.State != nil && inst.State != *filters.State {
			continue
		}
		if !listVisible(inst, filters) {
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

// listVisible mirrors the repo's disjunctive visibility filter so engine list
// tests exercise the same owner/public/department logic the SQL implements.
// An admin call arrives with OwnerID=nil, Departments=nil, IncludePublic=false
// (no narrowing) => everything is visible.
func listVisible(inst *instance.Instance, f instance.ListFilters) bool {
	if f.OwnerID == nil && len(f.Departments) == 0 && !f.IncludePublic {
		return true // unconstrained (admin)
	}
	if f.OwnerID != nil && inst.OwnerID == *f.OwnerID {
		return true
	}
	if f.IncludePublic && inst.Visibility == instance.VisibilityPublic {
		return true
	}
	if inst.Visibility == instance.VisibilityDepartment {
		for _, d := range f.Departments {
			if d == inst.Department {
				return true
			}
		}
	}
	return false
}

func (r *mockInstanceRepo) UpdateState(_ context.Context, id uuid.UUID, state instance.State) error {
	r.mu.Lock()
	if r.updateStateErr != nil {
		r.mu.Unlock()
		return r.updateStateErr
	}
	inst, ok := r.instances[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.State = state
	inst.UpdatedAt = time.Now()
	hook := r.onUpdateState
	r.mu.Unlock()
	if hook != nil {
		hook(id, state)
	}
	return nil
}

func (r *mockInstanceRepo) UpdateVariables(_ context.Context, id uuid.UUID, variables map[string]any) error {
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

func (r *mockInstanceRepo) UpdateCurrentNode(_ context.Context, id uuid.UUID, nodeID string) error {
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

func (r *mockInstanceRepo) SetFailed(_ context.Context, id uuid.UUID, failedNodeID string) error {
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

func (r *mockInstanceRepo) SetCompleted(_ context.Context, id uuid.UUID) error {
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

func (r *mockInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
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

func (r *mockInstanceRepo) UpdatePriority(_ context.Context, id uuid.UUID, priority instance.Priority) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.Priority = priority
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) SetWaiting(_ context.Context, id uuid.UUID, waitingUntil *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setWaitingErr != nil {
		return r.setWaitingErr
	}
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	if waitingUntil != nil {
		inst.State = instance.StateWaiting
	}
	inst.WaitingUntil = waitingUntil
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) SetWebhookFields(_ context.Context, id uuid.UUID, token *string, secretHash *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setWebhookFieldsErr != nil {
		return r.setWebhookFieldsErr
	}
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookToken = token
	inst.WebhookSecretHash = secretHash
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) SetWebhookReceived(_ context.Context, id uuid.UUID, receivedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookReceivedAt = &receivedAt
	inst.UpdatedAt = time.Now()
	return nil
}

func (r *mockInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return &instance.WorkflowStats{}, nil
}

func (r *mockInstanceRepo) get(id uuid.UUID) *instance.Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instances[id]
}

// all returns every instance the repo holds. Test-only.
func (r *mockInstanceRepo) all() []*instance.Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*instance.Instance, 0, len(r.instances))
	for _, inst := range r.instances {
		out = append(out, inst)
	}
	return out
}

// mockNodeExecutionRepo implements instance.NodeExecutionRepository
type mockNodeExecutionRepo struct {
	mu         sync.Mutex
	executions []*instance.NodeExecution
	createErr  error
	// onCreate, when set, is invoked from Create AFTER the record is stored.
	// Test-only ordering probe.
	onCreate func(*instance.NodeExecution)
}

func newMockNodeExecutionRepo() *mockNodeExecutionRepo {
	return &mockNodeExecutionRepo{}
}

func (r *mockNodeExecutionRepo) Create(_ context.Context, exec *instance.NodeExecution) error {
	r.mu.Lock()
	if r.createErr != nil {
		r.mu.Unlock()
		return r.createErr
	}
	r.executions = append(r.executions, exec)
	hook := r.onCreate
	r.mu.Unlock()
	if hook != nil {
		hook(exec)
	}
	return nil
}

func (r *mockNodeExecutionRepo) Update(_ context.Context, exec *instance.NodeExecution) error {
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

func (r *mockNodeExecutionRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
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

func (r *mockNodeExecutionRepo) GetByInstanceAndNode(_ context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.executions {
		if e.InstanceID == instanceID && e.NodeID == nodeID {
			return e, nil
		}
	}
	return nil, fmt.Errorf("execution not found for instance %s node %s", instanceID, nodeID)
}

func (r *mockNodeExecutionRepo) AggregateByInstances(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make(map[uuid.UUID]instance.NodeExecutionAggregate, len(ids))
	// latestModel tracks the StartedAt of the execution whose model is
	// currently recorded as LatestModel, mirroring the SQLite impl's
	// "most-recently-started execution that recorded a model" rule.
	latestModelAt := make(map[uuid.UUID]time.Time, len(ids))
	for _, e := range r.executions {
		if _, ok := want[e.InstanceID]; !ok {
			continue
		}
		agg, exists := out[e.InstanceID]
		if !exists {
			agg = instance.NodeExecutionAggregate{InstanceID: e.InstanceID}
		}
		if e.Attempt > agg.MaxAttempt {
			agg.MaxAttempt = e.Attempt
		}
		if e.LLMModel != nil && *e.LLMModel != "" {
			if prev, hasPrev := latestModelAt[e.InstanceID]; !hasPrev || e.StartedAt.After(prev) {
				agg.LatestModel = *e.LLMModel
				latestModelAt[e.InstanceID] = e.StartedAt
			}
		}
		out[e.InstanceID] = agg
	}
	return out, nil
}

// mockSnapshotRepo implements instance.VariableSnapshotRepository
type mockSnapshotRepo struct {
	mu        sync.Mutex
	snapshots []*instance.VariableSnapshot
}

func newMockSnapshotRepo() *mockSnapshotRepo {
	return &mockSnapshotRepo{}
}

func (r *mockSnapshotRepo) Create(_ context.Context, snap *instance.VariableSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, snap)
	return nil
}

func (r *mockSnapshotRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
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
	defaultUserID  uuid.UUID
	defaultUserErr error // if non-nil, GetDefaultUser returns this error
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{defaultUserID: uuid.New()}
}

func (r *mockUserRepo) GetDefaultUser(_ context.Context) (uuid.UUID, error) {
	if r.defaultUserErr != nil {
		return uuid.Nil, r.defaultUserErr
	}
	return r.defaultUserID, nil
}

func (r *mockUserRepo) GetByID(_ context.Context, _ uuid.UUID) (*instance.User, error) {
	return nil, instance.ErrNotFound
}

func (r *mockUserRepo) UsersByID(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]instance.User, error) {
	return map[uuid.UUID]instance.User{}, nil
}

func (r *mockUserRepo) GetBySubject(_ context.Context, _ string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}

func (r *mockUserRepo) GetByEmail(_ context.Context, _ string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}

func (r *mockUserRepo) Create(_ context.Context, _ *instance.User) error {
	return nil
}

func (r *mockUserRepo) UpdateUsername(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}

func (r *mockUserRepo) List(_ context.Context) ([]instance.User, error) { return nil, nil }

func (r *mockUserRepo) UpdateRole(_ context.Context, _ uuid.UUID, _ string) error { return nil }

func (r *mockUserRepo) CountAdmins(_ context.Context) (int, error) { return 0, nil }

func (r *mockUserRepo) ListDepartments(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}

func (r *mockUserRepo) ReplaceDepartments(_ context.Context, _ uuid.UUID, _ []string) error {
	return nil
}

// mockQueueManager implements engine.QueueManager
type mockQueueManager struct {
	mu                    sync.Mutex
	enqueueFn             func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error)
	notifyCapacityFreedCh chan struct{}
	// onNotify, when set, is invoked from NotifyCapacityFreed. Test-only
	// ordering probe.
	onNotify func()
}

func newMockQueueManager() *mockQueueManager {
	return &mockQueueManager{
		notifyCapacityFreedCh: make(chan struct{}, 100),
	}
}

func (m *mockQueueManager) Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
	m.mu.Lock()
	fn := m.enqueueFn
	m.mu.Unlock()
	if fn != nil {
		return fn(ctx, id, ownerID)
	}
	// Default: always start immediately (backwards-compatible for existing tests)
	return true, nil
}

func (m *mockQueueManager) NotifyCapacityFreed(_ context.Context) {
	m.mu.Lock()
	hook := m.onNotify
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	select {
	case m.notifyCapacityFreedCh <- struct{}{}:
	default:
	}
}

// mockQueueRepo implements instance.QueueRepository (minimal mock for engine tests)
type mockQueueRepo struct {
	mu       sync.Mutex
	instRepo *mockInstanceRepo
}

func (r *mockQueueRepo) CountRunning(_ context.Context) (int, error) { return 0, nil }
func (r *mockQueueRepo) CountRunningByOwner(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}
func (r *mockQueueRepo) CountQueued(_ context.Context) (int, error)  { return 0, nil }
func (r *mockQueueRepo) CountWaiting(_ context.Context) (int, error) { return 0, nil }
func (r *mockQueueRepo) NextEligible(_ context.Context, _ int, _ int) (*instance.Instance, error) {
	return nil, nil
}
func (r *mockQueueRepo) SetQueued(_ context.Context, id uuid.UUID, queuedAt time.Time) error {
	if r.instRepo != nil {
		r.instRepo.mu.Lock()
		if inst, ok := r.instRepo.instances[id]; ok {
			inst.State = instance.StateQueued
			inst.QueuedAt = &queuedAt
		}
		r.instRepo.mu.Unlock()
	}
	return nil
}
func (r *mockQueueRepo) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockQueueRepo) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
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

func (b *mockEventBus) SubscribeAll() (<-chan engine.ScopedEvent, func()) {
	ch := make(chan engine.ScopedEvent)
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

// eventCount returns the number of events published for the instance so far,
// in call order. Used to snapshot the ordered event log at a point in time
// (e.g. when the instance transitions to waiting) for cross-mock ordering
// assertions.
func (b *mockEventBus) eventCount(instanceID uuid.UUID) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events[instanceID])
}

// indexOfType returns the index, within the instance's ordered event log, of
// the first event with the given type, or -1 if no such event was published.
func (b *mockEventBus) indexOfType(instanceID uuid.UUID, eventType string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, e := range b.events[instanceID] {
		if e.Type == eventType {
			return i
		}
	}
	return -1
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
	queueManager *mockQueueManager
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
	qm := newMockQueueManager()

	executors := map[workflow.NodeType]engine.NodeExecutor{
		workflow.NodeTypeLLMCall:          exec,
		workflow.NodeTypeScript:           exec,
		workflow.NodeTypeToolCall:         exec,
		workflow.NodeTypeConditional:      exec,
		workflow.NodeTypeTransform:        exec,
		workflow.NodeTypeInteractiveLLM:   exec,
		workflow.NodeTypeInteractiveAgent: exec,
	}

	// The mock queue manager's default Enqueue calls StartInstance directly,
	// which simulates the old behavior of immediately starting.
	eng := engine.NewEngine(wfRepo, instRepo, nodeExecRepo, snapRepo, userRepo, executors, eventBus, qm, &mockQueueRepo{instRepo: instRepo}, "")

	// Wire the mock queue manager to call StartInstance on the engine.
	qm.mu.Lock()
	qm.enqueueFn = func(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	qm.mu.Unlock()

	return &testFixtures{
		workflowRepo: wfRepo,
		instanceRepo: instRepo,
		nodeExecRepo: nodeExecRepo,
		snapshotRepo: snapRepo,
		userRepo:     userRepo,
		eventBus:     eventBus,
		executor:     exec,
		queueManager: qm,
		engine:       eng,
	}
}

// waitForInstance waits until the instance reaches one of the given terminal states.
func waitForInstance(t *testing.T, repo *mockInstanceRepo, id uuid.UUID, timeout time.Duration, states ...instance.State) *instance.Instance {
	t.Helper()
	ctx := context.Background()
	deadline := time.After(timeout)
	stateSet := make(map[instance.State]bool)
	for _, s := range states {
		stateSet[s] = true
	}
	for {
		select {
		case <-deadline:
			inst, _ := repo.Get(ctx, id)
			if inst != nil {
				t.Fatalf("timed out waiting for instance %s to reach %v; current state: %s", id, states, inst.State)
			}
			t.Fatalf("timed out waiting for instance %s to reach %v; instance not found", id, states)
		default:
			inst, err := repo.Get(ctx, id)
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
//
//	--low-->  C
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

// originWF creates a minimal single-node workflow for origin-stamping tests.
func originWF() *workflow.Workflow {
	return &workflow.Workflow{
		Name:  "origin-test-wf",
		Nodes: []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
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

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "conditional-wf", map[string]any{"score": float64(90)}, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "conditional-wf", map[string]any{"score": float64(50)}, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-route-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "default-err-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_TriggerWithMissingRequiredInput(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(workflowWithInputs())

	// "name" is required but not provided
	_, _, err := f.engine.Trigger(context.Background(), "input-wf", map[string]any{}, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestEngine_TriggerWithDefaultInput(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(workflowWithInputs())

	id, _, err := f.engine.Trigger(context.Background(), "input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	// The "count" variable should have been set to its default value (10)
	assert.Equal(t, float64(10), inst.Variables["count"])
}

func TestEngine_TriggerWorkflowNotFound(t *testing.T) {
	f := newTestFixtures()
	_, _, err := f.engine.Trigger(context.Background(), "nonexistent", nil, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// Origin stamping: the engine reads instance.OriginFromContext and stamps
// TriggerSource + ScheduleID on every created instance. Manual triggers default
// to OriginManual; schedule/run-now triggers carry the injected origin.
func TestEngine_Trigger_StampsManualOrigin(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(originWF())
	id, _, err := f.engine.Trigger(context.Background(), "origin-test-wf", nil, "normal")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.OriginManual, inst.TriggerSource, "plain trigger defaults to OriginManual")
	assert.Nil(t, inst.ScheduleID, "manual trigger has no ScheduleID")
}

func TestEngine_Trigger_StampsScheduleOrigin(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(originWF())
	schedID := uuid.New()
	ctx := instance.WithOrigin(context.Background(), instance.Origin{
		Source:     instance.OriginSchedule,
		ScheduleID: &schedID,
	})
	id, _, err := f.engine.TriggerWithDepartment(ctx, "origin-test-wf", nil, "normal", "")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.OriginSchedule, inst.TriggerSource)
	require.NotNil(t, inst.ScheduleID)
	assert.Equal(t, schedID, *inst.ScheduleID)
}

func TestEngine_Trigger_StampsRunNowOrigin(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(originWF())
	schedID := uuid.New()
	ctx := instance.WithOrigin(context.Background(), instance.Origin{
		Source:     instance.OriginRunNow,
		ScheduleID: &schedID,
	})
	id, _, err := f.engine.TriggerWithDepartment(ctx, "origin-test-wf", nil, "normal", "")
	require.NoError(t, err)
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.OriginRunNow, inst.TriggerSource)
	require.NotNil(t, inst.ScheduleID)
	assert.Equal(t, schedID, *inst.ScheduleID)
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

	id, _, err := f.engine.Trigger(context.Background(), "pause-wf", nil, "normal")
	require.NoError(t, err)

	// Wait for B to start
	<-bStarted

	err = f.engine.Pause(bgAdmin(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "resume-wf", nil, "normal")
	require.NoError(t, err)

	<-bStarted

	err = f.engine.Pause(bgAdmin(), id)
	require.NoError(t, err)
	close(bDone)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Now resume
	err = f.engine.Resume(bgAdmin(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "cancel-wf", nil, "normal")
	require.NoError(t, err)

	<-bStarted

	err = f.engine.Cancel(bgAdmin(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Retry the failed instance
	err = f.engine.Retry(bgAdmin(), id, nil)
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_RerunCompleted(t *testing.T) {
	f := newTestFixtures()
	wf := workflowWithInputs()
	f.workflowRepo.add(wf)

	origID, _, err := f.engine.Trigger(context.Background(), "input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, origID, 5*time.Second, instance.StateCompleted)

	newID, err := f.engine.Rerun(bgAdmin(), origID)
	require.NoError(t, err)
	assert.NotEqual(t, origID, newID)

	inst := waitForInstance(t, f.instanceRepo, newID, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
	// Should use original_variables from the original instance
	assert.Equal(t, "Alice", inst.Variables["name"])
}

func TestEngine_RerunNonTerminalFails(t *testing.T) {
	f := newTestFixtures()

	// Use a two-node workflow so pause can be detected between nodes.
	wf := &workflow.Workflow{
		Name: "rerun-nonterminal-wf",
		Inputs: []workflow.InputField{
			{Name: "name", Type: "string", Required: true},
		},
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "B", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "A", Target: "B"},
		},
	}
	f.workflowRepo.add(wf)

	// Block on node B so we can pause while it's running.
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

	id, _, err := f.engine.Trigger(context.Background(), "rerun-nonterminal-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)

	// Wait for B to start, then pause.
	<-bStarted
	require.NoError(t, f.engine.Pause(bgAdmin(), id))
	close(bDone)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StatePaused)

	// Rerun should fail because the instance is paused (non-terminal).
	_, err = f.engine.Rerun(bgAdmin(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be rerun")
}

func TestEngine_RecoverCrashedInstances(t *testing.T) {
	f := newTestFixtures()

	// Directly create a "running" instance in the repo (simulating a crash)
	crashedID := uuid.New()
	err := f.instanceRepo.Create(context.Background(), &instance.Instance{
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

	inst, err := f.instanceRepo.Get(context.Background(), crashedID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateFailed, inst.State)
}

// ---------------------------------------------------------------------------
// Tests: Workflow snapshot stored on instance
// ---------------------------------------------------------------------------

func TestEngine_WorkflowSnapshotStored(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	inst, err := f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, inst.WorkflowSnapshot)

	// The snapshot should be a JSON-serializable representation of the workflow
	snapshotBytes, err := json.Marshal(inst.WorkflowSnapshot)
	require.NoError(t, err)
	assert.Contains(t, string(snapshotBytes), "linear-wf")
}

// TestEngine_WorkflowSnapshotImmutableAfterRePublish pins the FA-31 invariant:
// when the underlying workflow definition changes (e.g. via re-publish), any
// already-triggered instance continues to execute from its frozen snapshot —
// the new definition is only picked up by instances triggered after the change.
func TestEngine_WorkflowSnapshotImmutableAfterRePublish(t *testing.T) {
	f := newTestFixtures()

	// Trigger first instance with description "v1".
	v1 := &workflow.Workflow{
		Name:        "wf",
		Description: "v1",
		Nodes:       []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
	f.workflowRepo.add(v1)
	id1, _, err := f.engine.Trigger(context.Background(), "wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id1, 5*time.Second, instance.StateCompleted)

	// Simulate a re-publish: overwrite the repo entry with a different description.
	v2 := &workflow.Workflow{
		Name:        "wf",
		Description: "v2",
		Nodes:       []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}},
	}
	f.workflowRepo.add(v2)

	// Trigger second instance — it should see v2.
	id2, _, err := f.engine.Trigger(context.Background(), "wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id2, 5*time.Second, instance.StateCompleted)

	inst1, err := f.instanceRepo.Get(context.Background(), id1)
	require.NoError(t, err)
	inst2, err := f.instanceRepo.Get(context.Background(), id2)
	require.NoError(t, err)

	snap1, _ := json.Marshal(inst1.WorkflowSnapshot)
	snap2, _ := json.Marshal(inst2.WorkflowSnapshot)

	assert.Contains(t, string(snap1), "v1",
		"first instance snapshot must be frozen at trigger time — re-publish must not overwrite it")
	assert.NotContains(t, string(snap1), "v2",
		"first instance snapshot must not reflect the post-trigger re-publish")
	assert.Contains(t, string(snap2), "v2",
		"second instance snapshot must reflect the workflow state at its trigger time")
}

// ---------------------------------------------------------------------------
// Tests: Variable snapshots created per node
// ---------------------------------------------------------------------------

func TestEngine_VariableSnapshotsCreated(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	snaps, err := f.snapshotRepo.ListByInstance(context.Background(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	assert.Len(t, execs, 3)
}

func TestEngine_NodeExecutionHasCorrectTiming(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	before := time.Now()
	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	after := time.Now()

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
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

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
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

func TestEngine_NodeStartedEventCarriesLabelAndBackend(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	started := f.eventBus.eventsOfType(id, "node.started")
	require.NotEmpty(t, started)
	for _, e := range started {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok, "node.started Data should be a map")
		nodeID, _ := d["nodeId"].(string)
		label, hasLabel := d["nodeLabel"].(string)
		_, hasBackend := d["backend"]
		assert.True(t, hasLabel, "node.started Data should carry nodeLabel")
		assert.Equal(t, nodeID, label, "linear nodes have no Label, so label == nodeId")
		assert.True(t, hasBackend, "node.started Data should carry a backend key")
	}
}

func TestEngine_NodeFailedEventCarriesLabelAndBackend(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "halt"})
	f.workflowRepo.add(wf)

	// Node B fails
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	failed := f.eventBus.eventsOfType(id, "node.failed")
	require.NotEmpty(t, failed)
	for _, e := range failed {
		d, ok := e.Data.(map[string]any)
		require.True(t, ok, "node.failed Data should be a map")
		nodeID, _ := d["nodeId"].(string)
		label, hasLabel := d["nodeLabel"].(string)
		_, hasBackend := d["backend"]
		_, hasError := d["error"]
		assert.True(t, hasLabel, "node.failed Data should carry nodeLabel")
		assert.NotEmpty(t, label, "node.failed nodeLabel should be non-empty")
		assert.True(t, hasBackend, "node.failed Data should carry a backend key")
		assert.True(t, hasError, "node.failed Data should carry an error key")
		assert.Equal(t, "B", nodeID, "failed node should be B")
	}
}

func TestEngine_PublishesNodeCompletedEvents(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
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

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Pause(bgAdmin(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not running")
}

func TestEngine_ResumeNonPaused(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Resume(bgAdmin(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not paused")
}

func TestEngine_CancelTerminalState(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Cancel(bgAdmin(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be cancelled")
}

func TestEngine_RetryNonFailed(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	err = f.engine.Retry(bgAdmin(), id, nil)
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
	qm2 := newMockQueueManager()
	eng := engine.NewEngine(
		f.workflowRepo, f.instanceRepo, f.nodeExecRepo, f.snapshotRepo, f.userRepo,
		map[workflow.NodeType]engine.NodeExecutor{},
		f.eventBus,
		qm2,
		&mockQueueRepo{instRepo: f.instanceRepo},
		"",
	)
	qm2.mu.Lock()
	qm2.enqueueFn = func(ctx context.Context, id uuid.UUID, _ uuid.UUID) (bool, error) {
		if err := eng.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	qm2.mu.Unlock()

	id, _, err := eng.Trigger(context.Background(), "unknown-type-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
}

func TestEngine_NodeOutputAccessibleByLaterNode(t *testing.T) {
	f := newTestFixtures()
	wf := linearWorkflow() // A → B → C
	f.workflowRepo.add(wf)

	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{Output: map[string]any{"result": "hello"}}, nil
	})

	var aResultInB any
	var aResultFound bool
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		aResultInB, aResultFound = vars.Get("A.result")
		return &engine.NodeResult{Output: map[string]any{"result": "world"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.True(t, aResultFound, "A.result should be accessible from node B's variable store")
	assert.Equal(t, "hello", aResultInB)
}

func TestEngine_RetryPreservesUneditedNodeOutputs(t *testing.T) {
	f := newTestFixtures()
	wf := linearWorkflow() // A → B → C
	f.workflowRepo.add(wf)

	var once sync.Once
	var bRunCount int
	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		var err error
		once.Do(func() {
			bRunCount++
			err = fmt.Errorf("B exploded on first run")
		})
		if err != nil {
			return nil, err
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Retry with only "name" changed.
	err = f.engine.Retry(bgAdmin(), id, map[string]any{"name": "Bob"})
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Edited variable should be updated.
	assert.Equal(t, "Bob", inst.Variables["name"])
	// Node A's output should be preserved (A does not re-run on retry).
	aOutput, ok := inst.Variables["A"]
	assert.True(t, ok, "node A output key should still be in variables after retry")
	if aOut, isMap := aOutput.(map[string]any); isMap {
		assert.Equal(t, "ok", aOut["result"])
	}
}

func TestEngine_UpdateCurrentNodeFiresBeforeExecution(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	var currentNodeIDAtExecTime *string
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		// At this point the engine should have already called UpdateCurrentNode("A").
		running, _ := f.instanceRepo.ListRunning(context.Background())
		if len(running) == 1 {
			cid := running[0].CurrentNodeID
			currentNodeIDAtExecTime = cid
		}
		return &engine.NodeResult{Output: map[string]any{"result": "ok"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	require.NotNil(t, currentNodeIDAtExecTime, "CurrentNodeID should be set before executor runs")
	assert.Equal(t, "A", *currentNodeIDAtExecTime)
}

func TestEngine_VariableSnapshotCapturedPreExecution(t *testing.T) {
	f := newTestFixtures()
	wf := workflowWithInputs() // nodes: A; inputs: name(required), count(default 10)
	f.workflowRepo.add(wf)

	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{Output: map[string]any{"transformed": "yes"}}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "input-wf", map[string]any{"name": "Alice"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	snaps, err := f.snapshotRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, snaps, 1, "one snapshot per node execution")

	snap := snaps[0]
	assert.Equal(t, "A", snap.NodeID)

	// The snapshot captures pre-execution variables: inputs are present.
	assert.Equal(t, "Alice", snap.Variables["name"])
	// The snapshot does NOT yet contain node "A"s output (that's written after execution).
	assert.Nil(t, snap.Variables["A"], "snapshot should not contain node A output (captured pre-execution)")
}

func TestEngine_NodeResultTelemetryFieldsPersistedToNodeExecution(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	model := "claude-sonnet-4-6"
	tokIn := 500
	tokOut := 200
	stdout := "node stdout"
	stderr := "node stderr"
	exitCode := 0

	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{
			Output:         map[string]any{"ok": true},
			LLMModel:       model,
			LLMTokensIn:    tokIn,
			LLMTokensOut:   tokOut,
			ScriptStdout:   stdout,
			ScriptStderr:   stderr,
			ScriptExitCode: exitCode,
		}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, execs, 1)

	exec := execs[0]
	require.NotNil(t, exec.LLMModel)
	assert.Equal(t, model, *exec.LLMModel)
	require.NotNil(t, exec.LLMTokensIn)
	assert.Equal(t, tokIn, *exec.LLMTokensIn)
	require.NotNil(t, exec.LLMTokensOut)
	assert.Equal(t, tokOut, *exec.LLMTokensOut)
	require.NotNil(t, exec.ScriptStdout)
	assert.Equal(t, stdout, *exec.ScriptStdout)
	require.NotNil(t, exec.ScriptStderr)
	assert.Equal(t, stderr, *exec.ScriptStderr)
	// ScriptExitCode=0 is the zero value, so the engine does NOT set it (the condition is != 0)
	assert.Nil(t, exec.ScriptExitCode)
}

func TestEngine_NonZeroScriptExitCodePersisted(t *testing.T) {
	// ScriptExitCode is only written when != 0; verify a non-zero code is stored.
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "skip"}
	f.workflowRepo.add(wf)

	exitCode := 2
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{
			ScriptExitCode: exitCode,
		}, fmt.Errorf("exit status 2")
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	// Skip error behavior makes the instance complete even if the node fails
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.NotEmpty(t, execs)

	// Find the skipped execution
	var skipped *instance.NodeExecution
	for i := range execs {
		if execs[i].NodeID == "A" {
			skipped = &execs[i]
			break
		}
	}
	require.NotNil(t, skipped)
	require.NotNil(t, skipped.ScriptExitCode)
	assert.Equal(t, exitCode, *skipped.ScriptExitCode)
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

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", map[string]any{"name": "wrong"}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	// Retry with corrected variable
	err = f.engine.Retry(bgAdmin(), id, map[string]any{"name": "Alice"})
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
	// Variable should be updated
	assert.Equal(t, "Alice", inst.Variables["name"])
}

func TestEngine_WorkflowNamePassedToExecutor(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	calls := f.executor.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "single-wf", calls[0].WorkflowName, "executor must receive the workflow name, not empty string")
}

func TestEngine_LLMConversationPersistedToNodeExecution(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	conversation := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "hi"},
		},
	}

	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return &engine.NodeResult{
			Output:          map[string]any{"ok": true},
			LLMConversation: conversation,
		}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, execs, 1)

	assert.NotNil(t, execs[0].LLMConversation, "LLMConversation must be persisted to NodeExecution")
	msgs, ok := execs[0].LLMConversation["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, msgs, 2)
}

func TestEngine_RerunNilOriginalVariablesFails(t *testing.T) {
	f := newTestFixtures()
	wf := singleNodeWorkflow()
	f.workflowRepo.add(wf)

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", map[string]any{"x": 1}, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	// Manually nil out OriginalVariables to simulate corrupt/old DB row.
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id].OriginalVariables = nil
	f.instanceRepo.mu.Unlock()

	_, err = f.engine.Rerun(bgAdmin(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no original variables recorded")
}

func TestEngine_SkipConditionalNodeRoutesViaDefaultOutput(t *testing.T) {
	// When a conditional node fails with skip behavior, the engine must route
	// via the node's Config.DefaultOutput port (not an arbitrary first edge).
	f := newTestFixtures()

	wf := &workflow.Workflow{
		Name: "cond-skip-wf",
		Nodes: []workflow.Node{
			{
				ID:            "gate",
				Type:          workflow.NodeTypeConditional,
				Entrypoint:    true,
				ErrorBehavior: workflow.ErrorBehavior{Type: "skip"},
				Config: workflow.NodeConfig{
					Conditions:    []workflow.Condition{{Field: "x", Operator: "eq", Value: 1, Output: "branch-a"}},
					DefaultOutput: "branch-default",
				},
			},
			{ID: "good", Type: workflow.NodeTypeScript},
			{ID: "bad", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "gate", Target: "good", Output: "branch-default"},
			{Source: "gate", Target: "bad", Output: "branch-a"},
		},
	}
	f.workflowRepo.add(wf)

	// Force the conditional node to error (executor returns error).
	f.executor.onNode("gate", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("conditional eval failed")
	})

	id, _, err := f.engine.Trigger(context.Background(), "cond-skip-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	calls := f.executor.getCalls()
	nodeIDs := make([]string, len(calls))
	for i, c := range calls {
		nodeIDs[i] = c.NodeID
	}
	// "gate" ran (and failed), then "good" ran via branch-default — "bad" must NOT run.
	assert.Contains(t, nodeIDs, "gate")
	assert.Contains(t, nodeIDs, "good")
	assert.NotContains(t, nodeIDs, "bad", "skip on conditional must route through DefaultOutput, not arbitrary first edge")
}

// ---------------------------------------------------------------------------
// Tests: Queue integration — Trigger, WaitResult, waiting/queued lifecycle
// ---------------------------------------------------------------------------

func TestEngine_Trigger_ReturnsQueuedWhenNotStarted(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())
	f.queueManager.mu.Lock()
	f.queueManager.enqueueFn = func(_ context.Context, _ uuid.UUID, _ uuid.UUID) (bool, error) {
		return false, nil // simulate staying in queue
	}
	f.queueManager.mu.Unlock()

	id, started, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
	assert.False(t, started, "should return false when queue manager does not start immediately")

	inst, err := f.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, inst.State)
}

func TestEngine_WaitResult_ExitsLoopCleanlyNoExtraRecord(t *testing.T) {
	// When an executor returns IsWait=true, the run loop exits cleanly,
	// the instance stays in waiting state, and executeWithRetry must NOT
	// write a duplicate node execution record (ENGINE-003 fix).
	f := newTestFixtures()
	f.workflowRepo.add(singleNodeWorkflow())

	var executorRecordID uuid.UUID
	f.executor.onNode("A", func(ctx context.Context, node workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		id, _ := engine.InstanceIDFromContext(ctx)
		rec := &instance.NodeExecution{
			ID:         uuid.New(),
			InstanceID: id,
			NodeID:     node.ID,
			Attempt:    1,
			Status:     instance.NodeStatusSucceeded,
		}
		_ = f.nodeExecRepo.Create(ctx, rec)
		executorRecordID = rec.ID
		_ = f.instanceRepo.UpdateState(ctx, id, instance.StateWaiting)
		return &engine.NodeResult{IsWait: true}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "single-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateWaiting)

	execs, err := f.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, execs, 1, "executeWithRetry must not write a duplicate record when IsWait=true")
	assert.Equal(t, executorRecordID, execs[0].ID, "the only record should be the one written by the executor")
}

func TestEngine_PauseWaiting_SetsStateToPaused(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	f.engine.SetScheduler(scheduler)

	id := uuid.New()
	now := time.Now()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:        id,
		State:     instance.StateWaiting,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Pause(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StatePaused, inst.State)

	scheduler.mu.Lock()
	cancelled := scheduler.cancelCalls
	scheduler.mu.Unlock()
	assert.Empty(t, cancelled, "timer must not be cancelled when pausing from waiting state")
}

func TestEngine_ResumeFromWaiting_TimerExpired_Enqueues(t *testing.T) {
	f := newTestFixtures()

	id := uuid.New()
	past := time.Now().Add(-time.Minute)
	now := time.Now()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StatePaused,
		WaitingUntil: &past,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateQueued, inst.State)
	assert.NotNil(t, inst.QueuedAt, "QueuedAt should be set when re-enqueuing after timer expiry")
	assert.Nil(t, inst.WaitingUntil, "WaitingUntil should be cleared after timer expiry resume")
}

func TestEngine_ResumeFromWaiting_TimerNotExpired_RestoresWaiting(t *testing.T) {
	f := newTestFixtures()

	id := uuid.New()
	future := time.Now().Add(time.Hour)
	now := time.Now()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StatePaused,
		WaitingUntil: &future,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestEngine_ResumeFromWebhookWaiting_WebhookReceived_Enqueues(t *testing.T) {
	f := newTestFixtures()
	token := "test-token-received"
	now := time.Now()
	receivedAt := now.Add(-time.Second)

	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:                id,
		State:             instance.StatePaused,
		WebhookToken:      &token,
		WebhookReceivedAt: &receivedAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateQueued, inst.State)
	assert.NotNil(t, inst.QueuedAt)
}

func TestEngine_ResumeFromWebhookWaiting_NotReceived_RestoresWaiting(t *testing.T) {
	f := newTestFixtures()
	token := "test-token-pending"
	now := time.Now()

	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StatePaused,
		WebhookToken: &token,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestEngine_ResumeFromWebhookWaiting_TimeoutExpiredWhilePaused_FailsInstance(t *testing.T) {
	// ENGINE-005: when a webhook-wait instance is paused and the timer fires
	// while paused (OnTimerFired no-ops because state != StateWaiting), the timer
	// entry is removed from the scheduler. On Resume, if the timeout has passed
	// and no webhook was received, the instance must be failed — not returned to
	// StateWaiting with no active timer (which would leave it stuck).
	f := newTestFixtures()
	registry := &mockWebhookRegistrar{}
	f.engine.SetWebhookRegistry(registry)

	token := "expired-timeout-token"
	past := time.Now().Add(-time.Minute)
	now := time.Now()
	nodeID := "webhook-node"
	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:            id,
		State:         instance.StatePaused,
		WebhookToken:  &token,
		WaitingUntil:  &past,
		CurrentNodeID: &nodeID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateFailed, inst.State, "instance must be failed when webhook timeout expired while paused")
	require.NotNil(t, inst.FailedNodeID, "FailedNodeID must be set")
	assert.Equal(t, nodeID, *inst.FailedNodeID, "FailedNodeID must be set to the webhook node")

	registry.mu.Lock()
	deactivated := registry.deactivateCalls
	registry.mu.Unlock()
	assert.Contains(t, deactivated, token, "webhook token must be deactivated when failing on resume")
}

func TestEngine_ResumeFromWebhookWaiting_TimeoutNotExpired_RestoresWaiting(t *testing.T) {
	// When the webhook timeout has not yet expired and no webhook was received,
	// Resume should return the instance to StateWaiting (timer still running).
	f := newTestFixtures()

	token := "pending-timeout-token"
	future := time.Now().Add(time.Hour)
	now := time.Now()
	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StatePaused,
		WebhookToken: &token,
		WaitingUntil: &future,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Resume(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

func TestEngine_CancelWaiting_DeactivatesWebhookCancelsTimer(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	f.engine.SetScheduler(scheduler)
	f.engine.SetWebhookRegistry(registry)

	token := "cancel-waiting-token"
	now := time.Now()
	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StateWaiting,
		WebhookToken: &token,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)

	registry.mu.Lock()
	deactivated := registry.deactivateCalls
	registry.mu.Unlock()
	assert.Contains(t, deactivated, token, "webhook token must be deactivated on cancel")

	scheduler.mu.Lock()
	cancelled := scheduler.cancelCalls
	scheduler.mu.Unlock()
	assert.Contains(t, cancelled, id, "timer must be cancelled on cancel")
}

func TestEngine_CancelQueued_TransitionsToCancelled(t *testing.T) {
	f := newTestFixtures()

	id := uuid.New()
	now := time.Now()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:        id,
		State:     instance.StateQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)
}

func TestEngine_CancelPaused_WithWebhookToken_DeactivatesWebhookCancelsTimer(t *testing.T) {
	f := newTestFixtures()
	scheduler := &mockTimerScheduler{}
	registry := &mockWebhookRegistrar{}
	f.engine.SetScheduler(scheduler)
	f.engine.SetWebhookRegistry(registry)

	token := "paused-cancel-token"
	now := time.Now()
	id := uuid.New()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StatePaused,
		WebhookToken: &token,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.instanceRepo.mu.Unlock()

	err := f.engine.Cancel(bgAdmin(), id)
	require.NoError(t, err)

	inst := f.instanceRepo.get(id)
	assert.Equal(t, instance.StateCancelled, inst.State)

	registry.mu.Lock()
	deactivated := registry.deactivateCalls
	registry.mu.Unlock()
	assert.Contains(t, deactivated, token, "webhook token must be deactivated when cancelling paused instance")

	scheduler.mu.Lock()
	cancelled := scheduler.cancelCalls
	scheduler.mu.Unlock()
	assert.Contains(t, cancelled, id, "timer must be cancelled when cancelling paused webhook-wait instance")
}

// TestEngine_RunLoop_ParseFailure_NotifiesCapacity verifies ENGINE-006: when the
// run() loop fails early (e.g., corrupted WorkflowSnapshot), NotifyCapacityFreed
// is still called so waiting instances are not blocked.
func TestEngine_RunLoop_ParseFailure_NotifiesCapacity(t *testing.T) {
	f := newTestFixtures()

	// Pre-seed the instance with a snapshot that cannot be parsed as a Workflow.
	id := uuid.New()
	now := time.Now()
	f.instanceRepo.mu.Lock()
	f.instanceRepo.instances[id] = &instance.Instance{
		ID:               id,
		State:            instance.StateRunning,
		WorkflowSnapshot: map[string]any{"nodes": "not-an-array"},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	f.instanceRepo.mu.Unlock()

	// Drain any existing capacity notifications.
	for len(f.queueManager.notifyCapacityFreedCh) > 0 {
		<-f.queueManager.notifyCapacityFreedCh
	}

	err := f.engine.StartInstance(context.Background(), id)
	require.NoError(t, err) // StartInstance succeeds; the run loop handles the failure

	// Wait for the instance to be marked failed.
	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)

	// NotifyCapacityFreed must be called so queued instances are not stuck.
	select {
	case <-f.queueManager.notifyCapacityFreedCh:
		// success
	case <-time.After(time.Second):
		t.Fatal("expected NotifyCapacityFreed after run-loop parse failure")
	}
}

// ---------------------------------------------------------------------------
// Mock notification repository
// ---------------------------------------------------------------------------

type mockNotificationRepo struct {
	mu        sync.Mutex
	created   []notification.Notification
	createErr error
}

func newMockNotificationRepo() *mockNotificationRepo {
	return &mockNotificationRepo{}
}

func (r *mockNotificationRepo) Create(_ context.Context, n *notification.Notification) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, *n)
	return nil
}

func (r *mockNotificationRepo) List(_ context.Context, _ notification.ListFilters) ([]notification.Notification, error) {
	return nil, nil
}

func (r *mockNotificationRepo) CountUnread(_ context.Context, _ *uuid.UUID) (int, error) {
	return 0, nil
}

func (r *mockNotificationRepo) MarkAllRead(_ context.Context, _ *uuid.UUID) error { return nil }

func (r *mockNotificationRepo) Dismiss(_ context.Context, _ uuid.UUID, _ *uuid.UUID) error {
	return nil
}

func (r *mockNotificationRepo) getCreated() []notification.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]notification.Notification, len(r.created))
	copy(cp, r.created)
	return cp
}

// waitForNotification polls until at least one notification of the given type is created
// or the timeout expires.
func waitForNotification(t *testing.T, repo *mockNotificationRepo, notifType notification.Type, timeout time.Duration) notification.Notification {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for notification of type %q", notifType)
		default:
			for _, n := range repo.getCreated() {
				if n.Type == notifType {
					return n
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Engine notification wiring
// ---------------------------------------------------------------------------

func TestEngine_Notifications_CompletedWorkflowCreatesNotification(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	n := waitForNotification(t, notifRepo, notification.TypeCompleted, 2*time.Second)
	assert.Equal(t, id, n.InstanceID)
	assert.Equal(t, "linear-wf", n.WorkflowName, "notification must carry the workflow name")
	assert.Equal(t, notification.TypeCompleted, n.Type)
	assert.NotEqual(t, uuid.Nil, n.ID)
}

func TestEngine_Notifications_FailedWorkflowCreatesNotification(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "halt"})
	f.workflowRepo.add(wf)
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	n := waitForNotification(t, notifRepo, notification.TypeFailed, 2*time.Second)
	assert.Equal(t, id, n.InstanceID)
	assert.Equal(t, "error-wf", n.WorkflowName, "notification must carry the workflow name")
	assert.Equal(t, notification.TypeFailed, n.Type)
}

func TestEngine_Notifications_NoNotificationWhenRepoNotSet(t *testing.T) {
	// Without SetNotifications the engine must not panic and must still complete the instance.
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_Notifications_RepoErrorIsLoggedNotPropagated(t *testing.T) {
	// A notification repo failure must not affect instance state.
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	notifRepo.createErr = fmt.Errorf("db timeout")
	f.engine.SetNotifications(notifRepo)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State, "instance must still be completed even when notification fails")
}

// ---------------------------------------------------------------------------
// Mock external notifier
// ---------------------------------------------------------------------------

type mockExternalNotifier struct {
	mu       sync.Mutex
	called   []notification.Notification
	notifyFn func(ctx context.Context, n *notification.Notification) error
}

func newMockExternalNotifier() *mockExternalNotifier {
	return &mockExternalNotifier{}
}

func (m *mockExternalNotifier) Notify(ctx context.Context, n *notification.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = append(m.called, *n)
	if m.notifyFn != nil {
		return m.notifyFn(ctx, n)
	}
	return nil
}

func (m *mockExternalNotifier) getCalled() []notification.Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]notification.Notification, len(m.called))
	copy(cp, m.called)
	return cp
}

// waitForExternalNotification polls until at least one external notification
// of the given type is recorded or the timeout expires.
func waitForExternalNotification(t *testing.T, ext *mockExternalNotifier, notifType notification.Type, timeout time.Duration) notification.Notification {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for external notification of type %q", notifType)
		default:
			for _, n := range ext.getCalled() {
				if n.Type == notifType {
					return n
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Engine external notifier wiring
// ---------------------------------------------------------------------------

func TestEngine_ExternalNotifier_CalledOnCompletion(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	ext := newMockExternalNotifier()
	f.engine.SetExternalNotifier(ext)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	n := waitForExternalNotification(t, ext, notification.TypeCompleted, 2*time.Second)
	assert.Equal(t, id, n.InstanceID)
	assert.Equal(t, "linear-wf", n.WorkflowName)
	assert.Equal(t, notification.TypeCompleted, n.Type)
	assert.NotEqual(t, uuid.Nil, n.ID)
}

func TestEngine_ExternalNotifier_CalledOnFailure(t *testing.T) {
	f := newTestFixtures()
	wf := errorWorkflow(workflow.ErrorBehavior{Type: "halt"})
	f.workflowRepo.add(wf)
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	ext := newMockExternalNotifier()
	f.engine.SetExternalNotifier(ext)

	f.executor.onNode("B", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("node B exploded")
	})

	id, _, err := f.engine.Trigger(context.Background(), "error-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	n := waitForExternalNotification(t, ext, notification.TypeFailed, 2*time.Second)
	assert.Equal(t, id, n.InstanceID)
	assert.Equal(t, "error-wf", n.WorkflowName)
	assert.Equal(t, notification.TypeFailed, n.Type)
}

func TestEngine_ExternalNotifier_NilDoesNotPanic(t *testing.T) {
	// Without SetExternalNotifier the engine must not panic.
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)
}

func TestEngine_ExternalNotifier_ErrorDoesNotAffectInstance(t *testing.T) {
	// An external notifier error must not affect instance state.
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	ext := newMockExternalNotifier()
	ext.notifyFn = func(_ context.Context, _ *notification.Notification) error {
		return fmt.Errorf("external service down")
	}
	f.engine.SetExternalNotifier(ext)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State, "instance must still complete even when external notifier fails")
}

func TestEngine_ExternalNotifier_NotificationHasNonZeroCreatedAt(t *testing.T) {
	// Boundary: emitNotification must set CreatedAt on the notification before
	// forwarding it to the external notifier. A zero CreatedAt produces a
	// "0001-01-01" timestamp in external systems and event payloads.
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	notifRepo := newMockNotificationRepo()
	f.engine.SetNotifications(notifRepo)

	ext := newMockExternalNotifier()
	f.engine.SetExternalNotifier(ext)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	n := waitForExternalNotification(t, ext, notification.TypeCompleted, 2*time.Second)
	assert.False(t, n.CreatedAt.IsZero(), "notification.CreatedAt must not be zero when forwarded to external notifier")
}

// ---------------------------------------------------------------------------
// Tests: Workspace lifecycle
// ---------------------------------------------------------------------------

// fakeWorkspaceManager records workspace lifecycle calls.
type fakeWorkspaceManager struct {
	mu        sync.Mutex
	ensured   []uuid.UUID
	removed   []uuid.UUID
	ensureErr error
	// statsFn overrides the default (0, 0, nil) InstanceDirStats response
	// so tests can simulate a retain-on-failure workspace with content.
	statsFn func(uuid.UUID) (int64, int, error)
}

func (f *fakeWorkspaceManager) EnsureInstanceDir(id uuid.UUID) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensured = append(f.ensured, id)
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	return "/tmp/arbitron-test-ws/" + id.String(), nil
}

func (f *fakeWorkspaceManager) RemoveInstanceDir(id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

// InstanceDirStats returns (0, 0, nil) for the fake — the engine's retain-
// on-failure warning then no-ops because there's "nothing to retain". Tests
// that want to assert the warning fires can override this via the
// statsFn field below.
func (f *fakeWorkspaceManager) InstanceDirStats(id uuid.UUID) (int64, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.statsFn != nil {
		return f.statsFn(id)
	}
	return 0, 0, nil
}

func (f *fakeWorkspaceManager) ensuredIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]uuid.UUID, len(f.ensured))
	copy(cp, f.ensured)
	return cp
}

func (f *fakeWorkspaceManager) removedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]uuid.UUID, len(f.removed))
	copy(cp, f.removed)
	return cp
}

func TestEngine_WorkspaceLifecycle_RemovedOnSuccess(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)
	assert.Equal(t, instance.StateCompleted, inst.State)

	assert.Equal(t, []uuid.UUID{id}, fwm.ensuredIDs())
	assert.Equal(t, []uuid.UUID{id}, fwm.removedIDs())
}

func TestEngine_WorkspaceLifecycle_KeptOnFailure(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	// Force node A to fail so the instance reaches StateFailed.
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		return nil, fmt.Errorf("forced node failure")
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)

	assert.Equal(t, []uuid.UUID{id}, fwm.ensuredIDs(), "workspace must be created")
	assert.Empty(t, fwm.removedIDs(), "workspace must be kept on failure")
}

func TestEngine_WorkspaceSeededAsVariable(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	var seenWorkspace any
	f.executor.onNode("A", func(_ context.Context, _ workflow.Node, vars *variable.VariableStore) (*engine.NodeResult, error) {
		seenWorkspace, _ = vars.Get("workspace")
		return &engine.NodeResult{}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.Equal(t, "/tmp/arbitron-test-ws/"+id.String(), seenWorkspace)
}

func TestEngine_WorkspaceEnsureFailure_FailsInstance(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{ensureErr: fmt.Errorf("disk full")}
	f.engine.SetWorkspaceManager(fwm)

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)

	inst := waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateFailed)
	assert.Equal(t, instance.StateFailed, inst.State)
	assert.Empty(t, f.executor.getCalls(), "no nodes should run when workspace creation fails")
}

func TestEngine_WorkspacePersistedBeforeNodes(t *testing.T) {
	f := newTestFixtures()
	f.workflowRepo.add(linearWorkflow())
	fwm := &fakeWorkspaceManager{}
	f.engine.SetWorkspaceManager(fwm)

	var persistedWorkspace any
	f.executor.onNode("A", func(ctx context.Context, _ workflow.Node, _ *variable.VariableStore) (*engine.NodeResult, error) {
		id, _ := engine.InstanceIDFromContext(ctx)
		inst, _ := f.instanceRepo.Get(ctx, id)
		if inst != nil {
			persistedWorkspace = inst.Variables["workspace"]
		}
		return &engine.NodeResult{}, nil
	})

	id, _, err := f.engine.Trigger(context.Background(), "linear-wf", nil, "normal")
	require.NoError(t, err)
	waitForInstance(t, f.instanceRepo, id, 5*time.Second, instance.StateCompleted)

	assert.Equal(t, "/tmp/arbitron-test-ws/"+id.String(), persistedWorkspace,
		"workspace must be persisted to inst.Variables before the first node runs")
}
