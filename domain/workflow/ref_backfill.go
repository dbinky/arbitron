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
