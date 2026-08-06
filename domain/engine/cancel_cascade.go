package engine

import (
	"context"
	"errors"
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

// ListActiveChildren always reports no active children, which is what makes the
// cancel cascade a no-op under this Null Object.
func (NoopSpawnLinks) ListActiveChildren(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return []uuid.UUID{}, nil
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
// ctx MUST already be DETACHED from the caller's request (see Cancel): the walk
// below issues one authorized cancellation per descendant, and a client
// disconnect or request timeout part-way through would abandon the remainder of
// the subtree with the parent already cancelled.
func (e *Engine) cascadeCancel(ctx context.Context, parentID uuid.UUID, visited map[uuid.UUID]bool) {
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
			if errors.Is(cerr, instance.ErrCannotCancel) {
				// Already terminal: still descend, since a terminal child may have
				// active grandchildren of its own.
				e.cascadeCancel(ctx, childID, visited)
				continue
			}
			// A real failure (authorization denied, storage error, not-found): the
			// child ITSELF is untouched, so descending into its subtree with the
			// same identity could authorize a grandchild the actor could not
			// authorize on the child — cancelling a descendant while the node
			// between it and the parent stays running. Halt this branch instead of
			// leaving that inconsistent, unannounced hole in the tree.
			slog.Warn("cancel cascade: failed to cancel child, branch not descended",
				"parentID", parentID, "childID", childID, "error", cerr)
			continue
		}
		e.cascadeCancel(ctx, childID, visited)
	}
}
