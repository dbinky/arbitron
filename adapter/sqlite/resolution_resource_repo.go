package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

var _ runner.ResolutionResourceRegistry = (*ResolutionResourceRepo)(nil)

// ResolutionResourceRepo persists private resolution staging registrations.
type ResolutionResourceRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewResolutionResourceRepo builds the registry over the shared DB.
func NewResolutionResourceRepo(db *sql.DB) *ResolutionResourceRepo {
	return &ResolutionResourceRepo{db: db, now: time.Now}
}

func (r *ResolutionResourceRepo) EnsureResolution(ctx context.Context, res engine.ResolutionResource) error {
	if err := res.Validate(); err != nil {
		return err
	}
	now := r.now().UnixMilli()
	if _, err := r.db.ExecContext(ctx, `INSERT INTO resolution_resources
(id, group_run_id, merge_index, attempt, task_key, source_dir, work_dir, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		uuidToString(res.ID), uuidToString(res.Identity.GroupRunID), res.Identity.MergeIndex, res.Identity.Attempt,
		res.Identity.TaskKey, res.SourceDir, res.WorkDir, now, now); err != nil {
		return fmt.Errorf("ensure resolution resource: %w", err)
	}
	got, err := r.GetResolution(ctx, res.ID)
	if err != nil {
		return err
	}
	want := res
	want.Identity.Open = true
	if got != want {
		return fmt.Errorf("%w: %s", runner.ErrResolutionResourceConflict, res.ID)
	}
	return nil
}

func (r *ResolutionResourceRepo) GetResolution(ctx context.Context, id uuid.UUID) (engine.ResolutionResource, error) {
	var gid, task, src, work string
	var merge, attempt int
	err := r.db.QueryRowContext(ctx, `SELECT group_run_id, merge_index, attempt, task_key, source_dir, work_dir
FROM resolution_resources WHERE id = ?`, uuidToString(id)).Scan(&gid, &merge, &attempt, &task, &src, &work)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.ResolutionResource{}, fmt.Errorf("resolution resource %s: %w", id, instance.ErrNotFound)
	}
	if err != nil {
		return engine.ResolutionResource{}, fmt.Errorf("get resolution resource: %w", err)
	}
	groupRun, err := parseUUID(gid)
	if err != nil {
		return engine.ResolutionResource{}, err
	}
	return engine.ResolutionResource{ID: id, SourceDir: src, WorkDir: work, Identity: engine.GroupResolutionIdentity{GroupRunID: groupRun, MergeIndex: merge, Attempt: attempt, TaskKey: task, Open: true}}, nil
}

func (r *ResolutionResourceRepo) MarkMaterialized(ctx context.Context, id uuid.UUID, c engine.WorkspaceCompletion) error {
	if _, err := r.GetResolution(ctx, id); err != nil {
		return err
	}
	if c.Ref == "" || c.Resource.Key != engine.ResolutionResourceKey(id) {
		return fmt.Errorf("mark materialized %s: receipt does not name this resource", id)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE resolution_resources SET materialized_ref = ?, updated_at = ?
WHERE id = ? AND state = 'open'`, c.Ref, r.now().UnixMilli(), uuidToString(id))
	if err != nil {
		return fmt.Errorf("mark materialized: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	return r.missingOrClosed(ctx, id)
}

func (r *ResolutionResourceRepo) MaterializedRef(ctx context.Context, id uuid.UUID) (string, error) {
	var ref string
	err := r.db.QueryRowContext(ctx, `SELECT materialized_ref FROM resolution_resources WHERE id = ?`, uuidToString(id)).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolution resource %s: %w", id, instance.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read materialized ref: %w", err)
	}
	return ref, nil
}

func (r *ResolutionResourceRepo) CloseResolution(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx, `UPDATE resolution_resources SET state = 'closed', updated_at = ? WHERE id = ?`, r.now().UnixMilli(), uuidToString(id))
	if err != nil {
		return fmt.Errorf("close resolution resource: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("resolution resource %s: %w", id, instance.ErrNotFound)
	}
	return nil
}

func (r *ResolutionResourceRepo) missingOrClosed(ctx context.Context, id uuid.UUID) error {
	var state string
	err := r.db.QueryRowContext(ctx, `SELECT state FROM resolution_resources WHERE id = ?`, uuidToString(id)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("resolution resource %s: %w", id, instance.ErrNotFound)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("resolution resource %s: %w", id, runner.ErrResolutionClosed)
}
