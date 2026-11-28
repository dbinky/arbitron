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

var _ engine.TurnCommitter = (*TurnCommitRepo)(nil)

// TurnCommitRepo commits a turn's conversation, session state, workspace
// candidate promotion, intent completion and job completion in ONE
// transaction, after validating lease, intent, prior pair, job and resource
// fences inside that same transaction (contracts §21).
type TurnCommitRepo struct {
	db     *sql.DB
	now    func() time.Time
	execs  *NodeExecutionRepo
	ledger *RunnerLedgerRepo
}

// NewTurnCommitRepo builds the committer over the server database.
func NewTurnCommitRepo(db *sql.DB) *TurnCommitRepo {
	return &TurnCommitRepo{db: db, now: time.Now, execs: NewNodeExecutionRepo(db), ledger: NewRunnerLedgerRepo(db)}
}

// SetClock overrides the clock (tests).
func (r *TurnCommitRepo) SetClock(now func() time.Time) { r.now, r.ledger.now = now, now }

type storedIntent struct {
	leaseKey, runID, invocationID, requestID, kind, prior string
	turnIndex                                             int
	generation                                            int64
	completed                                             bool
}

func loadIntentTx(ctx context.Context, tx *sql.Tx, id uuid.UUID) (storedIntent, error) {
	var in storedIntent
	var completed sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT lease_key, run_id, invocation_id, request_id, kind, prior_checkpoint_ref, turn_index, session_generation, completed_at
		FROM turn_intents WHERE id = ?`, id.String()).Scan(&in.leaseKey, &in.runID, &in.invocationID, &in.requestID, &in.kind, &in.prior,
		&in.turnIndex, &in.generation, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return in, fmt.Errorf("%w: intent %s not found", engine.ErrTurnFenced, id)
	}
	in.completed = completed.Valid
	return in, err
}

type storedSession struct {
	exists        bool
	ref, pin, inv string
	generation    int64
}

func loadSessionTx(ctx context.Context, tx *sql.Tx, execID uuid.UUID) (storedSession, error) {
	var s storedSession
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(session_checkpoint_ref, ''), COALESCE(session_pin_runner, ''), COALESCE(session_invocation_id, ''), session_generation
		FROM node_executions WHERE id = ?`, uuidToString(execID)).Scan(&s.ref, &s.pin, &s.inv, &s.generation)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	s.exists = err == nil
	return s, err
}

// CommitTurn implements engine.TurnCommitter.
func (r *TurnCommitRepo) CommitTurn(ctx context.Context, c engine.TurnCommit) error {
	if c.Execution == nil || c.IntentID == uuid.Nil || c.LeaseToken == uuid.Nil || c.InvocationID == uuid.Nil {
		return fmt.Errorf("%w: execution, intent, lease token and invocation are required", engine.ErrMissingTurnReceipt)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("turn commit: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	in, err := loadIntentTx(ctx, tx, c.IntentID)
	if err != nil {
		return err
	}
	cur, err := loadSessionTx(ctx, tx, c.Execution.ID)
	if err != nil {
		return fmt.Errorf("turn commit: read session: %w", err)
	}
	if in.completed {
		// Exact idempotent retry: the committed outcome is exactly this commit.
		if cur.exists && cur.ref == c.CheckpointRef && cur.inv == c.InvocationID.String() &&
			cur.generation == c.SessionGeneration && in.requestID == c.RequestID.String() {
			return nil
		}
		return fmt.Errorf("%w: intent %s already committed a different outcome", engine.ErrTurnFenced, c.IntentID)
	}
	if in.leaseKey != c.LeaseKey || in.runID != c.Execution.InstanceID.String() || in.invocationID != c.InvocationID.String() ||
		in.requestID != c.RequestID.String() || in.turnIndex != c.TurnIndex || in.generation > c.SessionGeneration {
		return fmt.Errorf("%w: intent %s does not match this turn", engine.ErrTurnFenced, c.IntentID)
	}
	if err := checkLeaseTx(ctx, tx, c); err != nil {
		return err
	}
	if err := checkPriorPairTx(ctx, tx, c, in, cur); err != nil {
		return err
	}
	operationOnly := in.kind == engine.TurnIntentEnd || in.kind == engine.TurnIntentAbandon
	switch {
	case operationOnly && (len(c.Completions) > 0 || c.CheckpointRef != cur.ref):
		return fmt.Errorf("%w: end/abandon commits the existing pair without a stream receipt", engine.ErrInvalidStepCompletion)
	case !operationOnly && len(c.Completions) == 0:
		return fmt.Errorf("%w: turn has no completion receipt", engine.ErrInvalidStepCompletion)
	}
	resourceRun := c.ResourceRunID
	if resourceRun == uuid.Nil {
		resourceRun = c.Execution.InstanceID
	}
	sawWorkspace := false
	for _, comp := range c.Completions {
		if err := checkJobFenceTx(ctx, tx, comp.JobFence, c); err != nil {
			return err
		}
		w := comp.Workspace
		if w == nil {
			continue
		}
		sawWorkspace = true
		if err := checkResourceFenceTx(ctx, tx, w.Resource); err != nil {
			return fmt.Errorf("%w: %w", engine.ErrTurnFenced, err)
		}
		f := runner.Fence{JobID: comp.JobFence.JobID, RunnerID: runner.RunnerID(comp.JobFence.RunnerID), Generation: comp.JobFence.Generation}
		if err := requireJobHolderTx(ctx, tx, w.Resource, f); err != nil {
			return err
		}
		runID := resourceRun
		if w.NonPortable {
			if w.Ref != "" {
				return fmt.Errorf("%w: non-portable receipt carries a catalog ref", engine.ErrInvalidStepCompletion)
			}
			if err := commitNonPortableTx(ctx, tx, runID, f, w.LocalRef, timeToString(r.now())); err != nil {
				return err
			}
			continue
		}
		if w.LocalRef != "" {
			return fmt.Errorf("%w: portable receipt carries a local ref", engine.ErrInvalidStepCompletion)
		}
		if err := promoteCandidateTx(ctx, tx, runID, runner.Fence{JobID: comp.JobFence.JobID, RunnerID: runner.RunnerID(comp.JobFence.RunnerID), Generation: comp.JobFence.Generation}, *w, timeToString(r.now())); err != nil {
			return err
		}
	}
	if c.RequireWorkspace && !sawWorkspace {
		return fmt.Errorf("%w: mutable turn has no workspace receipt", engine.ErrInvalidStepCompletion)
	}

	switch {
	case c.Create:
		err = insertNodeExecutionTx(ctx, tx, c.Execution)
	case c.RequireOpenSession:
		var written bool
		written, err = r.execs.updateIfSessionOpenTx(ctx, tx, c.Execution)
		if err == nil && !written {
			err = fmt.Errorf("session %s was closed during this turn: %w", c.Execution.ID, instance.ErrNotInteractiveWait)
		}
	default:
		err = r.execs.updateTx(ctx, tx, c.Execution)
	}
	if err != nil {
		return fmt.Errorf("turn commit: %w", err)
	}
	st := instance.SessionState{Generation: c.SessionGeneration, InvocationID: c.InvocationID}
	if c.CheckpointRef != "" {
		ref := c.CheckpointRef
		st.CheckpointRef = &ref
	}
	if c.PinRunner != "" {
		pin := c.PinRunner
		st.PinRunner = &pin
	}
	if err := setSessionStateTx(ctx, tx, c.Execution.ID, st); err != nil {
		return fmt.Errorf("turn commit: %w", err)
	}
	if err := completeIntentTx(ctx, tx, c.IntentID, r.now()); err != nil {
		return fmt.Errorf("turn commit: %w", err)
	}
	for _, comp := range c.Completions {
		f := runner.Fence{JobID: comp.JobFence.JobID, RunnerID: runner.RunnerID(comp.JobFence.RunnerID), Generation: comp.JobFence.Generation}
		if err := r.ledger.fencedUpdate(ctx, tx, f, `state = 'completed', result_ref = ?, lease_until = NULL`, c.CheckpointRef); err != nil {
			return fmt.Errorf("%w: complete job %s: %v", engine.ErrTurnFenced, f.JobID, err)
		}
		if err := r.ledger.closeAttempt(ctx, tx, f, "completed"); err != nil {
			return err
		}
		if comp.Workspace != nil {
			if err := releaseResourceTx(ctx, tx, comp.Workspace.Resource); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("turn commit: %w", err)
	}
	return nil
}

func checkLeaseTx(ctx context.Context, tx *sql.Tx, c engine.TurnCommit) error {
	var token sql.NullString
	var gen int64
	err := tx.QueryRowContext(ctx, `SELECT token, generation FROM turn_leases WHERE key = ?`, c.LeaseKey).Scan(&token, &gen)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!token.Valid || token.String != c.LeaseToken.String() || gen != c.SessionGeneration)) {
		return fmt.Errorf("%w: lease %s", engine.ErrTurnFenced, c.LeaseKey)
	}
	if err != nil {
		return fmt.Errorf("turn commit: read lease: %w", err)
	}
	return nil
}

// checkPriorPairTx is the monotonic-turn CAS: an update must start from exactly
// the pair the intent was accepted against; a create must be the visit's first.
func checkPriorPairTx(ctx context.Context, tx *sql.Tx, c engine.TurnCommit, in storedIntent, cur storedSession) error {
	if c.Create {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_executions WHERE session_invocation_id = ?`, c.InvocationID.String()).Scan(&n); err != nil {
			return err
		}
		if n > 0 || cur.exists || in.prior != "" {
			return fmt.Errorf("%w: invocation %s already has a committed session", engine.ErrTurnFenced, c.InvocationID)
		}
		return nil
	}
	if !cur.exists {
		return fmt.Errorf("turn commit: execution %s: %w", c.Execution.ID, instance.ErrNotFound)
	}
	if cur.generation > c.SessionGeneration {
		return fmt.Errorf("turn commit: %w", instance.ErrStaleSessionGeneration)
	}
	if cur.inv != "" && cur.inv != uuid.Nil.String() && cur.inv != c.InvocationID.String() {
		return fmt.Errorf("%w: session belongs to invocation %s", engine.ErrTurnFenced, cur.inv)
	}
	if cur.ref != in.prior {
		return fmt.Errorf("%w: prior checkpoint %q is not the accepted %q", engine.ErrTurnFenced, cur.ref, in.prior)
	}
	return nil
}

func checkJobFenceTx(ctx context.Context, tx *sql.Tx, f engine.JobFenceRef, c engine.TurnCommit) error {
	var run, inv string
	err := tx.QueryRowContext(ctx, `SELECT run_id, invocation_id FROM runner_jobs WHERE `+fencedWhere,
		f.JobID.String(), f.RunnerID, f.Generation).Scan(&run, &inv)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: job %s generation %d is not live on %s", engine.ErrTurnFenced, f.JobID, f.Generation, f.RunnerID)
	}
	if err != nil {
		return err
	}
	if run != c.Execution.InstanceID.String() || inv != c.InvocationID.String() {
		return fmt.Errorf("%w: job %s belongs to another run/invocation", engine.ErrTurnFenced, f.JobID)
	}
	return nil
}
