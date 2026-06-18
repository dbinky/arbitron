package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
)

// scopeLabel maps a ScopeKind to its human-facing audit label.
func scopeLabel(k ScopeKind) string {
	if k == ScopeShared {
		return ScopeLabelShared
	}
	return ScopeLabelPrivate
}

// guardNoActiveInstances refuses an admin lifecycle op (Delete, Unpublish) while
// any non-terminal instance still carries the workflow's stable id, returning
// ErrHasRunningInstances. It is the single guard both ops share so the
// running-instance invariant cannot drift between them.
//
// FAIL-CLOSED on every misconfiguration of the SERVER/admin path (when a registry
// is wired):
//   - a counter error propagates (the op is refused), never treated as "no runs";
//   - a MISSING counter (registry wired but no ActiveInstanceCounter) is itself
//     refused with ErrMisconfiguredGuard. This is Must Fix #4: one safety
//     invariant must not be gated by the conjunction of two independently-optional
//     collaborators that fails open — a guard that silently no-ops on
//     misconfiguration is worse than no guard, because the operator believes they
//     are protected. An admin delete service that cannot count live runs is not
//     operable.
//
// Only the LOCAL path (no registry wired, which also has no registry id to count
// by) skips the guard — there is no stable id, no counter, and no shared store of
// live runs to protect against.
func (s *Service) guardNoActiveInstances(ctx context.Context, id uuid.UUID, name string) error {
	if s.activeInstances == nil {
		if s.registry != nil {
			// Server/admin path with a half-wired stack: refuse rather than silently
			// skip the running-instance guard.
			return fmt.Errorf("%w: active-instance counter not wired for %q", ErrMisconfiguredGuard, name)
		}
		return nil // local/unconfigured mode: no registry id to count by.
	}
	active, err := s.activeInstances.CountActiveByWorkflowID(ctx, id)
	if err != nil {
		return fmt.Errorf("counting active instances for %q: %w", name, err)
	}
	if active > 0 {
		return fmt.Errorf("%w: %s (%d active)", ErrHasRunningInstances, name, active)
	}
	return nil
}

// Delete permanently removes a workflow (admin-only). It is the registry-aware,
// guarded counterpart to the prior private-scope delete: it works for SHARED
// workflows too (relaxing the shared-read-only rule for this admin op) and
// preserves history — past instance and audit rows are NEVER touched.
//
// GUARD: if any non-terminal instance still carries this workflow's stable id the
// delete is refused with ErrHasRunningInstances; deleting a live workflow would
// orphan its run. The guard consults the wired ActiveInstanceCounter; when none is
// wired (local/unconfigured mode, which also has no registry id) the guard is
// skipped.
//
// CROSS-STORE ORDERING (folder, publication, and registry are separate stores with
// no shared transaction; mirrors Rename/Publish):
//  1. requireAdmin.
//  2. Resolve id + scope from the registry. ErrNotFound if the name is unknown.
//  3. Guard on non-terminal instances → ErrHasRunningInstances (nothing removed).
//  4. Remove the publication record (if any) — best-effort, idempotent.
//  5. Remove the name-keyed folder.
//  6. Remove the registry row LAST, so a partial failure never leaves a dangling
//     id whose folder is already gone (the registry row is the identity authority).
//
// When no registry is wired (MCP/local mode), Delete falls back to the prior
// behavior: an admin-gated delete of the caller's private folder (with the shared
// scope still surfaced via translateWrite for an informative error).
func (s *Service) Delete(ctx context.Context, name string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}

	// Local/unconfigured mode: no global identity to coordinate. Delete the
	// caller's private folder, mapping a shared-only name to an informative error.
	if s.registry == nil {
		id, err := caller(ctx)
		if err != nil {
			return err
		}
		return s.translateWrite(ctx, name, s.repo.Delete(ctx, Private(id.UserID), name))
	}

	// Resolve the stable id and the scope (which folder to remove) in one lookup.
	id, scope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}

	// Guard: refuse while non-terminal instances carry this id. History (terminal
	// instance rows + audit) is preserved either way — the guard only blocks; it
	// never deletes instance rows.
	if err := s.guardNoActiveInstances(ctx, id, name); err != nil {
		return err
	}

	// Remove the publication record first (if the name is published). Idempotent:
	// Delete on an unpublished name is a no-op. A shared workflow may carry one; a
	// private workflow normally does not.
	if s.published != nil {
		if _, ok, perr := s.published.PublicationOf(ctx, name); perr != nil {
			return fmt.Errorf("looking up publication for %q: %w", name, perr)
		} else if ok {
			if derr := s.published.Delete(ctx, name); derr != nil {
				return fmt.Errorf("removing publication for %q: %w", name, derr)
			}
		}
	}

	// Remove the name-keyed folder.
	if err := s.repo.Delete(ctx, scope, name); err != nil {
		return fmt.Errorf("removing workflow folder %q: %w", name, err)
	}

	// Remove the registry row LAST (the identity authority). A failure here leaves
	// a dangling id with no folder; surface it loudly so the operator reconciles.
	if err := s.registry.Delete(ctx, id); err != nil {
		return fmt.Errorf("removing registry row for %q: %w", name, err)
	}

	// Best-effort audit (nil-safe per audit.Service).
	s.audit.Record(ctx, audit.ActionWorkflowDelete, name, auditDetail(map[string]string{
		"scope": scopeLabel(scope.Kind),
	}))
	return nil
}

// Unpublish removes a workflow from the shared workspace (admin-only): it deletes
// the published_workflows record, the shared/<name> folder, and the shared name's
// registry entry. Any private copy of the same name is left intact (only the shared
// scope is touched). Relaxes the shared-read-only rule for this admin op.
//
// Unpublishing a name that is not published returns ErrNotFound (there is nothing
// to unpublish).
//
// GUARD: like Delete, Unpublish is refused with ErrHasRunningInstances while any
// non-terminal instance still carries the shared workflow's stable id. Unpublish
// removes the shared registry row that Rerun resolves WorkflowID->name through
// (and the anchor the archived-trigger refusal resolves against), so allowing it
// while runs are live would silently degrade rerun resolution for every
// historical instance. The guard keeps Unpublish symmetric with Delete (option a
// of PR #44 Must Fix #5).
//
// CROSS-STORE ORDERING:
//  1. Resolve the shared name's registry id (so we can remove it in step 4).
//  2. Guard on non-terminal instances → ErrHasRunningInstances (nothing removed).
//  3. Remove the publication record.
//  4. Remove the shared folder (ErrNotFound is idempotent).
//  5. Remove the registry row LAST, matching Delete's ordering: the registry is
//     the identity authority; removing it last means a failure at step 5 leaves a
//     dangling id whose folder is already gone, which is preferable to a reachable
//     id that blocks re-publish under the same name.
func (s *Service) Unpublish(ctx context.Context, name string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.published == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}

	if _, ok, err := s.published.PublicationOf(ctx, name); err != nil {
		return fmt.Errorf("looking up publication for %q: %w", name, err)
	} else if !ok {
		return fmt.Errorf("%w: %s is not published", ErrNotFound, name)
	}

	// Step 1: resolve the shared name's registry id (pre-flight for the guard and
	// step 5). When no registry is wired (local/unconfigured mode) or the entry is
	// missing, sharedID stays Nil — the guard is then skipped (no id to count by)
	// and step 5 is skipped.
	var sharedID uuid.UUID
	if s.registry != nil {
		if id, rerr := s.registry.ResolveID(ctx, name); rerr == nil {
			sharedID = id
		} else if !errors.Is(rerr, ErrNotFound) {
			return fmt.Errorf("resolving shared registry id for %q: %w", name, rerr)
		}
	}

	// Step 2: refuse while non-terminal instances carry this id (symmetric with
	// Delete). Skipped when sharedID is Nil (no id to count by).
	if sharedID != uuid.Nil {
		if err := s.guardNoActiveInstances(ctx, sharedID, name); err != nil {
			return err
		}
	}

	// Step 3: remove the publication record.
	if err := s.published.Delete(ctx, name); err != nil {
		return fmt.Errorf("removing publication for %q: %w", name, err)
	}

	// Step 4: remove the shared folder (ErrNotFound is idempotent).
	if err := s.repo.Delete(ctx, Shared(), name); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("removing shared folder %q: %w", name, err)
	}

	// Step 5: remove the shared registry row LAST so a re-publish under the same
	// name is possible. Without this, the cross-scope uniqueness check in Publish
	// would see the lingering registry entry and reject the re-publish.
	if s.registry != nil && sharedID != uuid.Nil {
		if err := s.registry.Delete(ctx, sharedID); err != nil {
			return fmt.Errorf("removing shared registry row for %q: %w", name, err)
		}
	}

	// Best-effort audit (nil-safe per audit.Service).
	s.audit.Record(ctx, audit.ActionWorkflowUnpublish, name, auditDetail(nil))
	return nil
}
