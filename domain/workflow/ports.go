package workflow

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a workflow does not exist.
var ErrNotFound = errors.New("workflow not found")

// WorkflowRepository defines the port for loading and listing workflow definitions.
// Implementations must wrap ErrNotFound (via fmt.Errorf %w) when a workflow does
// not exist, so that the HTTP adapter's errors.Is-based error mapping works correctly.
type WorkflowRepository interface {
	Load(ctx context.Context, name string) (*Workflow, error)
	List(ctx context.Context) ([]WorkflowSummary, error)
	SaveNodePositions(ctx context.Context, name string, positions map[string]NodePosition) error
}
