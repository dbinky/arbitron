package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// WorkspaceHomeRepo stores location metadata; it does not authorize writers.
type WorkspaceHomeRepo struct{ db *sql.DB }

func NewWorkspaceHomeRepo(db *sql.DB) *WorkspaceHomeRepo { return &WorkspaceHomeRepo{db: db} }

var _ runner.HomeStore = (*WorkspaceHomeRepo)(nil)

const homeColumns = `run_id, runner_id, generation, checkpoint_version, dirty, non_portable_reason, local_checkpoint`

func scanHome(s interface{ Scan(...any) error }) (runner.Home, error) {
	var h runner.Home
	err := s.Scan(&h.RunID, &h.RunnerID, &h.Generation, &h.CheckpointVersion, &h.Dirty, &h.NonPortableReason, &h.LocalCheckpoint)
	return h, err
}
func getHome(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id uuid.UUID) (runner.Home, error) {
	h, err := scanHome(q.QueryRowContext(ctx, `SELECT `+homeColumns+` FROM workspace_homes WHERE run_id=?`, id.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Home{RunID: id}, nil
	}
	return h, err
}
func (r *WorkspaceHomeRepo) GetHome(ctx context.Context, id uuid.UUID) (runner.Home, error) {
	return getHome(ctx, r.db, id)
}
func loadHomes(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[uuid.UUID]runner.Home, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+homeColumns+` FROM workspace_homes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]runner.Home{}
	for rows.Next() {
		h, err := scanHome(rows)
		if err != nil {
			return nil, err
		}
		out[h.RunID] = h
	}
	return out, rows.Err()
}
func (r *WorkspaceHomeRepo) ListByRunner(ctx context.Context, id runner.RunnerID) ([]runner.Home, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+homeColumns+` FROM workspace_homes WHERE runner_id=? ORDER BY run_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []runner.Home{}
	for rows.Next() {
		h, err := scanHome(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (r *WorkspaceHomeRepo) AcquireResource(ctx context.Context, id uuid.UUID, to runner.RunnerID, gen int64) (runner.Home, error) {
	return r.move(ctx, id, to, "acquire", 0, gen, runner.HomeMoveCause{}, true)
}
func (r *WorkspaceHomeRepo) HandOff(ctx context.Context, id uuid.UUID, to runner.RunnerID, reason string, bytes, gen int64, cause runner.HomeMoveCause) (runner.Home, error) {
	return r.move(ctx, id, to, reason, bytes, gen, cause, false)
}
func (r *WorkspaceHomeRepo) move(ctx context.Context, id uuid.UUID, to runner.RunnerID, reason string, bytes, gen int64, cause runner.HomeMoveCause, acquire bool) (runner.Home, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.Home{}, err
	}
	defer tx.Rollback()
	h, err := getHome(ctx, tx, id)
	if err != nil {
		return runner.Home{}, err
	}
	if h.Generation != gen {
		return runner.Home{}, runner.ErrHomeGenerationConflict
	}
	if acquire && h.RunnerID != runner.ServerHome && h.RunnerID != to {
		return runner.Home{}, runner.ErrHomeOwned
	}
	if h.RunnerID == to {
		return h, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_homes(run_id,updated_at) VALUES(?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT DO NOTHING`, id.String())
	if err != nil {
		return runner.Home{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_homes SET runner_id=?,generation=generation+1,dirty=0,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE run_id=? AND generation=?`, to, id.String(), gen)
	if err != nil {
		return runner.Home{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return runner.Home{}, err
	}
	if n != 1 {
		return runner.Home{}, runner.ErrHomeGenerationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO home_moves(run_id,from_runner,to_runner,reason,bytes,generation,moved_at,target_job_id,invocation_id,node_id,operation_id,from_generation,to_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, id.String(), h.RunnerID, to, reason, bytes, gen+1, time.Now().UTC().Format(time.RFC3339Nano), cause.TargetJobID, cause.InvocationID, cause.NodeID, cause.OperationID, gen, gen+1)
	if err != nil {
		return runner.Home{}, err
	}
	if err = tx.Commit(); err != nil {
		return runner.Home{}, err
	}
	h.RunnerID = to
	h.Generation++
	h.Dirty = false
	return h, nil
}
func (r *WorkspaceHomeRepo) Release(ctx context.Context, id uuid.UUID, gen int64) error {
	_, err := r.HandOff(ctx, id, runner.ServerHome, "release", 0, gen, runner.HomeMoveCause{})
	return err
}
func (r *WorkspaceHomeRepo) SetCheckpoint(ctx context.Context, id uuid.UUID, v int64) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO workspace_homes(run_id,checkpoint_version,updated_at) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(run_id) DO UPDATE SET checkpoint_version=MAX(checkpoint_version,excluded.checkpoint_version),updated_at=excluded.updated_at`, id.String(), v)
	return err
}
func (r *WorkspaceHomeRepo) MarkDirty(ctx context.Context, id uuid.UUID, dirty bool) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO workspace_homes(run_id,dirty,updated_at) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(run_id) DO UPDATE SET dirty=excluded.dirty,updated_at=excluded.updated_at`, id.String(), dirty)
	return err
}
func (r *WorkspaceHomeRepo) SetNonPortable(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO workspace_homes(run_id,non_portable_reason,updated_at) VALUES(?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(run_id) DO UPDATE SET non_portable_reason=excluded.non_portable_reason,updated_at=excluded.updated_at`, id.String(), reason)
	return err
}
func (r *WorkspaceHomeRepo) History(ctx context.Context, id uuid.UUID) ([]runner.HomeMove, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT from_runner,to_runner,reason,bytes,generation,moved_at,target_job_id,invocation_id,COALESCE(node_id,''),COALESCE(operation_id,''),COALESCE(from_generation,0),COALESCE(to_generation,0) FROM home_moves WHERE run_id=? ORDER BY generation`, id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []runner.HomeMove{}
	for rows.Next() {
		m := runner.HomeMove{RunID: id}
		var at string
		if err := rows.Scan(&m.From, &m.To, &m.Reason, &m.Bytes, &m.Generation, &at, &m.TargetJobID, &m.InvocationID, &m.NodeID, &m.OperationID, &m.FromGeneration, &m.ToGeneration); err != nil {
			return nil, err
		}
		m.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
