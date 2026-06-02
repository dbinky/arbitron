package provider_test

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
)

// Success: the Crypto sentinel errors are non-nil and distinct so adapters and
// callers can match each condition with errors.Is.
func TestCryptoSentinels_Distinct(t *testing.T) {
	assert.NotNil(t, provider.ErrMasterKeyMissing)
	assert.NotNil(t, provider.ErrMasterKeyInvalid)
	assert.NotNil(t, provider.ErrDecrypt)

	assert.NotErrorIs(t, provider.ErrMasterKeyMissing, provider.ErrMasterKeyInvalid)
	assert.NotErrorIs(t, provider.ErrMasterKeyInvalid, provider.ErrDecrypt)
	assert.NotErrorIs(t, provider.ErrDecrypt, provider.ErrMasterKeyMissing)
}

// Error: each sentinel stays matchable with errors.Is even when wrapped with
// context, which is how the adapter surfaces them.
func TestCryptoSentinels_WrappedMatch(t *testing.T) {
	wrapped := errors.Join(provider.ErrDecrypt, errors.New("gcm: message authentication failed"))
	assert.ErrorIs(t, wrapped, provider.ErrDecrypt)
}

// fakeCrypto verifies the Crypto interface is implementable by domain consumers
// without importing any adapter.
type fakeCrypto struct{}

func (fakeCrypto) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	return plaintext, []byte("nonce"), nil
}

func (fakeCrypto) Open(ciphertext, nonce []byte) (plaintext []byte, err error) {
	return ciphertext, nil
}

// Happy path: a value satisfies the Crypto port and round-trips through it.
func TestCrypto_PortSatisfied(t *testing.T) {
	var c provider.Crypto = fakeCrypto{}
	ct, nonce, err := c.Seal([]byte("secret"))
	assert.NoError(t, err)
	assert.Equal(t, []byte("nonce"), nonce)
	pt, err := c.Open(ct, nonce)
	assert.NoError(t, err)
	assert.Equal(t, []byte("secret"), pt)
}
