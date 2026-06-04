package identity

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TokenService mints, lists, and revokes personal API tokens for the caller
// identified by identity.FromContext. Every method is scoped to that user: a
// caller can never see or revoke another user's token. Admin is irrelevant here
// — token management is always "my own tokens"; cross-user token administration
// is out of scope for Phase 6.
type TokenService struct {
	repo TokenRepository
	now  func() time.Time // injectable clock for tests
}

// NewTokenService returns a TokenService backed by the given repository.
func NewTokenService(repo TokenRepository) *TokenService {
	return &TokenService{repo: repo, now: time.Now}
}

// Mint creates a new token for the caller and returns the plaintext ONCE. The
// returned Token never carries the plaintext; the plaintext string is the only
// secret-bearing value and is the caller's responsibility to surface once.
//
// SECURITY: this is the ONLY code path that ever holds the plaintext. It must
// never log it.
func (s *TokenService) Mint(ctx context.Context, label string) (plaintext string, t *Token, err error) {
	id, ok := FromContext(ctx)
	if !ok {
		return "", nil, ErrUnauthenticated
	}
	if strings.TrimSpace(label) == "" {
		return "", nil, ErrInvalidLabel
	}
	plaintext, prefix, hash, err := GenerateToken()
	if err != nil {
		return "", nil, err
	}
	tok := &Token{
		ID:        uuid.New(),
		UserID:    id.UserID,
		Label:     label,
		Prefix:    prefix,
		TokenHash: hash,
	}
	if err := s.repo.Create(ctx, tok); err != nil {
		// Do NOT return the plaintext on failure — the token was never persisted.
		return "", nil, err
	}
	return plaintext, tok, nil
}

// List returns the caller's tokens (metadata only; the surface DTOs strip the
// hash, and the plaintext is never available on this path).
func (s *TokenService) List(ctx context.Context) ([]Token, error) {
	id, ok := FromContext(ctx)
	if !ok {
		return nil, ErrUnauthenticated
	}
	return s.repo.ListByUser(ctx, id.UserID)
}

// Revoke deletes one of the caller's tokens by id. The repo Delete is scoped by
// (id, callerUserID), so revoking another user's token id returns ErrNotFound —
// there is no existence oracle across users.
func (s *TokenService) Revoke(ctx context.Context, tokenID uuid.UUID) error {
	id, ok := FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	return s.repo.Delete(ctx, tokenID, id.UserID)
}
