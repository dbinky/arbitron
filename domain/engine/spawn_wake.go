package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// OnChildTerminal reacts to childID reaching a terminal state.
//
// A parallel-group task is handed only to the group terminal handler: its
// coordinator durably decides whether the group's completion rule is satisfied
// and owns the node-addressed wake. A legacy spawn child re-queues the parent
// waiting on its spawn node, as follows.
//
// THE MATCH CONDITION IS THE CORRECTNESS CORE OF WAIT-FOR-CHILD. A terminal
// child wakes its parent ONLY IF all three hold:
//
//	the parent is in the WAITING state
//	∧ the parent's CURRENT node id == the child's recorded parent node id
//	∧ the link's mode is wait_for_child
//
// Every conjunct is required:
//
//   - Matching only on "this child's parent is waiting" is wrong: a parent can
//     be waiting at spawn node B while a fire-and-forget child launched earlier
//     from node A reaches a terminal state. That child would re-queue the
//     parent, which would resume at node B, short-circuit against B's
//     still-running child, and — worse, if B's child had by then finished —
//     route node B by an event that had nothing to do with it.
//   - The mode conjunct alone is not sufficient either, because a parent may
//     carry two awaited spawn nodes in sequence, each with its own child.
//
// It is deliberately VOID and best-effort. A missed wake is recovered by
// SpawnWaitReconciler at the next startup; a wake that fails must never fail the
// terminal transition that triggered it.
func (e *Engine) OnChildTerminal(ctx context.Context, childID uuid.UUID) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e.pinRelease != nil {
		e.pinRelease(childID)
	}
	child, err := e.instances.Get(ctx, childID)
	if err != nil {
		return // an unreadable child cannot identify a parent; the reconciler covers it
	}
	if child.IsRoot() {
		return // a root run has nobody to wake
	}
	if child.SpawnMode == instance.SpawnModeGroupTask {
		// A group task must never reach the legacy wake, even if its parent
		// is waiting at the exact node the task is linked to.
		e.dispatchGroupTerminal(ctx, child)
		return
	}
	if child.SpawnMode != instance.SpawnModeWaitForChild {
		return // fire-and-forget child outcomes never affect the parent
	}
	if !child.State.IsTerminal() {
		return // called too early; the real terminal write will call again
	}

	parent, err := e.instances.Get(ctx, *child.ParentInstanceID)
	if err != nil {
		return
	}
	// A parent that is no longer waiting is a NO-OP. This mirrors OnTimerFired's
	// guard and is what makes cancel-then-cascade safe: a parent cancelled first
	// cannot be resurrected through its own failure port by the very children
	// being torn down.
	if parent.State != instance.StateWaiting {
		return
	}
	if parent.CurrentNodeID == nil || *parent.CurrentNodeID != *child.ParentNodeID {
		return
	}

	// CONDITIONAL re-queue. The state read above is a snapshot; between it and
	// this write the spawn executor may have claimed the same parent back to
	// `running` to resume in-process (its lost-wake guard). An unconditional
	// SetQueued would then leave one run both queued and executing — two
	// goroutines walking the same graph. The `state = waiting` predicate makes
	// exactly one of the two movers win.
	requeued, err := e.queueRepo.SetQueuedIfWaiting(ctx, parent.ID, time.Now())
	if err != nil {
		// Surfaced but not fatal: the reconciler sweeps for this parent.
		slog.Error("spawn wake: failed to re-queue parent",
			"parentID", parent.ID, "childID", childID, "error", err)
		return
	}
	if !requeued {
		return // the executor claimed it first and is resuming it in-process
	}
	e.queueManager.NotifyCapacityFreed(ctx)
}
