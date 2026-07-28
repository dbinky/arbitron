package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
)

// SpawnWaitReconciler guarantees that no parent waits forever on a child that
// is already finished.
//
// An in-process wake (Engine.OnChildTerminal) is LOST if the process dies
// between the child's terminal write and the parent's re-queue. This sweep
// closes that hole and is therefore THE AUTHORITY FOR CORRECTNESS: the
// in-process hooks only make the common case immediate.
//
// STARTUP ORDERING IS SIGNIFICANT: it must run AFTER
// Engine.RecoverCrashedInstances. That sweep marks instances left in the
// running state by a crash as failed — which includes crashed CHILDREN — so
// running afterwards means those children are already terminal and their
// parents resume through the failure port in the SAME startup pass instead of
// hanging until the next restart. A waiting parent is itself untouched by
// RecoverCrashedInstances, since it is not in the running state.
type SpawnWaitReconciler struct {
	spawnWaits instance.SpawnWaitRepository
	instances  instance.InstanceRepository
	queueRepo  instance.QueueRepository
	queueMgr   QueueManager
}

// NewSpawnWaitReconciler constructs the reconciler.
func NewSpawnWaitReconciler(
	spawnWaits instance.SpawnWaitRepository,
	instances instance.InstanceRepository,
	queueRepo instance.QueueRepository,
	queueMgr QueueManager,
) *SpawnWaitReconciler {
	return &SpawnWaitReconciler{
		spawnWaits: spawnWaits,
		instances:  instances,
		queueRepo:  queueRepo,
		queueMgr:   queueMgr,
	}
}

// Reconcile re-queues every waiting parent whose awaited child is already
// terminal.
//
// The query error is RETURNED so the caller can surface it; the composition
// root logs it without blocking startup, matching the posture of the existing
// recovery sweeps. A per-candidate failure is logged and the sweep continues —
// one uncooperative parent must not abandon the rest.
func (r *SpawnWaitReconciler) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	candidates, err := r.spawnWaits.ListWaitingOnTerminalChild(ctx)
	if err != nil {
		return fmt.Errorf("spawn-wait reconciler: listing candidates: %w", err)
	}

	requeued := 0
	for _, c := range candidates {
		// Re-verify against the CURRENT row: the query result may be stale (the
		// parent could have been cancelled, or already woken in-process, between
		// the query and now). A cancelled parent must never be resurrected.
		parent, gerr := r.instances.Get(ctx, c.ParentID)
		if gerr != nil {
			slog.Warn("spawn-wait reconciler: skipping unreadable parent",
				"parentID", c.ParentID, "error", gerr)
			continue
		}
		if parent.State != instance.StateWaiting {
			continue
		}
		if parent.CurrentNodeID == nil || *parent.CurrentNodeID != c.ParentNodeID {
			continue
		}

		// Conditional, for the same reason the in-process wake is: the executor
		// may be claiming this very parent back to `running` right now. Losing
		// the race is success, not failure — the parent is resuming either way.
		ok, serr := r.queueRepo.SetQueuedIfWaiting(ctx, c.ParentID, time.Now())
		if serr != nil {
			slog.Error("spawn-wait reconciler: failed to re-queue parent",
				"parentID", c.ParentID, "childID", c.ChildID, "error", serr)
			continue
		}
		if !ok {
			continue
		}
		requeued++
	}

	if requeued > 0 {
		slog.Info("spawn-wait reconciler: re-queued parents whose awaited child had already finished",
			"count", requeued)
		r.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}
