# Phase 1 — Task 2: Config Loader Mapping + Normalization

**Goal:** Map koanf keys `notifications.teams.webhook_url`, `notifications.teams.enabled`, and `server.public_base_url` onto the domain config, and normalize `PublicBaseURL` by trimming any trailing `/`.

**Architecture:** Hexagonal — `adapter/config/loader.go` is the koanf-based adapter that loads defaults → YAML file → env vars and unmarshals into the domain `Config`. koanf lowercases/flattens keys; struct fields unmarshal case-insensitively, so `notifications.teams.webhookurl` maps to `Notifications.Teams.WebhookURL`. Normalization is an in-place pass after unmarshal.

**Tech Stack:** Go, koanf (`github.com/knadh/koanf/v2` + providers), `strings`. Tests with `testing` + `testify`.

---

## Context

- Edit: `adapter/config/loader.go`
- Edit test: `adapter/config/loader_test.go` (existing patterns at `TestLoad_Defaults`, `TestLoad_YAMLOverride`, `TestLoad_EnvVarOverride`)
- The loader's `normalize(cfg *domainconfig.Config)` already exists and floors `LLM.TimeoutSeconds`; we extend it.
- koanf env provider lowercases and maps `_`→`.`, so `ARBITRON_SERVER_PUBLIC_BASE_URL` becomes `server.public.base.url` — which does **not** match the `public_base_url` YAML key path. Therefore the env var name for the base URL is `ARBITRON_SERVER_PUBLICBASEURL` (maps to `server.publicbaseurl`, unmarshals to `PublicBaseURL`). Document this in the YAML defaults comment. We do **not** add a defaults map entry (empty string is the zero value); we only add a documented YAML comment + normalization.

## Steps

### 1. Write the failing test: YAML maps the new keys

- [ ] In `adapter/config/loader_test.go`, add this test at the end of the file:

```go
// Happy-path: notifications.teams.* and server.public_base_url load from YAML.
func TestLoad_TeamsAndPublicBaseURL_FromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
server:
  host: "127.0.0.1"
  port: 8080
  publicbaseurl: "http://localhost:8080"
notifications:
  teams:
    webhookurl: "https://example.com/hook"
    enabled: true
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "http://localhost:8080", cfg.Server.PublicBaseURL)
	assert.Equal(t, "https://example.com/hook", cfg.Notifications.Teams.WebhookURL)
	assert.True(t, cfg.Notifications.Teams.Enabled)
}

// Edge-case: a trailing slash on the public base URL is normalized away.
func TestLoad_PublicBaseURL_TrailingSlashTrimmed(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
server:
  publicbaseurl: "http://localhost:8080/"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080", cfg.Server.PublicBaseURL)
}

// Edge-case: multiple trailing slashes are all trimmed.
func TestLoad_PublicBaseURL_MultipleTrailingSlashesTrimmed(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
server:
  publicbaseurl: "http://localhost:8080///"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:8080", cfg.Server.PublicBaseURL)
}

// Edge-case: unset Teams config round-trips as disabled + empty.
func TestLoad_Teams_DefaultsDisabledEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Notifications.Teams.WebhookURL)
	assert.False(t, cfg.Notifications.Teams.Enabled)
	assert.Equal(t, "", cfg.Server.PublicBaseURL)
}
```

### 2. Run the test — confirm it FAILS

- [ ] Run:

```bash
go test ./adapter/config/ -run 'TestLoad_Teams|TestLoad_PublicBaseURL'
```

- [ ] Expected: `TestLoad_PublicBaseURL_TrailingSlashTrimmed` and `..._MultipleTrailingSlashesTrimmed` FAIL because no trimming happens yet:

```
--- FAIL: TestLoad_PublicBaseURL_TrailingSlashTrimmed (0.00s)
    loader_test.go:NN:
        expected: "http://localhost:8080"
        actual  : "http://localhost:8080/"
FAIL
```

(The `_FromJSON`/`_DefaultsDisabledEmpty` cases may already pass via koanf unmarshal — that is fine; the trimming cases drive the implementation.)

### 3. Add trailing-slash normalization to `normalize`

- [ ] In `adapter/config/loader.go`, replace the `normalize` function:

```go
// normalize applies safety floors and value normalization to a loaded config in
// place. It clamps a non-positive llm.timeoutseconds up to DefaultTimeoutSeconds
// so a misconfigured 0 cannot disable every deadline, and trims trailing
// slashes from the public base URL so callers can concatenate "/instances/<id>"
// deterministically.
func normalize(cfg *domainconfig.Config) {
	if cfg.LLM.TimeoutSeconds <= 0 {
		cfg.LLM.TimeoutSeconds = DefaultTimeoutSeconds
	}
	cfg.Server.PublicBaseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
}
```

(`strings` is already imported in this file.)

### 4. Document the new keys in the default YAML

- [ ] In `adapter/config/loader.go`, inside `defaultYAMLContent()`, replace the `server:` block (currently `host` + `port` only) with:

```go
	return `server:
  host: "127.0.0.1"
  port: 8080
  # publicbaseurl: ""
  #   Externally reachable base URL of the dashboard (e.g.
  #   "http://localhost:8080"). Used to build deep links in outbound
  #   notifications (Teams). Trailing slashes are trimmed automatically.
  #   Override via ARBITRON_SERVER_PUBLICBASEURL.
```

- [ ] Then, in the same `defaultYAMLContent()` string, add a `notifications` block immediately before the closing `tracing:` block. Replace:

```go
tracing:
  enabled: false
  exportdir: "./traces"
`
```

with:

```go
notifications:
  # teams:
  #   webhookurl: ""
  #     Microsoft Teams Workflows webhook URL. Sensitive: never echoed back on
  #     read. Override via ARBITRON_NOTIFICATIONS_TEAMS_WEBHOOKURL.
  #   enabled: false
  #     When true, every in-app notification is also forwarded to the Teams
  #     channel (best-effort, silent). Override via
  #     ARBITRON_NOTIFICATIONS_TEAMS_ENABLED.
tracing:
  enabled: false
  exportdir: "./traces"
`
```

### 5. Run the test — confirm it PASSES

- [ ] Run:

```bash
go test ./adapter/config/ -run 'TestLoad_Teams|TestLoad_PublicBaseURL'
```

- [ ] Expected:

```
ok  	github.com/arbitron/arbitron/adapter/config	0.0Ns
```

### 6. Run the full config package test suite (no regressions)

- [ ] Run:

```bash
go test ./adapter/config/
```

- [ ] Expected: `ok` with all existing `TestLoad_*` tests still passing.

### 7. Commit

- [ ] Run:

```bash
git add adapter/config/loader.go adapter/config/loader_test.go
git commit -m "feat(config): map + normalize Teams webhook and public base URL keys"
```
