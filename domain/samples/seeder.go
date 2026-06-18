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
	repo      workflow.Repository
	published workflow.PublishedStore
	ledger    Ledger
	owner     uuid.UUID
	logger    *slog.Logger
}

// NewSeeder constructs a Seeder. owner owns the seeded publications.
func NewSeeder(source SampleSource, repo workflow.Repository, published workflow.PublishedStore, ledger Ledger, owner uuid.UUID, logger *slog.Logger) *Seeder {
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
//
// Seeding is re-entrant: if a previous boot was killed between writing the
// shared tree and recording the ledger, the shared dir already exists. The
// write-tree + record + rollback + idempotency invariant lives in
// workflow.WriteAndRecord; seedOne supplies a write step that tolerates an
// already-present, bootstrap-owned tree (resume) and rolls back a fresh write
// whose publication failed.
func (s *Seeder) seedOne(ctx context.Context, name string) (bool, error) {
	wf, files, err := s.source.Read(ctx, name)
	if err != nil {
		s.logger.Warn("skipping malformed sample", "name", name, "err", err)
		return false, nil
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

// writeTree materializes the shared sample tree. On a fresh publish it Creates
// the workflow; if Create reports the dir already exists, the prior boot crashed
// after Create but before recording the publication — adopt that orphaned,
// bootstrap-owned tree and re-write its files idempotently rather than wedging.
// On the resume (overwrite) path the publication already exists, so Create is
// skipped and the files are re-written to heal any partial prior write.
func (s *Seeder) writeTree(ctx context.Context, shared workflow.Scope, name string, wf *workflow.Workflow, files map[string][]byte, overwrite bool) error {
	if !overwrite {
		if err := s.repo.Create(ctx, shared, name, wf); err != nil {
			if !errors.Is(err, workflow.ErrAlreadyExists) {
				return fmt.Errorf("creating shared sample %q: %w", name, err)
			}
			// An unpublished shared dir of this name already exists — only safe to
			// adopt if it was left by us (a crashed prior seed). A dir we did not
			// create means a name collision we must not silently overwrite.
			exists, exErr := s.repo.Exists(ctx, shared, name)
			if exErr != nil {
				return fmt.Errorf("checking shared sample %q: %w", name, exErr)
			}
			if !exists {
				return fmt.Errorf("creating shared sample %q: %w", name, err)
			}
		}
	}
	for rel, content := range files {
		if err := s.repo.WriteFile(ctx, shared, name, rel, content); err != nil {
			return fmt.Errorf("writing sample file %q/%q: %w", name, rel, err)
		}
	}
	return nil
}
