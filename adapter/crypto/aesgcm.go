// Package crypto provides the at-rest encryption adapter for connection
// secrets: an AES-256-GCM implementation of the domain provider.Crypto port
// plus the master-key bootstrap. The master key is held in memory only and is
// never logged or persisted.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// masterKeyLen is the required AES-256 key length in bytes.
const masterKeyLen = 32

// AESGCM is an AES-256-GCM implementation of provider.Crypto. It holds the
// derived GCM cipher in memory; the master key bytes are not retained on the
// struct beyond cipher construction and are never logged or persisted.
type AESGCM struct {
	gcm cipher.AEAD
}

// Compile-time assertion that AESGCM satisfies the domain port.
var _ provider.Crypto = (*AESGCM)(nil)

// NewAESGCM builds an AES-256-GCM Crypto from a 32-byte master key. The key is
// used only to construct the in-memory cipher; it is never logged or persisted.
// It returns provider.ErrMasterKeyInvalid when the key is not 32 bytes.
func NewAESGCM(masterKey []byte) (*AESGCM, error) {
	if len(masterKey) != masterKeyLen {
		return nil, fmt.Errorf("crypto: %w (got %d bytes)", provider.ErrMasterKeyInvalid, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("crypto: build cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: build gcm: %w", err)
	}
	return &AESGCM{gcm: gcm}, nil
}

// Seal encrypts plaintext under a fresh random 96-bit nonce generated per call.
// It returns the ciphertext and the nonce separately (mapping to the
// api_key_cipher / api_key_nonce columns). A nonce is never reused.
func (a *AESGCM) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, a.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	ciphertext = a.gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Open authenticates and decrypts ciphertext with the supplied nonce. On any
// failure (tamper or wrong key) it returns nil plaintext and a wrapped
// provider.ErrDecrypt; the raw GCM error is never surfaced to a logger.
func (a *AESGCM) Open(ciphertext, nonce []byte) (plaintext []byte, err error) {
	if len(nonce) != a.gcm.NonceSize() {
		return nil, fmt.Errorf("crypto: open: %w", provider.ErrDecrypt)
	}
	pt, err := a.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", provider.ErrDecrypt)
	}
	return pt, nil
}
