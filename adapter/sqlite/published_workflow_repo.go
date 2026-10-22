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
	Revision    string
	WorkflowID  uuid.UUID
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
	db      *sql.DB
	queries *generated.Queries
}

// NewPublishedWorkflowRepo creates a PublishedWorkflowRepo over the given DB.
func NewPublishedWorkflowRepo(db *sql.DB) *PublishedWorkflowRepo {
	return &PublishedWorkflowRepo{db: db, queries: generated.New(db)}
}

// SetWorkflowID links a publication record to the stable workflow identity
// (published_workflows.workflow_id, added in migration 017). It is invoked by
// the publish path once the shared name's registry id is known. A Nil id is a
// no-op (nothing to link). The generated UpsertPublishedWorkflow query predates
// the column, so this is a separate, hand-written UPDATE rather than a blanket
// sqlc regeneration (which would corrupt the hand-curated generated/ tree).
func (r *PublishedWorkflowRepo) SetWorkflowID(ctx context.Context, name string, id uuid.UUID) error {
	if id == uuid.Nil {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE published_workflows SET workflow_id = ? WHERE name = ?`,
		id.String(), name,
	); err != nil {
		return fmt.Errorf("linking published %q to workflow id: %w", name, err)
	}
	return nil
}

var _ workflow.RevisionPublicationStore = (*PublishedWorkflowRepo)(nil)

// CommitPublication makes identity and exact revision proof visible together.
// A nil identity is supported for local, registry-less publication only.
func (r *PublishedWorkflowRepo) CommitPublication(ctx context.Context, name string, publication workflow.Publication) error {
	if publication.Revision == "" {
		return fmt.Errorf("publication requires a saved revision")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO published_workflows (
        name,publisher_id,source_name,visibility,department,workflow_id,saved_revision
    ) VALUES(?,?,?,?,?,?,?)
    ON CONFLICT(name) DO UPDATE SET
        publisher_id=excluded.publisher_id,source_name=excluded.source_name,
        visibility=excluded.visibility,department=excluded.department,
        workflow_id=excluded.workflow_id,saved_revision=excluded.saved_revision,
        published_at=strftime('%Y-%m-%dT%H:%M:%SZ')`, name, publication.Publisher.String(), publication.SourceName, publication.Visibility, deptToNullString(publication.Department), ownerToNullString(publication.WorkflowID), publication.Revision)
	if err != nil {
		return fmt.Errorf("committing publication %q: %w", name, err)
	}
	return nil
}

// RenamePublication is the shared rename commit point. Private workflows use
// Registry.Rename instead; they have no publication to prove this operation.
func (r *PublishedWorkflowRepo) RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, publication workflow.Publication) error {
	if id == uuid.Nil || publication.WorkflowID != id || publication.Revision == "" {
		return fmt.Errorf("rename publication requires matching identity and saved revision")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin publication rename: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE workflow_identity SET name=?
    WHERE id=? AND name=? AND scope='shared' AND EXISTS (
        SELECT 1 FROM published_workflows
        WHERE name=? AND workflow_id=? AND publisher_id=? AND source_name=?
    )`, newName, id.String(), oldName, oldName, id.String(), publication.Publisher.String(), publication.SourceName)
	if err != nil {
		return publicationRenameError(newName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return publicationRenameError(newName, err)
	}
	if n == 0 {
		return fmt.Errorf("publication source %q: %w", oldName, workflow.ErrNotFound)
	}
	_, err = tx.ExecContext(ctx, `UPDATE published_workflows SET
        name=?,publisher_id=?,source_name=?,visibility=?,department=?,workflow_id=?,saved_revision=?,
        published_at=strftime('%Y-%m-%dT%H:%M:%SZ')
    WHERE name=?`, newName, publication.Publisher.String(), publication.SourceName, publication.Visibility, deptToNullString(publication.Department), id.String(), publication.Revision, oldName)
	if err != nil {
		return publicationRenameError(newName, err)
	}
	if err = tx.Commit(); err != nil {
		return publicationRenameError(newName, err)
	}
	return nil
}
func publicationRenameError(name string, err error) error {
	if isUniqueViolation(err) {
		return fmt.Errorf("publication %q: %w", name, workflow.ErrAlreadyExists)
	}
	return fmt.Errorf("renaming publication %q: %w", name, err)
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
	row, err := scanPublication(r.db.QueryRowContext(ctx, `SELECT `+publicationColumns+` FROM published_workflows WHERE name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get published %q: %w", name, err)
	}
	return &row, true, nil
}

const publicationColumns = `name,publisher_id,source_name,published_at,visibility,department,workflow_id,saved_revision`

func scanPublication(row interface{ Scan(...any) error }) (PublishedWorkflow, error) {
	var p PublishedWorkflow
	var publisher, published string
	var department, id, revision sql.NullString
	err := row.Scan(&p.Name, &publisher, &p.SourceName, &published, &p.Visibility, &department, &id, &revision)
	if err != nil {
		return p, err
	}
	p.PublisherID, err = uuid.Parse(publisher)
	if err != nil {
		return p, fmt.Errorf("parse publisher id: %w", err)
	}
	p.PublishedAt, err = parseTime(published)
	if err != nil {
		return p, fmt.Errorf("parse published_at: %w", err)
	}
	p.WorkflowID, err = ownerFromNullString(id)
	if err != nil {
		return p, err
	}
	p.Department = department.String
	p.Revision = revision.String
	return p, nil
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
		Revision:   row.Revision,
		WorkflowID: row.WorkflowID,
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
	rows, err := r.db.QueryContext(ctx, `SELECT `+publicationColumns+` FROM published_workflows ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list published workflows: %w", err)
	}
	defer rows.Close()
	out := []PublishedWorkflow{}
	for rows.Next() {
		row, err := scanPublication(rows)
		if err != nil {
			return nil, fmt.Errorf("decode publication: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Delete removes the publication record (used if a publish rolls back, or when
// a shared workflow is unpublished).
func (r *PublishedWorkflowRepo) Delete(ctx context.Context, name string) error {
	if err := r.queries.DeletePublishedWorkflow(ctx, name); err != nil {
		return fmt.Errorf("delete published %q: %w", name, err)
	}
	return nil
}
