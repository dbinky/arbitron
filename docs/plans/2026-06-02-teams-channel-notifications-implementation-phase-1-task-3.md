# Phase 1 — Task 3: HTTP Config Surface (read redaction + PUT update)

**Goal:** Extend `GET /system/config` to return a redacted `teams` block and `server.publicBaseUrl`, and add `PUT /system/config/teams` to update the in-memory Teams config with validation.

**Architecture:** Hexagonal — `adapter/http/server.go`'s `SystemHandler` reads/writes the shared `*config.Config` pointer under its `h.mu` mutex. Reads redact the webhook (`"**REDACTED**"` when set, `""` when unset), mirroring the existing `APIKey` redaction. The PUT uses pointer fields for partial updates and rejects non-`http(s)` URLs with `400`, leaving config unchanged.

**Tech Stack:** Go stdlib `net/http`, `net/url`, `encoding/json`, chi router. Tests with `httptest` + `testify`.

---

## Context

- Edit: `adapter/http/server.go`
- Edit test: `adapter/http/server_test.go` (existing `TestRouter_SystemConfig`, `newRouterTestDeps`, `adapthttp.NewRouter(...)`)
- `SystemHandler` struct + `systemConfigResponse` + `serverConfigResponse` + `GetConfig` are at `server.go:237-339`; `UpdateQueueConfig` at `server.go:347`; routes registered at `server.go:151-152`.
- Redaction rule (from design): `"**REDACTED**"` when `WebhookURL != ""`, else `""`.
- `newRouterTestDeps` builds a `*config.Config` literal at `server_test.go:117`.

## Steps

### 1. Write the failing test: GET returns redacted teams + publicBaseUrl

- [x] In `adapter/http/server_test.go`, add at the end of the file:

```go
// Success: GET /system/config exposes redacted teams config + publicBaseUrl.
func TestRouter_SystemConfig_TeamsRedactedAndBaseURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Server.PublicBaseURL = "http://localhost:8080"
	cfg.Notifications.Teams.WebhookURL = "https://example.com/hook"
	cfg.Notifications.Teams.Enabled = true
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	teams, ok := resp["teams"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "**REDACTED**", teams["webhookUrl"])
	assert.Equal(t, true, teams["enabled"])

	server, ok := resp["server"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "http://localhost:8080", server["publicBaseUrl"])
}

// Edge-case: unset webhook reads back as "" with enabled=false.
func TestRouter_SystemConfig_TeamsUnset(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/config", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	teams := resp["teams"].(map[string]any)
	assert.Equal(t, "", teams["webhookUrl"])
	assert.Equal(t, false, teams["enabled"])
}
```

### 2. Run — confirm it FAILS

- [x] Run:

```bash
go test ./adapter/http/ -run TestRouter_SystemConfig_Teams
```

- [x] Expected: panic/failure because `resp["teams"]` is `nil` (`not ok`):

```
--- FAIL: TestRouter_SystemConfig_TeamsRedactedAndBaseURL (0.00s)
    server_test.go:NN: ... require.True(... ok) failed
FAIL
```

### 3. Add the response structs + populate them in `GetConfig`

- [x] In `adapter/http/server.go`, add `PublicBaseURL` to `serverConfigResponse`:

```go
type serverConfigResponse struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	PublicBaseURL string `json:"publicBaseUrl"`
}
```

- [x] Add a `teamsConfigResponse` type next to the other `*ConfigResponse` types:

```go
type teamsConfigResponse struct {
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhookUrl"` // redacted: "**REDACTED**" or ""
}
```

- [x] Add a `Teams` field to `systemConfigResponse`:

```go
type systemConfigResponse struct {
	Server    serverConfigResponse    `json:"server"`
	Database  databaseConfigResponse  `json:"database"`
	Workspace workspaceConfigResponse `json:"workspace"`
	LLM       llmConfigResponse       `json:"llm"`
	Logging   loggingConfigResponse   `json:"logging"`
	Tracing   tracingConfigResponse   `json:"tracing"`
	Queue     queueConfigResponse     `json:"queue"`
	Teams     teamsConfigResponse     `json:"teams"`
}
```

- [x] In `GetConfig`, read the Teams config under the existing mutex. Replace the locked block:

```go
	h.mu.Lock()
	globalCap := h.config.Queue.GlobalCap
	perUserCap := h.config.Queue.PerUserCap
	h.mu.Unlock()
```

with:

```go
	h.mu.Lock()
	globalCap := h.config.Queue.GlobalCap
	perUserCap := h.config.Queue.PerUserCap
	teamsEnabled := h.config.Notifications.Teams.Enabled
	teamsWebhookSet := h.config.Notifications.Teams.WebhookURL != ""
	publicBaseURL := h.config.Server.PublicBaseURL
	h.mu.Unlock()

	redactedWebhook := ""
	if teamsWebhookSet {
		redactedWebhook = "**REDACTED**"
	}
```

- [x] In the same function, set `PublicBaseURL` in the `Server` field and add the `Teams` field to the `resp` literal. Replace the `Server:` line and add `Teams:` before the closing `}` of `systemConfigResponse{...}`:

```go
		Server: serverConfigResponse{
			Host:          h.config.Server.Host,
			Port:          h.config.Server.Port,
			PublicBaseURL: publicBaseURL,
		},
```

and add (after the `Queue:` block, before the closing brace):

```go
		Teams: teamsConfigResponse{
			Enabled:    teamsEnabled,
			WebhookURL: redactedWebhook,
		},
```

### 4. Run — confirm the GET tests PASS

- [x] Run:

```bash
go test ./adapter/http/ -run TestRouter_SystemConfig_Teams
```

- [x] Expected: `ok`.

### 5. Write the failing test: PUT success + partial update

- [x] In `adapter/http/server_test.go`, add:

```go
// Success: PUT /system/config/teams updates URL + toggle; GET reflects it.
func TestRouter_UpdateTeamsConfig_Success(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":"https://example.com/hook","enabled":true}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "https://example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
}

// Edge-case: a toggle-only PUT preserves the existing webhook URL.
func TestRouter_UpdateTeamsConfig_PartialPreservesURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://kept.example.com/hook"
	cfg.Notifications.Teams.Enabled = true
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"enabled":false}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "https://kept.example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
}

// Failure: a non-http(s) webhook URL is rejected with 400, config unchanged.
func TestRouter_UpdateTeamsConfig_InvalidURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://old.example.com/hook"
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":"ftp://nope"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "https://old.example.com/hook", cfg.Notifications.Teams.WebhookURL)
}

// Error: a malformed JSON body is rejected with 400.
func TestRouter_UpdateTeamsConfig_MalformedJSON(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Edge-case: clearing the URL with an explicit empty string is allowed.
func TestRouter_UpdateTeamsConfig_ClearURL(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	cfg.Notifications.Teams.WebhookURL = "https://old.example.com/hook"
	router := adapthttp.NewRouter(svc, wfRepo, instRepo, nodeExecRepo, snapRepo, fileReader, bus, cfg, qm, qr, nil, nil, nil, nil)

	body := `{"webhookUrl":""}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/system/config/teams", strings.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "", cfg.Notifications.Teams.WebhookURL)
}
```

### 6. Run — confirm it FAILS

- [x] Run:

```bash
go test ./adapter/http/ -run TestRouter_UpdateTeamsConfig
```

- [x] Expected: all subtests fail with `404` (route not registered) so status assertions fail:

```
--- FAIL: TestRouter_UpdateTeamsConfig_Success (0.00s)
    expected: 200
    actual  : 404
```

### 7. Register the route

- [x] In `adapter/http/server.go`, immediately after the queue route (`server.go:152`):

```go
		r.Put("/system/config/queue", sysHandler.UpdateQueueConfig)
		r.Put("/system/config/teams", sysHandler.UpdateTeamsConfig)
```

### 8. Add the `net/url` import

- [x] In `adapter/http/server.go`, add `"net/url"` to the import block (keep imports grouped/sorted with the other stdlib imports).

### 9. Implement `UpdateTeamsConfig`

- [x] In `adapter/http/server.go`, add immediately after `UpdateQueueConfig` (after `server.go:400`):

```go
// updateTeamsConfigRequest is the JSON body for updating Teams notification
// config. Pointer fields distinguish "absent" (leave unchanged) from "set".
type updateTeamsConfigRequest struct {
	WebhookURL *string `json:"webhookUrl"`
	Enabled    *bool   `json:"enabled"`
}

// validateTeamsWebhookURL reports whether raw is an acceptable webhook URL.
// An empty string is acceptable (clears the webhook); a non-empty value must
// be an absolute http or https URL with a host.
func validateTeamsWebhookURL(raw string) bool {
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

// UpdateTeamsConfig handles PUT /system/config/teams. Partial updates are
// supported via pointer fields. A non-empty webhook URL must be an absolute
// http(s) URL; otherwise the request is rejected and config is left unchanged.
func (h *SystemHandler) UpdateTeamsConfig(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req updateTeamsConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	if req.WebhookURL != nil && !validateTeamsWebhookURL(*req.WebhookURL) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "webhookUrl must be an absolute http(s) URL")
		return
	}

	h.mu.Lock()
	if req.WebhookURL != nil {
		h.config.Notifications.Teams.WebhookURL = *req.WebhookURL
	}
	if req.Enabled != nil {
		h.config.Notifications.Teams.Enabled = *req.Enabled
	}
	enabled := h.config.Notifications.Teams.Enabled
	webhookSet := h.config.Notifications.Teams.WebhookURL != ""
	h.mu.Unlock()

	redactedWebhook := ""
	if webhookSet {
		redactedWebhook = "**REDACTED**"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":    enabled,
		"webhookUrl": redactedWebhook,
	})
}
```

### 10. Run — confirm the PUT tests PASS

- [x] Run:

```bash
go test ./adapter/http/ -run TestRouter_UpdateTeamsConfig
```

- [x] Expected: `ok`.

### 11. Run the full HTTP package suite (no regressions)

- [x] Run:

```bash
go test ./adapter/http/
```

- [x] Expected: `ok` (all existing handler tests still pass).

### 12. Commit

- [x] Run:

```bash
git add adapter/http/server.go adapter/http/server_test.go
git commit -m "feat(http): redacted Teams config read + PUT /system/config/teams"
```
