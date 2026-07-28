package engine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// SetSpawnLinks wires the link-addressed read surface used by the cancel
// cascade and the list enricher's child counts. It is OPTIONAL: when unset,
// cancellation behaves exactly as it did before this feature and rows report no
// children.
func (e *Engine) SetSpawnLinks(repo instance.SpawnLinkRepository) {
	e.spawnLinks = repo
}

// cascadeCancel cancels every active child of parentID, depth-first.
//
// SCOPE: EVERY active child, BOTH spawn modes. The spec states the requirement
// without qualification. The tempting reading — that fire-and-forget children
// should survive because the parent disclaimed the child's lifecycle — confuses
// two directions: the spec's fire-and-forget carve-outs are all child->parent
// ("fire-and-forget child outcomes do not affect the parent"), and nothing
// weakens the parent->child direction. The outcome the cascade serves —
// operators tearing down a run tree without hunting for orphans — is defeated
// precisely by the children nobody is waiting on, since those are the ones no
// later failure will surface.
//
// AUTHORIZATION needs no special-casing: children inherit the parent's owner, so
// an identity authorized to intervene on the parent is authorized on every
// descendant. Routing through cancelAuthorized (rather than writing states
// directly) preserves the intervention audit trail for each cancelled run.
//
// PARTIAL FAILURE IS TOLERATED, NOT FATAL. The parent's own cancellation has
// already succeeded and must not be reported as failed because a descendant was
// uncooperative. A child already in a terminal state is the COMMON case and
// returns ErrCannotCancel, which the cascade treats as success.
//
// The `visited` set makes corrupt or hand-edited link data degrade into a
// halted traversal rather than unbounded recursion. The link graph is acyclic
// by construction — a child is always created after its parent — so this guard
// is unreachable in practice.
func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) {
	if e.spawnLinks == nil {
		return
	}
	children, err := e.spawnLinks.ListActiveChildren(ctx, parentID)
	if err != nil {
		// Logged, not fatal: the parent's state is the caller's contract. Any child
		// left running remains visible in the hierarchy, so the inconsistency is
		// observable rather than hidden.
		slog.Error("cancel cascade: failed to list active children",
			"parentID", parentID, "error", err)
		return
	}

	for _, childID := range children {
		if visited[childID] {
			continue
		}
		visited[childID] = true

		if cerr := e.cancelAuthorized(ctx, childID); cerr != nil {
			if !errors.Is(cerr, instance.ErrCannotCancel) {
				slog.Warn("cancel cascade: failed to cancel child",
					"parentID", parentID, "childID", childID, "error", cerr)
			}
			// Continue regardless: an already-terminal child may still have active
			// grandchildren of its own.
		}
		e.cascadeCancel(ctx, childID, visited)
	}
}
