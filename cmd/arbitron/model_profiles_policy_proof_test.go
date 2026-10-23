package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelSelectionProofWiring(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	leaf.mu.Lock()
	leaf.probeEntered = make(chan struct{})
	leaf.probeRelease = make(chan struct{})
	entered, release := leaf.probeEntered, leaf.probeRelease
	leaf.mu.Unlock()
	call := mcpToolCaller(t, ctx, ds)
	args := map[string]any{"alias": "worker", "kind": "chat", "connection_ref": "root", "selection_mode": "following", "follow_target": map[string]any{"provider": "openrouter", "tier": "flash"}}
	done := make(chan gomcp.CallToolResult, 1)
	go func() { res, _ := call("profile_create", args); done <- res }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("create did not collect prospective proof")
	}
	key := "rotated-lifecycle-key"
	require.NoError(t, ds.connSvc.Update(ctx, provider.Connection{Name: "root", Provider: "openrouter", BaseURL: "https://provider.example.test"}, &key))
	leaf.mu.Lock()
	leaf.key = key
	leaf.mu.Unlock()
	close(release)
	select {
	case res := <-done:
		require.True(t, res.IsError)
		assert.NotContains(t, strings.TrimSpace(res.Content[0].(gomcp.TextContent).Text), key)
	case <-time.After(2 * time.Second):
		t.Fatal("stale create did not return")
	}
	_, err := ds.profileSvc.Get(ctx, "worker")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='worker'`).Scan(&count))
	assert.Zero(t, count)
	res, _ := call("profile_create", args)
	require.False(t, res.IsError, "%+v", res.Content)
	lifecycleExecute(t, ds, ctx, rootNew)
}

func TestIdentityPolicyCannotEnableD8FollowingRoot(t *testing.T) {
	leaf := &lifecycleHTTP{}
	ds, _, _ := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := adminCtxForLocalUser(t, ds)
	call := mcpToolCaller(t, ctx, ds)
	res, _ := call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": []any{map[string]any{"providers": []any{"azure", "bedrock"}, "tier": "custom", "pattern": `claude-sonnet-(\d+)-(\d+)`, "version_groups": []any{1, 2}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	mapper, err := modelidentity.NewIdentityMapper(ds.IdentityPolicy)
	require.NoError(t, err)
	for _, route := range []string{"azure", "bedrock"} {
		t.Run(route, func(t *testing.T) {
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: route, Provider: route, BaseURL: "https://opaque.example.test", Extras: map[string]string{"api-version": "2025-01-01", "region": "us-east-1", "access-key-id": "test-account"}}, "opaque-key"))
			entry, err := mapper.Map(ctx, modelidentity.CatalogModel{CatalogProvider: route, NativeID: "claude-sonnet-4-6"})
			require.NoError(t, err)
			require.Equal(t, "custom", entry.Tier, "the edited rule really reaches the mapper")
			assert.Empty(t, entry.SpecificModel, "membership cannot certify an executable grammar")
			snap, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			var path modelcatalog.ExecutionPath
			for _, p := range snap.Paths {
				if p.ConnectionRef == route && p.Backend == "" && p.Mode == modelcatalog.ExecutionModeBatch {
					path = p
					break
				}
			}
			require.NotEmpty(t, path.Key)
			ev, err := ds.modelRefresh.probe.Check(ctx, path, entry)
			assert.ErrorIs(t, err, provider.ErrInvalidSelection)
			assert.False(t, ev.Confirmed())
			assert.Empty(t, ev.ProofRef)
			res, _ := call("profile_create", map[string]any{"alias": route, "kind": "chat", "connection_ref": route, "selection_mode": "following", "follow_target": map[string]any{"provider": route, "tier": "custom"}})
			require.True(t, res.IsError)
			_, err = ds.profileSvc.Get(ctx, route)
			assert.ErrorIs(t, err, provider.ErrProfileNotFound)
		})
	}
	leaf.mu.Lock()
	assert.Empty(t, leaf.models, "D8 must not reach the execution transport")
	leaf.mu.Unlock()
}

func TestIdentityPolicyChangeRejectsInFlightSelectionRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld, rootNew}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx, cancel := context.WithTimeout(lifecycleConnection(t, ds), 10*time.Second)
	defer cancel()
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	leaf.mu.Lock()
	leaf.probeEntered, leaf.probeRelease = entered, release
	leaf.mu.Unlock()
	call := mcpToolCaller(t, ctx, ds)
	args := map[string]any{"alias": "worker", "kind": "chat", "connection_ref": "root", "selection_mode": "following", "follow_target": map[string]any{"provider": "openrouter", "tier": "flash"}}
	done := make(chan gomcp.CallToolResult, 1)
	go func() { res, _ := call("profile_create", args); done <- res }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("prospective selection never reached its real probe")
	}
	res, _ := call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": []any{map[string]any{"providers": []any{"openrouter"}, "tier": "flash", "pattern": `deepseek-v(4)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	unblock()
	select {
	case res = <-done:
		require.True(t, res.IsError, "old-policy evidence cannot commit after the membership edit")
	case <-ctx.Done():
		t.Fatal("stale selection did not return")
	}
	_, err := ds.profileSvc.Get(ctx, "worker")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
	var count int
	require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='worker'`).Scan(&count))
	assert.Zero(t, count, "stale selection must persist nothing")
	res, _ = call("profile_create", args)
	require.False(t, res.IsError, "%+v", res.Content)
	stored, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, rootOld, stored.Model, "fresh selection consumes the edited policy, not the old proof")
	lifecycleExecute(t, ds, ctx, rootOld)
	leaf.mu.Lock()
	assert.Equal(t, []string{rootOld, rootOld}, leaf.executed)
	leaf.mu.Unlock()
}

func TestModelProfilesPolicyEditFeedsFreshSelectionRoot(t *testing.T) {
	leaf := &lifecycleHTTP{source: []string{rootOld}}
	ds, _, intervals := buildLifecycleRoot(t, "profiles: []\n", leaf)
	ctx := lifecycleConnection(t, ds)
	call := mcpToolCaller(t, ctx, ds)
	res, _ := call("model_identity_policy_update", map[string]any{"expected_revision": 1, "rules": []any{map[string]any{"providers": []any{"openrouter"}, "tier": "flash", "pattern": `deepseek-v(4)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	require.NoError(t, ds.Start(ctx))
	rootTicker(t, intervals)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, ConnectionRef: "root", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
	require.NoError(t, err)
	old, _, err := ds.eng.Trigger(ctx, "lifecycle", nil, "normal")
	require.NoError(t, err)
	waitTerminal(t, ds, old)
	leaf.mu.Lock()
	leaf.source = []string{rootOld, rootNew}
	leaf.mu.Unlock()
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError)
	before, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, rootOld, before.Model)
	lifecycleExecute(t, ds, ctx, rootOld)
	view, err := ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	unmatched := entryOf(t, view.Catalog.Slices, "openrouter", rootNew)
	assert.Empty(t, unmatched.Tier, "the new model is initially unmatched")
	assert.Empty(t, unmatched.OrderKey)
	frozen := catalogFrozenPayloads(t, ds)
	var proofs []modelcatalog.UsabilityEvidence
	for _, ev := range view.Evidence {
		if ev.Confirmed() {
			proofs = append(proofs, ev)
		}
	}
	require.NotEmpty(t, proofs)
	leaf.mu.Lock()
	calls := len(leaf.models)
	leaf.mu.Unlock()
	res, _ = call("model_identity_policy_update", map[string]any{"expected_revision": 2, "rules": []any{map[string]any{"providers": []any{"openrouter"}, "tier": "flash", "pattern": `deepseek-v(\d+(?:\.\d+)?)-flash`, "version_groups": []any{1}}}})
	require.False(t, res.IsError, "%+v", res.Content)
	policy, err := ds.IdentityPolicy.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, modelidentity.IdentityPolicy{Revision: 3, Rules: []modelidentity.PatternRule{{Providers: []string{"openrouter"}, Tier: "flash", Pattern: `deepseek-v(\d+(?:\.\d+)?)-flash`, VersionGroups: []int{1}}}}, policy)
	unchanged, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, before, unchanged, "editing membership alone must not move a profile")
	res, _ = call("model_catalog_refresh", nil)
	require.False(t, res.IsError, "%+v", res.Content)
	after, err := ds.profileSvc.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, rootNew, after.Model)
	assert.Equal(t, frozen, catalogFrozenPayloads(t, ds), "membership cannot create another discovery or rewrite original payload")
	leaf.mu.Lock()
	assert.Equal(t, calls, len(leaf.models), "policy-only edits reuse exact unchanged path confirmation")
	leaf.mu.Unlock()
	view, err = ds.catalogStore.Snapshot(ctx)
	require.NoError(t, err)
	for _, proof := range proofs {
		assert.Contains(t, view.Evidence, proof, "membership-only edits retain exact path confirmation")
	}
	matched := entryOf(t, view.Catalog.Slices, "openrouter", rootNew)
	assert.Equal(t, "flash", matched.Tier)
	assert.Equal(t, "4.1", matched.OrderKey)
	assert.Equal(t, policy.Revision, matched.PolicyRevision)
	lifecycleExecute(t, ds, ctx, rootNew)
	leaf.mu.Lock()
	assert.Equal(t, []string{rootOld, rootOld, rootOld, rootOld, rootNew, rootNew}, leaf.executed, "actual old and new run requests use their own selected models")
	leaf.mu.Unlock()
	prior, err := ds.instanceRepo.Get(context.Background(), old)
	require.NoError(t, err)
	assert.Equal(t, rootOld, prior.ModelPins["worker"])
}
