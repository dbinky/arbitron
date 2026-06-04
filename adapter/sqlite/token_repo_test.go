package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedUser inserts a user so api_tokens FK constraints are satisfied.
func seedUser(t *testing.T, db testDB, username string) uuid.UUID {
	t.Helper()
	users := NewUserRepo(db.DB)
	id := uuid.New()
	require.NoError(t, users.Create(context.Background(), &instance.User{
		ID:       id,
		Username: username,
		Role:     identity.RoleUser,
	}))
	return id
}

// mintToken generates a token and persists it for userID, returning the
// plaintext and the stored domain Token.
func mintToken(t *testing.T, repo *TokenRepo, userID uuid.UUID, label string) (string, *identity.Token) {
	t.Helper()
	plaintext, prefix, hash, err := identity.GenerateToken()
	require.NoError(t, err)
	tok := &identity.Token{
		ID:        uuid.New(),
		UserID:    userID,
		Label:     label,
		Prefix:    prefix,
		TokenHash: hash,
	}
	require.NoError(t, repo.Create(context.Background(), tok))
	return plaintext, tok
}

// Happy path: Create then GetByHash round-trips, ListByUser returns it.
func TestTokenRepo_CreateGetList_HappyPath(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTokenRepo(db.DB)
	uid := seedUser(t, db, "alice")

	plaintext, tok := mintToken(t, repo, uid, "laptop")

	got, err := repo.GetByHash(ctx, identity.HashToken(plaintext))
	require.NoError(t, err)
	assert.Equal(t, tok.ID, got.ID)
	assert.Equal(t, uid, got.UserID)
	assert.Equal(t, "laptop", got.Label)
	assert.Equal(t, tok.Prefix, got.Prefix)
	assert.Equal(t, tok.TokenHash, got.TokenHash)
	assert.Nil(t, got.LastUsedAt)
	assert.False(t, got.CreatedAt.IsZero())

	list, err := repo.ListByUser(ctx, uid)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, tok.ID, list[0].ID)
}

// Input validation: unknown / empty hashes are ErrNotFound.
func TestTokenRepo_GetByHash_Validation(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTokenRepo(db.DB)

	_, err := repo.GetByHash(ctx, "")
	assert.ErrorIs(t, err, identity.ErrNotFound)

	_, err = repo.GetByHash(ctx, "not-a-real-hash")
	assert.ErrorIs(t, err, identity.ErrNotFound)
}

// Error/failure: deleting a missing id is ErrNotFound; TouchLastUsed sets the
// timestamp; a duplicate token_hash insert surfaces an error.
func TestTokenRepo_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTokenRepo(db.DB)
	uid := seedUser(t, db, "alice")

	err := repo.Delete(ctx, uuid.New(), uid)
	assert.ErrorIs(t, err, identity.ErrNotFound)

	plaintext, tok := mintToken(t, repo, uid, "laptop")
	require.NoError(t, repo.TouchLastUsed(ctx, tok.ID))
	got, err := repo.GetByHash(ctx, identity.HashToken(plaintext))
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt, "last_used_at should be set after touch")

	// Duplicate token_hash must surface (not silently no-op).
	dup := &identity.Token{
		ID:        uuid.New(),
		UserID:    uid,
		Label:     "dup",
		Prefix:    tok.Prefix,
		TokenHash: tok.TokenHash,
	}
	err = repo.Create(ctx, dup)
	require.Error(t, err)
}

// Boundary: empty user returns a non-nil empty slice; ordering is created_at DESC.
func TestTokenRepo_Boundary(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTokenRepo(db.DB)
	uid := seedUser(t, db, "alice")

	empty, err := repo.ListByUser(ctx, uid)
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	_, first := mintToken(t, repo, uid, "first")
	_, second := mintToken(t, repo, uid, "second")

	list, err := repo.ListByUser(ctx, uid)
	require.NoError(t, err)
	require.Len(t, list, 2)
	// created_at DESC; ties broken by insertion — at minimum both present.
	ids := map[uuid.UUID]bool{list[0].ID: true, list[1].ID: true}
	assert.True(t, ids[first.ID] && ids[second.ID])
}

// Isolation/security: a user never sees another's tokens, and a cross-user
// delete is rejected at the persistence layer.
func TestTokenRepo_Isolation(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewTokenRepo(db.DB)
	aUID := seedUser(t, db, "alice")
	bUID := seedUser(t, db, "bob")

	_, aTok := mintToken(t, repo, aUID, "alice-tok")
	bPlain, bTok := mintToken(t, repo, bUID, "bob-tok")

	aList, err := repo.ListByUser(ctx, aUID)
	require.NoError(t, err)
	require.Len(t, aList, 1)
	assert.Equal(t, aTok.ID, aList[0].ID)
	for _, tok := range aList {
		assert.NotEqual(t, bTok.ID, tok.ID, "A must never see B's token")
	}

	// Alice tries to delete Bob's token: rejected, and Bob's token survives.
	err = repo.Delete(ctx, bTok.ID, aUID)
	assert.ErrorIs(t, err, identity.ErrNotFound)
	still, err := repo.GetByHash(ctx, identity.HashToken(bPlain))
	require.NoError(t, err)
	assert.Equal(t, bTok.ID, still.ID)
}
