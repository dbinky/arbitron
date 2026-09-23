package pi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// keyBinding builds a binding whose RevealKey returns key (or err).
func keyBinding(prov, key string, err error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: prov,
		Model:    "anthropic/claude-sonnet-5",
		RevealKey: func(context.Context) (string, error) {
			if err != nil {
				return "", err
			}
			return key, nil
		},
	}
}

// envValue returns the value of name in a KEY=VALUE env slice.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return strings.TrimPrefix(kv, name+"="), true
		}
	}
	return "", false
}

// HAPPY — a known provider's secret is injected under that provider's env var
// and nowhere else.
func TestPiEnv_InjectsProviderCredential(t *testing.T) {
	env, err := piEnv(context.Background(), keyBinding(provider.CanonicalAnthropicAPI, "sk-ant-secret", nil))
	require.NoError(t, err)
	got, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok, "anthropic binding must inject ANTHROPIC_API_KEY")
	assert.Equal(t, "sk-ant-secret", got)
}

// SUCCESS — each supported provider routes to its own documented env var, so a
// key is never handed to the wrong vendor.
func TestPiEnv_EachProviderUsesItsOwnVar(t *testing.T) {
	cases := map[string]string{
		provider.CanonicalAnthropicAPI: "ANTHROPIC_API_KEY",
		"openai":                       "OPENAI_API_KEY",
		"openrouter":                   "OPENROUTER_API_KEY",
		"deepseek":                     "DEEPSEEK_API_KEY",
	}
	for prov, wantVar := range cases {
		env, err := piEnv(context.Background(), keyBinding(prov, "k-"+prov, nil))
		require.NoError(t, err, "provider %q", prov)
		got, ok := envValue(env, wantVar)
		require.True(t, ok, "provider %q must inject %s", prov, wantVar)
		assert.Equal(t, "k-"+prov, got)
	}
}

// FAILURE — an unknown provider fails closed with NO env, so a mission cannot
// launch and silently fall back to whatever key happens to be in pi's own
// config or the ambient shell.
func TestPiEnv_UnknownProviderFailsClosed(t *testing.T) {
	env, err := piEnv(context.Background(), keyBinding("not-a-provider", "k", nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Nil(t, env, "no partial env on a failed build")
}

// ERROR — a RevealKey failure aborts before any env is assembled.
func TestPiEnv_RevealFailureProducesNoEnv(t *testing.T) {
	boom := errors.New("vault unavailable")
	env, err := piEnv(context.Background(), keyBinding(provider.CanonicalAnthropicAPI, "", boom))
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Nil(t, env)
}

// SECURITY / EDGE — the child env must not inherit the engine's own ambient
// credentials. A provider's var carries ONLY the revealed key, and no OTHER
// vendor key rides along.
func TestPiEnv_DoesNotLeakOtherVendorKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-openai-key-must-not-leak")
	env, err := piEnv(context.Background(), keyBinding(provider.CanonicalAnthropicAPI, "sk-ant", nil))
	require.NoError(t, err)
	_, present := envValue(env, "OPENAI_API_KEY")
	assert.False(t, present, "an anthropic mission must not carry an ambient OPENAI_API_KEY")
}

// EDGE — an empty revealed key fails closed rather than launching pi with a
// blank credential, which pi would treat as "not configured" and fall back on.
func TestPiEnv_EmptyKeyFailsClosed(t *testing.T) {
	env, err := piEnv(context.Background(), keyBinding(provider.CanonicalAnthropicAPI, "", nil))
	require.Error(t, err)
	assert.Nil(t, env)
}

// --- OAuth / subscription providers ----------------------------------------
//
// pi resolves subscription credentials from its OWN store (~/.pi/agent/auth.json,
// populated by `pi` → /login) and its resolution order puts that store ABOVE
// environment variables. Such a provider therefore needs no injection at all —
// and must not have a key demanded of it, because arbitron holds none.

// HAPPY — an OAuth provider builds a valid env with NO credential variable.
func TestPiEnv_OAuthProviderInjectsNoCredential(t *testing.T) {
	env, err := piEnv(context.Background(), keyBinding(providerOpenAICodex, "unused", nil))
	require.NoError(t, err)
	require.NotEmpty(t, env, "an OAuth provider still gets the filtered base env")
	for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		_, present := envValue(env, v)
		assert.False(t, present, "OAuth provider must inject no %s", v)
	}
}

// SUCCESS — an OAuth provider never calls RevealKey. arbitron has no secret for
// a subscription, so asking for one would fail a mission that pi can serve.
func TestPiEnv_OAuthProviderNeverRevealsAKey(t *testing.T) {
	revealed := false
	b := provider.ResolvedBinding{
		Provider: providerOpenAICodex,
		Model:    "openai-codex/gpt-5.6-sol",
		RevealKey: func(context.Context) (string, error) {
			revealed = true
			return "should-never-be-asked-for", nil
		},
	}
	_, err := piEnv(context.Background(), b)
	require.NoError(t, err)
	assert.False(t, revealed, "an OAuth provider must never reveal a key")
}

// EDGE — an OAuth provider with NO RevealKey at all still succeeds, which is
// the normal shape of a subscription binding.
func TestPiEnv_OAuthProviderWithoutRevealKeySucceeds(t *testing.T) {
	env, err := piEnv(context.Background(), provider.ResolvedBinding{Provider: providerOpenAICodex})
	require.NoError(t, err)
	assert.NotEmpty(t, env)
}

// SECURITY — the descriptor table must never mark a provider OAuth while also
// naming a credential variable. That combination would let a provider whose key
// failed to resolve silently fall through to an unauthenticated launch.
func TestProviderDescriptors_OAuthRowsCarryNoCredentialVar(t *testing.T) {
	for id, d := range providerDescriptors {
		if d.oauth {
			assert.Empty(t, d.credEnvVar,
				"descriptor %q: an OAuth row must not also set credEnvVar", id)
		} else {
			assert.NotEmpty(t, d.credEnvVar,
				"descriptor %q: a key-based row must name its credential var", id)
		}
	}
}

// --- provider resolution from the model prefix -------------------------------
//
// A profile can only name a provider by referencing a CONNECTION, and every
// connection requires an api_key + base_url. A subscription has neither, so a
// pi profile on the subscription path arrives with the no-connection sentinel
// ("claude-cli") — meaningless to pi. But pi's own model syntax already names
// the provider: "openai-codex/gpt-5.6-sol". Fall back to that prefix, and ONLY
// for OAuth providers: a key-based provider reached this way has no key for
// arbitron to inject, so it must still fail closed.

// HAPPY — a subscription binding (no connection) whose model names an OAuth
// provider resolves through the prefix and injects nothing.
func TestPiEnv_NoConnectionResolvesOAuthProviderFromModelPrefix(t *testing.T) {
	env, err := piEnv(context.Background(), provider.ResolvedBinding{
		Provider: noConnectionProvider,
		Model:    "openai-codex/gpt-5.6-sol",
	})
	require.NoError(t, err)
	require.NotEmpty(t, env)
	for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		_, present := envValue(env, v)
		assert.False(t, present, "subscription route must inject no %s", v)
	}
}

// SUCCESS — an explicit connection-backed provider still wins over the prefix,
// so a real connection is never overridden by whatever the model string says.
func TestPiEnv_ConnectionProviderWinsOverModelPrefix(t *testing.T) {
	b := keyBinding(provider.CanonicalAnthropicAPI, "sk-ant-secret", nil)
	b.Model = "openai-codex/gpt-5.6-sol" // prefix disagrees with the connection
	env, err := piEnv(context.Background(), b)
	require.NoError(t, err)
	got, ok := envValue(env, "ANTHROPIC_API_KEY")
	require.True(t, ok, "the connection's provider must still drive the credential")
	assert.Equal(t, "sk-ant-secret", got)
}

// FAILURE — a KEY-based provider named only by the model prefix fails closed.
// There is no connection, so arbitron holds no key; letting this through would
// hand the mission to whatever credential pi happens to have lying around.
func TestPiEnv_NoConnectionKeyProviderPrefixFailsClosed(t *testing.T) {
	env, err := piEnv(context.Background(), provider.ResolvedBinding{
		Provider: noConnectionProvider,
		Model:    "openai/gpt-5.6-sol", // key-based provider, no key available
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Nil(t, env)
}

// EDGE — a no-connection binding whose model has no provider prefix, or an
// unknown prefix, still fails closed rather than guessing.
func TestPiEnv_NoConnectionUnprefixedOrUnknownModelFailsClosed(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "not-a-provider/x", "", "/leading"} {
		_, err := piEnv(context.Background(), provider.ResolvedBinding{
			Provider: noConnectionProvider,
			Model:    model,
		})
		require.Error(t, err, "model %q must not resolve", model)
		assert.ErrorIs(t, err, ErrUnknownProvider)
	}
}
