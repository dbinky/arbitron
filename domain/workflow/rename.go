package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// requireAdmin is the shared authorization gate for the admin-only workflow
// lifecycle operations (Rename and the later archive/delete/transfer ops). It
// mirrors the UserService pattern: a missing identity or a non-admin caller is
// rejected with ErrForbidden (the canonical identity sentinel, so the adapters
// map it to 403 uniformly). Local/unconfigured mode seeds an admin identity, so
// it passes there too.
func (s *WorkflowService) requireAdmin(ctx context.Context) error {
	id, ok := identity.FromContext(ctx)
	if !ok || !id.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// Rename changes a workflow's human-facing name (admin-only). The stable id is
// the authority for every reference (instances, schedules, publications), so a
// rename only moves the name-keyed folder and updates the registry name+the
// workflow.json label — it NEVER touches the reference tables; they stay correct
// via the unchanged id.
//
// CROSS-STORE MOVE ORDERING (mirrors adapter/sqlite/department_renamer + Publish):
// the folder and the registry are two separate stores with no shared transaction.
//
//  1. requireAdmin.
//  2. Resolve id := registry.ResolveID(name); ErrNotFound if the name is unknown.
//  3. Uniqueness: if newName already resolves to an existing entry → ErrAlreadyExists
//     (nothing is moved).
//  4. CopyTree(old → new) within the SAME scope (private of the owner, or shared).
//  5. registry.Rename(id, newName). If this fails, ROLL BACK the copy (delete new)
//     and return — no half-rename leaving two folders and a stale registry.
//  6. Update the new folder's workflow.json label to newName (best-effort within
//     the success path; the folder + registry are already consistent).
//  7. Delete the old folder. If this fails the registry+new folder are already
//     correct, so the error is surfaced (the stale old folder is cosmetic, not a
//     correctness break — Load resolves via the new name).
func (s *WorkflowService) Rename(ctx context.Context, name, newName string) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.registry == nil {
		// Rename is a registry-coordinated lifecycle op; without a registry there is
		// no global identity to move (local/unconfigured mode does not expose it).
		return fmt.Errorf("%w: workflow renaming is not configured", ErrUnauthorized)
	}

	// Resolve the source identity and its scope (which folder to move).
	id, err := s.registry.ResolveID(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}
	scope, err := s.scopeOf(ctx, id, name)
	if err != nil {
		return err
	}

	// Uniqueness: the target name must not already be held by another entry.
	if _, err := s.registry.ResolveID(ctx, newName); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newName)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	// 4. Copy the folder old -> new within the same scope.
	if err := s.repo.CopyTree(ctx, scope, name, scope, newName, false); err != nil {
		return err
	}

	// 5. Rename the registry entry. On failure, roll back the copy (no half-rename).
	if err := s.registry.Rename(ctx, id, newName); err != nil {
		_ = s.repo.Delete(ctx, scope, newName)
		return err
	}

	// 6. Update the workflow.json label inside the new folder so it matches its
	// name. Read-modify-write through the repo; on failure the registry+folder are
	// already consistent, so surface the error rather than rolling back identity.
	if wf, lerr := s.repo.Load(ctx, scope, newName); lerr == nil {
		if wf.Name != newName {
			wf.Name = newName
			if uerr := s.repo.Update(ctx, scope, newName, wf); uerr != nil {
				return fmt.Errorf("updating renamed workflow %q label: %w", newName, uerr)
			}
		}
	}

	// 7. Delete the old folder. The registry + new folder are authoritative now;
	// a delete failure leaves a cosmetic stale folder but not a correctness break.
	if err := s.repo.Delete(ctx, scope, name); err != nil {
		return fmt.Errorf("removing old workflow folder %q after rename: %w", name, err)
	}
	return nil
}

// scopeOf resolves the storage scope (private-of-owner or shared) for the
// registry entry id, so a rename moves the folder in the correct place. The
// WorkflowRegistry exposes scope/owner only on the full entries from List, so it
// is matched there by id.
func (s *WorkflowService) scopeOf(ctx context.Context, id uuid.UUID, name string) (Scope, error) {
	entries, err := s.registry.List(ctx)
	if err != nil {
		return Scope{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			if e.Scope == ScopeShared {
				return Shared(), nil
			}
			return Private(e.OwnerID), nil
		}
	}
	return Scope{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}
