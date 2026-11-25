package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// BuiltInProcessRepo implements runner.BuiltInProcessStore over 044
// builtin_processes(job_id, generation, guardian_pid, process_start_marker,
// state). The built-in runner is implied: the table has no runner column, and
// every fence must name runner.BuiltInRunnerID.
type BuiltInProcessRepo struct{ db *sql.DB }

var _ runner.BuiltInProcessStore = (*BuiltInProcessRepo)(nil)

// NewBuiltInProcessRepo builds the repo on the shared DB.
func NewBuiltInProcessRepo(db *sql.DB) *BuiltInProcessRepo { return &BuiltInProcessRepo{db: db} }

func builtInFence(f runner.Fence) error {
	if f.JobID == uuid.Nil || f.RunnerID != runner.BuiltInRunnerID || f.Generation < 1 {
		return fmt.Errorf("%w: supervised processes need an exact built-in job fence (got %s/%q/%d)", runner.ErrFenced, f.JobID, f.RunnerID, f.Generation)
	}
	return nil
}

func readBuiltInProcess(ctx context.Context, q groupQueryer, f runner.Fence) (runner.BuiltInProcess, bool, error) {
	p := runner.BuiltInProcess{Fence: f}
	var state string
	err := q.QueryRowContext(ctx, `SELECT guardian_pid, process_start_marker, state FROM builtin_processes WHERE job_id = ? AND generation = ?`,
		uuidToString(f.JobID), f.Generation).Scan(&p.GuardianPID, &p.StartMarker, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.BuiltInProcess{}, false, nil
	}
	if err != nil {
		return runner.BuiltInProcess{}, false, fmt.Errorf("builtin process: read %s/%d: %w", f.JobID, f.Generation, err)
	}
	p.State = runner.BuiltInProcessState(state)
	return p, true, nil
}

func (r *BuiltInProcessRepo) Permit(ctx context.Context, f runner.Fence) error {
	if err := builtInFence(f); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("builtin process: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// The first statement takes the SQLite write lock (the same idiom as
	// lockLease), so the fence read and the row write cannot interleave with a
	// concurrent stop.
	if _, err := tx.ExecContext(ctx, `UPDATE builtin_processes SET state = state WHERE job_id = ? AND generation = ?`, uuidToString(f.JobID), f.Generation); err != nil {
		return fmt.Errorf("builtin process: lock: %w", err)
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE id = ? AND generation = ? AND runner_id = ? AND state IN ('offered', 'running')`,
		uuidToString(f.JobID), f.Generation, string(f.RunnerID)).Scan(&live); err != nil {
		return fmt.Errorf("builtin process: job fence: %w", err)
	}
	if live == 0 {
		return fmt.Errorf("%w: job %s generation %d is not an active built-in assignment", runner.ErrFenced, f.JobID, f.Generation)
	}
	cur, ok, err := readBuiltInProcess(ctx, tx, f)
	if err != nil {
		return err
	}
	switch {
	case !ok:
		_, err = tx.ExecContext(ctx, `INSERT INTO builtin_processes(job_id, generation, guardian_pid, process_start_marker, state) VALUES (?, ?, 0, '', ?)`,
			uuidToString(f.JobID), f.Generation, string(runner.ProcessPermitted))
	case cur.State == runner.ProcessStopped:
		_, err = tx.ExecContext(ctx, `UPDATE builtin_processes SET guardian_pid = 0, process_start_marker = '', state = ? WHERE job_id = ? AND generation = ?`,
			string(runner.ProcessPermitted), uuidToString(f.JobID), f.Generation)
	default:
		return fmt.Errorf("%w: job %s generation %d is %s", runner.ErrProcessActive, f.JobID, f.Generation, cur.State)
	}
	if err != nil {
		return fmt.Errorf("builtin process: permit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("builtin process: commit: %w", err)
	}
	return nil
}

func (r *BuiltInProcessRepo) Record(ctx context.Context, f runner.Fence, guardianPID int, startMarker string) error {
	if err := builtInFence(f); err != nil {
		return err
	}
	if guardianPID <= 1 || startMarker == "" {
		return fmt.Errorf("builtin process: record needs a guardian pid > 1 and a start marker (got %d, %q)", guardianPID, startMarker)
	}
	return r.transition(ctx, f, `UPDATE builtin_processes SET guardian_pid = ?, process_start_marker = ?, state = ? WHERE job_id = ? AND generation = ? AND state = ?`,
		guardianPID, startMarker, string(runner.ProcessRunning), uuidToString(f.JobID), f.Generation, string(runner.ProcessPermitted))
}

func (r *BuiltInProcessRepo) MarkStopped(ctx context.Context, f runner.Fence, from runner.BuiltInProcessState) error {
	if err := builtInFence(f); err != nil {
		return err
	}
	switch from {
	case runner.ProcessAbsent:
		return r.transition(ctx, f, `INSERT INTO builtin_processes(job_id, generation, guardian_pid, process_start_marker, state) VALUES (?, ?, 0, '', ?) ON CONFLICT(job_id, generation) DO NOTHING`,
			uuidToString(f.JobID), f.Generation, string(runner.ProcessStopped))
	case runner.ProcessPermitted, runner.ProcessRunning:
		return r.transition(ctx, f, `UPDATE builtin_processes SET state = ? WHERE job_id = ? AND generation = ? AND state = ?`,
			string(runner.ProcessStopped), uuidToString(f.JobID), f.Generation, string(from))
	default:
		return fmt.Errorf("builtin process: cannot stop from state %q", from)
	}
}

func (r *BuiltInProcessRepo) Get(ctx context.Context, f runner.Fence) (runner.BuiltInProcess, bool, error) {
	if err := builtInFence(f); err != nil {
		return runner.BuiltInProcess{}, false, err
	}
	return readBuiltInProcess(ctx, r.db, f)
}

// transition runs one compare-and-set statement; zero affected rows means the
// row was not in the expected state.
func (r *BuiltInProcessRepo) transition(ctx context.Context, f runner.Fence, query string, args ...any) error {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("builtin process: %s/%d: %w", f.JobID, f.Generation, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("builtin process: %s/%d: %w", f.JobID, f.Generation, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: job %s generation %d", runner.ErrProcessStateChanged, f.JobID, f.Generation)
	}
	return nil
}
