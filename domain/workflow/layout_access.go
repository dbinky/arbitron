package workflow

import (
	"context"
	"errors"
	"fmt"
)

// LoadForLayout uses Load's visibility policy without allocating a legacy ID.
func (s *Service) LoadForLayout(ctx context.Context, name string) (*Workflow, Scope, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, Scope{}, err
	}
	return LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.auditingFilter(id))
}

// AuthorizeLayoutRetry mirrors Update's own-private/admin-shared policy without
// writing the workflow or allocating an identity. Like every authoring operation
// it settles each candidate under the retry lock set before loading it: a caller
// that already holds those locks gets no settlement from the repository's Load,
// so a peer killed mid-commit would otherwise expose its uncommitted token.
func (s *Service) AuthorizeLayoutRetry(ctx context.Context, name string) (*Workflow, Scope, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, Scope{}, err
	}
	private := MutationTarget{Scope: Private(id.UserID), Name: name}
	shared := MutationTarget{Scope: Shared(), Name: name}
	var wf *Workflow
	var scope Scope
	err = s.withMutations(ctx, []MutationTarget{private, shared}, func(ctx context.Context) error {
		if err := s.settle(ctx, private); err != nil {
			return err
		}
		loaded, err := s.repo.Load(ctx, private.Scope, name)
		if err == nil {
			wf, scope = loaded, private.Scope
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if settleErr := s.settle(ctx, shared); settleErr != nil {
			return settleErr
		}
		exists, existsErr := s.repo.Exists(ctx, shared.Scope, name)
		if existsErr != nil || !exists {
			return err
		}
		if !id.IsAdmin() {
			return fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
		}
		if wf, err = s.repo.Load(ctx, shared.Scope, name); err != nil {
			return err
		}
		scope = shared.Scope
		return nil
	})
	if err != nil {
		return nil, Scope{}, err
	}
	return wf, scope, nil
}

// AuthorizeRetryDestination applies AuthorizeLayoutRetry's shared-scope rule to
// a destination resolved some other way, such as a run snapshot: owning a run
// grants no right over the shared workflow's revision row, so only an admin may
// re-queue a shared destination.
func AuthorizeRetryDestination(ctx context.Context, t MutationTarget) error {
	if t.Scope.Kind != ScopeShared {
		return nil
	}
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if !id.IsAdmin() {
		return fmt.Errorf("%w: %s", ErrSharedReadOnly, t.Name)
	}
	return nil
}

// LayoutRetryTargets lists the private-first candidates AuthorizeLayoutRetry may
// load, so a caller can lock both before authorizing again: the lock set held by
// a MutationCoordinator cannot grow, and the shared fallback needs the private
// load to happen under the same lock.
func (s *Service) LayoutRetryTargets(ctx context.Context, name string) ([]MutationTarget, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	return []MutationTarget{{Scope: Private(id.UserID), Name: name}, {Scope: Shared(), Name: name}}, nil
}
