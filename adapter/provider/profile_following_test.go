package provider

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowingProfileCacheReloadFromSQLite(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	repo := sqlite.NewProfileRepo(db)
	p := provider.Profile{Alias: "worker", Model: "Version-X", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}, AllowedTools: []string{"Read"}}
	require.NoError(t, repo.Create(ctx, p))
	cache := NewProfileCache(repo)
	require.NoError(t, cache.Load(ctx))
	got, ok := cache.get("worker")
	require.True(t, ok)
	require.NotNil(t, got.FollowTarget)
	assert.Equal(t, *p.FollowTarget, *got.FollowTarget)
	assert.Equal(t, provider.SelectionFollowing, got.SelectionMode)
	assert.Equal(t, int64(1), got.Revision)
	p.FollowTarget.Tier = "standard"
	p.Model = "Version-Y"
	require.NoError(t, repo.Update(ctx, p))
	require.NoError(t, cache.Reload(ctx))
	got, ok = cache.get("worker")
	require.True(t, ok)
	require.NotNil(t, got.FollowTarget)
	assert.Equal(t, "standard", got.FollowTarget.Tier)
	assert.Equal(t, "Version-Y", got.Model)
	assert.Equal(t, int64(2), got.Revision)
}

func TestFollowingProfileCacheCopiesNestedFields(t *testing.T) {
	p := provider.Profile{Alias: "worker", Model: "Version-X", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}, AllowedTools: []string{"Read"}}
	cache := NewProfileCacheFromProfiles(p)
	p.FollowTarget.Tier = "mutated-input"
	p.AllowedTools[0] = "Edit"
	got, ok := cache.get("worker")
	require.True(t, ok)
	assert.Equal(t, "premium", got.FollowTarget.Tier)
	assert.Equal(t, "Read", got.AllowedTools[0])
	got.FollowTarget.Tier = "mutated-output"
	got.AllowedTools[0] = "Bash"
	// Inspect before Reload: reloading would mask a leaked cache reference.
	got, ok = cache.get("worker")
	require.True(t, ok)
	assert.Equal(t, "premium", got.FollowTarget.Tier)
	assert.Equal(t, "Read", got.AllowedTools[0])
	resolver := NewDynamicProfileResolver(cache, nil)
	binding, err := resolver.Resolve(context.Background(), "worker")
	require.NoError(t, err)
	binding.AllowedTools[0] = "Write"
	binding, err = resolver.Resolve(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, []string{"Read"}, binding.AllowedTools)
	require.NoError(t, cache.Reload(context.Background()))
	got, ok = cache.get("worker")
	require.True(t, ok)
	assert.Equal(t, "premium", got.FollowTarget.Tier)
	assert.Equal(t, "Read", got.AllowedTools[0])
}

func TestFollowingProfileCacheRejectsCorruptRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"non-slug target", `UPDATE profiles SET follow_tier='bad tier', model='Version-Y' WHERE alias='worker'`},
		{"invalid revision", `PRAGMA ignore_check_constraints=ON; UPDATE profiles SET revision=0, model='Version-Y' WHERE alias='worker'; PRAGMA ignore_check_constraints=OFF`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.Open(":memory:")
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, sqlite.RunMigrations(db))
			repo := sqlite.NewProfileRepo(db)
			require.NoError(t, repo.Create(ctx, provider.Profile{Alias: "worker", Model: "Version-X", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}}))
			cache := NewProfileCache(repo)
			require.NoError(t, cache.Load(ctx))
			_, err = db.Exec(tc.sql)
			require.NoError(t, err)
			list, err := repo.List(ctx)
			require.Error(t, err)
			assert.Nil(t, list, "never publish a partial result")
			require.Error(t, cache.Reload(ctx))
			binding, err := NewDynamicProfileResolver(cache, nil).Resolve(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, "Version-X", binding.Model, "failed reload retains the entire prior snapshot")
			fresh := NewProfileCache(repo)
			require.Error(t, fresh.Load(ctx), "boot cache loading must fail closed")
			_, err = NewDynamicProfileResolver(fresh, nil).Resolve(ctx, "worker")
			assert.ErrorIs(t, err, provider.ErrUnknownProfile)
		})
	}
}
