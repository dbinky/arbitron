package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedDevice(t *testing.T, repo *DeviceCodeRepo, rawCode, userCode string, exp time.Time) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &identity.DeviceCode{
		HashedCode: identity.HashToken(rawCode),
		UserCode:   userCode,
		Status:     identity.DeviceCodePending,
		ExpiresAt:  exp,
	}))
}

// Happy path: Create round-trips via both lookups; Approve sets status+user_id;
// Consume flips approved->consumed.
func TestDeviceCodeRepo_HappyPath(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewDeviceCodeRepo(db.DB)
	uid := seedUser(t, db, "approver")

	raw := "dev-secret-abc"
	exp := time.Now().Add(10 * time.Minute).UTC()
	seedDevice(t, repo, raw, "WXYZ-1234", exp)

	byHash, err := repo.GetByHash(ctx, identity.HashToken(raw))
	require.NoError(t, err)
	assert.Equal(t, "WXYZ-1234", byHash.UserCode)
	assert.Equal(t, identity.DeviceCodePending, byHash.Status)
	assert.Nil(t, byHash.UserID)

	byUser, err := repo.GetByUserCode(ctx, "WXYZ-1234")
	require.NoError(t, err)
	assert.Equal(t, byHash.HashedCode, byUser.HashedCode)

	require.NoError(t, repo.Approve(ctx, "WXYZ-1234", uid))
	approved, err := repo.GetByUserCode(ctx, "WXYZ-1234")
	require.NoError(t, err)
	assert.Equal(t, identity.DeviceCodeApproved, approved.Status)
	require.NotNil(t, approved.UserID)
	assert.Equal(t, uid, *approved.UserID)

	require.NoError(t, repo.Consume(ctx, identity.HashToken(raw)))
	consumed, err := repo.GetByHash(ctx, identity.HashToken(raw))
	require.NoError(t, err)
	assert.Equal(t, identity.DeviceCodeConsumed, consumed.Status)
}

// Input validation: unknown lookups are ErrNotFound.
func TestDeviceCodeRepo_NotFound(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewDeviceCodeRepo(db.DB)

	_, err := repo.GetByHash(ctx, "")
	assert.ErrorIs(t, err, identity.ErrNotFound)
	_, err = repo.GetByUserCode(ctx, "nope")
	assert.ErrorIs(t, err, identity.ErrNotFound)
}

// Error/failure: Consume of a pending row and Approve of a denied row both hit
// their status guard (0 rows) and surface ErrNotFound.
func TestDeviceCodeRepo_StatusGuards(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewDeviceCodeRepo(db.DB)
	uid := seedUser(t, db, "approver")

	raw := "dev-secret"
	seedDevice(t, repo, raw, "AAAA-1111", time.Now().Add(time.Minute).UTC())

	// Consume while still pending: nothing approved -> ErrNotFound.
	err := repo.Consume(ctx, identity.HashToken(raw))
	assert.ErrorIs(t, err, identity.ErrNotFound)

	// Deny then Approve: approve of a non-pending row -> ErrNotFound.
	require.NoError(t, repo.Deny(ctx, "AAAA-1111"))
	err = repo.Approve(ctx, "AAAA-1111", uid)
	assert.ErrorIs(t, err, identity.ErrNotFound)
}

// Boundary: DeleteExpired removes only rows strictly before now; equal stays.
func TestDeviceCodeRepo_DeleteExpired(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewDeviceCodeRepo(db.DB)

	now := time.Now().UTC().Truncate(time.Second)
	seedDevice(t, repo, "old", "OLD-0000", now.Add(-time.Minute))
	seedDevice(t, repo, "eq", "EQ-0000", now)
	seedDevice(t, repo, "future", "NEW-0000", now.Add(time.Minute))

	n, err := repo.DeleteExpired(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the strictly-past row is deleted")

	_, err = repo.GetByUserCode(ctx, "OLD-0000")
	assert.ErrorIs(t, err, identity.ErrNotFound)
	_, err = repo.GetByUserCode(ctx, "EQ-0000")
	assert.NoError(t, err, "expires_at == now is not deleted")
	_, err = repo.GetByUserCode(ctx, "NEW-0000")
	assert.NoError(t, err)
}

// Isolation/security: the stored device_code_hash equals HashToken(raw) and the
// raw secret appears in no column.
func TestDeviceCodeRepo_Secrecy(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	repo := NewDeviceCodeRepo(db.DB)

	raw := "super-secret-device-code"
	seedDevice(t, repo, raw, "SEC-1234", time.Now().Add(time.Minute).UTC())
	_ = ctx

	var hashCol, userCode, status, createdAt, expiresAt string
	var userID *string
	row := db.DB.QueryRow("SELECT device_code_hash, user_code, status, user_id, created_at, expires_at FROM device_codes")
	require.NoError(t, row.Scan(&hashCol, &userCode, &status, &userID, &createdAt, &expiresAt))

	assert.Equal(t, identity.HashToken(raw), hashCol, "stored hash must be sha256(raw device code)")
	for _, col := range []string{hashCol, userCode, status, createdAt, expiresAt} {
		assert.NotEqual(t, raw, col, "raw device code must not appear in any column")
		assert.NotContains(t, col, raw)
	}
	assert.Equal(t, "SEC-1234", userCode, "user_code is clear (low-entropy, shown to approver)")
}

// Defensive: ensure uuid import used (Approve uses a real user id).
var _ = uuid.New
