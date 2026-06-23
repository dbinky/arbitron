package workflow

import (
	"context"

	"github.com/google/uuid"
)

// Repository defines the port for loading and listing workflow definitions.
// Implementations must wrap the sentinel errors defined in errors.go (via
// fmt.Errorf %w) so that the HTTP adapter's errors.Is-based error mapping works.
type Repository interface {
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

	// CopyTree atomically copies an entire workflow folder from src scope/name to
	// dst scope/name (temp dir + rename). It fails with ErrAlreadyExists if dst
	// already exists unless overwrite is true, in which case it replaces dst
	// atomically. It fails with ErrNotFound if the source does not exist.
	CopyTree(ctx context.Context, src Scope, srcName string, dst Scope, dstName string, overwrite bool) error
}

// ReferenceBackfiller is the port the one-time BackfillReferences uses to attach
// stable workflow ids to the reference tables (instances, schedules,
// published_workflows) that today key a workflow purely by name. It owns the SQL;
// the domain service owns the name->id resolution policy.
//
// A nil backfiller makes BackfillReferences a no-op (local/unconfigured mode).
type ReferenceBackfiller interface {
	// UnresolvedReferenceNames returns the distinct workflow names that appear in
	// any reference table with a NULL workflow_id. Resolved (non-NULL) rows are
	// excluded so the operation is idempotent.
	UnresolvedReferenceNames(ctx context.Context) ([]string, error)
	// SetReferenceID stamps workflow_id = id on every reference row whose
	// workflow_name equals name AND whose workflow_id is currently NULL, across all
	// three tables. Rows already carrying an id are left untouched. It reports the
	// total number of rows updated.
	SetReferenceID(ctx context.Context, name string, id uuid.UUID) (int64, error)

	// ReferencedWorkflowIDs returns the distinct NON-NULL workflow_id values that
	// appear in any reference table. The startup orphan-sweep resolves each against
	// the registry; an id with no registry row is a DANGLING reference (the identity
	// was deleted out from under historical rows) and is reconciled to NULL.
	ReferencedWorkflowIDs(ctx context.Context) ([]uuid.UUID, error)
	// ClearReferenceID resets workflow_id to NULL on every reference row currently
	// carrying id, across all three tables, reconciling a dangling reference to the
	// accepted "dangling label" NULL state (migration 016/017). It reports the total
	// number of rows updated.
	ClearReferenceID(ctx context.Context, id uuid.UUID) (int64, error)
}
