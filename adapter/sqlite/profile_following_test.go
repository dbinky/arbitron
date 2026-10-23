package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	provideradapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowingProfileRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "profiles.db")
	db, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	profiles := configadapter.ProfilesFromConfig([]domainconfig.ProfileConfig{{Alias: "worker", Kind: "agent", Model: "Vendor/Version-X", Backend: "claude-code", AllowedTools: []string{"Read"}, SelectionMode: "following", FollowTarget: &domainconfig.FollowTargetConfig{Provider: " Vendor ", Tier: " PREMIUM "}}})
	require.Len(t, profiles, 1)
	profiles[0].Revision = 999 // caller may not choose the stored revision
	require.NoError(t, NewProfileRepo(db).Create(ctx, profiles[0]))
	require.NoError(t, db.Close())
	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	repo := NewProfileRepo(db)
	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, provider.SelectionFollowing, got.SelectionMode)
	require.NotNil(t, got.FollowTarget)
	assert.Equal(t, provider.FollowTarget{Provider: "vendor", Tier: "premium"}, *got.FollowTarget)
	assert.Equal(t, "Vendor/Version-X", got.Model)
	assert.Equal(t, int64(1), got.Revision)
	rows, err := generated.New(db).ListProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "following", rows[0].SelectionMode)
	assert.Equal(t, "vendor", rows[0].FollowProvider)
	assert.Equal(t, "premium", rows[0].FollowTier)
	assert.Equal(t, int64(1), rows[0].Revision)
	// Load/Reload must carry the actual DB target, and ownership must not leak.
	cache := provideradapter.NewProfileCache(repo)
	require.NoError(t, cache.Load(ctx))
	updated := got
	updated.Model = "Vendor/Version-Y"
	require.NoError(t, repo.Update(ctx, updated))
	require.NoError(t, cache.Reload(ctx))
	binding, err := provideradapter.NewDynamicProfileResolver(cache, nil).Resolve(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, "Vendor/Version-Y", binding.Model)
}

func TestFollowingProfileRevisionABAAndConcurrentEdits(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	p := sampleProfile("worker")
	require.NoError(t, repo.Create(ctx, p))
	p.Model = "Y"
	p.Revision = 200
	require.NoError(t, repo.Update(ctx, p))
	p.Model = "opus-4.8"
	require.NoError(t, repo.Update(ctx, p))
	got, err := repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.Revision)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); assert.NoError(t, repo.Update(ctx, p)) }()
	}
	wg.Wait()
	got, err = repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Equal(t, int64(11), got.Revision)
	p.Alias = "missing"
	assert.ErrorIs(t, repo.Update(ctx, p), provider.ErrProfileNotFound)
	// Query failure cannot report success or change the previous row.
	_, err = db.DB.Exec(`CREATE TRIGGER reject_profile_edit BEFORE UPDATE ON profiles BEGIN SELECT RAISE(ABORT, 'write fault'); END`)
	require.NoError(t, err)
	p.Alias = "worker"
	assert.Error(t, repo.Update(ctx, p))
	got, err = repo.Get(ctx, p.Alias)
	require.NoError(t, err)
	assert.Equal(t, int64(11), got.Revision)
}

func TestFollowingProfileInvalidShape(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	for _, p := range []provider.Profile{
		{Alias: "bad", SelectionMode: "unknown"},
		{Alias: "bad", SelectionMode: provider.SelectionFollowing},
		{Alias: "bad", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: ""}},
		{Alias: "bad", SelectionMode: provider.SelectionPinned, FollowTarget: &provider.FollowTarget{Provider: "vendor", Tier: "premium"}},
	} {
		assert.Error(t, repo.Create(ctx, p))
	}
	count, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, count)
	_, err = db.DB.Exec(`INSERT INTO profiles(alias,kind,model,created_at,updated_at,selection_mode,follow_provider,follow_tier) VALUES ('raw','chat','X','now','now','following','','')`)
	assert.Error(t, err, "raw SQL must not persist a malformed shape")
	require.NoError(t, db.DB.Close())
	_, err = repo.Get(ctx, "worker")
	assert.Error(t, err)
	assert.Error(t, repo.Create(ctx, sampleProfile("worker")))
}

func TestFollowingProfilesMigrationOldRowsAndRollback(t *testing.T) {
	db := setupTestDB(t)
	// Roll back the dependent floor trigger before removing revision in 021.
	downFloor, err := migrationsFS.ReadFile("migrations/031_profile_revision_floor.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(downFloor))
	require.NoError(t, err)
	_, err = db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 31`)
	require.NoError(t, err)
	down, err := migrationsFS.ReadFile("migrations/025_following_profiles.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	_, err = db.DB.Exec(`DELETE FROM schema_migrations WHERE version = 25; INSERT INTO profiles(alias,kind,model,created_at,updated_at) VALUES ('legacy','chat','Old/Spelling','now','now')`)
	require.NoError(t, err)
	// Force the migration to fail after ALTERs, proving its transaction rolls back.
	_, err = db.DB.Exec(`CREATE TABLE profile_advancement_journal (id TEXT)`)
	require.NoError(t, err)
	assert.Error(t, RunMigrations(db.DB))
	var model string
	require.NoError(t, db.DB.QueryRow(`SELECT model FROM profiles WHERE alias='legacy'`).Scan(&model))
	assert.Equal(t, "Old/Spelling", model)
	var mode string
	assert.Error(t, db.DB.QueryRow(`SELECT selection_mode FROM profiles`).Scan(&mode))
	_, err = db.DB.Exec(`DROP TABLE profile_advancement_journal`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db.DB))
	got, err := NewProfileRepo(db.DB).Get(context.Background(), "legacy")
	require.NoError(t, err)
	assert.Equal(t, provider.SelectionPinned, got.SelectionMode)
	assert.Nil(t, got.FollowTarget)
	assert.Equal(t, int64(1), got.Revision)
	_, err = db.DB.Exec(`INSERT INTO profile_advancement_journal(id,refresh_id,alias,from_model,to_model,canonical_id,revision,at) VALUES ('req','refresh','legacy','X','Y','canonical',2,'now')`)
	require.NoError(t, err)
	_, err = db.DB.Exec(`INSERT INTO profile_advancement_journal(id,refresh_id,alias,from_model,to_model,canonical_id,revision,at) VALUES ('req','refresh','legacy','X','Y','canonical',2,'now')`)
	assert.Error(t, err)
	_, err = db.DB.Exec(string(downFloor))
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	require.NoError(t, db.DB.QueryRow(`SELECT model FROM profiles WHERE alias='legacy'`).Scan(&model))
	assert.Equal(t, "Old/Spelling", model)
}

func TestFollowingProfileSelectionConstraintsOnUpdate(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("worker")))
	for _, statement := range []string{
		`UPDATE profiles SET selection_mode='unknown' WHERE alias='worker'`,
		`UPDATE profiles SET selection_mode='following' WHERE alias='worker'`,
		`UPDATE profiles SET follow_provider='vendor' WHERE alias='worker'`,
		`UPDATE profiles SET follow_tier='premium' WHERE alias='worker'`,
		`UPDATE profiles SET revision=0 WHERE alias='worker'`,
	} {
		_, err := db.DB.Exec(statement)
		require.Error(t, err, statement)
		got, err := repo.Get(ctx, "worker")
		require.NoError(t, err)
		assert.Equal(t, provider.SelectionPinned, got.SelectionMode)
		assert.Nil(t, got.FollowTarget)
		assert.Equal(t, int64(1), got.Revision)
	}
	_, err := db.DB.Exec(`UPDATE profiles SET selection_mode='following',follow_provider='vendor',follow_tier='premium' WHERE alias='worker'`)
	require.NoError(t, err)
	for _, statement := range []string{
		`UPDATE profiles SET selection_mode='pinned' WHERE alias='worker'`,
		`UPDATE profiles SET follow_provider='' WHERE alias='worker'`,
		`UPDATE profiles SET follow_tier='' WHERE alias='worker'`,
	} {
		_, err := db.DB.Exec(statement)
		require.Error(t, err, statement)
	}
}
