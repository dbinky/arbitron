package samples

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Seeder writes not-yet-seeded sample workflows from a SampleSource into the
// shared scope and records each as a public publication owned by a bootstrap
// user. Each sample is seeded at most once (tracked via the Ledger).
type Seeder struct {
	source    SampleSource
	repo      workflow.WorkflowRepository
	published workflow.PublishedStore
	ledger    Ledger
	owner     uuid.UUID
	logger    *slog.Logger
}

// NewSeeder constructs a Seeder. owner owns the seeded publications.
func NewSeeder(source SampleSource, repo workflow.WorkflowRepository, published workflow.PublishedStore, ledger Ledger, owner uuid.UUID, logger *slog.Logger) *Seeder {
	return &Seeder{source: source, repo: repo, published: published, ledger: ledger, owner: owner, logger: logger}
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
func (s *Seeder) seedOne(ctx context.Context, name string) (bool, error) {
	wf, files, err := s.source.Read(ctx, name)
	if err != nil {
		s.logger.Warn("skipping malformed sample", "name", name, "err", err)
		return false, nil
	}
	shared := workflow.Shared()
	if err := s.repo.Create(ctx, shared, name, wf); err != nil {
		return false, fmt.Errorf("creating shared sample %q: %w", name, err)
	}
	for rel, content := range files {
		if err := s.repo.WriteFile(ctx, shared, name, rel, content); err != nil {
			return false, fmt.Errorf("writing sample file %q/%q: %w", name, rel, err)
		}
	}
	if err := s.published.Upsert(ctx, name, s.owner, name, workflow.VisibilityPublic, ""); err != nil {
		// Compensate the fresh write so we never leave an orphaned, unattributed
		// shared copy (mirrors WorkflowService.Publish's fresh-publish rollback).
		// The destination was empty before, so deleting it restores the prior
		// state. The rollback is best-effort; if Delete also fails, join both
		// wrapped errors so neither is lost, else surface just the Upsert error.
		if delErr := s.repo.Delete(ctx, shared, name); delErr != nil {
			return false, errors.Join(
				fmt.Errorf("recording publication for %q: %w", name, err),
				fmt.Errorf("rolling back shared sample %q: %w", name, delErr),
			)
		}
		return false, fmt.Errorf("recording publication for %q: %w", name, err)
	}
	return true, nil
}
