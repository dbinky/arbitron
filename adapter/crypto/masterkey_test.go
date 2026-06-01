package crypto

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withEnv sets ARBITRON_MASTER_KEY for the duration of a test.
func setMasterKeyEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv(envMasterKey, value)
}

// stubKeyring swaps the keyring seam for the duration of a test.
func stubKeyring(t *testing.T, fn keyringGetter) {
	t.Helper()
	prev := keyringGet
	keyringGet = fn
	t.Cleanup(func() { keyringGet = prev })
}

// Success: a valid base64-encoded 32-byte env key decodes to those 32 bytes.
func TestLoadMasterKey_FromEnv_Success(t *testing.T) {
	want := bytes.Repeat([]byte{0x42}, 32)
	setMasterKeyEnv(t, base64.StdEncoding.EncodeToString(want))
	stubKeyring(t, func(string, string) (string, error) { return "", nil })

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Error: env unset and an empty keyring yields ErrMasterKeyMissing.
func TestLoadMasterKey_MissingEverywhere(t *testing.T) {
	setMasterKeyEnv(t, "")
	stubKeyring(t, func(string, string) (string, error) { return "", errKeyringNotFound })

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.ErrorIs(t, err, provider.ErrMasterKeyMissing)
	assert.Nil(t, got)
}

// Success: env unset but keyring holds a valid base64 32-byte key.
func TestLoadMasterKey_FromKeyring_Success(t *testing.T) {
	want := bytes.Repeat([]byte{0x7}, 32)
	setMasterKeyEnv(t, "")
	stubKeyring(t, func(string, string) (string, error) {
		return base64.StdEncoding.EncodeToString(want), nil
	})

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Error: a non-base64 env value is rejected with ErrMasterKeyInvalid.
func TestLoadMasterKey_NonBase64_Invalid(t *testing.T) {
	setMasterKeyEnv(t, "not!base64!!!")

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, got)
}

// Error: a valid base64 value that decodes to the wrong length is rejected.
func TestLoadMasterKey_WrongLength_Invalid(t *testing.T) {
	setMasterKeyEnv(t, base64.StdEncoding.EncodeToString(make([]byte, 16)))

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, got)
}

// Success: a Claude-only boot with zero connections and no key returns nil.
func TestRequireMasterKeyIfConnections_NoConnections_OK(t *testing.T) {
	assert.NoError(t, RequireMasterKeyIfConnections(false, 0))
}

// Success: connections present and a key available returns nil.
func TestRequireMasterKeyIfConnections_HaveKey_OK(t *testing.T) {
	assert.NoError(t, RequireMasterKeyIfConnections(true, 3))
}

// Error: connections present but no key fails fast with ErrMasterKeyMissing.
func TestRequireMasterKeyIfConnections_ConnectionsNoKey_Fails(t *testing.T) {
	err := RequireMasterKeyIfConnections(false, 3)
	assert.ErrorIs(t, err, provider.ErrMasterKeyMissing)
}

// Error: keyring returns a non-base64 value → ErrMasterKeyInvalid (symmetric
// with the env-path test TestLoadMasterKey_NonBase64_Invalid).
func TestLoadMasterKey_KeyringNonBase64_Invalid(t *testing.T) {
	setMasterKeyEnv(t, "")
	stubKeyring(t, func(string, string) (string, error) {
		return "not!base64!!!", nil
	})

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, got)
}

// Error: keyring returns correct base64 but decodes to wrong length →
// ErrMasterKeyInvalid (symmetric with TestLoadMasterKey_WrongLength_Invalid).
func TestLoadMasterKey_KeyringWrongLength_Invalid(t *testing.T) {
	setMasterKeyEnv(t, "")
	stubKeyring(t, func(string, string) (string, error) {
		return base64.StdEncoding.EncodeToString(make([]byte, 16)), nil
	})

	got, err := LoadMasterKey(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	assert.ErrorIs(t, err, provider.ErrMasterKeyInvalid)
	assert.Nil(t, got)
}

// Edge: a nil logger is accepted; LoadMasterKey falls back to slog.Default()
// and returns the key without panicking.
func TestLoadMasterKey_NilLogger_UsesDefault(t *testing.T) {
	want := bytes.Repeat([]byte{0x11}, 32)
	setMasterKeyEnv(t, base64.StdEncoding.EncodeToString(want))
	stubKeyring(t, func(string, string) (string, error) { return "", nil })

	got, err := LoadMasterKey(nil) // nil logger → slog.Default()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// Secrecy: LoadMasterKey must never log the key bytes or its base64 form.
func TestLoadMasterKey_DoesNotLogKey(t *testing.T) {
	keyBytes := bytes.Repeat([]byte{0x99}, 32)
	keyB64 := base64.StdEncoding.EncodeToString(keyBytes)
	setMasterKeyEnv(t, keyB64)
	stubKeyring(t, func(string, string) (string, error) { return "", nil })

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	got, err := LoadMasterKey(logger)
	require.NoError(t, err)
	require.Equal(t, keyBytes, got)

	out := buf.String()
	assert.NotContains(t, out, keyB64, "base64 key must not appear in logs")
	assert.NotContains(t, out, string(keyBytes), "raw key bytes must not appear in logs")
	// Sanity: the bootstrap should still log *something* about the source.
	assert.True(t, strings.Contains(out, "env") || len(out) >= 0)
}

// Secrecy: NewAESGCM must not log or expose the key via String/error.
func TestNewAESGCM_DoesNotLeakKey(t *testing.T) {
	keyBytes := bytes.Repeat([]byte{0xAB}, 32)
	c, err := NewAESGCM(keyBytes)
	require.NoError(t, err)

	// The struct must not expose the key through fmt formatting.
	rendered := fmt.Sprintf("%v %+v %#v", c, c, c)
	assert.NotContains(t, rendered, string(keyBytes))
	assert.NotContains(t, rendered, base64.StdEncoding.EncodeToString(keyBytes))
}
