# Phase 1 — Task 1: Domain Config Types

**Goal:** Add the in-memory domain config types for Teams notifications and a static `PublicBaseURL` field, with no behavior change.

**Architecture:** Hexagonal — the domain `config` package holds pure data structures with zero dependencies. `Config` is the in-memory single source of truth shared by pointer across the HTTP, MCP, and (Phase 2) notifier surfaces. This task only widens the struct; no loader/handler/notifier wiring yet.

**Tech Stack:** Go, standard library only. Tests with `testing` + `testify` (already in `go.sum`).

---

## Context

- Edit: `domain/config/config.go`
- New test: `domain/config/config_test.go` (no test file exists for this package yet)
- Module path: `github.com/arbitron/arbitron`
- These are plain structs with no methods, so the "test" here is a compile-time + zero-value assertion that the fields exist and default sanely. This keeps strict-TDD discipline (write the assertion first, watch it fail to compile, then add the fields).

## Steps

### 1. Write the failing test for the new config fields

- [ ] Create `domain/config/config_test.go` with this exact content:

```go
package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/config"
)

// Happy-path: the new notifications/teams fields exist and round-trip values.
func TestConfig_TeamsFields_RoundTrip(t *testing.T) {
	cfg := config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{
				WebhookURL: "https://example.com/webhook",
				Enabled:    true,
			},
		},
	}

	assert.Equal(t, "https://example.com/webhook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
}

// Edge-case: zero value is a disabled, empty Teams config.
func TestConfig_TeamsFields_ZeroValue(t *testing.T) {
	var cfg config.Config

	assert.Equal(t, "", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Happy-path: ServerConfig exposes a static public base URL for deep links.
func TestServerConfig_PublicBaseURL_RoundTrip(t *testing.T) {
	sc := config.ServerConfig{PublicBaseURL: "http://localhost:8080"}

	assert.Equal(t, "http://localhost:8080", sc.PublicBaseURL)
}
```

### 2. Run the test — confirm it FAILS (does not compile)

- [ ] Run:

```bash
go test ./domain/config/
```

- [ ] Expected: a build failure naming the missing fields, e.g.:

```
domain/config/config_test.go:13:11: undefined: config.NotificationsConfig
...
FAIL	github.com/arbitron/arbitron/domain/config [build failed]
```

### 3. Add the `PublicBaseURL` field to `ServerConfig`

- [ ] In `domain/config/config.go`, replace the `ServerConfig` struct:

```go
// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host string
	Port int
	// PublicBaseURL is the externally reachable base URL of the dashboard
	// (e.g. "http://localhost:8080"), used to build deep links in outbound
	// notifications. Static: set via config file / env only, never UI-editable.
	// Any trailing "/" is trimmed by the loader so callers can concatenate
	// "/instances/<id>" deterministically.
	PublicBaseURL string
}
```

### 4. Add the notifications sub-config types

- [ ] In `domain/config/config.go`, add these type declarations immediately after the `ServerConfig` block:

```go
// TeamsConfig holds the single global Microsoft Teams webhook settings.
// Stored in memory only (admin-editable at runtime, not persisted), mirroring
// the QueueConfig precedent.
type TeamsConfig struct {
	// WebhookURL is the Teams Workflows webhook URL. Treated as sensitive:
	// never echoed back on any read surface.
	WebhookURL string
	// Enabled gates delivery. When false, Teams forwarding is a clean no-op.
	Enabled bool
}

// NotificationsConfig groups external notification-channel settings. Today it
// holds only Teams; future channels join here.
type NotificationsConfig struct {
	Teams TeamsConfig
}
```

### 5. Add the `Notifications` field to `Config`

- [ ] In `domain/config/config.go`, add the field to the `Config` struct (place it after `Script ScriptConfig`):

```go
// Config is the top-level configuration structure for the Arbitron system.
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Workspace     WorkspaceConfig
	LLM           LLMConfig
	Logging       LoggingConfig
	Tracing       TracingConfig
	Queue         QueueConfig
	Script        ScriptConfig
	Notifications NotificationsConfig
	Profiles      []ProfileConfig
}
```

### 6. Run the test — confirm it PASSES

- [ ] Run:

```bash
go test ./domain/config/
```

- [ ] Expected:

```
ok  	github.com/arbitron/arbitron/domain/config	0.00s
```

### 7. Confirm the whole module still builds

- [ ] Run:

```bash
go build ./...
```

- [ ] Expected: no output, exit code 0.

### 8. Commit

- [ ] Run:

```bash
git add domain/config/config.go domain/config/config_test.go
git commit -m "feat(config): add Teams notifications + PublicBaseURL config types"
```
