package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Engine is the core workflow execution engine. It orchestrates the loading,
// validation, and step-by-step execution of workflow graphs.
type Engine struct {
	workflows        workflow.WorkflowRepository
	instances        instance.InstanceRepository
	nodeExecutions   instance.NodeExecutionRepository
	snapshots        instance.VariableSnapshotRepository
	users            instance.UserRepository
	executors        map[workflow.NodeType]NodeExecutor
	eventBus         EventBus
	queueManager     QueueManager
	queueRepo        instance.QueueRepository
	scheduler        TimerScheduler
	webhookRegistry  WebhookRegistrar
	notifications    notification.Repository
	dismissals       instance.DismissalRepository
	externalNotifier notification.ExternalNotifier
	interventions    instance.InterventionRepository
	workspaceManager WorkspaceManager
	publishers       workflow.PublisherLookup // optional; gates the trigger read path by visibility
	mu               sync.Mutex
	contexts         map[uuid.UUID]context.CancelFunc
	wg               sync.WaitGroup
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

// SetNotifications sets the notification repository on the engine.
func (e *Engine) SetNotifications(repo notification.Repository) {
	e.notifications = repo
}

// SetDismissals wires the per-user dismissal repository. When set, Retry clears
// an instance's dismissals (for all users) so a retried item resurfaces. When
// unset, retry behaves exactly as before.
func (e *Engine) SetDismissals(repo instance.DismissalRepository) {
	e.dismissals = repo
}

// SetExternalNotifier sets the external notifier on the engine. When set,
// every notification emitted by the engine will also be forwarded to this
// notifier (e.g., for email, Slack, or logging).
func (e *Engine) SetExternalNotifier(n notification.ExternalNotifier) {
	e.externalNotifier = n
}

// SetWorkspaceManager wires the per-instance workspace manager. When unset,
// workspace directories are not created and {{workspace}} is not seeded.
func (e *Engine) SetWorkspaceManager(wm WorkspaceManager) {
	e.workspaceManager = wm
}

// SetInterventions wires the intervention audit repository. When unset,
// attribution rows are not written (nil-safe), but the instance.intervened
// event is still published so live viewers see admin actions.
func (e *Engine) SetInterventions(repo instance.InterventionRepository) {
	e.interventions = repo
}

// SetPublisherLookup wires the publication lookup used to gate the trigger read
// path by workflow visibility. When unset (nil), all shared workflows are
// triggerable (local/read-only mode), preserving today's behavior.
func (e *Engine) SetPublisherLookup(p workflow.PublisherLookup) {
	e.publishers = p
}

// recordIntervention writes an audit row and publishes instance.intervened when
// the acting identity is NOT the instance owner. Owner self-actions (and
// identity-less system actions) are skipped. Attribution is best-effort: a
// repo write failure is logged, never returned, so the operation that already
// succeeded is not undone.
func (e *Engine) recordIntervention(ctx context.Context, inst *instance.Instance, action string, detail *string) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.UserID == inst.OwnerID {
		return // owner self-action (or no identity): not logged
	}
	if e.interventions != nil {
		iv := &instance.Intervention{
			ID:          uuid.New(),
			InstanceID:  inst.ID,
			ActorUserID: id.UserID,
			Action:      action,
			Detail:      detail,
			CreatedAt:   time.Now(),
		}
		if err := e.interventions.Create(ctx, iv); err != nil {
			slog.Error("failed to record intervention", "instanceID", inst.ID, "error", err)
		}
	}
	e.eventBus.Publish(inst.ID, Event{
		Type:      "instance.intervened",
		Timestamp: time.Now(),
		Data:      map[string]any{"actorUserId": id.UserID.String(), "action": action},
	})
}

// edgeTarget describes a destination from a graph edge.
type edgeTarget struct {
	nodeID     string
	outputPort string
}

// triggerOwner returns the triggering user's id: the context identity when
// present (REST/MCP triggers), otherwise the default user (scheduler/webhook
// system triggers that carry no identity). The bool is false only when no
// identity is present and the default user cannot be resolved.
func triggerOwner(ctx context.Context, users instance.UserRepository) (uuid.UUID, bool) {
	if id, ok := identity.FromContext(ctx); ok {
		return id.UserID, true
	}
	def, err := users.GetDefaultUser(ctx)
	if err != nil {
		return uuid.Nil, false
	}
	return def, true
}

// Trigger loads a workflow by name, validates inputs, creates a new instance,
// and delegates to the queue manager for capacity-based scheduling.
// The second return value indicates whether the instance started immediately (true)
// or stayed in queued state (false).
func (e *Engine) Trigger(ctx context.Context, workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Resolve the triggering owner: the context identity for REST/MCP triggers,
	// or the default user for system triggers (scheduler/webhook) that carry no
	// identity. The workflow is resolved across the owner's visible scopes
	// (private first, then shared) so a shared workflow runs directly with no
	// private copy.
	owner, ok := triggerOwner(ctx, e.users)
	if !ok {
		return uuid.Nil, false, fmt.Errorf("failed to resolve triggering user")
	}

	// Gate the shared read path by visibility. The owner above may be the default
	// system user; gating keys on the CONTEXT identity (a REST/MCP caller). A
	// system trigger (no context identity) skips gating — a scheduler/webhook
	// trigger already passed authorization at registration time.
	var filter workflow.SharedVisibilityFilter
	if id, ok := identity.FromContext(ctx); ok && e.publishers != nil {
		pubs := e.publishers
		filter = func(ctx context.Context, name string) (bool, error) {
			pub, found, err := pubs.PublicationOf(ctx, name)
			if err != nil {
				return false, err
			}
			if !found {
				return true, nil // unpublished shared folder ⇒ open
			}
			return workflow.CanSeePublication(id, pub), nil
		}
	}
	wf, _, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)
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
				if err := vars.SetInput(inp.Name, inp.Default); err != nil {
					return uuid.Nil, false, fmt.Errorf("seed input default %q: %w", inp.Name, err)
				}
			}
		} else {
			if err := vars.SetInput(inp.Name, val); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed input %q: %w", inp.Name, err)
			}
		}
	}

	// Also set any extra variables that don't match input definitions.
	// SECURITY: this path accepts arbitrary caller-supplied keys; SetInput's
	// reserved-name guard prevents writing to engine-seeded variables like
	// "workspace".
	for k, v := range variables {
		if _, exists := vars.Get(k); !exists {
			if err := vars.SetInput(k, v); err != nil {
				return uuid.Nil, false, fmt.Errorf("seed extra variable %q: %w", k, err)
			}
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

	// OwnerID is the triggering user resolved above (the context identity, or the
	// default user for system triggers). A shared workflow therefore runs in the
	// triggering user's queue, independent of the publisher.
	ownerID := owner

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

// ListInstances returns the instances visible to the caller. The owner filter
// is derived from the request identity: a non-admin sees only their own
// instances; an admin sees all. Any OwnerID the caller set on filters is
// overwritten here so scope cannot be widened from request input. A request
// with no identity returns ErrNotFound (reveal nothing) and never queries the
// repository.
func (e *Engine) ListInstances(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	owner, ok := ownerConstraint(ctx)
	if !ok {
		return nil, instance.ErrNotFound // no identity: reveal nothing
	}
	filters.OwnerID = owner // service-set; ignore any caller-supplied value
	return e.instances.List(ctx, filters)
}

// GetInstance loads an instance the caller may view, else ErrNotFound. Both an
// absent instance and a not-owned instance collapse to ErrNotFound so existence
// is never leaked to a non-owner non-admin.
func (e *Engine) GetInstance(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, err // already ErrNotFound-wrapped on absence; infra errors pass through
	}
	if inst == nil {
		// Defensive: a repo that returns (nil, nil) is treated as absence so the
		// caller never dereferences a nil instance and existence is not leaked.
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if !canView(ctx, inst.OwnerID) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

// authorizeView is the shared guard for the sub-resource reads (trace,
// variables, history): it loads the owning instance and returns ErrNotFound if
// the caller may not view it. It returns the loaded instance for callers that
// need its fields (e.g. GetVariables). Sub-resource repos are only touched after
// this guard passes, so a non-viewer never causes a node-execution or snapshot
// read.
func (e *Engine) authorizeView(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	return e.GetInstance(ctx, id)
}

// authorizeIntervention loads the instance and confirms the caller may act on
// it (owner or admin). It returns the loaded instance so the caller reuses it
// (avoiding a second Get). A non-viewer gets ErrNotFound so a write probe cannot
// distinguish "exists but forbidden" from "absent" (existence not leaked),
// consistent with the read posture.
func (e *Engine) authorizeIntervention(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, err := e.instances.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance: %w", err)
	}
	if inst == nil {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if !canView(ctx, inst.OwnerID) {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return inst, nil
}

// GetTrace returns the ordered node executions for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetTrace(ctx context.Context, id uuid.UUID) ([]instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.ListByInstance(ctx, id)
}

// GetNodeTrace returns a single node execution for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetNodeTrace(ctx context.Context, id uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.nodeExecutions.GetByInstanceAndNode(ctx, id, nodeID)
}

// GetVariables returns the current variables for an instance the caller may
// view, else ErrNotFound.
func (e *Engine) GetVariables(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	inst, err := e.authorizeView(ctx, id)
	if err != nil {
		return nil, err
	}
	return inst.Variables, nil
}

// GetVariableHistory returns the variable snapshot history for an instance the
// caller may view, else ErrNotFound.
func (e *Engine) GetVariableHistory(ctx context.Context, id uuid.UUID) ([]instance.VariableSnapshot, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	return e.snapshots.ListByInstance(ctx, id)
}

// GetInterventions returns the intervention audit trail for an instance the
// caller may view (owner or admin), else ErrNotFound. When no intervention
// repository is wired it returns an empty list.
func (e *Engine) GetInterventions(ctx context.Context, id uuid.UUID) ([]instance.Intervention, error) {
	if _, err := e.authorizeView(ctx, id); err != nil {
		return nil, err
	}
	if e.interventions == nil {
		return []instance.Intervention{}, nil
	}
	return e.interventions.ListByInstance(ctx, id)
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

	// capacityFreed tracks whether capacity notification was already handled
	// by an executor (e.g., timer/webhook wait) or an external caller (pause/cancel).
	// The deferred cleanup only fires NotifyCapacityFreed if this remains false.
	capacityFreed := false

	// workflowName is set after the instance is loaded; the panic deferred function
	// reads it via closure so notifications carry the correct workflow name.
	var workflowName string

	defer func() {
		if r := recover(); r != nil {
			// A panic in the run loop should not crash the process.
			// Mark the instance as failed and log via event bus.
			if err := e.instances.SetFailed(ctx, instanceID, ""); err == nil {
				e.emitNotification(context.Background(), instanceID, workflowName, notification.TypeFailed)
			}
			e.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInstanceFailed,
				Timestamp: time.Now(),
				Data:      map[string]any{"error": fmt.Sprintf("panic: %v", r)},
			})
			capacityFreed = false // ensure we notify below
		}
		if !capacityFreed {
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
		return
	}
	workflowName = inst.WorkflowName

	// Carry the instance owner into the run context so file-reading executors
	// resolve workflow files from the owner's private workspace (with a shared
	// fallback) via the scope-aware FileReader.
	ctx = withOwner(ctx, inst.OwnerID)

	// Parse the workflow from the snapshot.
	wf, nodeMap, adj, parseErr := parseWorkflowGraph(inst.WorkflowSnapshot)
	if parseErr != nil {
		_ = e.instances.SetFailed(ctx, instanceID, "")
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
		return
	}

	// Reconstruct variable store using node IDs so object-valued inputs are not
	// misclassified as node outputs.
	nodeIDs := make(map[string]bool, len(nodeMap))
	for id := range nodeMap {
		nodeIDs[id] = true
	}
	vars := variable.FromSnapshotWithNodeIDs(inst.Variables, nodeIDs)

	if e.workspaceManager != nil {
		if !e.prepareWorkspace(ctx, instanceID, currentNodeID, vars) {
			return
		}
	}

	for currentNodeID != "" {
		// TODO(race): There is a TOCTOU race between Pause() writing StatePaused
		// and this loop reading the fresh state. If the pause arrives between Get()
		// and the next node execution, one extra node may execute. Fix: use
		// "UPDATE ... SET state = 'paused' WHERE state = 'running' RETURNING ..."
		// and check the affected row count to detect concurrent state changes.

		// Check for pause/cancel by re-reading instance state.
		freshInst, err := e.instances.Get(ctx, instanceID)
		if err != nil {
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}

		if freshInst.State == instance.StatePaused || freshInst.State == instance.StateCancelled {
			// The instance was paused/cancelled externally. Stop execution.
			// The caller already called NotifyCapacityFreed.
			capacityFreed = true
			return
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			capacityFreed = true
			return
		default:
		}

		node, exists := nodeMap[currentNodeID]
		if !exists {
			e.failInstance(ctx, instanceID, currentNodeID)
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
			e.failInstance(ctx, instanceID, currentNodeID)
			return
		}

		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeNodeStarted,
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
		})

		// Execute the node with retry logic. Each attempt is individually recorded.
		execCtx := ContextWithInstanceID(ctx, instanceID)
		result, nodeExec, execErr := e.executeWithRetry(execCtx, instanceID, node, vars, *wf)

		if execErr != nil {
			nextNodeID, halt := e.handleNodeError(ctx, instanceID, workflowName, currentNodeID, node, *wf, nodeExec, execErr, vars, adj)
			if halt {
				return
			}
			currentNodeID = nextNodeID
		} else {
			nextNodeID, halt := e.handleNodeSuccess(ctx, instanceID, workflowName, currentNodeID, node, result, vars, adj, &capacityFreed)
			if halt {
				return
			}
			currentNodeID = nextNodeID
		}
	}

	// All nodes executed successfully.
	if err := e.instances.SetCompleted(ctx, instanceID); err != nil {
		slog.Error("failed to set instance completed", "instanceID", instanceID, "error", err)
		// Persistence failed: keep the workspace for debugging/retry. Log
		// the retention with size/file-count so the operator can see how
		// much sensitive data is sitting on disk.
		e.warnWorkspaceRetained(instanceID, "set_completed_failed")
	} else {
		e.emitNotification(ctx, instanceID, workflowName, notification.TypeCompleted)
		// Workspace is removed only after SetCompleted succeeds.
		e.cleanupWorkspace(instanceID)
	}
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInstanceCompleted,
		Timestamp: time.Now(),
	})
}

// failInstance marks an instance as failed and logs errors. It consolidates
// the repeated SetFailed + slog.Error pattern used throughout the run loop.
// It returns true if the state change succeeded, false otherwise.
//
// SF9: on failure the workspace is intentionally NOT removed — it is retained
// for debugging (the success path in run() removes it after SetCompleted). A
// slog.Warn with size/file-count is emitted so the sensitive-data-retention
// surface is visible to operators.
func (e *Engine) failInstance(ctx context.Context, instanceID uuid.UUID, nodeID string) bool {
	if err := e.instances.SetFailed(ctx, instanceID, nodeID); err != nil {
		slog.Error("failed to set instance failed", "instanceID", instanceID, "error", err)
		return false
	}
	e.warnWorkspaceRetained(instanceID, "instance_failed")
	return true
}

// handleNodeError processes a node execution failure according to the resolved
// error behavior. It returns the next node ID to execute and whether the run
// loop should halt.
func (e *Engine) handleNodeError(
	ctx context.Context,
	instanceID uuid.UUID,
	workflowName string,
	currentNodeID string,
	node workflow.Node,
	wf workflow.Workflow,
	nodeExec *instance.NodeExecution,
	execErr error,
	vars *variable.VariableStore,
	adj map[string][]edgeTarget,
) (nextNodeID string, halt bool) {
	// Cancellation race: when the run context is cancelled (e.g. via Cancel()),
	// the executing node returns context.Canceled. That is a cancellation, NOT a
	// node failure — marking the instance Failed here would clobber the Cancelled
	// state Cancel() set. Halt without changing state; the cancel path owns it.
	if ctx.Err() != nil || errors.Is(execErr, context.Canceled) {
		return "", true
	}

	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeNodeFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type), "error": execErr.Error()},
	})

	eb := resolveErrorBehavior(node, wf)

	switch eb.Type {
	case "skip":
		if nodeExec != nil {
			nodeExec.Status = instance.NodeStatusSkipped
			_ = e.nodeExecutions.Update(ctx, nodeExec)
		}
		if eb.DefaultOutput != nil {
			vars.SetNodeOutput(currentNodeID, eb.DefaultOutput)
		}
		if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
			e.failInstance(ctx, instanceID, currentNodeID)
			return "", true
		}
		// Conditional nodes route via their declared default output port so
		// the skip path is deterministic rather than arbitrary-first-edge.
		skipPort := ""
		if node.Type == workflow.NodeTypeConditional {
			skipPort = node.Config.DefaultOutput
		}
		return e.findNextNode(currentNodeID, skipPort, adj), false

	case "route":
		if eb.ErrorNodeID != "" {
			return eb.ErrorNodeID, false
		}
		e.failInstance(ctx, instanceID, currentNodeID)
		return "", true

	default: // "halt" or unrecognized
		if e.failInstance(ctx, instanceID, currentNodeID) {
			e.emitNotification(ctx, instanceID, workflowName, notification.TypeFailed)
		}
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeInstanceFailed,
			Timestamp: time.Now(),
			Data:      map[string]any{"failedNodeId": currentNodeID, "error": execErr.Error()},
		})
		return "", true
	}
}

// handleNodeSuccess processes a successful node execution, updating variables
// and determining the next node. It returns the next node ID and whether the
// run loop should halt (e.g., for wait results).
func (e *Engine) handleNodeSuccess(
	ctx context.Context,
	instanceID uuid.UUID,
	workflowName string,
	currentNodeID string,
	node workflow.Node,
	result *NodeResult,
	vars *variable.VariableStore,
	adj map[string][]edgeTarget,
	capacityFreed *bool,
) (nextNodeID string, halt bool) {
	// Check if this is a wait result (timer delay, webhook wait, etc.)
	if result != nil && result.IsWait {
		e.eventBus.Publish(instanceID, Event{
			Type:      EventTypeNodeCompleted,
			Timestamp: time.Now(),
			Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
		})
		if workflow.IsInteractive(node.Type) {
			e.emitNotification(ctx, instanceID, workflowName, notification.TypeNeedsInteraction)
		}
		*capacityFreed = true
		return "", true
	}

	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeNodeCompleted,
		Timestamp: time.Now(),
		Data:      map[string]any{"nodeId": currentNodeID, "nodeType": string(node.Type)},
	})

	// Update variables with node output.
	if result != nil && result.Output != nil {
		vars.SetNodeOutput(currentNodeID, result.Output)
	}
	if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
		e.failInstance(ctx, instanceID, currentNodeID)
		return "", true
	}

	// Find next node.
	outputPort := ""
	if result != nil {
		outputPort = result.OutputPort
	}
	return e.findNextNode(currentNodeID, outputPort, adj), false
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
		nodeResultToExecution(result, nodeExec)
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

// nodeResultToExecution maps optional NodeResult fields onto a NodeExecution
// record. This consolidates the repeated field-copying logic from executeWithRetry.
func nodeResultToExecution(result *NodeResult, exec *instance.NodeExecution) {
	if result == nil {
		return
	}
	// LLMConversation is typed as any in NodeResult because different LLM
	// providers return different shapes. Only map[string]any values are stored;
	// non-map values (e.g., from a provider returning a typed struct that was
	// not JSON-round-tripped) are silently dropped since the DB column is JSONB.
	if result.LLMConversation != nil {
		if m, ok := result.LLMConversation.(map[string]any); ok {
			exec.LLMConversation = m
		}
	}
	if result.LLMModel != "" {
		exec.LLMModel = &result.LLMModel
	}
	if result.LLMTokensIn > 0 {
		exec.LLMTokensIn = &result.LLMTokensIn
	}
	if result.LLMTokensOut > 0 {
		exec.LLMTokensOut = &result.LLMTokensOut
	}
	if result.ScriptStdout != "" {
		exec.ScriptStdout = &result.ScriptStdout
	}
	if result.ScriptStderr != "" {
		exec.ScriptStderr = &result.ScriptStderr
	}
	if result.ScriptExitCode != 0 {
		exec.ScriptExitCode = &result.ScriptExitCode
	}
}

// emitNotification creates a notification and publishes a notification.created event.
// It is a no-op if the notification repository has not been wired.
//
// TODO: if a 4th notification type is added, or routing logic becomes necessary,
// extract a dedicated NotificationListener that subscribes to the event bus
// instead of embedding this logic in Engine.
func (e *Engine) emitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type) {
	if e.notifications == nil {
		return
	}
	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   instanceID,
		WorkflowName: workflowName,
		Type:         notifType,
		CreatedAt:    time.Now(),
	}
	if err := e.notifications.Create(ctx, n); err != nil {
		slog.Error("failed to create notification", "instanceID", instanceID, "error", err)
		return
	}
	e.eventBus.Publish(instanceID, Event{
		Type:      "notification.created",
		Timestamp: time.Now(),
		Data:      notificationEventData(n),
	})
	if e.externalNotifier != nil {
		if err := e.externalNotifier.Notify(ctx, n); err != nil {
			slog.Error("external notifier failed", "instanceID", instanceID, "error", err)
		}
	}
}

// notificationEventData converts a notification into the map shape expected
// by the frontend's NotificationItem interface.
func notificationEventData(n *notification.Notification) map[string]any {
	return map[string]any{
		"id":           n.ID.String(),
		"instanceId":   n.InstanceID.String(),
		"workflowName": n.WorkflowName,
		"type":         string(n.Type),
		"read":         n.Read,
		"dismissed":    n.Dismissed,
		"createdAt":    n.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	}
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
func (e *Engine) Pause(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
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
	e.recordIntervention(ctx, inst, instance.ActionPause, nil)
	return nil
}

// Resume sets a paused instance back to its appropriate state. If the instance
// was paused from a waiting state (WaitingUntil is set), it checks whether the
// timer has expired:
//   - Timer expired: transition to queued, trigger queue evaluation
//   - Timer not expired: transition back to waiting
//
// If the instance was paused from running (WaitingUntil is nil), resume normally.
func (e *Engine) Resume(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.State != instance.StatePaused {
		return fmt.Errorf("instance %s is not paused (state: %s): %w", instanceID, inst.State, instance.ErrNotPaused)
	}

	var resumeErr error
	switch {
	case inst.WebhookToken != nil:
		resumeErr = e.resumeFromWebhookWait(ctx, instanceID, inst)
	case inst.WaitingUntil != nil:
		resumeErr = e.resumeFromTimerWait(ctx, instanceID, inst)
	default:
		resumeErr = e.resumeFromRunning(ctx, instanceID)
	}
	if resumeErr != nil {
		return resumeErr
	}
	e.recordIntervention(ctx, inst, instance.ActionResume, nil)
	return nil
}

// resumeFromWebhookWait handles resume for instances paused during a webhook wait.
func (e *Engine) resumeFromWebhookWait(ctx context.Context, instanceID uuid.UUID, inst *instance.Instance) error {
	// Webhook received while paused -> go to queued.
	if inst.WebhookReceivedAt != nil {
		now := time.Now()
		if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
			return fmt.Errorf("failed to set queued: %w", err)
		}
		e.publishResumedEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())
		return nil
	}

	// Webhook timeout expired while paused -> fail.
	if inst.WaitingUntil != nil && time.Now().After(*inst.WaitingUntil) {
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
			Type:      EventTypeInstanceFailed,
			Timestamp: time.Now(),
			Data:      map[string]any{"reason": "webhook timeout expired while paused"},
		})
		return nil
	}

	// No webhook received and timeout not expired -> go back to waiting.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}
	e.publishResumedEvent(instanceID)
	return nil
}

// resumeFromTimerWait handles resume for instances paused during a timer delay.
func (e *Engine) resumeFromTimerWait(ctx context.Context, instanceID uuid.UUID, inst *instance.Instance) error {
	// Timer expired while paused -> go to queued.
	if time.Now().After(*inst.WaitingUntil) {
		now := time.Now()
		if err := e.queueRepo.SetQueued(ctx, instanceID, now); err != nil {
			return fmt.Errorf("failed to set queued: %w", err)
		}
		if err := e.instances.SetWaiting(ctx, instanceID, nil); err != nil {
			return fmt.Errorf("failed to clear waiting_until: %w", err)
		}
		e.publishResumedEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())
		return nil
	}

	// Timer has not expired -> go back to waiting.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}
	e.publishResumedEvent(instanceID)
	return nil
}

// resumeFromRunning handles resume for instances that were running when paused.
func (e *Engine) resumeFromRunning(ctx context.Context, instanceID uuid.UUID) error {
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateRunning); err != nil {
		return fmt.Errorf("failed to update state: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	e.contexts[instanceID] = cancel
	e.mu.Unlock()

	e.publishResumedEvent(instanceID)

	e.wg.Add(1)
	go e.run(runCtx, instanceID)
	return nil
}

// publishResumedEvent emits a standard instance.resumed event.
func (e *Engine) publishResumedEvent(instanceID uuid.UUID) {
	e.eventBus.Publish(instanceID, Event{
		Type:      "instance.resumed",
		Timestamp: time.Now(),
	})
}

// Cancel sets a running, queued, paused, or waiting instance to cancelled and
// cancels its context/timer as appropriate.
func (e *Engine) Cancel(ctx context.Context, instanceID uuid.UUID) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
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
		e.publishCancelledEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StateWaiting:
		e.deactivateWaitResources(instanceID, inst)
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateCancelled); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.publishCancelledEvent(instanceID)
		e.queueManager.NotifyCapacityFreed(context.Background())

	case instance.StatePaused, instance.StateQueued:
		e.deactivateWaitResources(instanceID, inst)
		if err := e.instances.UpdateState(ctx, instanceID, instance.StateCancelled); err != nil {
			return fmt.Errorf("failed to update state: %w", err)
		}
		e.publishCancelledEvent(instanceID)

	default:
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", instanceID, inst.State, instance.ErrCannotCancel)
	}
	e.recordIntervention(ctx, inst, instance.ActionCancel, nil)
	return nil
}

// deactivateWaitResources cleans up webhook tokens and timers associated with a
// waiting or paused-from-waiting instance.
func (e *Engine) deactivateWaitResources(instanceID uuid.UUID, inst *instance.Instance) {
	if e.webhookRegistry != nil && inst.WebhookToken != nil {
		e.webhookRegistry.Deactivate(*inst.WebhookToken)
	}
	if e.scheduler != nil && (inst.WaitingUntil != nil || inst.WebhookToken != nil) {
		e.scheduler.Cancel(instanceID)
	}
}

// publishCancelledEvent emits a standard instance.cancelled event.
func (e *Engine) publishCancelledEvent(instanceID uuid.UUID) {
	e.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInstanceCancelled,
		Timestamp: time.Now(),
	})
}

// Retry resumes a failed instance from the failed node, optionally
// applying variable edits.
func (e *Engine) Retry(ctx context.Context, instanceID uuid.UUID, variableEdits map[string]any) error {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return err
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

	// Resurface on a new outcome: clear this instance's dismissals for every
	// user so the now-running item reappears in Active and, when it reaches a
	// fresh terminal state, in Completed/Failed. A new result is new information
	// the user should see. This MUST run before flipping the instance to running:
	// if the clear fails after the state flip, the instance would be stranded in
	// running with no run goroutine (an unrecoverable stuck-running window). By
	// clearing first, a clear failure aborts the retry while the instance is
	// still failed and retryable.
	if e.dismissals != nil {
		if err := e.dismissals.ClearForInstance(ctx, instanceID); err != nil {
			return fmt.Errorf("failed to clear dismissals on retry: %w", err)
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
	e.recordIntervention(ctx, inst, instance.ActionRetry, nil)
	return nil
}

// Rerun creates a new instance from a completed instance's original variables
// and triggers it. Only terminal instances (completed, failed, cancelled) can
// be rerun.
func (e *Engine) Rerun(ctx context.Context, instanceID uuid.UUID) (uuid.UUID, error) {
	inst, err := e.authorizeIntervention(ctx, instanceID)
	if err != nil {
		return uuid.Nil, err
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

	// Forward the request ctx into Trigger so the new instance is owned by the
	// acting caller (Phase 3 Task 7's triggerOwner resolves the identity from ctx,
	// falling back to the default user for system triggers). The new instance is
	// independently scoped and owned by the actor, not the source's owner.
	//
	// Rerun creates a brand-new instance id via Trigger; new instances have no
	// dismissal rows, so the rerun is inherently un-dismissed for every user.
	// The original instance's historical dismissal (if any) is intentionally
	// left intact. No dismissal bookkeeping is needed here.
	id, _, err := e.Trigger(ctx, inst.WorkflowName, inst.OriginalVariables, string(inst.Priority))
	if err != nil {
		return id, err
	}
	// Attribute on the SOURCE instance so its owner sees an admin reran it; the
	// detail is the new instance's ID. The new instance is owned by the actor and
	// needs no attribution of its own.
	newIDStr := id.String()
	e.recordIntervention(ctx, inst, instance.ActionRerun, &newIDStr)
	return id, nil
}

// SetQueueManager sets the queue manager on the engine. This is used to break
// the circular dependency between engine and queue manager during initialization.
func (e *Engine) SetQueueManager(qm QueueManager) {
	e.queueManager = qm
}

// SetPriority changes the priority of a queued or waiting instance.
func (e *Engine) SetPriority(ctx context.Context, id uuid.UUID, priority string) error {
	inst, err := e.authorizeIntervention(ctx, id)
	if err != nil {
		return err
	}
	if inst.State != instance.StateQueued && inst.State != instance.StateWaiting {
		return fmt.Errorf("instance %s is not queued (state: %s): %w", id, inst.State, instance.ErrNotQueued)
	}
	if err := e.instances.UpdatePriority(ctx, id, instance.Priority(priority)); err != nil {
		return err
	}
	p := priority
	e.recordIntervention(ctx, inst, instance.ActionSetPriority, &p)
	return nil
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
