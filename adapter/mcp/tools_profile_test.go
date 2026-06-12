package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/provider"
)

// memProfileStore is an in-memory provider.ProfileStore for the profile-tool
// tests. (The service-level store lives in the provider package; this one backs
// the MCP handler tests.)
type memProfileStore struct {
	byAlias map[string]provider.Profile
}

func newMemProfileStore(seed ...provider.Profile) *memProfileStore {
	s := &memProfileStore{byAlias: map[string]provider.Profile{}}
	for _, p := range seed {
		s.byAlias[p.Alias] = p
	}
	return s
}

func (s *memProfileStore) Create(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; ok {
		return provider.ErrProfileExists
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *memProfileStore) Update(_ context.Context, p provider.Profile) error {
	if _, ok := s.byAlias[p.Alias]; !ok {
		return provider.ErrProfileNotFound
	}
	s.byAlias[p.Alias] = p
	return nil
}
func (s *memProfileStore) Delete(_ context.Context, alias string) error {
	if _, ok := s.byAlias[alias]; !ok {
		return provider.ErrProfileNotFound
	}
	delete(s.byAlias, alias)
	return nil
}
func (s *memProfileStore) Get(_ context.Context, alias string) (provider.Profile, error) {
	p, ok := s.byAlias[alias]
	if !ok {
		return provider.Profile{}, provider.ErrProfileNotFound
	}
	return p, nil
}
func (s *memProfileStore) List(_ context.Context) ([]provider.Profile, error) {
	out := make([]provider.Profile, 0, len(s.byAlias))
	for _, p := range s.byAlias {
		out = append(out, p)
	}
	return out, nil
}
func (s *memProfileStore) Count(_ context.Context) (int, error) { return len(s.byAlias), nil }

var _ provider.ProfileStore = (*memProfileStore)(nil)

// noopReloader satisfies provider.Reloader for handler tests where recache
// visibility is asserted separately (the e2e test exercises a real cache).
type noopReloader struct{ calls int }

func (r *noopReloader) Reload(context.Context) error { r.calls++; return nil }

// profileRules is the capability ruleset the profile-tool tests validate against:
// a claude-code with full axes (sonnet-4.6 caps effort at high) and a
// context-less codex.
func profileRules() provider.CapabilityRules {
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K, provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax},
			ModelOverrides: map[string]provider.ModelCapability{
				"sonnet-4.6": {
					EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortMax},
					EffortFloor:  map[provider.Effort]provider.Effort{provider.EffortXHigh: provider.EffortHigh},
				},
			},
		},
		"claude-cli": {
			Backend:      "claude-cli",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context200K, provider.Context1M},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax},
		},
		"codex": {
			Backend:      "codex",
			ContextTiers: []provider.ContextWindow{provider.ContextDefault},
			EffortLevels: []provider.Effort{provider.EffortDefault, provider.EffortNone, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh},
		},
	})
}

// newProfileServer wires an MCP server with the profile admin service + rules and
// registers both the profile tools and provider_capabilities.
func newProfileServer(t *testing.T, store provider.ProfileStore, reloader provider.Reloader) *adaptmcp.Server {
	t.Helper()
	rules := profileRules()
	svc := provider.NewProfileService(store, rules, reloader)
	deps := &adaptmcp.Deps{ProfileAdmin: svc, Capabilities: rules}
	srv := adaptmcp.NewServerWithDeps("test-profile", "0.0.0", deps)
	adaptmcp.RegisterProfileTools(srv)
	return srv
}

// Admin create echoes the stored profile and the floored effective values.
func TestProfileCreate_AdminEchoesEffective(t *testing.T) {
	store := newMemProfileStore()
	srv := newProfileServer(t, store, &noopReloader{})

	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias":   "fast",
		"kind":    "agent",
		"backend": "claude-code",
		"model":   "sonnet-4.6",
		"effort":  "xhigh", // floors to high on sonnet-4.6
	})
	require.False(t, res.IsError, extractText(t, res))

	var body struct {
		Status    string `json:"status"`
		Profile   map[string]any
		Effective struct {
			ContextWindow string `json:"context_window"`
			Effort        string `json:"effort"`
		} `json:"effective"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &body))
	assert.Equal(t, "created", body.Status)
	assert.Equal(t, "high", body.Effective.Effort, "xhigh floors to high on sonnet-4.6")

	// The store actually holds the requested (un-floored) value.
	got, err := store.Get(context.Background(), "fast")
	require.NoError(t, err)
	assert.Equal(t, provider.EffortXHigh, got.Effort)
}

// Non-admin profile_create is refused with the standard admin-only message.
func TestProfileCreate_NonAdminForbidden(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callTool(t, srv, "profile_create", map[string]any{
		"alias": "x", "kind": "agent", "backend": "claude-code",
	})
	require.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "forbidden: admin only")
}

// An inapplicable axis is rejected with the specific axis message; nothing is
// persisted.
func TestProfileCreate_InapplicableAxis(t *testing.T) {
	store := newMemProfileStore()
	srv := newProfileServer(t, store, &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "bad", "kind": "agent", "backend": "codex", "context_window": "1m",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "not applicable")
	_, err := store.Get(context.Background(), "bad")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// A malformed context_window string maps to the invalid-context tool error.
func TestProfileCreate_InvalidAxisString(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "bad", "kind": "agent", "backend": "claude-code", "context_window": "999k",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "context window")
}

// Duplicate alias surfaces ErrProfileExists -> "profile already exists".
func TestProfileCreate_Duplicate(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "dup", Kind: provider.ProfileKindChat})
	srv := newProfileServer(t, store, &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "dup", "kind": "agent", "backend": "claude-code",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "already exists")
}

// profile_update admin replaces by alias and echoes effective; non-admin refused.
func TestProfileUpdate_AdminAndForbidden(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "p", Kind: provider.ProfileKindAgent, Backend: "claude-code"})
	srv := newProfileServer(t, store, &noopReloader{})

	res := callToolAdmin(t, srv, "profile_update", map[string]any{
		"alias": "p", "kind": "agent", "backend": "claude-code", "model": "opus-4.8", "context_window": "1m", "effort": "high",
	})
	require.False(t, res.IsError, extractText(t, res))
	assert.Contains(t, extractText(t, res), "\"updated\"")
	got, _ := store.Get(context.Background(), "p")
	assert.Equal(t, provider.Context1M, got.ContextWindow)

	resN := callTool(t, srv, "profile_update", map[string]any{
		"alias": "p", "kind": "agent", "backend": "claude-code",
	})
	require.True(t, resN.IsError)
	assert.Contains(t, extractText(t, resN), "forbidden: admin only")
}

// profile_update on a missing alias -> "profile not found".
func TestProfileUpdate_Missing(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_update", map[string]any{
		"alias": "ghost", "kind": "agent", "backend": "claude-code",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "not found")
}

// profile_delete admin removes; non-admin refused; missing -> not found.
func TestProfileDelete(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "p", Kind: provider.ProfileKindChat})
	srv := newProfileServer(t, store, &noopReloader{})

	resN := callTool(t, srv, "profile_delete", map[string]any{"alias": "p"})
	require.True(t, resN.IsError)
	assert.Contains(t, extractText(t, resN), "forbidden: admin only")

	res := callToolAdmin(t, srv, "profile_delete", map[string]any{"alias": "p"})
	require.False(t, res.IsError, extractText(t, res))
	_, err := store.Get(context.Background(), "p")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)

	resMissing := callToolAdmin(t, srv, "profile_delete", map[string]any{"alias": "p"})
	require.True(t, resMissing.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, resMissing)), "not found")
}

// profile_list / profile_get are read-open: a non-admin can read.
func TestProfileListGet_ReadOpen(t *testing.T) {
	store := newMemProfileStore(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat, Model: "m"})
	srv := newProfileServer(t, store, &noopReloader{})

	listRes := callTool(t, srv, "profile_list", nil)
	require.False(t, listRes.IsError, extractText(t, listRes))
	assert.Contains(t, extractText(t, listRes), "\"a\"")

	getRes := callTool(t, srv, "profile_get", map[string]any{"alias": "a"})
	require.False(t, getRes.IsError, extractText(t, getRes))
	assert.Contains(t, extractText(t, getRes), "\"a\"")
}

// provider_capabilities is read-open and lists the configured backends.
func TestProviderCapabilities_ReadOpen(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callTool(t, srv, "provider_capabilities", nil)
	require.False(t, res.IsError, extractText(t, res))
	body := extractText(t, res)
	assert.Contains(t, body, "claude-code")
	assert.Contains(t, body, "codex")
}

// Error: an unauthenticated caller (no identity in context) hits the
// ErrUnauthenticated branch of profileErrorResult on both read and write tools.
func TestProfileTools_Unauthenticated(t *testing.T) {
	srv := newProfileServer(t,
		newMemProfileStore(provider.Profile{Alias: "a", Kind: provider.ProfileKindChat}),
		&noopReloader{},
	)
	ctx := context.Background() // no identity → ErrUnauthenticated
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"profile_create", map[string]any{"alias": "x", "kind": "agent", "backend": "claude-code"}},
		{"profile_update", map[string]any{"alias": "a", "kind": "chat"}},
		{"profile_delete", map[string]any{"alias": "a"}},
		{"profile_list", nil},
		{"profile_get", map[string]any{"alias": "a"}},
		{"provider_capabilities", nil},
	} {
		res := callToolCtx(t, srv, ctx, tc.tool, tc.args)
		require.True(t, res.IsError, "%s must fail for unauthenticated caller", tc.tool)
		assert.Contains(t, strings.ToLower(extractText(t, res)), "authentication required", tc.tool)
	}
}

// Edge: profile_get with an empty alias string is caught before the service
// call and returns "alias is required".
func TestProfileGet_EmptyAlias(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_get", map[string]any{"alias": ""})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "alias is required")
}

// Error: profile_get on a missing alias maps to "profile not found".
func TestProfileGet_NotFound(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_get", map[string]any{"alias": "ghost"})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "not found")
}

// Failure: profileFromArgs rejects an empty alias before the service is reached.
func TestProfileCreate_EmptyAlias(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{"alias": "", "kind": "agent"})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "alias is required")
}

// Failure: profileFromArgs rejects an empty kind before the service is reached.
func TestProfileCreate_EmptyKind(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{"alias": "x", "kind": ""})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "kind is required")
}

// Edge: a non-string element in allowed_tools is caught by stringSliceArg and
// returns a tool error before the service is reached.
func TestProfileCreate_NonStringAllowedToolsElement(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "x", "kind": "agent", "backend": "claude-code",
		"allowed_tools": []any{"bash", 42}, // 42 is not a string
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "string")
}

// Pass 2: provider_capabilities serializes model_overrides and effort_floor so
// callers can discover flooring rules before writing — verifies the full
// backendCapabilityBody → modelCapabilityBody → EffortFloor chain.
func TestProviderCapabilities_IncludesModelOverridesAndFloor(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callTool(t, srv, "provider_capabilities", nil)
	require.False(t, res.IsError, extractText(t, res))
	body := extractText(t, res)
	assert.Contains(t, body, "model_overrides", "model overrides must be serialized")
	assert.Contains(t, body, "effort_floor", "effort floor must be serialized so callers can discover flooring")
	assert.Contains(t, body, "sonnet-4.6", "the override model must appear by name")
	// The floor entry "xhigh" -> "high" must be present.
	assert.Contains(t, body, "xhigh", "xhigh must appear as a floor source key")
	assert.Contains(t, body, "\"high\"", "high must appear as a floor target value")
}

// Security (Pass 2): profile_list and profile_get responses must not contain
// any key-like field name. profileMeta deliberately has no APIKey field; this
// test pins the JSON representation to make an accidental addition observable.
func TestProfileMeta_NoKeyMaterialInResponse(t *testing.T) {
	srv := newProfileServer(t,
		newMemProfileStore(provider.Profile{Alias: "a", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "opus-4.8", ConnectionRef: "conn1"}),
		&noopReloader{},
	)
	for _, tc := range []struct{ tool string; args map[string]any }{
		{"profile_list", nil},
		{"profile_get", map[string]any{"alias": "a"}},
	} {
		res := callToolAdmin(t, srv, tc.tool, tc.args)
		require.False(t, res.IsError, tc.tool+": "+extractText(t, res))
		body := strings.ToLower(extractText(t, res))
		assert.NotContains(t, body, `"api_key"`, tc.tool+" must not include api_key field")
		assert.NotContains(t, body, `"apikey"`, tc.tool+" must not include apikey field")
		assert.NotContains(t, body, `"secret"`, tc.tool+" must not include secret field")
	}
}

// Edge (Pass 2): stringSliceArg with a non-array argument type (e.g. a plain
// string instead of []any) returns "allowed_tools must be an array".
func TestProfileCreate_NonArrayAllowedTools(t *testing.T) {
	srv := newProfileServer(t, newMemProfileStore(), &noopReloader{})
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "x", "kind": "agent", "backend": "claude-code",
		"allowed_tools": "bash", // plain string, not an array
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "array")
}

// Paired integration (#18): the profileErrorResult sentinel-to-message mapping is
// faithful when exercised through the REAL ProfileService (not a stub). Each key
// sentinel is triggered by a real service call and the tool error message is
// checked. This catches any future divergence between profileErrorResult and the
// actual sentinels the service returns.
func TestPaired_ProfileTools_SentinelMappingFaithful(t *testing.T) {
	srv := newProfileServer(t,
		newMemProfileStore(
			provider.Profile{Alias: "exists", Kind: provider.ProfileKindChat},
		),
		&noopReloader{},
	)

	// ErrForbidden (non-admin) → "forbidden: admin only"
	res := callTool(t, srv, "profile_create", map[string]any{"alias": "x", "kind": "agent", "backend": "claude-code"})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "forbidden", "ErrForbidden sentinel mapping")

	// ErrUnauthenticated (anon) → "authentication required"
	res = callToolCtx(t, srv, context.Background(), "profile_list", nil)
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "authentication required", "ErrUnauthenticated sentinel mapping")

	// ErrProfileExists → "profile already exists"
	res = callToolAdmin(t, srv, "profile_create", map[string]any{"alias": "exists", "kind": "agent", "backend": "claude-code"})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "already exists", "ErrProfileExists sentinel mapping")

	// ErrProfileNotFound → "profile not found"
	res = callToolAdmin(t, srv, "profile_delete", map[string]any{"alias": "ghost"})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "not found", "ErrProfileNotFound sentinel mapping")

	// ErrAxisNotApplicable → err.Error() which contains "not applicable"
	res = callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "bad", "kind": "agent", "backend": "codex", "context_window": "1m",
	})
	require.True(t, res.IsError)
	assert.Contains(t, strings.ToLower(extractText(t, res)), "not applicable", "ErrAxisNotApplicable sentinel mapping")
}

// Nil ProfileAdmin -> every tool reports the service unavailable, not a panic.
func TestProfileTools_NilService(t *testing.T) {
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{})
	adaptmcp.RegisterProfileTools(srv)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"profile_create", map[string]any{"alias": "x", "kind": "agent"}},
		{"profile_update", map[string]any{"alias": "x", "kind": "agent"}},
		{"profile_delete", map[string]any{"alias": "x"}},
		{"profile_get", map[string]any{"alias": "x"}},
		{"profile_list", nil},
		{"provider_capabilities", nil},
	} {
		res := callToolAdmin(t, srv, tc.tool, tc.args)
		require.True(t, res.IsError, tc.tool)
		assert.Contains(t, strings.ToLower(extractText(t, res)), "not available", tc.tool)
	}
}
