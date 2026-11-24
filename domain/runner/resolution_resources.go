package runner

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var (
	// ErrResolutionClosed prevents materialization into a closed attempt.
	ErrResolutionClosed = errors.New("runner: resolution resource is closed")
	// ErrResolutionResourceConflict rejects changes to a registered resource.
	ErrResolutionResourceConflict = errors.New("runner: resolution resource conflicts with its registration")
)

// ResolutionResourceRegistry persists resolution resources and materialization state.
type ResolutionResourceRegistry interface {
	EnsureResolution(ctx context.Context, r engine.ResolutionResource) error
	GetResolution(ctx context.Context, id uuid.UUID) (engine.ResolutionResource, error)
	MarkMaterialized(ctx context.Context, id uuid.UUID, c engine.WorkspaceCompletion) error
	MaterializedRef(ctx context.Context, id uuid.UUID) (string, error)
	CloseResolution(ctx context.Context, id uuid.UUID) error
}
