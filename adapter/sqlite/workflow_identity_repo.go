package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// WorkflowIdentityRepo implements workflow.WorkflowRegistry over SQLite. The
// workflow_identity table's UNIQUE(name) constraint is the authoritative
// global-uniqueness rule; each row resolves name<->id. A UNIQUE violation maps to
// workflow.ErrAlreadyExists and a missing row to workflow.ErrNotFound.
type WorkflowIdentityRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// Compile-time assertion that WorkflowIdentityRepo satisfies the domain port.
var _ workflow.WorkflowRegistry = (*WorkflowIdentityRepo)(nil)

// NewWorkflowIdentityRepo creates a WorkflowIdentityRepo over the given database.
func NewWorkflowIdentityRepo(db *sql.DB) *WorkflowIdentityRepo {
	return &WorkflowIdentityRepo{db: db, queries: generated.New(db)}
}

// scopeToString maps a ScopeKind to its stored label.
func scopeToString(s workflow.ScopeKind) string {
	if s == workflow.ScopeShared {
		return workflow.ScopeLabelShared
	}
	return workflow.ScopeLabelPrivate
}

// scopeFromString maps a stored label back to a ScopeKind.
func scopeFromString(s string) workflow.ScopeKind {
	if s == workflow.ScopeLabelShared {
		return workflow.ScopeShared
	}
	return workflow.ScopePrivate
}

// ownerToNullString maps an owner UUID to a nullable column value: the zero UUID
// (a shared entry has no single private owner) becomes SQL NULL.
func ownerToNullString(id uuid.UUID) sql.NullString {
	if id == uuid.Nil {
		return sql.NullString{}
	}
	return sql.NullString{String: id.String(), Valid: true}
}

// ownerFromNullString maps a nullable owner column back to a UUID; NULL => zero.
func ownerFromNullString(ns sql.NullString) (uuid.UUID, error) {
	if !ns.Valid || ns.String == "" {
		return uuid.Nil, nil
	}
	id, err := uuid.Parse(ns.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parsing owner_id %q: %w", ns.String, err)
	}
	return id, nil
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (r *WorkflowIdentityRepo) Register(ctx context.Context, e workflow.RegistryEntry) error {
	err := r.queries.RegisterWorkflow(ctx, generated.RegisterWorkflowParams{
		ID:       e.ID.String(),
		Name:     e.Name,
		Scope:    scopeToString(e.Scope),
		OwnerID:  ownerToNullString(e.OwnerID),
		Archived: boolToInt64(e.Archived),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("workflow %q: %w", e.Name, workflow.ErrAlreadyExists)
		}
		return fmt.Errorf("registering workflow %q: %w", e.Name, err)
	}
	return nil
}

func (r *WorkflowIdentityRepo) ResolveID(ctx context.Context, name string) (uuid.UUID, error) {
	idStr, err := r.queries.ResolveWorkflowID(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("workflow %q: %w", name, workflow.ErrNotFound)
		}
		return uuid.Nil, fmt.Errorf("resolving id for workflow %q: %w", name, err)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parsing id %q for workflow %q: %w", idStr, name, err)
	}
	return id, nil
}

func (r *WorkflowIdentityRepo) ResolveName(ctx context.Context, id uuid.UUID) (string, error) {
	name, err := r.queries.ResolveWorkflowName(ctx, id.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("workflow id %s: %w", id, workflow.ErrNotFound)
		}
		return "", fmt.Errorf("resolving name for workflow id %s: %w", id, err)
	}
	return name, nil
}

func (r *WorkflowIdentityRepo) Rename(ctx context.Context, id uuid.UUID, newName string) error {
	rows, err := r.queries.RenameWorkflow(ctx, generated.RenameWorkflowParams{
		Name: newName,
		ID:   id.String(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("workflow %q: %w", newName, workflow.ErrAlreadyExists)
		}
		return fmt.Errorf("renaming workflow id %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("workflow id %s: %w", id, workflow.ErrNotFound)
	}
	return nil
}

func (r *WorkflowIdentityRepo) SetArchived(ctx context.Context, id uuid.UUID, archived bool) error {
	rows, err := r.queries.SetArchived(ctx, generated.SetArchivedParams{
		Archived: boolToInt64(archived),
		ID:       id.String(),
	})
	if err != nil {
		return fmt.Errorf("setting archived for workflow id %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("workflow id %s: %w", id, workflow.ErrNotFound)
	}
	return nil
}

func (r *WorkflowIdentityRepo) SetOwner(ctx context.Context, id, owner uuid.UUID) error {
	rows, err := r.queries.SetOwner(ctx, generated.SetOwnerParams{
		OwnerID: ownerToNullString(owner),
		ID:      id.String(),
	})
	if err != nil {
		return fmt.Errorf("setting owner for workflow id %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("workflow id %s: %w", id, workflow.ErrNotFound)
	}
	return nil
}

func (r *WorkflowIdentityRepo) Delete(ctx context.Context, id uuid.UUID) error {
	rows, err := r.queries.DeleteWorkflowIdentity(ctx, id.String())
	if err != nil {
		return fmt.Errorf("deleting workflow id %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("workflow id %s: %w", id, workflow.ErrNotFound)
	}
	return nil
}

// IsArchived reports whether the workflow registered under name is archived. It
// is the engine's optional archive-check seam (engine.ArchiveChecker): the
// trigger path consults it to refuse archived workflows. An unknown name returns
// workflow.ErrNotFound. The query is hand-written (no generated code) to keep the
// curated generated/ tree untouched.
func (r *WorkflowIdentityRepo) IsArchived(ctx context.Context, name string) (bool, error) {
	var archived int64
	err := r.db.QueryRowContext(ctx, `SELECT archived FROM workflow_identity WHERE name = ?`, name).Scan(&archived)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("workflow %q: %w", name, workflow.ErrNotFound)
		}
		return false, fmt.Errorf("reading archived flag for workflow %q: %w", name, err)
	}
	return archived != 0, nil
}

func (r *WorkflowIdentityRepo) List(ctx context.Context) ([]workflow.RegistryEntry, error) {
	rows, err := r.queries.ListWorkflowIdentities(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing workflow identities: %w", err)
	}
	out := make([]workflow.RegistryEntry, 0, len(rows))
	for _, row := range rows {
		id, err := uuid.Parse(row.ID)
		if err != nil {
			return nil, fmt.Errorf("parsing id %q: %w", row.ID, err)
		}
		owner, err := ownerFromNullString(row.OwnerID)
		if err != nil {
			return nil, err
		}
		out = append(out, workflow.RegistryEntry{
			ID:       id,
			Name:     row.Name,
			Scope:    scopeFromString(row.Scope),
			OwnerID:  owner,
			Archived: row.Archived != 0,
		})
	}
	return out, nil
}
