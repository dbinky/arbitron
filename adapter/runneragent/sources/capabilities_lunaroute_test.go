package sources

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func lrRules() provider.CapabilityRules {
	chat := map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindChat: {Support: provider.SupportSupported, Evidence: "fixture"}}
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{"claude-code": {}}).WithLunaRouteModels(map[string]provider.LunaRouteModel{
		"glm-5.3":            {ID: "glm-5.3", Tasks: chat},
		"deepseek-4.1-flash": {ID: "deepseek-4.1-flash", Tasks: chat},
		"glm-5.3-flash":      {ID: "glm-5.3-flash"}, // chat support unknown: never advertised
	})
}

func lrConfig(models ...string) config.LunaRoute {
	return config.LunaRoute{ConnectionRef: "lr-main", Families: []modelidentity.Family{"zhipuai", "deepseek"}, Models: models, APIKey: "lr_local"}
}

func TestBuildCapabilities_LunaRouteFromConfiguredModelsAndRules(t *testing.T) {
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute})
	require.NoError(t, err)

	caps, reason := BuildCapabilities(runner.CredLunaRoute, "", DiscoverInput{Rules: lrRules(), LunaRoute: lrConfig("glm-5.3-flash", "glm-5.3", "not-in-rules")})
	require.Empty(t, reason)
	require.Len(t, caps, 2, "one chat and one stream capability")
	assert.ElementsMatch(t, []runner.OpType{runner.OpChat, runner.OpStream}, []runner.OpType{caps[0].Op, caps[1].Op})
	for _, c := range caps {
		assert.Equal(t, "", c.Backend)
		assert.Equal(t, provider.CanonicalLunaRoute, c.Provider)
		assert.Equal(t, fp, c.EndpointFingerprint, "the hosted root's fingerprint, the same function the server uses")
		assert.Equal(t, []modelidentity.Family{"zhipuai", "deepseek"}, c.Families)
		assert.Equal(t, []string{"glm-5.3"}, c.Models, "configured AND chat-supported in the rules; unknown support is never advertised")
		assert.True(t, c.ExecutedModel)
		assert.Empty(t, c.Tools)
		assert.Empty(t, c.ToolPolicy)
		assert.Empty(t, c.ContextWindows, "LunaRoute chat has no context tiers")
		assert.Empty(t, c.Efforts)
	}
	caps[0].Models[0] = "mutated"
	assert.Equal(t, "glm-5.3", caps[1].Models[0], "chat and stream do not share slices")

	caps, reason = BuildCapabilities(runner.CredLunaRoute, "", DiscoverInput{Rules: lrRules(), LunaRoute: lrConfig()})
	require.Empty(t, reason)
	assert.Equal(t, []string{"deepseek-4.1-flash", "glm-5.3"}, caps[0].Models, "no configured allowlist: every chat-supported rules model, sorted")

	caps, reason = BuildCapabilities(runner.CredLunaRoute, "", DiscoverInput{Rules: lrRules(), LunaRoute: lrConfig("glm-5.3-flash")})
	assert.Empty(t, caps)
	assert.Equal(t, ReasonLunaRouteModelsUnproven, reason)

	noKey := lrConfig("glm-5.3")
	noKey.APIKey = ""
	caps, reason = BuildCapabilities(runner.CredLunaRoute, "", DiscoverInput{Rules: lrRules(), LunaRoute: noKey})
	assert.Empty(t, caps)
	assert.Equal(t, ReasonCapabilityEvidenceUnproven, reason)
}

func TestDiscover_EndpointFingerprintSeparatesCustomRoutes(t *testing.T) {
	caps, reason := BuildCapabilities(runner.CredLunaRoute, "", DiscoverInput{Rules: lrRules(), LunaRoute: lrConfig("glm-5.3")})
	require.Empty(t, reason)
	chat := caps[0]
	if chat.Op != runner.OpChat {
		chat = caps[1]
	}
	req := func(t *testing.T, b provider.ResolvedBinding) runner.ExecRequirements {
		fp, err := runner.EndpointFingerprint(b)
		require.NoError(t, err)
		return runner.ExecRequirements{Op: runner.OpChat, Provider: b.Provider, EndpointFingerprint: fp, Model: "glm-5.3",
			Group: runner.ModelGroup{Kind: runner.CredLunaRoute, ConnectionRef: "lr-main", Family: "zhipuai"}, RequireExecutedModel: true}
	}
	hosted := req(t, provider.ResolvedBinding{Provider: "lunaroute", ConnectionRef: "lr-main"})
	assert.True(t, runner.CapabilitySatisfies(chat, hosted), "the canonical hosted route matches")
	hostedV1 := req(t, provider.ResolvedBinding{Provider: "LunaRoute", ConnectionRef: "lr-main", BaseURL: provider.LunaRouteHostedRoot + "/v1/"})
	assert.Equal(t, hosted.EndpointFingerprint, hostedV1.EndpointFingerprint, "every accepted spelling of the hosted root is one route")
	assert.True(t, runner.CapabilitySatisfies(chat, hostedV1))

	custom := req(t, provider.ResolvedBinding{Provider: "openrouter", ConnectionRef: "lr-main", BaseURL: "https://gateway.example/api/v1"})
	assert.NotEqual(t, hosted.EndpointFingerprint, custom.EndpointFingerprint, "a same-named custom connection is a different route")
	assert.False(t, runner.CapabilitySatisfies(chat, custom))
	_, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "lunaroute", BaseURL: "https://gateway.example/v1"})
	assert.Error(t, err, "a non-hosted LunaRoute endpoint has no fingerprint, so it can never match")

	other := hosted
	other.Model = "deepseek-4.1-flash"
	assert.False(t, runner.CapabilitySatisfies(chat, other), "exact model only")
	effort := hosted
	effort.Effort = provider.EffortHigh
	assert.False(t, runner.CapabilitySatisfies(chat, effort), "an unadvertised effort never matches")
}

func TestInventory_CapabilitiesForReturnsTheLiveAdvertisedEvidence(t *testing.T) {
	r := &fakeRunner{answers: map[string]cmdAnswer{"--version": {err: errors.New("no claude")}}}
	specs, harness := Discover(context.Background(), DiscoverInput{ClaudeBinary: "/x/claude", Runner: r,
		LunaRoute: lrConfig("glm-5.3"), LunaRouteProbe: &fakeConnProbe{}, Rules: lrRules()})
	require.Len(t, specs, 1)
	inv := New(specs, harness, nil)
	got := inv.Sources(context.Background())
	require.Len(t, got, 1)
	assert.Empty(t, got[0].UnavailableReason)
	require.Len(t, got[0].Capabilities, 2, "hello carries the evidence")
	caps := inv.CapabilitiesFor(runner.CredLunaRoute, "lr-main")
	assert.Equal(t, got[0].Capabilities, caps)
	caps[0].Models[0] = "mutated"
	assert.Equal(t, "glm-5.3", inv.CapabilitiesFor(runner.CredLunaRoute, "lr-main")[0].Models[0], "callers get copies")
	assert.Empty(t, inv.CapabilitiesFor(runner.CredLunaRoute, "other"), "exact credential ref")
	assert.Empty(t, inv.CapabilitiesFor(runner.CredAnthropicSubscription, ""))
}
