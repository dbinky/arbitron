package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openSettingsDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	return db
}

func TestSettingsPersistRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	db := openSettingsDB(t, path)
	svc := settings.NewService(NewSettingsRepo(db))
	ctx := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	catalog, webhook := "https://catalog.example.test/custom.json", "https://teams.example.test/hooks/restart-secret"
	_, err := svc.Update(ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &webhook})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	reopened := openSettingsDB(t, path)
	svc = settings.NewService(NewSettingsRepo(reopened))
	internal, err := svc.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, settings.Values{CatalogURL: catalog, TeamsWebhookURL: webhook}, internal)
	public, err := svc.Get(ctx)
	require.NoError(t, err)
	assert.True(t, public.TeamsConfigured)
	assert.NotEqual(t, webhook, public.TeamsWebhookURL)
	// Reapplying the seed must never overwrite existing values.
	migration, err := migrationsFS.ReadFile("migrations/023_system_settings.up.sql")
	require.NoError(t, err)
	_, err = reopened.Exec(string(migration))
	require.NoError(t, err)
	internal, err = svc.ReadInternal(ctx)
	require.NoError(t, err)
	assert.Equal(t, settings.Values{CatalogURL: catalog, TeamsWebhookURL: webhook}, internal)
	down, err := migrationsFS.ReadFile("migrations/023_system_settings.down.sql")
	require.NoError(t, err)
	_, err = reopened.Exec(string(down))
	require.NoError(t, err)
	_, err = svc.ReadInternal(ctx)
	require.Error(t, err)
}

func TestSettingsReadAndWriteFaults(t *testing.T) {
	db := openSettingsDB(t, ":memory:")
	repo := NewSettingsRepo(db)
	ctx := context.Background()
	catalog, webhook := "https://catalog.example.test/original.json", "https://teams.example.test/hooks/original"
	before, err := repo.Update(ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &webhook})
	require.NoError(t, err)
	assert.Equal(t, settings.Values{CatalogURL: catalog, TeamsWebhookURL: webhook}, before)
	_, err = db.Exec(`CREATE TRIGGER reject_webhook BEFORE UPDATE ON system_settings
 WHEN NEW.key = 'model_announcements.teams_webhook_url'
 BEGIN SELECT RAISE(ABORT, 'injected write fault'); END;`)
	require.NoError(t, err)
	replacement, replacementWebhook := "https://catalog.example.test/replacement.json", "https://teams.example.test/hooks/replacement"
	_, err = repo.Update(ctx, settings.Patch{CatalogURL: &replacement, TeamsWebhookURL: &replacementWebhook})
	require.ErrorContains(t, err, "injected write fault")
	after, err := repo.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = db.Exec("DELETE FROM system_settings WHERE key = 'model_announcements.teams_webhook_url'")
	require.NoError(t, err)
	_, err = repo.Read(ctx)
	assert.Error(t, err, "missing settings are corruption, not empty defaults")
	_, err = repo.Update(ctx, settings.Patch{CatalogURL: &replacement})
	assert.Error(t, err)
	var retainedCatalog string
	require.NoError(t, db.QueryRow("SELECT value FROM system_settings WHERE key = 'model_catalog.url'").Scan(&retainedCatalog))
	assert.Equal(t, catalog, retainedCatalog)
	require.NoError(t, db.Close())
	_, err = repo.Read(ctx)
	assert.Error(t, err)
	_, err = repo.Update(ctx, settings.Patch{CatalogURL: &replacement})
	assert.Error(t, err)
}

func TestSettingsIgnoredWriteRollsBackPatch(t *testing.T) {
	for _, key := range []string{"model_catalog.url", "model_announcements.teams_webhook_url"} {
		t.Run(key, func(t *testing.T) {
			db := openSettingsDB(t, ":memory:")
			repo := NewSettingsRepo(db)
			ctx := context.Background()
			before, err := repo.Read(ctx)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER ignore_settings_write BEFORE UPDATE ON system_settings
 WHEN NEW.key = '` + key + `'
 BEGIN SELECT RAISE(IGNORE); END;`)
			require.NoError(t, err)
			catalog, webhook := "https://catalog.test/replacement.json", "https://teams.example.test/hooks/replacement-secret"
			values, err := repo.Update(ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &webhook})
			assert.Error(t, err, "a skipped write must not report success")
			assert.Equal(t, settings.Values{}, values)
			after, err := repo.Read(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after, "the entire patch must roll back, including an earlier successful write")
		})
	}
}

func TestSettingsPatchOmissionAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	db := openSettingsDB(t, path)
	repo := NewSettingsRepo(db)
	svc := settings.NewService(repo)
	ctx := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	webhook := "https://teams.example.test/hooks/retain-secret"
	_, err := svc.Update(ctx, settings.Patch{TeamsWebhookURL: &webhook})
	require.NoError(t, err)
	catalog := "https://catalog.example.test/only-url.json"
	public, err := svc.Update(ctx, settings.Patch{CatalogURL: &catalog})
	require.NoError(t, err)
	assert.True(t, public.TeamsConfigured)
	assert.NotEqual(t, webhook, public.TeamsWebhookURL)
	got, err := svc.ReadInternal(ctx)
	require.NoError(t, err)
	assert.Equal(t, settings.Values{CatalogURL: catalog, TeamsWebhookURL: webhook}, got)
	_, err = svc.Update(ctx, settings.Patch{})
	require.NoError(t, err)
	got, err = svc.ReadInternal(ctx)
	require.NoError(t, err)
	assert.Equal(t, webhook, got.TeamsWebhookURL)
	empty := ""
	public, err = svc.Update(ctx, settings.Patch{TeamsWebhookURL: &empty})
	require.NoError(t, err)
	assert.False(t, public.TeamsConfigured)
	assert.Empty(t, public.TeamsWebhookURL)
	got, err = svc.ReadInternal(ctx)
	require.NoError(t, err)
	assert.Empty(t, got.TeamsWebhookURL)
	assert.Equal(t, catalog, got.CatalogURL)
	// Independent connections/repository objects rule out an instance-local mutex
	// or a read-then-overwrite of both fields as the atomic merge mechanism.
	other := NewSettingsRepo(openSettingsDB(t, path))
	const concurrencyRounds = 20
	for i := 0; i < concurrencyRounds; i++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Update(ctx, settings.Patch{CatalogURL: &catalog})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := other.Update(ctx, settings.Patch{TeamsWebhookURL: &webhook})
			errs <- err
		}()
		close(start)
		wg.Wait()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		got, err = repo.Read(ctx)
		require.NoError(t, err)
		assert.Equal(t, settings.Values{CatalogURL: catalog, TeamsWebhookURL: webhook}, got)
		_, err = repo.Update(ctx, settings.Patch{CatalogURL: &empty, TeamsWebhookURL: &empty})
		require.NoError(t, err)
	}
}
