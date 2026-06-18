package workflow

import (
	"context"

	"github.com/google/uuid"
)

// ServiceResolveID exposes the unexported resolveID helper to the external test
// package so the name→id seam can be exercised directly.
func ServiceResolveID(s *WorkflowService, ctx context.Context, name string) (uuid.UUID, error) {
	return s.resolveID(ctx, name)
}

// ServiceResolveName exposes the unexported resolveName helper to the external
// test package so the id→name seam can be exercised directly.
func ServiceResolveName(s *WorkflowService, ctx context.Context, id uuid.UUID) (string, error) {
	return s.resolveName(ctx, id)
}
