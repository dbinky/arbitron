# Phase 2 — Task 4: Wire the Default External Notifier into main

**Goal:** Replace the lone `LogNotifier` wiring with a `MultiNotifier{LogNotifier, TeamsNotifier}`, via a testable `NewDefaultExternalNotifier` constructor, so every emitted notification also reaches Teams.

**Architecture:** Hexagonal — the fan-out composition lives in `adapter/notification` (a small `NewDefaultExternalNotifier` factory), keeping `cmd/arbitron/main.go` a thin wiring line. `main.go` already calls `eng.SetExternalNotifier(...)` at line 416-417; we swap the argument. The notifier holds the shared `cfg` pointer so runtime config edits take effect live.

**Tech Stack:** Go stdlib `log/slog`. Black-box tests (`package notification_test`) with `httptest` + `testify`. `main.go` change verified by `go build` + `go vet`.

---

## Context

- New factory: `adapter/notification/default_notifier.go` (package `notification`)
- New test: `adapter/notification/default_notifier_test.go` (black-box `package notification_test`; reuses `sampleNotification()` from `teams_notifier_test.go`)
- Edit: `cmd/arbitron/main.go` (lines 415-417, the "12b" block; imports already include `adaptnotif "github.com/arbitron/arbitron/adapter/notification"` and `"github.com/arbitron/arbitron/domain/config"`; `cfg` is the `*config.Config` returned by `configadapter.Load`).

## Steps

### 1. Write the failing test for the factory

- [x] Create `adapter/notification/default_notifier_test.go`:

```go
package notification_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

	en := adaptnotif.NewDefaultExternalNotifier(cfg, logger)
	require.NotNil(t, en)
	require.NoError(t, en.Notify(context.Background(), sampleNotification()))

	assert.Equal(t, int32(1), atomic.LoadInt32(&teamsCalls), "Teams child should be hit")
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

	en := adaptnotif.NewDefaultExternalNotifier(cfg, logger)
	require.NoError(t, en.Notify(context.Background(), sampleNotification()))

	assert.Equal(t, int32(0), atomic.LoadInt32(&teamsCalls))
	assert.Contains(t, logBuf.String(), "external notification")
}
```

### 2. Run — confirm it FAILS (does not compile)

- [x] Run:

```bash
go test ./adapter/notification/ -run TestNewDefaultExternalNotifier
```

- [x] Expected: build failure, `undefined: adaptnotif.NewDefaultExternalNotifier`.

### 3. Implement the factory

- [x] Create `adapter/notification/default_notifier.go`:

```go
package notification

import (
	"log/slog"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/notification"
)

// NewDefaultExternalNotifier builds the standard fan-out external notifier: the
// development LogNotifier plus a best-effort TeamsNotifier. The TeamsNotifier
// reads cfg live, so an admin toggle takes effect on the next notification.
func NewDefaultExternalNotifier(cfg *config.Config, logger *slog.Logger) notification.ExternalNotifier {
	logNotifier := NewLogNotifier(logger)
	teamsNotifier := NewTeamsNotifier(cfg, cfg.Server.PublicBaseURL, nil, logger)
	return NewMultiNotifier(logger, logNotifier, teamsNotifier)
}
```

### 4. Run — confirm the factory tests PASS

- [x] Run:

```bash
go test ./adapter/notification/ -run TestNewDefaultExternalNotifier
```

- [x] Expected: `ok`.

### 5. Swap the wiring in main.go

- [x] In `cmd/arbitron/main.go`, replace the "12b" block (lines ~415-417):

```go
	// 12b. Create external notifier (logging-only) and wire it to the engine.
	logNotifier := adaptnotif.NewLogNotifier(logger)
	eng.SetExternalNotifier(logNotifier)
```

with:

```go
	// 12b. Create the fan-out external notifier (log + best-effort Teams) and
	// wire it to the engine. The Teams child reads cfg live, so an admin toggle
	// via the dashboard / MCP takes effect on the next notification.
	externalNotifier := adaptnotif.NewDefaultExternalNotifier(cfg, logger)
	eng.SetExternalNotifier(externalNotifier)
```

### 6. Verify the binary builds and vets clean

- [x] Run:

```bash
go build ./...
```

- [x] Expected: no output, exit code 0.

- [x] Run:

```bash
go vet ./cmd/... ./adapter/notification/...
```

- [x] Expected: no output, exit code 0. (If `go vet` reports `logNotifier declared and not used` you missed removing the old lines — fix and re-run.)

### 7. Run the full notification package + a module-wide test sweep

- [x] Run:

```bash
go test ./adapter/notification/ && go test ./...
```

- [x] Expected: `ok` for `adapter/notification` and no new failures across the module.

### 8. Commit

- [x] Run:

```bash
git add adapter/notification/default_notifier.go adapter/notification/default_notifier_test.go cmd/arbitron/main.go
git commit -m "feat(notification): wire fan-out log+Teams notifier into engine"
```
