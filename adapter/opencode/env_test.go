package opencode

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// bindingWithKey builds a ResolvedBinding whose RevealKey returns the given
// sentinel (or err when non-nil).
func bindingWithKey(prov, baseURL, sentinel string, err error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: prov,
		BaseURL:  baseURL,
		RevealKey: func(_ context.Context) (string, error) {
			if err != nil {
				return "", err
			}
			return sentinel, nil
		},
	}
}

// envValue returns the value of name in a KEY=VALUE slice, and whether present.
func envValue(env []string, name string) (string, bool) {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// Happy path — a known provider injects KEYVAR=<sentinel> and BASEURLVAR.
func TestOpencodeEnv_HappyPath(t *testing.T) {
	b := bindingWithKey("anthropic", "https://api.anthropic.com", "sk-sentinel-123", nil)

	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	key, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok, "expected ANTHROPIC_API_KEY in env")
	assert.Equal(t, "sk-sentinel-123", key)

	base, ok := envValue(env, "ANTHROPIC_BASE_URL")
	require.True(t, ok, "expected ANTHROPIC_BASE_URL in env")
	assert.Equal(t, "https://api.anthropic.com", base)
}

// Happy path — the injected vars are appended AFTER the base allowlist, and
// the base env (PATH augmentation) is preserved.
func TestOpencodeEnv_AppendsAfterBaseEnv(t *testing.T) {
	b := bindingWithKey("anthropic", "https://x", "sentinel", nil)

	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	base := procenv.BaseEnv()
	require.GreaterOrEqual(t, len(env), len(base)+1)
	// The base entries are a prefix of the returned env (appended, not merged).
	assert.Equal(t, base, env[:len(base)])

	// PATH from BaseEnv is preserved and augmented (StandardBinDirs present).
	path, ok := envValue(env, "PATH")
	require.True(t, ok)
	assert.Contains(t, path, "/opt/homebrew/bin")
}

// Success (variants) — multiple providers each map to their own key/base-url
// var names; the base allowlist is fully preserved for each.
func TestOpencodeEnv_ProviderVariants(t *testing.T) {
	cases := []struct {
		provider   string
		keyVar     string
		baseURLVar string
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		{"openai", "OPENAI_API_KEY", "OPENAI_BASE_URL"},
		{"openrouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			b := bindingWithKey(tc.provider, "https://base", "sentinel-"+tc.provider, nil)
			env, err := opencodeEnv(context.Background(), b)
			require.NoError(t, err)

			key, ok := envValue(env, tc.keyVar)
			require.True(t, ok)
			assert.Equal(t, "sentinel-"+tc.provider, key)

			base, ok := envValue(env, tc.baseURLVar)
			require.True(t, ok)
			assert.Equal(t, "https://base", base)

			// HOME from the base allowlist is preserved (when set in parent).
			t.Setenv("HOME", "/home/test")
			env2, err := opencodeEnv(context.Background(), b)
			require.NoError(t, err)
			home, ok := envValue(env2, "HOME")
			require.True(t, ok)
			assert.Equal(t, "/home/test", home)
		})
	}
}

// Failure — RevealKey returns an error → the helper returns it and produces
// NO env (no partial injection).
func TestOpencodeEnv_RevealKeyError(t *testing.T) {
	wantErr := errors.New("decrypt failed")
	b := bindingWithKey("anthropic", "https://x", "", wantErr)

	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, env, "no env should be produced on RevealKey error")
}

// Error — unknown provider → ErrUnknownProvider; empty provider too.
func TestOpencodeEnv_UnknownProvider(t *testing.T) {
	for _, prov := range []string{"mystery", ""} {
		b := bindingWithKey(prov, "https://x", "sentinel", nil)
		env, err := opencodeEnv(context.Background(), b)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUnknownProvider)
		assert.Nil(t, env)
	}
}

// Edge case — empty BaseURL still injects the key var; the base-url var is
// omitted (not set to empty), so opencode falls back to its own default.
func TestOpencodeEnv_EmptyBaseURL(t *testing.T) {
	b := bindingWithKey("anthropic", "", "sentinel", nil)

	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	key, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sentinel", key)

	_, ok = envValue(env, "ANTHROPIC_BASE_URL")
	assert.False(t, ok, "empty BaseURL should omit the base-url var")
}

// Security / log-capture — the revealed key NEVER appears in any log output,
// but IS present in the returned child env.
func TestOpencodeEnv_KeyAbsentFromLogs(t *testing.T) {
	const sentinel = "sk-super-secret-key-xyz"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := bindingWithKey("anthropic", "https://x", sentinel, nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	// The key reaches the child env...
	key, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, sentinel, key)

	// ...but never the logs.
	assert.NotContains(t, buf.String(), sentinel, "revealed key leaked into logs")
}

// Edge case — nil RevealKey returns a clear error instead of panicking. A
// binding with no RevealKey is invalid for opencode (it requires a key to
// authenticate); the function must fail before any env is produced.
func TestOpencodeEnv_NilRevealKey_Errors(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "anthropic",
		BaseURL:   "https://api.anthropic.com",
		RevealKey: nil, // no key function
	}

	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err, "nil RevealKey must return an error, not panic")
	assert.Nil(t, env, "no env must be produced when RevealKey is nil")
}

// Error — an injected var name that collides with the base allowlist is
// rejected. Uses a synthetic provider entry registered for the test.
func TestOpencodeEnv_BaseNameCollisionRejected(t *testing.T) {
	// PATH is a base allowlist name; a provider mapping its key onto PATH must
	// be rejected by the IsBaseName guard.
	keyVar, _, _ := providerEnvVars("anthropic")
	require.False(t, procenv.IsBaseName(keyVar), "sanity: real key var is not a base name")

	err := rejectBaseNames("PATH", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PATH")
}
