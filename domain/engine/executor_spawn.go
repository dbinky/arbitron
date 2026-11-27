package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

// ErrSpawnUnknownChildInput is returned when a spawn node's inputs map names a
// key the child workflow does not declare.
var ErrSpawnUnknownChildInput = errors.New("engine: spawn maps an input the child workflow does not declare")

// Provenance of each value handed to a spawned child, recorded in the node
// output as inputSources (names only, never values).
const (
	inputSourceMapped   = "mapped"
	inputSourceDefault  = "default"
	inputSourcePrepared = "prepared"
)

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

	// Re-check the configured contract through the SAME parser author-time
	// validation uses. Instances execute from a WorkflowSnapshot that may PREDATE
	// that validation, so the executor does not trust that validation ran — the
	// same defensive stance the binding kind guard documents. Sharing the parser
	// is what makes the two checks provably identical instead of two hand-copied
	// string comparisons that can drift. An unrecognized (or empty) mode is an
	// error, never a default.
	cfg, cfgErr := workflow.ParseSpawnConfig(node.Config)
	if cfgErr != nil {
		return nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, cfgErr)
	}
	mode := cfg.Mode

	if mode == workflow.SpawnModeWaitForChild {
		return e.executeWait(ctx, node, cfg, vars, workflowName, instanceID)
	}
	return e.executeFireAndForget(ctx, node, cfg, vars, workflowName, instanceID)
}

// executeFireAndForget starts the child and returns a NORMAL result so the
// engine advances along the node's single unnamed edge.
func (e *SpawnWorkflowExecutor) executeFireAndForget(ctx context.Context, node workflow.Node, cfg workflow.SpawnConfig, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	childID, sources, err := e.prepareAndSpawn(ctx, node, cfg, vars, workflowName, instanceID, instance.SpawnModeFireAndForget)
	if err != nil {
		return nil, err
	}
	return &NodeResult{Output: spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeFireAndForget, sources)}, nil
}

// executeWait implements the wait-for-child path.
//
// On re-entry it SHORT-CIRCUITS on the LINK rather than on node-execution
// status: the spawn node needs to know HOW the child ended in order to choose a
// port, and the child's persisted state is the single authority for that.
func (e *SpawnWorkflowExecutor) executeWait(ctx context.Context, node workflow.Node, cfg workflow.SpawnConfig, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID) (*NodeResult, error) {
	link, err := e.spawnWaits.FindChildBySpawnNode(ctx, instanceID, node.ID, instance.SpawnModeWaitForChild)
	if err != nil {
		// A storage failure must NOT be mistaken for "no child yet", which would
		// spawn a second child.
		return nil, fmt.Errorf("spawn_workflow %q: look up awaited child: %w", node.ID, err)
	}
	if link != nil {
		return e.resumeFromLink(ctx, node, link)
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
	childID, sources, err := e.prepareAndSpawn(ctx, node, cfg, vars, workflowName, instanceID, instance.SpawnModeWaitForChild)
	if err != nil {
		return nil, err
	}

	output := spawnOutput(childID, node.Config.ChildWorkflow, workflow.SpawnModeWaitForChild, sources)

	// executeWithRetry deliberately skips writing a node-execution record on the
	// wait path, so the executor writes its own — otherwise the awaited spawn
	// node would leave no trace entry at all.
	completedAt := time.Now()
	rec := &instance.NodeExecution{
		ID:          uuid.New(),
		InstanceID:  instanceID,
		NodeID:      node.ID,
		Attempt:     1,
		Status:      instance.NodeStatusSucceeded,
		Output:      output,
		StartedAt:   startedAt,
		CompletedAt: &completedAt,
	}
	// Input preparation may have run on a runner; carry its placement facts
	// onto this hand-written record, as executeWithRetry does for other nodes.
	if sink, ok := PlacementSinkFromContext(ctx); ok {
		applyPlacement(rec, sink.Facts())
	}
	if err := e.nodeExecs.Create(ctx, rec); err != nil {
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

	// THE LOST-WAKE GUARD.
	//
	// Everything above assumes the child is still running when the parent
	// suspends. It very often is not. Parent and child compete for the SAME
	// per-user capacity, and at any cap above one the child starts, runs, and can
	// reach a terminal state WHILE the parent is still `running` here — before
	// the `waiting` write above lands. OnChildTerminal fires in that window,
	// finds a parent that is not waiting, and correctly does nothing. The wake is
	// gone. No error, no log, and the parent hangs until the process restarts.
	// This is not a crash-only or write-failure case: it is the ordinary
	// happy path at the shipped default cap.
	//
	// The repair is to close the window from the other side. The `waiting` write
	// is now durable, so this read is ordered AFTER it: any wake lost to the race
	// necessarily left a terminal link visible right here. If it did, this
	// goroutine takes the parent back and routes it in-process; the wait never
	// happened as far as the graph is concerned.
	if res, claimed := e.recoverLostWake(ctx, node, instanceID); claimed {
		return res, nil
	}

	e.queueMgr.NotifyCapacityFreed(ctx)

	return &NodeResult{IsWait: true, Output: output}, nil
}

// recoverLostWake re-reads the awaited child after the parent's `waiting`
// transition is durable and, if the child has ALREADY finished, claims the
// parent back to `running` and returns the routed result.
//
// The claim is a COMPARE-AND-SET (waiting -> running, at this node), not a plain
// write, because a waiting parent has two possible movers: this goroutine and
// the wake path's re-queue. Losing the claim is the normal, benign outcome — it
// means the wake did land, the parent is queued, and it will re-enter
// executeWait and short-circuit on the link at the top. Either way the parent
// resumes exactly once.
//
// EVERY failure degrades to "keep waiting" rather than to an error: the parent
// is legitimately in the waiting state, and SpawnWaitReconciler sweeps for
// exactly this condition on a ticker. A hung parent is the thing being fixed;
// a failed spawn node would be a worse outcome than a slightly later wake.
func (e *SpawnWorkflowExecutor) recoverLostWake(ctx context.Context, node workflow.Node, instanceID uuid.UUID) (*NodeResult, bool) {
	link, err := e.spawnWaits.FindChildBySpawnNode(ctx, instanceID, node.ID, instance.SpawnModeWaitForChild)
	if err != nil {
		slog.Error("spawn_workflow: re-reading awaited child after suspending failed; "+
			"relying on the spawn-wait reconciler",
			"nodeID", node.ID, "instanceID", instanceID, "error", err)
		return nil, false
	}
	if link == nil || !link.ChildState.IsTerminal() {
		return nil, false // the child is still running: a real wait, and the wake will arrive
	}

	// Build the routed result BEFORE claiming: it reads the child's final
	// variables, and a parent claimed back to running with no result to route
	// would hang. Unclaimed, it stays waiting for the wake or the reconciler.
	res, rerr := e.resumeFromLink(ctx, node, link)
	if rerr != nil {
		slog.Error("spawn_workflow: routing an already-terminal child failed; "+
			"relying on the spawn-wait reconciler",
			"nodeID", node.ID, "instanceID", instanceID, "error", rerr)
		return nil, false
	}

	claimed, err := e.spawnWaits.ClaimWaitingParent(ctx, instanceID, node.ID)
	if err != nil {
		slog.Error("spawn_workflow: claiming the parent back from waiting failed; "+
			"relying on the spawn-wait reconciler",
			"nodeID", node.ID, "instanceID", instanceID, "error", err)
		return nil, false
	}
	if !claimed {
		return nil, false // the wake won the race and re-queued the parent
	}
	return res, true
}

// resumeFromLink decides the parent's next move from an EXISTING link.
//
// Keying on the link rather than on node-execution status is deliberate: the
// interactive executor only needs to know THAT it finished, while the spawn
// node needs to know HOW the child ended in order to choose a port. The child's
// persisted state is the single authority for that; a second copy in the node
// record could disagree with it after a retry or a late cancellation.
//
// A terminal child's final variables are returned as output["childOutput"], on
// the failure port as well as success: what a failed child produced before it
// stopped is often exactly what the parent needs in order to recover.
func (e *SpawnWorkflowExecutor) resumeFromLink(ctx context.Context, node workflow.Node, link *instance.ChildLink) (*NodeResult, error) {
	output := spawnOutput(link.ChildID, node.Config.ChildWorkflow, node.Config.SpawnMode, nil)
	output["childState"] = string(link.ChildState)

	if !link.ChildState.IsTerminal() {
		// Still active: return a wait result WITHOUT re-spawning and without
		// re-transitioning state. This is the idempotent path that makes a
		// spurious re-entry — a duplicate wake, a node-level retry — harmless.
		return &NodeResult{IsWait: true, Output: output}, nil
	}

	// A child that cannot be read must not route as though it returned nothing.
	child, err := e.instances.Get(ctx, link.ChildID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load finished child %s: %w", node.ID, link.ChildID, err)
	}
	output["childOutput"] = childOutput(child.Variables)

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
// owner's identity, load the child's declared inputs through it, classify them
// against the node's explicit inputs map, resolve the mapped values, ask the
// preparer only for unmapped required inputs without a default, and create the
// linked child. It returns the origin of each spawned value (names only).
func (e *SpawnWorkflowExecutor) prepareAndSpawn(ctx context.Context, node workflow.Node, cfg workflow.SpawnConfig, vars *variable.VariableStore, workflowName string, instanceID uuid.UUID, mode instance.SpawnMode) (uuid.UUID, map[string]string, error) {
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}

	// Reconstruct the PARENT OWNER's identity. A resolution failure is NOT
	// downgraded to a system identity: falling back would silently change which
	// workflows are visible to the spawn and could widen reach beyond the owner.
	ownerIdentity, err := e.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: resolve owner identity: %w", node.ID, err)
	}
	childCtx := identity.WithIdentity(ctx, ownerIdentity)

	// If the parent's owner cannot see the child workflow this returns not-found
	// and the node fails — fail closed, with no existence leak.
	fields, err := e.spawner.ChildInputs(childCtx, cfg.ChildWorkflow)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: load child workflow %q: %w",
			node.ID, cfg.ChildWorkflow, err)
	}

	plan := workflow.PlanChildInputs(fields, cfg.Inputs)
	if len(plan.Unknown) > 0 {
		declared := make([]string, 0, len(fields))
		for _, f := range fields {
			declared = append(declared, f.Name)
		}
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: %w: child workflow %q has no input(s) %s; declared inputs: %s",
			node.ID, ErrSpawnUnknownChildInput, cfg.ChildWorkflow,
			strings.Join(plan.Unknown, ", "), strings.Join(declared, ", "))
	}

	// Mapped values are resolved from the parent store and never pass through the
	// preparer, so the preparer's parent-context truncation cannot touch them. A
	// missing reference fails the node; the LLM is never a fallback.
	resolved, err := variable.ResolveTypedMap(cfg.Inputs, vars)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: resolve inputs: %w", node.ID, err)
	}
	for _, f := range fields {
		if v, mapped := resolved[f.Name]; mapped && v == nil && f.Required {
			return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: %w: mapped input %q of child %q resolved to null",
				node.ID, ErrChildContractUnsatisfied, f.Name, cfg.ChildWorkflow)
		}
	}

	var prepared map[string]any
	if len(plan.Prepare) > 0 {
		guidance := ""
		if node.Config.TemplateFile != "" {
			text, rerr := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
			if rerr != nil {
				return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: read guidance %q: %w",
					node.ID, node.Config.TemplateFile, rerr)
			}
			guidance = variable.Resolve(text, vars)
		}

		workDir, _ := WorkDirFromVars(vars)
		values, perr := e.preparer.Prepare(childCtx, ChildInputRequest{
			NodeID:        node.ID,
			ChildWorkflow: cfg.ChildWorkflow,
			Inputs:        plan.Prepare,
			ParentContext: vars.Snapshot(),
			Guidance:      guidance,
			ProfileAlias:  node.Config.ModelProfile,
			WorkDir:       workDir,
		})
		if perr != nil {
			return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", node.ID, perr)
		}

		// Keep only what was asked for: a model must not be able to override a
		// mapped value or inject an undeclared one.
		requested := make(map[string]struct{}, len(plan.Prepare))
		for _, f := range plan.Prepare {
			requested[f.Name] = struct{}{}
		}
		prepared = make(map[string]any, len(plan.Prepare))
		var dropped []string
		for k, v := range values {
			if _, ok := requested[k]; ok {
				prepared[k] = v
			} else {
				dropped = append(dropped, k)
			}
		}
		if len(dropped) > 0 {
			sort.Strings(dropped)
			slog.Debug("spawn_workflow: dropped preparer keys that were not requested",
				"nodeID", node.ID, "keys", dropped)
		}
		if verr := validateChildInputs(cfg.ChildWorkflow, plan.Prepare, prepared); verr != nil {
			return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, verr)
		}
	}

	// Precedence mapped > default > prepared. The sets are disjoint by
	// construction; applying them in this order is defence in depth.
	values := make(map[string]any, len(prepared)+len(plan.Defaults)+len(resolved))
	sources := make(map[string]string, len(prepared)+len(plan.Defaults)+len(resolved))
	for k, v := range prepared {
		values[k] = v
		sources[k] = inputSourcePrepared
	}
	for k, v := range plan.Defaults {
		values[k] = v
		sources[k] = inputSourceDefault
	}
	for k, v := range resolved {
		values[k] = v
		sources[k] = inputSourceMapped
	}

	// The assembled values are what the child will receive, whichever way each
	// one arrived (mapped, default or prepared). Hold them to the same declared-
	// type contract a parallel_group workflow task is held to before enqueue, so
	// a whole-reference mapping that resolves to the wrong type (an array into a
	// string input) fails here instead of reaching a child that misreads it.
	if cerr := checkChildContract(cfg.ChildWorkflow, fields, values); cerr != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, cerr)
	}

	childID, err := e.spawner.Spawn(childCtx, SpawnRequest{
		ChildWorkflow: cfg.ChildWorkflow,
		Variables:     values,
		Priority:      string(parent.Priority),
		Link: instance.SpawnLink{
			ParentInstanceID: instanceID,
			ParentNodeID:     node.ID,
			Mode:             mode,
		},
	})
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, err)
	}
	return childID, sources, nil
}

// spawnOutput is the ONE shape of a spawn node's output. Recording the child id
// makes the child addressable from the parent's variable store, so downstream
// nodes and the trace can reference the spawned run.
//
// inputSources is nil on the resume path, which rebuilds its output from the
// link and so omits the key.
func spawnOutput(childID uuid.UUID, childWorkflow string, mode string, inputSources map[string]string) map[string]any {
	out := map[string]any{
		"childInstanceId": childID.String(),
		"childWorkflow":   childWorkflow,
		"spawnMode":       mode,
	}
	if inputSources != nil {
		// map[string]any, not map[string]string: variable.walk only traverses
		// map[string]any, so a typed map would be unreadable downstream.
		src := make(map[string]any, len(inputSources))
		for k, v := range inputSources {
			src[k] = v
		}
		out["inputSources"] = src
	}
	return out
}

// childOutput copies a finished child's final variables for the parent, minus
// the reserved keys. The child's {{workspace}} is its own sandbox path; handing
// it back invites a parent template to mistake it for the parent's workspace.
// Always a map, so parent paths into it resolve as missing rather than nil.
func childOutput(vars map[string]any) map[string]any {
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		if variable.ReservedInputNames[k] {
			continue
		}
		out[k] = v
	}
	return out
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
