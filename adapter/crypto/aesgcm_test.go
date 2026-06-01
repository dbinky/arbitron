package crypto_test

import (
	"bytes"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// key32 returns a deterministic 32-byte key for tests.
func key32(fill byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = fill
	}
	return k
}

// Compile-time: AESGCM satisfies the domain port.
var _ provider.Crypto = (*adaptercrypto.AESGCM)(nil)

// Happy path: Seal then Open round-trips a representative API-key string.
func TestAESGCM_SealOpen_RoundTrip(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x01))
	require.NoError(t, err)

	plaintext := []byte("sk-abc123-this-is-a-secret-api-key")
	ct, nonce, err := c.Seal(plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, ct, "ciphertext must not equal plaintext")
	assert.Len(t, nonce, 12, "nonce is 96-bit")

	got, err := c.Open(ct, nonce)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// Success: NewAESGCM with a valid 32-byte key succeeds.
func TestNewAESGCM_ValidKey(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x02))
	require.NoError(t, err)
	assert.NotNil(t, c)
}

// Failure: a tampered ciphertext byte fails Open with ErrDecrypt and no output.
func TestAESGCM_TamperedCiphertext_Fails(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x03))
	require.NoError(t, err)

	ct, nonce, err := c.Seal([]byte("payload"))
	require.NoError(t, err)

	tampered := bytes.Clone(ct)
	tampered[0] ^= 0xFF

	got, err := c.Open(tampered, nonce)
	assert.ErrorIs(t, err, provider.ErrDecrypt)
	assert.Nil(t, got, "no partial plaintext on auth failure")
}

// Failure: a tampered nonce byte fails Open with ErrDecrypt and no output.
func TestAESGCM_TamperedNonce_Fails(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x04))
	require.NoError(t, err)

	ct, nonce, err := c.Seal([]byte("payload"))
	require.NoError(t, err)

	tampered := bytes.Clone(nonce)
	tampered[0] ^= 0xFF

	got, err := c.Open(ct, tampered)
	assert.ErrorIs(t, err, provider.ErrDecrypt)
	assert.Nil(t, got)
}

// Failure: ciphertext sealed with key A cannot be opened with key B.
func TestAESGCM_WrongKey_Fails(t *testing.T) {
	a, err := adaptercrypto.NewAESGCM(key32(0xAA))
	require.NoError(t, err)
	b, err := adaptercrypto.NewAESGCM(key32(0xBB))
	require.NoError(t, err)

	ct, nonce, err := a.Seal([]byte("payload"))
	require.NoError(t, err)

	got, err := b.Open(ct, nonce)
	assert.ErrorIs(t, err, provider.ErrDecrypt)
	assert.Nil(t, got)
}

// Error: a wrong-length key (16 bytes) is rejected with ErrMasterKeyInvalid.
func TestNewAESGCM_ShortKey_Rejected(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(make([]byte, 16))
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, c)
}

// Error: an empty key is rejected with ErrMasterKeyInvalid.
func TestNewAESGCM_EmptyKey_Rejected(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(nil)
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, c)
}

// Edge: empty plaintext seals and opens back to empty (non-nil zero-length).
func TestAESGCM_EmptyPlaintext_RoundTrip(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x05))
	require.NoError(t, err)

	ct, nonce, err := c.Seal([]byte{})
	require.NoError(t, err)
	assert.Len(t, nonce, 12)

	got, err := c.Open(ct, nonce)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// Edge: a long key-like plaintext round-trips cleanly.
func TestAESGCM_LongPlaintext_RoundTrip(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x06))
	require.NoError(t, err)

	plaintext := bytes.Repeat([]byte("sk-"), 4096)
	ct, nonce, err := c.Seal(plaintext)
	require.NoError(t, err)

	got, err := c.Open(ct, nonce)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

// Edge: nonce uniqueness across many seals of the same plaintext — all nonces
// distinct and all ciphertexts differ (probabilistic GCM nonce uniqueness).
func TestAESGCM_NonceUniqueness(t *testing.T) {
	c, err := adaptercrypto.NewAESGCM(key32(0x07))
	require.NoError(t, err)

	const n = 1000
	nonces := make(map[string]struct{}, n)
	ciphers := make(map[string]struct{}, n)
	plaintext := []byte("sk-same-plaintext-every-time")

	for i := 0; i < n; i++ {
		ct, nonce, err := c.Seal(plaintext)
		require.NoError(t, err)

		nk := string(nonce)
		_, dupNonce := nonces[nk]
		require.Falsef(t, dupNonce, "duplicate nonce at iteration %d", i)
		nonces[nk] = struct{}{}

		ck := string(ct)
		_, dupCipher := ciphers[ck]
		require.Falsef(t, dupCipher, "duplicate ciphertext at iteration %d", i)
		ciphers[ck] = struct{}{}
	}
	assert.Len(t, nonces, n)
	assert.Len(t, ciphers, n)
}
