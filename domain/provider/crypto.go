package provider

import "errors"

// Crypto seals and opens secrets for storage at rest. The plaintext never
// leaves the caller except through Open at the single decryption entry point.
type Crypto interface {
	// Seal encrypts plaintext, returning ciphertext and the per-seal nonce.
	Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
	// Open decrypts ciphertext using the supplied nonce. A tampered
	// ciphertext or wrong key returns an error and no plaintext.
	Open(ciphertext, nonce []byte) (plaintext []byte, err error)
}

// ErrMasterKeyMissing is returned by the bootstrap when no master key is
// available (env unset and keyring empty) while it is required.
var ErrMasterKeyMissing = errors.New("master key not available")

// ErrMasterKeyInvalid is returned when the supplied key is not 32 bytes.
var ErrMasterKeyInvalid = errors.New("master key must be 32 bytes (base64-encoded)")

// ErrDecrypt is returned when Open fails authentication (tamper or wrong key).
var ErrDecrypt = errors.New("decryption failed")
