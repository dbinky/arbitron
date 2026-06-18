package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// BackfillReferences is the one-time startup reconciliation that attaches the
// stable workflow id (from the Phase-1 identity registry) to the reference tables
// that today key a workflow purely by name: instances, schedules, and
// published_workflows. For each distinct name that still has a NULL workflow_id,
// it resolves the name through the registry and stamps the id onto every matching
// NULL-id row.
//
// It is purely additive: workflow_name is retained as a label and read/trigger
// behavior is unchanged. A name that no longer resolves (e.g. the workflow was
// deleted) is left NULL — a dangling historical label — and Warn-logged, NOT an
// error. Rows that already carry a workflow_id are excluded (the candidate set is
// NULL-id rows only), so the operation is idempotent: a second run finds nothing
// to do.
//
// A nil registry or nil reference backfiller makes this a no-op
// (local/unconfigured deployments).
func (s *WorkflowService) BackfillReferences(ctx context.Context) error {
	if s.registry == nil || s.refBackfiller == nil {
		return nil
	}

	names, err := s.refBackfiller.UnresolvedReferenceNames(ctx)
	if err != nil {
		return fmt.Errorf("backfill references: listing unresolved names: %w", err)
	}

	for _, name := range names {
		id, err := s.registry.ResolveID(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// Dangling historical label: the workflow no longer exists in the
				// registry. Leave the rows NULL and record it; not an error.
				slog.Default().Warn("workflow reference name does not resolve to an id; leaving workflow_id NULL",
					"workflow_name", name)
				continue
			}
			return fmt.Errorf("backfill references: resolving %q: %w", name, err)
		}
		if _, err := s.refBackfiller.SetReferenceID(ctx, name, id); err != nil {
			return fmt.Errorf("backfill references: stamping id for %q: %w", name, err)
		}
	}
	return nil
}

// SweepOrphans is the one-time startup self-heal for the bounded, benign
// half-states a mid-op crash can leave (PR #44 Consider #2). It reuses the
// existing backfill machinery — no outbox, no replaying reconciler — to repair
// the DANGLING-workflow_id orphan shape: a reference row whose NON-NULL
// workflow_id no longer resolves in the registry (the identity was deleted out
// from under historical rows). Such a reference is reconciled to NULL, the
// accepted "dangling label" transient state documented in migrations 016/017.
//
// It is idempotent (a second run finds nothing dangling) and best-effort at the
// call site (the boot wiring logs a failure, not fatal). A nil registry or nil
// reference backfiller makes it a no-op (local/unconfigured deployments).
//
// The complementary orphan-FOLDER shape (a folder with no registry row) is
// already reconciled by BackfillRegistry, which registers any unregistered
// folder on the same boot; SweepOrphans therefore focuses on the dangling-id
// shape that the registry-side backfill cannot see.
func (s *WorkflowService) SweepOrphans(ctx context.Context) error {
	if s.registry == nil || s.refBackfiller == nil {
		return nil
	}

	ids, err := s.refBackfiller.ReferencedWorkflowIDs(ctx)
	if err != nil {
		return fmt.Errorf("sweep orphans: listing referenced ids: %w", err)
	}

	for _, id := range ids {
		if _, err := s.registry.ResolveName(ctx, id); err == nil {
			continue // the identity still exists — not an orphan.
		} else if !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("sweep orphans: resolving id %s: %w", id, err)
		}
		// Dangling reference: the identity is gone. Reconcile the rows to NULL.
		n, cerr := s.refBackfiller.ClearReferenceID(ctx, id)
		if cerr != nil {
			return fmt.Errorf("sweep orphans: clearing dangling id %s: %w", id, cerr)
		}
		slog.Default().Warn("reconciled dangling workflow_id reference to NULL (deleted identity)",
			"workflow_id", id, "rows", n)
	}
	return nil
}
