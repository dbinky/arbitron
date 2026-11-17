package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliLRStore struct {
	provider.ConnectionStore
	conn provider.Connection
	key  string
}

func (s cliLRStore) Get(_ context.Context, name string) (provider.Connection, error) {
	if name != s.conn.Name {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return s.conn, nil
}
func (s cliLRStore) Reveal(_ context.Context, name string) (string, error) {
	if name != s.conn.Name {
		return "", provider.ErrConnectionNotFound
	}
	return s.key, nil
}

type cliLRGateway struct {
	urls  []string
	empty bool
}

func (g *cliLRGateway) RoundTrip(r *http.Request) (*http.Response, error) {
	g.urls = append(g.urls, r.URL.String())
	status, body := 200, `{"data":[{"id":"glm-5.3"},{"id":"claude-sonnet-5-lr1"}]}`
	if g.empty {
		body = `{"data":[]}`
	}
	if r.Method != http.MethodGet || r.URL.String() != "https://gw.lunaroute.com/v1/models" {
		status, body = 404, `{"error":"wrong route"}`
	} else if r.Header.Get("Authorization") != "Bearer lr_cli_key" {
		status, body = 401, `{"error":{"code":"INVALID_API_KEY","message":"echo `+r.Header.Get("Authorization")+`"}}`
	}
	for _, name := range []string{"x-api-key", "LUNAROUTE-API-KEY", "anthropic-version", "X-LunaRoute-Client"} {
		if r.Header.Get(name) != "" {
			status, body = 400, `{"error":"unexpected header"}`
		}
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

func TestRunConnectionValidate_LunaRoute(t *testing.T) {
	g := &cliLRGateway{}
	sel := adaptprobe.NewProbeFactory(adaptprobe.Deps{HTTPClient: &http.Client{Transport: g}})
	conn := provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}
	var out, logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	require.NoError(t, runConnectionValidate(context.Background(), validateDeps{store: cliLRStore{conn: conn, key: "lr_cli_key"}, selector: sel}, "lr", &out))
	assert.Equal(t, "connection \"lr\" (lunaroute): reachable. 2 model(s):\n  - glm-5.3\n  - claude-sonnet-5-lr1\n", out.String())
	assert.Equal(t, []string{"https://gw.lunaroute.com/v1/models"}, g.urls)

	out.Reset()
	err := runConnectionValidate(context.Background(), validateDeps{store: cliLRStore{conn: conn, key: "lr_wrong"}, selector: sel}, "lr", &out)
	require.ErrorIs(t, err, provider.ErrLunaRouteCredentialsRejected)
	assert.NotContains(t, err.Error(), "lr_wrong")
	assert.Empty(t, out.String(), "no success output on a failed probe")

	err = runConnectionValidate(context.Background(), validateDeps{store: cliLRStore{conn: conn, key: "sk-or-v1-foreign"}, selector: sel}, "lr", &out)
	require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
	assert.Len(t, g.urls, 2, "foreign key was never sent")
	assert.Empty(t, out.String())

	g.empty = true
	require.NoError(t, runConnectionValidate(context.Background(), validateDeps{store: cliLRStore{conn: conn, key: "lr_cli_key"}, selector: sel}, "lr", &out))
	assert.Equal(t, "connection \"lr\" (lunaroute): reachable, no models.\n", out.String())
	for _, key := range []string{"lr_cli_key", "lr_wrong", "sk-or-v1-foreign"} {
		assert.NotContains(t, out.String(), key)
		assert.NotContains(t, logs.String(), key)
	}
}

// Exercise Cobra -> buildValidateDeps -> encrypted SQLite Get/Reveal -> the
// production ProbeFactory -> HTTP header, then the actual CLI output sink.
// Only the outbound transport is replaced; neither bindings nor probes are
// constructed by the test on the command's execution path.
func TestConnectionValidate_LunaRouteProductionWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	dir := filepath.Join(home, ".arbitron")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("database:\n  path: "+filepath.Join(dir, "arbitron.db")+"\n"), 0600))
	ctx := context.Background()
	deps, closeDB, err := buildValidateDeps(ctx)
	require.NoError(t, err)
	conn := provider.Connection{Name: "lr-persisted", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com/v1/"}
	err = deps.store.Create(ctx, conn, "lr_cli_key")
	closeDB()
	require.NoError(t, err)

	g := &cliLRGateway{}
	original := http.DefaultTransport
	http.DefaultTransport = g
	t.Cleanup(func() { http.DefaultTransport = original })
	var out bytes.Buffer
	cmd := newConnectionCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"validate", conn.Name})
	require.NoError(t, cmd.ExecuteContext(ctx))
	assert.Equal(t, "connection \"lr-persisted\" (lunaroute): reachable. 2 model(s):\n  - glm-5.3\n  - claude-sonnet-5-lr1\n", out.String())
	assert.Equal(t, []string{"https://gw.lunaroute.com/v1/models"}, g.urls)
	assert.NotContains(t, out.String(), "lr_cli_key")
}
