package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ engine.RetryProgressStore = (*StepCommitRepo)(nil)

// LoadRetryProgress returns the persisted retry position of an invocation;
// an invocation with no journaled failure is at attempt 1.
func (r *StepCommitRepo) LoadRetryProgress(ctx context.Context, invocationID uuid.UUID) (engine.RetryProgress, error) {
	var runStr string
	err := r.db.QueryRowContext(ctx, `SELECT run_id FROM node_invocations WHERE id = ?`, uuidToString(invocationID)).Scan(&runStr)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.RetryProgress{}, fmt.Errorf("%w: %s", engine.ErrInvocationNotFound, invocationID)
	}
	if err != nil {
		return engine.RetryProgress{}, fmt.Errorf("reading invocation: %w", err)
	}
	p := engine.RetryProgress{InvocationID: invocationID, Attempt: 1}
	var notBefore, lastExec sql.NullString
	err = r.db.QueryRowContext(ctx,
		`SELECT attempt, retry_not_before, exhausted, last_execution_id FROM node_retry_progress WHERE invocation_id = ?`,
		uuidToString(invocationID)).Scan(&p.Attempt, &notBefore, &p.Exhausted, &lastExec)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return engine.RetryProgress{}, fmt.Errorf("reading retry progress: %w", err)
	}
	if p.RetryNotBefore, err = nullStringToTimePtr(notBefore); err != nil {
		return engine.RetryProgress{}, err
	}
	if lastExec.Valid {
		runID, err := parseUUID(runStr)
		if err != nil {
			return engine.RetryProgress{}, err
		}
		execID, err := parseUUID(lastExec.String)
		if err != nil {
			return engine.RetryProgress{}, err
		}
		if p.LastExecution, err = loadNodeExecution(ctx, r.db, runID, execID); err != nil {
			return engine.RetryProgress{}, err
		}
	}
	return p, nil
}

// CommitRetryFailure writes the failed execution, its failure receipt and the
// next retry position (attempt+1 with retryAt, or exhaustion when retryAt is
// nil) in one transaction, after a CAS on the exact invocation/run/node/attempt.
// An identical replay returns the current position; a conflict writes nothing.
func (r *StepCommitRepo) CommitRetryFailure(ctx context.Context, invocationID uuid.UUID, attempt int,
	x *instance.NodeExecution, retryAt *time.Time) (engine.RetryProgress, error) {
	if x == nil || x.ID == uuid.Nil || attempt < 1 || x.Attempt != attempt || x.Status != instance.NodeStatusFailed {
		return engine.RetryProgress{}, fmt.Errorf("%w: a retry failure needs the failed execution of attempt %d", engine.ErrRetryProgressConflict, attempt)
	}
	hash, err := retryPayloadHash(x, retryAt)
	if err != nil {
		return engine.RetryProgress{}, err
	}
	inv := uuidToString(invocationID)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// First statement takes the SQLite write lock before any read.
	if _, err := tx.ExecContext(ctx, `UPDATE node_invocations SET id = id WHERE id = ?`, inv); err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: lock: %w", err)
	}
	var runStr, nodeID string
	var committed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT run_id, node_id, committed_at FROM node_invocations WHERE id = ?`, inv).Scan(&runStr, &nodeID, &committed)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.RetryProgress{}, fmt.Errorf("%w: %s", engine.ErrInvocationNotFound, invocationID)
	}
	if err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: reading invocation: %w", err)
	}
	if runStr != uuidToString(x.InstanceID) || nodeID != x.NodeID || committed.Valid {
		return engine.RetryProgress{}, fmt.Errorf("%w: execution of %s/%q does not belong to open invocation %s (%s/%q)",
			engine.ErrRetryProgressConflict, x.InstanceID, x.NodeID, invocationID, runStr, nodeID)
	}
	var prevExec, prevHash string
	err = tx.QueryRowContext(ctx, `SELECT execution_id, payload_hash FROM node_retry_failures WHERE invocation_id = ? AND attempt = ?`,
		inv, attempt).Scan(&prevExec, &prevHash)
	switch {
	case err == nil:
		if prevExec == uuidToString(x.ID) && prevHash == hash {
			_ = tx.Rollback() // release the single connection before reading
			return r.LoadRetryProgress(ctx, invocationID)
		}
		return engine.RetryProgress{}, fmt.Errorf("%w: attempt %d of invocation %s is already journaled", engine.ErrRetryProgressConflict, attempt, invocationID)
	case !errors.Is(err, sql.ErrNoRows):
		return engine.RetryProgress{}, fmt.Errorf("retry journal: reading failure: %w", err)
	}
	cur, exhausted := 1, false
	err = tx.QueryRowContext(ctx, `SELECT attempt, exhausted FROM node_retry_progress WHERE invocation_id = ?`, inv).Scan(&cur, &exhausted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: reading progress: %w", err)
	}
	if exhausted || cur != attempt {
		return engine.RetryProgress{}, fmt.Errorf("%w: expected attempt %d, invocation %s is at %d (exhausted=%t)",
			engine.ErrRetryProgressConflict, attempt, invocationID, cur, exhausted)
	}
	if err := insertNodeExecutionTx(ctx, tx, x); err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_retry_failures (invocation_id, attempt, execution_id, payload_hash) VALUES (?, ?, ?, ?)`,
		inv, attempt, uuidToString(x.ID), hash); err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: recording failure: %w", err)
	}
	next, exh := attempt, 1
	if retryAt != nil {
		next, exh = attempt+1, 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_retry_progress (invocation_id, attempt, retry_not_before, exhausted, last_execution_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(invocation_id) DO UPDATE SET attempt = excluded.attempt, retry_not_before = excluded.retry_not_before,
			exhausted = excluded.exhausted, last_execution_id = excluded.last_execution_id`,
		inv, next, timeToNullString(retryAt), exh, uuidToString(x.ID)); err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: advancing progress: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return engine.RetryProgress{}, fmt.Errorf("retry journal: commit: %w", err)
	}
	return r.LoadRetryProgress(ctx, invocationID)
}

func retryPayloadHash(x *instance.NodeExecution, retryAt *time.Time) (string, error) {
	at := ""
	if retryAt != nil {
		at = timeToString(*retryAt)
	}
	b, err := json.Marshal(struct {
		Execution string
		Attempt   int
		Error     map[string]any
		Output    map[string]any
		RetryAt   string
	}{uuidToString(x.ID), x.Attempt, x.Error, x.Output, at})
	if err != nil {
		return "", fmt.Errorf("retry journal: hashing payload: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
