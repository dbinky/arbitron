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

// Compile-time: satisfies the Task-3 publisher lookup port.
var _ workflow.PublisherLookup = (*PublishedWorkflowRepo)(nil)

// PublishedWorkflow is the domain-facing publication record.
type PublishedWorkflow struct {
	Name        string
	PublisherID uuid.UUID
	SourceName  string
	PublishedAt time.Time
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
	}, true, nil
}

// Upsert records (or re-records) a publication. The publisher-only-overwrite
// rule is enforced by the caller (Task 5) before this is invoked.
func (r *PublishedWorkflowRepo) Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string) error {
	if err := r.queries.UpsertPublishedWorkflow(ctx, generated.UpsertPublishedWorkflowParams{
		Name:        name,
		PublisherID: publisher.String(),
		SourceName:  sourceName,
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
