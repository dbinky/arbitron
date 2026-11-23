package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ instance.SessionStateStore = (*NodeExecutionRepo)(nil)

// sqlExecQuerier is satisfied by *sql.DB and *sql.Tx so the same fenced
// statements run standalone or inside a turn-commit transaction.
type sqlExecQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// setSessionStateTx writes the session columns iff st.Generation is not older
// than the stored generation.
func setSessionStateTx(ctx context.Context, q sqlExecQuerier, execID uuid.UUID, st instance.SessionState) error {
	res, err := q.ExecContext(ctx, `UPDATE node_executions
SET session_invocation_id = ?, session_checkpoint_ref = ?, session_pin_runner = ?, session_generation = ?
WHERE id = ? AND session_generation <= ?`,
		uuidToString(st.InvocationID), ptrToNullString(st.CheckpointRef), ptrToNullString(st.PinRunner), st.Generation, uuidToString(execID), st.Generation)
	if err != nil {
		return fmt.Errorf("setting session state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("setting session state: %w", err)
	}
	if n == 1 {
		return nil
	}
	return missingOrStale(ctx, q, execID)
}

func missingOrStale(ctx context.Context, q sqlExecQuerier, execID uuid.UUID) error {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_executions WHERE id = ?`, uuidToString(execID)).Scan(&count); err != nil {
		return fmt.Errorf("checking node execution %s: %w", execID, err)
	}
	if count == 0 {
		return fmt.Errorf("node execution %s: %w", execID, instance.ErrNotFound)
	}
	return fmt.Errorf("node execution %s: %w", execID, instance.ErrStaleSessionGeneration)
}

// SetSessionState implements instance.SessionStateStore.
func (r *NodeExecutionRepo) SetSessionState(ctx context.Context, execID uuid.UUID, st instance.SessionState) error {
	return setSessionStateTx(ctx, r.db, execID, st)
}

// RecordSessionMove implements instance.SessionStateStore: the pin/generation
// update and the move row commit together or not at all.
func (r *NodeExecutionRepo) RecordSessionMove(ctx context.Context, execID uuid.UUID, m instance.SessionMoveRecord) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE node_executions SET session_pin_runner = ?, session_generation = ?, session_invocation_id = ?
WHERE id = ? AND instance_id = ? AND node_id = ? AND session_generation < ?
AND (session_invocation_id IS NULL OR session_invocation_id = ? OR session_invocation_id = ?)`, m.ToRunner, m.Generation, uuidToString(m.InvocationID), uuidToString(execID), uuidToString(m.RunID), m.NodeID, m.Generation, uuidToString(uuid.Nil), uuidToString(m.InvocationID))
	if err != nil {
		return fmt.Errorf("recording session move: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("recording session move: %w", err)
	}
	if n != 1 {
		return missingOrStale(ctx, tx, execID)
	}
	lossy := 0
	if m.Lossy {
		lossy = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_moves
(id, run_id, invocation_id, node_id, from_runner, to_runner, reason, lossy, notice, generation, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuidToString(m.ID), uuidToString(m.RunID), uuidToString(m.InvocationID), m.NodeID, m.FromRunner, m.ToRunner,
		m.Reason, lossy, m.Notice, m.Generation, m.At.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("inserting session move: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session move: %w", err)
	}
	return nil
}

// LatestSessionState selects the newest visit; retry counters reset on back-edges.
func (r *NodeExecutionRepo) LatestSessionState(ctx context.Context, runID uuid.UUID, nodeID string) (instance.SessionState, bool, error) {
	var inv, ref, pin sql.NullString
	var gen int64
	err := r.db.QueryRowContext(ctx, `SELECT session_invocation_id, session_checkpoint_ref, session_pin_runner, session_generation
FROM node_executions WHERE instance_id = ? AND node_id = ? ORDER BY started_at DESC, attempt DESC, rowid DESC LIMIT 1`,
		uuidToString(runID), nodeID).Scan(&inv, &ref, &pin, &gen)
	if errors.Is(err, sql.ErrNoRows) {
		return instance.SessionState{}, false, nil
	}
	if err != nil {
		return instance.SessionState{}, false, fmt.Errorf("reading session state: %w", err)
	}
	var invocationID uuid.UUID
	if inv.Valid {
		invocationID, err = parseUUID(inv.String)
		if err != nil {
			return instance.SessionState{}, false, err
		}
	}
	return instance.SessionState{InvocationID: invocationID, CheckpointRef: nullStringToPtr(ref), PinRunner: nullStringToPtr(pin), Generation: gen}, true, nil
}

// attachSessionState fills the session columns and move history on execs (all
// records of instanceID). Moves are joined by exact invocation, never timestamps.
func (r *NodeExecutionRepo) attachSessionState(ctx context.Context, instanceID uuid.UUID, execs []*instance.NodeExecution) error {
	if len(execs) == 0 {
		return nil
	}
	byID := make(map[uuid.UUID]*instance.NodeExecution, len(execs))
	for _, e := range execs {
		byID[e.ID] = e
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, session_invocation_id, session_checkpoint_ref, session_pin_runner, session_generation
FROM node_executions WHERE instance_id = ?`, uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("reading session columns: %w", err)
	}
	for rows.Next() {
		var idStr string
		var inv, ref, pin sql.NullString
		var gen int64
		if err := rows.Scan(&idStr, &inv, &ref, &pin, &gen); err != nil {
			rows.Close()
			return fmt.Errorf("scanning session columns: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			rows.Close()
			return err
		}
		if e, ok := byID[id]; ok {
			if inv.Valid {
				e.SessionInvocationID, err = parseUUID(inv.String)
				if err != nil {
					rows.Close()
					return err
				}
			}
			e.SessionCheckpointRef = nullStringToPtr(ref)
			e.SessionPinRunner = nullStringToPtr(pin)
			e.SessionGeneration = gen
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterating session columns: %w", err)
	}
	rows.Close()

	mrows, err := r.db.QueryContext(ctx, `SELECT id, run_id, invocation_id, node_id, from_runner, to_runner, reason, lossy, notice, generation, at
FROM session_moves WHERE run_id = ? ORDER BY at, generation`, uuidToString(instanceID))
	if err != nil {
		return fmt.Errorf("reading session moves: %w", err)
	}
	defer mrows.Close()
	for mrows.Next() {
		var idStr, runStr, invStr string
		var m instance.SessionMoveRecord
		var lossy int
		var atMs int64
		if err := mrows.Scan(&idStr, &runStr, &invStr, &m.NodeID, &m.FromRunner, &m.ToRunner, &m.Reason, &lossy, &m.Notice, &m.Generation, &atMs); err != nil {
			return fmt.Errorf("scanning session move: %w", err)
		}
		if m.ID, err = parseUUID(idStr); err != nil {
			return err
		}
		if m.RunID, err = parseUUID(runStr); err != nil {
			return err
		}
		if m.InvocationID, err = parseUUID(invStr); err != nil {
			return err
		}
		m.Lossy = lossy == 1
		m.At = time.UnixMilli(atMs).UTC()
		var owner *instance.NodeExecution
		for _, e := range execs {
			if e.NodeID != m.NodeID || e.SessionInvocationID != m.InvocationID {
				continue
			}
			owner = e
			break
		}
		if owner != nil {
			owner.SessionMoves = append(owner.SessionMoves, m)
		}
	}
	return mrows.Err()
}
