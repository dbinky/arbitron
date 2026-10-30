package samples

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// SetRevisionStamper enables captured revisions for newly seeded samples.
func (s *Seeder) SetRevisionStamper(st *workflow.RevisionStamper) { s.stamper = st }

// SetMutationCoordinator installs the operation-wide destination lock.
func (s *Seeder) SetMutationCoordinator(m workflow.MutationCoordinator) { s.mutations = m }

// SetRegistry assigns final shared identities before the first visibility write.
func (s *Seeder) SetRegistry(reg workflow.Registry) { s.registry = reg }

const seedCleanupTimeout = 30 * time.Second

func (s *Seeder) seedRevision(ctx context.Context, name string) error {
	trees, ok := s.repo.(workflow.TreeCommitter)
	if !ok || s.mutations == nil {
		return workflow.ErrMutationMisconfigured
	}
	publications, ok := s.published.(workflow.RevisionPublicationStore)
	if !ok {
		return workflow.ErrMutationMisconfigured
	}
	// Group samples are validated against the live profiles, exactly as on the
	// legacy path; an invalid one is skipped and retried after a fix. This runs
	// BEFORE the mutation lock: a workflows-form group reads its children's input
	// contracts, and a held lock set cannot widen to them. Retain that exact
	// manifest and supporting input; a second read could import an unchecked edit.
	wf, files, readErr := s.source.Read(ctx, name)
	if readErr == nil && wf != nil {
		validationCtx := identity.WithIdentity(ctx, identity.Identity{UserID: s.owner, Role: identity.RoleAdmin})
		if err := s.validator.ValidateGroupImport(validationCtx, wf, newSeedFiles(files, s.libFiles, s.owner)); err != nil {
			if errors.Is(err, workflow.ErrGroupInvalid) {
				s.logger.Warn("skipping invalid group sample", "name", name, "err", err)
				return nil
			}
			return err
		}
	}
	target := workflow.MutationTarget{Scope: workflow.Shared(), Name: name}
	return s.mutations.WithMutations(ctx, []workflow.MutationTarget{target}, func(ctx context.Context) error {
		seeded, err := s.ledger.Seeded(ctx)
		if err != nil {
			return fmt.Errorf("reading seed ledger: %w", err)
		}
		if seeded[name] {
			return nil
		}

		// EnsureStamped settles interrupted prepares before inspecting the target.
		// Only an exact committed input can resume (from the journal or durable
		// enqueue acknowledgement); folder presence never authorises adoption.
		req, resumeErr := s.stamper.EnsureStamped(ctx, target.Scope, name, workflow.SaveReasonSeed)
		if resumeErr == nil {
			pub, present, err := publications.PublicationOf(ctx, name)
			if err != nil {
				return err
			}
			if !present || pub.Publisher != s.owner || pub.SourceName != name || pub.Revision != req.Revision || pub.WorkflowID != req.WorkflowID {
				return workflow.ErrMutationPending
			}
			return s.recordSeed(ctx, name, req)
		}
		if !errors.Is(resumeErr, workflow.ErrNotFound) {
			return resumeErr
		}
		if _, present, err := publications.PublicationOf(ctx, name); err != nil {
			return err
		} else if present {
			return fmt.Errorf("%w: sample publication %s", workflow.ErrAlreadyExists, name)
		}

		if readErr != nil {
			if !errors.Is(readErr, ErrMalformedSample) {
				return fmt.Errorf("reading sample %q: %w", name, readErr)
			}
			s.logger.Warn("skipping malformed sample", "name", name, "err", readErr)
			return nil
		}
		if wf == nil {
			return fmt.Errorf("sample %q has no manifest", name)
		}
		// Source identity and presentation are untrusted at this new destination.
		wf.ID, wf.Name = uuid.New(), name
		claimed := false
		if s.registry != nil {
			if _, err := s.registry.Resolve(ctx, name); err == nil {
				return workflow.ErrAlreadyExists
			} else if !errors.Is(err, workflow.ErrNotFound) {
				return err
			}
		}
		var tree *workflow.TreeCommit
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
		err = workflow.WriteAndRecord(ctx, publications, workflow.PublishRequest{
			PublishedName: name, Publisher: s.owner, SourceName: name, Visibility: workflow.VisibilityPublic,
			WriteTree: func(ctx context.Context, overwrite bool) error {
				if overwrite {
					return workflow.ErrAlreadyExists
				}
				var err error
				req, err = s.stamper.Stamp(ctx, target.Scope, name, wf, nil, workflow.SaveReasonSeed)
				if err != nil {
					return err
				}
				// The durable input must precede the claim, so a crash immediately
				// after Register still leaves ownership evidence for compensation.
				if s.registry != nil {
					if err := s.registry.Register(ctx, workflow.RegistryEntry{ID: wf.ID, Name: name, Scope: workflow.ScopeShared}); err != nil {
						return err
					}
					claimed = true
				}
				prepared, err := trees.PrepareTreeCommit(ctx, target, req.Revision)
				if err != nil {
					return err
				}
				tree = &prepared
				if err := s.repo.Create(ctx, target.Scope, name, wf); err != nil {
					return err
				}
				paths := make([]string, 0, len(files))
				for path := range files {
					paths = append(paths, path)
				}
				sort.Strings(paths)
				for _, path := range paths {
					if err := s.repo.WriteFile(ctx, target.Scope, name, path, files[path]); err != nil {
						return fmt.Errorf("writing sample %q/%q: %w", name, path, err)
					}
				}
				return nil
			},
			PreparedPublication: func() (workflow.Publication, error) {
				return workflow.Publication{Publisher: s.owner, SourceName: name, Visibility: workflow.VisibilityPublic, Revision: req.Revision, WorkflowID: wf.ID}, nil
			},
			Rollback: rollback,
		})
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seedCleanupTimeout)
			defer cancel()
			if !rollbackAttempted {
				err = errors.Join(err, rollback(cleanupCtx))
			}
			if rolledBack {
				if claimed {
					if undoErr := s.registry.Delete(cleanupCtx, wf.ID); undoErr != nil {
						return errors.Join(err, undoErr)
					}
				}
				s.stamper.Abandon(cleanupCtx, req)
			}
			return err
		}
		return s.recordSeed(ctx, name, req)
	})
}

func (s *Seeder) recordSeed(ctx context.Context, name string, req workflow.ArrangementRequest) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seedCleanupTimeout)
	defer cancel()
	s.stamper.Committed(ctx, req)
	if err := s.ledger.Record(ctx, name); err != nil {
		return fmt.Errorf("recording seeded %q: %w", name, err)
	}
	return nil
}
