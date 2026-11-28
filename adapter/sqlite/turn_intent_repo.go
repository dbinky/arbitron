package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// TurnIntentRepo stores durable resend payloads, with millisecond timestamps.
type TurnIntentRepo struct {
	db  *sql.DB
	now func() time.Time
}

var _ engine.TurnIntentStore = (*TurnIntentRepo)(nil)

func NewTurnIntentRepo(db *sql.DB) *TurnIntentRepo      { return &TurnIntentRepo{db: db, now: time.Now} }
func (r *TurnIntentRepo) SetClock(now func() time.Time) { r.now = now }

const turnIntentColumns = `id, request_id, accepted_binding, lease_key, run_id, invocation_id, node_id, turn_index, kind, prompt, system_prompt, nonce, message, model, tools, prior_checkpoint_ref, session_generation, operation_id, created_at, completed_at`

func (r *TurnIntentRepo) Persist(ctx context.Context, in engine.TurnIntent) error {
	return persistTurnIntentTx(ctx, r.db, in, r.now())
}

// persistTurnIntentTx lets a move row and its resend intent commit together.
func persistTurnIntentTx(ctx context.Context, q groupQueryer, in engine.TurnIntent, now time.Time) error {
	if in.ID == uuid.Nil || in.RequestID == uuid.Nil || in.CompletedAt != nil {
		return errors.New("turn intent: id and request id required; new intents must be pending")
	}
	binding, err := json.Marshal(in.Binding)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(in.Tools)
	if err != nil {
		return err
	}
	created := in.CreatedAt
	if created.IsZero() {
		created = now
	}
	res, err := q.ExecContext(ctx, `INSERT INTO turn_intents (`+turnIntentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL) ON CONFLICT(request_id) DO NOTHING`,
		in.ID.String(), in.RequestID.String(), string(binding), in.LeaseKey, in.RunID.String(), in.InvocationID.String(), in.NodeID, in.TurnIndex, in.Kind, in.Prompt, in.SystemPrompt, in.Nonce, in.Message, in.Model, string(tools), in.PriorCheckpointRef, in.SessionGeneration, in.OperationID, created.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("persisting turn intent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	existing, err := scanTurnIntent(q.QueryRowContext(ctx, `SELECT `+turnIntentColumns+` FROM turn_intents WHERE request_id = ?`, in.RequestID.String()))
	if err != nil {
		return err
	}
	// Persistence timestamps are not part of the immutable accepted payload.
	existing.CreatedAt, in.CreatedAt = time.Time{}, time.Time{}
	existing.CompletedAt = nil
	if !reflect.DeepEqual(existing, in) {
		return errors.New("turn intent: request id already has a different accepted payload")
	}
	return nil
}

// completeIntentTx is shared with the atomic turn-commit transaction.
func completeIntentTx(ctx context.Context, q sqlExecQuerier, id uuid.UUID, at time.Time) error {
	res, err := q.ExecContext(ctx, `UPDATE turn_intents SET completed_at = COALESCE(completed_at, ?) WHERE id = ?`, at.UTC().UnixMilli(), id.String())
	if err != nil {
		return fmt.Errorf("completing turn intent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("turn intent: %w", instance.ErrNotFound)
	}
	return nil
}
func (r *TurnIntentRepo) Complete(ctx context.Context, id uuid.UUID) error {
	return completeIntentTx(ctx, r.db, id, r.now())
}
func (r *TurnIntentRepo) Pending(ctx context.Context) ([]engine.TurnIntent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+turnIntentColumns+` FROM turn_intents WHERE completed_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []engine.TurnIntent
	for rows.Next() {
		in, err := scanTurnIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}
func (r *TurnIntentRepo) PendingFor(ctx context.Context, key string) (*engine.TurnIntent, error) {
	in, err := scanTurnIntent(r.db.QueryRowContext(ctx, `SELECT `+turnIntentColumns+` FROM turn_intents WHERE completed_at IS NULL AND lease_key = ? ORDER BY created_at, id LIMIT 1`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}
func scanTurnIntent(s rowScanner) (engine.TurnIntent, error) {
	var in engine.TurnIntent
	var id, request, run, inv, binding, tools string
	var created int64
	var completed sql.NullInt64
	err := s.Scan(&id, &request, &binding, &in.LeaseKey, &run, &inv, &in.NodeID, &in.TurnIndex, &in.Kind, &in.Prompt, &in.SystemPrompt, &in.Nonce, &in.Message, &in.Model, &tools, &in.PriorCheckpointRef, &in.SessionGeneration, &in.OperationID, &created, &completed)
	if err != nil {
		return in, err
	}
	for _, p := range []struct {
		text string
		dest *uuid.UUID
	}{{id, &in.ID}, {request, &in.RequestID}, {run, &in.RunID}, {inv, &in.InvocationID}} {
		*p.dest, err = uuid.Parse(p.text)
		if err != nil {
			return in, err
		}
	}
	if err = json.Unmarshal([]byte(binding), &in.Binding); err != nil {
		return in, err
	}
	if err = json.Unmarshal([]byte(tools), &in.Tools); err != nil {
		return in, err
	}
	in.CreatedAt = time.UnixMilli(created).UTC()
	if completed.Valid {
		at := time.UnixMilli(completed.Int64).UTC()
		in.CompletedAt = &at
	}
	return in, nil
}
