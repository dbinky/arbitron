package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Compile-time: satisfies the runner ledger port.
var _ runner.Ledger = (*RunnerLedgerRepo)(nil)

// RunnerLedgerRepo implements runner.Ledger over SQLite (migration 041).
type RunnerLedgerRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewRunnerLedgerRepo creates a RunnerLedgerRepo.
func NewRunnerLedgerRepo(db *sql.DB) *RunnerLedgerRepo {
	return &RunnerLedgerRepo{db: db, now: time.Now}
}

const jobColumns = `id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind, group_ref, group_family, ` +
	`model, runner_id, source_id, generation, state, lease_until, reason, wait, result_ref, checkpoint_ref, created_at, updated_at`

// fencedWhere is THE fence predicate every runner-originated mutation uses.
const fencedWhere = `id = ? AND runner_id = ? AND generation = ? AND state IN ('offered', 'running')`

func scanJob(s rowScanner) (runner.Job, error) {
	var j runner.Job
	var id, runID, invID, op, kind, ref, family, state, created, updated string
	var runnerID, sourceID, lease, wait sql.NullString
	if err := s.Scan(&id, &runID, &invID, &j.Key.OperationID, &j.Key.Attempt, &j.NodeID, &op, &kind, &ref, &family,
		&j.Model, &runnerID, &sourceID, &j.Generation, &state, &lease, &j.Reason, &wait, &j.ResultRef, &j.CheckpointRef,
		&created, &updated); err != nil {
		return runner.Job{}, err
	}
	var err error
	if j.ID, err = parseUUID(id); err != nil {
		return runner.Job{}, err
	}
	if j.Key.RunID, err = parseUUID(runID); err != nil {
		return runner.Job{}, err
	}
	if j.Key.InvocationID, err = parseUUID(invID); err != nil {
		return runner.Job{}, err
	}
	j.Op = runner.OpType(op)
	j.Group = runner.ModelGroup{Kind: runner.CredentialKind(kind), ConnectionRef: ref, Family: modelidentity.Family(family)}
	j.RunnerID, j.SourceID, j.State = runner.RunnerID(runnerID.String), runner.SourceID(sourceID.String), runner.JobState(state)
	if j.LeaseUntil, err = nullStringToTimePtr(lease); err != nil {
		return runner.Job{}, err
	}
	if wait.Valid && wait.String != "" {
		var w runner.WaitReason
		if err := json.Unmarshal([]byte(wait.String), &w); err != nil {
			return runner.Job{}, fmt.Errorf("decoding job wait: %w", err)
		}
		j.Wait = &w
	}
	if j.CreatedAt, err = parseTime(created); err != nil {
		return runner.Job{}, err
	}
	if j.UpdatedAt, err = parseTime(updated); err != nil {
		return runner.Job{}, err
	}
	return j, nil
}

func getJob(ctx context.Context, q groupQueryer, id uuid.UUID) (runner.Job, error) {
	j, err := scanJob(q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM runner_jobs WHERE id = ?`, uuidToString(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Job{}, fmt.Errorf("%w: %s", runner.ErrJobNotFound, id)
	}
	if err != nil {
		return runner.Job{}, fmt.Errorf("reading job: %w", err)
	}
	return j, nil
}

// fenceErr is THE single fence rule (also FenceCheck's body).
func fenceErr(ctx context.Context, q groupQueryer, f runner.Fence) error {
	j, err := getJob(ctx, q, f.JobID)
	if err != nil {
		return err
	}
	if !j.State.HoldsSlot() || j.Generation != f.Generation || j.RunnerID != f.RunnerID {
		return fmt.Errorf("%w: job %s state %s generation %d runner %q (fence generation %d runner %q)",
			runner.ErrFenced, f.JobID, j.State, j.Generation, j.RunnerID, f.Generation, f.RunnerID)
	}
	return nil
}

func listJobs(ctx context.Context, q groupQueryer, where string, args ...any) ([]runner.Job, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+jobColumns+` FROM runner_jobs WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close()
	var out []runner.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// EnsureIntent writes (or returns) the queued intent for key. A terminal job
// for the same key is re-opened as queued with generation+1 (Phase 1 D7
// re-execution); an active job is returned unchanged.
func (r *RunnerLedgerRepo) EnsureIntent(ctx context.Context, key runner.JobKey, nodeID string, req runner.ExecRequirements) (runner.Job, error) {
	requirements, err := json.Marshal(req)
	if err != nil {
		return runner.Job{}, fmt.Errorf("encoding execution requirements: %w", err)
	}
	if !key.Valid() {
		return runner.Job{}, fmt.Errorf("runner ledger: incomplete job key %+v", key)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.Job{}, fmt.Errorf("beginning intent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := timeToString(r.now())

	// Write first so the transaction holds the write lock before reading.
	res, err := tx.ExecContext(ctx, `UPDATE runner_jobs SET state = 'queued', generation = generation + 1, runner_id = NULL,
		source_id = NULL, lease_until = NULL, reason = '', wait = NULL, result_ref = '', updated_at = ?
		WHERE run_id = ? AND invocation_id = ? AND operation_id = ? AND attempt = ? AND state IN ('completed', 'failed', 'fenced')`,
		now, uuidToString(key.RunID), uuidToString(key.InvocationID), key.OperationID, key.Attempt)
	if err != nil {
		return runner.Job{}, fmt.Errorf("re-opening intent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op,
			group_kind, group_ref, group_family, model, requirements, generation, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'queued', ?, ?)
			ON CONFLICT(run_id, invocation_id, operation_id, attempt) DO NOTHING`,
			uuidToString(uuid.New()), uuidToString(key.RunID), uuidToString(key.InvocationID), key.OperationID, key.Attempt,
			nodeID, string(req.Op), string(req.Group.Kind), req.Group.ConnectionRef, string(req.Group.Family), req.Model, string(requirements), now, now); err != nil {
			return runner.Job{}, fmt.Errorf("writing intent: %w", err)
		}
	}
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM runner_jobs
		WHERE run_id = ? AND invocation_id = ? AND operation_id = ? AND attempt = ?`,
		uuidToString(key.RunID), uuidToString(key.InvocationID), key.OperationID, key.Attempt))
	if err != nil {
		return runner.Job{}, fmt.Errorf("reading intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return runner.Job{}, fmt.Errorf("committing intent: %w", err)
	}
	return j, nil
}

// SetWait records (or clears, w == nil) the wait reason of a queued intent.
func (r *RunnerLedgerRepo) SetWait(ctx context.Context, jobID uuid.UUID, w *runner.WaitReason) error {
	var wait sql.NullString
	if w != nil {
		b, err := json.Marshal(w)
		if err != nil {
			return fmt.Errorf("encoding wait: %w", err)
		}
		wait = sql.NullString{String: string(b), Valid: true}
	}
	res, err := r.db.ExecContext(ctx, `UPDATE runner_jobs SET wait = ?, updated_at = ? WHERE id = ? AND state = 'queued'`,
		wait, timeToString(r.now()), uuidToString(jobID))
	if err != nil {
		return fmt.Errorf("setting wait: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := getJob(ctx, r.db, jobID); err != nil {
		return err
	}
	return fmt.Errorf("%w: job %s is not queued", runner.ErrFenced, jobID)
}

// Reserve validates persisted requirements and current eligibility under a write
// lock before claiming a slot and recording its assignment attempt. The persisted
// runner limit is authoritative; the caller's limit may be stale.
func (r *RunnerLedgerRepo) Reserve(ctx context.Context, jobID uuid.UUID, d runner.Decision, limit int, leaseUntil time.Time) (runner.Job, error) {
	if !d.Placed || d.Runner == "" || limit < 1 {
		return runner.Job{}, errors.New("runner ledger: invalid reservation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// First statement obtains the SQLite write lock before reading eligibility.
	if _, err := tx.ExecContext(ctx, `UPDATE runner_jobs SET updated_at = updated_at WHERE id = ?`, uuidToString(jobID)); err != nil {
		return runner.Job{}, err
	}
	j, err := getJob(ctx, tx, jobID)
	if err != nil {
		return runner.Job{}, err
	}
	if j.State != runner.JobQueued {
		return runner.Job{}, runner.ErrFenced
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT requirements FROM runner_jobs WHERE id = ?`, uuidToString(jobID)).Scan(&raw); err != nil {
		return runner.Job{}, err
	}
	var req runner.ExecRequirements
	if err := json.Unmarshal([]byte(raw), &req); err != nil || req.Op == "" || req.Group.Kind == "" {
		return runner.Job{}, errors.New("runner ledger: invalid persisted requirements")
	}
	rn, err := scanRunner(tx.QueryRowContext(ctx, `SELECT `+runnerColumns+` FROM runners WHERE id = ?`, string(d.Runner)))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Job{}, runner.ErrSlotTaken
	}
	if err != nil {
		return runner.Job{}, err
	}
	var used, sourceUsed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE runner_id = ? AND state IN ('offered','running')`, string(d.Runner)).Scan(&used); err != nil {
		return runner.Job{}, err
	}
	snap := runner.PoolSnapshot{Runners: []runner.Runner{rn}, SlotsInUse: map[runner.RunnerID]int{rn.ID: used}, SourceInFlight: map[runner.SourceID]int{}, Now: r.now()}
	if req.Group.Kind != runner.CredNone {
		src, err := scanSource(tx.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM runner_sources WHERE id = ? AND runner_id = ?`, string(d.Source), string(d.Runner)))
		if errors.Is(err, sql.ErrNoRows) {
			return runner.Job{}, runner.ErrSlotTaken
		}
		if err != nil {
			return runner.Job{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_jobs WHERE source_id = ? AND state IN ('offered','running')`, string(d.Source)).Scan(&sourceUsed); err != nil {
			return runner.Job{}, err
		}
		snap.Sources, snap.SourceInFlight[d.Source] = []runner.Source{src}, sourceUsed
	}
	// Phase 1 affinities are built-in; P3 extends this snapshot with the resource home.
	validated := runner.Place(req, snap, runner.PlacementPolicy{})
	if !validated.Placed || validated.Runner != d.Runner || validated.Source != d.Source {
		return runner.Job{}, runner.ErrSlotTaken
	}
	// The caller's stale limit never overrides the persisted runner's StepLimit.
	now := timeToString(r.now())
	res, err := tx.ExecContext(ctx, `UPDATE runner_jobs SET state = 'offered', runner_id = ?, source_id = NULLIF(?, ''), reason = ?, generation = generation + 1, lease_until = ?, wait = NULL, updated_at = ? WHERE id = ? AND state = 'queued'`, string(d.Runner), string(d.Source), d.Reason, timeToString(leaseUntil), now, uuidToString(jobID))
	if err != nil {
		return runner.Job{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return runner.Job{}, runner.ErrFenced
	}
	j, err = getJob(ctx, tx, jobID)
	if err != nil {
		return runner.Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_job_attempts (job_id, generation, runner_id, source_id, reason, started_at) VALUES (?, ?, ?, ?, ?, ?)`, uuidToString(jobID), j.Generation, string(d.Runner), string(d.Source), d.Reason, now); err != nil {
		return runner.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return runner.Job{}, err
	}
	return j, nil
}

func (r *RunnerLedgerRepo) fencedUpdate(ctx context.Context, q groupQueryer, f runner.Fence, set string, args ...any) error {
	args = append(args, timeToString(r.now()), uuidToString(f.JobID), string(f.RunnerID), f.Generation)
	res, err := q.ExecContext(ctx, `UPDATE runner_jobs SET `+set+`, updated_at = ? WHERE `+fencedWhere, args...)
	if err != nil {
		return fmt.Errorf("updating job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if err := fenceErr(ctx, q, f); err != nil {
			return err
		}
		return fmt.Errorf("%w: job %s changed concurrently", runner.ErrFenced, f.JobID)
	}
	return nil
}

func (r *RunnerLedgerRepo) MarkRunning(ctx context.Context, f runner.Fence) error {
	return r.fencedUpdate(ctx, r.db, f, `state = 'running'`)
}

func (r *RunnerLedgerRepo) ExtendLease(ctx context.Context, f runner.Fence, until time.Time) error {
	return r.fencedUpdate(ctx, r.db, f, `lease_until = ?`, timeToString(until))
}

func (r *RunnerLedgerRepo) FenceCheck(ctx context.Context, f runner.Fence) error {
	return fenceErr(ctx, r.db, f)
}

// finish moves a live assignment to a terminal state and closes its attempt.
// Repeating it with the same fence on a job already in that state is a no-op.
func (r *RunnerLedgerRepo) finish(ctx context.Context, f runner.Fence, state runner.JobState, outcome, resultRef string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	err = r.fencedUpdate(ctx, tx, f, `state = ?, result_ref = ?, lease_until = NULL`, string(state), resultRef)
	if errors.Is(err, runner.ErrFenced) {
		j, gerr := getJob(ctx, tx, f.JobID)
		if gerr == nil && j.State == state && j.Generation == f.Generation && j.RunnerID == f.RunnerID {
			return nil // duplicate acknowledgement
		}
		return err
	}
	if err != nil {
		return err
	}
	if err := r.closeAttempt(ctx, tx, f, outcome); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing finish: %w", err)
	}
	return nil
}

func (r *RunnerLedgerRepo) closeAttempt(ctx context.Context, q groupQueryer, f runner.Fence, outcome string) error {
	if _, err := q.ExecContext(ctx, `UPDATE runner_job_attempts SET outcome = ?, ended_at = ?
		WHERE job_id = ? AND generation = ? AND ended_at IS NULL`,
		outcome, timeToString(r.now()), uuidToString(f.JobID), f.Generation); err != nil {
		return fmt.Errorf("closing attempt: %w", err)
	}
	return nil
}

func outcomeText(class runner.OutcomeClass) string {
	if class == runner.OutcomeNone {
		return string(runner.OutcomeOther)
	}
	return string(class)
}

func (r *RunnerLedgerRepo) Complete(ctx context.Context, f runner.Fence, resultRef string) error {
	return r.finish(ctx, f, runner.JobCompleted, "completed", resultRef)
}

func (r *RunnerLedgerRepo) Fail(ctx context.Context, f runner.Fence, class runner.OutcomeClass) error {
	return r.finish(ctx, f, runner.JobFailed, outcomeText(class), "")
}

// Requeue returns the job to queued with generation+1, releasing its slot and
// closing the attempt with class.
func (r *RunnerLedgerRepo) Requeue(ctx context.Context, f runner.Fence, class runner.OutcomeClass) (runner.Job, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return runner.Job{}, fmt.Errorf("beginning requeue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.fencedUpdate(ctx, tx, f, `state = 'queued', generation = generation + 1, runner_id = NULL, source_id = NULL,
		lease_until = NULL, reason = ''`); err != nil {
		return runner.Job{}, err
	}
	if err := r.closeAttempt(ctx, tx, f, outcomeText(class)); err != nil {
		return runner.Job{}, err
	}
	j, err := getJob(ctx, tx, f.JobID)
	if err != nil {
		return runner.Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return runner.Job{}, fmt.Errorf("committing requeue: %w", err)
	}
	return j, nil
}

func (r *RunnerLedgerRepo) Get(ctx context.Context, jobID uuid.UUID) (runner.Job, error) {
	return getJob(ctx, r.db, jobID)
}

func (r *RunnerLedgerRepo) FindByKey(ctx context.Context, key runner.JobKey) (runner.Job, bool, error) {
	j, err := scanJob(r.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM runner_jobs
		WHERE run_id = ? AND invocation_id = ? AND operation_id = ? AND attempt = ?`,
		uuidToString(key.RunID), uuidToString(key.InvocationID), key.OperationID, key.Attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.Job{}, false, nil
	}
	if err != nil {
		return runner.Job{}, false, fmt.Errorf("finding job: %w", err)
	}
	return j, true, nil
}

func (r *RunnerLedgerRepo) ListByRun(ctx context.Context, runID uuid.UUID) ([]runner.Job, error) {
	return listJobs(ctx, r.db, `run_id = ?`, uuidToString(runID))
}

func (r *RunnerLedgerRepo) ListActive(ctx context.Context) ([]runner.Job, error) {
	return listJobs(ctx, r.db, `state IN ('queued', 'offered', 'running')`)
}

func (r *RunnerLedgerRepo) ListActiveByRunner(ctx context.Context, id runner.RunnerID) ([]runner.Job, error) {
	return listJobs(ctx, r.db, `runner_id = ? AND state IN ('offered', 'running')`, string(id))
}

func (r *RunnerLedgerRepo) Attempts(ctx context.Context, jobID uuid.UUID) ([]runner.Attempt, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT job_id, generation, runner_id, source_id, reason, outcome, started_at, ended_at
		FROM runner_job_attempts WHERE job_id = ? ORDER BY generation`, uuidToString(jobID))
	if err != nil {
		return nil, fmt.Errorf("listing attempts: %w", err)
	}
	defer rows.Close()
	var out []runner.Attempt
	for rows.Next() {
		var a runner.Attempt
		var id, rid, sid, started string
		var ended sql.NullString
		if err := rows.Scan(&id, &a.Generation, &rid, &sid, &a.Reason, &a.Outcome, &started, &ended); err != nil {
			return nil, fmt.Errorf("scanning attempt: %w", err)
		}
		if a.JobID, err = parseUUID(id); err != nil {
			return nil, err
		}
		a.RunnerID, a.SourceID = runner.RunnerID(rid), runner.SourceID(sid)
		if a.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if a.EndedAt, err = nullStringToTimePtr(ended); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

var _ runner.IntentAbandoner = (*RunnerLedgerRepo)(nil)

// Abandon closes a queued intent (queued -> failed, wait cleared). Non-queued
// jobs are left unchanged; a missing job is ErrJobNotFound.
func (r *RunnerLedgerRepo) Abandon(ctx context.Context, jobID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx, `UPDATE runner_jobs SET state = 'failed', wait = NULL, updated_at = ? WHERE id = ? AND state = 'queued'`,
		timeToString(r.now()), uuidToString(jobID))
	if err != nil {
		return fmt.Errorf("abandoning intent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	_, err = getJob(ctx, r.db, jobID)
	return err
}
