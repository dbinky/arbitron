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

	"github.com/arbitron/arbitron/domain/runner"
)

// RemoteInboxRepo stores receipts in the same database as the fenced ledger.
type RemoteInboxRepo struct{ db *sql.DB }

func NewRemoteInboxRepo(db *sql.DB) *RemoteInboxRepo { return &RemoteInboxRepo{db: db} }

var _ runner.RemoteInbox = (*RemoteInboxRepo)(nil)

func (r *RemoteInboxRepo) accept(ctx context.Context, f runner.Fence, kind string, seq uint64, payload any) (uint64, error) {
	if f.Generation < 1 || (kind == "output" && seq == 0) {
		return 0, runner.ErrMalformedMessage
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	hash := sha256.Sum256(b)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// A conditional write takes SQLite's write lock and checks authority in the
	// same transaction as the receipt. No check-then-write race with fencing.
	res, err := tx.ExecContext(ctx, `UPDATE runner_jobs SET generation=generation WHERE id=? AND runner_id=? AND generation=? AND state IN ('offered','running','completed','failed')`, f.JobID.String(), string(f.RunnerID), f.Generation)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, runner.ErrFenced
	}
	if kind == "output" {
		var cursor uint64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT last_output_seq FROM runner_inbox_cursors WHERE job_id=? AND generation=? AND runner_id=?),0)`, f.JobID.String(), f.Generation, string(f.RunnerID)).Scan(&cursor)
		if err != nil {
			return 0, err
		}
		// Gaps are not accepted: re-ACK the durable cursor to request replay.
		if seq > cursor+1 {
			return cursor, tx.Commit()
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_inbox(job_id,generation,runner_id,seq,kind,payload,payload_hash,received_at) VALUES(?,?,?,?,?,?,?,?)`, f.JobID.String(), f.Generation, string(f.RunnerID), seq, kind, b, hex.EncodeToString(hash[:]), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_inbox_cursors(job_id,generation,runner_id) VALUES(?,?,?) ON CONFLICT(job_id,generation) DO NOTHING`, f.JobID.String(), f.Generation, string(f.RunnerID))
	if err != nil {
		return 0, err
	}
	var cursor uint64
	err = tx.QueryRowContext(ctx, `SELECT last_output_seq FROM runner_inbox_cursors WHERE job_id=? AND generation=? AND runner_id=?`, f.JobID.String(), f.Generation, string(f.RunnerID)).Scan(&cursor)
	if err != nil {
		return 0, err
	}
	for {
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runner_inbox WHERE job_id=? AND generation=? AND kind='output' AND seq=?)`, f.JobID.String(), f.Generation, cursor+1).Scan(&exists)
		if err != nil {
			return 0, err
		}
		if !exists {
			break
		}
		cursor++
	}
	_, err = tx.ExecContext(ctx, `UPDATE runner_inbox_cursors SET last_output_seq=?, result_received=MAX(result_received,?) WHERE job_id=? AND generation=? AND runner_id=?`, cursor, kind == "result", f.JobID.String(), f.Generation, string(f.RunnerID))
	if err != nil {
		return 0, err
	}
	return cursor, tx.Commit()
}
func (r *RemoteInboxRepo) AcceptOutput(ctx context.Context, f runner.Fence, frame runner.OutputFrame) (uint64, error) {
	return r.accept(ctx, f, "output", frame.Seq, frame)
}
func (r *RemoteInboxRepo) AcceptResult(ctx context.Context, f runner.Fence, result runner.RemoteResult) error {
	_, err := r.accept(ctx, f, "result", 0, result)
	return err
}
func (r *RemoteInboxRepo) Receipt(ctx context.Context, f runner.Fence) (runner.InboxReceipt, bool, error) {
	out := runner.InboxReceipt{Fence: f}
	err := r.db.QueryRowContext(ctx, `SELECT last_output_seq FROM runner_inbox_cursors WHERE job_id=? AND generation=? AND runner_id=?`, f.JobID.String(), f.Generation, string(f.RunnerID)).Scan(&out.LastOutputSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	var b []byte
	err = r.db.QueryRowContext(ctx, `SELECT payload FROM runner_inbox WHERE job_id=? AND generation=? AND runner_id=? AND kind='result'`, f.JobID.String(), f.Generation, string(f.RunnerID)).Scan(&b)
	if err == nil {
		out.Result = &runner.RemoteResult{}
		err = json.Unmarshal(b, out.Result)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return out, true, err
}
func (r *RemoteInboxRepo) Outputs(ctx context.Context, f runner.Fence, after uint64) ([]runner.OutputFrame, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT i.payload FROM runner_inbox i JOIN runner_inbox_cursors c USING(job_id,generation,runner_id) WHERE i.job_id=? AND i.generation=? AND i.runner_id=? AND i.kind='output' AND i.seq>? AND i.seq<=c.last_output_seq ORDER BY i.seq`, f.JobID.String(), f.Generation, string(f.RunnerID), after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runner.OutputFrame
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		var frame runner.OutputFrame
		if err = json.Unmarshal(b, &frame); err != nil {
			return nil, fmt.Errorf("decode inbox output: %w", err)
		}
		out = append(out, frame)
	}
	return out, rows.Err()
}
