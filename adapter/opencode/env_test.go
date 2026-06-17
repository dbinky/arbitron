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
// env.go — credential-only env (no base-URL vars on opencode 1.17.x)
// ---------------------------------------------------------------------------

// TestOpencodeEnv_SetsCredentialOnly verifies that opencodeEnv sets ONLY the
// credential env var and never injects a base-URL or endpoint var. Base URLs
// move to a generated config file in opencode 1.17.x, not the subprocess env.
func TestOpencodeEnv_SetsCredentialOnly(t *testing.T) {
	cases := []struct{ name, provider, keyVar string }{
		{"anthropic-api", "anthropic-api", "ANTHROPIC_API_KEY"},
		{"openai", "openai", "OPENAI_API_KEY"},
		{"openrouter", "openrouter", "OPENROUTER_API_KEY"},
		{"ollama-cloud", "ollama-cloud", "OLLAMA_API_KEY"},
		{"azure", "azure", "AZURE_API_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bindingWithKey(tc.provider, "https://base.example/v1", "sentinel-key", nil)
			env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
			require.NoError(t, err)
			got, ok := envValue(env, tc.keyVar)
			require.True(t, ok, "expected %s set", tc.keyVar)
			assert.Equal(t, "sentinel-key", got)
			for _, e := range env {
				assert.NotContains(t, e, "https://base.example/v1", "base url must not be in env")
				assert.NotContains(t, e, "_BASE_URL=", "no base-url env var on 1.17.x")
				assert.NotContains(t, e, "_ENDPOINT=", "no endpoint env var on 1.17.x")
			}
		})
	}
}

// TestOpencodeEnv_KeylessLocalOllamaAllowed verifies that a keyless local
// Ollama binding (no key, cased/spaced provider) succeeds without setting
// OLLAMA_API_KEY (base URL routing moves to config, not env).
func TestOpencodeEnv_KeylessLocalOllamaAllowed(t *testing.T) {
	b := provider.ResolvedBinding{Provider: " Ollama ", BaseURL: "http://localhost:11434/v1", RevealKey: nil}
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err, "local ollama is keyless")
	for _, e := range env {
		assert.NotContains(t, e, "OLLAMA_API_KEY=")
	}
}

// TestOpencodeEnv_BedrockQuartetUnchanged verifies that the bedrock AWS-quartet
// path still injects AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION (and
// optionally AWS_SESSION_TOKEN). This path is NOT changed by the no-base-URL
// refactor.
func TestOpencodeEnv_BedrockQuartetUnchanged(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIA-X"},
		RevealKey: func(_ context.Context) (string, error) { return "raw-secret", nil },
	}
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err)
	id, _ := envValue(env, "AWS_ACCESS_KEY_ID")
	assert.Equal(t, "AKIA-X", id)
	secret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, "raw-secret", secret)
}

// NOTE: opencodeEnv now takes an already-resolved descriptor (RunAgent resolves
// the provider ONCE and threads it to args/env/config). Unknown-provider
// fail-closed therefore lives at the RunAgent level — see provider_test.go
// (TestRunAgent_UnknownProviderFailsClosed and the control-char %q variant) and
// descriptorFor (providers_test.go) — not here.

// ---------------------------------------------------------------------------
// bedrock AWS-quartet path (unchanged by base-URL removal)
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
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
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
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
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
		env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBedrockConfig)
		assert.Nil(t, env, "no partial env on missing bedrock config")
	}
}

// ERROR — bedrock RevealKey failure aborts with no env.
func TestOpencodeEnv_BedrockRevealError(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA", "", errors.New("decrypt boom"))
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
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
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), secret)
	assert.NotContains(t, buf.String(), sess)
	gotSecret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, secret, gotSecret)
}

// GOTCHA — the bedrock alias resolves the AWS-quartet builder via descriptorFor,
// so a "bedrock" (non-canonical) agent binding sets AWS_* env rather than
// failing closed at launch.
func TestOpencodeEnv_BedrockAliasResolvesAWSQuartet(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "bedrock", // alias of the opencode id "amazon-bedrock"
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIA-ALIAS"},
		RevealKey: func(_ context.Context) (string, error) { return "raw-secret", nil },
	}
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err, "bedrock alias must route to the AWS-quartet builder")

	id, ok := envValue(env, "AWS_ACCESS_KEY_ID")
	require.True(t, ok)
	assert.Equal(t, "AKIA-ALIAS", id)
	secret, ok := envValue(env, "AWS_SECRET_ACCESS_KEY")
	require.True(t, ok)
	assert.Equal(t, "raw-secret", secret)
}

// seam (awssig.ParseCredential ↔ bedrockEnv): when RevealKey returns ("", nil)
// the empty payload is forwarded to awssig.ParseCredential, which returns
// ErrMissingSecret. bedrockEnv must wrap and propagate that error — no partial
// env, no panic. This is distinct from RevealKey returning an error directly;
// it exercises the awssig parsing step at the credential seam.
func TestOpencodeEnv_BedrockEmptyRevealPropagatesMissingSecret(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA", "", nil) // RevealKey returns ("", nil)
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.Error(t, err, "empty sealed credential must fail via awssig.ErrMissingSecret")
	assert.ErrorIs(t, err, awssig.ErrMissingSecret,
		"error chain must include awssig.ErrMissingSecret from the ParseCredential seam")
	assert.Nil(t, env, "no partial env when sealed credential is empty")
}

// ERROR — bedrock binding with nil RevealKey fails closed before any reveal.
func TestOpencodeEnv_BedrockNilRevealKey(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "amazon-bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKIA"},
		RevealKey: nil,
	}
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no RevealKey")
	assert.Nil(t, env)
}

// ---------------------------------------------------------------------------
// keyed-provider security properties (no base URL in env)
// ---------------------------------------------------------------------------

// ERROR — a non-keyless provider whose RevealKey returns an empty string (not an
// error) must still fail, because running without a key is unacceptable.
func TestOpencodeEnv_EmptyKeyFromRevealRejectsNonKeyless(t *testing.T) {
	b := bindingWithKey("anthropic", "https://api.anthropic.com", "", nil) // key=""
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.Error(t, err, "empty key from RevealKey must be rejected for non-keyless provider")
	assert.Contains(t, err.Error(), "requires a key")
	assert.Nil(t, env)
}

// Failure — RevealKey returns an error → the helper returns it and produces
// NO env (no partial injection).
func TestOpencodeEnv_RevealKeyError(t *testing.T) {
	wantErr := errors.New("decrypt failed")
	b := bindingWithKey("anthropic", "https://x", "", wantErr)

	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, env, "no env should be produced on RevealKey error")
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

	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.Error(t, err, "nil RevealKey must return an error, not panic")
	assert.Nil(t, env, "no env must be produced when RevealKey is nil")
}

// Happy path — a known provider injects KEYVAR=<sentinel> and NEVER a base-URL
// env var (base URLs move to generated config in opencode 1.17.x).
func TestOpencodeEnv_HappyPath(t *testing.T) {
	b := bindingWithKey("anthropic", "https://api.anthropic.com", "sk-sentinel-123", nil)

	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err)

	key, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok, "expected ANTHROPIC_API_KEY in env")
	assert.Equal(t, "sk-sentinel-123", key)

	_, ok = envValue(env, "ANTHROPIC_BASE_URL")
	assert.False(t, ok, "ANTHROPIC_BASE_URL must not be set (base URL moves to config)")
}

// Happy path — the injected vars are appended AFTER the base allowlist, and
// the base env (PATH augmentation) is preserved.
func TestOpencodeEnv_AppendsAfterBaseEnv(t *testing.T) {
	b := bindingWithKey("anthropic", "https://x", "sentinel", nil)

	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
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

// Security / log-capture — the revealed key NEVER appears in any log output,
// but IS present in the returned child env.
func TestOpencodeEnv_KeyAbsentFromLogs(t *testing.T) {
	const sentinel = "sk-super-secret-key-xyz"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	b := bindingWithKey("anthropic", "https://x", sentinel, nil)
	env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
	require.NoError(t, err)

	// The key reaches the child env...
	key, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, sentinel, key)

	// ...but never the logs.
	assert.NotContains(t, buf.String(), sentinel, "revealed key leaked into logs")
}

// security — ANTHROPIC_API_KEY must not be on the base allowlist (if it were
// added there it would shadow the per-call key; rejectBaseNames would catch it).
func TestOpencodeEnv_AnthropicKeyNotBaseName(t *testing.T) {
	const keyVar = "ANTHROPIC_API_KEY"
	assert.False(t, procenv.IsBaseName(keyVar), "ANTHROPIC_API_KEY must not be in base allowlist")
	require.NoError(t, rejectBaseNames(keyVar))
}

// Error — an injected var name that collides with the base allowlist is
// rejected by rejectBaseNames (used directly by bedrockEnv).
func TestOpencodeEnv_BaseNameCollisionRejected(t *testing.T) {
	err := rejectBaseNames("PATH", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PATH")
}

// NONREGRESSION — NonKeylessEmptyKeyFailsClosed: keyed providers must reject an
// empty-string key (EmptyKeyReject policy). Tests the three most common providers.
func TestOpencodeEnv_NonKeylessEmptyKeyFailsClosed(t *testing.T) {
	for _, prov := range []string{"anthropic", "openai", "openrouter"} {
		t.Run(prov, func(t *testing.T) {
			b := bindingWithKey(prov, "https://base", "", nil) // RevealKey returns ("", nil)
			env, err := opencodeEnv(context.Background(), mustDesc(t, b.Provider), b)
			require.Error(t, err, "empty revealed key must fail closed for a non-keyless provider")
			assert.Contains(t, err.Error(), "requires a key")
			assert.Nil(t, env, "no partial env when the key is empty for a non-keyless provider")
		})
	}
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

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
