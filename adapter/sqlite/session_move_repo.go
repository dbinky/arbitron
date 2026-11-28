// adapter/sqlite/session_move_repo.go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

var _ runner.SessionMoveStore = (*SessionMoveRepo)(nil)

// SessionMoveRepo implements runner.SessionMoveStore over migration 045's
// session_moves (durable state columns: unstick 2026-11-24).
type SessionMoveRepo struct {
	db     *sql.DB
	now    func() time.Time
	ledger *RunnerLedgerRepo
}

// NewSessionMoveRepo creates a SessionMoveRepo.
func NewSessionMoveRepo(db *sql.DB) *SessionMoveRepo {
	return &SessionMoveRepo{db: db, now: time.Now, ledger: NewRunnerLedgerRepo(db)}
}

// SetClock overrides the clock (tests).
func (r *SessionMoveRepo) SetClock(now func() time.Time) { r.now, r.ledger.now = now, now }

const moveStateColumns = `id, run_id, invocation_id, node_id, from_runner, to_runner, reason, lossy, notice, generation, at,
state, intent_id, owns_intent, execution_id, target_source, job_id, requirements, work_dir, resource_id, restore_workspace,
old_jobs, new_resource, failure`

const openMoveStates = `('preparing', 'restoring', 'importing')`

func optUUID(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

func fencesJSON(f []engine.JobFenceRef) (string, error) {
	if f == nil {
		f = []engine.JobFenceRef{}
	}
	b, err := json.Marshal(f)
	return string(b), err
}

func scanMoveState(s rowScanner) (runner.SessionMoveState, error) {
	var st runner.SessionMoveState
	var id, run, inv, state, intent, exec, source, job, reqs, resource, oldJobs, newRes string
	var lossy, owns, restore int
	var at int64
	if err := s.Scan(&id, &run, &inv, &st.Move.NodeID, &st.Move.FromRunner, &st.Move.ToRunner, &st.Move.Reason, &lossy,
		&st.Move.Notice, &st.Move.Generation, &at, &state, &intent, &owns, &exec, &source, &job, &reqs, &st.WorkDir,
		&resource, &restore, &oldJobs, &newRes, &st.Failure); err != nil {
		return st, err
	}
	for _, p := range []struct {
		text string
		dest *uuid.UUID
	}{{id, &st.Move.ID}, {run, &st.Move.RunID}, {inv, &st.Move.InvocationID}, {intent, &st.IntentID},
		{exec, &st.ExecutionID}, {job, &st.JobID}, {resource, &st.ResourceID}} {
		if p.text == "" {
			continue
		}
		parsed, err := uuid.Parse(p.text)
		if err != nil {
			return st, fmt.Errorf("session move: bad uuid %q: %w", p.text, err)
		}
		*p.dest = parsed
	}
	st.Move.Lossy, st.OwnsIntent, st.RestoreWorkspace = lossy == 1, owns == 1, restore == 1
	st.Move.At = time.UnixMilli(at).UTC()
	st.State, st.TargetSource = runner.MoveState(state), runner.SourceID(source)
	if err := json.Unmarshal([]byte(reqs), &st.Requirements); err != nil {
		return st, fmt.Errorf("session move: requirements: %w", err)
	}
	if err := json.Unmarshal([]byte(oldJobs), &st.OldJobs); err != nil {
		return st, fmt.Errorf("session move: old jobs: %w", err)
	}
	if newRes != "" {
		var ref engine.ResourceFenceRef
		if err := json.Unmarshal([]byte(newRes), &ref); err != nil {
			return st, fmt.Errorf("session move: restore claim: %w", err)
		}
		st.NewResource = &ref
	}
	return st, nil
}

func moveForIntentTx(ctx context.Context, q groupQueryer, intentID uuid.UUID) (runner.SessionMoveState, bool, error) {
	st, err := scanMoveState(q.QueryRowContext(ctx, `SELECT `+moveStateColumns+` FROM session_moves WHERE intent_id = ?
ORDER BY CASE WHEN state IN `+openMoveStates+` THEN 0 ELSE 1 END, updated_at DESC, at DESC LIMIT 1`, intentID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return runner.SessionMoveState{}, false, nil
	}
	if err != nil {
		return runner.SessionMoveState{}, false, fmt.Errorf("session move: read move of intent %s: %w", intentID, err)
	}
	return st, true, nil
}

// MoveForIntent implements runner.SessionMoveStore.
func (r *SessionMoveRepo) MoveForIntent(ctx context.Context, intentID uuid.UUID) (runner.SessionMoveState, bool, error) {
	return moveForIntentTx(ctx, r.db, intentID)
}

// BeginMove implements runner.SessionMoveStore.
func (r *SessionMoveRepo) BeginMove(ctx context.Context, b runner.SessionMoveBegin) (runner.SessionMoveState, error) {
	st := b.State
	switch {
	case st.Move.ID == uuid.Nil || st.IntentID == uuid.Nil || st.JobID == uuid.Nil || st.Move.Generation < 1:
		return st, errors.New("session move: begin needs move, intent and job ids and a planned generation")
	case st.State != runner.MoveStatePreparing:
		return st, errors.New("session move: a move begins in state preparing")
	case b.Intent != nil && (!st.OwnsIntent || b.Intent.ID != st.IntentID || b.Intent.Kind != engine.TurnIntentMove):
		return st, errors.New("session move: only the move's own kind=move intent is persisted with it")
	case b.Intent == nil && st.OwnsIntent:
		return st, errors.New("session move: an owned intent must be persisted with its move")
	}
	reqs, err := json.Marshal(st.Requirements)
	if err != nil {
		return st, fmt.Errorf("session move: encode requirements: %w", err)
	}
	oldJobs, err := fencesJSON(st.OldJobs)
	if err != nil {
		return st, fmt.Errorf("session move: encode old jobs: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return st, fmt.Errorf("session move: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, ok, err := moveForIntentTx(ctx, tx, st.IntentID)
	switch {
	case err != nil:
		return st, err
	case ok && cur.Move.ID == st.Move.ID:
		return cur, nil
	case ok && cur.State.Open():
		return st, fmt.Errorf("%w: intent %s already has open move %s", engine.ErrTurnFenced, st.IntentID, cur.Move.ID)
	}
	now := r.now().UTC()
	if b.Intent != nil {
		if err := persistTurnIntentTx(ctx, tx, *b.Intent, now); err != nil {
			return st, fmt.Errorf("session move: persist move intent: %w", err)
		}
	}
	in, err := loadIntentTx(ctx, tx, st.IntentID)
	if err != nil {
		return st, err
	}
	if in.completed || in.runID != st.Move.RunID.String() || in.invocationID != st.Move.InvocationID.String() {
		return st, fmt.Errorf("%w: intent %s cannot own a move of this session", engine.ErrTurnFenced, st.IntentID)
	}
	if st.Move.At.IsZero() {
		st.Move.At = now
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_moves (`+moveStateColumns+`, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?, 'preparing', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?)`,
		st.Move.ID.String(), st.Move.RunID.String(), st.Move.InvocationID.String(), st.Move.NodeID, st.Move.FromRunner,
		st.Move.ToRunner, st.Move.Reason, st.Move.Generation, st.Move.At.UTC().UnixMilli(),
		st.IntentID.String(), boolToInt(st.OwnsIntent), optUUID(st.ExecutionID), string(st.TargetSource), st.JobID.String(),
		string(reqs), st.WorkDir, optUUID(st.ResourceID), boolToInt(st.RestoreWorkspace), oldJobs, now.UnixMilli()); err != nil {
		return st, fmt.Errorf("session move: insert move row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return st, fmt.Errorf("session move: commit begin: %w", err)
	}
	return st, nil
}

// AdvanceMove implements runner.SessionMoveStore.
func (r *SessionMoveRepo) AdvanceMove(ctx context.Context, st runner.SessionMoveState) error {
	if !st.State.Open() {
		return errors.New("session move: advance only to preparing, restoring or importing; commit and fail close a move")
	}
	oldJobs, err := fencesJSON(st.OldJobs)
	if err != nil {
		return fmt.Errorf("session move: encode old jobs: %w", err)
	}
	newRes := ""
	if st.NewResource != nil {
		b, err := json.Marshal(*st.NewResource)
		if err != nil {
			return fmt.Errorf("session move: encode restore claim: %w", err)
		}
		newRes = string(b)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE session_moves
SET state = ?, generation = ?, job_id = ?, old_jobs = ?, new_resource = ?, updated_at = ?
WHERE id = ? AND state IN `+openMoveStates+` AND generation <= ?`,
		string(st.State), st.Move.Generation, st.JobID.String(), oldJobs, newRes, r.now().UTC().UnixMilli(),
		st.Move.ID.String(), st.Move.Generation)
	if err != nil {
		return fmt.Errorf("session move: advance: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("%w: move %s is unknown, closed or would lower its generation", engine.ErrTurnFenced, st.Move.ID)
	}
	return nil
}

// FailMove implements runner.SessionMoveStore.
func (r *SessionMoveRepo) FailMove(ctx context.Context, moveID uuid.UUID, cause string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("session move: begin fail: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := scanMoveState(tx.QueryRowContext(ctx, `SELECT `+moveStateColumns+` FROM session_moves WHERE id = ?`, moveID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("session move %s: %w", moveID, instance.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("session move: read: %w", err)
	}
	switch {
	case st.State == runner.MoveStateFailed:
		return nil
	case !st.State.Open():
		return fmt.Errorf("%w: move %s is already %s", engine.ErrTurnFenced, moveID, st.State)
	}
	now := r.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE session_moves SET state = 'failed', failure = ?, updated_at = ? WHERE id = ?`,
		cause, now.UnixMilli(), moveID.String()); err != nil {
		return fmt.Errorf("session move: fail: %w", err)
	}
	if st.OwnsIntent {
		if err := completeIntentTx(ctx, tx, st.IntentID, now); err != nil {
			return fmt.Errorf("session move: close move intent: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("session move: commit fail: %w", err)
	}
	return nil
}

func checkMoveLeaseTx(ctx context.Context, tx *sql.Tx, l engine.TurnLease, gen int64) error {
	var token sql.NullString
	var cur int64
	err := tx.QueryRowContext(ctx, `SELECT token, generation FROM turn_leases WHERE key = ?`, l.Key).Scan(&token, &cur)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!token.Valid || token.String != l.Token.String() || cur != gen)) {
		return fmt.Errorf("%w: lease %s is not held at generation %d by this mover", engine.ErrTurnFenced, l.Key, gen)
	}
	if err != nil {
		return fmt.Errorf("session move: read lease: %w", err)
	}
	return nil
}

// CommitMove implements runner.SessionMoveStore (see the port for the contract).
func (r *SessionMoveRepo) CommitMove(ctx context.Context, c runner.SessionMoveCommit) error {
	if c.Move.ID == uuid.Nil || c.IntentID == uuid.Nil || c.Lease.Token == uuid.Nil || c.InvocationID == uuid.Nil {
		return fmt.Errorf("%w: move, intent, lease token and invocation are required", engine.ErrTurnFenced)
	}
	if len(c.Completions) != 1 {
		return fmt.Errorf("%w: a move commits exactly its reserved import job", engine.ErrInvalidStepCompletion)
	}
	f := c.Completions[0].JobFence
	if err := f.Validate(); err != nil {
		return err
	}
	if c.Move.At.IsZero() {
		c.Move.At = r.now().UTC()
	}
	patch, err := json.Marshal(map[string]any{
		engine.ConversationKeySessionID:         c.SessionID,
		engine.ConversationKeyPendingRebuild:    c.PendingRebuild,
		engine.ConversationKeyProvisionalResume: c.Provisional,
	})
	if err != nil {
		return fmt.Errorf("session move: encode conversation patch: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("session move: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	st, err := scanMoveState(tx.QueryRowContext(ctx, `SELECT `+moveStateColumns+` FROM session_moves WHERE id = ?`, c.Move.ID.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: move %s was never begun", engine.ErrTurnFenced, c.Move.ID)
	}
	if err != nil {
		return fmt.Errorf("session move: read: %w", err)
	}
	if st.State == runner.MoveStateCommitted {
		var conversation string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(llm_conversation, '{}') FROM node_executions WHERE id = ?`, st.ExecutionID.String()).Scan(&conversation); err != nil {
			return fmt.Errorf("session move: read committed outcome: %w", err)
		}
		var values map[string]any
		if err := json.Unmarshal([]byte(conversation), &values); err != nil {
			return fmt.Errorf("session move: read committed conversation: %w", err)
		}
		if st.IntentID == c.IntentID && st.ExecutionID == c.ExecutionID && st.Move.RunID == c.Move.RunID &&
			st.Move.InvocationID == c.InvocationID && st.Move.InvocationID == c.Move.InvocationID &&
			st.Move.ToRunner == c.Move.ToRunner && st.Move.Generation == c.Move.Generation &&
			st.JobID == f.JobID && f.RunnerID == st.Move.ToRunner &&
			st.Move.Lossy == c.Move.Lossy && st.Move.Notice == c.Move.Notice &&
			values[engine.ConversationKeySessionID] == c.SessionID &&
			values[engine.ConversationKeyPendingRebuild] == c.PendingRebuild &&
			values[engine.ConversationKeyProvisionalResume] == c.Provisional {
			return nil
		}
		return fmt.Errorf("%w: move %s already committed a different outcome", engine.ErrTurnFenced, c.Move.ID)
	}
	switch {
	case !st.State.Open():
		return fmt.Errorf("%w: move %s is %s", engine.ErrTurnFenced, c.Move.ID, st.State)
	case st.IntentID != c.IntentID, st.ExecutionID != c.ExecutionID, st.Move.RunID != c.Move.RunID,
		st.Move.InvocationID != c.InvocationID, st.Move.ToRunner != c.Move.ToRunner, st.JobID != f.JobID,
		f.RunnerID != c.Move.ToRunner, c.Lease.Generation != c.Move.Generation, st.Move.Generation > c.Move.Generation:
		return fmt.Errorf("%w: commit does not match persisted move %s", engine.ErrTurnFenced, c.Move.ID)
	}
	in, err := loadIntentTx(ctx, tx, c.IntentID)
	if err != nil {
		return err
	}
	if in.completed || in.leaseKey != c.Lease.Key || in.runID != c.Move.RunID.String() ||
		in.invocationID != c.InvocationID.String() || in.generation > c.Move.Generation {
		return fmt.Errorf("%w: intent %s does not own this move", engine.ErrTurnFenced, c.IntentID)
	}
	if err := checkMoveLeaseTx(ctx, tx, c.Lease, c.Move.Generation); err != nil {
		return err
	}
	cur, err := loadSessionTx(ctx, tx, c.ExecutionID)
	if err != nil {
		return fmt.Errorf("session move: read prior pair: %w", err)
	}
	if err := checkPriorPairTx(ctx, tx, engine.TurnCommit{Execution: &instance.NodeExecution{ID: c.ExecutionID},
		InvocationID: c.InvocationID, SessionGeneration: c.Move.Generation}, in, cur); err != nil {
		return err
	}
	var jobRun, jobInvocation, jobNode, jobOp, jobSource string
	err = tx.QueryRowContext(ctx, `SELECT run_id, invocation_id, node_id, op, source_id FROM runner_jobs WHERE `+fencedWhere,
		f.JobID.String(), f.RunnerID, f.Generation).Scan(&jobRun, &jobInvocation, &jobNode, &jobOp, &jobSource)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (jobRun != c.Move.RunID.String() || jobInvocation != c.InvocationID.String() ||
		jobNode != st.Move.NodeID || jobOp != string(runner.OpSessionImport) || jobSource != string(st.TargetSource))) {
		return fmt.Errorf("%w: import job %s generation %d is not live on %s", engine.ErrTurnFenced, f.JobID, f.Generation, f.RunnerID)
	}
	if err != nil {
		return fmt.Errorf("session move: read import job: %w", err)
	}
	switch {
	case st.RestoreWorkspace && st.NewResource == nil:
		return fmt.Errorf("%w: workspace move has no restore claim", engine.ErrTurnFenced)
	case st.NewResource == nil && c.Resource != (engine.ResourceFenceRef{}):
		return fmt.Errorf("%w: move %s holds no restore claim", engine.ErrTurnFenced, c.Move.ID)
	case st.NewResource != nil && c.Resource != *st.NewResource:
		return fmt.Errorf("%w: restore claim is not the move's own", engine.ErrTurnFenced)
	case st.NewResource != nil:
		if err := checkResourceFenceTx(ctx, tx, c.Resource); err != nil {
			return fmt.Errorf("%w: %w", engine.ErrTurnFenced, err)
		}
		var holder string
		if err := tx.QueryRowContext(ctx, `SELECT holder FROM workspace_leases WHERE resource_key = ?`, c.Resource.Key).Scan(&holder); err != nil {
			return fmt.Errorf("session move: read restore holder: %w", err)
		}
		if holder != "session-move:"+st.Move.ID.String() {
			return fmt.Errorf("%w: restore claim belongs to another operation", engine.ErrTurnFenced)
		}
	}
	if err := recordSessionMoveTx(ctx, tx, c.ExecutionID, c.Move); err != nil {
		return fmt.Errorf("session move: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_executions
SET llm_conversation = json_patch(COALESCE(llm_conversation, '{}'), ?) WHERE id = ?`,
		string(patch), uuidToString(c.ExecutionID)); err != nil {
		return fmt.Errorf("session move: patch conversation: %w", err)
	}
	fence := runner.Fence{JobID: f.JobID, RunnerID: runner.RunnerID(f.RunnerID), Generation: f.Generation}
	if err := r.ledger.fencedUpdate(ctx, tx, fence, `state = 'completed', result_ref = ?, lease_until = NULL`, ""); err != nil {
		return fmt.Errorf("%w: complete import job %s: %v", engine.ErrTurnFenced, f.JobID, err)
	}
	if err := r.ledger.closeAttempt(ctx, tx, fence, "completed"); err != nil {
		return err
	}
	if st.NewResource != nil {
		if err := releaseResourceTx(ctx, tx, c.Resource); err != nil {
			return fmt.Errorf("%w: %w", engine.ErrTurnFenced, err)
		}
	}
	if st.OwnsIntent {
		if err := completeIntentTx(ctx, tx, c.IntentID, r.now()); err != nil {
			return fmt.Errorf("session move: close move intent: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("session move: commit: %w", err)
	}
	return nil
}
