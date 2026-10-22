package main

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
)

// seedSampleWorkflows seeds bundled samples from sourceDir into the shared scope.
// The "should I run" decision (seed enabled + a non-empty source dir) is hoisted
// to the call site so boot wiring reads honestly; this function unconditionally
// seeds. profiles supplies the live group resolver checker. Once-per-name
// idempotency is enforced by the SeedLedger in the workspace root; injected
// revision dependencies govern interrupted-save resume.
func seedSampleWorkflows(ctx context.Context, sourceDir, workspacePath string, repo workflow.Repository, published workflow.PublishedStore, stamper *workflow.RevisionStamper, mutations workflow.MutationCoordinator, registry workflow.Registry, owner uuid.UUID, logger *slog.Logger, profiles ...workflow.ProfileChecker) error {
	source := filesystem.NewSampleSource(sourceDir)
	ledger := filesystem.NewSeedLedger(workspacePath)
	seeder := samples.NewSeeder(source, repo, published, ledger, owner, logger, profiles...)
	seeder.SetRevisionStamper(stamper)
	seeder.SetMutationCoordinator(mutations)
	seeder.SetRegistry(registry)
	return seeder.Seed(ctx)
}
