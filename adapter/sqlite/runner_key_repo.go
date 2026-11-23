package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/runner"
)

var _ runner.KeyStore = (*RunnerKeyRepo)(nil)

// RunnerKeyRepo implements runner.KeyStore over SQLite (hand-written SQL).
// SECURITY: it never receives the plaintext key — only its hash and the clear prefix.
type RunnerKeyRepo struct {
	db *sql.DB
}

// NewRunnerKeyRepo creates a RunnerKeyRepo.
func NewRunnerKeyRepo(db *sql.DB) *RunnerKeyRepo { return &RunnerKeyRepo{db: db} }

const runnerKeyColumns = `id, name, runner_id, key_hash, prefix, machine_public_key, created_at, claimed_at, last_used_at, revoked_at`

func (r *RunnerKeyRepo) Create(ctx context.Context, k *runner.RunnerKey) error {
	const q = `INSERT INTO runner_keys (id, name, runner_id, key_hash, prefix, machine_public_key, created_at)
	           VALUES (?, ?, ?, ?, ?, NULL, ?)`
	_, err := r.db.ExecContext(ctx, q, uuidToString(k.ID), k.Name, string(k.RunnerID), k.Hash, k.Prefix, timeToString(k.CreatedAt))
	if err != nil {
		msg := err.Error()
		if isUniqueViolation(err) && (strings.Contains(msg, "runner_keys.name") || strings.Contains(msg, "runner_keys.runner_id")) {
			return fmt.Errorf("creating runner key %q: %w", k.Name, runner.ErrKeyNameTaken)
		}
		return fmt.Errorf("creating runner key: %w", err)
	}
	return nil
}

func (r *RunnerKeyRepo) GetByHash(ctx context.Context, hash string) (runner.RunnerKey, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+runnerKeyColumns+` FROM runner_keys WHERE key_hash = ?`, hash)
	k, err := scanRunnerKey(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.RunnerKey{}, runner.ErrKeyNotFound
	}
	return k, err
}

func (r *RunnerKeyRepo) List(ctx context.Context) ([]runner.RunnerKey, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+runnerKeyColumns+` FROM runner_keys ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("listing runner keys: %w", err)
	}
	defer rows.Close()
	out := make([]runner.RunnerKey, 0)
	for rows.Next() {
		k, err := scanRunnerKey(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Claim binds the key to pub only if no machine has claimed it yet. The single
// conditional UPDATE is the atomic first-claim: concurrent claimers race on one
// row and SQLite's single writer lets exactly one see RowsAffected == 1.
func (r *RunnerKeyRepo) Claim(ctx context.Context, id uuid.UUID, pub []byte, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE runner_keys SET machine_public_key = ?, claimed_at = ?
		 WHERE id = ? AND machine_public_key IS NULL AND revoked_at IS NULL`,
		pub, timeToString(at), uuidToString(id))
	if err != nil {
		return fmt.Errorf("claiming runner key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("claiming runner key: %w", err)
	}
	if n == 1 {
		return nil
	}
	var revoked sql.NullString
	err = r.db.QueryRowContext(ctx, `SELECT revoked_at FROM runner_keys WHERE id = ?`, uuidToString(id)).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return runner.ErrKeyNotFound
	case err != nil:
		return fmt.Errorf("claiming runner key: %w", err)
	case revoked.Valid:
		return runner.ErrKeyRevoked
	default:
		return runner.ErrKeyAlreadyClaimed
	}
}

func (r *RunnerKeyRepo) TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE runner_keys SET last_used_at = ? WHERE id = ?`, timeToString(at), uuidToString(id)); err != nil {
		return fmt.Errorf("touching runner key: %w", err)
	}
	return nil
}

func (r *RunnerKeyRepo) Revoke(ctx context.Context, id uuid.UUID, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE runner_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, timeToString(at), uuidToString(id))
	if err != nil {
		return fmt.Errorf("revoking runner key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	var one int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM runner_keys WHERE id = ?`, uuidToString(id)).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return runner.ErrKeyNotFound
	} else if err != nil {
		return fmt.Errorf("revoking runner key: %w", err)
	}
	return nil // already revoked: idempotent
}

func scanRunnerKey(scan func(dest ...any) error) (runner.RunnerKey, error) {
	var (
		idStr, name, runnerID, hash, prefix, created string
		pub                                          []byte
		claimed, lastUsed, revoked                   sql.NullString
	)
	if err := scan(&idStr, &name, &runnerID, &hash, &prefix, &pub, &created, &claimed, &lastUsed, &revoked); err != nil {
		return runner.RunnerKey{}, err
	}
	id, err := parseUUID(idStr)
	if err != nil {
		return runner.RunnerKey{}, fmt.Errorf("runner key: %w", err)
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return runner.RunnerKey{}, fmt.Errorf("runner key: %w", err)
	}
	claimedAt, err := nullStringToTimePtr(claimed)
	if err != nil {
		return runner.RunnerKey{}, fmt.Errorf("runner key: %w", err)
	}
	lastUsedAt, err := nullStringToTimePtr(lastUsed)
	if err != nil {
		return runner.RunnerKey{}, fmt.Errorf("runner key: %w", err)
	}
	revokedAt, err := nullStringToTimePtr(revoked)
	if err != nil {
		return runner.RunnerKey{}, fmt.Errorf("runner key: %w", err)
	}
	k := runner.RunnerKey{ID: id, Name: name, RunnerID: runner.RunnerID(runnerID), Hash: hash, Prefix: prefix,
		CreatedAt: createdAt, ClaimedAt: claimedAt, LastUsedAt: lastUsedAt, RevokedAt: revokedAt}
	if len(pub) > 0 {
		k.MachinePublicKey = append([]byte(nil), pub...)
	}
	return k, nil
}
