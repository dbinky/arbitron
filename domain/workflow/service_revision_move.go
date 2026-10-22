package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
)

// withIdentityMutation re-resolves registry authority after acquisition. If a
// concurrent owner move changed the source scope, no unlocked tree is touched.
func (s *Service) withIdentityMutation(ctx context.Context, name string, destination func(Scope) MutationTarget, fn func(context.Context, uuid.UUID, Scope, MutationTarget) error) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	if s.localMode() {
		return fmt.Errorf("%w: workflow identity mutations are not configured", ErrUnauthorized)
	}
	id, scope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return err
	}
	target := destination(scope)
	return s.withMutations(ctx, []MutationTarget{{Scope: scope, Name: name}, target}, func(ctx context.Context) error {
		if err := s.requireAdmin(ctx); err != nil {
			return err
		}
		currentID, currentScope, err := s.resolveIDScope(ctx, name)
		if err != nil {
			return err
		}
		if currentID != id || currentScope != scope {
			return ErrMutationPending
		}
		if err := s.settle(ctx, MutationTarget{Scope: scope, Name: name}); err != nil {
			return err
		}
		if target.Scope != scope || target.Name != name {
			if err := s.settle(ctx, target); err != nil {
				return err
			}
		}
		// Settlement can finish an interrupted relocation, so recheck identity too.
		currentID, currentScope, err = s.resolveIDScope(ctx, name)
		if err != nil {
			return err
		}
		if currentID != id || currentScope != scope {
			return ErrMutationPending
		}
		return fn(ctx, id, scope, target)
	})
}
func (s *Service) freeDestination(ctx context.Context, target MutationTarget) error {
	if _, err := s.registry.ResolveID(ctx, target.Name); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, target.Name)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if exists, err := s.repo.Exists(ctx, target.Scope, target.Name); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, target.Name)
	}
	return nil
}
func (s *Service) duplicateRevision(ctx context.Context, name, newName string) error {
	callerID, err := caller(ctx)
	if err != nil {
		return err
	}
	checked, err := s.precheckCopiedGroups(ctx, name, newName)
	if err != nil {
		return err
	}
	return s.withIdentityMutation(ctx, name, func(Scope) MutationTarget { return MutationTarget{Scope: Private(callerID.UserID), Name: newName} }, func(ctx context.Context, _ uuid.UUID, scope Scope, target MutationTarget) error {
		if err := s.freeDestination(ctx, target); err != nil {
			return err
		}
		prior, err := s.repo.Load(ctx, scope, name)
		if err != nil {
			return err
		}
		if err := checked.check(scope, prior); err != nil {
			return err
		}
		newID := uuid.New()
		if err := s.registry.Register(ctx, RegistryEntry{ID: newID, Name: newName, Scope: ScopePrivate, OwnerID: target.Scope.OwnerID}); err != nil {
			return err
		}
		var req ArrangementRequest
		err = s.repo.CopyTreeStamped(ctx, scope, name, target.Scope, newName, false, func(staged *Workflow) error {
			staged.ID, staged.Name = newID, newName
			var err error
			req, err = s.stamper.Stamp(ctx, target.Scope, newName, staged, prior, SaveReasonDuplicate)
			return err
		})
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
			defer cancel()
			if s.isUnwritten(cleanupCtx, req) {
				if rollbackErr := s.registry.Delete(cleanupCtx, newID); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				s.stamper.Abandon(cleanupCtx, req)
			}
			return err
		}
		s.committed(ctx, req, nil)
		s.audit.Record(ctx, audit.ActionWorkflowDuplicate, newName, auditDetail(map[string]string{"duplicatedFrom": name}))
		return nil
	})
}

func (s *Service) renameRevision(ctx context.Context, name, newName string) error {
	checked, err := s.precheckCopiedGroups(ctx, name, newName)
	if err != nil {
		return err
	}
	return s.withIdentityMutation(ctx, name, func(scope Scope) MutationTarget { return MutationTarget{Scope: scope, Name: newName} }, func(ctx context.Context, id uuid.UUID, scope Scope, target MutationTarget) error {
		if err := s.freeDestination(ctx, target); err != nil {
			return err
		}
		prior, err := s.repo.Load(ctx, scope, name)
		if err != nil {
			return err
		}
		if err := checked.check(scope, prior); err != nil {
			return err
		}
		var pubs RevisionPublicationStore
		var trees TreeCommitter
		var publication Publication
		if scope.Kind == ScopeShared {
			var ok bool
			pubs, ok = s.published.(RevisionPublicationStore)
			if !ok {
				return ErrMutationMisconfigured
			}
			trees, ok = s.repo.(TreeCommitter)
			if !ok {
				return ErrMutationMisconfigured
			}
			publication, ok, err = pubs.PublicationOf(ctx, name)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: %s", ErrNotFound, name)
			}
		}
		intent, err := s.stamper.deps.Journal.BeginRelocation(ctx, scope, name, target.Scope, newName, id)
		if err != nil {
			return err
		}
		var req ArrangementRequest
		var tree *TreeCommit
		// Stamp settles the destination, which this operation's own intent covers.
		stampCtx := withOwnRelocation(ctx, intent.ID)
		err = s.repo.CopyTreeStamped(ctx, scope, name, target.Scope, newName, false, func(staged *Workflow) error {
			staged.ID, staged.Name = id, newName
			var err error
			req, err = s.stamper.Stamp(stampCtx, target.Scope, newName, staged, prior, SaveReasonRename)
			if err != nil {
				return err
			}
			if trees != nil {
				prepared, err := trees.PrepareTreeCommit(ctx, target, req.Revision)
				if err != nil {
					return err
				}
				tree = &prepared
			}
			return nil
		})
		if err == nil {
			if pubs != nil {
				publication.Revision, publication.WorkflowID = req.Revision, id
				err = pubs.RenamePublication(ctx, id, name, newName, publication)
			} else {
				err = s.registry.Rename(ctx, id, newName)
			}
		}
		if err != nil {
			return s.rollbackMove(ctx, err, intent, req, tree)
		}
		// Identity committed: neither cleanup nor relocation can abandon this save.
		s.committed(ctx, req, tree)
		if err := s.finishRelocation(ctx, intent); err != nil {
			return err
		}
		s.audit.Record(ctx, audit.ActionWorkflowRename, newName, auditDetail(map[string]string{"from": name, "to": newName}))
		return nil
	})
}

func (s *Service) rollbackMove(ctx context.Context, cause error, intent RelocationIntent, req ArrangementRequest, tree *TreeCommit) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
	defer cancel()
	if tree != nil {
		if err := s.repo.(TreeCommitter).RollbackTreeCommit(ctx, tree.ID); err != nil {
			return errors.Join(cause, err)
		}
	} else if err := s.repo.Delete(ctx, intent.To, intent.ToName); err != nil && !errors.Is(err, ErrNotFound) {
		return errors.Join(cause, err)
	}
	if err := s.stamper.deps.Journal.EndRelocation(ctx, intent.ID); err != nil {
		return errors.Join(cause, err)
	}
	s.stamper.Abandon(ctx, req)
	return cause
}

func (s *Service) finishRelocation(ctx context.Context, intent RelocationIntent) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
	defer cancel()
	if err := s.stamper.deps.Journal.Relocate(ctx, intent.From, intent.FromName, intent.To, intent.ToName); err != nil {
		s.stamper.deps.Logger.Error("relocate committed workflow journal", "intent", intent.ID, "error", err)
		return nil
	}
	if err := s.stamper.deps.Requester.RelocateArrangements(ctx, intent.From, intent.FromName, intent.To, intent.ToName); err != nil {
		s.stamper.deps.Logger.Error("relocate committed workflow arrangements", "intent", intent.ID, "error", err)
		return nil
	}
	if err := s.repo.Delete(ctx, intent.From, intent.FromName); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("removing source workflow folder after identity commit: %w", err)
	}
	if err := s.stamper.deps.Journal.EndRelocation(ctx, intent.ID); err != nil {
		s.stamper.deps.Logger.Error("finish committed workflow relocation", "intent", intent.ID, "error", err)
	}
	return nil
}

func (s *Service) transferRevision(ctx context.Context, name string, newOwner uuid.UUID) error {
	if newOwner == uuid.Nil {
		return fmt.Errorf("workflow: owner is required")
	}
	return s.withIdentityMutation(ctx, name, func(scope Scope) MutationTarget {
		if scope.Kind == ScopeShared {
			return MutationTarget{Scope: scope, Name: name}
		}
		return MutationTarget{Scope: Private(newOwner), Name: name}
	}, func(ctx context.Context, id uuid.UUID, scope Scope, target MutationTarget) error {
		if scope.Kind == ScopeShared {
			if err := s.registry.SetOwner(ctx, id, newOwner); err != nil {
				return err
			}
			s.audit.Record(ctx, audit.ActionWorkflowTransfer, name, auditDetail(map[string]string{"to": newOwner.String(), "scope": ScopeLabelShared}))
			return nil
		}
		if scope == target.Scope {
			return nil
		}
		if exists, err := s.repo.Exists(ctx, target.Scope, name); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, name)
		}
		intent, err := s.stamper.deps.Journal.BeginRelocation(ctx, scope, name, target.Scope, name, id)
		if err != nil {
			return err
		}
		if err = s.repo.CopyTree(ctx, scope, name, target.Scope, name, false); err == nil {
			err = s.registry.SetOwner(ctx, id, newOwner)
		}
		if err != nil {
			return s.rollbackMove(ctx, err, intent, ArrangementRequest{}, nil)
		}
		if err := s.finishRelocation(ctx, intent); err != nil {
			return err
		}
		s.audit.Record(ctx, audit.ActionWorkflowTransfer, name, auditDetail(map[string]string{"from": scope.OwnerID.String(), "to": newOwner.String(), "scope": ScopeLabelPrivate}))
		return nil
	})
}

// precheckCopiedGroups validates a stored group definition before an identity
// mutation copies it, exactly as the unstamped Duplicate/Rename do. It runs
// BEFORE the mutation lock: a workflows-form group reads its children's input
// contracts, and the settled repository refuses to widen a held lock set to
// them. Admin and identity checks are re-run under the lock.
func (s *Service) precheckCopiedGroups(ctx context.Context, name, newName string) (checkedGroupImport, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return checkedGroupImport{}, err
	}
	if s.localMode() {
		return checkedGroupImport{}, nil // withIdentityMutation reports the unconfigured mode
	}
	_, scope, err := s.resolveIDScope(ctx, name)
	if err != nil {
		return checkedGroupImport{}, err
	}
	return s.precheckCopiedGroupImport(ctx, scope, name, newName)
}
