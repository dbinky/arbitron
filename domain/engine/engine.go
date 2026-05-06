package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	workflows       workflow.WorkflowRepository
	instances       instance.InstanceRepository
	nodeExecutions  instance.NodeExecutionRepository
	snapshots       instance.VariableSnapshotRepository
	users           instance.UserRepository
	executors       map[workflow.NodeType]NodeExecutor
	eventBus        EventBus
	queueManager    QueueManager
	queueRepo       instance.QueueRepository
	scheduler       TimerScheduler
	webhookRegistry WebhookRegistrar
	mu              sync.Mutex
	contexts        map[uuid.UUID]context.CancelFunc
	wg              sync.WaitGroup
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
	queueManager QueueManager,
	queueRepo instance.QueueRepository,
) *Engine {
	return &Engine{
		workflows:      workflows,
		instances:      instances,
		nodeExecutions: nodeExecutions,
		snapshots:      snapshots,
		users:          users,
		executors:      executors,
		eventBus:       eventBus,
		queueManager:   queueManager,
		queueRepo:      queueRepo,
		contexts:       make(map[uuid.UUID]context.CancelFunc),
	}
}

// SetScheduler sets the timer scheduler on the engine.
func (e *Engine) SetScheduler(s TimerScheduler) {
	e.scheduler = s
}

// SetWebhookRegistry sets the webhook registry on the engine.
func (e *Engine) SetWebhookRegistry(reg WebhookRegistrar) {
	e.webhookRegistry = reg
}

// edgeTarget describes a destination from a graph edge.
type edgeTarget struct {
	nodeID     string
	outputPort string
}

// Trigger loads a workflow by name, validates inputs, creates a new instance,
// and delegates to the queue manager for capacity-based scheduling.
// The second return value indicates whether the instance started immediately (true)
// or stayed in queued state (false).
func (e *Engine) Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	ctx := context.Background()

	wf, err := e.workflows.Load(ctx, workflowName)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
	}

	// Validate inputs.
	vars := variable.NewStore()
	for _, inp := range wf.Inputs {
		val, provided := variables[inp.Name]
		if !provided || val == nil {
			if inp.Required {
				return uuid.Nil, false, fmt.Errorf("missing required input: %s", inp.Name)
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
		return uuid.Nil, false, fmt.Errorf("failed to snapshot workflow: %w", err)
	}
	var wfSnapshot map[string]any
	if err := json.Unmarshal(wfBytes, &wfSnapshot); err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to parse workflow snapshot: %w", err)
	}

	// Get default user for OwnerID.
	ownerID, err := e.users.GetDefaultUser(ctx)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to get default user: %w", err)
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
		State:             instance.StateQueued,
		Priority:          instance.Priority(priority),
		Variables:         snap,
		OriginalVariables: origVars,
		QueuedAt:          &now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := e.instances.Create(ctx, inst); err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to create instance: %w", err)
	}

	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.created",
		Timestamp: now,
		Data:      map[string]any{"workflowName": workflowName},
	})

	started, err := e.queueManager.Enqueue(ctx, inst.ID, inst.OwnerID)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to enqueue instance: %w", err)
	}

	return inst.ID, started, nil
}

// StartInstance transitions a queued instance to running and launches the
// graph walker goroutine. It implements queue.InstanceStarter.
func (e *Engine) StartInstance(ctx context.Context, id uuid.UUID) error {
	if err := e.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to transition to running: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[id] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(id, Event{
		Type:      "instance.started",
		Timestamp: time.Now(),
		Data:      map[string]any{},
	})

	e.wg.Add(1)
	go e.run(runCtx, id)

	return nil
}

// run is the graph walker loop. It executes nodes sequentially following
// edges until there are no more nodes to execute.
func (e *Engine) run(ctx context.Context, instanceID uuid.UUID) {
	defer e.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			// A panic in the run loop should not crash the process.
			// Mark the instance as failed and log via event bus.
			_ = e.instances.SetFailed(ctx, instanceID, "")
			e.eventBus.Publish(instanceID, Event{
				Type:      "instance.failed",
				Timestamp: time.Now(),
				Data:      map[string]any{"error": fmt.Sprintf("panic: %v", r)},
			})
			e.queueManager.NotifyCapacityFreed(context.Background())
		}
	}()
	defer func() {
		e.mu.Lock()
		delete(e.contexts, instanceID)
		e.mu.Unlock()
	}()

	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		e.queueManager.NotifyCapacityFreed(context.Background())
		return
	}

	// Parse the workflow from the snapshot.
	wf, nodeMap, adj, parseErr := parseWorkflowGraph(inst.WorkflowSnapshot)
	if parseErr != nil {
		_ = e.instances.SetFailed(ctx, instanceID, "")
		e.queueManager.NotifyCapacityFreed(context.Background())
		return
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
		_ = e.instances.SetFailed(ctx, instanceID, "")
		e.queueManager.NotifyCapacityFreed(context.Background())
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
		// TODO(race): There is a TOCTOU race between Pause() writing StatePaused
		// and this loop reading the fresh state. If the pause arrives between Get()
		// and the next node execution, one extra node may execute. Fix: use
		// "UPDATE ... SET state = 'paused' WHERE state = 'running' RETURNING ..."
		// and check the affected row count to detect concurrent state changes.

		// Check for pause/cancel by re-reading instance state.
		freshInst, err := e.instances.Get(ctx, instanceID)
		if err != nil {
			if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
				slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
			}
			e.queueManager.NotifyCapacityFreed(context.Background())
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
			if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
				slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
			}
			e.queueManager.NotifyCapacityFreed(context.Background())
			return
		}

		// Snapshot variables before execution (diagnostic; log-only on error).
		if err := e.snapshots.Create(ctx, &instance.VariableSnapshot{
			ID:         uuid.New(),
			InstanceID: instanceID,
			NodeID:     currentNodeID,
			Variables:  vars.Snapshot(),
			CapturedAt: time.Now(),
		}); err != nil {
			slog.Warn("failed to create variable snapshot", "instanceID", instanceID, "nodeID", currentNodeID, "error", err)
		}

		// Update current node.
		if err := e.instances.UpdateCurrentNode(ctx, instanceID, currentNodeID); err != nil {
			if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
				slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
			}
			e.queueManager.NotifyCapacityFreed(context.Background())
			return
		}

		e.eventBus.Publish(instanceID, Event{
			Type:      "node.started",
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
		})

		// Execute the node with retry logic. Each attempt is individually recorded.
		execCtx := ContextWithInstanceID(ctx, instanceID)
		result, nodeExec, execErr := e.executeWithRetry(execCtx, instanceID, node, vars, *wf)

		if execErr != nil {
			e.eventBus.Publish(instanceID, Event{
				Type:      "node.failed",
				Timestamp: time.Now(),
				Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "error": execErr.Error()},
			})

			// Handle error behavior.
			eb := resolveErrorBehavior(node, *wf)

			switch eb.Type {
			case "skip":
				// Persist the skipped status.
				if nodeExec != nil {
					nodeExec.Status = instance.NodeStatusSkipped
					_ = e.nodeExecutions.Update(ctx, nodeExec)
				}
				if eb.DefaultOutput != nil {
					vars.SetNodeOutput(currentNodeID, eb.DefaultOutput)
				}
				if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
					if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
						slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
					}
					e.queueManager.NotifyCapacityFreed(context.Background())
					return
				}
				// Conditional nodes route via their declared default output port so
				// the skip path is deterministic rather than arbitrary-first-edge.
				skipPort := ""
				if node.Type == workflow.NodeTypeConditional {
					skipPort = node.Config.DefaultOutput
				}
				currentNodeID = e.findNextNode(currentNodeID, skipPort, adj)

			case "route":
				// Jump to the error node.
				if eb.ErrorNodeID != "" {
					currentNodeID = eb.ErrorNodeID
				} else {
					if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
						slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
					}
					e.queueManager.NotifyCapacityFreed(context.Background())
					return
				}

			default: // "halt" or unrecognized
				if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
					slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
				}
				e.eventBus.Publish(instanceID, Event{
					Type:      "instance.failed",
					Timestamp: time.Now(),
					Data:      map[string]any{"failedNodeId": currentNodeID, "error": execErr.Error()},
				})
				e.queueManager.NotifyCapacityFreed(context.Background())
				return
			}
		} else {
			// Check if this is a wait result (timer delay, webhook wait, etc.)
			if result != nil && result.IsWait {
				// Instance has transitioned to waiting. Exit the run loop cleanly.
				// Do NOT mark as completed or failed — the executor already handled state transition.
				e.eventBus.Publish(instanceID, Event{
					Type:      "node.completed",
					Timestamp: time.Now(),
					Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
				})
				return
			}

			e.eventBus.Publish(instanceID, Event{
				Type:      "node.completed",
				Timestamp: time.Now(),
				Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
			})

			// Success: update variables with node output.
			if result != nil && result.Output != nil {
				vars.SetNodeOutput(currentNodeID, result.Output)
			}
			if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
				if err := e.instances.SetFailed(ctx, instanceID, currentNodeID); err != nil {
					slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
				}
				e.queueManager.NotifyCapacityFreed(context.Background())
				return
			}

			// Find next node.
			outputPort := ""
			if result != nil {
				outputPort = result.OutputPort
			}
			currentNodeID = e.findNextNode(currentNodeID, outputPort, adj)
		}
	}

	// All nodes executed successfully.
	if err := e.instances.SetCompleted(ctx, instanceID); err != nil {
		slog.Error("failed to set instance completed", "instanceID", instanceID, "error", err)
	}
	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.completed",
		Timestamp: time.Now(),
	})
	e.queueManager.NotifyCapacityFreed(context.Background())
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

		resolvedInputs := vars.Snapshot()
		startedAt := time.Now()
		result, err := executor.Execute(ctx, node, vars, wf.Name)
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
			ID:             uuid.New(),
			InstanceID:     instanceID,
			NodeID:         node.ID,
			Attempt:        attempt,
			Status:         execStatus,
			ResolvedInputs: resolvedInputs,
			Output:         execOutput,
			Error:          execError,
			StartedAt:      startedAt,
			CompletedAt:    &completedAt,
		}
		if result != nil {
			if result.LLMConversation != nil {
				if m, ok := result.LLMConversation.(map[string]any); ok {
					nodeExec.LLMConversation = m
				}
			}
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
		// Skip writing when the executor itself already wrote the record (IsWait path).
		if result == nil || !result.IsWait {
			_ = e.nodeExecutions.Create(ctx, nodeExec)
		}
		lastExec = nodeExec

		if err == nil {
			return result, nodeExec, nil
		}
		lastErr = err
	}

	return nil, lastExec, lastErr
}

// parseWorkflowGraph deserializes a workflow snapshot into its definition,
// a node lookup map, and an adjacency list. This is a pure function with no
// side effects, making it straightforward to test in isolation.
func parseWorkflowGraph(snapshot map[string]any) (*workflow.Workflow, map[string]workflow.Node, map[string][]edgeTarget, error) {
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing workflow snapshot: %w", err)
	}

	nodeMap := make(map[string]workflow.Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		nodeMap[n.ID] = n
	}

	adj := make(map[string][]edgeTarget)
	for _, edge := range wf.Edges {
		adj[edge.Source] = append(adj[edge.Source], edgeTarget{
			nodeID:     edge.Target,
			outputPort: edge.Output,
		})
	}

	return &wf, nodeMap, adj, nil
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

// Pause sets a running or waiting instance to paused state. The graph walker will
// detect this on its next iteration and stop. For waiting instances, the timer
// keeps counting (do NOT cancel the scheduler entry).
func (e *Engine) Pause(instanceID uuid.UUID) error {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}

	switch inst.State {
	case instance.StateRunning:
		if err := e.instances.UpdateState(ctx, instanceID, instance.StatePaused); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.paused",
			Timestamp: time.Now(),
		})
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StateWaiting:
		// Transition to paused — timer keeps counting (do NOT cancel scheduler entry).
		if err := e.instances.UpdateState(ctx, instanceID, instance.StatePaused); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.paused",
			Timestamp: time.Now(),
		})

	default:
		return fmt.Errorf("instance %s is not running (state: %s): %w", instanceID, inst.State, instance.ErrNotRunning)
	}
	return nil
}

// Resume sets a paused instance back to its appropriate state. If the instance
// was paused from a waiting state (WaitingUntil is set), it checks whether the
// timer has expired:
//   - Timer expired: transition to queued, trigger queue evaluation
//   - Timer not expired: transition back to waiting
//
// If the instance was paused from running (WaitingUntil is nil), resume normally.
func (e *Engine) Resume(instanceID uuid.UUID) error {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StatePaused {
		return fmt.Errorf("instance %s is not paused (state: %s): %w", instanceID, inst.State, instance.ErrNotPaused)
	}

	// Check if the instance was waiting on a webhook.
	if inst.WebhookToken != nil {
		if inst.WebhookReceivedAt != nil {
			// Webhook received while paused -> go to queued.
			now := time.Now()
			if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
				return fmt.Errorf("failed to set queued: %w", err)
			}
			e.eventBus.Publish(instanceID, Event{
				Type:      "instance.resumed",
				Timestamp: time.Now(),
			})
			e.queueManager.NotifyCapacityFreed(context.Background())
			return nil
		}
		// Check if webhook timeout expired while paused (timer fired, no-oped, is now gone).
		if inst.WaitingUntil != nil && time.Now().After(*inst.WaitingUntil) {
			// Deactivate the token so no stale webhook can arrive after this point.
			if e.webhookRegistry != nil {
				e.webhookRegistry.Deactivate(*inst.WebhookToken)
			}
			failedNodeID := ""
			if inst.CurrentNodeID != nil {
				failedNodeID = *inst.CurrentNodeID
			}
			if err := e.instances.SetFailed(ctx, instanceID, failedNodeID); err != nil {
				return fmt.Errorf("failed to fail expired webhook instance on resume: %w", err)
			}
			e.eventBus.Publish(instanceID, Event{
				Type:      "instance.failed",
				Timestamp: time.Now(),
				Data:      map[string]any{"reason": "webhook timeout expired while paused"},
			})
			return nil
		}
		// No webhook received and timeout not expired -> go back to waiting.
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.resumed",
			Timestamp: time.Now(),
		})
		return nil
	}

	// Check if the instance was waiting on a timer delay.
	if inst.WaitingUntil != nil {
		if time.Now().After(*inst.WaitingUntil) {
			// Timer expired while paused -> go to queued.
			now := time.Now()
			if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
				return fmt.Errorf("failed to set queued: %w", err)
			}
			// Clear waitingUntil.
			if err := e.instances.SetWaiting(ctx, instanceID, nil); err != nil {
				return fmt.Errorf("failed to clear waiting_until: %w", err)
			}
			e.eventBus.Publish(instanceID, Event{
				Type:      "instance.resumed",
				Timestamp: time.Now(),
			})
			e.queueManager.NotifyCapacityFreed(context.Background())
			return nil
		}
		// Timer has not expired -> go back to waiting.
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.resumed",
			Timestamp: time.Now(),
		})
		return nil
	}

	// Normal resume (was running when paused) -> go back to running.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.resumed",
		Timestamp: time.Now(),
	})

	e.wg.Add(1)
	go e.run(runCtx, instanceID)
	return nil
}

// Cancel sets a running, queued, paused, or waiting instance to cancelled and
// cancels its context/timer as appropriate.
func (e *Engine) Cancel(instanceID uuid.UUID) error {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}

	switch inst.State {
	case instance.StateRunning:
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateCancelled); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.mu.Lock()
		if cancelFn, ok := e.contexts[instanceID]; ok {
			cancelFn()
		}
		e.mu.Unlock()
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.cancelled",
			Timestamp: time.Now(),
		})
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StateWaiting:
		// Deactivate webhook endpoint if this was a webhook wait.
		if e.webhookRegistry != nil && inst.WebhookToken != nil {
			e.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		// Cancel the pending timer.
		if e.scheduler != nil {
			e.scheduler.Cancel(instanceID)
		}
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateCancelled); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.cancelled",
			Timestamp: time.Now(),
		})
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StatePaused, instance.StateQueued:
		// Deactivate webhook endpoint if present (paused from webhook wait).
		if e.webhookRegistry != nil && inst.WebhookToken != nil {
			e.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateCancelled); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		// Cancel any timer that might still be running (for paused-from-waiting).
		if e.scheduler != nil && (inst.WaitingUntil != nil || inst.WebhookToken != nil) {
			e.scheduler.Cancel(instanceID)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      "instance.cancelled",
			Timestamp: time.Now(),
		})

	default:
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", instanceID, inst.State, instance.ErrCannotCancel)
	}
	return nil
}

// Retry resumes a failed instance from the failed node, optionally
// applying variable edits.
func (e *Engine) Retry(instanceID uuid.UUID, variableEdits map[string]any) error {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StateFailed {
		return fmt.Errorf("instance %s is not failed (state: %s): %w", instanceID, inst.State, instance.ErrNotFailed)
	}

	// Apply variable edits.
	if variableEdits != nil {
		for k, v := range variableEdits {
			inst.Variables[k] = v
		}
		if err := e.instances.UpdateVariables(ctx, instanceID, inst.Variables); err != nil {
			return fmt.Errorf("failed to update variables: %w", err)
		}
	}

	// Set the instance to running, keeping the current node as the failed node.
	if inst.FailedNodeID != nil {
		if err := e.instances.UpdateCurrentNode(ctx, instanceID, *inst.FailedNodeID); err != nil {
			return fmt.Errorf("failed to update current node: %w", err)
		}
	}

	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.retrying",
		Timestamp: time.Now(),
	})

	e.wg.Add(1)
	go e.run(runCtx, instanceID)
	return nil
}

// Rerun creates a new instance from a completed instance's original variables
// and triggers it. Only terminal instances (completed, failed, cancelled) can
// be rerun.
func (e *Engine) Rerun(instanceID uuid.UUID) (uuid.UUID, error) {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to get instance: %w", err)
	}

	switch inst.State {
	case instance.StateCompleted, instance.StateFailed, instance.StateCancelled:
		// ok
	default:
		return uuid.Nil, fmt.Errorf("instance %s cannot be rerun (state: %s): %w", instanceID, inst.State, instance.ErrCannotRerun)
	}

	if inst.OriginalVariables == nil {
		return uuid.Nil, fmt.Errorf("instance %s has no original variables recorded", instanceID)
	}

	id, _, err := e.Trigger(inst.WorkflowName, inst.OriginalVariables, string(inst.Priority))
	return id, err
}

// SetQueueManager sets the queue manager on the engine. This is used to break
// the circular dependency between engine and queue manager during initialization.
func (e *Engine) SetQueueManager(qm QueueManager) {
	e.queueManager = qm
}

// SetPriority changes the priority of a queued or waiting instance.
func (e *Engine) SetPriority(id uuid.UUID, priority string) error {
	ctx := context.Background()
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get instance: %w", err)
	}
	if inst.State != instance.StateQueued && inst.State != instance.StateWaiting {
		return fmt.Errorf("instance %s is not queued (state: %s): %w", id, inst.State, instance.ErrNotQueued)
	}
	return e.instances.UpdatePriority(ctx, id, instance.Priority(priority))
}

// Shutdown gracefully waits for all in-flight workflow goroutines to finish.
// It should be called before closing database connections or other resources.
func (e *Engine) Shutdown() {
	e.wg.Wait()
}

// RecoverCrashedInstances finds instances that are in running state
// (from a previous crash) and sets them to failed.
func (e *Engine) RecoverCrashedInstances() error {
	ctx := context.Background()
	running, err := e.instances.ListRunning(ctx)
	if err != nil {
		return fmt.Errorf("failed to list running instances: %w", err)
	}

	for _, inst := range running {
		failedNode := ""
		if inst.CurrentNodeID != nil {
			failedNode = *inst.CurrentNodeID
		}
		if err := e.instances.SetFailed(ctx, inst.ID, failedNode); err != nil {
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
