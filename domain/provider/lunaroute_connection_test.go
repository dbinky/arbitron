package provider_test

import (
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrConn(base string) provider.Connection {
	return provider.Connection{ConfigRevision: 7, Name: "lr", Provider: "lunaroute", BaseURL: base, Extras: map[string]string{"note": "x"}}
}

func ptr(s string) *string { return &s }

// Edge: the prefix needs a suffix; pasted whitespace and controls are rejected,
// not trimmed, including Unicode whitespace and non-whitespace controls.
func TestValidLunaRouteKey(t *testing.T) {
	for _, k := range []string{"lr_a", "lr_live_abcDEF123", "lr_-_.~"} {
		assert.True(t, provider.ValidLunaRouteKey(k), k)
	}
	for _, k := range []string{"lr_live_ab!cd#ef", "lr_ab,cd", "lr_ab\"cd", "lr_ab'cd", "lr_ab;cd", "lr_ab\u00e9c", "lr_a:b", "lr_a@b", "lr_a%b", "lr_a*b"} {
		assert.False(t, provider.ValidLunaRouteKey(k), "punctuation outside the redactable set is refused: %q", k)
	}
	for _, k := range []string{"", "lr_", "LR_abc", "sk-abc", "sk-or-v1-abc", " lr_abc", "lr_abc ", "lr_ab cd", "lr_abc\n", "lr_ab\nc", "lr_ab\tc", "xlr_abc", "lr_ab\u00a0c", "lr_ab\u0085c", "lr_ab\x00c", "lr_ab\x7fc"} {
		assert.False(t, provider.ValidLunaRouteKey(k), "%q", k)
	}
}

// Happy: only provider and base URL change; the rest of the connection survives.
func TestPrepareLunaRouteCreate_HappyPathNormalizes(t *testing.T) {
	for _, base := range []string{"", "https://gw.lunaroute.com/v1/", "https://GW.lunaroute.com/"} {
		c := lrConn(base)
		c.Provider = "  LunaRoute "
		got, err := provider.PrepareLunaRouteCreate(c, "lr_live_key")
		require.NoError(t, err)
		want := c
		want.Provider = "lunaroute"
		want.BaseURL = "https://gw.lunaroute.com"
		assert.Equal(t, want, got)
		assert.Equal(t, "  LunaRoute ", c.Provider, "input unchanged")
		assert.Equal(t, base, c.BaseURL, "input unchanged")
	}
}

// Failure/error: refuse foreign or missing credentials with safe typed errors.
func TestPrepareLunaRouteCreate_RejectsForeignAndEmptyKeys(t *testing.T) {
	got, err := provider.PrepareLunaRouteCreate(lrConn(""), "")
	require.ErrorIs(t, err, provider.ErrLunaRouteKeyRequired)
	assert.Equal(t, provider.Connection{}, got)
	for _, k := range []string{"sk-abc123", "sk-or-v1-abcdef", "lr_", " lr_abc", "lr_abc\n", "lr_ab\u00a0c", "lr_ab\x00c"} {
		got, err := provider.PrepareLunaRouteCreate(lrConn(""), k)
		require.ErrorIs(t, err, provider.ErrLunaRouteKeyPrefix, "%q", k)
		assert.Equal(t, provider.Connection{}, got)
		if len(k) > 3 {
			assert.NotContains(t, err.Error(), k, "no key material in errors")
		}
	}
}

func TestPrepareLunaRouteCreate_RejectsNonHostedEndpoint(t *testing.T) {
	for _, base := range []string{"https://openrouter.ai/api/v1", "https://lr_secret@evil.example", "https://gw.lunaroute.com?key=lr_secret"} {
		got, err := provider.PrepareLunaRouteCreate(lrConn(base), "lr_secret")
		require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
		assert.NotContains(t, err.Error(), "lr_secret")
		assert.Equal(t, provider.Connection{}, got)
	}
}

func TestPrepareLunaRouteUpdate(t *testing.T) {
	// Success: metadata-only edits retain the key, including normalized prior providers.
	for _, storedProvider := range []string{"lunaroute", " LUNAROUTE "} {
		c := lrConn("https://gw.lunaroute.com/v1")
		c.Provider = " LunaRoute "
		got, err := provider.PrepareLunaRouteUpdate(c, nil, storedProvider)
		require.NoError(t, err)
		want := c
		want.Provider = "lunaroute"
		want.BaseURL = "https://gw.lunaroute.com"
		assert.Equal(t, want, got)
	}

	// Failure: switching without a key must not reuse a foreign credential.
	for _, storedProvider := range []string{"openrouter", "openai", ""} {
		got, err := provider.PrepareLunaRouteUpdate(lrConn(""), nil, storedProvider)
		require.ErrorIs(t, err, provider.ErrLunaRouteKeyRequired)
		assert.Equal(t, provider.Connection{}, got)
	}

	// A new own key works for both a switch and a same-provider rotation.
	for _, storedProvider := range []string{"openrouter", "lunaroute"} {
		c := lrConn("")
		got, err := provider.PrepareLunaRouteUpdate(c, ptr("lr_new"), storedProvider)
		require.NoError(t, err)
		want := c
		want.BaseURL = "https://gw.lunaroute.com"
		assert.Equal(t, want, got)
	}

	// A supplied invalid key is rejected even when the old provider is lunaroute.
	for _, storedProvider := range []string{"openrouter", "lunaroute"} {
		for _, k := range []string{"sk-abc123", "sk-or-v1-x", "lr_", " lr_abc", "lr_abc\n", "lr_ab\u00a0c", "lr_ab\x00c"} {
			got, err := provider.PrepareLunaRouteUpdate(lrConn(""), ptr(k), storedProvider)
			require.ErrorIs(t, err, provider.ErrLunaRouteKeyPrefix, "%q", k)
			assert.Equal(t, provider.Connection{}, got)
			if len(k) > 3 {
				assert.NotContains(t, err.Error(), k, "no key material in errors")
			}
		}
		got, err := provider.PrepareLunaRouteUpdate(lrConn(""), ptr(""), storedProvider)
		require.ErrorIs(t, err, provider.ErrLunaRouteKeyRequired)
		assert.Equal(t, provider.Connection{}, got)
	}

	// Endpoint enforcement applies to both metadata edits and key replacement.
	for _, key := range []*string{nil, ptr("lr_secret")} {
		got, err := provider.PrepareLunaRouteUpdate(lrConn("https://lr_secret@evil.example"), key, "lunaroute")
		require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
		assert.NotContains(t, err.Error(), "lr_secret")
		assert.Equal(t, provider.Connection{}, got)
	}
}

// Edge: writes unrelated to LunaRoute keep their existing behavior.
func TestPrepareLunaRoute_NonLunaRoutePassesThroughUnchanged(t *testing.T) {
	for _, c := range []provider.Connection{
		{ConfigRevision: 9, Name: "or", Provider: " OpenRouter ", BaseURL: "not even a URL", Extras: map[string]string{"a": "b"}},
		{Name: "other", Provider: "openai", BaseURL: "https://example.com/v1/"},
	} {
		for _, key := range []string{"", "sk-or-v1-x"} {
			got, err := provider.PrepareLunaRouteCreate(c, key)
			require.NoError(t, err)
			assert.Equal(t, c, got)
		}
		for _, key := range []*string{nil, ptr(""), ptr("sk-foreign")} {
			got, err := provider.PrepareLunaRouteUpdate(c, key, "openrouter")
			require.NoError(t, err)
			assert.Equal(t, c, got)
		}
	}
}

func TestPrepareLunaRouteUpdate_SwitchAwayRequiresReplacement(t *testing.T) {
	c := provider.Connection{Name: "lr", Provider: " OpenRouter ", BaseURL: "https://openrouter.ai/api/v1"}
	for _, stored := range []string{"lunaroute", " LunaRoute "} {
		for _, key := range []*string{nil, ptr("")} {
			got, err := provider.PrepareLunaRouteUpdate(c, key, stored)
			require.ErrorIs(t, err, provider.ErrLunaRouteDestinationKeyRequired)
			assert.Equal(t, provider.Connection{}, got)
			assert.Contains(t, err.Error(), "destination provider")
		}
		got, err := provider.PrepareLunaRouteUpdate(c, ptr("sk-or-v1-replacement"), stored)
		require.NoError(t, err)
		assert.Equal(t, c, got)
	}
}

func TestErrConnectionChangedConcurrently_Distinct(t *testing.T) {
	require.Error(t, provider.ErrConnectionChangedConcurrently)
	assert.NotErrorIs(t, provider.ErrConnectionChangedConcurrently, provider.ErrConnectionNotFound)
	assert.NotErrorIs(t, provider.ErrConnectionChangedConcurrently, provider.ErrConnectionExists)
	assert.Contains(t, provider.ErrConnectionChangedConcurrently.Error(), "retry")
}

// Every key the validator accepts is redacted completely by the fallback
// pattern, with no secret list to help. The accepted alphabet is exercised
// character by character, at each position, and in context.
func TestValidLunaRouteKey_AcceptedKeysFullyRedact(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._~+/=-"
	keys := []string{"lr_a", "lr_live_abcDEF123", "lr_" + alphabet, "lr_" + alphabet + alphabet}
	for _, c := range alphabet {
		keys = append(keys, "lr_"+string(c), "lr_ab"+string(c)+"cd", "lr_live_"+string(c)+"tail")
	}
	for _, k := range keys {
		require.True(t, provider.ValidLunaRouteKey(k), k)
		for _, wrap := range []string{"%s", "auth failed for %s today", "key=%s", "(%s)", "\"%s\""} {
			assert.Equal(t, fmt.Sprintf(wrap, "lr_[REDACTED]"), provider.RedactLunaRoute(fmt.Sprintf(wrap, k)), "%q in %q", k, wrap)
		}
		assert.Equal(t, "lr_[REDACTED]", provider.RedactLunaRoute(k), k)
	}
}
