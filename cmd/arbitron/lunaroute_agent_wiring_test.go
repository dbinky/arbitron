package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrRootKey = "lr_live_root_key"

// Only the selected credential and the canonical listing route are accepted.
// The log records auth equality, never the credential itself.
type agentListingGateway struct {
	mu       sync.Mutex
	models   []string
	requests []string
}

func (g *agentListingGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	auth := "bad"
	if r.Header.Get("Authorization") == "Bearer "+lrRootKey && r.Header.Get("x-api-key") == "" && r.Header.Get("LUNAROUTE-API-KEY") == "" {
		auth = "ok"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r.Method+" "+r.URL.String()+" auth="+auth)
	reply := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	if r.Method != http.MethodGet || r.URL.String() != provider.LunaRouteHostedRoot+"/v1/models" {
		return reply(404, `{}`)
	}
	if auth != "ok" {
		return reply(401, `{"code":"INVALID_API_KEY"}`)
	}
	data := make([]map[string]string, 0, len(g.models))
	for _, m := range g.models {
		data = append(data, map[string]string{"id": m})
	}
	raw, err := json.Marshal(map[string]any{"object": "list", "data": data})
	if err != nil {
		return nil, err
	}
	return reply(200, string(raw))
}
func (g *agentListingGateway) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}
func writeLunaRouteCaps(t *testing.T) string {
	t.Helper()
	return lunaRouteCapsFile(t, `lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: 202752, evidence: "test-fixture#glm-5.3"}
    output_tokens: {tokens: 32768, evidence: "test-fixture#glm-5.3"}
    reasoning: {support: unsupported, evidence: "test-fixture#reasoning"}
  deepseek-4.1-flash: {} # known executable, deliberately unknown fixture limits
`)
}
func TestLunaRouteAgentProfileSaveWiring(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+writeLunaRouteCaps(t)+"\n", gw, func(string) {})
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	for _, backend := range []string{"codex", "pi"} {
		alias := "lr-" + backend
		bad, text = call("profile_create", map[string]any{"alias": alias, "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": backend})
		require.False(t, bad, text)
		var model, gotBackend, connection string
		require.NoError(t, ds.db.QueryRow(`SELECT model, backend, connection_ref FROM profiles WHERE alias=?`, alias).Scan(&model, &gotBackend, &connection))
		assert.Equal(t, "glm-5.3", model)
		assert.Equal(t, backend, gotBackend)
		assert.Equal(t, "lr", connection)
	}
	for _, tc := range []struct{ alias, model, backend, setting, value, want string }{
		{"pi-unknown", "deepseek-4.1-flash", "pi", "", "", "pi on LunaRoute needs known context and output limits"},
		{"tier", "glm-5.3", "pi", "context_window", "200k", "Claude-native switch"},
		{"codex-effort", "glm-5.3", "codex", "effort", "none", "no exact emission on codex"},
		{"numeric", "glm-999.9", "codex", "", "", "reviewed fixed LunaRoute id"},
		{"revoked", "claude-sonnet-5-lr1", "pi", "", "", "reviewed fixed LunaRoute id"},
	} {
		args := map[string]any{"alias": "lr-" + tc.alias, "kind": "agent", "connection_ref": "lr", "model": tc.model, "backend": tc.backend}
		if tc.setting != "" {
			args[tc.setting] = tc.value
		}
		bad, text = call("profile_create", args)
		assert.True(t, bad, tc.alias)
		assert.Contains(t, text, tc.want)
		assert.NotContains(t, text, lrRootKey)
		var n int
		require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias=?`, "lr-"+tc.alias).Scan(&n))
		assert.Zero(t, n)
	}
	assert.Empty(t, gw.seen(), "save performs no gateway I/O")
}
