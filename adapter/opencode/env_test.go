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

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// ---------------------------------------------------------------------------
// env_providers.go — table coverage for the full provider set
// ---------------------------------------------------------------------------

// All non-AWS, keyed providers map to a key var + base-url var.
func TestProviderEnvVars_FullKeyedSet(t *testing.T) {
	cases := []struct{ prov, keyVar, baseURLVar string }{
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		{"openai", "OPENAI_API_KEY", "OPENAI_BASE_URL"},
		{"openrouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
		{"azure", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"},
		{"ollama", "OLLAMA_API_KEY", "OLLAMA_BASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.prov, func(t *testing.T) {
			k, b, ok := providerEnvVars(tc.prov)
			require.True(t, ok)
			assert.Equal(t, tc.keyVar, k)
			assert.Equal(t, tc.baseURLVar, b)
		})
	}
}

// amazon-bedrock is registered and flagged as an AWS-quartet provider.
func TestProviderEnvVars_BedrockIsAWS(t *testing.T) {
	entry, ok := providerEnvVarTable["amazon-bedrock"]
	require.True(t, ok, "amazon-bedrock must be registered")
	assert.True(t, entry.aws, "bedrock must route to the AWS-quartet env builder")
}

// Ollama base-url var name is exposed for the keyless local path.
func TestProviderEnvVars_OllamaBaseURLVar(t *testing.T) {
	_, baseURLVar, ok := providerEnvVars("ollama")
	require.True(t, ok)
	assert.Equal(t, "OLLAMA_BASE_URL", baseURLVar)
}

// ---------------------------------------------------------------------------
// env.go — bedrock AWS-quartet + keyless local-ollama branches
// ---------------------------------------------------------------------------

// bedrockBinding builds a binding whose Extras carry region + access-key-id and
// whose RevealKey returns the sealed payload (bare secret or JSON composite).
func bedrockBinding(region, accessKeyID, sealedPayload string, err error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "amazon-bedrock",
		Extras: map[string]string{
			"region":        region,
			"access-key-id": accessKeyID,
		},
		RevealKey: func(_ context.Context) (string, error) {
			if err != nil {
				return "", err
			}
			return sealedPayload, nil
		},
	}
}

// EDGE-CASE / HAPPY — bedrock bare-secret payload expands to the 3-var quartet
// (no session token): AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION.
func TestOpencodeEnv_BedrockBareSecret(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA-TEST", "raw-secret-key", nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	id, ok := envValue(env, "AWS_ACCESS_KEY_ID")
	require.True(t, ok)
	assert.Equal(t, "AKIA-TEST", id)
	secret, ok := envValue(env, "AWS_SECRET_ACCESS_KEY")
	require.True(t, ok)
	assert.Equal(t, "raw-secret-key", secret)
	region, ok := envValue(env, "AWS_REGION")
	require.True(t, ok)
	assert.Equal(t, "us-east-1", region)
	_, ok = envValue(env, "AWS_SESSION_TOKEN")
	assert.False(t, ok, "no session token => AWS_SESSION_TOKEN omitted")
}

// EDGE-CASE — bedrock JSON composite payload expands to the FULL quartet incl.
// AWS_SESSION_TOKEN.
func TestOpencodeEnv_BedrockComposite(t *testing.T) {
	payload := `{"secret":"composite-secret","session_token":"sess-123"}`
	b := bedrockBinding("eu-west-1", "AKIA-2", payload, nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	secret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, "composite-secret", secret)
	tok, ok := envValue(env, "AWS_SESSION_TOKEN")
	require.True(t, ok)
	assert.Equal(t, "sess-123", tok)
	region, _ := envValue(env, "AWS_REGION")
	assert.Equal(t, "eu-west-1", region)
}

// INPUT-VALIDATION — bedrock missing region or access-key-id fails before reveal.
func TestOpencodeEnv_BedrockMissingExtras(t *testing.T) {
	for _, tc := range []struct{ region, id string }{
		{"", "AKIA"},
		{"us-east-1", ""},
	} {
		b := bedrockBinding(tc.region, tc.id, "secret", nil)
		env, err := opencodeEnv(context.Background(), b)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBedrockConfig)
		assert.Nil(t, env, "no partial env on missing bedrock config")
	}
}

// ERROR — bedrock RevealKey failure aborts with no env.
func TestOpencodeEnv_BedrockRevealError(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA", "", errors.New("decrypt boom"))
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt boom")
	assert.Nil(t, env)
}

// SECURITY — neither the bedrock secret nor session token leaks into logs; both
// reach the child env only.
func TestOpencodeEnv_BedrockSecretsNotLogged(t *testing.T) {
	const secret = "super-secret-aws"
	const sess = "super-secret-session"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	payload := `{"secret":"` + secret + `","session_token":"` + sess + `"}`
	b := bedrockBinding("us-east-1", "AKIA", payload, nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), secret)
	assert.NotContains(t, buf.String(), sess)
	gotSecret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, secret, gotSecret)
}

// EDGE-CASE — keyless local Ollama: a binding with no RevealKey but a BaseURL
// sets ONLY OLLAMA_BASE_URL, no key var, no error.
func TestOpencodeEnv_LocalOllamaKeyless(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "ollama",
		BaseURL:   "http://localhost:11434",
		RevealKey: nil, // keyless local path
	}
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	base, ok := envValue(env, "OLLAMA_BASE_URL")
	require.True(t, ok)
	assert.Equal(t, "http://localhost:11434", base)
	_, ok = envValue(env, "OLLAMA_API_KEY")
	assert.False(t, ok, "keyless local ollama must NOT set a key var")
}

// SECURITY — keyless launches are restricted to the allowlist: a NON-ollama
// provider with a nil RevealKey must NOT run unauthenticated even with a BaseURL.
func TestOpencodeEnv_KeylessRestrictedToOllama(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "openai",
		BaseURL:   "https://api.openai.com",
		RevealKey: nil,
	}
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err, "non-ollama keyless launch must be rejected")
	assert.Nil(t, env)
}

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

// seam (awssig.ParseCredential ↔ bedrockEnv): when RevealKey returns ("", nil)
// the empty payload is forwarded to awssig.ParseCredential, which returns
// ErrMissingSecret. bedrockEnv must wrap and propagate that error — no partial
// env, no panic. This is distinct from RevealKey returning an error directly;
// it exercises the awssig parsing step at the credential seam.
func TestOpencodeEnv_BedrockEmptyRevealPropagatesMissingSecret(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA", "", nil) // RevealKey returns ("", nil)
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err, "empty sealed credential must fail via awssig.ErrMissingSecret")
	assert.ErrorIs(t, err, awssig.ErrMissingSecret,
		"error chain must include awssig.ErrMissingSecret from the ParseCredential seam")
	assert.Nil(t, env, "no partial env when sealed credential is empty")
}

// ERROR — bedrock binding with nil RevealKey fails closed before any reveal.
func TestOpencodeEnv_BedrockNilRevealKey(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider: "amazon-bedrock",
		Extras:   map[string]string{"region": "us-east-1", "access-key-id": "AKIA"},
		RevealKey: nil,
	}
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no RevealKey")
	assert.Nil(t, env)
}

// ERROR — keyless ollama with an empty BaseURL must fail (nothing to route to).
func TestOpencodeEnv_KeylessOllamaEmptyBaseURL(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "ollama",
		BaseURL:   "",   // no endpoint configured
		RevealKey: nil,  // keyless path
	}
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err, "keyless ollama with empty BaseURL must fail")
	assert.Contains(t, err.Error(), "BaseURL")
	assert.Nil(t, env)
}

// ERROR — a non-keyless provider whose RevealKey returns an empty string (not an
// error) must still fail, because running without a key is unacceptable.
func TestOpencodeEnv_EmptyKeyFromRevealRejectsNonKeyless(t *testing.T) {
	b := bindingWithKey("anthropic", "https://api.anthropic.com", "", nil) // key=""
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err, "empty key from RevealKey must be rejected for non-keyless provider")
	assert.Contains(t, err.Error(), "empty key")
	assert.Nil(t, env)
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

// SECURITY — control chars in a provider string must be %q-escaped in the env
// error so they never reach logs as raw bytes (same property as buildArgs / factory).
func TestOpencodeEnv_UnknownProviderControlCharsSafelyQuoted(t *testing.T) {
	b := bindingWithKey("foo\nmalicious", "https://x", "sentinel", nil)
	_, err := opencodeEnv(context.Background(), b)
	require.ErrorIs(t, err, ErrUnknownProvider)
	assert.NotContains(t, err.Error(), "\n",
		"raw newline must not appear in the error message")
	assert.Contains(t, err.Error(), `\n`,
		"escaped \\n should appear via %q-quoted provider string")
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
