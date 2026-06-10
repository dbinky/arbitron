package main

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
)

// seedSampleWorkflows seeds bundled samples into the shared scope when enabled.
// It is a no-op when seed is false or sourceDir is empty. The actual once-per-name
// idempotency is enforced by the SeedLedger in the workspace root.
func seedSampleWorkflows(ctx context.Context, seed bool, sourceDir, workspacePath string, repo workflow.WorkflowRepository, published workflow.PublishedStore, owner uuid.UUID, logger *slog.Logger) error {
	if !seed || sourceDir == "" {
		return nil
	}
	source := filesystem.NewSampleSource(sourceDir)
	ledger := filesystem.NewSeedLedger(workspacePath)
	return samples.NewSeeder(source, repo, published, ledger, owner, logger).Seed(ctx)
}
