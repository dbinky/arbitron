package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cmStore struct {
	provider.ConnectionStore
	key string
}

func (s cmStore) Get(_ context.Context, name string) (provider.Connection, error) {
	if name != "lr" {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}, nil
}
func (s cmStore) Reveal(_ context.Context, name string) (string, error) {
	if name != "lr" {
		return "", provider.ErrConnectionNotFound
	}
	return s.key, nil
}

type cmProbe struct {
	models []string
	err    error
	calls  *int
}

func (p cmProbe) ProbeFor(provider.ResolvedBinding) (provider.ConnectionProbe, error) { return p, nil }
func (p cmProbe) Probe(ctx context.Context, b provider.ResolvedBinding) (provider.ProbeResult, error) {
	if p.calls != nil {
		(*p.calls)++
	}
	if b.Provider != "lunaroute" || b.BaseURL != "https://gw.lunaroute.com" || b.ConnectionRef != "lr" || b.Model != "" || b.RevealKey == nil {
		return provider.ProbeResult{}, provider.ErrSelectionPortsMissing
	}
	key, err := b.RevealKey(ctx)
	if err != nil {
		return provider.ProbeResult{}, err
	}
	if key != "lr_mcp_key" {
		return provider.ProbeResult{}, provider.ErrLunaRouteStoredCredential
	}
	if p.err != nil {
		return provider.ProbeResult{}, p.err
	}
	return provider.ProbeResult{Reachable: true, Models: p.models}, nil
}

func callConnectionModels(t *testing.T, svc *provider.ModelDiscoveryService, ctx context.Context, name string) *gomcp.CallToolResult {
	t.Helper()
	srv := NewServerWithDeps("t", "0", &Deps{ModelDiscovery: svc})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "connection_models"
	req.Params.Arguments = map[string]any{"name": name}
	res, err := connectionModelsHandler(srv)(ctx, req)
	require.NoError(t, err)
	return res
}

var cmAdmin = identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})

func TestConnectionModels_ReturnsView(t *testing.T) {
	calls := 0
	svc := provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{models: []string{"glm-5.3"}, calls: &calls}, provider.CapabilityRules{})
	res := callConnectionModels(t, svc, cmAdmin, "lr")
	require.False(t, res.IsError)
	text := resultText(t, res)
	var view provider.DiscoveryView
	require.NoError(t, json.Unmarshal([]byte(text), &view))
	require.Len(t, view.Models, 1)
	assert.Equal(t, "zhipuai", view.Models[0].Family)
	assert.Equal(t, "unknown", view.Models[0].Backends["opencode"].Support)
	assert.NotContains(t, text, "lr_mcp_key")
	assert.Equal(t, 1, calls)
}

func TestConnectionModels_ErrorMapping(t *testing.T) {
	fail := func(status int) error {
		return provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: status, Body: "echo lr_mcp_key", Secrets: []string{"lr_mcp_key"}})
	}
	cases := []struct {
		name string
		svc  *provider.ModelDiscoveryService
		ctx  context.Context
		conn string
		want string
	}{
		{"forbidden", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{}, provider.CapabilityRules{}), identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser}), "lr", "forbidden: admin only"},
		{"unauthenticated", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{}, provider.CapabilityRules{}), context.Background(), "lr", "authentication required"},
		{"not found", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{}, provider.CapabilityRules{}), cmAdmin, "ghost", `connection "ghost" not found`},
		{"401", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: fail(401)}, provider.CapabilityRules{}), cmAdmin, "lr", "LunaRoute rejected the credential"},
		{"429", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: fail(429)}, provider.CapabilityRules{}), cmAdmin, "lr", "LunaRoute is busy"},
		{"503", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: fail(503)}, provider.CapabilityRules{}), cmAdmin, "lr", "LunaRoute service failure"},
		{"429 spend cap", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: 429, Code: "BYOK_SPEND_CAP_REACHED", Body: "x"})}, provider.CapabilityRules{}), cmAdmin, "lr", "will not succeed on retry"},
		{"404 listing", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: fail(404)}, provider.CapabilityRules{}), cmAdmin, "lr", "cause is unknown"},
		{"foreign stored key", provider.NewModelDiscoveryService(cmStore{key: "sk-or-v1-foreign"}, cmProbe{}, provider.CapabilityRules{}), cmAdmin, "lr", "not an lr_ key"},
		{"endpoint", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: fmt.Errorf("unsafe lr_mcp_key: %w", provider.ErrLunaRouteEndpoint)}, provider.CapabilityRules{}), cmAdmin, "lr", provider.ErrLunaRouteEndpoint.Error()},
		{"unknown", provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: errors.New("unexpected lr_mcp_key sk-or-v1-foreign")}, provider.CapabilityRules{}), cmAdmin, "lr", "cause is unknown"},
		{"unwired", provider.NewModelDiscoveryService(nil, nil, nil), cmAdmin, "lr", "model discovery is not configured"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := callConnectionModels(t, c.svc, c.ctx, c.conn)
			require.True(t, res.IsError)
			text := resultText(t, res)
			assert.Contains(t, text, c.want)
			assert.NotContains(t, text, "lr_mcp_key")
			assert.NotContains(t, text, "sk-or-v1-foreign")
		})
	}
}

func TestConnectionModels_AuthBeforeProbe(t *testing.T) {
	calls := 0
	svc := provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{calls: &calls}, provider.CapabilityRules{})
	for _, ctx := range []context.Context{context.Background(), identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser})} {
		assert.True(t, callConnectionModels(t, svc, ctx, "lr").IsError)
	}
	assert.Zero(t, calls)
}

func TestConnectionModels_Unavailable(t *testing.T) {
	srv := NewServerWithDeps("t", "0", &Deps{})
	req := gomcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "lr"}
	res, err := connectionModelsHandler(srv)(cmAdmin, req)
	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "model discovery not available")
	req.Params.Arguments = map[string]any{}
	res, err = connectionModelsHandler(NewServerWithDeps("t", "0", &Deps{ModelDiscovery: provider.NewModelDiscoveryService(nil, nil, nil)}))(cmAdmin, req)
	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "name is required")
}

func TestConnectionModels_LogsNeverEchoUnclassifiedErrors(t *testing.T) {
	var logs bytes.Buffer
	svc := provider.NewModelDiscoveryService(cmStore{key: "lr_mcp_key"}, cmProbe{err: errors.New("echo lr_mcp_key sk-or-v1-foreign arbitrary-secret-material")}, provider.CapabilityRules{})
	srv := NewServerWithDeps("t", "0", &Deps{ModelDiscovery: svc, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	req := gomcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "lr"}
	res, err := connectionModelsHandler(srv)(cmAdmin, req)
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "cause is unknown")
	assert.Contains(t, logs.String(), "connection_models failed")
	assert.Contains(t, logs.String(), `"connection":"lr"`)
	for _, secret := range []string{"lr_mcp_key", "sk-or-v1-foreign", "arbitrary-secret-material"} {
		assert.NotContains(t, logs.String(), secret)
		assert.NotContains(t, resultText(t, res), secret)
	}
}

func TestConnectionModels_DescriptionGuidesTheLLM(t *testing.T) {
	// One load-bearing phrase per concatenated segment, so a mutated or dropped
	// segment in the LLM-facing text fails (G11).
	for _, s := range []string{
		"(admin only)", "claude-code, opencode, codex, pi", "supported efforts",
		"unknown is not a guarantee", "does NOT imply task or backend compatibility",
		"nothing is recommended or auto-selected", "must name their backend and use pinned selection",
		"keep doer and judge on different vendors", `an "unresolved" family is refused`, "Never returns credentials.",
	} {
		assert.Contains(t, connectionModelsDescription, s)
	}
}
