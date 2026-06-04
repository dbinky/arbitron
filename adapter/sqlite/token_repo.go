package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// TokenRepo implements identity.TokenRepository backed by SQLite.
//
// SECURITY: this adapter NEVER receives or stores the plaintext token. Only the
// sha256(full token) hash (for lookup) and the clear, non-secret prefix (for
// display) are persisted. The plaintext exists solely on the Mint return path
// (the service); a database read leak yields hashes that cannot be replayed.
type TokenRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

var _ identity.TokenRepository = (*TokenRepo)(nil)

// NewTokenRepo creates a TokenRepo using the given database connection.
func NewTokenRepo(db *sql.DB) *TokenRepo {
	return &TokenRepo{db: db, queries: generated.New(db)}
}

func (r *TokenRepo) Create(ctx context.Context, t *identity.Token) error {
	err := r.queries.CreateAPIToken(ctx, generated.CreateAPITokenParams{
		ID:        uuidToString(t.ID),
		UserID:    uuidToString(t.UserID),
		Label:     t.Label,
		TokenHash: t.TokenHash,
		Prefix:    t.Prefix,
	})
	if err != nil {
		// A token_hash collision is astronomically unlikely; surface it rather
		// than swallow so the service can retry/regenerate.
		if isUniqueViolation(err) {
			return fmt.Errorf("creating api token: duplicate token hash: %w", err)
		}
		return fmt.Errorf("creating api token: %w", err)
	}
	return nil
}

func (r *TokenRepo) GetByHash(ctx context.Context, tokenHash string) (*identity.Token, error) {
	row, err := r.queries.GetAPITokenByHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("token: %w", identity.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting api token: %w", err)
	}
	return rowToToken(row)
}

func (r *TokenRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]identity.Token, error) {
	rows, err := r.queries.ListAPITokensByUser(ctx, uuidToString(userID))
	if err != nil {
		return nil, fmt.Errorf("listing api tokens: %w", err)
	}
	out := make([]identity.Token, 0, len(rows))
	for _, row := range rows {
		tok, err := rowToToken(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *tok)
	}
	return out, nil
}

func (r *TokenRepo) Delete(ctx context.Context, id, userID uuid.UUID) error {
	n, err := r.queries.DeleteAPIToken(ctx, generated.DeleteAPITokenParams{
		ID:     uuidToString(id),
		UserID: uuidToString(userID),
	})
	if err != nil {
		return fmt.Errorf("deleting api token: %w", err)
	}
	// 0 rows means no such token OR not owned by userID — both are "not yours".
	if n == 0 {
		return fmt.Errorf("token: %w", identity.ErrNotFound)
	}
	return nil
}

func (r *TokenRepo) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	if err := r.queries.TouchAPIToken(ctx, uuidToString(id)); err != nil {
		return fmt.Errorf("touching api token: %w", err)
	}
	return nil
}

// rowToToken maps a generated api_tokens row to the domain Token, parsing UUIDs
// and the nullable/required TEXT timestamps.
func rowToToken(row generated.ApiToken) (*identity.Token, error) {
	id, err := parseUUID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	userID, err := parseUUID(row.UserID)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	lastUsed, err := nullStringToTimePtr(row.LastUsedAt)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	return &identity.Token{
		ID:         id,
		UserID:     userID,
		Label:      row.Label,
		Prefix:     row.Prefix,
		TokenHash:  row.TokenHash,
		CreatedAt:  createdAt,
		LastUsedAt: lastUsed,
	}, nil
}
