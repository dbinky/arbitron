package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

var _ runner.ProbeCommitter = (*ProbeCommitRepo)(nil)

// ProbeCommitRepo completes synthetic quota-confirmation jobs under their
// exact ledger fence after validating same-model evidence.
type ProbeCommitRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewProbeCommitRepo creates a ProbeCommitRepo over the server database.
func NewProbeCommitRepo(db *sql.DB) *ProbeCommitRepo {
	return &ProbeCommitRepo{db: db, now: time.Now}
}

func probeResultRef(model string) string { return "probe:" + model }

// CommitProbe validates the evidence and the job (a "probe/" operation with a
// RequiredSource and the same requested model), then completes the job,
// records the durable result and closes its attempt, releasing the slot, in
// one transaction. Invalid evidence writes nothing.
func (r *ProbeCommitRepo) CommitProbe(ctx context.Context, fr engine.JobFenceRef, res runner.ProbeCompletion) error {
	if err := fr.Validate(); err != nil {
		return err
	}
	if err := res.Validate(); err != nil {
		return err
	}
	f := runner.Fence{JobID: fr.JobID, RunnerID: runner.RunnerID(fr.RunnerID), Generation: fr.Generation}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("probe commit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_jobs SET updated_at = updated_at WHERE id = ?`, uuidToString(f.JobID)); err != nil {
		return fmt.Errorf("probe commit: lock: %w", err)
	}
	if err := fenceErr(ctx, tx, f); err != nil {
		j, gerr := getJob(ctx, tx, f.JobID)
		if gerr == nil && j.State == runner.JobCompleted && j.Generation == f.Generation && j.RunnerID == f.RunnerID &&
			j.ResultRef == probeResultRef(res.ExecutedModel) {
			return nil // identical repeat
		}
		return fmt.Errorf("probe commit: %w", err)
	}
	j, err := getJob(ctx, tx, f.JobID)
	if err != nil {
		return err
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT requirements FROM runner_jobs WHERE id = ?`, uuidToString(f.JobID)).Scan(&raw); err != nil {
		return fmt.Errorf("probe commit: reading requirements: %w", err)
	}
	var req runner.ExecRequirements
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return fmt.Errorf("probe commit: decoding requirements: %w", err)
	}
	if !strings.HasPrefix(j.Key.OperationID, "probe/") || req.RequiredSource == "" {
		return fmt.Errorf("%w: job %s (%s) is not a synthetic probe", runner.ErrInvalidProbeCompletion, f.JobID, j.Key.OperationID)
	}
	if j.Model != res.RequestedModel {
		return fmt.Errorf("%w: job %s requested %q, the evidence names %q", runner.ErrInvalidProbeCompletion, f.JobID, j.Model, res.RequestedModel)
	}
	if err := completeJobTx(ctx, tx, f, probeResultRef(res.ExecutedModel), "", timeToString(r.now())); err != nil {
		return fmt.Errorf("probe commit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("probe commit: commit: %w", err)
	}
	return nil
}

// completeJobTx completes one live assignment under its exact fence and closes
// its attempt inside the caller's transaction (shared with StepCommitRepo).
func completeJobTx(ctx context.Context, tx groupQueryer, f runner.Fence, resultRef, checkpointRef, now string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE runner_jobs SET state = 'completed', result_ref = ?, checkpoint_ref = ?, lease_until = NULL, updated_at = ? WHERE `+fencedWhere,
		resultRef, checkpointRef, now, uuidToString(f.JobID), string(f.RunnerID), f.Generation)
	if err != nil {
		return fmt.Errorf("completing job %s: %w", f.JobID, err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: job %s changed concurrently", runner.ErrFenced, f.JobID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runner_job_attempts SET outcome = 'completed', ended_at = ? WHERE job_id = ? AND generation = ? AND ended_at IS NULL`,
		now, uuidToString(f.JobID), f.Generation); err != nil {
		return fmt.Errorf("closing attempt of job %s: %w", f.JobID, err)
	}
	return nil
}
