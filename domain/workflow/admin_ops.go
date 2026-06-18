package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
)

// Duplicate copies an existing workflow's definition into the REQUESTER's private
// space under newName with a NEW stable id (admin-only). The source may be in any
// scope (private or shared); the copy always lands in the admin caller's private
// space and is fully independent of the original — references keyed by the
// original id are untouched.
//
// CROSS-STORE ORDERING (folder + registry are separate stores, mirrors Create):
//  1. requireAdmin.
//  2. Resolve the source id + scope (ErrNotFound if the name is unknown).
//  3. Uniqueness: newName must not already resolve to an entry → ErrAlreadyExists
//     (nothing copied).
//  4. CopyTree(source → requester's private/newName). A pre-existing folder there
//     surfaces as ErrAlreadyExists from the repo.
//  5. Register the new name with a freshly allocated id. On failure, roll back the
//     copied folder so the two stores do not diverge.
//  6. Stamp the new id onto the copied folder's workflow.json so the copy carries
//     its own identity (best-effort within the success path).
func (s *WorkflowService) Duplicate(ctx context.Context, name, newName string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.registry == nil {
		return fmt.Errorf("%w: workflow duplication is not configured", ErrUnauthorized)
	}

	// Resolve the source identity + scope (which folder to copy FROM) in one lookup.
	_, srcScope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}

	// Uniqueness: the target name must be free globally.
	if _, err := s.registry.ResolveID(ctx, newName); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newName)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	// Copy the definition into the requester's private space.
	dstScope := Private(id.UserID)
	if err := s.repo.CopyTree(ctx, srcScope, name, dstScope, newName, false); err != nil {
		return err
	}

	// Register the copy under a fresh id, scoped private to the requester. On
	// failure, roll back the copied folder (no orphan folder with no identity).
	newID := uuid.New()
	if err := s.registry.Register(ctx, RegistryEntry{
		ID:      newID,
		Name:    newName,
		Scope:   ScopePrivate,
		OwnerID: id.UserID,
	}); err != nil {
		_ = s.repo.Delete(ctx, dstScope, newName)
		return err
	}

	// Stamp the new id (and name) onto the copied folder so the copy carries its
	// own stable identity rather than the source's. Best-effort within the success
	// path: the folder + registry are already consistent on the name.
	if wf, lerr := s.repo.Load(ctx, dstScope, newName); lerr == nil {
		if wf.ID != newID || wf.Name != newName {
			wf.ID = newID
			wf.Name = newName
			if uerr := s.repo.Update(ctx, dstScope, newName, wf); uerr != nil {
				return fmt.Errorf("stamping duplicated workflow %q identity: %w", newName, uerr)
			}
		}
	}

	s.audit.Record(ctx, audit.ActionWorkflowDuplicate, newName, auditDetail(map[string]string{
		"duplicatedFrom": name,
	}))
	return nil
}

// Archive marks a workflow archived (admin-only): it stays listable and editable,
// and its in-flight instances continue, but new triggers are refused
// (engine returns ErrArchived). Archiving an already-archived workflow is a no-op.
func (s *WorkflowService) Archive(ctx context.Context, name string) error {
	return s.setArchived(ctx, name, true)
}

// Unarchive clears the archived flag (admin-only), making the workflow triggerable
// again. Unarchiving an already-active workflow is a no-op.
func (s *WorkflowService) Unarchive(ctx context.Context, name string) error {
	return s.setArchived(ctx, name, false)
}

// setArchived is the shared archive/unarchive implementation: admin-gated,
// registry-coordinated, idempotent (SetArchived to the same value is harmless).
func (s *WorkflowService) setArchived(ctx context.Context, name string, archived bool) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.registry == nil {
		return fmt.Errorf("%w: workflow archiving is not configured", ErrUnauthorized)
	}
	id, err := s.registry.ResolveID(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}
	if err := s.registry.SetArchived(ctx, id, archived); err != nil {
		return err
	}
	action := audit.ActionWorkflowArchive
	if !archived {
		action = audit.ActionWorkflowUnarchive
	}
	s.audit.Record(ctx, action, name, auditDetail(nil))
	return nil
}

// TransferOwnership reassigns a workflow's owner (admin-only). For a PRIVATE
// workflow it ALSO moves the name-keyed folder from the old owner's private space
// to the new owner's (CopyTree+Delete, ordered with rollback like Rename); a
// SHARED workflow has no single private owner folder, so only the registry owner
// (the publisher/owner attribution) is updated, with no folder move.
//
// CROSS-STORE ORDERING (private case; folder + registry are separate stores):
//  1. requireAdmin; resolve id + scope (ErrNotFound if unknown).
//  2. SHARED scope: SetOwner only, return (no folder move).
//  3. PRIVATE scope: CopyTree(old-owner → new-owner). On failure, abort.
//  4. SetOwner. On failure, roll back the copied folder (no half-transfer).
//  5. Delete the old-owner folder. A delete failure leaves a cosmetic stale
//     folder (the registry + new folder are authoritative) and is surfaced.
func (s *WorkflowService) TransferOwnership(ctx context.Context, name string, newOwnerID uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.registry == nil {
		return fmt.Errorf("%w: workflow ownership transfer is not configured", ErrUnauthorized)
	}

	id, scope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}

	// Shared workflow: reassign the owner/publisher attribution only — there is no
	// per-user private folder to move.
	if scope.Kind == ScopeShared {
		if err := s.registry.SetOwner(ctx, id, newOwnerID); err != nil {
			return err
		}
		s.audit.Record(ctx, audit.ActionWorkflowTransfer, name, auditDetail(map[string]string{
			"to":    newOwnerID.String(),
			"scope": ScopeLabelShared,
		}))
		return nil
	}

	// Private workflow: move the folder between user trees alongside the owner
	// reassignment. A no-op transfer (same owner) still moves nothing meaningful;
	// CopyTree(src==dst) would collide, so short-circuit it.
	oldOwner := scope.OwnerID
	if oldOwner == newOwnerID {
		return nil
	}
	dstScope := Private(newOwnerID)
	// Same move-with-rollback envelope as Rename: copy → SetOwner (rolls back the
	// copy on failure) → delete the old folder. No folder relabel here (the name is
	// unchanged; only the owner moves).
	if err := s.copyThenSwap(ctx, scope, name, dstScope, name,
		func(ctx context.Context) error {
			return s.registry.SetOwner(ctx, id, newOwnerID)
		}); err != nil {
		return err
	}

	s.audit.Record(ctx, audit.ActionWorkflowTransfer, name, auditDetail(map[string]string{
		"from":  oldOwner.String(),
		"to":    newOwnerID.String(),
		"scope": ScopeLabelPrivate,
	}))
	return nil
}
