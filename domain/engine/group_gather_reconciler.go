package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Abort reasons recorded in GatherReport.Error and session end reasons.
const (
	gatherAbortParentCancelled = "parent instance was cancelled while the group was gathering files"
	gatherAbortParentFailed    = "parent instance failed while the group was gathering files"
	gatherAbortParentCompleted = "parent instance finished without delivering the group's file gather; its working area is gone, so nothing was restored"

	resolutionCloseCancelled = "cancelled"
	resolutionCloseError     = "error"
	resolutionCloseEnded     = "ended"
	resolutionEndAbandoned   = "abandoned"
)

// GroupGatherReconciler recovers results_and_files gathers that no parent
// executor is driving: at startup (after RecoverCrashedInstances and the group
// coordinator's pass), on the group sweep tick, and — through Watch — as soon
// as a parent is cancelled, fails or finishes. It never writes a parent's
// working area while that parent's executor holds the run's gather gate.
type GroupGatherReconciler struct {
	groups    instance.GroupRunRepository
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	aborter   GatherAborter
	closer    GroupSessionCloser
	completer GroupResolutionCompleter
	waker     instance.NodeAddressedWaker
	queueMgr  QueueManager
	locker    GatherLocker
	bus       EventBus
	pruner    *GroupResourcePruner
	now       func() time.Time
}

// NewGroupGatherReconciler constructs the reconciler. Every collaborator is
// required: a missing one is a composition-root bug that would leave
// cancelled parents' gathers unsettled or resources unreleased.
func NewGroupGatherReconciler(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	aborter GatherAborter,
	closer GroupSessionCloser,
	completer GroupResolutionCompleter,
	waker instance.NodeAddressedWaker,
	queueMgr QueueManager,
	locker GatherLocker,
	bus EventBus,
	pruner *GroupResourcePruner,
) *GroupGatherReconciler {
	switch {
	case groups == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupRunRepository")
	case instances == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil InstanceRepository")
	case nodeExecs == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil NodeExecutionRepository")
	case aborter == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GatherAborter")
	case closer == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupSessionCloser")
	case completer == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupResolutionCompleter")
	case waker == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil NodeAddressedWaker")
	case queueMgr == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil QueueManager")
	case locker == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GatherLocker")
	case bus == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil EventBus")
	case pruner == nil:
		panic("engine: NewGroupGatherReconciler requires a non-nil GroupResourcePruner")
	}
	return &GroupGatherReconciler{
		groups: groups, instances: instances, nodeExecs: nodeExecs, aborter: aborter, closer: closer,
		completer: completer, waker: waker, queueMgr: queueMgr, locker: locker, bus: bus, pruner: pruner, now: time.Now,
	}
}

// Reconcile recovers every unsettled file gather, then sweeps working-area
// resources. Per-run problems are logged (the next pass retries); only the
// listing error is returned. Satisfies cmd/arbitron's groupReconciler.
func (r *GroupGatherReconciler) Reconcile(ctx context.Context) error {
	ids, err := r.groups.ListUnsettled(ctx)
	if err != nil {
		return fmt.Errorf("group gather reconciler: list unsettled group runs: %w", err)
	}
	for _, id := range ids {
		if err := r.RecoverRun(ctx, id); err != nil {
			slog.Warn("group gather reconciler: run not recovered this pass", "groupRunID", id, "error", err)
		}
	}
	if err := r.pruner.Sweep(ctx); err != nil {
		slog.Warn("group gather reconciler: resource sweep incomplete", "error", err)
	}
	return nil
}

// RecoverRun brings one results_and_files run that is decided, gathering or
// resolving conflicts in line with its parent's state (see the task's
// "Recovery by parent state" decision). Other runs are left alone.
func (r *GroupGatherReconciler) RecoverRun(ctx context.Context, runID uuid.UUID) error {
	unlock, ok := r.locker.TryLockGather(runID)
	if !ok {
		return nil // the parent's executor owns this gather; retry next pass
	}
	defer unlock()

	// Read both the run and parent under the same gate used by the executor.
	// Session restoration also writes attempts and may wake the parent.
	run, err := r.groups.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: load run %s: %w", runID, err)
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles || run.Decision == nil || !gatherableGroupState(run.State) {
		return nil
	}
	parent, err := r.instances.Get(ctx, run.ParentInstanceID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: load parent %s: %w", run.ID, run.ParentInstanceID, err)
	}
	switch parent.State {
	case instance.StateCancelled:
		return r.abort(ctx, run, ResolutionOutcomeCancelled, gatherAbortParentCancelled, resolutionCloseCancelled, false)
	case instance.StateFailed:
		return r.abort(ctx, run, ResolutionOutcomeError, gatherAbortParentFailed, resolutionCloseError, false)
	case instance.StateCompleted:
		return r.abort(ctx, run, ResolutionOutcomeError, gatherAbortParentCompleted, resolutionCloseError, true)
	case instance.StateWaiting:
		if parent.CurrentNodeID != nil && *parent.CurrentNodeID == run.NodeID {
			return r.restore(ctx, run)
		}
	}
	return nil
}

// abort settles a run whose parent will never continue it. areaGone drops the
// gather handle first, so nothing is restored into a removed working area.
func (r *GroupGatherReconciler) abort(ctx context.Context, run *instance.GroupRun, outcome, reason, sessionReason string, areaGone bool) error {
	if run.Gather != nil && run.Gather.ActiveSession != nil {
		if err := r.closer.CloseSession(ctx, *run.Gather.ActiveSession, sessionReason); err != nil {
			return fmt.Errorf("group gather reconciler: run %s: close the open resolution session: %w", run.ID, err)
		}
	}
	target := run
	if areaGone {
		// Drop the handle durably first: the gatherer's report writes are a
		// CAS over the report it was given, and a restart must not roll back
		// into a vanished area either.
		target = withoutGatherHandle(run)
		if run.Gather != nil {
			if err := r.groups.ReplaceGatherReport(ctx, run.ID, run.Gather, *target.Gather); err != nil {
				return fmt.Errorf("group gather reconciler: run %s: forget the vanished area's gather handle: %w", run.ID, err)
			}
		}
	}
	if _, err := r.aborter.AbortGather(ctx, target, outcome, reason); err != nil {
		return fmt.Errorf("group gather reconciler: run %s: abort the gather: %w", run.ID, err)
	}
	fresh, err := r.groups.Get(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("group gather reconciler: reload run %s: %w", run.ID, err)
	}
	slog.Info("group gather reconciler: aborted a file gather whose parent will not continue it",
		"groupRunID", run.ID, "parentID", run.ParentInstanceID, "reason", reason)
	return r.settle(ctx, fresh)
}

// settle records that a done run's continuation will never be consumed, so it
// leaves ListUnsettled.
func (r *GroupGatherReconciler) settle(ctx context.Context, run *instance.GroupRun) error {
	if run.ContinuationDelivered {
		return nil
	}
	if err := r.groups.MarkContinuationDelivered(ctx, run.ID); err != nil {
		return fmt.Errorf("group gather reconciler: settle run %s: %w", run.ID, err)
	}
	return nil
}

// restore handles a parent that is waiting at the group node.
func (r *GroupGatherReconciler) restore(ctx context.Context, run *instance.GroupRun) error {
	switch run.State {
	case instance.GroupRunDecided:
		if !run.ActionsApplied {
			return nil
		}
		return r.wake(ctx, run)
	case instance.GroupRunGathering:
		return r.wake(ctx, run) // crash window, or a failed rollback to retry
	}
	rep := run.Gather
	if rep == nil || rep.ActiveSession == nil {
		return r.wake(ctx, run) // the re-entered gatherer opens the recorded attempt's session
	}
	idx := latestAttemptIndex(rep, rep.MergeIndex)
	if idx < 0 {
		return r.wake(ctx, run)
	}
	active := *rep.ActiveSession
	if rep.Attempts[idx].EndedAt != nil {
		if err := r.closer.CloseSession(ctx, active, resolutionCloseEnded); err != nil {
			return fmt.Errorf("group gather reconciler: run %s: close the ended resolution session: %w", run.ID, err)
		}
		return r.wake(ctx, run)
	}
	exec, err := findGroupSessionExec(ctx, r.nodeExecs, active)
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: find the resolution session: %w", run.ID, err)
	}
	if exec != nil && IsOpenGroupResolutionSession(exec.LLMConversation) {
		if sent, _ := exec.LLMConversation["notificationSent"].(bool); !sent {
			return r.closer.NotifyResolutionRequired(ctx, active)
		}
		return nil // restored: the persisted session serves the next operator turn
	}
	abandoned := false
	if exec != nil {
		reason, _ := exec.LLMConversation[ConversationKeyEndReason].(string)
		abandoned = reason == resolutionEndAbandoned
	}
	if err := r.completer.OnResolutionSessionEnded(ctx, active, abandoned); err != nil {
		return fmt.Errorf("group gather reconciler: run %s: finish the interrupted resolution attempt: %w", run.ID, err)
	}
	return nil
}

func (r *GroupGatherReconciler) wake(ctx context.Context, run *instance.GroupRun) error {
	woke, err := r.waker.SetQueuedIfWaitingAtNode(ctx, run.ParentInstanceID, run.NodeID, r.now())
	if err != nil {
		return fmt.Errorf("group gather reconciler: run %s: wake parent %s: %w", run.ID, run.ParentInstanceID, err)
	}
	if woke {
		r.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}

// Watch subscribes to every instance's events NOW (before returning) and, in
// its own goroutine until ctx ends, reacts to terminal instance events and
// group completions: a cancelled/failed/completed parent's file gathers are
// recovered at once, and runs whose last writer just stopped are pruned. The
// returned channel closes when the loop has stopped.
func (r *GroupGatherReconciler) Watch(ctx context.Context) <-chan struct{} {
	events, unsubscribe := r.bus.SubscribeAll()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ctx.Err() != nil {
					return
				}
				r.handleEvent(ctx, ev)
			}
		}
	}()
	return done
}

func (r *GroupGatherReconciler) handleEvent(ctx context.Context, ev ScopedEvent) {
	switch ev.Event.Type {
	case EventTypeInstanceCancelled, EventTypeInstanceFailed, EventTypeInstanceCompleted:
		r.recoverParent(ctx, ev.InstanceID)
		r.pruneForChild(ctx, ev.InstanceID)
	case EventTypeGroupCompleted:
		if id, ok := eventGroupRunID(ev.Event.Data); ok {
			r.prune(ctx, id)
		}
	}
}

func (r *GroupGatherReconciler) recoverParent(ctx context.Context, parentID uuid.UUID) {
	runs, err := r.groups.ListByParent(ctx, parentID)
	if err != nil {
		slog.Warn("group gather reconciler: list the parent's group runs failed; the sweep retries", "parentID", parentID, "error", err)
		return
	}
	for _, run := range runs {
		if run.GatherMode != workflow.GroupGatherResultsAndFiles {
			continue
		}
		if run.State != instance.GroupRunDone {
			if err := r.RecoverRun(ctx, run.ID); err != nil {
				slog.Warn("group gather reconciler: run not recovered; the sweep retries", "groupRunID", run.ID, "error", err)
			}
		}
		r.prune(ctx, run.ID)
	}
}

func (r *GroupGatherReconciler) pruneForChild(ctx context.Context, childID uuid.UUID) {
	run, err := r.groups.GetByChild(ctx, childID)
	if err != nil {
		slog.Warn("group gather reconciler: resolve a task's group run failed; the sweep retries", "childID", childID, "error", err)
		return
	}
	if run == nil {
		return
	}
	r.prune(ctx, run.ID)
}

func (r *GroupGatherReconciler) prune(ctx context.Context, runID uuid.UUID) {
	if _, err := r.pruner.PruneRun(ctx, runID); err != nil {
		slog.Warn("group gather reconciler: resources not released this pass", "groupRunID", runID, "error", err)
	}
}

// withoutGatherHandle returns a copy of run whose gather report has no
// working-area handle, so AbortGather records the failure without touching an
// area that no longer exists.
func withoutGatherHandle(run *instance.GroupRun) *instance.GroupRun {
	cp := *run
	if run.Gather != nil {
		rep := copyGatherReport(*run.Gather)
		rep.Handle = nil
		cp.Gather = &rep
	}
	return &cp
}

// eventGroupRunID reads the "groupRunId" every group.* event carries.
func eventGroupRunID(data any) (uuid.UUID, bool) {
	m, ok := data.(map[string]any)
	if !ok {
		return uuid.Nil, false
	}
	s, _ := m["groupRunId"].(string)
	id, err := uuid.Parse(s)
	return id, err == nil && id != uuid.Nil
}
