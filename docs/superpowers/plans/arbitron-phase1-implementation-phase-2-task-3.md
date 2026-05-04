# Phase 2 Task 3: Graph Walker & Instance Lifecycle

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the execution engine (graph walker) that orchestrates node execution, handles errors/retries, and manages instance lifecycle operations (trigger, pause, resume, cancel, retry, re-run).

**Architecture:** The `Engine` struct in `domain/engine/` holds all port dependencies and coordinates execution. It launches goroutines for workflow execution, manages cancellation contexts, and delegates to node executors. The EventBus port is defined here but a no-op implementation is used until Phase 3.

**Tech Stack:** Go 1.23+, goroutines, context cancellation

---

### Task 1: EventBus Port & No-Op Implementation

**Files:**
- Modify: `domain/engine/ports.go` (add EventBus)
- Create: `adapter/eventbus/eventbus.go`

- [ ] **Step 1: Add EventBus to ports**

Add to `domain/engine/ports.go`:

```go
import (
	"time"
	"github.com/google/uuid"
)

type Event struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

type EventBus interface {
	Publish(instanceID uuid.UUID, event Event)
	Subscribe(instanceID uuid.UUID) (<-chan Event, func())
}
```

- [ ] **Step 2: Create no-op EventBus for initial testing**

Create `adapter/eventbus/noop.go`:

```go
package eventbus

import (
	"github.com/google/uuid"
	"github.com/arbitron/arbitron/domain/engine"
)

type NoOpEventBus struct{}

func NewNoOp() *NoOpEventBus {
	return &NoOpEventBus{}
}

func (b *NoOpEventBus) Publish(instanceID uuid.UUID, event engine.Event) {}

func (b *NoOpEventBus) Subscribe(instanceID uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}
```

- [ ] **Step 3: Commit**

```bash
git add domain/engine/ports.go adapter/eventbus/
git commit -m "feat: EventBus port and no-op implementation"
```

---

### Task 2: Engine Core

**Files:**
- Create: `domain/engine/engine.go`
- Create: `domain/engine/engine_test.go`

- [ ] **Step 4: Write failing tests for the engine**

Create `domain/engine/engine_test.go`. Use mock implementations of all port interfaces. Tests:

**Linear workflow execution:**
```go
func TestEngine_LinearWorkflow(t *testing.T) {
	// Workflow: transform_a → transform_b → transform_c
	// Each transform sets a variable
	// Verify: all three variables set, instance completes, execution trace has 3 records
}
```

**Conditional branching:**
```go
func TestEngine_ConditionalBranch(t *testing.T) {
	// Workflow: transform_set_x → conditional → branch_a OR branch_b
	// Set x=10, condition: x > 5 → "yes" branch
	// Verify: branch_a executed, branch_b not executed
}
```

**Conditional default output:**
```go
func TestEngine_ConditionalDefault(t *testing.T) {
	// No conditions match → defaultOutput path taken
}
```

**Single node workflow:**
```go
func TestEngine_SingleNode(t *testing.T) {
	// One transform node, no edges → instance completes after one node
}
```

**Error halt:**
```go
func TestEngine_ErrorHalt(t *testing.T) {
	// A node that fails → instance moves to failed, failed_node_id recorded
}
```

**Error skip:**
```go
func TestEngine_ErrorSkip(t *testing.T) {
	// A node with errorBehavior=skip → continues with defaultOutput
}
```

**Error route:**
```go
func TestEngine_ErrorRoute(t *testing.T) {
	// A node with errorBehavior=route → follows error edge
}
```

**Retry succeeds on second attempt:**
```go
func TestEngine_RetrySuccess(t *testing.T) {
	// A mock executor that fails once then succeeds
	// errorBehavior=retry with retries=2
	// Verify: instance completes, two node execution records
}
```

**Retry exhausted:**
```go
func TestEngine_RetryExhausted(t *testing.T) {
	// Mock executor always fails, retries=2
	// Verify: instance fails after 3 attempts (1 original + 2 retries)
}
```

**Default error behavior from workflow:**
```go
func TestEngine_DefaultErrorBehavior(t *testing.T) {
	// Workflow defaultErrorBehavior=skip, node has no override
	// Node fails → skip behavior applied
}
```

- [ ] **Step 5: Run tests to verify they fail**

```bash
go test ./domain/engine/ -v -run TestEngine
```

Expected: FAIL — `engine.Engine` not defined.

- [ ] **Step 6: Implement the Engine**

Create `domain/engine/engine.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

type Engine struct {
	workflows      workflow.WorkflowRepository
	instances      instance.InstanceRepository
	nodeExecutions instance.NodeExecutionRepository
	snapshots      instance.VariableSnapshotRepository
	executors      map[workflow.NodeType]NodeExecutor
	eventBus       EventBus

	mu       sync.Mutex
	contexts map[uuid.UUID]context.CancelFunc
}

func NewEngine(
	workflows workflow.WorkflowRepository,
	instances instance.InstanceRepository,
	nodeExecutions instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	eventBus EventBus,
	executors map[workflow.NodeType]NodeExecutor,
) *Engine {
	return &Engine{
		workflows:      workflows,
		instances:      instances,
		nodeExecutions: nodeExecutions,
		snapshots:      snapshots,
		eventBus:       eventBus,
		executors:      executors,
		contexts:       make(map[uuid.UUID]context.CancelFunc),
	}
}

func (e *Engine) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, error) {
	wf, err := e.workflows.Load(workflowName)
	if err != nil {
		return uuid.Nil, fmt.Errorf("loading workflow: %w", err)
	}

	vars := variable.NewStore()
	for _, input := range wf.Inputs {
		if input.Default != nil {
			vars.SetInput(input.Name, input.Default)
		}
	}
	for k, v := range variables {
		vars.SetInput(k, v)
	}

	for _, input := range wf.Inputs {
		if input.Required {
			if _, ok := vars.Get(input.Name); !ok {
				return uuid.Nil, fmt.Errorf("missing required input: %s", input.Name)
			}
		}
	}

	snapshotData, err := json.Marshal(wf)
	if err != nil {
		return uuid.Nil, fmt.Errorf("marshaling workflow snapshot: %w", err)
	}
	var snapshotMap map[string]any
	json.Unmarshal(snapshotData, &snapshotMap)

	inst := &instance.Instance{
		ID:                uuid.New(),
		WorkflowName:      workflowName,
		WorkflowSnapshot:  snapshotMap,
		State:             instance.StateRunning,
		Priority:          instance.Priority(priority),
		Variables:         vars.Snapshot(),
		OriginalVariables: vars.Snapshot(),
	}

	if err := e.instances.Create(inst); err != nil {
		return uuid.Nil, fmt.Errorf("creating instance: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[inst.ID] = cancel
	e.mu.Unlock()

	go e.run(ctx, inst.ID)

	return inst.ID, nil
}

func (e *Engine) run(ctx context.Context, instanceID uuid.UUID) {
	defer func() {
		e.mu.Lock()
		delete(e.contexts, instanceID)
		e.mu.Unlock()
	}()

	inst, err := e.instances.Get(instanceID)
	if err != nil {
		slog.Error("failed to load instance", "instance_id", instanceID, "error", err)
		return
	}

	var wf workflow.Workflow
	wfData, _ := json.Marshal(inst.WorkflowSnapshot)
	json.Unmarshal(wfData, &wf)

	vars := variable.FromSnapshot(inst.Variables)
	adj := buildAdjacencyMap(wf.Edges)
	nodeMap := buildNodeMap(wf.Nodes)

	var startNodeID string
	if inst.FailedNodeID != nil {
		startNodeID = *inst.FailedNodeID
	} else if inst.CurrentNodeID != nil {
		startNodeID = findNextNodeID(adj, *inst.CurrentNodeID, "")
	} else {
		for _, n := range wf.Nodes {
			if n.Entrypoint {
				startNodeID = n.ID
				break
			}
		}
	}

	currentNodeID := startNodeID
	for currentNodeID != "" {
		select {
		case <-ctx.Done():
			return
		default:
		}

		inst, err = e.instances.Get(instanceID)
		if err != nil {
			return
		}
		if inst.State == instance.StatePaused || inst.State == instance.StateCancelled {
			return
		}

		node, ok := nodeMap[currentNodeID]
		if !ok {
			e.instances.SetFailed(instanceID, currentNodeID)
			return
		}

		e.snapshots.Create(&instance.VariableSnapshot{
			ID: uuid.New(), InstanceID: instanceID, NodeID: currentNodeID,
			Variables: vars.Snapshot(), CapturedAt: time.Now(),
		})

		e.instances.UpdateCurrentNode(instanceID, currentNodeID)

		e.eventBus.Publish(instanceID, Event{Type: "node_started", Timestamp: time.Now(), Data: map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)}})

		executor, ok := e.executors[node.Type]
		if !ok {
			e.instances.SetFailed(instanceID, currentNodeID)
			return
		}

		result, execErr := e.executeWithRetry(ctx, instanceID, node, vars, wf.Name, wf.DefaultErrorBehavior)

		if execErr != nil {
			errorBehavior := node.ErrorBehavior
			if errorBehavior.Type == "" {
				errorBehavior = wf.DefaultErrorBehavior
			}

			switch errorBehavior.Type {
			case "skip":
				result = &NodeResult{Output: errorBehavior.DefaultOutput}
			case "route":
				e.eventBus.Publish(instanceID, Event{Type: "node_failed", Timestamp: time.Now()})
				currentNodeID = errorBehavior.ErrorNodeID
				continue
			default: // "halt" or ""
				e.instances.SetFailed(instanceID, currentNodeID)
				e.eventBus.Publish(instanceID, Event{Type: "node_failed", Timestamp: time.Now()})
				e.eventBus.Publish(instanceID, Event{Type: "state_changed", Timestamp: time.Now(), Data: map[string]any{"newState": "failed"}})
				return
			}
		}

		_ = executor // used in executeWithRetry

		e.eventBus.Publish(instanceID, Event{Type: "node_completed", Timestamp: time.Now()})

		if result != nil && result.Output != nil {
			vars.SetNodeOutput(currentNodeID, result.Output)
		}

		e.instances.UpdateVariables(instanceID, vars.Snapshot())
		e.eventBus.Publish(instanceID, Event{Type: "variable_updated", Timestamp: time.Now()})

		outputPort := ""
		if result != nil {
			outputPort = result.OutputPort
		}
		currentNodeID = findNextNodeID(adj, currentNodeID, outputPort)
	}

	e.instances.SetCompleted(instanceID)
	e.eventBus.Publish(instanceID, Event{Type: "state_changed", Timestamp: time.Now(), Data: map[string]any{"newState": "completed"}})
}

func (e *Engine) executeWithRetry(ctx context.Context, instanceID uuid.UUID, node workflow.Node, vars *variable.VariableStore, workflowName string, defaultBehavior workflow.ErrorBehavior) (*NodeResult, error) {
	executor := e.executors[node.Type]

	startedAt := time.Now()
	result, err := executor.Execute(ctx, node, vars, workflowName)

	e.recordNodeExecution(instanceID, node.ID, 1, result, err, startedAt)

	if err == nil {
		return result, nil
	}

	errorBehavior := node.ErrorBehavior
	if errorBehavior.Type == "" {
		errorBehavior = defaultBehavior
	}

	if errorBehavior.Type != "retry" {
		return result, err
	}

	for attempt := 2; attempt <= errorBehavior.Retries+1; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(errorBehavior.RetryDelaySeconds) * time.Second):
		}

		startedAt = time.Now()
		result, err = executor.Execute(ctx, node, vars, workflowName)
		e.recordNodeExecution(instanceID, node.ID, attempt, result, err, startedAt)

		if err == nil {
			return result, nil
		}
	}

	return result, err
}

func (e *Engine) recordNodeExecution(instanceID uuid.UUID, nodeID string, attempt int, result *NodeResult, execErr error, startedAt time.Time) {
	now := time.Now()
	status := instance.NodeStatusSucceeded
	if execErr != nil {
		status = instance.NodeStatusFailed
	}

	exec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     nodeID,
		Attempt:    attempt,
		Status:     status,
		StartedAt:  startedAt,
		CompletedAt: &now,
	}

	if result != nil {
		if m, ok := result.Output.(map[string]any); ok {
			exec.Output = m
		}
		if result.LLMModel != "" {
			exec.LLMModel = &result.LLMModel
			exec.LLMTokensIn = &result.LLMTokensIn
			exec.LLMTokensOut = &result.LLMTokensOut
		}
		if result.ScriptStdout != "" {
			exec.ScriptStdout = &result.ScriptStdout
			exec.ScriptStderr = &result.ScriptStderr
			exec.ScriptExitCode = &result.ScriptExitCode
		}
	}

	if execErr != nil {
		exec.Error = map[string]any{"message": execErr.Error()}
	}

	e.nodeExecutions.Create(exec)
}

func (e *Engine) Pause(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StateRunning {
		return fmt.Errorf("cannot pause instance in state %s", inst.State)
	}
	return e.instances.UpdateState(instanceID, instance.StatePaused)
}

func (e *Engine) Resume(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StatePaused {
		return fmt.Errorf("cannot resume instance in state %s", inst.State)
	}
	if err := e.instances.UpdateState(instanceID, instance.StateRunning); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	go e.run(ctx, instanceID)
	return nil
}

func (e *Engine) Cancel(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StateRunning && inst.State != instance.StateQueued && inst.State != instance.StatePaused {
		return fmt.Errorf("cannot cancel instance in state %s", inst.State)
	}

	e.mu.Lock()
	if cancel, ok := e.contexts[instanceID]; ok {
		cancel()
	}
	e.mu.Unlock()

	return e.instances.UpdateState(instanceID, instance.StateCancelled)
}

func (e *Engine) Retry(instanceID uuid.UUID, variableEdits map[string]any) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StateFailed {
		return fmt.Errorf("cannot retry instance in state %s", inst.State)
	}

	if len(variableEdits) > 0 {
		for k, v := range variableEdits {
			inst.Variables[k] = v
		}
		if err := e.instances.UpdateVariables(instanceID, inst.Variables); err != nil {
			return err
		}
	}

	if err := e.instances.UpdateState(instanceID, instance.StateRunning); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	go e.run(ctx, instanceID)
	return nil
}

func (e *Engine) Rerun(instanceID uuid.UUID) (uuid.UUID, error) {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return uuid.Nil, err
	}
	if inst.State != instance.StateCompleted && inst.State != instance.StateFailed && inst.State != instance.StateCancelled {
		return uuid.Nil, fmt.Errorf("cannot re-run instance in state %s", inst.State)
	}

	newInst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           inst.OwnerID,
		WorkflowName:      inst.WorkflowName,
		WorkflowSnapshot:  inst.WorkflowSnapshot,
		State:             instance.StateRunning,
		Priority:          inst.Priority,
		Variables:         inst.OriginalVariables,
		OriginalVariables: inst.OriginalVariables,
	}

	if err := e.instances.Create(newInst); err != nil {
		return uuid.Nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[newInst.ID] = cancel
	e.mu.Unlock()

	go e.run(ctx, newInst.ID)
	return newInst.ID, nil
}

func (e *Engine) RecoverCrashedInstances() error {
	running, err := e.instances.ListRunning()
	if err != nil {
		return err
	}
	for _, inst := range running {
		nodeID := ""
		if inst.CurrentNodeID != nil {
			nodeID = *inst.CurrentNodeID
		}
		slog.Warn("recovering crashed instance", "instance_id", inst.ID, "node_id", nodeID)
		e.instances.SetFailed(inst.ID, nodeID)
	}
	return nil
}

type edgeTarget struct {
	nodeID     string
	outputPort string
}

func buildAdjacencyMap(edges []workflow.Edge) map[string][]edgeTarget {
	adj := make(map[string][]edgeTarget)
	for _, e := range edges {
		adj[e.Source] = append(adj[e.Source], edgeTarget{nodeID: e.Target, outputPort: e.Output})
	}
	return adj
}

func buildNodeMap(nodes []workflow.Node) map[string]workflow.Node {
	m := make(map[string]workflow.Node)
	for _, n := range nodes {
		m[n.ID] = n
	}
	return m
}

func findNextNodeID(adj map[string][]edgeTarget, currentNodeID string, outputPort string) string {
	targets := adj[currentNodeID]
	if len(targets) == 0 {
		return ""
	}
	if outputPort == "" {
		return targets[0].nodeID
	}
	for _, t := range targets {
		if t.outputPort == outputPort {
			return t.nodeID
		}
	}
	return ""
}
```

- [ ] **Step 7: Run engine tests**

```bash
go test ./domain/engine/ -v -run TestEngine
```

Expected: all PASS

- [ ] **Step 8: Write lifecycle operation tests**

Add tests to `domain/engine/engine_test.go` for:
- Trigger with valid inputs → instance created, running
- Trigger with missing required input → error
- Pause running instance → state changes to paused
- Pause non-running → error
- Resume paused → state changes to running, execution continues
- Cancel running → context cancelled, state changes to cancelled
- Cancel terminal → error
- Retry failed → resumes at failed node with edited variables
- Retry non-failed �� error
- Rerun completed → new instance created with original variables
- Rerun running → error
- RecoverCrashedInstances → running instances moved to failed

- [ ] **Step 9: Run all engine tests**

```bash
go test ./domain/engine/ -v -timeout 60s
```

Expected: all PASS

- [ ] **Step 10: Commit**

```bash
git add domain/engine/engine* adapter/eventbus/
git commit -m "feat: execution engine with graph walker and instance lifecycle"
```
