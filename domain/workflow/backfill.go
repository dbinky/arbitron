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
// Repository can enumerate) and, for every workflow that is not yet
// registered, allocates+persists a stable id and inserts a registry row.
//
// It is idempotent: a workflow that already has an id (or already has a registry
// row for its name) is left untouched, so running it repeatedly produces no
// duplicate rows. Private workflows of other users are not visible to this scan
// and are reconciled lazily on their first Load (see Load → ensureID).
//
// A nil registry makes this a no-op (local/unconfigured deployments).
// Invalid group definitions are left untouched; their errors are collected
// across both scopes so one bad definition cannot prevent valid neighbors from
// being reconciled. Infrastructure failures still abort immediately.
func (s *Service) BackfillRegistry(ctx context.Context) error {
	if s.localMode() {
		return nil
	}
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	var rejected []error
	for _, scope := range ReadScopes(id.UserID) {
		if err := s.backfillScope(ctx, scope); err != nil {
			if errors.Is(err, ErrGroupInvalid) {
				rejected = append(rejected, err)
				continue
			}
			return err
		}
	}
	return errors.Join(rejected...)
}

// backfillScope registers every unregistered workflow in a single scope.
func (s *Service) backfillScope(ctx context.Context, scope Scope) error {
	summaries, err := s.repo.List(ctx, scope)
	if err != nil {
		return fmt.Errorf("backfill: listing %s scope: %w", scopeName(scope), err)
	}
	var rejected []error
	for _, sum := range summaries {
		// Validate a stored group before the lock: a workflows-form group reads
		// its children's input contracts, which a held lock set cannot widen to.
		if wf, err := s.repo.Load(ctx, scope, sum.Name); err != nil {
			return fmt.Errorf("backfill: loading %q: %w", sum.Name, err)
		} else if err := s.rejectStoredGroupImport(ctx, wf, scope, sum.Name, sum.Name); err != nil {
			validationErr := fmt.Errorf("backfill: validating %q: %w", sum.Name, err)
			if errors.Is(err, ErrGroupInvalid) {
				rejected = append(rejected, validationErr)
				continue
			}
			return validationErr
		}
		target := MutationTarget{Scope: scope, Name: sum.Name}
		if err := s.withMutations(ctx, []MutationTarget{target}, func(ctx context.Context) error {
			if err := s.settle(ctx, target); err != nil {
				return err
			}
			return s.backfillOne(ctx, scope, sum.Name)
		}); err != nil {
			return err
		}
	}
	return errors.Join(rejected...)
}

// Read/modify/write all happen under the same authoring lock, preserving a newer
// Presentation even if the catalog listing preceded an authoring save.
func (s *Service) backfillOne(ctx context.Context, scope Scope, name string) error {
	if _, err := s.registry.ResolveID(ctx, name); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("backfill: resolving %q: %w", name, err)
	}
	wf, err := s.repo.Load(ctx, scope, name)
	if err != nil {
		return fmt.Errorf("backfill: loading %q: %w", name, err)
	}
	if wf.ID == uuid.Nil {
		wf.ID = uuid.New()
	}
	if err := s.registry.Register(ctx, RegistryEntry{ID: wf.ID, Name: name, Scope: scope.Kind, OwnerID: scope.OwnerID}); err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("backfill: registering %q: %w", name, err)
	}
	if err := s.repo.Update(ctx, scope, name, wf); err != nil {
		return fmt.Errorf("backfill: persisting id for %q: %w", name, err)
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
