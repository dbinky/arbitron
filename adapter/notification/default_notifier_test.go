package notification_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/config"
)

// Success: the default notifier fans out to BOTH the log notifier and Teams.
func TestNewDefaultExternalNotifier_FansOut(t *testing.T) {
	var teamsCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&teamsCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{PublicBaseURL: "http://localhost:8080"},
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: true},
		},
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// PR #18 item 14: the production default client carries a dial-time SSRF
	// guard that (correctly) blocks loopback, so the httptest server on
	// 127.0.0.1 cannot be reached through NewDefaultExternalNotifier's default
	// client. Compose the same fan-out (log + Teams) with an injected,
	// guard-free client to verify the wiring reaches both children. Real Teams
	// delivery is covered against loopback in teams_notifier_test.go via the
	// same injected-client seam; production loopback delivery is intentionally
	// refused.
	settings := config.NewSettingsStore(&cfg.Notifications.Teams)
	teams := adaptnotif.NewTeamsNotifier(settings, cfg.Server.PublicBaseURL, srv.Client(), logger)
	en := adaptnotif.NewMultiNotifier(logger, adaptnotif.NewLogNotifier(logger), teams)
	require.NotNil(t, en)
	require.NoError(t, en.Notify(context.Background(), sampleNotification()))

	// Teams delivery is fire-and-forget; the log child runs synchronously.
	assert.Eventually(t, func() bool { return atomic.LoadInt32(&teamsCalls) == 1 },
		2*time.Second, 5*time.Millisecond, "Teams child should be hit")
	assert.Contains(t, logBuf.String(), "external notification", "LogNotifier child should run")
}

// Edge-case: Teams disabled still runs the log notifier and makes zero HTTP calls.
func TestNewDefaultExternalNotifier_TeamsDisabled(t *testing.T) {
	var teamsCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&teamsCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: false},
		},
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	en := adaptnotif.NewDefaultExternalNotifier(config.NewSettingsStore(&cfg.Notifications.Teams), cfg.Server.PublicBaseURL, logger)
	require.NoError(t, en.Notify(context.Background(), sampleNotification()))

	assert.Equal(t, int32(0), atomic.LoadInt32(&teamsCalls))
	assert.Contains(t, logBuf.String(), "external notification")
}
