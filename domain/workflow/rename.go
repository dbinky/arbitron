package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// requireAdmin is the shared authorization gate for the admin-only workflow
// lifecycle operations (Rename and the later archive/delete/transfer ops). It
// mirrors the UserService pattern: a missing identity or a non-admin caller is
// rejected with ErrForbidden (the canonical identity sentinel, so the adapters
// map it to 403 uniformly). Local/unconfigured mode seeds an admin identity, so
// it passes there too.
func (s *Service) requireAdmin(ctx context.Context) error {
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
// The move-with-rollback envelope (copy → mutate-registry-with-rollback → delete
// source) lives in copyThenSwap, shared with Transfer-private so the commit-point
// discipline is asserted in ONE tested home.
//
//  1. requireAdmin.
//  2. Resolve id + scope in a single lookup; ErrNotFound if the name is unknown.
//  3. Uniqueness: if newName already resolves to an existing entry → ErrAlreadyExists
//     (nothing is moved).
//  4. copyThenSwap(old → new): copy the folder, then — BEFORE the registry commits
//     identity — update the new folder's workflow.json label to newName, then
//     registry.Rename. A failure of EITHER the label update or the registry rename
//     rolls back through the copy-delete envelope (delete new), so no half-rename
//     leaves the old folder, the new folder, and a moved registry all diverging.
//     Finally the old folder is removed.
func (s *Service) Rename(ctx context.Context, name, newName string) error {
	if s.stamper != nil {
		return s.renameRevision(ctx, name, newName)
	}
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.localMode() {
		// Rename is a registry-coordinated lifecycle op; without a global identity
		// store there is nothing to move (local/unconfigured mode does not expose it).
		return fmt.Errorf("%w: workflow renaming is not configured", ErrUnauthorized)
	}

	// Resolve the source identity and its scope (which folder to move) in one lookup.
	id, scope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return err // ErrNotFound propagates
	}

	// Uniqueness: the target name must not already be held by another entry.
	if _, err := s.registry.ResolveID(ctx, newName); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newName)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	// Move the folder old -> new within the same scope, relabel BEFORE committing
	// identity, then commit the registry rename — all under the rollback envelope.
	if err := s.copyThenSwap(ctx, scope, name, scope, newName,
		func(ctx context.Context) error {
			// Relabel the new folder's workflow.json to newName BEFORE registry.Rename
			// commits identity, so a relabel failure rolls back the copy (Rename
			// step-6 fix: never return after identity commit but before source cleanup).
			if wf, lerr := s.repo.Load(ctx, scope, newName); lerr == nil {
				if wf.Name != newName {
					wf.Name = newName
					if uerr := s.repo.Update(ctx, scope, newName, wf); uerr != nil {
						return fmt.Errorf("updating renamed workflow %q label: %w", newName, uerr)
					}
				}
			}
			return s.registry.Rename(ctx, id, newName)
		}); err != nil {
		return err
	}

	// Best-effort audit of the successful rename, carrying the before/after names
	// (target is the new name, the workflow's current identity).
	s.audit.Record(ctx, audit.ActionWorkflowRename, newName, auditDetail(map[string]string{
		"from": name,
		"to":   newName,
	}))
	return nil
}

// copyThenSwap is the shared cross-store "move a workflow folder with rollback"
// envelope used by Rename and Transfer-private. The folder store and the registry
// are separate stores with no shared transaction, so the commit-point discipline
// lives here ONCE rather than re-asserted in prose at each call site:
//
//  1. CopyTree(src → dst). On failure, return (nothing has changed).
//  2. mutate: the caller's registry/label mutation (the identity commit point,
//     plus any pre-commit folder relabel). On ANY failure, ROLL BACK the copied
//     dst folder so the two stores never diverge into a half-move.
//  3. Delete the src folder. After the identity commit the registry + dst folder
//     are authoritative, so a src-delete failure leaves only a COSMETIC stale
//     folder (Load resolves via dst) and is surfaced for the operator to reconcile.
//
// The mutation MUST perform any folder relabel BEFORE the durable registry commit
// so that a relabel failure is still inside the rollback envelope (Rename step-6).
func (s *Service) copyThenSwap(ctx context.Context, srcScope Scope, srcName string, dstScope Scope, dstName string, mutate func(context.Context) error) error {
	if err := s.rejectCopiedGroups(ctx, srcScope, srcName, dstName); err != nil {
		return err
	}
	if err := s.repo.CopyTree(ctx, srcScope, srcName, dstScope, dstName, false); err != nil {
		return err
	}
	if err := mutate(ctx); err != nil {
		_ = s.repo.Delete(ctx, dstScope, dstName) // roll back the copy: no half-move
		return err
	}
	if err := s.repo.Delete(ctx, srcScope, srcName); err != nil {
		return fmt.Errorf("removing source workflow folder %q after move: %w", srcName, err)
	}
	return nil
}

// resolveIDScope resolves the stable id AND storage scope (private-of-owner or
// shared) for name in a SINGLE registry lookup. It replaces the prior
// ResolveID-then-List-scan: one indexed Resolve removes the O(n) scan and the
// ResolveID→scopeOf TOCTOU (a concurrent rename/delete between the two reads
// previously yielded a mid-op ErrNotFound on an id that had just resolved).
// ErrNotFound propagates when the name is unknown.
func (s *Service) resolveIDScope(ctx context.Context, name string) (uuid.UUID, Scope, error) {
	e, err := s.registry.Resolve(ctx, name)
	if err != nil {
		return uuid.Nil, Scope{}, err // ErrNotFound propagates
	}
	return e.ID, entryScope(e), nil
}

// entryScope maps a registry entry to its storage scope.
func entryScope(e RegistryEntry) Scope {
	if e.Scope == ScopeShared {
		return Shared()
	}
	return Private(e.OwnerID)
}
