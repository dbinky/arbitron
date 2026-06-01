package crypto

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/arbitron/arbitron/domain/provider"
)

// envMasterKey is the environment variable holding the base64-encoded 32-byte
// master key.
const envMasterKey = "ARBITRON_MASTER_KEY"

const (
	keyringService = "arbitron"
	keyringKey     = "master-key"
)

// errKeyringNotFound is the seam's "no value" signal. The production keyring
// getter returns it when the keyring has no entry; tests stub it likewise.
var errKeyringNotFound = errors.New("keyring: master key not found")

// keyringGetter reads a secret from the OS keyring. It is a seam so tests can
// stub keyring access without a real keyring backend.
type keyringGetter func(service, key string) (string, error)

// keyringGet is the active keyring getter. The MVP default returns
// errKeyringNotFound (no keyring dependency is wired yet); a real OS-keyring
// getter is a follow-up that swaps this var in. See package note.
var keyringGet keyringGetter = func(service, key string) (string, error) {
	return "", errKeyringNotFound
}

// LoadMasterKey resolves the 32-byte master key from, in order:
//  1. ARBITRON_MASTER_KEY (base64-encoded 32 bytes), then
//  2. the OS keyring (service "arbitron", key "master-key").
//
// It returns provider.ErrMasterKeyMissing when neither source yields a key, and
// provider.ErrMasterKeyInvalid when the decoded key is not 32 bytes. The key
// bytes and their base64 form are never logged — only the source is recorded.
func LoadMasterKey(logger *slog.Logger) ([]byte, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if encoded, ok := os.LookupEnv(envMasterKey); ok && encoded != "" {
		key, err := decodeMasterKey(encoded)
		if err != nil {
			logger.Error("master key from env is invalid", slog.String("source", "env"))
			return nil, err
		}
		logger.Info("master key loaded", slog.String("source", "env"))
		return key, nil
	}

	encoded, err := keyringGet(keyringService, keyringKey)
	if err != nil || encoded == "" {
		logger.Info("master key not available", slog.String("source", "none"))
		return nil, fmt.Errorf("crypto: %w (set %s or store it in the OS keyring)", provider.ErrMasterKeyMissing, envMasterKey)
	}

	key, err := decodeMasterKey(encoded)
	if err != nil {
		logger.Error("master key from keyring is invalid", slog.String("source", "keyring"))
		return nil, err
	}
	logger.Info("master key loaded", slog.String("source", "keyring"))
	return key, nil
}

// decodeMasterKey base64-decodes the key and enforces the 32-byte length. It
// never includes the key value in its returned error.
func decodeMasterKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("crypto: decode master key: %w", provider.ErrMasterKeyInvalid)
	}
	if len(key) != masterKeyLen {
		return nil, fmt.Errorf("crypto: %w (got %d bytes)", provider.ErrMasterKeyInvalid, len(key))
	}
	return key, nil
}

// RequireMasterKeyIfConnections fails fast at startup when connections exist but
// no master key is available, because their ciphertext could never be
// decrypted. It returns provider.ErrMasterKeyMissing with an actionable message,
// and nil otherwise (a Claude-only deployment with zero connections boots with
// no master key).
func RequireMasterKeyIfConnections(haveKey bool, connectionCount int) error {
	if connectionCount > 0 && !haveKey {
		return fmt.Errorf(
			"crypto: %w: %d stored connection(s) require a master key; set %s or store it in the OS keyring",
			provider.ErrMasterKeyMissing, connectionCount, envMasterKey,
		)
	}
	return nil
}
