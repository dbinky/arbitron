package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentityPolicyMigrationFaultRetainsPreviousPolicy(t *testing.T) {
	ctx := context.Background()
	db := openPolicyDB(t, ":memory:")
	repo := NewModelIdentityPolicyRepo(db)
	before, err := repo.Replace(ctx, 1, []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "custom", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}})
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM schema_migrations WHERE version=29;
		CREATE TRIGGER fail_policy_migration BEFORE INSERT ON schema_migrations
		WHEN NEW.version=29 BEGIN SELECT RAISE(ABORT, 'policy migration fault'); END;`)
	require.NoError(t, err)
	err = RunMigrations(db)
	require.ErrorContains(t, err, "recording migration 29")
	after, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	var applied int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=29`).Scan(&applied))
	assert.Zero(t, applied, "a failed migration must not be recorded as applied")
	_, err = db.Exec(`DROP TRIGGER fail_policy_migration`)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(db))
	after, err = repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "retry preserves the admin policy too")
}

func TestIdentityPolicySeedsD2(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policy.db")
	db := openPolicyDB(t, path)
	repo := NewModelIdentityPolicyRepo(db)
	seed, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, modelidentity.IdentityPolicy{Revision: 1, Rules: modelidentity.InitialRules()}, seed)
	mapper, err := modelidentity.NewIdentityMapper(repo)
	require.NoError(t, err)
	for _, tc := range []struct{ provider, tier, older, newer, key string }{
		{"anthropic", "opus", "claude-opus-5", "claude-opus-5-5", "5.5"},
		{"openai", "sol", "gpt-6-sol", "gpt-6.1-sol", "6.1"},
		{"ollama-cloud", "flash", "deepseek-v4-flash", "deepseek-v4.1-flash", "4.1"},
		{"ollama-cloud", "glm", "glm-5.2", "glm-5.3", "5.3"},
		{"ollama-cloud", "kimi", "kimi-k2.7", "kimi-k3", "3"},
		{"ollama-cloud", "kimi-code", "kimi-k2.6-code", "kimi-k2.7-code", "2.7"},
	} {
		t.Run(tc.provider+"/"+tc.tier, func(t *testing.T) {
			older, err := mapper.Map(ctx, modelidentity.CatalogModel{CatalogProvider: tc.provider, NativeID: tc.older})
			require.NoError(t, err)
			newer, err := mapper.Map(ctx, modelidentity.CatalogModel{CatalogProvider: tc.provider, NativeID: tc.newer})
			require.NoError(t, err)
			assert.Equal(t, tc.tier, newer.Tier)
			assert.Equal(t, tc.key, newer.OrderKey)
			cmp, err := modelidentity.Compare(newer, older)
			require.NoError(t, err)
			assert.Equal(t, 1, cmp)
		})
	}
	for _, tc := range []struct{ provider, id string }{
		{"anthropic", "claude-haiku-4-5-20251001"},
		{"anthropic", "claude-opus-5-5-fast"},
		{"ollama-cloud", "deepseek-v4-flash:0731"},
		{"ollama-cloud", "glm-5.3-vision-exp"},
	} {
		entry, err := mapper.Map(ctx, modelidentity.CatalogModel{CatalogProvider: tc.provider, NativeID: tc.id})
		require.NoError(t, err)
		assert.Empty(t, entry.Tier, tc.id)
		assert.Empty(t, entry.OrderKey, tc.id)
	}
	edited, err := repo.Replace(ctx, seed.Revision, []modelidentity.PatternRule{{Providers: []string{"ollama-cloud"}, Tier: "custom", Pattern: `glm-(\d+(?:\.\d+)?)`, VersionGroups: []int{1}}})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	reopened := openPolicyDB(t, path)
	after, err := NewModelIdentityPolicyRepo(reopened).Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, edited, after, "migrations never reseed over an edit on restart")
}
