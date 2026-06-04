package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seededUserID(t *testing.T, db testDB) string {
	t.Helper()
	var id string
	require.NoError(t, db.DB.QueryRow(
		"SELECT id FROM users WHERE username = 'local'").Scan(&id))
	return id
}

func TestSessionRepo_Create_Get_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()

	uid, err := NewUserRepo(db.DB).GetDefaultUser(ctx)
	require.NoError(t, err)

	exp := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	s := &identity.Session{ID: "raw-token-abc", UserID: uid, CreatedAt: time.Now().UTC(), ExpiresAt: exp}
	require.NoError(t, repo.Create(ctx, s))

	got, err := repo.Get(ctx, "raw-token-abc")
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
	assert.WithinDuration(t, exp, got.ExpiresAt, time.Second)
}

func TestSessionRepo_Create_EmptyToken(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()

	err := repo.Create(ctx, &identity.Session{ID: ""})
	require.Error(t, err)

	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count))
	assert.Equal(t, 0, count, "no row may be written for an empty token")
}

func TestSessionRepo_Get_Unknown(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	_, err := repo.Get(context.Background(), "never-issued")
	require.Error(t, err)
	assert.ErrorIs(t, err, identity.ErrNotFound)
}

func TestSessionRepo_Delete_ThenGet(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()
	uid, _ := NewUserRepo(db.DB).GetDefaultUser(ctx)

	require.NoError(t, repo.Create(ctx, &identity.Session{
		ID: "tok", UserID: uid, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	require.NoError(t, repo.Delete(ctx, "tok"))
	_, err := repo.Get(ctx, "tok")
	assert.ErrorIs(t, err, identity.ErrNotFound, "logout invalidates immediately")
}

func TestSessionRepo_Get_Expired(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()
	uid, _ := NewUserRepo(db.DB).GetDefaultUser(ctx)

	require.NoError(t, repo.Create(ctx, &identity.Session{
		ID: "past", UserID: uid, CreatedAt: time.Now().UTC().Add(-time.Hour),
		ExpiresAt: time.Now().UTC().Add(-time.Second),
	}))
	_, err := repo.Get(ctx, "past")
	assert.ErrorIs(t, err, identity.ErrNotFound, "an expired session is filtered server-side")

	require.NoError(t, repo.Create(ctx, &identity.Session{
		ID: "future", UserID: uid, CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))
	got, err := repo.Get(ctx, "future")
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
}

func TestSessionRepo_DeleteExpired_OnlyPastDue(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()
	uid, _ := NewUserRepo(db.DB).GetDefaultUser(ctx)
	now := time.Now().UTC()

	require.NoError(t, repo.Create(ctx, &identity.Session{ID: "expired", UserID: uid, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}))
	require.NoError(t, repo.Create(ctx, &identity.Session{ID: "now", UserID: uid, CreatedAt: now.Add(-time.Hour), ExpiresAt: now}))
	require.NoError(t, repo.Create(ctx, &identity.Session{ID: "future", UserID: uid, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}))

	n, err := repo.DeleteExpired(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "expired and expiring-now rows (<= now) are removed")

	// Future row survives and is gettable.
	got, err := repo.Get(ctx, "future")
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
}

// SECURITY: the raw session token is never persisted; only sha256(token) is.
func TestSecrecy_SessionToken_NeverStoredRaw(t *testing.T) {
	db := setupTestDB(t)
	repo := NewSessionRepo(db.DB)
	ctx := context.Background()
	uid, _ := NewUserRepo(db.DB).GetDefaultUser(ctx)

	const raw = "raw-SENTINEL-TOKEN-DO-NOT-STORE"
	require.NoError(t, repo.Create(ctx, &identity.Session{
		ID: raw, UserID: uid, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}))

	var idHash string
	require.NoError(t, db.DB.QueryRow("SELECT id_hash FROM sessions").Scan(&idHash))
	assert.NotContains(t, idHash, raw, "the raw token must never be stored")

	sum := sha256.Sum256([]byte(raw))
	assert.Equal(t, hex.EncodeToString(sum[:]), idHash, "stored id_hash must be sha256(token) hex")

	// A different token whose hash differs must not authenticate.
	_, err := repo.Get(ctx, "raw-SENTINEL-TOKEN-DO-NOT-STORX")
	assert.ErrorIs(t, err, identity.ErrNotFound)

	// A forged id_hash (one char flipped) does not authenticate via Get because
	// Get always hashes the supplied token; a tampered stored hash never matches
	// any real token. Confirm Get with the right token still works.
	got, err := repo.Get(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, uid, got.UserID)
}
