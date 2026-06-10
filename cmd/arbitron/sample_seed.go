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
// seeds. Once-per-name idempotency is enforced by the SeedLedger in the
// workspace root, and seeding is re-entrant across a crash-interrupted boot.
func seedSampleWorkflows(ctx context.Context, sourceDir, workspacePath string, repo workflow.WorkflowRepository, published workflow.PublishedStore, owner uuid.UUID, logger *slog.Logger) error {
	source := filesystem.NewSampleSource(sourceDir)
	ledger := filesystem.NewSeedLedger(workspacePath)
	return samples.NewSeeder(source, repo, published, ledger, owner, logger).Seed(ctx)
}
