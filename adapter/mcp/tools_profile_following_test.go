package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	provideradapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An invalid stored selection must reach the MCP validation branch without
// changing the durable row or the resolver's last valid snapshot.
func TestProfileUpdate_InvalidStoredSelectionIsValidationError(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, sqlite.RunMigrations(db))
	repo := sqlite.NewProfileRepo(db)
	require.NoError(t, repo.Create(ctx, provider.Profile{
		Alias: "worker", Kind: provider.ProfileKindAgent, Backend: "claude-code",
		Model: "Vendor/Version-X", SelectionMode: provider.SelectionFollowing,
		FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"},
	}))
	cache := provideradapter.NewProfileCache(repo)
	require.NoError(t, cache.Load(ctx))
	srv := newProfileServer(t, repo, cache)

	// SQL enforces the mode/target shape, but slug validation belongs to the
	// domain mapper. Simulate an out-of-band edit after the cache was loaded.
	_, err = db.Exec(`UPDATE profiles SET follow_tier='bad tier' WHERE alias='worker'`)
	require.NoError(t, err)
	_, err = repo.Get(ctx, "worker")
	require.ErrorIs(t, err, provider.ErrInvalidSelection)
	queries := generated.New(db)
	before, err := queries.GetProfile(ctx, "worker")
	require.NoError(t, err)
	require.Equal(t, "Vendor/Version-X", before.Model)
	require.Equal(t, int64(1), before.Revision)

	res := callToolAdmin(t, srv, "profile_update", map[string]any{
		"alias": "worker", "model": "Vendor/Version-Y",
	})
	require.True(t, res.IsError)
	message := extractText(t, res)
	assert.Contains(t, message, "invalid profile selection")
	assert.NotContains(t, message, "profile operation failed", "selection rejection is a validation error")

	after, err := queries.GetProfile(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, before, after, "rejected edit must preserve every stored field, including model and revision")
	binding, err := provideradapter.NewDynamicProfileResolver(cache, nil).Resolve(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, "Vendor/Version-X", binding.Model)
}

// emptySource is a candidate source that finds nothing usable.
type emptySource struct {
	provider.EligibleCandidateSource
}

func (emptySource) Newest(context.Context, provider.Profile) (provider.EligibleCandidate, error) {
	return provider.EligibleCandidate{}, nil
}

func decodeProfileResult(t *testing.T, res *gomcp.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, res.IsError, extractText(t, res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &out))
	return out
}

// pinnedSelectionRoutes supplies only the explicit subscription route these
// schema tests configure. Discovery cannot authorize a following profile here.
type pinnedSelectionRoutes struct{ emptySource }

func (pinnedSelectionRoutes) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if (p.Alias != "pin" && p.Alias != "p") ||
		p.Kind != provider.ProfileKindAgent || p.Backend != "claude-code" ||
		p.Model != "sonnet-4.6" || p.ConnectionRef != "" ||
		p.SelectionMode != provider.SelectionPinned || p.FollowTarget != nil || len(p.AllowedTools) != 0 ||
		p.ContextWindow != "" && p.ContextWindow != provider.ContextDefault ||
		p.Effort != "" && p.Effort != provider.EffortDefault && p.Effort != provider.EffortLow {
		return nil, fmt.Errorf("schema fixture: unexpected prospective pinned profile")
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "claude-cli")}, nil
}

// anyRouteSource authorizes pinned writes on the subscription route for every
// profile, for tests about the tools' own behavior rather than route proof.
type anyRouteSource struct{ emptySource }

func (anyRouteSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if p.Backend == "codex" {
		return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "claude-cli")}, nil
}

func selectionSchemaServer(t *testing.T, store provider.ProfileStore) *adaptmcp.Server {
	t.Helper()
	rules := profileRules()
	svc := provider.NewProfileService(store, rules, &noopReloader{})
	svc.SetModelSelection(modelidentity.NewVersionResolver(), pinnedSelectionRoutes{})
	svc.SetAdvancementDependencies(nil, nil, provider.NewExecutionGate())
	srv := adaptmcp.NewServerWithDeps("selection-schema", "0", &adaptmcp.Deps{ProfileAdmin: svc, Capabilities: rules})
	adaptmcp.RegisterProfileTools(srv)
	return srv
}

// The profile tools accept selection_mode and follow_target on create and echo
// them, with the stored revision, on create, get and list.
func TestProfileTools_SelectionFieldsRoundTrip(t *testing.T) {
	store := newMemProfileStore()
	srv := selectionSchemaServer(t, store)

	created := decodeProfileResult(t, callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "pin", "kind": "agent", "backend": "claude-code", "model": "sonnet-4.6",
	}))
	profile := created["profile"].(map[string]any)
	assert.Equal(t, "pinned", profile["selection_mode"])
	assert.EqualValues(t, 1, profile["revision"], "create reports the stored revision, not the request's 0")
	assert.NotContains(t, profile, "follow_target")

	got := decodeProfileResult(t, callToolAdmin(t, srv, "profile_get", map[string]any{"alias": "pin"}))
	assert.EqualValues(t, 1, got["revision"])

	// A pinned profile cannot carry a follow target: the argument is parsed and
	// reaches the domain rule rather than being silently dropped.
	res := callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "bad", "kind": "agent", "backend": "claude-code", "model": "sonnet-4.6",
		"follow_target": map[string]any{"provider": "anthropic", "tier": "premium"},
	})
	require.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "pinned profile cannot have a follow target")
	_, err := store.Get(context.Background(), "bad")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "a refused create persists nothing")

	for name, args := range map[string]map[string]any{
		"mode not a string":   {"selection_mode": 3},
		"target not object":   {"follow_target": "anthropic/premium"},
		"tier not a string":   {"follow_target": map[string]any{"provider": "anthropic", "tier": 1}},
		"unknown mode":        {"selection_mode": "latest"},
		"following no target": {"selection_mode": "following"},
	} {
		t.Run(name, func(t *testing.T) {
			base := map[string]any{"alias": "x", "kind": "agent", "backend": "claude-code", "model": "sonnet-4.6"}
			for k, v := range args {
				base[k] = v
			}
			res := callToolAdmin(t, srv, "profile_create", base)
			assert.True(t, res.IsError)
			_, err := store.Get(context.Background(), "x")
			assert.ErrorIs(t, err, provider.ErrProfileNotFound)
		})
	}
}

// Following creation without its candidate ports, or with an empty catalog,
// fails with a clear tool error and stores nothing.
func TestProfileCreate_FollowingFailsClosed(t *testing.T) {
	args := map[string]any{
		"alias": "follow", "kind": "chat", "connection_ref": "route",
		"selection_mode": "following",
		"follow_target":  map[string]any{"provider": "openrouter", "tier": "flash"},
	}
	t.Run("ports missing", func(t *testing.T) {
		store := newMemProfileStore()
		rules := profileRules()
		svc := provider.NewProfileService(store, rules, &noopReloader{}) // no selection ports
		srv := adaptmcp.NewServerWithDeps("t", "0", &adaptmcp.Deps{ProfileAdmin: svc, Capabilities: rules})
		adaptmcp.RegisterProfileTools(srv)
		res := callToolAdmin(t, srv, "profile_create", args)
		require.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "model selection ports not configured")
		assert.NotContains(t, extractText(t, res), "profile operation failed")
		_, err := store.Get(context.Background(), "follow")
		assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	})
	t.Run("no eligible candidate", func(t *testing.T) {
		store := newMemProfileStore()
		rules := profileRules()
		svc := provider.NewProfileService(store, rules, &noopReloader{})
		svc.SetModelSelection(nil, emptySource{})
		srv := adaptmcp.NewServerWithDeps("t", "0", &adaptmcp.Deps{ProfileAdmin: svc, Capabilities: rules})
		adaptmcp.RegisterProfileTools(srv)
		res := callToolAdmin(t, srv, "profile_create", args)
		require.True(t, res.IsError)
		assert.Contains(t, extractText(t, res), "no eligible model candidate")
		assert.Contains(t, extractText(t, res), "refresh the catalog")
		_, err := store.Get(context.Background(), "follow")
		assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	})
}

// profile_update honours patch omission and clear for follow_target, and an
// expected revision rejects a stale edit without touching the row.
func TestProfileUpdate_RevisionAndFollowTargetPatch(t *testing.T) {
	store := newMemProfileStore()
	srv := selectionSchemaServer(t, store)
	decodeProfileResult(t, callToolAdmin(t, srv, "profile_create", map[string]any{
		"alias": "p", "kind": "agent", "backend": "claude-code", "model": "sonnet-4.6",
	}))

	stale := callToolAdmin(t, srv, "profile_update", map[string]any{"alias": "p", "effort": "low", "revision": 7})
	require.True(t, stale.IsError)
	assert.Contains(t, extractText(t, stale), "changed concurrently")
	got, _ := store.Get(context.Background(), "p")
	assert.NotEqual(t, provider.EffortLow, got.Effort, "a stale edit must not apply")

	fresh := decodeProfileResult(t, callToolAdmin(t, srv, "profile_update", map[string]any{"alias": "p", "effort": "low", "revision": 1}))
	assert.EqualValues(t, 2, fresh["profile"].(map[string]any)["revision"])

	// An explicit null clears the target (a no-op on a pinned profile); a bad
	// revision type is rejected before the service is reached.
	decodeProfileResult(t, callToolAdmin(t, srv, "profile_update", map[string]any{"alias": "p", "follow_target": nil}))
	for _, bad := range []any{"2", 1.5, 0} {
		res := callToolAdmin(t, srv, "profile_update", map[string]any{"alias": "p", "revision": bad})
		assert.True(t, res.IsError, "revision %v", bad)
		assert.Contains(t, extractText(t, res), "revision must be a positive integer")
	}
	// Setting a target on a pinned profile is refused by the domain rule.
	res := callToolAdmin(t, srv, "profile_update", map[string]any{
		"alias": "p", "follow_target": map[string]any{"provider": "a", "tier": "b"},
	})
	require.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "pinned profile cannot have a follow target")
}
