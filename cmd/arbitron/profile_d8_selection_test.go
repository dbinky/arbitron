package main

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Uses the real root's route inventory, connection/profile services, SQLite
// and executor-facing cache. No version, inventory or candidate port is replaced.
func TestD8ProfileSelectionRoot(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5d}, 32)))
	for _, route := range []string{"azure-openai", "azure-foundry", "azure", "bedrock", "amazon-bedrock"} {
		t.Run(route, func(t *testing.T) {
			ds := bootPinStack(t)
			ctx := adminCtxForLocalUser(t, ds)
			conn := provider.Connection{Name: "native", Provider: route, BaseURL: "https://native.example", Extras: map[string]string{"api-version": "2024-10-21", "region": "us-east-1", "access-key-id": "access"}}
			require.NoError(t, ds.connSvc.Create(ctx, conn, "secret"))
			p := provider.Profile{Alias: "native", Kind: provider.ProfileKindChat, ConnectionRef: conn.Name, Model: "Configured/Deployment:ID"}
			repo := sqlite.NewProfileRepo(ds.db)
			got, _, err := ds.profileSvc.Create(ctx, p)
			require.NoError(t, err)
			stored, err := repo.Get(ctx, p.Alias)
			require.NoError(t, err)
			assert.Equal(t, got, stored)
			assert.Equal(t, p.Model, stored.Model)
			binding, err := ds.profileResolver.Resolve(ctx, p.Alias)
			require.NoError(t, err)
			assert.Equal(t, p.Model, binding.Model)
			assert.Equal(t, route, binding.Provider)
			// The same connection is irrelevant when the actual agent backend is
			// Codex: its subscription needs a fixed OpenAI ID, not a D8 exception.
			subscription := provider.Profile{Alias: "subscription", Kind: provider.ProfileKindAgent, ConnectionRef: conn.Name, Backend: "codex", Model: "gpt-6.1-sol"}
			_, _, err = ds.profileSvc.Create(ctx, subscription)
			require.NoError(t, err)
			fixed, err := repo.Get(ctx, subscription.Alias)
			require.NoError(t, err)
			assert.Equal(t, subscription.Model, fixed.Model)
			live, err := ds.profileResolver.Resolve(ctx, subscription.Alias)
			require.NoError(t, err)
			assert.Equal(t, subscription.Model, live.Model)
			assert.Equal(t, "codex", live.Backend)
			opaque := "deployment"
			_, _, err = ds.profileSvc.UpdatePatch(ctx, subscription.Alias, provider.ProfilePatch{Model: &opaque})
			require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
			fixedAfter, err := repo.Get(ctx, subscription.Alias)
			require.NoError(t, err)
			assert.Equal(t, fixed, fixedAfter)
			following := string(provider.SelectionFollowing)
			target := &provider.FollowTarget{Provider: "openai", Tier: "sol"}
			_, _, err = ds.profileSvc.UpdatePatch(ctx, p.Alias, provider.ProfilePatch{SelectionMode: &following, FollowTarget: &target})
			require.ErrorIs(t, err, provider.ErrInvalidSelection)
			assert.Contains(t, err.Error(), "pinned-only")
			after, err := repo.Get(ctx, p.Alias)
			require.NoError(t, err)
			assert.Equal(t, stored, after)
			binding, err = ds.profileResolver.Resolve(ctx, p.Alias)
			require.NoError(t, err)
			assert.Equal(t, p.Model, binding.Model)
		})
	}
}
