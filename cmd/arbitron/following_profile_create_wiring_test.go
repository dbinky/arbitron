package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	settingsdomain "github.com/arbitron/arbitron/domain/settings"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Creating a following profile through the root-assembled ProfileService picks
// the newest catalog model that actually executes on the profile's route: the
// stored profile (the sink) names it, an unusable newest falls back, and an
// empty catalog refuses the write instead of storing a guessed alias.
func TestFollowingProfileRootCreateSelectsNewestUsableModel(t *testing.T) {
	const oldModel, newModel = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	for _, tc := range []struct {
		name      string
		refreshed bool
		rejectNew bool
		want      string
		wantErrIs error
	}{
		{name: "newest confirmed on the route", refreshed: true, want: newModel},
		{name: "unusable newest falls back to the next newest", refreshed: true, rejectNew: true, want: oldModel},
		{name: "cold catalog unavailable refuses the write", wantErrIs: provider.ErrNoEligibleCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var probed []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				probed = append(probed, body.Model)
				mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer refresh-key" || (tc.rejectNew && body.Model == newModel) {
					http.Error(w, "refused", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			catalogURL := "https://catalog.example.test/api.json"
			_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
			require.NoError(t, err)

			deps := ds.refreshDeps
			fetcher := &fixtureFetcher{body: refreshCatalogFixture}
			if !tc.refreshed {
				fetcher.err = errors.New("catalog unavailable")
			}
			deps.Fetcher = fetcher
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
			if tc.refreshed {
				_, err = refresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
			}

			created, _, err := ds.profileSvc.Create(ctx, provider.Profile{
				Alias: "fresh", Kind: provider.ProfileKindChat, ConnectionRef: "refresh-sink",
				SelectionMode: provider.SelectionFollowing,
				FollowTarget:  &provider.FollowTarget{Provider: "openrouter", Tier: "flash"},
			})
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
				_, getErr := ds.profileSvc.Get(ctx, "fresh")
				assert.ErrorIs(t, getErr, provider.ErrProfileNotFound, "a refused create stores nothing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, created.Model)
			stored, err := ds.profileSvc.Get(ctx, "fresh")
			require.NoError(t, err)
			assert.Equal(t, tc.want, stored.Model)
			assert.Equal(t, provider.SelectionFollowing, stored.SelectionMode)
		})
	}
}

// The registered MCP profile_create tool, driven with no model, reaches the
// root-assembled candidate source and stores the newest usable model at
// revision 1; profile_update with that revision succeeds once and the stale
// replay is refused without a write.
func TestFollowingProfileCreateWithoutModelThroughMCP(t *testing.T) {
	const newModel = "deepseek/deepseek-v4.1-flash"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
	}))
	defer upstream.Close()
	ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
	catalogURL := "https://catalog.example.test/api.json"
	_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
	require.NoError(t, err)
	deps := ds.refreshDeps
	deps.Fetcher = &fixtureFetcher{body: refreshCatalogFixture}
	refresh, err := newModelRefresh(deps)
	require.NoError(t, err)
	defer refresh.stop()
	ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
	_, err = refresh.coordinator.Refresh(ctx)
	require.NoError(t, err)

	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(name string, args map[string]any) (gomcp.CallToolResult, string) {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(ctx, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result, fmt.Sprint(response.Result.Content)
	}

	res, text := call("profile_create", map[string]any{
		"alias": "fresh", "kind": "chat", "connection_ref": "refresh-sink",
		"selection_mode": "following",
		"follow_target":  map[string]any{"provider": "OpenRouter", "tier": "flash"},
	})
	require.False(t, res.IsError, text)
	assert.Contains(t, text, newModel)
	stored, err := ds.profileSvc.Get(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, newModel, stored.Model)
	assert.Equal(t, provider.SelectionFollowing, stored.SelectionMode)
	require.NotNil(t, stored.FollowTarget)
	assert.Equal(t, provider.FollowTarget{Provider: "openrouter", Tier: "flash"}, *stored.FollowTarget)
	assert.Equal(t, int64(1), stored.Revision)
	assert.Regexp(t, `"revision": 1,`, text)
	binding, err := ds.profileResolver.Resolve(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, newModel, binding.Model, "the live resolver cache serves the stored model")

	res, text = call("profile_update", map[string]any{"alias": "fresh", "effort": "high", "revision": 1})
	require.False(t, res.IsError, text)
	res, text = call("profile_update", map[string]any{"alias": "fresh", "effort": "low", "revision": 1})
	require.True(t, res.IsError)
	assert.Contains(t, text, "changed concurrently")
	after, err := ds.profileSvc.Get(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, int64(2), after.Revision, "the stale replay wrote nothing")
}

// The YAML seed writes through the root-assembled ProfileService. A model-less
// following entry is initialized from the newest usable catalog model (the stored
// row, revision 1, and the live resolver name it) and is skipped, never stored
// empty, while no catalog exists. A following entry that names a model is proved
// exactly like an admin create: it seeds once the catalog confirms it on its own
// route and is refused while no evidence exists. Pinned entries seed.
func TestSeedProfilesInitializesFollowingFromCandidateAtRoot(t *testing.T) {
	const newModel, oldModel = "deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4-flash"
	for _, refreshed := range []bool{true, false} {
		t.Run(fmt.Sprintf("refreshed=%v", refreshed), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "profiles: []\n", func(string) {})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			require.NoError(t, newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory).Create(ctx, provider.Connection{Name: "refresh-sink", Provider: "openrouter", BaseURL: upstream.URL}, "refresh-key"))
			catalogURL := "https://catalog.example.test/api.json"
			_, err = ds.SystemSettings.Update(ctx, settingsdomain.Patch{CatalogURL: &catalogURL})
			require.NoError(t, err)
			deps := ds.refreshDeps
			fetcher := &fixtureFetcher{body: refreshCatalogFixture}
			if !refreshed {
				fetcher.err = errors.New("catalog unavailable")
			}
			deps.Fetcher = fetcher
			refresh, err := newModelRefresh(deps)
			require.NoError(t, err)
			defer refresh.stop()
			ds.profileSvc.SetModelSelection(modelidentity.NewVersionResolver(), refresh.candidates)
			if refreshed {
				_, err = refresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
			}

			repo := adaptsqlite.NewProfileRepo(ds.db)
			target := &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}
			var skippedAliases []string
			seeded, skipped, err := SeedProfiles(ctx, repo, ds.profileSvc, []provider.Profile{
				{Alias: "follow-new", Kind: provider.ProfileKindChat, ConnectionRef: "refresh-sink", SelectionMode: provider.SelectionFollowing, FollowTarget: target},
				{Alias: "follow-start", Kind: provider.ProfileKindChat, ConnectionRef: "refresh-sink", SelectionMode: provider.SelectionFollowing, FollowTarget: target, Model: oldModel},
				{Alias: "follow-bad", Kind: provider.ProfileKindChat, ConnectionRef: "refresh-sink", SelectionMode: provider.SelectionFollowing, Model: oldModel},
				{Alias: "pinned", Kind: provider.ProfileKindChat, ConnectionRef: "refresh-sink", Model: newModel},
			}, func(alias string, _ error) { skippedAliases = append(skippedAliases, alias) })
			require.NoError(t, err)

			_, getErr := ds.profileSvc.Get(ctx, "follow-bad")
			assert.ErrorIs(t, getErr, provider.ErrProfileNotFound, "a following entry without a target is refused before persistence")
			_, err = ds.profileSvc.Get(ctx, "pinned")
			assert.NoError(t, err)
			if !refreshed {
				assert.Equal(t, 1, seeded)
				assert.Equal(t, 3, skipped)
				assert.ElementsMatch(t, []string{"follow-new", "follow-start", "follow-bad"}, skippedAliases)
				_, getErr = ds.profileSvc.Get(ctx, "follow-new")
				assert.ErrorIs(t, getErr, provider.ErrProfileNotFound, "no empty-model following row is stored")
				_, getErr = ds.profileSvc.Get(ctx, "follow-start")
				assert.ErrorIs(t, getErr, provider.ErrProfileNotFound, "an explicit start with no catalog evidence is refused, not stored unproved")
				fetcher.err = nil
				_, err = refresh.coordinator.Refresh(ctx)
				require.NoError(t, err)
				skippedAliases = nil
				// A later boot retries the original pending snapshot even with a
				// non-empty profile table and no config entries supplied again.
				seeded, skipped, err = SeedProfiles(ctx, repo, ds.profileSvc, nil, func(alias string, _ error) { skippedAliases = append(skippedAliases, alias) })
				require.NoError(t, err)
				assert.Equal(t, 2, seeded)
			} else {
				assert.Equal(t, 3, seeded)
			}
			assert.Equal(t, 1, skipped)
			assert.Equal(t, []string{"follow-bad"}, skippedAliases)
			start, err := ds.profileSvc.Get(ctx, "follow-start")
			require.NoError(t, err)
			assert.Equal(t, oldModel, start.Model, "an explicit start confirmed on its own route keeps the operator's model")
			assert.Equal(t, provider.SelectionFollowing, start.SelectionMode)
			assert.Equal(t, int64(1), start.Revision)
			stored, err := ds.profileSvc.Get(ctx, "follow-new")
			require.NoError(t, err)
			assert.Equal(t, newModel, stored.Model)
			assert.Equal(t, int64(1), stored.Revision)
			binding, err := ds.profileResolver.Resolve(ctx, "follow-new")
			require.NoError(t, err)
			assert.Equal(t, newModel, binding.Model, "the live resolver serves the seeded model")
		})
	}
}

// First boot from YAML reaches the same service: with no catalog yet, a
// model-less following entry is skipped instead of being stored with an empty
// model, a model-bearing following entry is skipped instead of being stored
// unproved, a mutable pinned alias is refused, and the valid pinned entry seeds.
func TestBootSeedRoutesYAMLProfilesThroughProfileService(t *testing.T) {
	ds := bootSinkRoot(t, `profiles:
  - alias: pinned-ok
    kind: chat
    model: claude-sonnet-4-5
  - alias: pinned-mutable
    kind: chat
    model: sonnet
  - alias: follow-start
    kind: chat
    backend: claude-code
    connectionref: catalog-sink
    model: deepseek/deepseek-v4-flash
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
  - alias: follow-empty
    kind: chat
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
`, func(string) {})
	ctx := adminCtxForLocalUser(t, ds)
	_, err := ds.profileSvc.Get(ctx, "follow-empty")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "the unprovable model-less entry is not stored empty")
	_, err = ds.profileSvc.Get(ctx, "follow-start")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "an explicit following start with no catalog evidence is refused, not stored unproved")
	_, err = ds.profileSvc.Get(ctx, "pinned-ok")
	assert.NoError(t, err)
	_, err = ds.profileSvc.Get(ctx, "pinned-mutable")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound, "a mutable pinned alias is held to the trigger's fixed-version grammar and skipped")
}
