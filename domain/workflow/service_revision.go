package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ErrMutationMisconfigured refuses a partially installed save protocol.
var ErrMutationMisconfigured = errors.New("workflow: revision saves require a mutation coordinator and durable tree/publication ports for shared writes")

const postcommitTimeout = 30 * time.Second

func (s *Service) withMutations(ctx context.Context, targets []MutationTarget, fn func(context.Context) error) error {
	if s.mutations == nil {
		if s.stamper != nil {
			return ErrMutationMisconfigured
		}
		return fn(ctx)
	}
	return s.mutations.WithMutations(ctx, targets, fn)
}

func (s *Service) settle(ctx context.Context, target MutationTarget) error {
	if s.stamper == nil {
		return nil
	}
	return s.stamper.deps.Settler.Settle(ctx, target.Scope, target.Name)
}

// Postcommit work keeps the operation's lock ownership, but not caller
// cancellation. Recovery owns retries when the bounded attempt fails.
func (s *Service) committed(ctx context.Context, req ArrangementRequest, tree *TreeCommit) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
	defer cancel()
	if s.stamper.committed(ctx, req) && tree != nil {
		if err := s.repo.(TreeCommitter).FinishTreeCommit(ctx, tree.ID); err != nil {
			s.stamper.deps.Logger.Error("finish committed workflow tree", "revision", req.Revision, "error", err)
		}
	}
}

// An error from a repository is not itself proof that its rename did not happen.
// Preserve the prepare on ambiguous I/O; settlement decides it under this lock.
func (s *Service) isUnwritten(ctx context.Context, req ArrangementRequest) bool {
	if req.Revision == "" {
		return true
	}
	saved, err := s.repo.Load(ctx, req.Scope, req.Name)
	return errors.Is(err, ErrNotFound) || err == nil && saved.SavedRevision() != req.Revision
}

func (s *Service) abandonUnwritten(ctx context.Context, req ArrangementRequest) {
	if s.isUnwritten(ctx, req) {
		s.stamper.Abandon(ctx, req)
	}
}

func (s *Service) createRevision(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if wf == nil {
		return fmt.Errorf("workflow: cannot create nil workflow")
	}
	target := MutationTarget{Scope: Private(id.UserID), Name: name}
	return s.withMutations(ctx, []MutationTarget{target}, func(ctx context.Context) error {
		if _, err := caller(ctx); err != nil {
			return err
		}
		if err := validateStampDestination(target.Scope, name, SaveReasonCreate); err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		if exists, err := s.repo.Exists(ctx, target.Scope, name); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("%w: %s", ErrAlreadyExists, name)
		}
		wf.Name = name
		claimed := false
		if !s.localMode() {
			if wf.ID == uuid.Nil {
				wf.ID = uuid.New()
			}
			if err := s.registry.Register(ctx, RegistryEntry{ID: wf.ID, Name: name, Scope: ScopePrivate, OwnerID: id.UserID}); err != nil {
				return err
			}
			claimed = true
		}
		undoClaim := func(ctx context.Context) error {
			if claimed {
				return s.registry.Delete(ctx, wf.ID)
			}
			return nil
		}
		req, err := s.stamper.Stamp(ctx, target.Scope, name, wf, nil, SaveReasonCreate)
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
			defer cancel()
			return errors.Join(err, undoClaim(cleanupCtx))
		}
		if err := s.repo.Create(ctx, target.Scope, name, wf); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
			defer cancel()
			if s.isUnwritten(cleanupCtx, req) {
				if rollbackErr := undoClaim(cleanupCtx); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				s.stamper.Abandon(cleanupCtx, req)
			}
			return err
		}
		s.committed(ctx, req, nil)
		return nil
	})
}

// Resolve before locking, then again inside. Never probe authorization by first
// writing private bytes: a denied edit must not prepare or mutate anything.
func (s *Service) updateTarget(ctx context.Context, id identity.Identity, name string) (MutationTarget, error) {
	private := MutationTarget{Scope: Private(id.UserID), Name: name}
	if exists, err := s.repo.Exists(ctx, private.Scope, name); err != nil {
		return MutationTarget{}, err
	} else if exists {
		return private, nil
	}
	if exists, err := s.repo.Exists(ctx, Shared(), name); err != nil {
		return MutationTarget{}, err
	} else if !exists {
		return MutationTarget{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if !id.IsAdmin() {
		return MutationTarget{}, fmt.Errorf("%w: %s", ErrSharedReadOnly, name)
	}
	return MutationTarget{Scope: Shared(), Name: name}, nil
}

func (s *Service) updateRevision(ctx context.Context, name string, wf *Workflow) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if wf == nil {
		return fmt.Errorf("workflow: cannot update nil workflow")
	}
	target, err := s.updateTarget(ctx, id, name)
	if err != nil {
		return err
	}
	// Both possible resolutions are locked, including when a private copy appears
	// between the initial authorization and acquisition.
	targets := []MutationTarget{{Scope: Private(id.UserID), Name: name}, {Scope: Shared(), Name: name}}
	return s.withMutations(ctx, targets, func(ctx context.Context) error {
		id, err := caller(ctx)
		if err != nil {
			return err
		}
		target, err = s.updateTarget(ctx, id, name)
		if err != nil {
			return err
		}
		if err := s.settle(ctx, target); err != nil {
			return err
		}
		prior, err := s.repo.Load(ctx, target.Scope, name)
		if err != nil {
			return err
		}
		wf.Name, wf.ID = name, prior.ID
		if !s.localMode() {
			entry, err := s.registry.Resolve(ctx, name)
			if err == nil {
				if entryScope(entry) != target.Scope {
					return fmt.Errorf("%w: %s", ErrNotFound, name)
				}
				wf.ID = entry.ID
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		var trees TreeCommitter
		var publications RevisionPublicationStore
		var publication Publication
		if target.Scope.Kind == ScopeShared {
			var ok bool
			trees, ok = s.repo.(TreeCommitter)
			if !ok {
				return ErrMutationMisconfigured
			}
			publications, ok = s.published.(RevisionPublicationStore)
			if !ok {
				return ErrMutationMisconfigured
			}
			publication, ok, err = publications.PublicationOf(ctx, name)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: publication %s", ErrNotFound, name)
			}
			if !s.localMode() {
				wf.ID, err = s.registry.ResolveID(ctx, name)
				if err != nil {
					return err
				}
			}
			if wf.ID == uuid.Nil {
				return ErrMutationMisconfigured
			}
		}
		req, err := s.stamper.Stamp(ctx, target.Scope, name, wf, prior, SaveReasonUpdate)
		if err != nil {
			return err
		}
		var tree *TreeCommit
		if trees != nil {
			prepared, err := trees.PrepareTreeCommit(ctx, target, req.Revision)
			if err != nil {
				s.stamper.Abandon(ctx, req)
				return err
			}
			tree = &prepared
		}
		rollback := func(cause error) error {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postcommitTimeout)
			defer cancel()
			if tree != nil {
				if err := trees.RollbackTreeCommit(ctx, tree.ID); err != nil {
					return errors.Join(cause, err)
				}
				s.stamper.Abandon(ctx, req)
			} else {
				s.abandonUnwritten(ctx, req)
			}
			return cause
		}
		if err := s.repo.Update(ctx, target.Scope, name, wf); err != nil {
			return rollback(err)
		}
		if publications != nil {
			publication.Revision, publication.WorkflowID = req.Revision, req.WorkflowID
			if err := publications.CommitPublication(ctx, name, publication); err != nil {
				return rollback(err)
			}
		}
		s.committed(ctx, req, tree)
		if target.Scope.Kind == ScopeShared {
			s.audit.Record(ctx, audit.ActionWorkflowEditShared, name, auditDetail(map[string]string{"scope": ScopeLabelShared}))
		}
		return nil
	})
}
