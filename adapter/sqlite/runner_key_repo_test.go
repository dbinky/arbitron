package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
)

func newRunnerKeyFixture(name string) *runner.RunnerKey {
	return &runner.RunnerKey{ID: uuid.New(), Name: name, RunnerID: runner.RunnerID(name),
		Hash: "hash-" + name, Prefix: "ark_abcdefgh", CreatedAt: time.Now().UTC().Truncate(time.Second)}
}

func fileDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "keys.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	return db
}

func TestRunnerKeyRepo_CreateGetList(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRunnerKeyRepo(db.DB)
	ctx := context.Background()
	k := newRunnerKeyFixture("mac-a")
	require.NoError(t, repo.Create(ctx, k))

	got, err := repo.GetByHash(ctx, "hash-mac-a")
	require.NoError(t, err)
	assert.Equal(t, k.ID, got.ID)
	assert.Equal(t, runner.RunnerID("mac-a"), got.RunnerID)
	assert.Nil(t, got.MachinePublicKey)
	assert.Nil(t, got.ClaimedAt)
	assert.True(t, k.CreatedAt.Equal(got.CreatedAt))

	_, err = repo.GetByHash(ctx, "nope")
	assert.ErrorIs(t, err, runner.ErrKeyNotFound)

	require.NoError(t, repo.Create(ctx, newRunnerKeyFixture("mac-b")))
	all, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	dup := newRunnerKeyFixture("mac-a")
	dup.Hash = "other"
	assert.ErrorIs(t, repo.Create(ctx, dup), runner.ErrKeyNameTaken)
}

func TestRunnerKeyRepo_ClaimIsAtomicFirstWins(t *testing.T) {
	db := fileDB(t)
	repo := NewRunnerKeyRepo(db)
	ctx := context.Background()
	k := newRunnerKeyFixture("mac-a")
	require.NoError(t, repo.Create(ctx, k))

	const n = 20
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pub := make([]byte, 32)
			copy(pub, fmt.Sprintf("machine-%02d", i))
			results[i] = repo.Claim(ctx, k.ID, pub, time.Now())
		}(i)
	}
	wg.Wait()
	winners := 0
	winner := -1
	for i, err := range results {
		if err == nil {
			winners++
			winner = i
			continue
		}
		assert.ErrorIs(t, err, runner.ErrKeyAlreadyClaimed)
	}
	require.Equal(t, 1, winners, "exactly one machine binds the key")
	got, err := repo.GetByHash(ctx, k.Hash)
	require.NoError(t, err)
	want := make([]byte, 32)
	copy(want, fmt.Sprintf("machine-%02d", winner))
	assert.Equal(t, want, got.MachinePublicKey)
	assert.NotNil(t, got.ClaimedAt)
}

func TestRunnerKeyRepo_ClaimRevokedOrUnknown(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRunnerKeyRepo(db.DB)
	ctx := context.Background()
	k := newRunnerKeyFixture("mac-a")
	require.NoError(t, repo.Create(ctx, k))
	require.NoError(t, repo.Revoke(ctx, k.ID, time.Now()))
	assert.ErrorIs(t, repo.Claim(ctx, k.ID, make([]byte, 32), time.Now()), runner.ErrKeyRevoked)
	assert.ErrorIs(t, repo.Claim(ctx, uuid.New(), make([]byte, 32), time.Now()), runner.ErrKeyNotFound)
}

func TestRunnerKeyRepo_RevokeIdempotentAndTouch(t *testing.T) {
	db := setupTestDB(t)
	repo := NewRunnerKeyRepo(db.DB)
	ctx := context.Background()
	k := newRunnerKeyFixture("mac-a")
	require.NoError(t, repo.Create(ctx, k))

	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, repo.TouchLastUsed(ctx, k.ID, at))
	require.NoError(t, repo.Revoke(ctx, k.ID, at))
	require.NoError(t, repo.Revoke(ctx, k.ID, at.Add(time.Hour)), "second revoke is a no-op")
	got, err := repo.GetByHash(ctx, k.Hash)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt)
	assert.True(t, at.Equal(*got.RevokedAt), "first revocation time is kept")
	require.NotNil(t, got.LastUsedAt)
	assert.ErrorIs(t, repo.Revoke(ctx, uuid.New(), at), runner.ErrKeyNotFound)
}

func TestMigration043_RejectsMalformedMachineKey(t *testing.T) {
	db := setupTestDB(t)
	_, err := db.DB.Exec(`INSERT INTO runner_keys (id, name, runner_id, key_hash, prefix, machine_public_key, created_at)
		VALUES ('x', 'n', 'n', 'h', 'p', X'0102', '2026-11-20T00:00:00Z')`)
	assert.Error(t, err, "a machine public key must be exactly 32 bytes")
}
