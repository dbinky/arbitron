package mcp_test

// profile_authz_e2e_test.go — end-to-end adapter-level authorization tests for
// the profile-editing Phase 4 surface. They mirror authz_e2e_test.go: a real
// ProfileService/ConnectionService (not a stub) is wired behind the MCP handlers,
// an admin vs non-admin identity is injected into the tool context (as the auth
// middleware would in org mode), and each tool must respect the
// read-open-profiles / admin-only-mutation / admin-only-connections split.
//
// Covered: every profile_* and connection_* tool, provider_capabilities,
// live-recache visibility (update → resolver reflects it with no restart), the
// effective-value echo, and the no-secret-in-any-connection-response invariant.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
)

// newProfileAuthzServer wires a server with REAL profile + connection admin
// services over in-memory stores, plus a live ProfileCache so a recache after a
// write is observable through a DynamicProfileResolver reading the same cache.
// It returns the server, the resolver (for recache assertions), and the connection
// store (to seed connections the profile tools reference).
func newProfileAuthzServer(t *testing.T, seed ...provider.Profile) (*adaptmcp.Server, *providersadapter.DynamicProfileResolver, *fakeConnStore) {
	t.Helper()
	pstore := newMemProfileStore(seed...)
	cache := providersadapter.NewProfileCache(pstore)
	require.NoError(t, cache.Load(context.Background()))

	connStore := newFakeConnStore()
	rules := profileRules()
	// The cache satisfies provider.Reloader directly, so the service reloads the
	// SAME cache the resolver reads.
	profileSvc := provider.NewProfileService(pstore, rules, cache)
	connSvc := provider.NewConnectionService(connStore)

	deps := &adaptmcp.Deps{
		ProfileAdmin:    profileSvc,
		ConnectionAdmin: connSvc,
		Connections:     connStore,
		Capabilities:    rules,
	}
	srv := adaptmcp.NewServerWithDeps("test-profile-authz", "0.0.0", deps)
	adaptmcp.RegisterProfileTools(srv)
	adaptmcp.RegisterConnectionTools(srv)

	resolver := providersadapter.NewDynamicProfileResolver(cache, connStore)
	return srv, resolver, connStore
}

// Every profile mutation tool refuses a non-admin and is accepted for an admin.
func TestProfileAuthzE2E_ProfileMutationAdminOnly(t *testing.T) {
	srv, _, _ := newProfileAuthzServer(t, provider.Profile{Alias: "seed", Kind: provider.ProfileKindAgent, Backend: "claude-code"})

	mutations := []struct {
		tool string
		args map[string]any
	}{
		{"profile_create", map[string]any{"alias": "new", "kind": "agent", "backend": "claude-code"}},
		{"profile_update", map[string]any{"alias": "seed", "kind": "agent", "backend": "claude-code"}},
		{"profile_delete", map[string]any{"alias": "seed"}},
	}

	for _, m := range mutations {
		nonAdmin := callTool(t, srv, m.tool, m.args)
		require.True(t, nonAdmin.IsError, "%s must refuse a non-admin", m.tool)
		assert.Contains(t, extractText(t, nonAdmin), "forbidden: admin only", m.tool)
	}

	// Admin path succeeds (create then update then delete, fresh server each time
	// to keep ordering simple).
	for _, m := range mutations {
		s, _, _ := newProfileAuthzServer(t, provider.Profile{Alias: "seed", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
		res := callToolAdmin(t, s, m.tool, m.args)
		require.False(t, res.IsError, "%s must succeed for an admin: %s", m.tool, extractText(t, res))
	}
}

// profile_list / profile_get / provider_capabilities are read-open: a non-admin
// (any authenticated identity) can read them.
func TestProfileAuthzE2E_ReadOpen(t *testing.T) {
	srv, _, _ := newProfileAuthzServer(t, provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "m"})

	for _, tool := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"profile_list", nil, "\"a\""},
		{"profile_get", map[string]any{"alias": "a"}, "\"a\""},
		{"provider_capabilities", nil, "claude-code"},
	} {
		res := callTool(t, srv, tool.name, tool.args) // non-admin
		require.False(t, res.IsError, "%s must be readable by a non-admin: %s", tool.name, extractText(t, res))
		assert.Contains(t, extractText(t, res), tool.want, tool.name)
	}
}

// A live edit through profile_update is visible to the resolver reading the same
// cache, with NO restart — the recache hook fired after the write.
func TestProfileAuthzE2E_RecacheVisibility(t *testing.T) {
	srv, resolver, _ := newProfileAuthzServer(t,
		provider.Profile{Alias: "fast", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "opus-4.8"})

	// Before the edit the resolver sees the seeded (default) effort.
	before, err := resolver.Resolve(context.Background(), "fast")
	require.NoError(t, err)
	assert.Equal(t, provider.Effort(""), before.Effort, "seeded profile carries no effort")

	// Admin edits the profile to context_window=1m, effort=high.
	res := callToolAdmin(t, srv, "profile_update", map[string]any{
		"alias": "fast", "kind": "agent", "backend": "claude-code", "model": "opus-4.8",
		"context_window": "1m", "effort": "high",
	})
	require.False(t, res.IsError, extractText(t, res))

	// The SAME resolver now reflects the edit without a restart.
	after, err := resolver.Resolve(context.Background(), "fast")
	require.NoError(t, err)
	assert.Equal(t, provider.Context1M, after.ContextWindow, "recache made the new context window visible")
	assert.Equal(t, provider.EffortHigh, after.Effort, "recache made the new effort visible")
}

// The create/update echo carries the floored effective values.
func TestProfileAuthzE2E_EffectiveEcho(t *testing.T) {
	srv, _, _ := newProfileAuthzServer(t)

	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "capped", "kind": "agent", "backend": "claude-code", "model": "sonnet-4.6",
		"effort": "xhigh", // sonnet-4.6 floors xhigh -> high
	})
	require.False(t, res.IsError, extractText(t, res))

	var body struct {
		Effective struct {
			ContextWindow string `json:"context_window"`
			Effort        string `json:"effort"`
		} `json:"effective"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &body))
	assert.Equal(t, "high", body.Effective.Effort, "effective echo is the floored value")
}

// Connection tools are admin-only; admin CRUD round-trips and NO secret ever
// appears in any connection response.
func TestProfileAuthzE2E_ConnectionAdminOnlyNoSecret(t *testing.T) {
	srv, _, _ := newProfileAuthzServer(t)

	const secret = "sk-DO-NOT-ECHO-9000"

	// Non-admin is refused on create/get/list.
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"connection_create", map[string]any{"name": "c", "provider": "openrouter", "base_url": "https://x", "api_key": secret}},
		{"connection_get", map[string]any{"name": "c"}},
		{"connection_list", nil},
	} {
		res := callTool(t, srv, tc.tool, tc.args) // non-admin
		require.True(t, res.IsError, "%s must refuse a non-admin", tc.tool)
		assert.Contains(t, extractText(t, res), "forbidden: admin only", tc.tool)
	}

	// Admin create + get + list, asserting the secret never appears.
	createRes := callToolAdmin(t, srv, "connection_create", map[string]any{
		"name": "c", "provider": "openrouter", "base_url": "https://x", "api_key": secret,
	})
	require.False(t, createRes.IsError, extractText(t, createRes))
	assert.NotContains(t, extractText(t, createRes), secret, "create response must not echo the key")

	getRes := callToolAdmin(t, srv, "connection_get", map[string]any{"name": "c"})
	require.False(t, getRes.IsError, extractText(t, getRes))
	assert.NotContains(t, extractText(t, getRes), secret, "get response must not echo the key")

	listRes := callToolAdmin(t, srv, "connection_list", nil)
	require.False(t, listRes.IsError, extractText(t, listRes))
	assert.NotContains(t, extractText(t, listRes), secret, "list response must not echo the key")
}
