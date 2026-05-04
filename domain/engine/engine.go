package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Engine is the core workflow execution engine. It orchestrates the loading,
// validation, and step-by-step execution of workflow graphs.
type Engine struct {
	workflows      workflow.WorkflowRepository
	instances      instance.InstanceRepository
	nodeExecutions instance.NodeExecutionRepository
	snapshots      instance.VariableSnapshotRepository
	users          instance.UserRepository
	executors      map[workflow.NodeType]NodeExecutor
	eventBus       EventBus
	mu             sync.Mutex
	contexts       map[uuid.UUID]context.CancelFunc
}

// NewEngine constructs a new Engine with all required dependencies.
func NewEngine(
	workflows workflow.WorkflowRepository,
	instances instance.InstanceRepository,
	nodeExecutions instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	users instance.UserRepository,
	executors map[workflow.NodeType]NodeExecutor,
	eventBus EventBus,
) *Engine {
	return &Engine{
		workflows:      workflows,
		instances:      instances,
		nodeExecutions: nodeExecutions,
		snapshots:      snapshots,
		users:          users,
		executors:      executors,
		eventBus:       eventBus,
		contexts:       make(map[uuid.UUID]context.CancelFunc),
	}
}

// edgeTarget describes a destination from a graph edge.
type edgeTarget struct {
	nodeID     string
	outputPort string
}

// Trigger loads a workflow by name, validates inputs, creates a new instance,
// and launches the graph walker in a goroutine.
func (e *Engine) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, error) {
	wf, err := e.workflows.Load(workflowName)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to load workflow: %w", err)
	}

	// Validate inputs.
	vars := variable.NewStore()
	for _, inp := range wf.Inputs {
		val, provided := variables[inp.Name]
		if !provided || val == nil {
			if inp.Required {
				return uuid.Nil, fmt.Errorf("missing required input: %s", inp.Name)
			}
			if inp.Default != nil {
				vars.SetInput(inp.Name, inp.Default)
			}
		} else {
			vars.SetInput(inp.Name, val)
		}
	}

	// Also set any extra variables that don't match input definitions.
	for k, v := range variables {
		if _, exists := vars.Get(k); !exists {
			vars.SetInput(k, v)
		}
	}

	// Snapshot the workflow definition as a JSON-serializable map.
	wfBytes, err := json.Marshal(wf)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to snapshot workflow: %w", err)
	}
	var wfSnapshot map[string]any
	if err := json.Unmarshal(wfBytes, &wfSnapshot); err != nil {
		return uuid.Nil, fmt.Errorf("failed to parse workflow snapshot: %w", err)
	}

	// Get default user for OwnerID.
	ownerID, err := e.users.GetDefaultUser()
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get default user: %w", err)
	}

	now := time.Now()
	snap := vars.Snapshot()
	origVars := make(map[string]any, len(snap))
	for k, v := range snap {
		origVars[k] = v
	}

	inst := &instance.Instance{
		ID:                uuid.New(),
		OwnerID:           ownerID,
		WorkflowName:      workflowName,
		WorkflowSnapshot:  wfSnapshot,
		State:             instance.StateRunning,
		Priority:          instance.Priority(priority),
		Variables:         snap,
		OriginalVariables: origVars,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := e.instances.Create(inst); err != nil {
		return uuid.Nil, fmt.Errorf("failed to create instance: %w", err)
	}

	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.created",
		Timestamp: now,
		Data:      map[string]any{"workflowName": workflowName},
	})

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[inst.ID] = cancel
	e.mu.Unlock()

	go e.run(ctx, inst.ID)

	return inst.ID, nil
}

// run is the graph walker loop. It executes nodes sequentially following
// edges until there are no more nodes to execute.
func (e *Engine) run(ctx context.Context, instanceID uuid.UUID) {
	defer func() {
		e.mu.Lock()
		delete(e.contexts, instanceID)
		e.mu.Unlock()
	}()

	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return
	}

	// Parse the workflow from the snapshot.
	wfBytes, err := json.Marshal(inst.WorkflowSnapshot)
	if err != nil {
		_ = e.instances.SetFailed(instanceID, "")
		return
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		_ = e.instances.SetFailed(instanceID, "")
		return
	}

	// Build node lookup map.
	nodeMap := make(map[string]workflow.Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeMap[n.ID] = n
	}

	// Build adjacency map: map[sourceNodeID][]edgeTarget
	adj := make(map[string][]edgeTarget)
	for _, edge := range wf.Edges {
		adj[edge.Source] = append(adj[edge.Source], edgeTarget{
			nodeID:     edge.Target,
			outputPort: edge.Output,
		})
	}

	// Find start node.
	var currentNodeID string
	if inst.CurrentNodeID != nil && *inst.CurrentNodeID != "" {
		// Resuming from a specific node.
		currentNodeID = *inst.CurrentNodeID
	} else {
		// Find the entrypoint.
		for _, n := range wf.Nodes {
			if n.Entrypoint {
				currentNodeID = n.ID
				break
			}
		}
	}

	if currentNodeID == "" {
		_ = e.instances.SetFailed(instanceID, "")
		return
	}

	// Reconstruct variable store using node IDs so object-valued inputs are not
	// misclassified as node outputs.
	nodeIDs := make(map[string]bool, len(nodeMap))
	for id := range nodeMap {
		nodeIDs[id] = true
	}
	vars := variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)

	for currentNodeID != "" {
		// Check for pause/cancel by re-reading instance state.
		freshInst, err := e.instances.Get(instanceID)
		if err != nil {
			_ = e.instances.SetFailed(instanceID, currentNodeID)
			return
		}

		if freshInst.State == instance.StatePaused {
			// The instance was paused externally. Stop execution.
			return
		}
		if freshInst.State == instance.StateCancelled {
			// The instance was cancelled externally. Stop execution.
			return
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			return
		default:
		}

		node, exists := nodeMap[currentNodeID]
		if !exists {
			_ = e.instances.SetFailed(instanceID, currentNodeID)
			return
		}

		// Snapshot variables before execution.
		_ = e.snapshots.Create(&instance.VariableSnapshot{
			ID:         uuid.New(),
			InstanceID: instanceID,
			NodeID:     currentNodeID,
			Variables:  vars.Snapshot(),
			CapturedAt: time.Now(),
		})

		// Update current node.
		_ = e.instances.UpdateCurrentNode(instanceID, currentNodeID)

		e.eventBus.Publish(instanceID, Event{
			Type:      "node.started",
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
		})

		// Execute the node with retry logic. Each attempt is individually recorded.
		result, nodeExec, execErr := e.executeWithRetry(ctx, instanceID, node, vars, wf)

		if execErr != nil {
			e.eventBus.Publish(instanceID, Event{
				Type:      "node.failed",
				Timestamp: time.Now(),
				Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "error": execErr.Error()},
			})

			// Handle error behavior.
			eb := resolveErrorBehavior(node, wf)

			switch eb.Type {
			case "skip":
				// Persist the skipped status.
				if nodeExec != nil {
					nodeExec.Status = instance.NodeStatusSkipped
					_ = e.nodeExecutions.Update(nodeExec)
				}
				if eb.DefaultOutput != nil {
					vars.SetNodeOutput(currentNodeID, eb.DefaultOutput)
				}
				_ = e.instances.UpdateVariables(instanceID, vars.Snapshot())
				currentNodeID = e.findNextNode(currentNodeID, "", adj)

			case "route":
				// Jump to the error node.
				if eb.ErrorNodeID != "" {
					currentNodeID = eb.ErrorNodeID
				} else {
					_ = e.instances.SetFailed(instanceID, currentNodeID)
					return
				}

			default: // "halt" or unrecognized
				_ = e.instances.SetFailed(instanceID, currentNodeID)
				e.eventBus.Publish(instanceID, Event{
					Type:      "instance.failed",
					Timestamp: time.Now(),
					Data:      map[string]any{"failedNodeId": currentNodeID, "error": execErr.Error()},
				})
				return
			}
		} else {
			e.eventBus.Publish(instanceID, Event{
				Type:      "node.completed",
				Timestamp: time.Now(),
				Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
			})

			// Success: update variables with node output.
			if result != nil && result.Output != nil {
				vars.SetNodeOutput(currentNodeID, result.Output)
			}
			_ = e.instances.UpdateVariables(instanceID, vars.Snapshot())

			// Find next node.
			outputPort := ""
			if result != nil {
				outputPort = result.OutputPort
			}
			currentNodeID = e.findNextNode(currentNodeID, outputPort, adj)
		}
	}

	// All nodes executed successfully.
	_ = e.instances.SetCompleted(instanceID)
	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.completed",
		Timestamp: time.Now(),
	})
}

// executeWithRetry executes a node, applying retry logic from the error behavior.
// A NodeExecution record is created for each attempt. The final record is returned
// so the caller can update it (e.g., marking it skipped).
func (e *Engine) executeWithRetry(ctx context.Context, instanceID uuid.UUID, node workflow.Node, vars *variable.VariableStore, wf workflow.Workflow) (*NodeResult, *instance.NodeExecution, error) {
	executor, ok := e.executors[node.Type]
	if !ok {
		return nil, nil, fmt.Errorf("no executor registered for node type: %s", node.Type)
	}

	eb := resolveErrorBehavior(node, wf)
	maxAttempts := 1
	if eb.Type == "retry" && eb.Retries > 0 {
		maxAttempts = eb.Retries + 1
	}

	var lastErr error
	var lastExec *instance.NodeExecution

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 && eb.RetryDelaySeconds > 0 {
			select {
			case <-ctx.Done():
				return nil, lastExec, ctx.Err()
			case <-time.After(time.Duration(eb.RetryDelaySeconds) * time.Second):
			}
		}

		startedAt := time.Now()
		result, err := executor.Execute(ctx, node, vars, "")
		completedAt := time.Now()

		execStatus := instance.NodeStatusSucceeded
		var execError map[string]any
		if err != nil {
			execStatus = instance.NodeStatusFailed
			execError = map[string]any{"error": err.Error()}
		}

		var execOutput map[string]any
		if result != nil && result.Output != nil {
			if m, ok := result.Output.(map[string]any); ok {
				execOutput = m
			} else {
				execOutput = map[string]any{"value": result.Output}
			}
		}

		nodeExec := &instance.NodeExecution{
			ID:          uuid.New(),
			InstanceID:  instanceID,
			NodeID:      node.ID,
			Attempt:     attempt,
			Status:      execStatus,
			Output:      execOutput,
			Error:       execError,
			StartedAt:   startedAt,
			CompletedAt: &completedAt,
		}
		if result != nil {
			if result.LLMModel != "" {
				nodeExec.LLMModel = &result.LLMModel
			}
			if result.LLMTokensIn > 0 {
				nodeExec.LLMTokensIn = &result.LLMTokensIn
			}
			if result.LLMTokensOut > 0 {
				nodeExec.LLMTokensOut = &result.LLMTokensOut
			}
			if result.ScriptStdout != "" {
				nodeExec.ScriptStdout = &result.ScriptStdout
			}
			if result.ScriptStderr != "" {
				nodeExec.ScriptStderr = &result.ScriptStderr
			}
			if result.ScriptExitCode != 0 {
				nodeExec.ScriptExitCode = &result.ScriptExitCode
			}
		}
		_ = e.nodeExecutions.Create(nodeExec)
		lastExec = nodeExec

		if err == nil {
			return result, nodeExec, nil
		}
		lastErr = err
	}

	return nil, lastExec, lastErr
}

// resolveErrorBehavior returns the effective error behavior for a node,
// falling back to the workflow default, then to "halt".
func resolveErrorBehavior(node workflow.Node, wf workflow.Workflow) workflow.ErrorBehavior {
	if node.ErrorBehavior.Type != "" {
		return node.ErrorBehavior
	}
	if wf.DefaultErrorBehavior.Type != "" {
		return wf.DefaultErrorBehavior
	}
	return workflow.ErrorBehavior{Type: "halt"}
}

// findNextNode finds the next node to execute based on the current node's
// output port and the adjacency map.
func (e *Engine) findNextNode(currentNodeID string, outputPort string, adj map[string][]edgeTarget) string {
	targets := adj[currentNodeID]
	if len(targets) == 0 {
		return ""
	}

	// If there's an output port, find the matching edge.
	if outputPort != "" {
		for _, t := range targets {
			if t.outputPort == outputPort {
				return t.nodeID
			}
		}
		// No matching edge found for the output port.
		return ""
	}

	// No output port specified: take the first edge with no output port,
	// or the first edge if all have output ports.
	for _, t := range targets {
		if t.outputPort == "" {
			return t.nodeID
		}
	}
	// Fallback: take the first edge.
	return targets[0].nodeID
}

// Pause sets a running instance to paused state. The graph walker will
// detect this on its next iteration and stop.
func (e *Engine) Pause(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StateRunning {
		return fmt.Errorf("instance %s is not running (state: %s)", instanceID, inst.State)
	}
	if err := e.instances.UpdateState(instanceID, instance.StatePaused); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}
	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.paused",
		Timestamp: time.Now(),
	})
	return nil
}

// Resume sets a paused instance back to running and re-launches the
// graph walker from the current node.
func (e *Engine) Resume(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StatePaused {
		return fmt.Errorf("instance %s is not paused (state: %s)", instanceID, inst.State)
	}
	if err := e.instances.UpdateState(instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.resumed",
		Timestamp: time.Now(),
	})

	go e.run(ctx, instanceID)
	return nil
}

// Cancel sets a running instance to cancelled and cancels its context.
func (e *Engine) Cancel(instanceID uuid.UUID) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StateRunning && inst.State != instance.StatePaused {
		return fmt.Errorf("instance %s cannot be cancelled (state: %s)", instanceID, inst.State)
	}

	if err := e.instances.UpdateState(instanceID, instance.StateCancelled); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	// Cancel the context to interrupt any running executor.
	e.mu.Lock()
	if cancelFn, ok := e.contexts[instanceID]; ok {
		cancelFn()
	}
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.cancelled",
		Timestamp: time.Now(),
	})
	return nil
}

// Retry resumes a failed instance from the failed node, optionally
// applying variable edits.
func (e *Engine) Retry(instanceID uuid.UUID, variableEdits map[string]any) error {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StateFailed {
		return fmt.Errorf("instance %s is not failed (state: %s)", instanceID, inst.State)
	}

	// Apply variable edits.
	if variableEdits != nil {
		for k, v := range variableEdits {
			inst.Variables[k] = v
		}
		if err := e.instances.UpdateVariables(instanceID, inst.Variables); err != nil {
			return fmt.Errorf("failed to update variables: %w", err)
		}
	}

	// Set the instance to running, keeping the current node as the failed node.
	if inst.FailedNodeID != nil {
		if err := e.instances.UpdateCurrentNode(instanceID, *inst.FailedNodeID); err != nil {
			return fmt.Errorf("failed to update current node: %w", err)
		}
	}

	if err := e.instances.UpdateState(instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.retrying",
		Timestamp: time.Now(),
	})

	go e.run(ctx, instanceID)
	return nil
}

// Rerun creates a new instance from a completed instance's original variables
// and triggers it.
func (e *Engine) Rerun(instanceID uuid.UUID) (uuid.UUID, error) {
	inst, err := e.instances.Get(instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get instance: %w", err)
	}

	return e.Trigger(inst.WorkflowName, inst.OriginalVariables, string(inst.Priority))
}

// RecoverCrashedInstances finds instances that are in running state
// (from a previous crash) and sets them to failed.
func (e *Engine) RecoverCrashedInstances() error {
	running, err := e.instances.ListRunning()
	if err != nil {
		return fmt.Errorf("failed to list running instances: %w", err)
	}

	for _, inst := range running {
		failedNode := ""
		if inst.CurrentNodeID != nil {
			failedNode = *inst.CurrentNodeID
		}
		if err := e.instances.SetFailed(inst.ID, failedNode); err != nil {
			return fmt.Errorf("failed to set instance %s as failed: %w", inst.ID, err)
		}
		e.eventBus.Publish(inst.ID, Event{
			Type:      "instance.recovered",
			Timestamp: time.Now(),
			Data:      map[string]any{"reason": "crash recovery"},
		})
	}

	return nil
}
