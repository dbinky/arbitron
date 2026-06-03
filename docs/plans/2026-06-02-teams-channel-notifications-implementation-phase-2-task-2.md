# Phase 2 — Task 2: TeamsNotifier (best-effort delivery)

**Goal:** Implement `TeamsNotifier`, an `ExternalNotifier` that reads live config, gates on enabled+URL, and POSTs the Adaptive Card to the webhook — best-effort and silent (failures logged, `Notify` always returns `nil`).

**Architecture:** Hexagonal — `adapter/notification/teams_notifier.go` implements the domain `notification.ExternalNotifier` port. It holds the shared `*config.Config` pointer (live reads so an admin toggle takes effect on the next call), a static `publicBaseURL`, a bounded `*http.Client`, and a `*slog.Logger`. Disabled/empty ⇒ clean no-op, zero HTTP. Transport error or non-2xx ⇒ logged, `nil` returned.

**Tech Stack:** Go stdlib `net/http`, `bytes`, `context`, `log/slog`, `time`. Black-box tests (`package notification_test`) with `httptest` + `testify`.

---

## Context

- New file: `adapter/notification/teams_notifier.go` (package `notification`)
- New test: `adapter/notification/teams_notifier_test.go` (black-box `package notification_test`, mirroring `log_notifier_test.go`)
- Port: `domain/notification.ExternalNotifier { Notify(ctx, *Notification) error }`
- Config read: `cfg.Notifications.Teams.{Enabled,WebhookURL}` and the injected `publicBaseURL`.
- Uses `buildTeamsPayload` from Task 1 (same package, unexported — callable internally).

## Steps

### 1. Write the failing test

- [ ] Create `adapter/notification/teams_notifier_test.go`:

```go
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
	n := adaptnotif.NewTeamsNotifier(cfg, "http://localhost:8080", srv.Client(), discardLogger())

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
	n := adaptnotif.NewTeamsNotifier(cfg, "http://localhost:8080", srv.Client(), discardLogger())

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))
}

// Edge-case: enabled but empty URL is also a zero-HTTP no-op.
func TestTeamsNotifier_Notify_EmptyURLNoOp(t *testing.T) {
	cfg := &config.Config{Notifications: config.NotificationsConfig{
		Teams: config.TeamsConfig{WebhookURL: "", Enabled: true},
	}}
	n := adaptnotif.NewTeamsNotifier(cfg, "http://localhost:8080", http.DefaultClient, discardLogger())
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
	n := adaptnotif.NewTeamsNotifier(cfg, "", srv.Client(), logger)

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
	n := adaptnotif.NewTeamsNotifier(cfg, "", http.DefaultClient, logger)

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Contains(t, logBuf.String(), "failed")
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
	n := adaptnotif.NewTeamsNotifier(cfg, "", srv.Client(), discardLogger())

	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(0), atomic.LoadInt32(&calls))

	cfg.Notifications.Teams.Enabled = true // live edit via shared pointer
	require.NoError(t, n.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}
```

### 2. Run — confirm it FAILS (does not compile)

- [ ] Run:

```bash
go test ./adapter/notification/ -run TestTeamsNotifier
```

- [ ] Expected: build failure, `undefined: adaptnotif.NewTeamsNotifier`.

### 3. Implement the notifier

- [ ] Create `adapter/notification/teams_notifier.go`:

```go
package notification

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/notification"
)

// Compile-time assertion that *TeamsNotifier satisfies the port.
var _ notification.ExternalNotifier = (*TeamsNotifier)(nil)

// defaultTeamsTimeout bounds a single webhook POST so a hung endpoint cannot
// stall the notification path.
const defaultTeamsTimeout = 10 * time.Second

// TeamsNotifier forwards notifications to a Microsoft Teams Workflows webhook.
// Delivery is best-effort and silent: gating and failures never produce an
// error, so the engine's notification path is never blocked.
type TeamsNotifier struct {
	config        *config.Config // shared pointer; read live each call
	publicBaseURL string
	client        *http.Client
	logger        *slog.Logger
}

// NewTeamsNotifier constructs a TeamsNotifier. A nil client gets a default
// client with a bounded timeout.
func NewTeamsNotifier(cfg *config.Config, publicBaseURL string, client *http.Client, logger *slog.Logger) *TeamsNotifier {
	if client == nil {
		client = &http.Client{Timeout: defaultTeamsTimeout}
	}
	return &TeamsNotifier{
		config:        cfg,
		publicBaseURL: publicBaseURL,
		client:        client,
		logger:        logger,
	}
}

// Notify reads the live Teams config and, when enabled with a URL, POSTs an
// Adaptive Card to the webhook. Disabled/empty is a clean no-op. Any build,
// transport, or non-2xx error is logged and swallowed; Notify always nil.
func (t *TeamsNotifier) Notify(ctx context.Context, n *notification.Notification) error {
	enabled := t.config.Notifications.Teams.Enabled
	webhookURL := t.config.Notifications.Teams.WebhookURL
	if !enabled || webhookURL == "" {
		return nil
	}

	payload, err := buildTeamsPayload(n, t.publicBaseURL)
	if err != nil {
		t.logger.Warn("teams: failed to build payload", "error", err)
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		t.logger.Warn("teams: failed to build request", "error", err)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		t.logger.Warn("teams: webhook delivery failed", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.logger.Warn("teams: webhook returned non-success status", "status", resp.StatusCode)
		return nil
	}
	return nil
}
```

### 4. Run — confirm it PASSES

- [ ] Run:

```bash
go test ./adapter/notification/ -run TestTeamsNotifier
```

- [ ] Expected: `ok`.

### 5. Run the full notification package + race detector

- [ ] Run:

```bash
go test -race ./adapter/notification/
```

- [ ] Expected: `ok` (no data races in the sequential tests).

### 6. Commit

- [ ] Run:

```bash
git add adapter/notification/teams_notifier.go adapter/notification/teams_notifier_test.go
git commit -m "feat(notification): best-effort silent TeamsNotifier"
```
