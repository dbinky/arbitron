package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

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
	e.pauseIfParentPaused(context.WithoutCancel(ctx), req.Link.ParentInstanceID, childID)
	return childID, nil
}

// pauseIfParentPaused closes the window in which a parent is paused while its
// spawn node is mid-flight: the pause cascade has already walked the parent's
// children, so a child created now would run unsupervised. The child is
// cascade-paused on behalf of whichever pause holds the parent (the parent's
// own cascade marker if an ancestor paused it, else the parent). Best-effort:
// a failure is logged and never fails the spawn.
func (e *Engine) pauseIfParentPaused(ctx context.Context, parentID, childID uuid.UUID) {
	parent, err := e.instances.Get(ctx, parentID)
	if err != nil || parent.State != instance.StatePaused {
		return
	}
	by, err := e.spawnLinks.CascadePausedBy(ctx, parentID)
	if err != nil {
		slog.Warn("spawn: failed to read parent's cascade pause", "parentID", parentID, "error", err)
		return
	}
	pauser := parentID
	if by != nil {
		pauser = *by
	}
	if _, err := e.pauseDescendant(ctx, childID, pauser); err != nil {
		slog.Warn("spawn: failed to pause child of paused parent", "parentID", parentID, "childID", childID, "error", err)
	}
}

// preflightSpawnChildren refuses to start a workflow whose spawn_workflow nodes
// map inputs the PUBLISHED child no longer (or does not yet) declare.
//
// Why at trigger time: the sample seeder never overwrites a workflow already in
// its ledger, so an upgraded install can hold a new parent beside an old child.
// The spawn-time guard (ErrSpawnUnknownChildInput) still catches that, but only
// when the spawn node runs, which for a late-stage child is after hours of
// completed upstream work. This runs the same unknown-key check against the
// child contract as the RUN OWNER sees it (private scope first, then the
// shared/published one, through the same visibility filter the trigger and the
// spawn use) before anything is created or enqueued, so zero parent nodes
// execute.
//
// Only an unambiguous contract violation is refused: a child that cannot be
// resolved (not found, hidden, a lookup error) is left to the spawn-time guard,
// because children can change or appear later and a transient lookup failure
// must not block an otherwise valid run. Spawn nodes without a literal child
// name are skipped.
func (e *Engine) preflightSpawnChildren(ctx context.Context, owner uuid.UUID, wf *workflow.Workflow, filter workflow.SharedVisibilityFilter) error {
	type contract struct {
		fields   []workflow.InputField
		resolved bool
	}
	contracts := map[string]contract{}
	for _, n := range wf.Nodes {
		if n.Type != workflow.NodeTypeSpawnWorkflow || n.Config.ChildWorkflow == "" || len(n.Config.Inputs) == 0 {
			continue
		}
		child := n.Config.ChildWorkflow
		c, seen := contracts[child]
		if !seen {
			if cw, _, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, child, filter); err == nil {
				c = contract{fields: cw.Inputs, resolved: true}
			}
			contracts[child] = c
		}
		if !c.resolved {
			continue
		}
		fields := c.fields
		plan := workflow.PlanChildInputs(fields, n.Config.Inputs)
		if len(plan.Unknown) == 0 {
			continue
		}
		declared := make([]string, 0, len(fields))
		for _, f := range fields {
			declared = append(declared, f.Name)
		}
		return fmt.Errorf("workflow %q cannot start: %w: spawn_workflow node %q maps input(s) %s that the published child workflow %q does not declare (declared: %s); update and publish the installed %q workflow, then trigger again",
			wf.Name, ErrSpawnUnknownChildInput, n.ID, strings.Join(plan.Unknown, ", "), child, strings.Join(declared, ", "), child)
	}
	return nil
}
