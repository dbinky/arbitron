package workflow

import (
	"context"
)

// WorkflowRepository defines the port for loading and listing workflow definitions.
// Implementations must wrap the sentinel errors defined in errors.go (via
// fmt.Errorf %w) so that the HTTP adapter's errors.Is-based error mapping works.
type WorkflowRepository interface {
	Load(ctx context.Context, scope Scope, name string) (*Workflow, error)
	List(ctx context.Context, scope Scope) ([]WorkflowSummary, error)
	SaveNodePositions(ctx context.Context, scope Scope, name string, positions map[string]NodePosition) error

	// Write operations for workflow CRUD.
	Create(ctx context.Context, scope Scope, name string, wf *Workflow) error
	Update(ctx context.Context, scope Scope, name string, wf *Workflow) error
	Delete(ctx context.Context, scope Scope, name string) error

	// File management within a workflow directory.
	WriteFile(ctx context.Context, scope Scope, name string, relativePath string, content []byte) error
	DeleteFile(ctx context.Context, scope Scope, name string, relativePath string) error
	ListFiles(ctx context.Context, scope Scope, name string) ([]string, error)

	// Exists reports whether a workflow with the given name exists in scope.
	Exists(ctx context.Context, scope Scope, name string) (bool, error)
}
