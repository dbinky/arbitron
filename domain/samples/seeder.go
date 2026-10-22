package samples

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Seeder writes not-yet-seeded sample workflows from a SampleSource into the
// shared scope and records each as a public publication owned by a bootstrap
// user. Each sample is seeded at most once (tracked via the Ledger).
type Seeder struct {
	source    SampleSource
	repo      workflow.Repository
	published workflow.PublishedStore
	ledger    Ledger
	owner     uuid.UUID
	logger    *slog.Logger
	stamper   *workflow.RevisionStamper
	mutations workflow.MutationCoordinator
	registry  workflow.Registry
	validator *workflow.Service
}

// NewSeeder constructs a Seeder. owner owns the seeded publications. Group
// samples require the live profile checker; without it groups are skipped,
// while legacy samples keep their existing behavior.
func NewSeeder(source SampleSource, repo workflow.Repository, published workflow.PublishedStore, ledger Ledger, owner uuid.UUID, logger *slog.Logger, profiles ...workflow.ProfileChecker) *Seeder {
	validator := workflow.NewWorkflowService(repo, published)
	var checker workflow.ProfileChecker
	if len(profiles) > 0 {
		checker = profiles[0]
	}
	validator.SetGroupCheckers(checker, nil) // incoming files are supplied explicitly
	return &Seeder{source: source, repo: repo, published: published, ledger: ledger, owner: owner, logger: logger, validator: validator}
}

// Seed seeds every not-yet-seeded sample. A malformed sample is skipped + logged
// and NOT recorded (so a fixed source retries later); it does not abort the
// rest. The first infrastructure error aborts and is returned.
func (s *Seeder) Seed(ctx context.Context) error {
	names, err := s.source.List(ctx)
	if err != nil {
		return fmt.Errorf("listing samples: %w", err)
	}
	seeded, err := s.ledger.Seeded(ctx)
	if err != nil {
		return fmt.Errorf("reading seed ledger: %w", err)
	}
	for _, name := range names {
		if seeded[name] {
			continue
		}
		if s.stamper != nil {
			if err := s.seedRevision(ctx, name); err != nil {
				return err
			}
			continue
		}
		ok, err := s.seedOne(ctx, name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := s.ledger.Record(ctx, name); err != nil {
			return fmt.Errorf("recording seeded %q: %w", name, err)
		}
	}
	return nil
}

// seedOne returns (true, nil) on success, (false, nil) when the sample was
// skipped as malformed, and (false, err) on an infrastructure failure.
//
// Legacy isolated callers can create fresh samples only. Resuming requires
// the stamped protocol's exact publication and journal proof.
func (s *Seeder) seedOne(ctx context.Context, name string) (bool, error) {
	wf, files, err := s.source.Read(ctx, name)
	if err != nil {
		if !errors.Is(err, ErrMalformedSample) {
			return false, fmt.Errorf("reading sample %q: %w", name, err)
		}
		s.logger.Warn("skipping malformed sample", "name", name, "err", err)
		return false, nil
	}
	validationCtx := identity.WithIdentity(ctx, identity.Identity{UserID: s.owner, Role: identity.RoleAdmin})
	if err := s.validator.ValidateGroupImport(validationCtx, wf, seedFiles(files)); err != nil {
		if errors.Is(err, workflow.ErrGroupInvalid) {
			s.logger.Warn("skipping invalid group sample", "name", name, "err", err)
			return false, nil
		}
		return false, err
	}
	shared := workflow.Shared()
	err = workflow.WriteAndRecord(ctx, s.published, workflow.PublishRequest{
		PublishedName: name,
		Publisher:     s.owner,
		SourceName:    name,
		Visibility:    workflow.VisibilityPublic,
		Department:    "",
		WriteTree: func(ctx context.Context, overwrite bool) error {
			return s.writeTree(ctx, shared, name, wf, files, overwrite)
		},
		Rollback: func(ctx context.Context) error {
			return s.repo.Delete(ctx, shared, name)
		},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// writeTree never adopts an existing tree without durable seed provenance.
func (s *Seeder) writeTree(ctx context.Context, shared workflow.Scope, name string, wf *workflow.Workflow, files map[string][]byte, overwrite bool) error {
	if overwrite {
		return fmt.Errorf("%w: shared sample %q requires commit proof", workflow.ErrAlreadyExists, name)
	}
	if err := s.repo.Create(ctx, shared, name, wf); err != nil {
		return fmt.Errorf("creating shared sample %q: %w", name, err)
	}
	for rel, content := range files {
		if err := s.repo.WriteFile(ctx, shared, name, rel, content); err != nil {
			return fmt.Errorf("writing sample file %q/%q: %w", name, rel, err)
		}
	}
	return nil
}

// seedFiles describes only files in the incoming sample, never a stale shared copy.
type seedFiles map[string][]byte

func (f seedFiles) FileExists(_ string, path string) bool { _, ok := f[path]; return ok }
