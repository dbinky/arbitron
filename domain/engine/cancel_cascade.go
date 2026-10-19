package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// NoopSpawnLinks is the Null Object for instance.SpawnLinkRepository.
//
// DANGER — INSTALLING IT DISABLES DESCENDANT CANCELLATION. An Engine holding
// this reports that no run has any children, so Cancel tears down the named run
// ONLY and every spawned descendant keeps running with the parent's inherited
// credentials, burning queue capacity and billed provider calls after the
// operator believes the tree is gone. Rows also report ChildCount 0 and render
// no hierarchy expander.
//
// It exists so a composition root that genuinely has no link store can say so
// EXPLICITLY, and so the nil case can be rejected outright: a silently nil
// containment control is indistinguishable from a working one at the call site.
type NoopSpawnLinks struct{}

// ChildCountsByParents always reports no children.
func (NoopSpawnLinks) ChildCountsByParents(context.Context, []uuid.UUID, instance.ChildVisibility) (map[uuid.UUID]int, error) {
	return map[uuid.UUID]int{}, nil
}

// ListActiveChildren always reports no active children.
func (NoopSpawnLinks) ListActiveChildren(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return []uuid.UUID{}, nil
}

// ListChildren always reports no children, disabling the cascade.
func (NoopSpawnLinks) ListChildren(context.Context, uuid.UUID) ([]instance.ChildRef, error) {
	return []instance.ChildRef{}, nil
}

// MarkSubtreeCancelRequested records nothing under this explicit Null Object.
func (NoopSpawnLinks) MarkSubtreeCancelRequested(context.Context, uuid.UUID) error {
	return nil
}

// ListOrphanedActiveChildren always reports no orphans.
func (NoopSpawnLinks) ListOrphanedActiveChildren(context.Context) ([]uuid.UUID, error) {
	return []uuid.UUID{}, nil
}

// SetSpawnLinks wires the link-addressed read surface used by the cancel
// cascade and the list enricher's child counts.
//
// repo MUST be non-nil — pass NoopSpawnLinks{} to opt out DELIBERATELY, reading
// its doc comment first, because opting out disables descendant cancellation.
// This mirrors SetQueuePositionProvider: a nil here is a programming error that
// would silently downgrade a containment control to a no-op, so it panics
// rather than fails open.
func (e *Engine) SetSpawnLinks(repo instance.SpawnLinkRepository) {
	if repo == nil {
		panic("engine: SetSpawnLinks requires a non-nil SpawnLinkRepository (use NoopSpawnLinks{} to disable descendant cancellation deliberately)")
	}
	e.spawnLinks = repo
}

// setCancelledState distinguishes a lost terminal-state race from a storage
// failure. Group-task terminal writes are immutable: completion can win after
// cancelAuthorized's read. Re-authorize the fresh terminal row before returning
// ErrCannotCancel, so Cancel still stamps a terminal root and traverses its
// descendants without publishing a cancellation event for the lost write.
func (e *Engine) setCancelledState(ctx context.Context, id uuid.UUID) error {
	err := e.instances.UpdateState(ctx, id, instance.StateCancelled)
	if err == nil {
		return nil
	}
	if fresh, readErr := e.authorizeIntervention(ctx, id); readErr == nil && fresh.State.IsTerminal() {
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", id, fresh.State, instance.ErrCannotCancel)
	}
	return fmt.Errorf("failed to update state: %w", err)
}

// cascadeCancel cancels every active descendant, including those below terminal
// intermediates, and returns how many it cancelled. Every node is re-authorized:
// only an authorized terminal node (ErrCannotCancel) may be traversed unchanged.
// Other errors halt that branch. Storage failures are best-effort; the orphan
// sweep repairs residue under cancelled or subtree-cancel-stamped ancestors.
//
// visited bounds corrupt link cycles. ctx must already be detached from request
// cancellation (while retaining identity), so disconnects cannot strand work.
func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) int {
	children, err := e.spawnLinks.ListChildren(ctx, parentID)
	if err != nil {
		slog.Error("cancel cascade: failed to list children", "parentID", parentID, "error", err)
		return 0
	}

	cancelled := 0
	for _, child := range children {
		if visited[child.ID] {
			continue
		}
		visited[child.ID] = true

		cerr := e.cancelAuthorized(ctx, child.ID)
		switch {
		case cerr == nil:
			cancelled++
		case errors.Is(cerr, instance.ErrCannotCancel):
			// Authorized terminal node: leave its outcome untouched, but descend.
		default:
			slog.Warn("cancel cascade: failed to cancel child, branch not descended",
				"parentID", parentID, "childID", child.ID, "listedState", child.State, "error", cerr)
			continue
		}
		cancelled += e.cascadeCancel(ctx, child.ID, visited)
	}
	return cancelled
}
