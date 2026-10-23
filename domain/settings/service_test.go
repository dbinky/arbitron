package settings_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func adminContext() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
}

func settingsService(t *testing.T) (*settings.Service, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	return settings.NewService(sqlite.NewSettingsRepo(db)), db
}

func TestSettingsPublicRedacts(t *testing.T) {
	svc, _ := settingsService(t)
	ctx := adminContext()
	defaults, err := svc.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://models.dev/api.json", defaults.CatalogURL)
	assert.Empty(t, defaults.TeamsWebhookURL)
	assert.False(t, defaults.TeamsConfigured)
	webhook := "https://teams.example.test/hooks/submitted-secret"
	public, err := svc.Update(ctx, settings.Patch{TeamsWebhookURL: &webhook})
	require.NoError(t, err)
	assert.True(t, public.TeamsConfigured)
	assert.Equal(t, config.RedactedSecret, public.TeamsWebhookURL)
	assert.NotEqual(t, webhook, public.TeamsWebhookURL)
	readPublic, err := svc.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, public, readPublic)
	internal, err := svc.ReadInternal(context.Background())
	require.NoError(t, err)
	assert.Equal(t, webhook, internal.TeamsWebhookURL)
}

func TestSettingsRejectsNonAdminAndInvalidURLs(t *testing.T) {
	svc, db := settingsService(t)
	ctx := adminContext()
	catalog, webhook := "https://catalog.example.test/models.json", "https://teams.example.test/hooks/secret"
	_, err := svc.Update(ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &webhook})
	require.NoError(t, err)
	before, err := svc.ReadInternal(ctx)
	require.NoError(t, err)
	// Observe writes at SQLite, including attempts to write unchanged values.
	_, err = db.Exec(`CREATE TABLE settings_writes (key TEXT);
 CREATE TRIGGER observe_settings_writes AFTER UPDATE ON system_settings
 BEGIN INSERT INTO settings_writes VALUES (NEW.key); END;`)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"absent", context.Background(), identity.ErrUnauthenticated},
		{"user", identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser}), identity.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Get(tc.ctx)
			assert.ErrorIs(t, err, tc.want)
			_, err = svc.Update(tc.ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &webhook})
			assert.ErrorIs(t, err, tc.want)
		})
	}
	for _, raw := range []string{"", "/relative", "ftp://catalog.example.test/models", "http://catalog.example.test/models.json", "https://user:password@catalog.example.test/models", "https:///models", config.RedactedSecret, "https://bad host/models"} {
		t.Run("catalog/"+raw, func(t *testing.T) {
			_, err := svc.Update(ctx, settings.Patch{CatalogURL: &raw, TeamsWebhookURL: &webhook})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "password")
		})
	}
	for _, raw := range []string{"invalid", "http://teams.example.test/hook", "https://127.0.0.1/hook", config.RedactedSecret} {
		t.Run("webhook/"+raw, func(t *testing.T) {
			_, err := svc.Update(ctx, settings.Patch{CatalogURL: &catalog, TeamsWebhookURL: &raw})
			require.Error(t, err)
		})
	}
	after, err := svc.ReadInternal(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	var writes int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM settings_writes").Scan(&writes))
	assert.Zero(t, writes)
}

func TestSettingsServiceReadAndWriteFaults(t *testing.T) {
	svc, db := settingsService(t)
	require.NoError(t, db.Close())
	storageErr := db.Ping()
	require.Error(t, storageErr)
	public, err := svc.Get(adminContext())
	assert.ErrorIs(t, err, storageErr)
	assert.Equal(t, settings.PublicValues{}, public)
	internal, err := svc.ReadInternal(context.Background())
	assert.ErrorIs(t, err, storageErr)
	assert.Equal(t, settings.Values{}, internal)
	catalog := "https://catalog.example.test/models.json"
	public, err = svc.Update(adminContext(), settings.Patch{CatalogURL: &catalog})
	assert.ErrorIs(t, err, storageErr)
	assert.Equal(t, settings.PublicValues{}, public)
}

func TestSettingsRejectsNilStore(t *testing.T) {
	t.Run("nil interface", func(t *testing.T) {
		require.Panics(t, func() { settings.NewService(nil) })
	})
	t.Run("typed nil", func(t *testing.T) {
		var repo *sqlite.SettingsRepo
		require.Panics(t, func() { settings.NewService(repo) })
	})
}
