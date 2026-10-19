package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time assertion: the engine satisfies the port the spawn executor
// depends on.
var _ ChildSpawner = (*Engine)(nil)

// ChildInputs implements ChildSpawner.
//
// It resolves the child workflow through the IDENTICAL owner-scoped,
// visibility-filtered load the trigger path performs, so a spawn node can never
// reach a workflow the same caller could not trigger directly. If the parent's
// owner cannot see the child, this returns not-found — the visibility filter's
// contract is to never reveal that a department-scoped workflow exists to an
// outsider.
func (e *Engine) ChildInputs(ctx context.Context, workflowName string) ([]workflow.InputField, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner, ok := triggerOwner(ctx, e.users)
	if !ok {
		return nil, fmt.Errorf("child inputs %q: failed to resolve owner", workflowName)
	}
	wf, _, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, e.sharedVisibilityFilter(ctx))
	if err != nil {
		return nil, fmt.Errorf("child inputs %q: %w", workflowName, err)
	}
	return wf.Inputs, nil
}

// Spawn implements ChildSpawner.
//
// It is deliberately THIN: it derives a context carrying the link and delegates
// to the existing trigger path, which resolves the owner from the context
// identity, gates the shared read by that identity's departments, refuses an
// archived workflow, validates the child's declared inputs, stamps the link
// onto the Instance literal, creates the row, and enqueues it. Every one of
// those behaviours is inherited rather than reimplemented — that is why owner
// inheritance and correct authorization need no owner-override refactor of the
// trigger path.
//
// The CALLER is responsible for injecting the parent owner's reconstructed
// identity into ctx before calling; Spawn does not resolve identities.
func (e *Engine) Spawn(ctx context.Context, req SpawnRequest) (uuid.UUID, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Link.Mode == instance.SpawnModeGroupTask {
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: group_task mode requires StartGroupTask", req.ChildWorkflow)
	}
	if !req.Link.Valid() {
		// Reject BEFORE creating anything: a child whose link cannot be matched
		// by the wake would strand its parent forever, which is worse than a
		// failed spawn.
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: incomplete spawn link %+v", req.ChildWorkflow, req.Link)
	}

	// DERIVE the child's depth here, from the PARENT'S PERSISTED value, and
	// refuse to exceed the ceiling.
	//
	// This is the single choke point through which every child is created, which
	// is why the bound lives here rather than in the executor: no future caller
	// can forget it, and no caller can supply a depth of its own — whatever
	// req.Link.SpawnDepth held is overwritten below.
	//
	// The check must be RUN-TIME. The spawn graph is a graph of workflow NAMES
	// resolved at run time, so a cycle can be indirect (A spawns B spawns A) and
	// invisible in any single definition; the author-time self-reference check
	// in workflow validation closes only the direct case. Unbounded, each
	// generation costs a row, a workspace, and a billed provider call — and a
	// shared or published workflow bills the TRIGGERING tenant, not the author.
	parent, perr := e.instances.Get(ctx, req.Link.ParentInstanceID)
	if perr != nil {
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: load parent instance: %w", req.ChildWorkflow, perr)
	}
	depth := parent.SpawnDepth + 1
	if depth > instance.MaxSpawnDepth {
		return uuid.Nil, fmt.Errorf(
			"spawn child workflow %q: %w (depth %d, limit %d); the run tree is %d generations deep, "+
				"which usually means a spawn cycle",
			req.ChildWorkflow, instance.ErrSpawnDepthExceeded, depth, instance.MaxSpawnDepth, parent.SpawnDepth)
	}

	link := req.Link
	link.SpawnDepth = depth
	ctx = instance.WithSpawnLink(ctx, link)
	childID, _, err := e.TriggerWithDepartment(ctx, req.ChildWorkflow, req.Variables, req.Priority, "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("spawn child workflow %q: %w", req.ChildWorkflow, err)
	}
	return childID, nil
}
