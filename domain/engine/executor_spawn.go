package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SpawnWorkflowExecutor executes one spawn_workflow node: prepare the child's
// startup values, start the child, and tell the engine how to proceed.
//
// Every collaborator is a PORT, so the executor holds no LLM, no queue, and no
// engine. Phase 3 implements fire-and-forget only; wait-for-child fails loudly
// (see Execute).
type SpawnWorkflowExecutor struct {
	instances  instance.InstanceRepository
	preparer   ChildInputPreparer
	spawner    ChildSpawner
	identities IdentityResolver
	fileReader FileReader
}

// NewSpawnWorkflowExecutor constructs the executor.
func NewSpawnWorkflowExecutor(
	instances instance.InstanceRepository,
	preparer ChildInputPreparer,
	spawner ChildSpawner,
	identities IdentityResolver,
	fileReader FileReader,
) *SpawnWorkflowExecutor {
	return &SpawnWorkflowExecutor{
		instances:  instances,
		preparer:   preparer,
		spawner:    spawner,
		identities: identities,
		fileReader: fileReader,
	}
}

var _ NodeExecutor = (*SpawnWorkflowExecutor)(nil)

// Execute runs one spawn node.
//
// Every failure below returns a plain error; the engine then applies the node's
// resolved ErrorBehavior (retry / default output / error-node routing). The
// executor encodes no control-flow policy of its own.
func (e *SpawnWorkflowExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// 1. The run this node belongs to.
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: instance ID not found in context", node.ID)
	}

	// 2. Re-check the configured contract. Instances execute from a
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
		// FAIL LOUDLY. Silently treating this as fire-and-forget would leave the
		// parent's success/failure ports unreachable and the parent would proceed
		// as though a child had succeeded — a wrong answer rather than a visible
		// gap.
		return nil, fmt.Errorf("spawn_workflow %q: spawnMode %q is not yet supported",
			node.ID, workflow.SpawnModeWaitForChild)
	}

	// 3. Load the parent run for its owner and priority.
	parent, err := e.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load parent instance: %w", node.ID, err)
	}

	// 4. Reconstruct the PARENT OWNER's identity and derive the child-spawn
	// context. This is what makes the child inherit the parent's owner rather
	// than the ambient caller. A resolution failure is NOT downgraded to a system
	// identity: falling back would silently change which workflows are visible to
	// the spawn and could widen reach beyond the owner.
	ownerIdentity, err := e.identities.IdentityForUser(ctx, parent.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: resolve owner identity: %w", node.ID, err)
	}
	childCtx := identity.WithIdentity(ctx, ownerIdentity)

	// 5. Load the child's declared inputs THROUGH that identity. If the parent's
	// owner cannot see the child workflow, this returns not-found and the node
	// fails — fail closed, with no existence leak.
	inputs, err := e.spawner.ChildInputs(childCtx, node.Config.ChildWorkflow)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: load child workflow %q: %w",
			node.ID, node.Config.ChildWorkflow, err)
	}

	// 6. Optional guidance prose. An UNCONFIGURED file is not an error; a
	// configured file that cannot be read is a misconfiguration.
	guidance := ""
	if node.Config.TemplateFile != "" {
		b, rerr := e.fileReader.ReadFile(ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
		if rerr != nil {
			return nil, fmt.Errorf("spawn_workflow %q: read guidance %q: %w",
				node.ID, node.Config.TemplateFile, rerr)
		}
		guidance = variable.Resolve(string(b), vars)
	}

	// 7. Prepare the startup values. A contract failure here is the spec's
	// "required startup values cannot be produced" condition and fails the node
	// BEFORE any child exists.
	values, err := e.preparer.Prepare(childCtx, ChildInputRequest{
		NodeID:        node.ID,
		ChildWorkflow: node.Config.ChildWorkflow,
		Inputs:        inputs,
		ParentContext: vars.Snapshot(),
		Guidance:      guidance,
		ProfileAlias:  node.Config.ModelProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", node.ID, err)
	}

	// 8. Create and enqueue the child under the parent's owner, with the link set.
	//
	// If creation partially succeeded (the child exists queued with its link but
	// the enqueue errored), a retry of this node would create a SECOND child.
	// That is accepted for fire-and-forget, where child outcomes do not affect
	// the parent — recorded here so the behaviour is a decision, not a surprise.
	childID, err := e.spawner.Spawn(childCtx, SpawnRequest{
		ChildWorkflow: node.Config.ChildWorkflow,
		Variables:     values,
		Priority:      string(parent.Priority),
		Link: instance.SpawnLink{
			ParentInstanceID: instanceID,
			ParentNodeID:     node.ID,
			Mode:             instance.SpawnMode(mode),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: %w", node.ID, err)
	}

	// 9. A normal (non-wait) result. Recording the child id in the node output
	// makes the child addressable from the parent's variable store, so downstream
	// nodes and the trace can reference the spawned run.
	return &NodeResult{Output: map[string]any{
		"childInstanceId": childID.String(),
		"childWorkflow":   node.Config.ChildWorkflow,
		"spawnMode":       mode,
	}}, nil
}
