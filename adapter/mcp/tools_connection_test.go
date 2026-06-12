package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/provider"
)

// fakeConnStore is an in-memory provider.ConnectionStore for exercising the
// connection-management tools. It records every plaintext key it is handed so a
// test can assert the key is sealed (kept here, never echoed back or logged).
type fakeConnStore struct {
	conns map[string]provider.Connection
	keys  map[string]string // name -> last plaintext key handed to Create/Update
}

func newFakeConnStore() *fakeConnStore {
	return &fakeConnStore{
		conns: map[string]provider.Connection{},
		keys:  map[string]string{},
	}
}

func (s *fakeConnStore) Create(_ context.Context, c provider.Connection, apiKey string) error {
	if err := provider.ValidateConnection(c); err != nil {
		return err
	}
	if _, ok := s.conns[c.Name]; ok {
		return provider.ErrConnectionExists
	}
	s.conns[c.Name] = c
	s.keys[c.Name] = apiKey
	return nil
}

func (s *fakeConnStore) Update(_ context.Context, c provider.Connection, apiKey *string) error {
	if err := provider.ValidateConnection(c); err != nil {
		return err
	}
	if _, ok := s.conns[c.Name]; !ok {
		return provider.ErrConnectionNotFound
	}
	s.conns[c.Name] = c
	if apiKey != nil {
		s.keys[c.Name] = *apiKey
	}
	return nil
}

func (s *fakeConnStore) Delete(_ context.Context, name string) error {
	if _, ok := s.conns[name]; !ok {
		return provider.ErrConnectionNotFound
	}
	delete(s.conns, name)
	delete(s.keys, name)
	return nil
}

func (s *fakeConnStore) Get(_ context.Context, name string) (provider.Connection, error) {
	c, ok := s.conns[name]
	if !ok {
		return provider.Connection{}, provider.ErrConnectionNotFound
	}
	return c, nil
}

func (s *fakeConnStore) List(_ context.Context) ([]provider.Connection, error) {
	out := make([]provider.Connection, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c)
	}
	return out, nil
}

func (s *fakeConnStore) Reveal(_ context.Context, name string) (string, error) {
	k, ok := s.keys[name]
	if !ok {
		return "", provider.ErrConnectionNotFound
	}
	return k, nil
}

func (s *fakeConnStore) Count(_ context.Context) (int, error) { return len(s.conns), nil }

func newConnServer(t *testing.T, store provider.ConnectionStore, logger *slog.Logger) *adaptmcp.Server {
	t.Helper()
	// The connection_* tools are admin-only now: they route through the
	// ConnectionService admin gate. A nil store yields a nil service so the
	// nil-guard ("connection service not available") tests still exercise it.
	var svc *provider.ConnectionService
	if store != nil {
		svc = provider.NewConnectionService(store)
	}
	deps := &adaptmcp.Deps{
		ConnectionAdmin: svc,
		Connections:     store,
		Logger:          logger,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterConnectionTools(srv)
	return srv
}

// --- Tests ---

// Happy path: connection_create seals the key and returns metadata only.
func TestConnectionCreate_HappyPath(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	result := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "azure-prod",
		"provider": "openrouter",
		"base_url": "https://api.openrouter.ai",
		"api_key":  "sk-secret-123",
	})
	require.False(t, result.IsError, extractText(t, result))

	// The store sealed the key.
	assert.Equal(t, "sk-secret-123", store.keys["azure-prod"])

	// The response body carries metadata only -- no key.
	body := extractText(t, result)
	assert.NotContains(t, body, "sk-secret-123")
	assert.Contains(t, body, "azure-prod")
}

// Success: create then get returns metadata with no key; list is metadata only;
// update without api_key preserves the key; delete removes it.
func TestConnectionCRUD_RoundTrip(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	createRes := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c1",
		"provider": "openrouter",
		"base_url": "https://api.openrouter.ai",
		"api_key":  "sk-orig",
	})
	require.False(t, createRes.IsError, extractText(t, createRes))

	getRes := callToolAdmin(t, srv, "connection_get", map[string]any{"name": "c1"})
	require.False(t, getRes.IsError)
	getBody := extractText(t, getRes)
	assert.Contains(t, getBody, "c1")
	assert.NotContains(t, getBody, "sk-orig")
	assert.NotContains(t, strings.ToLower(getBody), "api_key")

	listRes := callToolAdmin(t, srv, "connection_list", nil)
	require.False(t, listRes.IsError)
	assert.NotContains(t, extractText(t, listRes), "sk-orig")

	// Update with no api_key preserves the sealed key.
	updRes := callToolAdmin(t, srv, "connection_update", map[string]any{
		"name":     "c1",
		"provider": "litellm",
		"base_url": "https://litellm.local",
	})
	require.False(t, updRes.IsError, extractText(t, updRes))
	assert.Equal(t, "sk-orig", store.keys["c1"])
	got, _ := store.Get(context.Background(), "c1")
	assert.Equal(t, "litellm", got.Provider)

	// Update with a new api_key re-seals.
	updKeyRes := callToolAdmin(t, srv, "connection_update", map[string]any{
		"name":     "c1",
		"provider": "litellm",
		"base_url": "https://litellm.local",
		"api_key":  "sk-new",
	})
	require.False(t, updKeyRes.IsError, extractText(t, updKeyRes))
	assert.Equal(t, "sk-new", store.keys["c1"])

	delRes := callToolAdmin(t, srv, "connection_delete", map[string]any{"name": "c1"})
	require.False(t, delRes.IsError, extractText(t, delRes))
	_, err := store.Get(context.Background(), "c1")
	assert.ErrorIs(t, err, provider.ErrConnectionNotFound)
}

// Failure: creating a connection whose name already exists surfaces
// ErrConnectionExists through the tool.
func TestConnectionCreate_Duplicate(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	args := map[string]any{
		"name":     "dup",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  "sk-1",
	}
	require.False(t, callToolAdmin(t, srv, "connection_create", args).IsError)

	res := callToolAdmin(t, srv, "connection_create", args)
	assert.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "already exists")
}

// Error: get/update/delete on a missing connection surfaces
// ErrConnectionNotFound through the tool.
func TestConnection_MissingErrors(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_get", map[string]any{"name": "ghost"}},
		{"connection_delete", map[string]any{"name": "ghost"}},
		{"connection_update", map[string]any{"name": "ghost", "provider": "x", "base_url": "y"}},
	} {
		res := callToolAdmin(t, srv, tc.tool, tc.args)
		assert.True(t, res.IsError, tc.tool)
		assert.Contains(t, strings.ToLower(extractText(t, res)), "not found", tc.tool)
	}
}

// Error: an Azure connection_create missing api-version is rejected by
// ValidateConnection through the surface.
func TestConnectionCreate_AzureMissingAPIVersion(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "az",
		"provider": "azure-openai",
		"base_url": "https://az.example.com",
		"api_key":  "sk-az",
	})
	assert.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "api-version")
}

// Edge: extras are accepted and round-tripped on create/get.
func TestConnectionCreate_WithExtras(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "az",
		"provider": "azure-openai",
		"base_url": "https://az.example.com",
		"api_key":  "sk-az",
		"extras":   map[string]any{"api-version": "2024-02-01"},
	})
	require.False(t, res.IsError, extractText(t, res))

	got, err := store.Get(context.Background(), "az")
	require.NoError(t, err)
	assert.Equal(t, "2024-02-01", got.Extras["api-version"])
}

// SECURITY: the create tool never logs the plaintext api_key. Capture all log
// output produced during a create and assert the sentinel never appears; the
// connection NAME may appear.
func TestConnectionCreate_DoesNotLogKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := newFakeConnStore()
	srv := newConnServer(t, store, logger)

	const sentinel = "sk-SENTINEL-DO-NOT-LOG"
	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "redact-me",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  sentinel,
	})
	require.False(t, res.IsError, extractText(t, res))

	logs := buf.String()
	assert.NotContains(t, logs, sentinel, "plaintext key must never be logged")
	// If the create logs at all, it references the connection name only.
	if strings.TrimSpace(logs) != "" {
		assert.Contains(t, logs, "redact-me")
	}
}

// Helper: ensure the connection tools serialize without leaking the key field
// even when the underlying value is present in the store.
func TestConnection_ResponseShape(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	require.False(t, callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c1",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  "sk-zzz",
	}).IsError)

	body := extractText(t, callToolAdmin(t, srv, "connection_get", map[string]any{"name": "c1"}))
	var meta map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &meta))
	_, hasKey := meta["api_key"]
	assert.False(t, hasKey)
	_, hasApiKey := meta["apiKey"]
	assert.False(t, hasApiKey)
}

// Edge: nil Connections store → every tool returns a tool error, not a panic.
func TestConnectionTools_NilStore(t *testing.T) {
	srv := newConnServer(t, nil, nil)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_create", map[string]any{"name": "x", "provider": "p", "base_url": "u", "api_key": "k"}},
		{"connection_update", map[string]any{"name": "x", "provider": "p", "base_url": "u"}},
		{"connection_delete", map[string]any{"name": "x"}},
		{"connection_get", map[string]any{"name": "x"}},
		{"connection_list", nil},
	} {
		res := callTool(t, srv, tc.tool, tc.args)
		assert.True(t, res.IsError, "%s must return an error when store is nil", tc.tool)
		assert.Contains(t, strings.ToLower(extractText(t, res)), "not available", tc.tool)
	}
}

// Edge: empty name for each name-requiring tool returns a tool error.
func TestConnectionTools_EmptyNameRejected(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_create", map[string]any{"name": "", "provider": "p", "base_url": "u", "api_key": "k"}},
		{"connection_update", map[string]any{"name": "", "provider": "p", "base_url": "u"}},
		{"connection_delete", map[string]any{"name": ""}},
		{"connection_get", map[string]any{"name": ""}},
	} {
		res := callTool(t, srv, tc.tool, tc.args)
		assert.True(t, res.IsError, "%s must reject empty name", tc.tool)
		assert.Contains(t, strings.ToLower(extractText(t, res)), "required", tc.tool)
	}
}

// Edge: extras with a non-string value returns a tool error from stringExtras.
func TestConnectionCreate_ExtrasNonStringValue(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "bad-extras",
		"provider": "azure-openai",
		"base_url": "https://x",
		"api_key":  "sk-x",
		"extras":   map[string]any{"api-version": float64(2024)}, // number, not string
	})
	assert.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "string")
}

// Security: connection_update with api_key="" (empty string, explicitly supplied)
// preserves the stored key rather than rotating it. An LLM passing an empty
// key must not clear a valid sealed key.
func TestConnectionUpdate_EmptyAPIKeyPreservesKey(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	const origKey = "sk-original-sealed"
	require.False(t, callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c1",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  origKey,
	}).IsError)

	// Update with an explicitly-empty api_key — must be treated as "omit rotation".
	res := callToolAdmin(t, srv, "connection_update", map[string]any{
		"name":     "c1",
		"provider": "litellm",
		"base_url": "https://x",
		"api_key":  "",
	})
	require.False(t, res.IsError, extractText(t, res))

	// The original sealed key must be unchanged.
	assert.Equal(t, origKey, store.keys["c1"],
		"empty api_key on update must not rotate the stored key")
}

// Security: connection_update logs key_rotated=true when a new key is supplied,
// key_rotated=false when omitted — and never logs the key value itself.
func TestConnectionUpdate_DoesNotLogKeyValue(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := newFakeConnStore()
	srv := newConnServer(t, store, logger)

	const sentinel = "sk-SENTINEL-UPDATE-LOG"
	require.False(t, callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c1",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  "sk-orig",
	}).IsError)
	buf.Reset() // clear create log

	res := callToolAdmin(t, srv, "connection_update", map[string]any{
		"name":     "c1",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  sentinel,
	})
	require.False(t, res.IsError, extractText(t, res))

	logs := buf.String()
	assert.NotContains(t, logs, sentinel, "updated key must never appear in logs")
	assert.Contains(t, logs, "key_rotated")
}

// Security (retrofit): the connection_* tools are admin-only now. A non-admin
// caller is refused on every operation with the standard admin-only message, and
// the refusal happens before the store is touched (no existence leak).
func TestConnectionTools_NonAdminForbidden(t *testing.T) {
	store := newFakeConnStore()
	// Seed a connection so a read could succeed if the gate were absent.
	require.NoError(t, store.Create(context.Background(),
		provider.Connection{Name: "seeded", Provider: "openrouter", BaseURL: "https://x"}, "sk-secret"))
	srv := newConnServer(t, store, nil)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_create", map[string]any{"name": "new", "provider": "openrouter", "base_url": "https://y", "api_key": "k"}},
		{"connection_update", map[string]any{"name": "seeded", "provider": "litellm", "base_url": "https://y"}},
		{"connection_delete", map[string]any{"name": "seeded"}},
		{"connection_get", map[string]any{"name": "seeded"}},
		{"connection_list", nil},
	} {
		// callTool injects a NON-admin identity.
		res := callTool(t, srv, tc.tool, tc.args)
		require.True(t, res.IsError, "%s must refuse a non-admin", tc.tool)
		assert.Contains(t, extractText(t, res), "forbidden: admin only", tc.tool)
	}

	// The non-admin create/delete must not have mutated the store.
	assert.NotContains(t, store.keys, "new", "forbidden create must not persist")
	_, err := store.Get(context.Background(), "seeded")
	assert.NoError(t, err, "forbidden delete must not remove the seeded connection")
}

// Security (retrofit): an unauthenticated caller (no identity in context) is
// rejected with "authentication required" on every connection tool.
func TestConnectionTools_Unauthenticated(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_create", map[string]any{"name": "x", "provider": "p", "base_url": "u", "api_key": "k"}},
		{"connection_get", map[string]any{"name": "x"}},
		{"connection_list", nil},
	} {
		res := callToolCtx(t, srv, context.Background(), tc.tool, tc.args)
		require.True(t, res.IsError, tc.tool)
		assert.Contains(t, extractText(t, res), "authentication required", tc.tool)
	}
}

// Failure: connection_create with an empty api_key returns "api_key is required"
// before the service is reached. An LLM omitting the key must not create a keyless
// connection entry.
func TestConnectionCreate_EmptyAPIKeyRejected(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)
	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name": "c", "provider": "openrouter", "base_url": "https://x", "api_key": "",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "api_key")
	assert.Equal(t, 0, len(store.conns), "nothing stored when api_key is empty")
}

// Edge: stringExtras with a non-object type (e.g. a plain string) returns a tool
// error from the "extras must be an object" guard.
func TestConnectionCreate_ExtrasNonObject(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)
	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  "sk-x",
		"extras":   "not-an-object",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "object")
}

// Error: a bedrock connection with a missing region lands in the connErrorResult
// default branch (no explicit bedrock case), producing "failed to create…".
func TestConnectionCreate_BedrockValidationUsesDefaultBranch(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)
	res := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "br",
		"provider": "bedrock",
		"base_url": "https://bedrock.local",
		"api_key":  "sk-br",
		// missing region + access-key-id in extras → ErrBedrockCredentialsRequired → default
	})
	require.True(t, res.IsError)
	body := strings.ToLower(extractText(t, res))
	assert.Contains(t, body, "failed to create", "bedrock validation falls through to the default error branch")
}

// Security (Pass 2): connection_update response never echoes the rotated api_key.
// The update handler returns metaOf(conn) which has no key field, but this pins
// the behaviour so a future change is caught immediately.
func TestConnectionUpdate_ResponseDoesNotEchoKey(t *testing.T) {
	store := newFakeConnStore()
	srv := newConnServer(t, store, nil)

	const origKey = "sk-ORIG-KEY"
	const newKey = "sk-NEW-ROTATED-KEY"
	require.False(t, callToolAdmin(t, srv, "connection_create", map[string]any{
		"name":     "c",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  origKey,
	}).IsError)

	res := callToolAdmin(t, srv, "connection_update", map[string]any{
		"name":     "c",
		"provider": "openrouter",
		"base_url": "https://x",
		"api_key":  newKey,
	})
	require.False(t, res.IsError, extractText(t, res))
	body := extractText(t, res)
	assert.NotContains(t, body, newKey, "update response must not echo the new key")
	assert.NotContains(t, body, origKey, "update response must not echo the old key")
}
