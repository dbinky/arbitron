package sqlite

import (
	"context"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKey32 returns a deterministic 32-byte key for the AES-GCM Crypto.
func testKey32(fill byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill
	}
	return k
}

func newConnRepo(t *testing.T) (*ConnectionRepo, testDB) {
	t.Helper()
	db := setupTestDB(t)
	crypto, err := adaptercrypto.NewAESGCM(testKey32(0x2A))
	require.NoError(t, err)
	return NewConnectionRepo(db.DB, crypto), db
}

func sampleConn(name string) provider.Connection {
	return provider.Connection{
		Name:     name,
		Provider: "azure-openai",
		BaseURL:  "https://example.openai.azure.com",
		Extras:   map[string]string{"api-version": "2024-02-01"},
	}
}

// ---------------------------------------------------------------------------
// Migration: the connections table exists after RunMigrations.
// ---------------------------------------------------------------------------

func TestConnectionsMigration_TableExists(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM connections").Scan(&count))
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// Happy: Create then Get returns the stored metadata and no key; Reveal returns
// the exact plaintext that was passed to Create.
func TestConnectionRepo_CreateGetReveal(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	const key = "sk-super-secret-key-0xDEADBEEF"

	require.NoError(t, repo.Create(ctx, sampleConn("azure-prod"), key))

	got, err := repo.Get(ctx, "azure-prod")
	require.NoError(t, err)
	assert.Equal(t, "azure-prod", got.Name)
	assert.Equal(t, "azure-openai", got.Provider)
	assert.Equal(t, "https://example.openai.azure.com", got.BaseURL)
	assert.Equal(t, map[string]string{"api-version": "2024-02-01"}, got.Extras)

	revealed, err := repo.Reveal(ctx, "azure-prod")
	require.NoError(t, err)
	assert.Equal(t, key, revealed)
}

// ---------------------------------------------------------------------------
// Success
// ---------------------------------------------------------------------------

// Success: Update with a new non-nil key changes what Reveal returns.
func TestConnectionRepo_UpdateWithNewKey(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("c1"), "old-key"))

	newKey := "new-rotated-key"
	updated := sampleConn("c1")
	updated.BaseURL = "https://updated.example.com"
	require.NoError(t, repo.Update(ctx, updated, &newKey))

	revealed, err := repo.Reveal(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, newKey, revealed)

	got, err := repo.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "https://updated.example.com", got.BaseURL)
}

// Success: List returns all created connections ordered by name, metadata only.
func TestConnectionRepo_List(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("bravo"), "k1"))
	require.NoError(t, repo.Create(ctx, sampleConn("alpha"), "k2"))
	require.NoError(t, repo.Create(ctx, sampleConn("charlie"), "k3"))

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "alpha", list[0].Name)
	assert.Equal(t, "bravo", list[1].Name)
	assert.Equal(t, "charlie", list[2].Name)
}

// Success: Delete removes the row so a subsequent Get errors.
func TestConnectionRepo_Delete(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("temp"), "k"))

	require.NoError(t, repo.Delete(ctx, "temp"))

	_, err := repo.Get(ctx, "temp")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// ---------------------------------------------------------------------------
// Failure
// ---------------------------------------------------------------------------

// Failure: Get and Reveal on a missing connection return ErrConnectionNotFound.
func TestConnectionRepo_MissingConnection(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()

	_, err := repo.Get(ctx, "nope")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)

	_, err = repo.Reveal(ctx, "nope")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// Failure: Create with a duplicate name returns ErrConnectionExists.
func TestConnectionRepo_DuplicateName(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("dup"), "k1"))

	err := repo.Create(ctx, sampleConn("dup"), "k2")
	assert.ErrorIs(t, err, provider.ErrConnectionExists)
}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

// Error: Reveal when the stored cipher is tampered propagates the decrypt error
// and returns no key.
func TestConnectionRepo_RevealTamperedCipher(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("tampered"), "real-key"))

	// Corrupt the ciphertext directly in the DB.
	_, err := db.DB.Exec(
		"UPDATE connections SET api_key_cipher = ? WHERE name = ?",
		[]byte("garbage-not-valid-ciphertext"), "tampered")
	require.NoError(t, err)

	revealed, err := repo.Reveal(ctx, "tampered")
	assert.ErrorIs(t, err, provider.ErrDecrypt)
	assert.Empty(t, revealed)
}

// Error: Update on a missing connection returns ErrConnectionNotFound.
func TestConnectionRepo_UpdateMissing(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	key := "k"
	err := repo.Update(ctx, sampleConn("ghost"), &key)
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// Error: Delete on a missing connection returns ErrConnectionNotFound.
func TestConnectionRepo_DeleteMissing(t *testing.T) {
	repo, _ := newConnRepo(t)
	err := repo.Delete(context.Background(), "ghost")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// Edge: Update with apiKey == nil preserves the previously stored key.
func TestConnectionRepo_UpdateNilKeyPreservesKey(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("keep"), "original-key"))

	changed := sampleConn("keep")
	changed.Provider = "openrouter"
	changed.Extras = map[string]string{"region": "us"}
	require.NoError(t, repo.Update(ctx, changed, nil))

	// Metadata changed.
	got, err := repo.Get(ctx, "keep")
	require.NoError(t, err)
	assert.Equal(t, "openrouter", got.Provider)
	assert.Equal(t, map[string]string{"region": "us"}, got.Extras)

	// Key preserved.
	revealed, err := repo.Reveal(ctx, "keep")
	require.NoError(t, err)
	assert.Equal(t, "original-key", revealed)
}

// Edge: empty Extras round-trips as an empty (non-nil) map.
func TestConnectionRepo_EmptyExtrasRoundTrip(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	c := provider.Connection{Name: "noextras", Provider: "litellm", BaseURL: "http://x", Extras: nil}
	require.NoError(t, repo.Create(ctx, c, "k"))

	got, err := repo.Get(ctx, "noextras")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{}, got.Extras)

	// Stored as the JSON object literal '{}'.
	var stored string
	require.NoError(t, db.DB.QueryRow("SELECT extras FROM connections WHERE name = ?", "noextras").Scan(&stored))
	assert.Equal(t, "{}", stored)
}

// Edge: a connection name with unusual but valid characters round-trips.
func TestConnectionRepo_UnusualNameRoundTrip(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	name := "weird:name/with-chars_123.üñïçödé"
	require.NoError(t, repo.Create(ctx, sampleConn(name), "k"))

	got, err := repo.Get(ctx, name)
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
}

// Edge: Count reflects the number of rows (0 fresh, N after N creates).
func TestConnectionRepo_Count(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	require.NoError(t, repo.Create(ctx, sampleConn("a"), "k"))
	require.NoError(t, repo.Create(ctx, sampleConn("b"), "k"))
	n, err = repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

// ---------------------------------------------------------------------------
// Secrecy assertions
// ---------------------------------------------------------------------------

// Secrecy: after Create, no column in the raw row contains the plaintext key.
func TestConnectionRepo_PlaintextNotInColumns(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	const key = "sk-PLAINTEXT-MUST-NOT-APPEAR"
	require.NoError(t, repo.Create(ctx, sampleConn("secret"), key))

	var (
		name, prov, baseURL, extras string
		cipher, nonce               []byte
		createdAt, updatedAt        string
	)
	require.NoError(t, db.DB.QueryRow(
		`SELECT name, provider, base_url, extras, api_key_cipher, api_key_nonce, created_at, updated_at
		 FROM connections WHERE name = ?`, "secret").Scan(
		&name, &prov, &baseURL, &extras, &cipher, &nonce, &createdAt, &updatedAt))

	for _, col := range []string{name, prov, baseURL, extras, createdAt, updatedAt} {
		assert.NotContains(t, col, key)
	}
	assert.NotContains(t, string(cipher), key)
	assert.NotContains(t, string(nonce), key)
	// Sanity: cipher/nonce are populated.
	assert.NotEmpty(t, cipher)
	assert.NotEmpty(t, nonce)
}

// fakeDecryptFailCrypto seals normally but always fails to open, exercising the
// ErrDecrypt propagation path without tampering bytes.
type fakeDecryptFailCrypto struct{}

func (fakeDecryptFailCrypto) Seal(pt []byte) (ciphertext, nonce []byte, err error) {
	return append([]byte("ct:"), pt...), []byte("nonce"), nil
}
func (fakeDecryptFailCrypto) Open(ciphertext, nonce []byte) ([]byte, error) {
	return nil, provider.ErrDecrypt
}

// Error (stub variant): a Crypto whose Open returns ErrDecrypt propagates the
// sentinel and returns no key.
func TestConnectionRepo_RevealCryptoOpenFails(t *testing.T) {
	db := setupTestDB(t)
	repo := NewConnectionRepo(db.DB, fakeDecryptFailCrypto{})
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("stub"), "any-key"))

	revealed, err := repo.Reveal(ctx, "stub")
	assert.ErrorIs(t, err, provider.ErrDecrypt)
	assert.Empty(t, revealed)
}

var _ provider.Crypto = fakeDecryptFailCrypto{}
