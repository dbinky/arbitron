package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// BackfillRegistry is the one-time startup reconciliation between the name-keyed
// folder store and the identity registry. It scans the caller's visible scopes
// (the caller's private space and the shared space — the scopes the
// WorkflowRepository can enumerate) and, for every workflow that is not yet
// registered, allocates+persists a stable id and inserts a registry row.
//
// It is idempotent: a workflow that already has an id (or already has a registry
// row for its name) is left untouched, so running it repeatedly produces no
// duplicate rows. Private workflows of other users are not visible to this scan
// and are reconciled lazily on their first Load (see Load → ensureID).
//
// A nil registry makes this a no-op (local/unconfigured deployments).
func (s *WorkflowService) BackfillRegistry(ctx context.Context) error {
	if s.registry == nil {
		return nil
	}
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	for _, scope := range ReadScopes(id.UserID) {
		if err := s.backfillScope(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}

// backfillScope registers every unregistered workflow in a single scope.
func (s *WorkflowService) backfillScope(ctx context.Context, scope Scope) error {
	summaries, err := s.repo.List(ctx, scope)
	if err != nil {
		return fmt.Errorf("backfill: listing %s scope: %w", scopeName(scope), err)
	}
	for _, sum := range summaries {
		// If a registry row already exists for this name, it is already reconciled.
		if _, err := s.registry.ResolveID(ctx, sum.Name); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("backfill: resolving %q: %w", sum.Name, err)
		}

		wf, err := s.repo.Load(ctx, scope, sum.Name)
		if err != nil {
			return fmt.Errorf("backfill: loading %q: %w", sum.Name, err)
		}
		if wf.ID == uuid.Nil {
			wf.ID = uuid.New()
		}
		if err := s.registry.Register(ctx, RegistryEntry{
			ID:      wf.ID,
			Name:    sum.Name,
			Scope:   scope.Kind,
			OwnerID: scope.OwnerID,
		}); err != nil {
			// A concurrent backfill/load may have just claimed the name; that is the
			// reconciled state, so treat it as done rather than an error.
			if errors.Is(err, ErrAlreadyExists) {
				continue
			}
			return fmt.Errorf("backfill: registering %q: %w", sum.Name, err)
		}
		// Persist the (possibly newly allocated) id back into the folder so the
		// definition body and the registry agree and the id is stable.
		if err := s.repo.Update(ctx, scope, sum.Name, wf); err != nil {
			return fmt.Errorf("backfill: persisting id for %q: %w", sum.Name, err)
		}
	}
	return nil
}

// scopeName labels a scope for diagnostics.
func scopeName(s Scope) string {
	if s.Kind == ScopeShared {
		return ScopeLabelShared
	}
	return ScopeLabelPrivate
}
