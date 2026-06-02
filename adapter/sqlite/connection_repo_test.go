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

// Failure: Create of an Azure connection missing api-version is rejected by the
// write-time provider rule and persists nothing.
func TestConnectionRepo_CreateAzureMissingAPIVersion_Rejected(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	c := provider.Connection{
		Name:     "az-bad",
		Provider: "azure-openai",
		BaseURL:  "https://example.openai.azure.com",
		Extras:   map[string]string{}, // no api-version
	}

	err := repo.Create(ctx, c, "k")
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)

	// Nothing persisted.
	_, getErr := repo.Get(ctx, "az-bad")
	assert.ErrorIs(t, getErr, provider.ErrConnectionNotFound)
}

// Failure: Update that drops an Azure connection's api-version is rejected and
// the prior stored metadata is left unchanged.
func TestConnectionRepo_UpdateAzureMissingAPIVersion_Rejected(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("az-up"), "k"))

	bad := sampleConn("az-up")
	bad.Extras = map[string]string{"api-version": ""} // empty == missing
	err := repo.Update(ctx, bad, nil)
	assert.ErrorIs(t, err, provider.ErrAzureAPIVersionRequired)

	// Prior metadata unchanged.
	got, getErr := repo.Get(ctx, "az-up")
	require.NoError(t, getErr)
	assert.Equal(t, "2024-02-01", got.Extras["api-version"])
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
	c := provider.Connection{Name: "noextras", Provider: "litellm", BaseURL: "https://x", Extras: nil}
	require.NoError(t, repo.Create(ctx, c, "k"))

	got, err := repo.Get(ctx, "noextras")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{}, got.Extras)

	// Stored as the JSON object literal '{}'.
	var stored string
	require.NoError(t, db.DB.QueryRow("SELECT extras FROM connections WHERE name = ?", "noextras").Scan(&stored))
	assert.Equal(t, "{}", stored)
}

// SECURITY (SSRF): the strict (production) store rejects a non-https or
// internal-host BaseURL at the write path, before any key is sealed.
func TestConnectionRepo_StrictPolicy_RejectsUnsafeBaseURL(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	for _, raw := range []string{
		"http://api.example.com",          // non-https
		"https://127.0.0.1/v1",            // loopback
		"https://169.254.169.254/latest/", // cloud metadata
		"https://10.1.2.3/v1",             // RFC1918 private
	} {
		c := provider.Connection{Name: "unsafe", Provider: "openrouter", BaseURL: raw}
		err := repo.Create(ctx, c, "k")
		assert.ErrorIs(t, err, provider.ErrInvalidBaseURL, "base_url %q must be rejected", raw)
	}
}

// SECURITY (SSRF): Update on an existing connection also re-validates the
// BaseURL, so a stored connection cannot be repointed at an unsafe host.
func TestConnectionRepo_StrictPolicy_RejectsUnsafeBaseURLOnUpdate(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("c"), "k"))

	c := sampleConn("c")
	c.BaseURL = "https://192.168.0.5/v1"
	err := repo.Update(ctx, c, nil)
	assert.ErrorIs(t, err, provider.ErrInvalidBaseURL)
}

// The permissive constructor accepts loopback/http so tests and local fixtures
// (httptest servers, fake routers) can persist connections.
func TestConnectionRepoWithPolicy_Permissive_AcceptsLoopback(t *testing.T) {
	db := setupTestDB(t)
	crypto, err := adaptercrypto.NewAESGCM(testKey32(0x2A))
	require.NoError(t, err)
	repo := NewConnectionRepoWithPolicy(db.DB, crypto, provider.AllowAnyURLPolicy)
	ctx := context.Background()

	c := provider.Connection{Name: "loop", Provider: "openrouter", BaseURL: "http://127.0.0.1:8080"}
	require.NoError(t, repo.Create(ctx, c, "k"))
}

// Edge: List on an empty store returns a non-nil empty slice, not nil.
// Callers that distinguish nil from []Connection{} (e.g. JSON marshalling) would
// see a different result if the repo returned the raw nil slice from sqlc.
func TestConnectionRepo_ListEmpty(t *testing.T) {
	repo, _ := newConnRepo(t)

	list, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, list, "List must return a non-nil slice on an empty store")
	assert.Empty(t, list)
}

// Edge: if the stored extras column contains malformed JSON, Get returns a
// non-nil error and no Connection value — not a silent garbled struct.
func TestConnectionRepo_CorruptedExtras_GetErrors(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("corrupt"), "k"))

	_, err := db.DB.Exec("UPDATE connections SET extras = ? WHERE name = ?", "not-json{{{", "corrupt")
	require.NoError(t, err)

	got, err := repo.Get(ctx, "corrupt")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, provider.ErrConnectionNotFound, "corruption is not a missing-connection error")
	assert.Equal(t, provider.Connection{}, got)
}

// Edge: if one row in the store has malformed extras JSON, List fails with a
// non-nil error rather than silently skipping the bad row or panicking.
func TestConnectionRepo_CorruptedExtras_ListErrors(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("good"), "k"))
	require.NoError(t, repo.Create(ctx, sampleConn("bad"), "k"))

	_, err := db.DB.Exec("UPDATE connections SET extras = ? WHERE name = ?", "not-json{{{", "bad")
	require.NoError(t, err)

	list, err := repo.List(ctx)
	assert.Error(t, err)
	assert.Nil(t, list)
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

// ---------------------------------------------------------------------------
// Paired integration: AESGCM crypto ↔ ConnectionRepo seam
// ---------------------------------------------------------------------------

// Error: a ConnectionRepo opened with a different master key cannot Reveal a
// connection sealed by the original key — AES-GCM authentication fails and
// ErrDecrypt propagates through the repo boundary.  This is the "wrong key"
// integration test that proves seal-on-write and Reveal-on-read are coupled to
// the same key at the repo seam, not just at the AESGCM unit level.
func TestConnectionRepo_RevealWrongKey_ErrDecrypt(t *testing.T) {
	repo, db := newConnRepo(t) // crypto = testKey32(0x2A)
	ctx := context.Background()
	const sentinel = "sk-WRONG-KEY-MUST-NOT-DECRYPT"
	require.NoError(t, repo.Create(ctx, sampleConn("sealed-with-a"), sentinel))

	// Build a second repo over the same DB but with a different master key.
	wrongCrypto, err := adaptercrypto.NewAESGCM(testKey32(0xFF))
	require.NoError(t, err)
	wrongRepo := NewConnectionRepo(db.DB, wrongCrypto)

	revealed, err := wrongRepo.Reveal(ctx, "sealed-with-a")
	assert.ErrorIs(t, err, provider.ErrDecrypt,
		"wrong-key Reveal must propagate ErrDecrypt through the repo boundary")
	assert.Empty(t, revealed, "no partial plaintext on wrong-key Reveal")
}
