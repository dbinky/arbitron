package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// TransferSessionRepo persists transfer sessions (migration 044
// transfer_sessions). Only token hashes are stored, with every pinned scope
// column. Hand-written SQL, modeled on AuditRepo.
type TransferSessionRepo struct {
	db *sql.DB
}

var _ runner.TransferSessionStore = (*TransferSessionRepo)(nil)

// NewTransferSessionRepo creates a TransferSessionRepo over db.
func NewTransferSessionRepo(db *sql.DB) *TransferSessionRepo {
	return &TransferSessionRepo{db: db}
}

const transferSessionCols = `token_hash, job_id, generation, maintenance_operation_id, base_ref, bundle_hash,
	resource_key, resource_owner, resource_generation, resource_token, runner_id, run_id, direction,
	checkpoint_ref, expires_at, parts, created_at`

func (r *TransferSessionRepo) Create(ctx context.Context, s runner.TransferSession) error {
	parts, err := encodeTransferParts(s.Parts)
	if err != nil {
		return err
	}
	var jobID, gen, maint any // NULL for a maintenance scope / a job scope respectively
	if s.JobID != uuid.Nil {
		jobID, gen = uuidToString(s.JobID), s.Generation
	}
	if s.MaintenanceOperationID != "" {
		maint = s.MaintenanceOperationID
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO transfer_sessions (`+transferSessionCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.TokenHash, jobID, gen, maint, s.BaseRef, s.BundleHash,
		s.Resource.Key, s.Resource.Owner, s.Resource.Generation, s.Resource.Token.String(),
		string(s.RunnerID), uuidToString(s.RunID), s.Direction,
		s.CheckpointRef, timeToString(s.ExpiresAt), parts, timeToString(s.CreatedAt)); err != nil {
		return fmt.Errorf("creating transfer session: %w", err)
	}
	return nil
}

func (r *TransferSessionRepo) GetByHash(ctx context.Context, hash string) (runner.TransferSession, error) {
	var s runner.TransferSession
	var jobStr, maint sql.NullString
	var gen sql.NullInt64
	var resToken, runnerID, runStr, expires, parts, created string
	err := r.db.QueryRowContext(ctx, `SELECT `+transferSessionCols+` FROM transfer_sessions WHERE token_hash = ?`, hash).
		Scan(&s.TokenHash, &jobStr, &gen, &maint, &s.BaseRef, &s.BundleHash,
			&s.Resource.Key, &s.Resource.Owner, &s.Resource.Generation, &resToken,
			&runnerID, &runStr, &s.Direction, &s.CheckpointRef, &expires, &parts, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.TransferSession{}, runner.ErrTransferTokenInvalid
	}
	if err != nil {
		return runner.TransferSession{}, fmt.Errorf("reading transfer session: %w", err)
	}
	if jobStr.Valid {
		if s.JobID, err = parseUUID(jobStr.String); err != nil {
			return runner.TransferSession{}, fmt.Errorf("transfer session job id: %w", err)
		}
		s.Generation = gen.Int64
	}
	s.MaintenanceOperationID = maint.String
	if s.Resource.Token, err = uuid.Parse(resToken); err != nil {
		return runner.TransferSession{}, fmt.Errorf("transfer session resource token: %w", err)
	}
	if s.RunID, err = parseUUID(runStr); err != nil {
		return runner.TransferSession{}, fmt.Errorf("transfer session run id: %w", err)
	}
	if s.ExpiresAt, err = parseTime(expires); err != nil {
		return runner.TransferSession{}, fmt.Errorf("transfer session expiry: %w", err)
	}
	if s.CreatedAt, err = parseTime(created); err != nil {
		return runner.TransferSession{}, fmt.Errorf("transfer session created_at: %w", err)
	}
	s.RunnerID = runner.RunnerID(runnerID)
	raw := map[string]string{}
	if err := json.Unmarshal([]byte(parts), &raw); err != nil {
		return runner.TransferSession{}, fmt.Errorf("transfer session parts: %w", err)
	}
	s.Parts = make(map[int]string, len(raw))
	for k, v := range raw {
		i, err := strconv.Atoi(k)
		if err != nil {
			return runner.TransferSession{}, fmt.Errorf("transfer session part index %q: %w", k, err)
		}
		s.Parts[i] = v
	}
	return s, nil
}

func (r *TransferSessionRepo) RecordPart(ctx context.Context, hash string, index int, sha256Hex string) error {
	if index < 0 {
		return fmt.Errorf("transfer session: negative part index %d", index)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE transfer_sessions SET parts = json_set(parts, ?, ?) WHERE token_hash = ?`,
		fmt.Sprintf(`$."%d"`, index), sha256Hex, hash)
	if err != nil {
		return fmt.Errorf("recording transfer part: %w", err)
	}
	return oneTransferRow(res)
}

func (r *TransferSessionRepo) SetCheckpointRef(ctx context.Context, hash, ref string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE transfer_sessions SET checkpoint_ref = ? WHERE token_hash = ?`, ref, hash)
	if err != nil {
		return fmt.Errorf("recording transfer commit: %w", err)
	}
	return oneTransferRow(res)
}

func (r *TransferSessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM transfer_sessions WHERE expires_at <= ?`, timeToString(now))
	if err != nil {
		return 0, fmt.Errorf("deleting expired transfer sessions: %w", err)
	}
	return res.RowsAffected()
}

func oneTransferRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("transfer session rows: %w", err)
	}
	if n != 1 {
		return runner.ErrTransferTokenInvalid
	}
	return nil
}

func encodeTransferParts(parts map[int]string) (string, error) {
	raw := make(map[string]string, len(parts))
	for k, v := range parts {
		raw[strconv.Itoa(k)] = v
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("encoding transfer parts: %w", err)
	}
	return string(b), nil
}
