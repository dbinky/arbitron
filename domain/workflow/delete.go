package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
)

// scopeLabel maps a ScopeKind to its human-facing audit label.
func scopeLabel(k ScopeKind) string {
	if k == ScopeShared {
		return ScopeLabelShared
	}
	return ScopeLabelPrivate
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
func (s *WorkflowService) Delete(ctx context.Context, name string) error {
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

	// Resolve the stable id and the scope (which folder to remove).
	id, err := s.registry.ResolveID(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}
	scope, err := s.scopeOf(ctx, id, name)
	if err != nil {
		return err
	}

	// Guard: refuse while non-terminal instances carry this id. History (terminal
	// instance rows + audit) is preserved either way — the guard only blocks; it
	// never deletes instance rows.
	if s.activeInstances != nil {
		active, cerr := s.activeInstances.CountActiveByWorkflowID(ctx, id)
		if cerr != nil {
			return fmt.Errorf("counting active instances for %q: %w", name, cerr)
		}
		if active > 0 {
			return fmt.Errorf("%w: %s (%d active)", ErrHasRunningInstances, name, active)
		}
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
// the published_workflows record and the shared/<name> folder. Any private copy of
// the same name is left intact (only the shared scope is touched). Relaxes the
// shared-read-only rule for this admin op.
//
// Unpublishing a name that is not published returns ErrNotFound (there is nothing
// to unpublish). The publication record is removed before the folder so a partial
// failure does not leave an unreferenced shared folder claimed by a stale record.
func (s *WorkflowService) Unpublish(ctx context.Context, name string) error {
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

	// Remove the publication record, then the shared folder.
	if err := s.published.Delete(ctx, name); err != nil {
		return fmt.Errorf("removing publication for %q: %w", name, err)
	}
	if err := s.repo.Delete(ctx, Shared(), name); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("removing shared folder %q: %w", name, err)
	}

	// Best-effort audit (nil-safe per audit.Service).
	s.audit.Record(ctx, audit.ActionWorkflowUnpublish, name, auditDetail(nil))
	return nil
}
