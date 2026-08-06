package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// RunCanceller is the one operation the orphan sweep needs from the engine: an
// authorized cancellation that cascades. *Engine.Cancel satisfies it.
//
// It is a narrow port rather than a back-reference to the whole Engine so the
// reconciler is testable with a recording fake, matching SpawnWaitReconciler's
// dependency shape.
type RunCanceller interface {
	Cancel(ctx context.Context, instanceID uuid.UUID) error
}

// OrphanedChildReconciler cancels runs that an interrupted cancel cascade left
// behind: still-active children of an already-cancelled parent.
//
// WHY A SWEEP IS REQUIRED. Engine.cascadeCancel is a best-effort in-process
// walk. It tolerates partial failure by design — the parent's own cancellation
// has already succeeded and must not be reported as failed because one
// descendant was uncooperative — and it can be cut short by a storage error on
// one branch or by the process dying mid-walk. Every one of those outcomes
// leaves an active run under a cancelled parent, holding queue capacity, a
// workspace, and the parent's inherited credentials, AFTER the operator has
// been told the tree is gone. Nothing else notices; the cascade is fire and
// forget. This sweep is what makes the containment control eventually
// consistent rather than best-effort-and-then-silent.
//
// IDENTITY. Cancellation is authorized, so the sweep cannot run headless: it
// reconstructs the ORPHAN OWNER's identity and cancels under it — the same
// identity reconstruction the spawn executor performs before creating a child,
// through the same IdentityResolver value. Children inherit their parent's
// owner, so this is the identity that authorized the original cancellation
// anyway. A resolution failure SKIPS the orphan (logged); it is never
// downgraded to a system identity, which would widen what the cancellation may
// reach.
//
// Cancel cascades, so cancelling a top-level orphan tears down its own subtree
// in the same pass; the query only needs to be one level deep.
type OrphanedChildReconciler struct {
	spawnLinks instance.SpawnLinkRepository
	instances  instance.InstanceRepository
	identities IdentityResolver
	canceller  RunCanceller
}

// NewOrphanedChildReconciler constructs the sweep.
func NewOrphanedChildReconciler(
	spawnLinks instance.SpawnLinkRepository,
	instances instance.InstanceRepository,
	identities IdentityResolver,
	canceller RunCanceller,
) *OrphanedChildReconciler {
	return &OrphanedChildReconciler{
		spawnLinks: spawnLinks,
		instances:  instances,
		identities: identities,
		canceller:  canceller,
	}
}

// Reconcile cancels every active child of a cancelled parent.
//
// The query error is RETURNED so the caller can surface it; a per-orphan
// failure is logged and the sweep continues — one uncooperative run must not
// abandon the rest. An orphan that reached a terminal state between the query
// and the cancel returns ErrCannotCancel, which is the COMMON benign race and
// is not logged as a failure.
func (r *OrphanedChildReconciler) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	orphans, err := r.spawnLinks.ListOrphanedActiveChildren(ctx)
	if err != nil {
		return fmt.Errorf("orphaned-child reconciler: listing orphans: %w", err)
	}

	cancelled := 0
	for _, childID := range orphans {
		child, gerr := r.instances.Get(ctx, childID)
		if gerr != nil {
			slog.Warn("orphaned-child reconciler: skipping unreadable orphan",
				"childID", childID, "error", gerr)
			continue
		}
		ownerIdentity, ierr := r.identities.IdentityForUser(ctx, child.OwnerID)
		if ierr != nil {
			slog.Warn("orphaned-child reconciler: skipping orphan whose owner identity did not resolve",
				"childID", childID, "ownerID", child.OwnerID, "error", ierr)
			continue
		}

		if cerr := r.canceller.Cancel(identity.WithIdentity(ctx, ownerIdentity), childID); cerr != nil {
			if errors.Is(cerr, instance.ErrCannotCancel) {
				// Already terminal — it finished between the query and now.
				continue
			}
			slog.Warn("orphaned-child reconciler: failed to cancel orphan",
				"childID", childID, "error", cerr)
			continue
		}
		cancelled++
	}

	if cancelled > 0 {
		slog.Info("orphaned-child reconciler: cancelled runs stranded under a cancelled parent",
			"count", cancelled)
	}
	return nil
}
