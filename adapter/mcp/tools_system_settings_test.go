package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Forward to SQLite so authorization checks observe both reads and writes.
type observedSettingsStore struct {
	settings.SystemSettings
	calls int
}

func (s *observedSettingsStore) Read(ctx context.Context) (settings.Values, error) {
	s.calls++
	return s.SystemSettings.Read(ctx)
}

func (s *observedSettingsStore) Update(ctx context.Context, patch settings.Patch) (settings.Values, error) {
	s.calls++
	return s.SystemSettings.Update(ctx, patch)
}

func TestModelSettingsMCP(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	store := &observedSettingsStore{SystemSettings: sqlite.NewSettingsRepo(db)}
	svc := settings.NewService(store)
	srv := adaptmcp.NewServerWithDeps("settings", "test", &adaptmcp.Deps{SystemSettings: svc})
	adaptmcp.RegisterSystemTools(srv)
	admin := adminIdentityCtx(uuid.New())
	initial, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	store.calls = 0
	for _, ctx := range []context.Context{context.Background(), nonAdminIdentityCtx(uuid.New())} {
		for _, tool := range []string{"system_settings_get", "system_settings_update"} {
			for _, args := range []map[string]any{{}, {"catalog_url": "https://unauthorized.test"}, {"teams_webhook_url": nil}, {"unknown": 42}} {
				assert.True(t, callToolCtx(t, srv, ctx, tool, args).IsError)
				assert.Zero(t, store.calls, "unauthorized tool call must not read or write settings")
			}
		}
	}
	unchanged, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, initial, unchanged, "unauthorized updates must not persist")
	const secret = "https://teams.example.test/hooks/mcp-secret"
	result := callToolCtx(t, srv, admin, "system_settings_update", map[string]any{"catalog_url": "https://catalog.test/mcp.json", "teams_webhook_url": secret})
	require.False(t, result.IsError)
	stored, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, secret, stored.TeamsWebhookURL)
	for _, args := range []map[string]any{{"unknown": secret}, {"teams_webhook_url": "**REDACTED**"}, {"teams_webhook_url": nil}, {"catalog_url": 42}} {
		result = callToolCtx(t, srv, admin, "system_settings_update", args)
		assert.True(t, result.IsError)
		data, err := json.Marshal(result)
		require.NoError(t, err)
		assert.NotContains(t, string(data), secret)
		after, err := svc.ReadInternal(admin)
		require.NoError(t, err)
		assert.Equal(t, stored, after)
	}
	require.False(t, callToolCtx(t, srv, admin, "system_settings_update", map[string]any{"catalog_url": "https://catalog.test/only.json"}).IsError)
	after, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, secret, after.TeamsWebhookURL)
	data, err := json.Marshal(callToolCtx(t, srv, admin, "system_settings_get", nil))
	require.NoError(t, err)
	assert.Contains(t, string(data), `teamsConfigured`)
	assert.NotContains(t, string(data), secret)
	_, err = db.Exec(`CREATE TRIGGER fail_settings BEFORE UPDATE ON system_settings BEGIN SELECT RAISE(ABORT, 'https://teams.example.test/hooks/mcp-secret'); END;`)
	require.NoError(t, err)
	result = callToolCtx(t, srv, admin, "system_settings_update", map[string]any{"catalog_url": "https://catalog.test/fault.json"})
	require.True(t, result.IsError)
	data, err = json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(data), secret)
	unchanged, err = svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, after, unchanged)
	_, err = db.Exec(`DROP TRIGGER fail_settings`)
	require.NoError(t, err)
	require.False(t, callToolCtx(t, srv, admin, "system_settings_update", map[string]any{"teams_webhook_url": ""}).IsError)
	after, err = svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Empty(t, after.TeamsWebhookURL)
	require.NoError(t, db.Close())
	for _, tool := range []string{"system_settings_get", "system_settings_update"} {
		assert.True(t, callToolCtx(t, srv, admin, tool, map[string]any{}).IsError)
	}
	srv = adaptmcp.NewServerWithDeps("missing", "test", &adaptmcp.Deps{})
	adaptmcp.RegisterSystemTools(srv)
	for _, tool := range []string{"system_settings_get", "system_settings_update"} {
		assert.True(t, callToolCtx(t, srv, admin, tool, map[string]any{}).IsError)
	}
}
