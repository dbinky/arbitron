package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpCallAs calls a tool on the production MCP server built from ds.mcpDeps().
func mcpCallAs(t *testing.T, ds *domainStack, id identity.Identity) func(name string, args map[string]any) (bool, string) {
	t.Helper()
	return mcpToolCallerAs(t, ds, identity.WithIdentity(context.Background(), id))
}

func TestLunaRouteDiscoveryWiring(t *testing.T) {
	caps := filepath.Join(t.TempDir(), "caps.yaml")
	require.NoError(t, os.WriteFile(caps, []byte(`lunaroute_models:
  glm-5.3:
    tasks:
      agent: {support: supported, evidence: "ev#agent"}
    backends:
      codex: {support: unsupported, evidence: "ev#codex"}
    context_tokens: {tokens: 202752, evidence: "ev#ctx"}
    output_tokens: {tokens: 8192, evidence: "ev#out"}
    efforts:
      chat: {levels: [low, high], evidence: "ev#effort"}
    reasoning: {support: supported, evidence: "ev#reasoning"}
    request_settings:
      temperature: {support: unsupported, evidence: "ev#temperature"}
  claude-sonnet-5-lr1:
    context_tokens: {tokens: 1, evidence: "ev#unlisted"}
`), 0o600))
	gw := newFakeLunaRouteGateway(t, "lr_live_disc", "glm-5.3", "deepseek-4.1-flash-1017", "muse-spark-open")
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+caps+"\n", gw, func(string) {})
	adminCall := mcpCallAs(t, ds, identity.Identity{UserID: localDefaultUser, Role: identity.RoleAdmin})

	isErr, text := adminCall("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": "lr_live_disc"})
	require.False(t, isErr, text)

	isErr, text = adminCall("connection_models", map[string]any{"name": "lr"})
	require.False(t, isErr, text)
	assert.NotContains(t, text, "lr_live_disc")
	var view provider.DiscoveryView
	require.NoError(t, json.Unmarshal([]byte(text), &view))
	assert.Equal(t, "lr", view.Connection)
	assert.Equal(t, "lunaroute", view.Provider)
	assert.True(t, view.ListingSupported)
	assert.Contains(t, view.Note, "does not imply")
	byID := map[string]provider.DiscoveredModel{}
	for _, m := range view.Models {
		byID[m.ID] = m
	}
	require.Len(t, byID, 3, "only listed models; the unlisted table entry is not shown")
	assert.NotContains(t, byID, "claude-sonnet-5-lr1")
	glm := byID["glm-5.3"]
	assert.Equal(t, "zhipuai", glm.Family)
	assert.Equal(t, provider.LimitView{Known: true, Tokens: 202752, Evidence: "ev#ctx"}, glm.ContextTokens, "table fact from the root's capability file")
	assert.Equal(t, provider.LimitView{Known: true, Tokens: 8192, Evidence: "ev#out"}, glm.OutputTokens)
	assert.Equal(t, provider.FactView{Support: "supported", Evidence: "ev#agent"}, glm.Tasks["agent"])
	assert.Equal(t, provider.FactView{Support: "unsupported", Evidence: "ev#codex"}, glm.Backends["codex"])
	assert.Equal(t, provider.EffortView{Known: true, Levels: []string{"low", "high"}, Evidence: "ev#effort"}, glm.Efforts["chat"])
	assert.Equal(t, provider.FactView{Support: "supported", Evidence: "ev#reasoning"}, glm.Reasoning)
	assert.Equal(t, provider.FactView{Support: "unsupported", Evidence: "ev#temperature"}, glm.RequestSettings["temperature"])
	assert.Equal(t, provider.FactView{Support: "unknown"}, glm.RequestSettings["output_schema"])
	assert.Equal(t, "unknown", glm.Backends["opencode"].Support)
	assert.Equal(t, "unknown", byID["deepseek-4.1-flash-1017"].Tasks["chat"].Support)
	assert.Equal(t, "unresolved", byID["muse-spark-open"].Family)

	// SINK: the gateway saw exactly one listing call with the stored lr_ key.
	reqs := gw.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "GET", reqs[0].Method)
	assert.Equal(t, "https://gw.lunaroute.com/v1/models", reqs[0].URL)
	assert.Equal(t, "Bearer lr_live_disc", reqs[0].Authorization)
	assert.Empty(t, reqs[0].Body)
	for _, header := range []string{"x-api-key", "LUNAROUTE-API-KEY", "LUNAROUTE-PROJECT-ID", "anthropic-version"} {
		assert.Empty(t, reqs[0].Header.Get(header))
	}

	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"credentials", `{"error":"bad lr_live_disc"}`, "LunaRoute rejected the credential", 401},
		{"service", `{"error":"down lr_live_disc"}`, "LunaRoute service failure", 503},
		{"busy", `{"error":"busy lr_live_disc"}`, "LunaRoute is busy", 429},
		{"refused", `{"type":"error","error":{"code":"BYOK_SPEND_CAP_REACHED","message":"lr_live_disc"}}`, "will not succeed on retry", 429},
		{"uncertain", `{"error":"lr_live_disc"}`, "cause is unknown", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(gw.Requests())
			gw.Reply("GET /v1/models", lunaRouteGatewayReply{Status: tc.status, Body: tc.body})
			isErr, text := adminCall("connection_models", map[string]any{"name": "lr"})
			assert.True(t, isErr)
			assert.Contains(t, text, tc.want)
			assert.NotContains(t, text, "lr_live_disc")
			assert.Len(t, gw.Requests(), before+1, "one listing; no retries or fallback")
		})
	}

	before := len(gw.Requests())
	isErr, text = adminCall("connection_models", map[string]any{"name": "ghost"})
	assert.True(t, isErr)
	assert.Contains(t, text, `connection "ghost" not found`)
	assert.Len(t, gw.Requests(), before)
	userCall := mcpCallAs(t, ds, identity.Identity{UserID: localDefaultUser, Role: identity.RoleUser})
	isErr, text = userCall("connection_models", map[string]any{"name": "lr"})
	assert.True(t, isErr)
	assert.Contains(t, text, "forbidden: admin only")
	assert.Len(t, gw.Requests(), before, "authz precedes any gateway call")

	// One serve-path factory feeds discovery AND the agent wrapper. P3-T3
	// exercises the real diagnosis sink once a backend is enabled.
	require.NotNil(t, ds.probeFactory)
	require.NotNil(t, ds.providerFactory)
	assert.Same(t, ds.probeFactory, ds.providerFactory.ProbeSelector())
}

func TestLunaRouteDiscoveryWiringEmbeddedDefaults(t *testing.T) {
	// Source-reviewed defaults currently mark all static facts unknown. They
	// must stay unknown in discovery, not acquire invented limits or support.
	rules, err := configadapter.LoadCapabilityRules("")
	require.NoError(t, err)
	fact, ok := rules.LunaRouteModel("glm-5.3")
	require.True(t, ok)
	assert.False(t, fact.ContextTokens.Known())
	assert.False(t, fact.OutputTokens.Known())
	gw := newFakeLunaRouteGateway(t, "lr_default_disc", "glm-5.3")
	ds := bootLunaRouteRoot(t, "", gw, func(string) {})
	call := mcpCallAs(t, ds, identity.Identity{UserID: localDefaultUser, Role: identity.RoleAdmin})
	isErr, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "", "api_key": "lr_default_disc"})
	require.False(t, isErr, text)
	isErr, text = call("connection_models", map[string]any{"name": "lr"})
	require.False(t, isErr, text)
	assert.NotContains(t, text, "lr_default_disc")
	var view provider.DiscoveryView
	require.NoError(t, json.Unmarshal([]byte(text), &view))
	require.Len(t, view.Models, 1)
	m := view.Models[0]
	assert.Equal(t, "glm-5.3", m.ID)
	assert.Equal(t, "zhipuai", m.Family)
	assert.Equal(t, provider.LimitView{}, m.ContextTokens)
	assert.Equal(t, provider.LimitView{}, m.OutputTokens)
	assert.Equal(t, provider.FactView{Support: "unknown"}, m.Reasoning)
	for _, kind := range []string{"chat", "agent"} {
		assert.Equal(t, provider.FactView{Support: "unknown"}, m.Tasks[kind])
	}
	for _, backend := range provider.LunaRouteBackends {
		assert.Equal(t, provider.FactView{Support: "unknown"}, m.Backends[backend])
	}
	for _, setting := range []string{"temperature", "output_schema"} {
		assert.Equal(t, provider.FactView{Support: "unknown"}, m.RequestSettings[setting])
	}
	require.Len(t, gw.Requests(), 1)
	assert.Equal(t, "Bearer lr_default_disc", gw.Requests()[0].Authorization)
	assert.Equal(t, "https://gw.lunaroute.com/v1/models", gw.Requests()[0].URL)
}
