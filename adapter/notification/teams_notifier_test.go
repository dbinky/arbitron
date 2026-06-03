package notification_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/notification"
)

func sampleNotification() *notification.Notification {
	return &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.New(),
		WorkflowName: "wf",
		Type:         notification.TypeCompleted,
		CreatedAt:    time.Now(),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Success: an enabled notifier POSTs a JSON Adaptive Card to the webhook.
func TestTeamsNotifier_Notify_PostsCard(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"http://localhost:8080", srv.Client(), discardLogger())

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, "application/json", gotContentType)
	assert.Contains(t, string(gotBody), "AdaptiveCard")
}

// Edge-case: disabled config produces zero HTTP calls and no error.
func TestTeamsNotifier_Notify_DisabledNoOp(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: false},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"http://localhost:8080", srv.Client(), discardLogger())

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))
}

// Edge-case: enabled but empty URL is also a zero-HTTP no-op.
func TestTeamsNotifier_Notify_EmptyURLNoOp(t *testing.T) {
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: "", Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"http://localhost:8080", http.DefaultClient, discardLogger())
	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
}

// Failure: a 500 response is logged and swallowed (Notify returns nil).
func TestTeamsNotifier_Notify_Non2xxSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"", srv.Client(), logger)

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Contains(t, logBuf.String(), "non-success")
}

// Error: an unreachable host is logged and swallowed (Notify returns nil).
func TestTeamsNotifier_Notify_TransportErrorSwallowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // now unreachable

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: url, Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"", http.DefaultClient, logger)

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Contains(t, logBuf.String(), "failed")
}

// Security: the webhook URL is a bearer secret (the token lives in the query,
// ?sig=...). On a transport failure the raw *url.Error preserves that token, so
// the failure log must never contain it — host-only scrub.
func TestTeamsNotifier_Notify_DeliveryErrorDoesNotLogSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	secretURL := srv.URL + "/workflows/abc?sig=SUPERSECRETTOKEN"
	srv.Close() // now unreachable -> transport error path

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: secretURL, Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams), "", http.DefaultClient, logger)

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))

	logged := logBuf.String()
	assert.Contains(t, logged, "failed", "the failure should still be logged")
	assert.NotContains(t, logged, "SUPERSECRETTOKEN", "the bearer secret must not be logged")
	assert.NotContains(t, logged, "sig=", "the query string carrying the secret must not be logged")
}

// Edge-case: a nil logger does not panic on the failure path; the constructor
// defaults it to a discard logger so best-effort delivery stays panic-free.
func TestTeamsNotifier_Notify_NilLoggerNoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"", srv.Client(), nil)

	require.NotPanics(t, func() {
		require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	})
}

// Edge-case: a hung endpoint is bounded by the HTTP client timeout rather than
// stalling the notification path; the transport error is logged and swallowed.
func TestTeamsNotifier_Notify_TimeoutBoundsHungEndpoint(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // block until the test releases the handler
	}))
	defer srv.Close()
	defer close(release) // runs before srv.Close(): unblock the handler so Close() returns

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: true},
	}}
	client := &http.Client{Timeout: 50 * time.Millisecond}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"", client, logger)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- n.Notify(context.Background(), sampleNotification()) }()

	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Less(t, time.Since(start), 2*time.Second, "timeout should bound the hung endpoint")
		assert.Contains(t, logBuf.String(), "failed")
	case <-time.After(2 * time.Second):
		t.Fatal("Notify did not return; the client timeout did not bound the hung endpoint")
	}
}

// Edge-case: a live config edit between calls is observed on the second call.
func TestTeamsNotifier_Notify_LiveConfigEdit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: srv.URL, Enabled: false},
	}}
	n := adaptnotif.NewTeamsNotifier(config.NewSettingsStore(&cfg.Notifications.Teams),"", srv.Client(), discardLogger())

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))

	cfg.Notifications.Teams.Enabled = true // live edit via shared pointer
	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}
