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
// idempotency (including the shipped @lib files, when lib is non-nil) is enforced by the SeedLedger in the workspace root; injected
// revision dependencies govern interrupted-save resume.
//
// files answers whether an @lib path a sample references exists; it is consulted
// against the SHARED library only (see sharedLibFiles), after the shipped
// library files have been seeded. A nil files leaves lib references unchecked.
func seedSampleWorkflows(ctx context.Context, sourceDir, workspacePath string, repo workflow.Repository, published workflow.PublishedStore, stamper *workflow.RevisionStamper, mutations workflow.MutationCoordinator, registry workflow.Registry, lib workflow.LibraryStore, files workflow.OwnerFileChecker, owner uuid.UUID, logger *slog.Logger, profiles ...workflow.ProfileChecker) error {
	source := filesystem.NewSampleSource(sourceDir)
	ledger := filesystem.NewSeedLedger(workspacePath)
	seeder := samples.NewSeeder(source, repo, published, ledger, owner, logger, profiles...)
	seeder.SetRevisionStamper(stamper)
	seeder.SetMutationCoordinator(mutations)
	seeder.SetRegistry(registry)
	if lib != nil {
		seeder.SetLibraryStore(lib)
	}
	if files != nil {
		seeder.SetLibFiles(sharedLibFiles{files})
	}
	return seeder.Seed(ctx)
}

// sharedLibFiles answers @lib existence from the shared library only. The
// ordinary file reader resolves the asking owner's private library first, so a
// private-only copy under the seed owner would make a globally published sample
// look self-contained although every other user would resolve nothing. Asking
// with no owner leaves the shared root as the only candidate.
type sharedLibFiles struct{ inner workflow.OwnerFileChecker }

func (s sharedLibFiles) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	return s.inner.FileExists(uuid.Nil, workflowName, relativePath)
}
