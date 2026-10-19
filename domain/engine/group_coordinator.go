package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// GroupTaskCanceller cancels one unfinished group task and its descendants;
// a task already terminal is left, with its own descendants, untouched
// (ErrCannotCancel). *Engine.CancelGroupTask satisfies it. The coordinator
// never takes the operator's subtree Cancel: on a task that just completed it
// would stamp subtree intent and stop that task's unrelated independent work.
type GroupTaskCanceller interface {
	CancelGroupTask(ctx context.Context, taskID uuid.UUID) error
}

// GroupCoordinator owns every parallel-group decision.
//
// THE PERSISTED STATE IS THE AUTHORITY. Evaluate reads the run, the parent and
// every task child from storage on every pass and moves the run forward with
// compare-and-set writes, re-reading after each one. In-process terminal
// callbacks (OnTaskTerminal) only make the common case immediate; Reconcile
// over ListUnsettled is what guarantees that a crash between any two steps —
// decision, cancellation, finalization, wake — is finished later. Losing a CAS
// is never permission to skip the work that follows it.
type GroupCoordinator struct {
	groups     instance.GroupRunRepository
	instances  instance.InstanceRepository
	waker      instance.NodeAddressedWaker
	canceller  GroupTaskCanceller
	identities IdentityResolver
	bus        EventBus
	queueMgr   QueueManager
	now        func() time.Time

	mu    sync.Mutex
	gates map[uuid.UUID]*evalGate
}

// evalGate coalesces in-process re-entry for one run: a call that arrives
// while a pass for the same run is running marks it dirty, and the running
// call makes one more pass instead of recursing.
type evalGate struct {
	dirty bool
}

// NewGroupCoordinator constructs the coordinator. Every collaborator is
// required; a nil one panics, because each silently disables a guarantee
// (persistence, child state, wakes, cancellation, authorization, visibility,
// or queue capacity).
func NewGroupCoordinator(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	waker instance.NodeAddressedWaker,
	canceller GroupTaskCanceller,
	identities IdentityResolver,
	bus EventBus,
	queueMgr QueueManager,
) *GroupCoordinator {
	switch {
	case groups == nil:
		panic("engine: NewGroupCoordinator requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupCoordinator requires a non-nil InstanceRepository")
	case waker == nil:
		panic("engine: NewGroupCoordinator requires a non-nil NodeAddressedWaker")
	case canceller == nil:
		panic("engine: NewGroupCoordinator requires a non-nil GroupTaskCanceller")
	case identities == nil:
		panic("engine: NewGroupCoordinator requires a non-nil IdentityResolver")
	case bus == nil:
		panic("engine: NewGroupCoordinator requires a non-nil EventBus")
	case queueMgr == nil:
		panic("engine: NewGroupCoordinator requires a non-nil QueueManager")
	}
	return &GroupCoordinator{
		groups:     groups,
		instances:  instances,
		waker:      waker,
		canceller:  canceller,
		identities: identities,
		bus:        bus,
		queueMgr:   queueMgr,
		now:        time.Now,
		gates:      map[uuid.UUID]*evalGate{},
	}
}

// OnTaskTerminal is the in-process intake of one task child's terminal
// transition. It is VOID and best-effort, like Engine.OnChildTerminal: a lost
// or failed call is recovered by Reconcile.
func (c *GroupCoordinator) OnTaskTerminal(ctx context.Context, childID uuid.UUID) {
	if ctx == nil {
		ctx = context.Background()
	}
	run, err := c.groups.GetByChild(ctx, childID)
	if err != nil {
		slog.Error("group coordinator: resolving a task child's group run failed; the group sweep retries",
			"childID", childID, "error", err)
		return
	}
	if run == nil {
		return
	}
	if err := c.Evaluate(ctx, run.ID); err != nil {
		slog.Warn("group coordinator: evaluation incomplete; the group sweep retries",
			"groupRunID", run.ID, "childID", childID, "error", err)
	}
}

// Reconcile evaluates every unsettled run. The listing error is returned; a
// per-run error is logged and the sweep continues.
func (c *GroupCoordinator) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ids, err := c.groups.ListUnsettled(ctx)
	if err != nil {
		return fmt.Errorf("group reconciler: listing unsettled group runs: %w", err)
	}
	for _, id := range ids {
		if err := c.Evaluate(ctx, id); err != nil {
			slog.Warn("group reconciler: group run not settled this pass", "groupRunID", id, "error", err)
		}
	}
	return nil
}

// Evaluate moves one run as far forward as its persisted state allows:
// intake terminals -> decide (CAS) -> apply unfinished actions -> finalize the
// results-mode output -> wake the parent. See the task contract for each step.
func (c *GroupCoordinator) Evaluate(ctx context.Context, runID uuid.UUID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.enter(runID) {
		return nil
	}
	for {
		err := c.evaluateOnce(ctx, runID)
		if !c.again(runID, err) {
			return err
		}
	}
}

func (c *GroupCoordinator) enter(runID uuid.UUID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, busy := c.gates[runID]; busy {
		g.dirty = true
		return false
	}
	c.gates[runID] = &evalGate{}
	return true
}

func (c *GroupCoordinator) again(runID uuid.UUID, err error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.gates[runID]
	if err == nil && g != nil && g.dirty {
		g.dirty = false
		return true
	}
	delete(c.gates, runID)
	return false
}

func (c *GroupCoordinator) evaluateOnce(ctx context.Context, runID uuid.UUID) error {
	run, err := c.reload(ctx, runID)
	if err != nil {
		return err
	}
	parent, err := c.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group coordinator: run %s: load parent %s: %w", runID, run.ParentInstanceID, err)
	}

	if err := c.intake(ctx, run); err != nil {
		return err
	}
	if run, err = c.reload(ctx, runID); err != nil {
		return err
	}

	if parent.State == instance.StateFailed || parent.State == instance.StateCancelled {
		return c.settleUnderDeadParent(ctx, run, parent)
	}

	if run.State == instance.GroupRunLaunching {
		advanced, err := c.completeLaunch(ctx, run, parent)
		if err != nil {
			return err
		}
		if !advanced {
			return nil
		}
		if run, err = c.reload(ctx, runID); err != nil {
			return err
		}
	}

	if run.Decision == nil && run.State == instance.GroupRunRunning {
		if d, ok := DecideGroup(run, c.now()); ok {
			won, err := c.groups.Decide(ctx, run.ID, *d)
			if err != nil {
				return fmt.Errorf("group coordinator: record the decision of run %s: %w", run.ID, err)
			}
			if won {
				publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
			}
			if run, err = c.reload(ctx, runID); err != nil {
				return err
			}
		}
	}
	if run.Decision == nil {
		return nil
	}

	if !run.ActionsApplied {
		if err := c.applyActions(ctx, run, parent); err != nil {
			return err
		}
	}
	if run.State == instance.GroupRunDecided && run.GatherMode != workflow.GroupGatherResultsAndFiles {
		if err := c.finalize(ctx, run); err != nil {
			return err
		}
	}
	if run, err = c.reload(ctx, runID); err != nil {
		return err
	}

	if run.State == instance.GroupRunDone && !run.ContinuationDelivered && parentMovedOn(parent, run) {
		if err := c.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
			return fmt.Errorf("group coordinator: settle the delivered continuation of run %s: %w", run.ID, err)
		}
		return c.refresh(ctx, run)
	}
	c.WakeParent(ctx, run)
	return nil
}

func (c *GroupCoordinator) reload(ctx context.Context, runID uuid.UUID) (*instance.GroupRun, error) {
	run, err := c.groups.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("group coordinator: load run %s: %w", runID, err)
	}
	return run, nil
}

// refresh replaces the caller's snapshot after a write, including a lost CAS.
func (c *GroupCoordinator) refresh(ctx context.Context, run *instance.GroupRun) error {
	fresh, err := c.reload(ctx, run.ID)
	if err != nil {
		return err
	}
	*run = *fresh
	return nil
}

// intake refreshes nonterminal live state. Production terminal receipts are
// already in the roster, atomically committed by the terminal writer (P2-T16).
func (c *GroupCoordinator) intake(ctx context.Context, run *instance.GroupRun) error {
	for _, task := range run.Tasks {
		if task.LaunchState != instance.GroupTaskLaunched || task.TerminalSeq != nil {
			continue
		}
		child, err := c.instances.Get(ctx, task.ChildInstanceID)
		if err != nil {
			return fmt.Errorf("group intake child %s: %w", task.ChildInstanceID, err)
		}
		if child.State.IsTerminal() {
			// The child may have committed after our first roster read.
			fresh, err := c.groups.Get(ctx, run.ID)
			if err != nil {
				return err
			}
			receipt, ok := fresh.TaskByKey(task.Key)
			if !ok || receipt.TerminalSeq == nil {
				return fmt.Errorf("terminal group task %q has no atomic receipt; verify terminal persistence wiring", task.Key)
			}
			continue
		}
		status := instance.GroupTaskStatusFromState(child.State)
		if status != task.LiveStatus {
			if err := c.groups.UpdateTaskLiveStatus(ctx, run.ID, task.Key, status); err != nil {
				return err
			}
			publishGroupEvent(c.bus, run, EventTypeGroupTaskUpdated, map[string]any{"taskKey": task.Key, "status": string(status)}, c.now())
			if err := c.refresh(ctx, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// settleUnderDeadParent stops all of a failed or cancelled parent's group work
// and settles the run without any continuation.
func (c *GroupCoordinator) settleUnderDeadParent(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	if run.Decision == nil && (run.State == instance.GroupRunLaunching || run.State == instance.GroupRunRunning) {
		d := DecideLaunchFailure(run, fmt.Sprintf("parent run %s before the group decided", parent.State), c.now())
		won, err := c.groups.Decide(ctx, run.ID, *d)
		if err != nil {
			return fmt.Errorf("group coordinator: record the dead-parent decision of run %s: %w", run.ID, err)
		}
		if won {
			publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
		}
		if run, err = c.reload(ctx, run.ID); err != nil {
			return err
		}
	}
	if err := c.cancelOpenTasks(ctx, run, parent); err != nil {
		return err
	}
	if !run.ActionsApplied {
		if err := c.groups.MarkActionsApplied(ctx, run.ID); err != nil {
			return fmt.Errorf("group coordinator: mark actions applied on run %s: %w", run.ID, err)
		}
		if err := c.refresh(ctx, run); err != nil {
			return err
		}
	}
	if run.GatherMode == workflow.GroupGatherResultsAndFiles && run.State != instance.GroupRunDone {
		// The gather reconciler owns abort/rollback and files-mode output under
		// the gather gate. Finalizing here would freeze results-only output
		// (possibly success) before that recovery can run.
		return nil
	}
	if run.State == instance.GroupRunDecided && run.Decision != nil {
		if err := c.finalize(ctx, run); err != nil {
			return err
		}
	}
	if !run.ContinuationDelivered {
		if err := c.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
			return fmt.Errorf("group coordinator: settle run %s under a %s parent: %w", run.ID, parent.State, err)
		}
		return c.refresh(ctx, run)
	}
	return nil
}

// completeLaunch handles a run still in launching. It reports whether the run
// advanced (barrier opened, or an interrupted launch was decided).
func (c *GroupCoordinator) completeLaunch(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (bool, error) {
	allLaunched := true
	for _, t := range run.Tasks {
		if t.LaunchState != instance.GroupTaskLaunched {
			allLaunched = false
			break
		}
	}
	if allLaunched {
		if _, err := c.groups.TransitionState(ctx, run.ID, instance.GroupRunLaunching, instance.GroupRunRunning); err != nil {
			return false, fmt.Errorf("group coordinator: open the launch barrier of run %s: %w", run.ID, err)
		}
		return true, nil
	}
	atNode := parent.CurrentNodeID != nil && *parent.CurrentNodeID == run.NodeID
	if atNode && (parent.State == instance.StateRunning || parent.State == instance.StatePaused) {
		return false, nil // the launcher is still starting tasks in that goroutine
	}
	d := DecideLaunchFailure(run, "launch failed: the launch was interrupted before every task started", c.now())
	won, err := c.groups.Decide(ctx, run.ID, *d)
	if err != nil {
		return false, fmt.Errorf("group coordinator: record the interrupted-launch decision of run %s: %w", run.ID, err)
	}
	if won {
		publishGroupEvent(c.bus, run, EventTypeGroupDecided, decisionEventData(d), c.now())
	}
	return true, nil
}

// applyActions performs the decision's unfinished-task actions, then records
// that they are complete. Any error is returned so the sweep finishes them.
func (c *GroupCoordinator) applyActions(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	// The frozen roster counts too: tasks recorded launched after an
	// interrupted-launch decision must not be let off as independent work.
	launchFailure := decidedOnPartialRoster(run.Decision)
	for _, t := range run.Tasks {
		if t.LaunchState != instance.GroupTaskLaunched {
			launchFailure = true
			break
		}
	}
	switch {
	case launchFailure || run.Decision.Behavior == workflow.GroupUnfinishedCancel:
		if err := c.cancelOpenTasks(ctx, run, parent); err != nil {
			return err
		}
	case run.Decision.Behavior == workflow.GroupUnfinishedFinishIndependently:
		for _, t := range run.Tasks {
			if isTerminalGroupStatus(t.LiveStatus) || t.LaunchState != instance.GroupTaskLaunched || t.ContinuingIndependently {
				continue
			}
			if err := c.groups.MarkTaskContinuingIndependently(ctx, run.ID, t.Key); err != nil {
				return fmt.Errorf("group coordinator: run %s task %q: mark continuing independently: %w", run.ID, t.Key, err)
			}
			if err := c.refresh(ctx, run); err != nil {
				return err
			}
		}
	}
	if err := c.groups.MarkActionsApplied(ctx, run.ID); err != nil {
		return fmt.Errorf("group coordinator: mark actions applied on run %s: %w", run.ID, err)
	}
	return c.refresh(ctx, run)
}

// cancelOpenTasks requests and performs cancellation of every non-terminal
// task under the parent OWNER's reconstructed identity. A never-launched task's
// reserved id is cancelled too: a crash between StartGroupTask and
// MarkTaskLaunched can leave a real child behind it. Already-terminal and
// absent children are the normal benign races.
func (c *GroupCoordinator) cancelOpenTasks(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) error {
	var open []instance.GroupTask
	for _, t := range run.Tasks {
		if !isTerminalGroupStatus(t.LiveStatus) {
			open = append(open, t)
		}
	}
	if len(open) == 0 {
		return nil
	}
	ownerCtx, err := c.ownerContext(ctx, parent.OwnerID)
	if err != nil {
		return err
	}
	for _, t := range open {
		if t.LaunchState == instance.GroupTaskLaunched && !t.CancelRequested {
			if err := c.groups.MarkTaskCancelRequested(ctx, run.ID, t.Key); err != nil {
				return fmt.Errorf("group coordinator: run %s task %q: record the cancellation request: %w", run.ID, t.Key, err)
			}
			if err := c.refresh(ctx, run); err != nil {
				return err
			}
		}
		if err := c.canceller.CancelGroupTask(ownerCtx, t.ChildInstanceID); err != nil &&
			!errors.Is(err, instance.ErrCannotCancel) && !errors.Is(err, instance.ErrNotFound) {
			return fmt.Errorf("group coordinator: run %s task %q: cancel %s: %w", run.ID, t.Key, t.ChildInstanceID, err)
		}
		if err := c.refresh(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func (c *GroupCoordinator) ownerContext(ctx context.Context, owner uuid.UUID) (context.Context, error) {
	id, err := c.identities.IdentityForUser(ctx, owner)
	if err != nil {
		// Never downgraded to a system identity: that would widen what the
		// cancellation may reach.
		return nil, fmt.Errorf("group coordinator: resolve owner identity %s: %w", owner, err)
	}
	return identity.WithIdentity(ctx, id), nil
}

// finalize freezes the results-mode output and makes the continuation ready.
func (c *GroupCoordinator) finalize(ctx context.Context, run *instance.GroupRun) error {
	port := workflow.GroupPortFailure
	if run.Decision.Outcome == groupOutcomeSuccess {
		port = workflow.GroupPortSuccess
	}
	won, err := c.groups.SetFinalOutput(ctx, run.ID, port, BuildGroupOutput(run), instance.GroupRunDecided)
	if err != nil {
		return fmt.Errorf("group coordinator: finalize run %s: %w", run.ID, err)
	}
	if won {
		publishGroupEvent(c.bus, run, EventTypeGroupCompleted, map[string]any{"outcome": run.Decision.Outcome, "port": port}, c.now())
	}
	return c.refresh(ctx, run)
}

// parentMovedOn reports whether the parent can no longer be continued by this
// run: it is terminal, or its cursor has left the group node.
func parentMovedOn(parent *instance.Instance, run *instance.GroupRun) bool {
	if parent.State.IsTerminal() {
		return true
	}
	return parent.CurrentNodeID == nil || *parent.CurrentNodeID != run.NodeID
}

// WakeParent re-queues the parent iff it is waiting at THIS group node and the
// run is ready to continue it (done with a ready, undelivered continuation; or,
// for file gathering, decided with its actions applied). It never wakes a
// cancelled, failed, paused or elsewhere-waiting parent: the node-addressed
// conditional update matches only state = waiting at run.NodeID, which is also
// what makes it lose benignly to the executor's in-process claim.
func (c *GroupCoordinator) WakeParent(ctx context.Context, run *instance.GroupRun) bool {
	if run == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ready := run.State == instance.GroupRunDone && run.ContinuationReady && !run.ContinuationDelivered
	gatherReady := run.GatherMode == workflow.GroupGatherResultsAndFiles && run.State == instance.GroupRunDecided && run.ActionsApplied
	if !ready && !gatherReady {
		return false
	}
	parent, err := c.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		slog.Warn("group coordinator: reading the parent to wake failed; the group sweep retries",
			"groupRunID", run.ID, "parentID", run.ParentInstanceID, "error", err)
		return false
	}
	if parent.State != instance.StateWaiting || parent.CurrentNodeID == nil || *parent.CurrentNodeID != run.NodeID {
		return false
	}
	woke, err := c.waker.SetQueuedIfWaitingAtNode(ctx, parent.ID, run.NodeID, c.now())
	if err != nil {
		slog.Error("group coordinator: re-queueing the waiting parent failed; the group sweep retries",
			"groupRunID", run.ID, "parentID", parent.ID, "error", err)
		return false
	}
	if !woke {
		return false // the executor claimed it back in-process, or it moved
	}
	c.queueMgr.NotifyCapacityFreed(ctx)
	return true
}
