package workflow

import (
	"context"
)

// WorkflowRepository defines the port for loading and listing workflow definitions.
// Implementations must wrap the sentinel errors defined in errors.go (via
// fmt.Errorf %w) so that the HTTP adapter's errors.Is-based error mapping works.
type WorkflowRepository interface {
	Load(ctx context.Context, name string) (*Workflow, error)
	List(ctx context.Context) ([]WorkflowSummary, error)
	SaveNodePositions(ctx context.Context, name string, positions map[string]NodePosition) error

	// Write operations for workflow CRUD.
	Create(ctx context.Context, name string, wf *Workflow) error
	Update(ctx context.Context, name string, wf *Workflow) error
	Delete(ctx context.Context, name string) error

	// File management within a workflow directory.
	WriteFile(ctx context.Context, name string, relativePath string, content []byte) error
	DeleteFile(ctx context.Context, name string, relativePath string) error
	ListFiles(ctx context.Context, name string) ([]string, error)
}
