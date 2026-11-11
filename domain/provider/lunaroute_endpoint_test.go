package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeLunaRouteBaseURL_AcceptsHostedSpellings(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"https://gw.lunaroute.com",
		"https://gw.lunaroute.com/",
		"https://gw.lunaroute.com/v1",
		"https://gw.lunaroute.com/v1/",
		"https://GW.LunaRoute.com/v1",
		"HTTPS://gw.lunaroute.com",
		"  https://gw.lunaroute.com/  ",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := provider.NormalizeLunaRouteBaseURL(raw)
			require.NoError(t, err)
			assert.Equal(t, "https://gw.lunaroute.com", got)
			assert.Equal(t, provider.LunaRouteHostedRoot, got)
		})
	}
}

func TestNormalizeLunaRouteBaseURL_RejectsOtherDestinations(t *testing.T) {
	for _, raw := range []string{
		"http://gw.lunaroute.com",
		"https://gw.lunaroute.com:443",
		"https://gw.lunaroute.com:",
		"https://gw.lunaroute.com:8443/v1",
		"https://user:pw@gw.lunaroute.com",
		"https://gw.lunaroute.com/v2",
		"https://gw.lunaroute.com/v1/chat/completions",
		"https://gw.lunaroute.com/v%31",
		"https://gw.lunaroute.com?x=1",
		"https://gw.lunaroute.com?",
		"https://gw.lunaroute.com/#frag",
		"https://gw.lunaroute.com/#",
		"https://gw.lunaroute.com.",
		"https://evil.example/gw.lunaroute.com",
		"https://gw.lunaroute.com.evil.example",
		"https://openrouter.ai/api/v1",
		"ftp://gw.lunaroute.com",
		"gw.lunaroute.com",
		"://bad",
		"https://user:private-secret@gw.lunaroute.com/%zz",
		"https:gw.lunaroute.com",
	} {
		t.Run(raw, func(t *testing.T) {
			for _, normalize := range []func(string) (string, error){
				provider.NormalizeLunaRouteBaseURL,
				provider.LunaRouteMessagesBaseURL,
				provider.LunaRouteOpenAIBaseURL,
			} {
				got, err := normalize(raw)
				require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
				assert.Empty(t, got)
				assert.Equal(t, provider.ErrLunaRouteEndpoint.Error(), err.Error(), "only the fixed safe diagnostic is returned, never the submitted URL")
				assert.NotContains(t, err.Error(), "pw@")
				assert.NotContains(t, err.Error(), "private-secret")
			}
		})
	}
}

func TestLunaRouteDialectURLs(t *testing.T) {
	for _, raw := range []string{
		"", "   ", "https://gw.lunaroute.com", "https://gw.lunaroute.com/",
		"https://gw.lunaroute.com/v1", "https://gw.lunaroute.com/v1/",
		"https://GW.LunaRoute.com/v1", "HTTPS://gw.lunaroute.com",
		"  https://gw.lunaroute.com/  ",
	} {
		t.Run(raw, func(t *testing.T) {
			msgs, err := provider.LunaRouteMessagesBaseURL(raw)
			require.NoError(t, err)
			assert.Equal(t, "https://gw.lunaroute.com", msgs, "Messages clients append /v1/messages themselves")
			oai, err := provider.LunaRouteOpenAIBaseURL(raw)
			require.NoError(t, err)
			assert.Equal(t, "https://gw.lunaroute.com/v1", oai, "exactly one /v1, never doubled")
		})
	}
	got, err := provider.LunaRouteOpenAIBaseURL("https://example.com")
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
	assert.Empty(t, got)
	got, err = provider.LunaRouteMessagesBaseURL("https://example.com")
	require.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
	assert.Empty(t, got)
}

func TestIsLunaRoute(t *testing.T) {
	assert.True(t, provider.IsLunaRoute("lunaroute"))
	assert.True(t, provider.IsLunaRoute("  LunaRoute "))
	assert.False(t, provider.IsLunaRoute("openrouter"))
	assert.False(t, provider.IsLunaRoute(""))
	assert.False(t, provider.IsLunaRoute("luna-route"))
}
