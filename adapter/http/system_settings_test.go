package http_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Count boundary calls while forwarding every argument to the real SQLite sink.
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

func TestModelSettingsHTTP(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, sqlite.RunMigrations(db))
	store := &observedSettingsStore{SystemSettings: sqlite.NewSettingsRepo(db)}
	svc := settings.NewService(store)
	deps := adapthttp.RouterDeps{Config: &config.Config{}, SystemSettings: svc}
	router := adapthttp.NewRouterFromDeps(deps)
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	user := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleUser})
	call := func(ctx context.Context, method, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/system/settings", strings.NewReader(body)).WithContext(ctx))
		return w
	}
	initial, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	store.calls = 0
	for _, tc := range []struct {
		ctx    context.Context
		status int
	}{{context.Background(), 401}, {user, 403}} {
		for _, method := range []string{"GET", "PUT"} {
			for _, body := range []string{`{"catalogUrl":"https://unauthorized.test"}`, `{"teamsWebhookUrl":`, `{"teamsWebhookUrl":null}`, `{"unknown":42}`} {
				assert.Equal(t, tc.status, call(tc.ctx, method, body).Code)
				assert.Zero(t, store.calls, "unauthorized request must not read or write settings")
			}
		}
	}
	before, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, initial, before, "unauthorized updates must not persist")
	const secret = "https://teams.example.test/hooks/http-secret"
	w := call(admin, "PUT", `{"catalogUrl":"https://catalog.test/custom.json","teamsWebhookUrl":"`+secret+`"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"teamsConfigured":true`)
	assert.NotContains(t, w.Body.String(), secret)
	stored, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, secret, stored.TeamsWebhookURL)
	assert.NotEqual(t, before.CatalogURL, stored.CatalogURL)
	for _, body := range []string{`{"unknown":"` + secret + `"}`, `{"teamsWebhookUrl":"**REDACTED**"}`, `{"teamsWebhookUrl":null}`, `{"catalogUrl":"invalid"}`, `{} {}`} {
		w = call(admin, "PUT", body)
		assert.Equal(t, 400, w.Code, body)
		assert.NotContains(t, w.Body.String(), secret)
		after, err := svc.ReadInternal(admin)
		require.NoError(t, err)
		assert.Equal(t, stored, after)
	}
	require.Equal(t, 200, call(admin, "PUT", `{"catalogUrl":"https://catalog.test/only.json"}`).Code)
	after, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, secret, after.TeamsWebhookURL)
	_, err = db.Exec(`CREATE TRIGGER fail_settings BEFORE UPDATE ON system_settings BEGIN SELECT RAISE(ABORT, 'https://teams.example.test/hooks/http-secret'); END;`)
	require.NoError(t, err)
	w = call(admin, "PUT", `{"catalogUrl":"https://catalog.test/fault.json"}`)
	assert.Equal(t, 500, w.Code)
	assert.NotContains(t, w.Body.String(), secret)
	unchanged, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	assert.Equal(t, after, unchanged)
	assert.Contains(t, logs.String(), "system settings unavailable")
	assert.NotContains(t, logs.String(), secret)
	_, err = db.Exec(`DROP TRIGGER fail_settings`)
	require.NoError(t, err)
	require.Equal(t, 200, call(admin, "PUT", `{"teamsWebhookUrl":""}`).Code)
	assert.Contains(t, call(admin, "GET", "").Body.String(), `"teamsConfigured":false`)
	require.NoError(t, db.Close())
	for _, method := range []string{"GET", "PUT"} {
		w = call(admin, method, `{}`)
		assert.Equal(t, 500, w.Code)
		assert.NotContains(t, w.Body.String(), secret)
	}
	deps.SystemSettings = nil
	router = adapthttp.NewRouterFromDeps(deps)
	for _, method := range []string{"GET", "PUT"} {
		assert.Equal(t, 503, call(admin, method, `{}`).Code)
	}
}

func TestModelSettingsHTTPRejectsOversizedPatch(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	svc := settings.NewService(sqlite.NewSettingsRepo(db))
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{Config: &config.Config{}, SystemSettings: svc})
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	before, err := svc.ReadInternal(admin)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"large field", `{"teamsWebhookUrl":"https://teams.example.test/hooks/` + strings.Repeat("secret", 1<<18) + `"}`},
		{"large trailing whitespace", `{"catalogUrl":"https://catalog.test/replacement.json"}` + strings.Repeat(" ", 2<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/system/settings", strings.NewReader(tc.body)).WithContext(admin))
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.NotContains(t, w.Body.String(), "secret")
			after, err := svc.ReadInternal(admin)
			require.NoError(t, err)
			assert.True(t, before == after, "oversized patches must persist nothing")
		})
	}
}
