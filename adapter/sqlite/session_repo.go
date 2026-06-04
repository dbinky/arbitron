package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
)

// SessionRepo implements identity.SessionRepository backed by SQLite.
//
// SECURITY: the raw session token (the cookie value) is NEVER stored. Only its
// sha256 hex digest is written to / read from the id_hash column, so a database
// read leak yields hashes that cannot be replayed as cookies. Expiry is filtered
// server-side in SQL (GetSession's expires_at > now), so an expired row is
// indistinguishable from absent and never authenticates a request.
type SessionRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

var _ identity.SessionRepository = (*SessionRepo)(nil)

// NewSessionRepo creates a SessionRepo using the given database connection.
func NewSessionRepo(db *sql.DB) *SessionRepo {
	return &SessionRepo{db: db, queries: generated.New(db)}
}

// hashToken returns the hex sha256 of the raw token. Centralised so Create and
// Get hash identically.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (r *SessionRepo) Create(ctx context.Context, s *identity.Session) error {
	if s == nil || s.ID == "" {
		return fmt.Errorf("creating session: empty token")
	}
	err := r.queries.CreateSession(ctx, generated.CreateSessionParams{
		IDHash:    hashToken(s.ID),
		UserID:    uuidToString(s.UserID),
		CreatedAt: timeToString(s.CreatedAt),
		ExpiresAt: timeToString(s.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

func (r *SessionRepo) Get(ctx context.Context, id string) (*identity.Session, error) {
	row, err := r.queries.GetSession(ctx, generated.GetSessionParams{
		IDHash:    hashToken(id),
		ExpiresAt: timeToString(time.Now().UTC()),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting session: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	userID, err := parseUUID(row.UserID)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	// NOTE: ID is intentionally NOT repopulated with the raw token (the store
	// never had it); callers that need the cookie value already hold it.
	return &identity.Session{
		UserID:    userID,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}, nil
}

func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	if err := r.queries.DeleteSession(ctx, hashToken(id)); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

func (r *SessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	n, err := r.queries.DeleteExpiredSessions(ctx, timeToString(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("deleting expired sessions: %w", err)
	}
	return int(n), nil
}

// SweepExpiredSessions runs DeleteExpired on a ticker until ctx is cancelled.
// Wired in cmd/arbitron/main.go (Task 8) only in org mode. Errors are logged,
// not fatal -- a transient DB error on one tick must not kill the loop.
func SweepExpiredSessions(ctx context.Context, repo identity.SessionRepository, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if n, err := repo.DeleteExpired(ctx, now.UTC()); err != nil {
				log.Error("session sweep failed", "error", err)
			} else if n > 0 {
				log.Debug("session sweep removed expired sessions", "count", n)
			}
		}
	}
}
