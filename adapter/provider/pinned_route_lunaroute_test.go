package provider

import (
	"testing"

	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePinnedRoute_LunaRouteCodexKeepsProvider(t *testing.T) {
	b := domprovider.ResolvedBinding{Provider: "lunaroute", Model: "glm-5.3", Pinned: true}
	require.NoError(t, validatePinnedRoute(b, "codex"))
	b.Model = "gpt-6.1-sol"
	require.Error(t, validatePinnedRoute(b, "codex"), "LunaRoute codex pins use LunaRoute grammar")
	sub := domprovider.ResolvedBinding{Provider: "openrouter", Model: "gpt-6.1-sol", Pinned: true}
	require.NoError(t, validatePinnedRoute(sub, "codex"), "subscription carve-out unchanged")
}

func TestRouteBinding_NonLunaRouteCodexNeverReveals(t *testing.T) {
	conn := domprovider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	store := &lrConnStore{conn: conn, key: "sk-or-v1-never-revealed"}
	b := routeBinding(domprovider.Profile{Kind: domprovider.ProfileKindAgent, ConnectionRef: "or", Model: "gpt-6.1-sol"}, "codex", conn, store)
	key, err := b.RevealKey(t.Context())
	require.NoError(t, err)
	assert.Empty(t, key)
	assert.Zero(t, store.calls, "subscription route never decrypts a connection key")
}
