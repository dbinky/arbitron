# Phase 1 — Task 4: MCP Config Surface (redacted read + update tool)

**Goal:** Redact the Teams webhook in the `system_config` MCP tool result and add a `system_update_teams_config` tool that writes the same in-memory config the HTTP handler does.

**Architecture:** Hexagonal — `adapter/mcp/tools_system.go` registers MCP tools over the shared `srv.deps.Config` pointer. `system_config` currently marshals the raw `*config.Config`, which would leak the webhook; we marshal a value-copy with the webhook redacted. The update tool mirrors `system_update_queue_config`, validating a non-empty webhook as an absolute http(s) URL.

**Tech Stack:** Go, `mcp-go` (`github.com/mark3labs/mcp-go`), `net/url`. Tests with `testify` against the in-process MCP server harness (`newSystemServer`, `callTool`, `extractText`).

---

## Context

- Edit: `adapter/mcp/tools_system.go`
- Edit test: `adapter/mcp/tools_system_test.go` (harness `newSystemServer`, `callTool`, `extractText` at `tools_system_test.go:135+`)
- `system_config` handler at `tools_system.go:74`; queue update handler at `tools_system.go:104`; registration in `RegisterSystemTools` at `tools_system.go:16`.
- The MCP read must redact identically to the HTTP read (Task 3): `"**REDACTED**"` when set, `""` when unset.

## Steps

### 1. Write the failing test: system_config redacts the webhook

- [ ] In `adapter/mcp/tools_system_test.go`, add:

```go
// Edge-case: system_config never echoes the raw Teams webhook URL.
func TestSystemConfig_RedactsTeamsWebhook(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://secret.example.com/hook", Enabled: true},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})
	result := callTool(t, srv, "system_config", nil)

	assert.False(t, result.IsError)
	text := extractText(t, result)
	assert.Contains(t, text, "**REDACTED**")
	assert.NotContains(t, text, "secret.example.com")
}
```

### 2. Run — confirm it FAILS

- [ ] Run:

```bash
go test ./adapter/mcp/ -run TestSystemConfig_RedactsTeamsWebhook
```

- [ ] Expected: fails because the raw URL leaks:

```
--- FAIL: TestSystemConfig_RedactsTeamsWebhook (0.00s)
    Error: "...secret.example.com..." should not contain "secret.example.com"
FAIL
```

### 3. Redact the webhook in `systemConfigHandler`

- [ ] In `adapter/mcp/tools_system.go`, in `systemConfigHandler`, replace the `result` map construction (the block that sets `"config": srv.deps.Config`):

```go
		// Marshal a value-copy with the Teams webhook redacted so the raw URL is
		// never echoed on read, matching the HTTP GET /system/config behavior.
		redacted := *srv.deps.Config
		if redacted.Notifications.Teams.WebhookURL != "" {
			redacted.Notifications.Teams.WebhookURL = "**REDACTED**"
		}

		result := map[string]any{
			"config": redacted,
			"queue": map[string]int{
				"running": running,
				"queued":  queued,
				"waiting": waiting,
			},
		}
		return MarshalResult(result)
```

### 4. Run — confirm it PASSES

- [ ] Run:

```bash
go test ./adapter/mcp/ -run TestSystemConfig_RedactsTeamsWebhook
```

- [ ] Expected: `ok`.

### 5. Write the failing test: system_update_teams_config

- [ ] In `adapter/mcp/tools_system_test.go`, add:

```go
// Success: system_update_teams_config writes URL + toggle to in-memory config.
func TestSystemUpdateTeamsConfig_Success(t *testing.T) {
	cfg := &config.Config{}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{
		"webhookUrl": "https://example.com/hook",
		"enabled":    true,
	})

	assert.False(t, result.IsError)
	assert.Equal(t, "https://example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
	text := extractText(t, result)
	assert.Contains(t, text, "**REDACTED**")
	assert.NotContains(t, text, "example.com/hook")
}

// Edge-case: omitting webhookUrl preserves the stored URL (toggle-only update).
func TestSystemUpdateTeamsConfig_PartialPreservesURL(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://kept.example.com/hook", Enabled: true},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"enabled": false})

	assert.False(t, result.IsError)
	assert.Equal(t, "https://kept.example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Failure: a non-http(s) webhook URL is rejected, config unchanged.
func TestSystemUpdateTeamsConfig_InvalidURL(t *testing.T) {
	cfg := &config.Config{
		Notifications: config.NotificationsConfig{
			Teams: config.TeamsConfig{WebhookURL: "https://old.example.com/hook"},
		},
	}
	srv := newSystemServer(cfg, &stubQueueRepo{}, &stubQueueManager{}, &stubNotificationRepo{}, &stubInstanceRepo{})

	result := callTool(t, srv, "system_update_teams_config", map[string]any{"webhookUrl": "ftp://nope"})

	assert.True(t, result.IsError)
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}
```

### 6. Run — confirm it FAILS

- [ ] Run:

```bash
go test ./adapter/mcp/ -run TestSystemUpdateTeamsConfig
```

- [ ] Expected: fails because the tool is not registered (`callTool` returns an error result / unknown tool).

### 7. Add the `net/url` import

- [ ] In `adapter/mcp/tools_system.go`, add `"net/url"` to the import block (with the other stdlib imports).

### 8. Register the new tool

- [ ] In `adapter/mcp/tools_system.go`, in `RegisterSystemTools`, after the `system_update_queue_config` registration block (`tools_system.go:24-31`), add:

```go
	srv.AddTool(
		gomcp.NewTool("system_update_teams_config",
			gomcp.WithDescription("Update the global Teams notification webhook URL and enable toggle"),
			gomcp.WithString("webhookUrl", gomcp.Description("Teams Workflows webhook URL (omit to leave unchanged; empty string clears it)")),
			gomcp.WithBoolean("enabled", gomcp.Description("Enable/disable Teams forwarding (omit to leave unchanged)")),
		),
		systemUpdateTeamsConfigHandler(srv),
	)
```

### 9. Implement the handler + helper

- [ ] In `adapter/mcp/tools_system.go`, add after `systemUpdateQueueConfigHandler` (after `tools_system.go:158`):

```go
// validTeamsWebhookURL reports whether raw is acceptable as a Teams webhook
// URL: an empty string (clears it) or an absolute http(s) URL with a host.
func validTeamsWebhookURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

func systemUpdateTeamsConfigHandler(srv *Server) server.ToolHandlerFunc {
	return func(_ context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Config == nil {
			return gomcp.NewToolResultError("config not available"), nil
		}
		args := request.GetArguments()

		if raw, ok := args["webhookUrl"]; ok {
			webhookURL, ok := raw.(string)
			if !ok {
				return gomcp.NewToolResultError("webhookUrl must be a string"), nil
			}
			if !validTeamsWebhookURL(webhookURL) {
				return gomcp.NewToolResultError("webhookUrl must be an absolute http(s) URL"), nil
			}
			srv.deps.Config.Notifications.Teams.WebhookURL = webhookURL
		}

		if raw, ok := args["enabled"]; ok {
			enabled, ok := raw.(bool)
			if !ok {
				return gomcp.NewToolResultError("enabled must be a boolean"), nil
			}
			srv.deps.Config.Notifications.Teams.Enabled = enabled
		}

		redactedWebhook := ""
		if srv.deps.Config.Notifications.Teams.WebhookURL != "" {
			redactedWebhook = "**REDACTED**"
		}

		return MarshalResult(map[string]any{
			"status":     "updated",
			"enabled":    srv.deps.Config.Notifications.Teams.Enabled,
			"webhookUrl": redactedWebhook,
		})
	}
}
```

### 10. Run — confirm the update tests PASS

- [ ] Run:

```bash
go test ./adapter/mcp/ -run TestSystemUpdateTeamsConfig
```

- [ ] Expected: `ok`.

### 11. Run the full MCP package suite (no regressions)

- [ ] Run:

```bash
go test ./adapter/mcp/
```

- [ ] Expected: `ok` (existing `TestSystemConfig`, `TestSystemUpdateQueueConfig`, etc. still pass).

### 12. Commit

- [ ] Run:

```bash
git add adapter/mcp/tools_system.go adapter/mcp/tools_system_test.go
git commit -m "feat(mcp): redact Teams webhook in system_config + system_update_teams_config tool"
```
