package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

var _ engine.TurnLeaseStore = (*TurnLeaseRepo)(nil)

// TurnLeaseRepo implements engine.TurnLeaseStore over the turn_leases table
// (migration 045). Hand-written SQL. expires_at is unix milliseconds.
type TurnLeaseRepo struct {
	db     *sql.DB
	holder string
	bootID string
	now    func() time.Time
}

// NewTurnLeaseRepo creates the lease repo. holder identifies this process in
// the row (diagnostics only; ownership is the token).
func NewTurnLeaseRepo(db *sql.DB, holder string) *TurnLeaseRepo {
	return &TurnLeaseRepo{db: db, holder: holder, now: time.Now}
}

// SetClock replaces the clock (tests).
func (r *TurnLeaseRepo) SetClock(now func() time.Time) { r.now = now }

// SetBootID sets this process's shared server boot ID before use.
func (r *TurnLeaseRepo) SetBootID(bootID string) { r.bootID = bootID }

// Acquire inserts the lease or takes over a RELEASED row in one statement. An
// occupied row is never stolen, even when expires_at has passed: expiry alone
// does not prove the holder (possibly a remote harness) stopped. Reclaiming an
// occupied expired row goes only through RecoverTurnLease with a durable proof
// (P4 T10). A clean re-acquire keeps the generation.
func (r *TurnLeaseRepo) Acquire(ctx context.Context, key string, ttl time.Duration) (engine.TurnLease, error) {
	if ttl <= 0 {
		return engine.TurnLease{}, fmt.Errorf("turn lease %s: ttl must be positive", key)
	}
	now := r.now().UTC()
	token := uuid.New()
	exp := now.Add(ttl)
	var gen int64
	err := r.db.QueryRowContext(ctx, `INSERT INTO turn_leases (key, token, generation, expires_at, holder, boot_id)
VALUES (?, ?, 1, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
    token      = excluded.token,
    expires_at = excluded.expires_at,
    holder     = excluded.holder,
    boot_id    = excluded.boot_id
WHERE turn_leases.token IS NULL
RETURNING generation`, key, token.String(), exp.UnixMilli(), r.holder, r.bootID).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, fmt.Errorf("turn lease %s: %w", key, engine.ErrInteractiveTurnInProgress)
	}
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("acquiring turn lease %s: %w", key, err)
	}
	return engine.TurnLease{Key: key, Token: token, Generation: gen, ExpiresAt: time.UnixMilli(exp.UnixMilli()).UTC()}, nil
}

// Renew extends the lease iff the token still owns it (even past expiry, as
// long as nobody took it over).
func (r *TurnLeaseRepo) Renew(ctx context.Context, l engine.TurnLease, ttl time.Duration) (engine.TurnLease, error) {
	exp := r.now().UTC().Add(ttl)
	var gen int64
	err := r.db.QueryRowContext(ctx, `UPDATE turn_leases SET expires_at = ? WHERE key = ? AND token = ? RETURNING generation`,
		exp.UnixMilli(), l.Key, l.Token.String()).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, engine.ErrTurnLeaseLost
	}
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("renewing turn lease %s: %w", l.Key, err)
	}
	l.ExpiresAt = time.UnixMilli(exp.UnixMilli()).UTC()
	l.Generation = gen
	return l, nil
}

// Release frees the lease iff the token still owns it; otherwise a no-op.
func (r *TurnLeaseRepo) Release(ctx context.Context, l engine.TurnLease) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE turn_leases SET token = NULL, expires_at = 0, holder = '' WHERE key = ? AND token = ?`,
		l.Key, l.Token.String()); err != nil {
		return fmt.Errorf("releasing turn lease %s: %w", l.Key, err)
	}
	return nil
}

// BumpGeneration reserves the next session generation under the held lease.
func (r *TurnLeaseRepo) BumpGeneration(ctx context.Context, l engine.TurnLease) (engine.TurnLease, error) {
	var gen, exp int64
	err := r.db.QueryRowContext(ctx, `UPDATE turn_leases SET generation = generation + 1 WHERE key = ? AND token = ? RETURNING generation, expires_at`,
		l.Key, l.Token.String()).Scan(&gen, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, engine.ErrTurnLeaseLost
	}
	if err != nil {
		return engine.TurnLease{}, fmt.Errorf("bumping turn lease %s: %w", l.Key, err)
	}
	l.Generation = gen
	l.ExpiresAt = time.UnixMilli(exp).UTC()
	return l, nil
}

// Current returns the held lease for key, if any (possibly expired).
func (r *TurnLeaseRepo) Current(ctx context.Context, key string) (engine.TurnLease, bool, error) {
	var tok string
	var gen, exp int64
	err := r.db.QueryRowContext(ctx, `SELECT token, generation, expires_at FROM turn_leases WHERE key = ? AND token IS NOT NULL`, key).
		Scan(&tok, &gen, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.TurnLease{}, false, nil
	}
	if err != nil {
		return engine.TurnLease{}, false, fmt.Errorf("reading turn lease %s: %w", key, err)
	}
	token, err := parseUUID(tok)
	if err != nil {
		return engine.TurnLease{}, false, err
	}
	return engine.TurnLease{Key: key, Token: token, Generation: gen, ExpiresAt: time.UnixMilli(exp).UTC()}, true, nil
}
