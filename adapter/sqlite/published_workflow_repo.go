package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time: satisfies the read+write publication store port.
var _ workflow.PublishedStore = (*PublishedWorkflowRepo)(nil)

// PublishedWorkflow is the domain-facing publication record.
type PublishedWorkflow struct {
	Name        string
	PublisherID uuid.UUID
	SourceName  string
	PublishedAt time.Time
	Visibility  string // "department" | "public"
	Department  string // "" for public
}

// PublishedWorkflowRepo is the authority for the unique-name and
// publisher-only-overwrite rules backing the shared workspace.
type PublishedWorkflowRepo struct {
	queries *generated.Queries
}

// NewPublishedWorkflowRepo creates a PublishedWorkflowRepo over the given DB.
func NewPublishedWorkflowRepo(db *sql.DB) *PublishedWorkflowRepo {
	return &PublishedWorkflowRepo{queries: generated.New(db)}
}

// PublisherOf implements workflow.PublisherLookup.
func (r *PublishedWorkflowRepo) PublisherOf(ctx context.Context, name string) (uuid.UUID, bool, error) {
	row, err := r.queries.GetPublishedWorkflow(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("get published %q: %w", name, err)
	}
	id, perr := parseUUID(row.PublisherID)
	if perr != nil {
		return uuid.Nil, false, fmt.Errorf("parse publisher id: %w", perr)
	}
	return id, true, nil
}

// Get returns the full publication record for a name, or (nil, false, nil) when
// it does not exist.
func (r *PublishedWorkflowRepo) Get(ctx context.Context, name string) (*PublishedWorkflow, bool, error) {
	row, err := r.queries.GetPublishedWorkflow(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get published %q: %w", name, err)
	}
	id, perr := parseUUID(row.PublisherID)
	if perr != nil {
		return nil, false, fmt.Errorf("parse publisher id: %w", perr)
	}
	publishedAt, terr := parseTime(row.PublishedAt)
	if terr != nil {
		return nil, false, fmt.Errorf("parse published_at: %w", terr)
	}
	return &PublishedWorkflow{
		Name:        row.Name,
		PublisherID: id,
		SourceName:  row.SourceName,
		PublishedAt: publishedAt,
		Visibility:  row.Visibility,
		Department:  row.Department.String, // "" when NULL
	}, true, nil
}

// GetPublication returns the full publication record for name, including its
// visibility and department, or (nil, false, nil) when no publication exists.
// It is the internal struct accessor the port-shaped PublicationOf bridges.
func (r *PublishedWorkflowRepo) GetPublication(ctx context.Context, name string) (*PublishedWorkflow, bool, error) {
	return r.Get(ctx, name)
}

// PublicationOf implements workflow.PublisherLookup. It maps the stored row to
// the domain Publication value. (zero, false, nil) when the name is unpublished.
func (r *PublishedWorkflowRepo) PublicationOf(ctx context.Context, name string) (workflow.Publication, bool, error) {
	row, ok, err := r.GetPublication(ctx, name)
	if err != nil || !ok {
		return workflow.Publication{}, ok, err
	}
	return workflow.Publication{
		Publisher:  row.PublisherID,
		SourceName: row.SourceName,
		Visibility: workflow.Visibility(row.Visibility),
		Department: row.Department,
	}, true, nil
}

// Upsert records (or re-records) a publication with its visibility level. A
// department string is stored as SQL NULL when empty (public). The
// publisher-only-overwrite and role-gating rules are enforced by the caller
// before this is invoked.
func (r *PublishedWorkflowRepo) Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	dept := sql.NullString{}
	if department != "" {
		dept = sql.NullString{String: department, Valid: true}
	}
	if err := r.queries.UpsertPublishedWorkflow(ctx, generated.UpsertPublishedWorkflowParams{
		Name:        name,
		PublisherID: publisher.String(),
		SourceName:  sourceName,
		Visibility:  string(visibility),
		Department:  dept,
	}); err != nil {
		return fmt.Errorf("upsert published %q: %w", name, err)
	}
	return nil
}

// List returns all publication records sorted by name.
func (r *PublishedWorkflowRepo) List(ctx context.Context) ([]PublishedWorkflow, error) {
	rows, err := r.queries.ListPublishedWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list published workflows: %w", err)
	}
	out := make([]PublishedWorkflow, 0, len(rows))
	for _, row := range rows {
		id, perr := parseUUID(row.PublisherID)
		if perr != nil {
			return nil, fmt.Errorf("parse publisher id: %w", perr)
		}
		publishedAt, terr := parseTime(row.PublishedAt)
		if terr != nil {
			return nil, fmt.Errorf("parse published_at: %w", terr)
		}
		out = append(out, PublishedWorkflow{
			Name:        row.Name,
			PublisherID: id,
			SourceName:  row.SourceName,
			PublishedAt: publishedAt,
			Visibility:  row.Visibility,
			Department:  row.Department.String,
		})
	}
	return out, nil
}

// Delete removes the publication record (used if a publish rolls back, or when
// a shared workflow is unpublished).
func (r *PublishedWorkflowRepo) Delete(ctx context.Context, name string) error {
	if err := r.queries.DeletePublishedWorkflow(ctx, name); err != nil {
		return fmt.Errorf("delete published %q: %w", name, err)
	}
	return nil
}
