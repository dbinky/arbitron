package workflow

import (
	"context"
	"github.com/google/uuid"
)

func (s *Service) withSupportingWrite(ctx context.Context, name string, fn func(context.Context) error) error {
	if s.stamper == nil && s.mutations == nil {
		return fn(ctx)
	}
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if _, err := s.updateTarget(ctx, id, name); err != nil {
		return err
	}
	targets := []MutationTarget{{Scope: Private(id.UserID), Name: name}, {Scope: Shared(), Name: name}}
	return s.withMutations(ctx, targets, func(ctx context.Context) error {
		id, err := caller(ctx)
		if err != nil {
			return err
		}
		target, err := s.updateTarget(ctx, id, name)
		if err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		return fn(ctx)
	})
}

func (s *Service) lockedDelete(ctx context.Context, name string, unpublish bool) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	target := MutationTarget{Scope: Shared(), Name: name}
	if !unpublish {
		_, scope, err := s.resolveIDScope(ctx, name)
		if err != nil {
			return err
		}
		target.Scope = scope
	}
	return s.withMutations(ctx, []MutationTarget{target}, func(ctx context.Context) error {
		if err := s.requireAdmin(ctx); err != nil {
			return err
		}
		var priorID uuid.UUID
		if !unpublish || !s.localMode() {
			id, scope, err := s.resolveIDScope(ctx, name)
			if err != nil {
				return err
			}
			if scope != target.Scope {
				return ErrMutationPending
			}
			priorID = id
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		if !unpublish || !s.localMode() {
			id, scope, err := s.resolveIDScope(ctx, name)
			if err != nil {
				return err
			}
			if scope != target.Scope || !s.localMode() && id != priorID {
				return ErrMutationPending
			}
		}
		if unpublish {
			return s.unpublishWorkflow(ctx, name)
		}
		return s.deleteWorkflow(ctx, name)
	})
}
