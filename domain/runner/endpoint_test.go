package runner_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fingerprintOf(identity string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(identity))) }

func TestEndpointFingerprint_HostedLunaRouteAliases(t *testing.T) {
	for _, raw := range []string{"", "https://gw.lunaroute.com", "HTTPS://GW.LUNAROUTE.COM/", "https://gw.lunaroute.com/v1", "https://gw.lunaroute.com/v1/"} {
		got, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "LunaRoute", BaseURL: raw, Backend: "codex", ConnectionRef: "lr"})
		require.NoError(t, err)
		assert.Equal(t, fingerprintOf("lunaroute\nhttps://gw.lunaroute.com"), got)
	}
}

func TestEndpointFingerprint_RejectsCustomLunaRoute(t *testing.T) {
	for _, raw := range []string{"https://custom.example", "http://gw.lunaroute.com", "https://gw.lunaroute.com:443", "https://gw.lunaroute.com/v2"} {
		got, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "lunaroute", BaseURL: raw})
		require.Error(t, err)
		assert.Empty(t, got)
		assert.NotContains(t, err.Error(), raw)
	}
}

func TestEndpointFingerprint_SubscriptionBackend(t *testing.T) {
	for _, backend := range []string{"claude-code", "codex", "pi", "opencode"} {
		got, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: backend, RevealKey: func(_ context.Context) (string, error) { t.Fatal("must not reveal credentials"); return "", nil }})
		require.NoError(t, err)
		assert.Equal(t, fingerprintOf(provider.Canonicalize("claude-cli")+"\nlocal-subscription:"+backend), got)
	}
}

func TestEndpointFingerprint_GenericRouteChanges(t *testing.T) {
	b := provider.ResolvedBinding{Provider: "openai", ConnectionRef: "prod", BaseURL: "HTTPS://API.EXAMPLE.COM:443/v1/"}
	got, err := runner.EndpointFingerprint(b)
	require.NoError(t, err)
	assert.Equal(t, fingerprintOf("openai\nhttps://api.example.com/v1"), got)
	for _, raw := range []string{"https://api.example.com/v1", "https://API.EXAMPLE.COM:443/v1/"} {
		b.BaseURL = raw
		equivalent, err := runner.EndpointFingerprint(b)
		require.NoError(t, err)
		assert.Equal(t, got, equivalent)
	}
	for _, raw := range []string{"https://other.example.com/v1", "https://api.example.com/v2", "https://api.example.com:8443/v1", "http://api.example.com/v1", ""} {
		b.BaseURL = raw
		different, err := runner.EndpointFingerprint(b)
		require.NoError(t, err)
		assert.NotEqual(t, got, different)
	}
	b.BaseURL = ""
	defaultRoute, err := runner.EndpointFingerprint(b)
	require.NoError(t, err)
	assert.Equal(t, fingerprintOf("openai\nserver-default"), defaultRoute)
	b.BaseURL = "HTTP://API.EXAMPLE.COM:80/"
	plain, err := runner.EndpointFingerprint(b)
	require.NoError(t, err)
	assert.Equal(t, fingerprintOf("openai\nhttp://api.example.com"), plain)
	b.Provider = "anthropic"
	changedProvider, err := runner.EndpointFingerprint(b)
	require.NoError(t, err)
	assert.NotEqual(t, plain, changedProvider)
}

func TestEndpointFingerprint_RejectsSecretBearingURL(t *testing.T) {
	for _, raw := range []string{"https://alice:secret@example.com", "https://example.com?key=secret", "https://example.com#secret", "https://example.com?", "https://example.com#", "://secret", "not-a-url", "https:///secret", "ftp://example.com", "https://example.com:99999"} {
		got, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "openai", ConnectionRef: "prod", BaseURL: raw})
		require.Error(t, err, raw)
		assert.Empty(t, got)
		assert.Equal(t, "runner: invalid endpoint", err.Error())
	}
}
