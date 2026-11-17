package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type lunaRouteGatewayRequest struct {
	Method, URL, Authorization, APIKey string
	Header                             http.Header
	Body                               []byte
}
type lunaRouteGatewayReply struct {
	Status int
	Body   string
	Err    error
}

// fakeLunaRouteGateway replaces only the provider HTTP transport in the real
// root. A chat response requires explicit, exact request expectations.
type fakeLunaRouteGateway struct {
	mu           sync.Mutex
	validKey     string
	models       []string
	replies      map[string]lunaRouteGatewayReply
	requests     []lunaRouteGatewayRequest
	chatExpected map[string]any
}

func newFakeLunaRouteGateway(t *testing.T, validKey string, models ...string) *fakeLunaRouteGateway {
	t.Helper()
	return &fakeLunaRouteGateway{validKey: validKey, models: slices.Clone(models), replies: map[string]lunaRouteGatewayReply{}}
}
func (g *fakeLunaRouteGateway) ExpectChat(model, prompt string, maxTokens int, effort string) {
	expected := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}}
	if maxTokens > 0 {
		expected["max_tokens"] = float64(maxTokens)
	}
	if effort != "" {
		expected["reasoning_effort"] = effort
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chatExpected = expected
}
func (g *fakeLunaRouteGateway) Reply(route string, r lunaRouteGatewayReply) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.replies[route] = r
}
func (g *fakeLunaRouteGateway) Requests() []lunaRouteGatewayRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := slices.Clone(g.requests)
	for i := range out {
		out[i].Header = out[i].Header.Clone()
		out[i].Body = slices.Clone(out[i].Body)
	}
	return out
}
func gatewayResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func (g *fakeLunaRouteGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	route := r.Method + " " + r.URL.Path
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, lunaRouteGatewayRequest{Method: r.Method, URL: r.URL.String(), Authorization: r.Header.Get("Authorization"), APIKey: r.Header.Get("x-api-key"), Header: r.Header.Clone(), Body: body})
	if r.URL.Scheme != "https" || r.URL.Host != "gw.lunaroute.com" || r.URL.RawQuery != "" {
		return nil, fmt.Errorf("fake gateway: unexpected destination")
	}
	if r.Header.Get("Authorization") != "Bearer "+g.validKey || r.Header.Get("x-api-key") != "" || r.Header.Get("LUNAROUTE-API-KEY") != "" {
		return gatewayResponse(r, 401, `{"error":{"code":"INVALID_API_KEY"}}`), nil
	}
	var model string
	if route == "POST /v1/chat/completions" {
		var decoded map[string]any
		if json.Unmarshal(body, &decoded) != nil || g.chatExpected == nil || !reflect.DeepEqual(decoded, g.chatExpected) {
			return gatewayResponse(r, 400, `{"error":{"message":"unexpected chat arguments"}}`), nil
		}
		model, _ = decoded["model"].(string)
		if !slices.Contains(g.models, model) {
			return gatewayResponse(r, 404, `{"error":{"message":"model not found"}}`), nil
		}
	}
	if reply, ok := g.replies[route]; ok {
		if reply.Err != nil {
			return nil, reply.Err
		}
		return gatewayResponse(r, reply.Status, reply.Body), nil
	}
	switch route {
	case "GET /v1/models":
		data := make([]map[string]string, 0, len(g.models))
		for _, m := range g.models {
			data = append(data, map[string]string{"id": m})
		}
		raw, err := json.Marshal(map[string]any{"object": "list", "data": data})
		if err != nil {
			return nil, err
		}
		return gatewayResponse(r, 200, string(raw)), nil
	case "POST /v1/chat/completions":
		return gatewayResponse(r, 200, fmt.Sprintf(`{"model":%q,"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, model)), nil
	default:
		return gatewayResponse(r, 404, `{"error":{"message":"no route"}}`), nil
	}
}

// bootLunaRouteRoot mirrors bootSinkRootWithClose, using the actual root's
// ProviderTransport seam, never a second composition root. Explicit real binary
// paths in extraCfg take precedence over the fixture-only failing Claude stub.
func bootLunaRouteRoot(t *testing.T, extraCfg string, transport http.RoundTripper, setup func(workspaceDir string)) *domainStack {
	t.Helper()
	pinSinkRootEnv(t)
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	setup(ws)
	if !strings.Contains(extraCfg, "binarypath:") {
		bin := filepath.Join(root, "unconfigured-claude")
		require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700))
		if strings.Contains(extraCfg, "llm:\n") {
			extraCfg = strings.Replace(extraCfg, "llm:\n", "llm:\n  binarypath: "+bin+"\n", 1)
		} else {
			extraCfg += "llm:\n  binarypath: " + bin + "\n"
		}
	}
	cfg := "server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: \"" + filepath.Join(root, "arbitron.db") + "\"\n" +
		"workspace:\n  path: \"" + ws + "\"\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: \"" + filepath.Join(root, "arbitron.log") + "\"\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" + extraCfg
	configPath := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	source := &rootCatalogSource{}
	ds, err := initDomainWithModelDependencies(context.Background(), configPath, ModelDependencies{FetchTransport: source, ProviderTransport: transport})
	require.NoError(t, err)
	rootCatalogSources.Store(ds, source)
	t.Cleanup(func() { rootCatalogSources.Delete(ds) })
	t.Cleanup(func() { ds.Close(context.Background()) })
	ds.refreshDeps.FetchTransport = nil
	seedBuiltStack(t, ds)
	return ds
}

// nodeError requires a genuinely failed node and a persisted, nonempty error.
func nodeError(t *testing.T, ds *domainStack, id uuid.UUID) string {
	t.Helper()
	var msg string
	require.NoError(t, ds.db.QueryRow(`SELECT error FROM node_executions WHERE instance_id=? AND status='failed' AND error IS NOT NULL AND error!='' ORDER BY rowid LIMIT 1`, id.String()).Scan(&msg))
	require.NotEmpty(t, msg)
	return msg
}
func successfulNodeOutput(t *testing.T, ds *domainStack, id uuid.UUID, node string) map[string]any {
	t.Helper()
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM node_executions WHERE instance_id=? AND node_id=? AND status='succeeded'`, id.String(), node).Scan(&count))
	require.Equal(t, 1, count)
	var raw string
	require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id=? AND status='succeeded'`, id.String(), node).Scan(&raw))
	var output map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &output))
	return output
}
