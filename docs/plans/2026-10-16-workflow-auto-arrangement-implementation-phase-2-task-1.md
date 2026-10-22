# Workflow Auto-Arrangement — Phase 2 Task 1: `LayoutConfig` (maxattempts, browserpath, artifactdir, browsernosandbox)

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Make the arrangement attempt budget and the render-browser settings configurable through the config file and environment, with a default of five attempts and a visible warning when a value below one is clamped.

**Architecture:** `domain/config.Config` gains a `Layout LayoutConfig` block and a `LoadWarnings []string` slot. `adapter/config/loader.go` supplies koanf defaults, writes a live `layout:` block into a freshly created config file, and in `normalize` clamps `MaxAttempts < 1` to `1` and appends a warning. Before this task `normalize` had no way to report anything, so `LoadWarnings` is now how it does that. The composition root (P2T9) logs each warning and passes the budget into `layout.NewRequester`.

**Tech Stack:** Go 1.25.5, koanf v2 (`confmap`, `file`+`yaml`, `env` providers; mapstructure weak typing), testify `assert`/`require`.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` ("Bound refinement … configurable through a configuration file"); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-2.md` §6 "Configuration and composition root" and the `LAYOUT-CONFIG` row of "Data Provenance".

---

## Wired-and-fed

| Item | Producer (data source) | Composition-root wiring | Consumer |
|---|---|---|---|
| `cfg.Layout.MaxAttempts` | YAML `layout.maxattempts` / env `ARBITRON_LAYOUT_MAXATTEMPTS` / default `5`, read by `adapter/config/loader.go:Load` (this task) | **P2T9** `cmd/arbitron/layout_wiring.go:newLayoutStack` calls `layout.NewRequester(store, cfg.Layout.MaxAttempts, logger)` | P2T5 `Requester` → row `attempt_limit` → P2T8 `Job.MaxAttempts` → P1T9 `Coordinator.Run` |
| `cfg.LoadWarnings` | `adapter/config/loader.go:normalize` (this task) | **P2T9** `cmd/arbitron/main.go:initDomain` runs `for _, w := range cfg.LoadWarnings { logger.Warn(w) }` | operator log |
| `cfg.Layout.BrowserPath`, `ArtifactDir`, `BrowserNoSandbox` | YAML/env via `Load` (this task) | **P3T7** `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` builds `renderreview.BrowserConfig{BrowserPath, ArtifactDir (default <dir of database.path>/layout-artifacts), NoSandbox}` | P3T5 `renderreview.BrowserRenderer` |

This task is not the last one for these fields. P2T9 must include a composition-root test asserting that the stored row's `AttemptLimit` equals the configured `layout.maxattempts`. That test is the provenance probe that fails if the field stops being read.

---

### Task 2.1: `LayoutConfig` fields, defaults, YAML/env loading and clamp warning

**Files:**
- Modify: `domain/config/config.go:5-23` (add `Layout` + `LoadWarnings` to `Config`), and append `LayoutConfig` after `SamplesConfig` (`domain/config/config.go:214-222`)
- Modify: `adapter/config/loader.go:32-34` (defaults map), `adapter/config/loader.go:279-282` (default YAML: insert the `layout:` block between `tracing:` and `# samples:`), `adapter/config/loader.go:506-510` (add `DefaultLayoutMaxAttempts` after `DefaultSessionTTLSeconds`), `adapter/config/loader.go:512-547` (`normalize`)
- Modify: `adapter/config/loader_test.go:15-40` (`expectedDefaults` gains `Layout`), `adapter/config/loader_test.go:55-67` (`TestLoad_Defaults` asserts it), `adapter/config/loader_test.go:361-368` (`TestLoad_MissingFileCreatesDefault` compares `Layout`)
- Test: `adapter/config/loader_layout_test.go` (new)

**Interfaces**

Produces (contracts §2 + Addendum 1, exact):
```go
// package config (domain/config)
type Config struct {
	// ...existing fields...
	Layout LayoutConfig
	// LoadWarnings collects non-fatal normalisation warnings (e.g. clamped values) for the composition root to log.
	LoadWarnings []string
}
type LayoutConfig struct {
	MaxAttempts      int    // layout.maxattempts, default 5, values < 1 clamp to 1 with a warning
	BrowserPath      string // layout.browserpath, default "" (auto-detect Chrome/Chromium)
	ArtifactDir      string // layout.artifactdir, default "" (composition root uses <dir of database.path>/layout-artifacts)
	BrowserNoSandbox bool   // layout.browsernosandbox, default false
}

// package config (adapter/config), imported as adapterconfig
const DefaultLayoutMaxAttempts = 5
// Env: ARBITRON_LAYOUT_MAXATTEMPTS, ARBITRON_LAYOUT_BROWSERPATH, ARBITRON_LAYOUT_ARTIFACTDIR, ARBITRON_LAYOUT_BROWSERNOSANDBOX
// Warning text exactly: fmt.Sprintf("layout.maxattempts %d is below 1; using 1", v)
```

Consumes (existing):
```go
func Load(configPath string) (*domainconfig.Config, error) // adapter/config/loader.go:336
func writeConfigFile(t *testing.T, yaml string) string     // adapter/config/loader_test.go:1072 (package config_test)
```

How the env mapping works (existing loader, `loader.go:363-368`): the provider strips `ARBITRON_`, lowercases the rest and turns `_` into `.`. So `ARBITRON_LAYOUT_MAXATTEMPTS` becomes `layout.maxattempts`. Koanf's mapstructure decoder runs with `WeaklyTypedInput`, so `"7"` decodes to `int` 7 and `"true"` decodes to `bool`. No overlay code is needed. Note the trap: an env var that is **set but empty** decodes to `0`. The tests therefore unset the variables instead of setting them to `""`.

---

#### Cycle 1 — fields, defaults, YAML/env, default config file

- [x] **Step 1: Write the failing test**

Create `adapter/config/loader_layout_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
)

// layoutEnvKeys are every env override the layout block honours.
var layoutEnvKeys = []string{
	"ARBITRON_LAYOUT_MAXATTEMPTS",
	"ARBITRON_LAYOUT_BROWSERPATH",
	"ARBITRON_LAYOUT_ARTIFACTDIR",
	"ARBITRON_LAYOUT_BROWSERNOSANDBOX",
}

// isolateLayoutEnv removes any ambient layout override for the duration of the
// test. t.Setenv registers the restore-on-cleanup; os.Unsetenv then removes the
// variable entirely. Setting it to "" is NOT equivalent: koanf's env provider
// would load layout.maxattempts="" which weak-decodes to 0 and trips the clamp.
func isolateLayoutEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, k := range layoutEnvKeys {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestLoad_Layout_DefaultsWhenBlockAbsent(t *testing.T) {
	isolateLayoutEnv(t)
	p := writeConfigFile(t, "server:\n  port: 9999\n")

	cfg, err := adapterconfig.Load(p)
	require.NoError(t, err)

	assert.Equal(t, 5, adapterconfig.DefaultLayoutMaxAttempts, "the shipped default budget is five attempts")
	assert.Equal(t, adapterconfig.DefaultLayoutMaxAttempts, cfg.Layout.MaxAttempts, "no layout block → default budget")
	assert.Equal(t, "", cfg.Layout.BrowserPath, "empty browserpath means auto-detect")
	assert.Equal(t, "", cfg.Layout.ArtifactDir, "empty artifactdir means the composition-root default")
	assert.False(t, cfg.Layout.BrowserNoSandbox, "the browser sandbox stays ON by default")
	assert.Empty(t, cfg.LoadWarnings, "a default load produces no warnings")
}

func TestLoad_Layout_YAMLValues(t *testing.T) {
	isolateLayoutEnv(t)
	p := writeConfigFile(t, "layout:\n"+
		"  maxattempts: 3\n"+
		"  browserpath: \"/opt/chrome/chrome\"\n"+
		"  artifactdir: \"/var/lib/arbitron/layout-artifacts\"\n"+
		"  browsernosandbox: true\n")

	cfg, err := adapterconfig.Load(p)
	require.NoError(t, err)

	assert.Equal(t, 3, cfg.Layout.MaxAttempts)
	assert.Equal(t, "/opt/chrome/chrome", cfg.Layout.BrowserPath)
	assert.Equal(t, "/var/lib/arbitron/layout-artifacts", cfg.Layout.ArtifactDir)
	assert.True(t, cfg.Layout.BrowserNoSandbox)
	assert.Empty(t, cfg.LoadWarnings)
}

func TestLoad_Layout_EnvOverridesYAML(t *testing.T) {
	isolateLayoutEnv(t)
	p := writeConfigFile(t, "layout:\n"+
		"  maxattempts: 3\n"+
		"  browserpath: \"/yaml/chrome\"\n"+
		"  artifactdir: \"/yaml/artifacts\"\n"+
		"  browsernosandbox: false\n")
	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "7")
	t.Setenv("ARBITRON_LAYOUT_BROWSERPATH", "/env/chromium")
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", "/env/artifacts")
	t.Setenv("ARBITRON_LAYOUT_BROWSERNOSANDBOX", "true")

	cfg, err := adapterconfig.Load(p)
	require.NoError(t, err)

	assert.Equal(t, 7, cfg.Layout.MaxAttempts, "ARBITRON_LAYOUT_MAXATTEMPTS outranks YAML")
	assert.Equal(t, "/env/chromium", cfg.Layout.BrowserPath)
	assert.Equal(t, "/env/artifacts", cfg.Layout.ArtifactDir)
	assert.True(t, cfg.Layout.BrowserNoSandbox)
}

func TestLoad_Layout_EnvOnlyOverridesDefault(t *testing.T) {
	isolateLayoutEnv(t)
	p := writeConfigFile(t, "server:\n  port: 9999\n")
	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "9")

	cfg, err := adapterconfig.Load(p)
	require.NoError(t, err)
	assert.Equal(t, 9, cfg.Layout.MaxAttempts)
}

func TestLoad_Layout_FreshDefaultFileShipsLiveMaxAttempts(t *testing.T) {
	isolateLayoutEnv(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	cfg, err := adapterconfig.Load(configPath) // creates the default file
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.Layout.MaxAttempts)

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(raw)
	assert.Contains(t, content, "layout:\n  maxattempts: 5\n", "the budget is a LIVE key in a fresh config file")
	assert.Contains(t, content, "# browserpath:", "browserpath is documented but commented")
	assert.Contains(t, content, "# artifactdir:", "artifactdir is documented but commented")
	assert.Contains(t, content, "# browsernosandbox:", "browsernosandbox is documented but commented")

	// The live key is really read from the written file, not just echoing the
	// built-in default: edit it and reload.
	edited := strings.Replace(content, "  maxattempts: 5\n", "  maxattempts: 2\n", 1)
	require.NotEqual(t, content, edited, "the default file must contain the exact live line")
	require.NoError(t, os.WriteFile(configPath, []byte(edited), 0o600))

	cfg2, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, 2, cfg2.Layout.MaxAttempts, "editing the shipped key changes the budget")
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/853cf863-f0e7-4b99-b4e7-6330bfd19a2d/work
go test ./adapter/config/ -run 'TestLoad_Layout' -v
```

First output (missing-symbol compilation check only; NOT the behavioral RED):

```
# github.com/arbitron/arbitron/adapter/config_test [github.com/arbitron/arbitron/adapter/config.test]
adapter/config/loader_layout_test.go:...: undefined: adapterconfig.DefaultLayoutMaxAttempts
adapter/config/loader_layout_test.go:...: cfg.Layout undefined (type *config.Config has no field or method Layout)
adapter/config/loader_layout_test.go:...: cfg.LoadWarnings undefined (type *config.Config has no field or method LoadWarnings)
FAIL	github.com/arbitron/arbitron/adapter/config [build failed]
```

- [x] **Behavioral RED prerequisite:** Add only the domain fields/constant so tests compile, keeping loader defaults/YAML behavior unchanged. Rerun and observe the default budget/YAML/env/file assertions fail for their intended reasons before wiring the loader. The compilation check above alone does not satisfy strict TDD.

- [x] **Step 3: Minimal implementation**

3a. In `domain/config/config.go`, replace the `Config` struct (lines 5-23) with:

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
	Auth          AuthConfig
	Samples       SamplesConfig
	// Layout configures automatic workflow arrangement and its rendered review.
	Layout LayoutConfig
	// CapabilitiesPath is the path to an optional capability-ruleset override file
	// (profile-editing Phase 2). Empty or a non-existent path falls back to the
	// embedded default ruleset (LoadCapabilityRules treats a missing file as
	// "use the default", not an error). Default: ~/.arbitron/capabilities.yaml.
	CapabilitiesPath string
	// LoadWarnings collects non-fatal normalisation warnings (e.g. clamped values) for the composition root to log.
	LoadWarnings []string
}
```

3b. In `domain/config/config.go`, insert directly after the `SamplesConfig` struct (after line 222):

```go
// LayoutConfig configures automatic workflow arrangement (arrange → render →
// visual review → refine) for every committed authoring save.
type LayoutConfig struct {
	// MaxAttempts is the TOTAL arrangement-and-review attempts per saved
	// revision (layout.maxattempts). Default 5. Values below 1 are clamped to 1
	// by the loader, which records a LoadWarnings entry.
	MaxAttempts int
	// BrowserPath is the Chrome/Chromium executable used to measure, render and
	// review layouts (layout.browserpath). Empty auto-detects an installed browser.
	BrowserPath string
	// ArtifactDir stores rendered review artifacts (overview + tile captures)
	// (layout.artifactdir). Empty means the composition root uses
	// <dir of database.path>/layout-artifacts.
	ArtifactDir string
	// BrowserNoSandbox launches the review browser with --no-sandbox
	// (layout.browsernosandbox). SECURITY: default false; only for containers
	// that cannot provide the Chrome sandbox.
	BrowserNoSandbox bool
}
```

3c. In `adapter/config/loader.go` `defaults()`, replace lines 33-34:

```go
		"samples.seed":      true,
		"samples.sourcedir": "",
```

with:

```go
		"samples.seed":      true,
		"samples.sourcedir": "",
		// Automatic arrangement: total arrange+review attempts per saved revision
		// (clamped to >= 1 in normalize) and the review-browser settings. Empty
		// browserpath auto-detects Chrome/Chromium; empty artifactdir lets the
		// composition root default to <dir of database.path>/layout-artifacts.
		"layout.maxattempts":      DefaultLayoutMaxAttempts,
		"layout.browserpath":      "",
		"layout.artifactdir":      "",
		"layout.browsernosandbox": false,
```

3d. In `adapter/config/loader.go` `defaultYAMLContent()`, replace lines 279-282:

```
tracing:
  enabled: false
  exportdir: "./traces"
# samples:
```

with:

```
tracing:
  enabled: false
  exportdir: "./traces"
layout:
  maxattempts: 5
  #   Total arrangement-and-review attempts per saved workflow revision. Each
  #   attempt arranges the graph top-to-bottom, renders it and visually reviews
  #   readability; failing candidates are refined and reviewed again. When the
  #   budget is spent the best layout is kept and marked as needing attention.
  #   Values below 1 are clamped to 1 with a startup warning.
  #   Override via ARBITRON_LAYOUT_MAXATTEMPTS.
  # browserpath: ""
  #   Chrome/Chromium executable used to render and review layouts. Empty
  #   (default) auto-detects an installed Chrome/Chromium.
  #   Override via ARBITRON_LAYOUT_BROWSERPATH.
  # artifactdir: ""
  #   Directory for rendered review artifacts (overview and tile captures).
  #   Empty (default) uses <directory of database.path>/layout-artifacts.
  #   Override via ARBITRON_LAYOUT_ARTIFACTDIR.
  # browsernosandbox: false
  #   SECURITY: true launches the review browser with --no-sandbox. Only set it
  #   in a container that cannot provide the Chrome sandbox (e.g. root in Docker).
  #   Override via ARBITRON_LAYOUT_BROWSERNOSANDBOX.
# samples:
```

3e. In `adapter/config/loader.go`, insert after `const DefaultSessionTTLSeconds = 43200` (line 510):

```go

// DefaultLayoutMaxAttempts is the shipped total arrangement-and-review attempt
// budget per saved workflow revision (layout.maxattempts). The spec fixes the
// default at five; values below one are clamped to one in normalize.
const DefaultLayoutMaxAttempts = 5
```

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./adapter/config/ -run 'TestLoad_Layout' -v
```

Expected output:

```
=== RUN   TestLoad_Layout_DefaultsWhenBlockAbsent
--- PASS: TestLoad_Layout_DefaultsWhenBlockAbsent (0.00s)
=== RUN   TestLoad_Layout_YAMLValues
--- PASS: TestLoad_Layout_YAMLValues (0.00s)
=== RUN   TestLoad_Layout_EnvOverridesYAML
--- PASS: TestLoad_Layout_EnvOverridesYAML (0.00s)
=== RUN   TestLoad_Layout_EnvOnlyOverridesDefault
--- PASS: TestLoad_Layout_EnvOnlyOverridesDefault (0.00s)
=== RUN   TestLoad_Layout_FreshDefaultFileShipsLiveMaxAttempts
--- PASS: TestLoad_Layout_FreshDefaultFileShipsLiveMaxAttempts (0.00s)
PASS
ok  	github.com/arbitron/arbitron/adapter/config	...
```

- [x] **Step 5: Extend the existing default-config assertions**

`expectedDefaults()` is only compared field by field, so nothing breaks. It should still describe the new default. In `adapter/config/loader_test.go`, replace `expectedDefaults` (lines 15-40) with:

```go
// expectedDefaults returns a Config with all the expected default values.
func expectedDefaults() domainconfig.Config {
	return domainconfig.Config{
		Server: domainconfig.ServerConfig{
			Host: "127.0.0.1",
			Port: 8080,
		},
		Database: domainconfig.DatabaseConfig{
			Path: "./arbitron.db",
		},
		Workspace: domainconfig.WorkspaceConfig{
			Path: "./workspaces",
		},
		LLM: domainconfig.LLMConfig{
			Provider: "anthropic",
			APIKey:   "",
		},
		Logging: domainconfig.LoggingConfig{
			Level: "info",
			File:  "arbitron.log",
		},
		Tracing: domainconfig.TracingConfig{
			Enabled:   false,
			ExportDir: "./traces",
		},
		Layout: domainconfig.LayoutConfig{
			MaxAttempts: 5,
		},
	}
}
```

In `TestLoad_Defaults`, add these lines after `assert.Equal(t, 5, cfg.Queue.PerUserCap, "default PerUserCap must be 5")` (line 67):

```go
	assert.Equal(t, expected.Layout, cfg.Layout, "default layout block: 5 attempts, auto-detected browser, sandbox on")
	assert.Empty(t, cfg.LoadWarnings, "a default load produces no warnings")
```

In `TestLoad_MissingFileCreatesDefault`, add after `assert.Equal(t, cfg1.Queue, cfg2.Queue)` (line 368):

```go
	assert.Equal(t, cfg1.Layout, cfg2.Layout, "the written default file reproduces the default layout block")
```

Run:

```bash
go test ./adapter/config/ -run 'TestLoad_Defaults|TestLoad_MissingFileCreatesDefault' -v
```

Expected: `--- PASS: TestLoad_Defaults`, `--- PASS: TestLoad_MissingFileCreatesDefault`, `ok`.

- [x] **Step 6: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go adapter/config/loader_test.go adapter/config/loader_layout_test.go
git commit -m "feat(config): add layout block (maxattempts, browserpath, artifactdir, browsernosandbox)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

#### Cycle 2 — clamp values below one and surface `LoadWarnings`

- [x] **Step 1: Write the failing test**

Append to `adapter/config/loader_layout_test.go`:

```go
func TestLoad_Layout_MaxAttemptsBelowOneClampsWithWarning(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		env      string // "" = do not set
		wantWarn string
	}{
		{name: "yaml zero", yaml: "layout:\n  maxattempts: 0\n", wantWarn: "layout.maxattempts 0 is below 1; using 1"},
		{name: "yaml negative", yaml: "layout:\n  maxattempts: -2\n", wantWarn: "layout.maxattempts -2 is below 1; using 1"},
		{name: "env zero over valid yaml", yaml: "layout:\n  maxattempts: 4\n", env: "0", wantWarn: "layout.maxattempts 0 is below 1; using 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateLayoutEnv(t)
			if tc.env != "" {
				t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", tc.env)
			}
			cfg, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			require.NoError(t, err, "a too-small budget is clamped, never fatal")
			assert.Equal(t, 1, cfg.Layout.MaxAttempts, "values below one clamp to exactly one")
			assert.Equal(t, []string{tc.wantWarn}, cfg.LoadWarnings, "exactly one visible warning naming the bad value")
		})
	}
}

func TestLoad_Layout_MaxAttemptsOneIsAcceptedSilently(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "layout:\n  maxattempts: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Layout.MaxAttempts)
	assert.Empty(t, cfg.LoadWarnings, "the boundary value 1 is valid and not warned about")
}

func TestLoad_Layout_LoadWarningsCannotBeInjectedFromConfig(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "loadwarnings: [\"injected\"]\nlayout:\n  maxattempts: 2\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.LoadWarnings, "LoadWarnings is produced only by normalisation, never read from the file")
}
```

- [x] **Step 2: Run it, confirm it fails**

```bash
go test ./adapter/config/ -run 'TestLoad_Layout_MaxAttempts|TestLoad_Layout_LoadWarnings' -v
```

Expected output (runtime failures; `MaxAttempts` is passed through unclamped):

```
--- FAIL: TestLoad_Layout_MaxAttemptsBelowOneClampsWithWarning/yaml_zero
    loader_layout_test.go:...: Error: Not equal: expected: 1 actual  : 0
        Messages: values below one clamp to exactly one
    loader_layout_test.go:...: Error: Not equal: expected: []string{"layout.maxattempts 0 is below 1; using 1"} actual  : []string(nil)
--- FAIL: TestLoad_Layout_MaxAttemptsBelowOneClampsWithWarning/yaml_negative
--- FAIL: TestLoad_Layout_MaxAttemptsBelowOneClampsWithWarning/env_zero_over_valid_yaml
--- PASS: TestLoad_Layout_MaxAttemptsOneIsAcceptedSilently
--- FAIL: TestLoad_Layout_LoadWarningsCannotBeInjectedFromConfig
    loader_layout_test.go:...: Error: Should be empty, but was [injected]
FAIL
```

- [x] **Step 3: Minimal implementation**

In `adapter/config/loader.go`, replace the `normalize` doc comment and function (lines 512-547) with:

```go
// normalize applies safety floors and value normalization to a loaded config in
// place. It clamps a non-positive llm.timeoutseconds up to DefaultTimeoutSeconds
// so a misconfigured 0 cannot disable every deadline, trims trailing slashes
// from the public base URL so callers can concatenate "/instances/<id>"
// deterministically, and clamps layout.maxattempts to at least 1.
//
// Non-fatal adjustments that an operator should see are appended to
// cfg.LoadWarnings; the composition root logs each one at Warn. LoadWarnings is
// reset first so it can only ever carry normalisation output, never a value
// smuggled in through the YAML file or the environment.
func normalize(cfg *domainconfig.Config) {
	cfg.LoadWarnings = nil
	if cfg.LLM.TimeoutSeconds <= 0 {
		cfg.LLM.TimeoutSeconds = DefaultTimeoutSeconds
	}
	// Org-readiness Phase 2: floor an empty (or whitespace-only) default agent
	// backend to claude-code. This is the SINGLE deployment-policy seam for the
	// backend fallback so a blank value cannot silently leave the deployment
	// without a default backend; the executor and the factory both read it.
	if strings.TrimSpace(cfg.LLM.DefaultAgentBackend) == "" {
		cfg.LLM.DefaultAgentBackend = DefaultAgentBackend
	}
	cfg.Server.PublicBaseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	cfg.Auth.Mode = strings.ToLower(strings.TrimSpace(cfg.Auth.Mode))
	// Trim the edge teamdomain so a YAML value with surrounding whitespace
	// reaches edge.New as a clean URL rather than causing a confusing parse error.
	cfg.Auth.Edge.TeamDomain = strings.TrimSpace(cfg.Auth.Edge.TeamDomain)
	// Trim GroupClaim so a padded YAML value (" groups ") does not cause the OIDC
	// provider or edge verifier to look for the wrong claim name. The edge verifier
	// also trims at construction; this makes both paths consistent and the config
	// struct self-consistent (whitespace-only GroupClaim → "" → no group extraction).
	cfg.Auth.RBAC.GroupClaim = strings.TrimSpace(cfg.Auth.RBAC.GroupClaim)
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = domainconfig.AuthModeLocal
	}
	if cfg.Auth.Session.TTLSeconds <= 0 {
		cfg.Auth.Session.TTLSeconds = DefaultSessionTTLSeconds
	}
	if cfg.Auth.Session.CookieName == "" {
		cfg.Auth.Session.CookieName = "arbitron_session"
	}
	// Automatic arrangement needs at least one attempt per saved revision. A
	// zero/negative budget would make every save "exhausted" before arranging,
	// so clamp it to one and make the misconfiguration visible.
	if cfg.Layout.MaxAttempts < 1 {
		cfg.LoadWarnings = append(cfg.LoadWarnings,
			fmt.Sprintf("layout.maxattempts %d is below 1; using 1", cfg.Layout.MaxAttempts))
		cfg.Layout.MaxAttempts = 1
	}
	cfg.Layout.BrowserPath = strings.TrimSpace(cfg.Layout.BrowserPath)
	cfg.Layout.ArtifactDir = strings.TrimSpace(cfg.Layout.ArtifactDir)
}
```

(`fmt` is already imported by `loader.go`.)

- [x] **Step 4: Run it, confirm it passes**

```bash
go test ./adapter/config/ -run 'TestLoad_Layout' -v
```

Expected: every `TestLoad_Layout_*` test and subtest reports `--- PASS`, then `ok  	github.com/arbitron/arbitron/adapter/config`.

- [x] **Step 5: Commit**

```bash
git add adapter/config/loader.go adapter/config/loader_layout_test.go
git commit -m "feat(config): clamp layout.maxattempts below 1 and report LoadWarnings" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

#### Final regression

- [x] **Step 1: Full-package regression + build**

> Current plan correction (§6.18): the default-profile assumption has been replaced in source by `adapter/config/sample_validation_test.go:TestSampleWorkflows_ValidateWithTheirDocumentedProfiles`, with an undocumented-alias negative test. Re-run that actual suite and the prescribed package gate; do not change default profiles or sample behavior, and do not keep skipping this gate for the obsolete test name. Production budget/browser wiring is present; historical results below are not new verification.

```bash
go build ./...
go vet ./adapter/config/ ./domain/config/
go test ./domain/workflow/... ./domain/samples/... ./adapter/config/... ./domain/engine/...
```

Expected: `go build` and `go vet` print nothing. Each test package reports `ok` (for example `ok  	github.com/arbitron/arbitron/adapter/config	…`). No `FAIL` lines.

- [x] **Step 2: Commit (only if Step 1 required fixes)**

No regression fixes within this task's slice were needed. The historical sample-profile failure is resolved by the documented-profile suite; the final gate now passes (see completion evidence below).

```bash
git add -A adapter/config domain/config
git commit -m "test(config): keep layout config regression green" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Config rejection and boundary regression (strict RED→GREEN)

Before final package verification add table cases for malformed YAML, nonnumeric maxattempts,
invalid boolean browsernosandbox, valid budget 1, negative env override, surrounding spaces
in browser/artifact paths and repeated loads after a warned load. Invalid input returns a
configuration decode error rather than silently installing five; a fresh valid load has no
stale LoadWarnings. Observe behavioral RED for any new normalization, implement minimum,
GREEN/refactor. Existing decode-error behavior may start green: mutation-probe the decode
error propagation, observe test failure, restore. Root P2T9 proves warnings are actually
logged and effective budget reaches SQLite; P3T7 proves browserpath/artifactdir/noSandbox
reach the real browser process and artifact sink.

## Execution evidence

- Selected P2T1 after checking the still-missing root/browser/store dependencies recorded under P1T4–P1T9; did not repeat those blocked gates or implement another task.
- Cycle 1: observed missing-symbol failures, added only declarations/constant, then observed behavioral failures for absent-block defaults and generated YAML. Existing koanf YAML/env decoding already passed. Implemented the exact four fields/defaults and live generated YAML; reload after editing the generated key proves it is read. Updated existing default assertions. Commit: 87b52f9.
- Cycle 2: observed behavioral RED for zero/negative budgets, warning injection, path trimming and negative env override/repeated loads. Implemented minimum normalization, exact clamp warnings, warning reset and path trimming. Invalid YAML, nonnumeric budget and invalid boolean characterization cases already passed; bypassing parser/decode error returns made each fail, then guards were restored.
- Self-review in order: (1) checked exact contracts §2/addendum 1 and P2T1 ownership: no extra flags, custom env overlays or unrelated profile changes. The remaining production gate is explicitly blocked, not waived. (2) Cleared the decoded Layout block temporarily; defaults/YAML/env sink tests failed. Changed the clamp boundary, removed warning reset and path trimming; the corresponding boundary/injection/path assertions failed. Restored every mutation. These use actual Load/files/env and assert effective Config/written bytes, not a fake or intermediate koanf map; they are not claimed as SQLite/browser/root acceptance. (3) Read neighboring config types/tests/normalization, kept koanf's existing precedence/decode errors, isolated env without empty-string overrides, used the existing fmt import and named default, and ran gofmt/diff checks.
- Final local GREEN: go test ./adapter/config -run TestLoad -count=1; go test -race ./adapter/config -run TestLoad -count=1; go build ./...; go vet ./adapter/config ./domain/config; workflow/samples/engine tests; gofmt -l (empty); git diff --check. Full prescribed regression has only the baseline sample-profile failure documented above. No production stub or mutation remains.

## Blocked-pass selection audit (after d0b02e9)

- Listed all 32 task plans in numeric phase/task order. P2T1 is the first with an unchecked step. Corrected its stale missing-wiring note from current production source; the unrelated sample/default-profile regression remains recorded, not re-failed.
- Applied the skip rule across all later unchecked tasks. All 14 unfinished task files already record blockers: rename intent/settlement and seed evidence/registry protocols; shared-retry lock-set expansion; contradictory zero-option validation; missing stdio identity and raw-file settlement seams; complex-sample readability acceptance; Docker/two-browser availability; client-font retry policy; and previously failed or timed-out whole-suite gates. No independent unchecked step without a recorded blocker was found. No subsequent task was implemented.
- Inspected the current rename BeginRelocation→Stamp→Settle path, Committed journal removal/EnsureStamped evidence requirement, shared lock-set guard, context-free raw FileReader, stdio identity deferral, server/client zero-option rules and panel Retry eligibility. Those contract/producer blockers remain in source. Existing environment/release failures were not rerun and are not claimed green.
- Ordered self-review: (1) spec—documentation-only blocker audit, no scope expansion or unchecked gate waived; (2) anti-green-theater—no new test or production completion claim, and wiring existence is distinguished from prior sink evidence and full regression; (3) quality—updated the existing failing-step note rather than adding a contradictory duplicate. Preserved unrelated untracked docs/visuals. Verification for this documentation-only pass: git diff --check; no tests rerun.

## Final regression completion (2026-10-18)

- Revisited the first unchecked task under current contracts §§6.18–6.19. The former sample-profile blocker no longer applies: the actual documented-profile validation and undocumented-profile negative test both run in the prescribed package suite. No production or test behavior changed; the earlier RED→GREEN evidence remains above.
- Ordered self-review: (1) spec—only P2T1's remaining regression gate is closed, with no profile/sample changes or expanded scope; (2) anti-green-theater—reviewed actual Load/file/env assertions and production `initDomain` → `newLayoutStack` → Requester/browser config wiring, then ran `TestLayoutE2E_ConfigBudgetAndWarnings` uncached: YAML budgets 3/0/-2 reach terminal SQLite rows as 3/1/1 and exact clamp warnings reach the real log file; (3) quality—reviewed neighboring config types/normalization and root tests, preserved koanf precedence and historical execution evidence, and checked the documentation diff. No code fix was required.
- After review, GREEN: `go test ./domain/workflow/... ./domain/samples/... ./adapter/config/... ./domain/engine/... -count=1`; `go test ./cmd/arbitron -run '^TestLayoutE2E_ConfigBudgetAndWarnings$' -count=1`; `go test -race ./adapter/config -run '^TestLoad_Layout' -count=1`; `go build ./...`; `go vet ./adapter/config/ ./domain/config/`; `git diff --check`.
- This closes P2T1 only. Browser/release and other unfinished tasks retain their own acceptance gates; no later task was started.
