package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
)

func (s *Service) publishRevision(ctx context.Context, privateName, name string, visibility Visibility, department string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	publications, ok := s.published.(RevisionPublicationStore)
	if !ok {
		return ErrMutationMisconfigured
	}
	trees, ok := s.repo.(TreeCommitter)
	if !ok {
		return ErrMutationMisconfigured
	}
	source := MutationTarget{Scope: Private(id.UserID), Name: privateName}
	target := MutationTarget{Scope: Shared(), Name: name}
	// Validate a stored group before the lock: child contracts cannot be read
	// under a held lock set (see precheckCopiedGroups).
	checked, err := s.precheckCopiedGroupImport(ctx, source.Scope, privateName, name)
	if err != nil {
		return err
	}
	return s.withMutations(ctx, []MutationTarget{source, target}, func(ctx context.Context) error {
		id, err := caller(ctx)
		if err != nil {
			return err
		}
		if err := s.settle(ctx, source); err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		priorPub, overwrite, err := publications.PublicationOf(ctx, name)
		if err != nil {
			return err
		}
		ownerDept := ""
		if overwrite {
			if priorPub.Publisher != id.UserID {
				return fmt.Errorf("%w: published name %q owned by another user", ErrUnauthorized, name)
			}
			ownerDept = priorPub.Department
		}
		if err := authorizePublish(id, visibility, department, ownerDept); err != nil {
			return err
		}
		if err := s.checkSourceScope(ctx, source); err != nil {
			return err
		}
		sourceWF, err := s.repo.Load(ctx, source.Scope, privateName)
		if err != nil {
			return err
		}
		if err := checked.check(source.Scope, sourceWF); err != nil {
			return err
		}
		prior := sourceWF
		if overwrite {
			prior, err = s.repo.Load(ctx, target.Scope, name)
			if err != nil {
				return err
			}
		}
		finalID := uuid.New()
		claimed := false
		if !s.localMode() {
			entry, err := s.registry.Resolve(ctx, name)
			if err == nil {
				if !overwrite || entry.Scope != ScopeShared {
					return fmt.Errorf("%w: %s", ErrAlreadyExists, name)
				}
				finalID = entry.ID
			} else if errors.Is(err, ErrNotFound) {
				if err := s.registry.Register(ctx, RegistryEntry{ID: finalID, Name: name, Scope: ScopeShared}); err != nil {
					return err
				}
				claimed = true
			} else {
				return err
			}
		} else if overwrite && prior.ID != uuid.Nil {
			finalID = prior.ID
		}
		var req ArrangementRequest
		var tree *TreeCommit
		rollbackAttempted, rolledBack := false, false
		rollback := func(ctx context.Context) error {
			rollbackAttempted = true
			if tree != nil {
				if err := trees.RollbackTreeCommit(ctx, tree.ID); err != nil {
					return err
				}
			}
			rolledBack = true
			return nil
		}
		err = WriteAndRecord(ctx, publications, PublishRequest{
			PublishedName: name, Publisher: id.UserID, SourceName: privateName, Visibility: visibility, Department: department,
			WriteTree: func(ctx context.Context, replace bool) error {
				return s.repo.CopyTreeStamped(ctx, source.Scope, privateName, target.Scope, name, replace, func(staged *Workflow) error {
					staged.ID, staged.Name = finalID, name
					var err error
					req, err = s.stamper.Stamp(ctx, target.Scope, name, staged, prior, SaveReasonPublish)
					if err != nil {
						return err
					}
					prepared, err := trees.PrepareTreeCommit(ctx, target, req.Revision)
					if err != nil {
						return err
					}
					tree = &prepared
					return nil
				})
			},
			PreparedPublication: func() (Publication, error) {
				if req.Revision == "" {
					return Publication{}, ErrMutationPending
				}
				return Publication{Publisher: id.UserID, SourceName: privateName, Visibility: visibility, Department: department, Revision: req.Revision, WorkflowID: finalID}, nil
			},
			Rollback: rollback,
		})
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
			defer cancel()
			if !rollbackAttempted {
				err = errors.Join(err, rollback(cleanupCtx))
			}
			if rolledBack {
				if claimed {
					if rollbackErr := s.registry.Delete(cleanupCtx, finalID); rollbackErr != nil {
						return errors.Join(err, rollbackErr)
					}
				}
				s.stamper.Abandon(cleanupCtx, req)
			}
			return err
		}
		s.committed(ctx, req, tree)
		s.audit.Record(ctx, audit.ActionWorkflowPublish, name, auditDetail(map[string]string{"to": string(visibility), "department": department}))
		return nil
	})
}

// A leftover source tree after an owner move is not authority to publish/copy
// it. Unregistered legacy and ordinary user copies remain name-addressed.
func (s *Service) checkSourceScope(ctx context.Context, source MutationTarget) error {
	if s.localMode() {
		return nil
	}
	entry, err := s.registry.Resolve(ctx, source.Name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if entryScope(entry) != source.Scope {
		return fmt.Errorf("%w: %s", ErrNotFound, source.Name)
	}
	return nil
}

func (s *Service) copyRevision(ctx context.Context, sourceName, name string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	source := MutationTarget{Scope: Shared(), Name: sourceName}
	target := MutationTarget{Scope: Private(id.UserID), Name: name}
	// Visibility first (no existence leak through a validation error), then the
	// pre-lock group check; both are re-checked or settled under the lock.
	if s.publishers != nil {
		pub, ok, err := s.publishers.PublicationOf(ctx, sourceName)
		if err != nil {
			return err
		}
		if ok && !CanSeePublication(id, pub) {
			return fmt.Errorf("%w: %s", ErrNotFound, sourceName)
		}
	}
	checked, err := s.precheckCopiedGroupImport(ctx, source.Scope, sourceName, name)
	if err != nil {
		return err
	}
	return s.withMutations(ctx, []MutationTarget{source, target}, func(ctx context.Context) error {
		id, err := caller(ctx)
		if err != nil {
			return err
		}
		if s.publishers != nil {
			pub, ok, err := s.publishers.PublicationOf(ctx, sourceName)
			if err != nil {
				return err
			}
			if ok && !CanSeePublication(id, pub) {
				return fmt.Errorf("%w: %s", ErrNotFound, sourceName)
			}
		}
		if err := s.settle(ctx, source); err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		if err := s.checkSourceScope(ctx, source); err != nil {
			return err
		}
		prior, err := s.repo.Load(ctx, source.Scope, sourceName)
		if err != nil {
			return err
		}
		if err := checked.check(source.Scope, prior); err != nil {
			return err
		}
		var req ArrangementRequest
		err = s.repo.CopyTreeStamped(ctx, source.Scope, sourceName, target.Scope, name, false, func(staged *Workflow) error {
			staged.ID, staged.Name = uuid.New(), name
			req, err = s.stamper.Stamp(ctx, target.Scope, name, staged, prior, SaveReasonCopy)
			return err
		})
		if err != nil {
			s.abandonUnwritten(ctx, req)
			return err
		}
		s.committed(ctx, req, nil)
		return nil
	})
}
