package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// SpawnWorkflowExecutor executes one spawn_workflow node: prepare the child's
// startup values, start the child, and tell the engine how to proceed.
//
// Every collaborator is a PORT, so the executor holds no LLM and no engine.
type SpawnWorkflowExecutor struct {
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	spawnWaits instance.SpawnWaitRepository
	preparer   ChildInputPreparer
	spawner    ChildSpawner
	identities IdentityResolver
	fileReader FileReader
	queueMgr   QueueManager
}

// NewSpawnWorkflowExecutor constructs the executor.
func NewSpawnWorkflowExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	spawnWaits instance.SpawnWaitRepository,
	preparer ChildInputPreparer,
	spawner ChildSpawner,
	identities IdentityResolver,
	fileReader FileReader,
	queueMgr QueueManager,
) *SpawnWorkflowExecutor {
	return &SpawnWorkflowExecutor{
		instances:  instances,
		nodeExecs:  nodeExecs,
		spawnWaits: spawnWaits,
		preparer:   preparer,
		spawner:    spawner,
		identities: identities,
		fileReader: fileReader,
		queueMgr:   queueMgr,
	}
}

var _ NodeExecutor = (*SpawnWorkflowExecutor)(nil)

// Execute runs one spawn node.
//
// Every failure below returns a plain error; the engine then applies the node's
// resolved ErrorBehavior (retry / default output / error-node routing). The
// executor encodes no control-flow policy of its own.
func (e *SpawnWorkflowExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: instance ID not found in context", node.ID)
	}

	// Re-check the configured contract. Instances execute from a
	// WorkflowSnapshot that may PREDATE Phase 1's validation, so the executor
	// does not trust that validation ran — the same defensive stance the binding
	// kind guard documents. An unrecognized (or empty) mode is an error, never a
	// default.
	mode := node.Config.SpawnMode
	if !workflow.IsValidSpawnMode(mode) {
		return nil, fmt.Errorf("spawn_workflow %q: unrecognized spawnMode %q (must be %q or %q)",
			node.ID, mode, workflow.SpawnModeFireAndForget, workflow.SpawnModeWaitForChild)
	}
	if node.Config.ChildWorkflow == "" {
		return nil, fmt.Errorf("spawn_workflow %q: childWorkflow is not configured", node.ID)
	}

	if mode == workflow.SpawnModeWaitForChild {
		return e.executeWait(ctx, node, vars, workflowName, instanceID)
	}
	return e.executeFireAndForget(ctx, node, vars, workflowName, instanceID)
}

// executeFireAndForget starts the child and returns a NORMAL result so the
// engine advances along the node's single unnamed edge.
func (e *SpawnWorkflowExecutor) executeFireAndForget(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	childID, err := e.prepareAndSpawn(ctx, node, vars, workflowName, instanceID, instance.SpawnModeFireAndForget)
	if err != nil {
		return nil, err
	}
	return &NodeResult{Output: spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeFireAndForget)}, nil
}

// executeWait implements the wait-for-child path.
//
// On re-entry it SHORT-CIRCUITS on the LINK rather than on node-execution
// status: the spawn node needs to know HOW the child ended in order to choose a
// port, and the child's persisted state is the single authority for that.
func (e *SpawnWorkflowExecutor) executeWait(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	link, err := e.spawnWaits.FindChildBySpawnNode(ctx, instanceID, node.ID, instance.SpawnModeWaitForChild)
	if err != nil {
		// A storage failure must NOT be mistaken for "no child yet", which would
		// spawn a second child.
		return nil, fmt.Errorf("spawn_workflow %q: look up awaited child: %w", node.ID, err)
	}
	if link != nil {
		return e.resumeFromLink(node, link)
	}

	// FIRST EXECUTION. Verify both branch ports are present in the SNAPSHOT
	// before a child is created. findNextNode returns an empty string when no
	// edge matches the port, which ends the run as if complete — so a snapshot
	// predating Phase 1's validation would silently complete the parent after the
	// child finished. Fail here instead, while nothing has been created yet.
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}
	if err := assertSpawnBranchPorts(node.ID, parent.WorkflowSnapshot); err != nil {
		return nil, err
	}

	startedAt := time.Now()

	// ORDER IS LOAD-BEARING (the deadlock guard):
	//   enqueue the child -> write the node execution record ->
	//   transition the parent to waiting -> notify that capacity was freed.
	// Parent and child are enqueued under the SAME owner and compete for the
	// same per-user capacity; with a cap of one, the child sits queued until the
	// parent's waiting transition releases the slot. Skipping the notify, or
	// transitioning before the child is enqueued, produces a stall that looks
	// like a hung workflow rather than an error.
	childID, err := e.prepareAndSpawn(ctx, node, vars, workflowName, instanceID, instance.SpawnModeWaitForChild)
	if err != nil {
		return nil, err
	}

	output := spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeWaitForChild)

	// executeWithRetry deliberately skips writing a node-execution record on the
	// wait path, so the executor writes its own — otherwise the awaited spawn
	// node would leave no trace entry at all.
	completedAt := time.Now()
	if err := e.nodeExecs.Create(ctx, &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      output,
		StartedAt:   startedAt,
		CompletedAt: &completedAt,
	}); err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: write node execution record: %w", node.ID, err)
	}

	// waiting_until stays NULL: this wait has no deadline, so the timer recovery
	// sweep (which filters on a non-null waiting_until) must not pick it up. Same
	// choice the webhook executor makes for an untimed wait.
	if err := e.instances.UpdateState(ctx, instanceID, instance.StateWaiting); err != nil {
		// The child is already enqueued and is deliberately NOT rolled back: it may
		// already be executing side effects, and cancelling it from a failing
		// parent would layer a second failure on the first. It stays visible in the
		// hierarchy as an orphaned-but-linked run.
		return nil, fmt.Errorf("spawn_workflow %q: set waiting state: %w", node.ID, err)
	}

	e.queueMgr.NotifyCapacityFreed(ctx)

	return &NodeResult{IsWait: true, Output: output}, nil
}

// resumeFromLink decides the parent's next move from an EXISTING link.
//
// Keying on the link rather than on node-execution status is deliberate: the
// interactive executor only needs to know THAT it finished, while the spawn
// node needs to know HOW the child ended in order to choose a port. The child's
// persisted state is the single authority for that; a second copy in the node
// record could disagree with it after a retry or a late cancellation.
func (e *SpawnWorkflowExecutor) resumeFromLink(node workflow.Node, link *instance.ChildLink) (*NodeResult, error) {
	output := spawnOutput(link.ChildID, node.Config.ChildWorkflow, node.Config.SpawnMode)
	output["childState"] = string(link.ChildState)

	if !link.ChildState.IsTerminal() {
		// Still active: return a wait result WITHOUT re-spawning and without
		// re-transitioning state. This is the idempotent path that makes a
		// spurious re-entry — a duplicate wake, a node-level retry — harmless.
		return &NodeResult{IsWait: true, Output: output}, nil
	}

	// Terminal: route by outcome. A CANCELLED child routes through failure, the
	// same port as a failed one — the spec is explicit that there is no separate
	// cancellation path.
	port := workflow.SpawnPortFailure
	if link.ChildState == instance.StateCompleted {
		port = workflow.SpawnPortSuccess
	}
	return &NodeResult{OutputPort: port, Output: output}, nil
}

// prepareAndSpawn is the sequence both modes share: reconstruct the parent
// owner's identity, load the child's declared inputs through it, read optional
// guidance, prepare the startup values, and create the linked child.
func (e *SpawnWorkflowExecutor) prepareAndSpawn(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID, mode instance.SpawnMode) (uuid.UUID, error) {
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}

	// Reconstruct the PARENT OWNER's identity. A resolution failure is NOT
	// downgraded to a system identity: falling back would silently change which
	// workflows are visible to the spawn and could widen reach beyond the owner.
	ownerIdentity, err := e.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: resolve owner identity: %w", node.ID, err)
	}
	childCtx := identity.WithIdentity(ctx, ownerIdentity)

	// If the parent's owner cannot see the child workflow this returns not-found
	// and the node fails — fail closed, with no existence leak.
	inputs, err := e.spawner.ChildInputs(childCtx, node.Config.ChildWorkflow)
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: load child workflow %q: %w",
			node.ID, node.Config.ChildWorkflow, err)
	}

	guidance := ""
	if node.Config.TemplateFile != "" {
		b, rerr := e.fileReader.ReadFile(ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
		if rerr != nil {
			return uuid.Nil, fmt.Errorf("spawn_workflow %q: read guidance %q: %w",
				node.ID, node.Config.TemplateFile, rerr)
		}
		guidance = variable.Resolve(string(b), vars)
	}

	workDir, _ := WorkDirFromVars(vars)
	values, err := e.preparer.Prepare(childCtx, ChildInputRequest{
		NodeID:        node.ID,
		ChildWorkflow: node.Config.ChildWorkflow,
		Inputs:        inputs,
		ParentContext: vars.Snapshot(),
		Guidance:      guidance,
		ProfileAlias:  node.Config.ModelProfile,
		WorkDir:       workDir,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", node.ID, err)
	}

	childID, err := e.spawner.Spawn(childCtx, SpawnRequest{
		ChildWorkflow: node.Config.ChildWorkflow,
		Variables:     values,
		Priority:      string(parent.Priority),
		Link: instance.SpawnLink{
			ParentInstanceID: instanceID,
			ParentNodeID:     node.ID,
			Mode:             mode,
		},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, err)
	}
	return childID, nil
}

// spawnOutput is the ONE shape of a spawn node's output. Recording the child id
// makes the child addressable from the parent's variable store, so downstream
// nodes and the trace can reference the spawned run.
func spawnOutput(childID uuid.UUID, childWorkflow string, mode string) map[string]any {
	return map[string]any{
		"childInstanceId": childID.String(),
		"childWorkflow":   childWorkflow,
		"spawnMode":       mode,
	}
}

// assertSpawnBranchPorts verifies that BOTH the success and failure ports have
// an outgoing edge in the parent's snapshot.
func assertSpawnBranchPorts(nodeID string, snapshot map[string]any) error {
	wf, _, _, err := parseWorkflowGraph(snapshot)
	if err != nil {
		return fmt.Errorf("spawn_workflow %q: parse workflow snapshot: %w", nodeID, err)
	}
	ports := map[string]bool{}
	for _, edge := range wf.Edges {
		if edge.Source == nodeID {
			ports[edge.Output] = true
		}
	}
	for _, want := range []string{workflow.SpawnPortSuccess, workflow.SpawnPortFailure} {
		if !ports[want] {
			return fmt.Errorf("spawn_workflow %q: wait_for_child requires an outgoing edge for the %q port; "+
				"without it the parent would silently complete when the child finishes", nodeID, want)
		}
	}
	return nil
}
