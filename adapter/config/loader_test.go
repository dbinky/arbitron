package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	}
}

func TestLoad_Defaults(t *testing.T) {
	// When no config file exists and no env vars are set, Load should return
	// all default values and create a default config file.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	expected := expectedDefaults()
	assert.Equal(t, expected.Server.Host, cfg.Server.Host)
	assert.Equal(t, expected.Server.Port, cfg.Server.Port)
	assert.Equal(t, expected.Database.Path, cfg.Database.Path)
	assert.Equal(t, expected.Workspace.Path, cfg.Workspace.Path)
	assert.Equal(t, expected.LLM.Provider, cfg.LLM.Provider)
	assert.Equal(t, expected.LLM.APIKey, cfg.LLM.APIKey)
	assert.Equal(t, expected.Logging.Level, cfg.Logging.Level)
	assert.Equal(t, expected.Logging.File, cfg.Logging.File)
	assert.Equal(t, expected.Tracing.Enabled, cfg.Tracing.Enabled)
	assert.Equal(t, expected.Tracing.ExportDir, cfg.Tracing.ExportDir)
	assert.Equal(t, 20, cfg.Queue.GlobalCap, "default GlobalCap must be 20")
	assert.Equal(t, 5, cfg.Queue.PerUserCap, "default PerUserCap must be 5")
}

// SECURITY: the shipped default is CLOSED (false). Allow-all on a
// credential-bearing, tool-using subprocess fed model-influenced prompts is a
// lethal-trifecta posture, so it must be an explicit operator opt-in — never the
// out-of-the-box behavior. (PR #23 security HIGH.)
func TestLoad_AgentAllowAllToolsDefaultsFalse(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.LLM.AgentAllowAllTools, "omitted llm.agentallowalltools must default to false (closed; allow-all is an explicit opt-in)")
}

// An explicit true in YAML opts in to the allow-all agent posture.
func TestLoad_AgentAllowAllToolsOverrideTrue(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := "llm:\n  agentallowalltools: true\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.LLM.AgentAllowAllTools, "explicit true must opt in to the allow-all agent posture")
}

// Edge: ARBITRON_LLM_AGENTALLOWALLTOOLS=false lets an operator tighten the
// open-by-default posture at deployment time without touching the config file
// (the primary zero-touch path for containerized / 12-factor deployments).
// The env provider maps ARBITRON_LLM_AGENTALLOWALLTOOLS → llm.agentallowalltools.
func TestLoad_AgentAllowAllTools_EnvVarDisables(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_LLM_AGENTALLOWALLTOOLS", "false")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.LLM.AgentAllowAllTools,
		"ARBITRON_LLM_AGENTALLOWALLTOOLS=false must disable the open-by-default agent posture")
}

// Success: env var override of agentallowalltools takes precedence over YAML true.
// An operator can harden a deployment without modifying the checked-in config file.
func TestLoad_AgentAllowAllTools_EnvVarWinsOverYAMLTrue(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_LLM_AGENTALLOWALLTOOLS", "false")

	// YAML says true, env says false — env must win (highest precedence).
	yamlContent := "llm:\n  agentallowalltools: true\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.LLM.AgentAllowAllTools,
		"env ARBITRON_LLM_AGENTALLOWALLTOOLS=false must win over YAML agentallowalltools: true")
}

func TestLoad_YAMLOverride(t *testing.T) {
	// A YAML config file should override default values.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
server:
  host: "127.0.0.1"
  port: 9090
database:
  path: "/data/arbitron.db"
workspace:
  path: "/data/workspaces"
llm:
  provider: "openai"
  apikey: "yaml-api-key"
logging:
  level: "debug"
  file: "/var/log/arbitron.log"
tracing:
  enabled: true
  exportdir: "/data/traces"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "/data/arbitron.db", cfg.Database.Path)
	assert.Equal(t, "/data/workspaces", cfg.Workspace.Path)
	assert.Equal(t, "openai", cfg.LLM.Provider)
	assert.Equal(t, "yaml-api-key", cfg.LLM.APIKey)
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "/var/log/arbitron.log", cfg.Logging.File)
	assert.Equal(t, true, cfg.Tracing.Enabled)
	assert.Equal(t, "/data/traces", cfg.Tracing.ExportDir)
}

func TestLoad_EnvVarOverride(t *testing.T) {
	// ARBITRON_ prefixed env vars should override default values.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	t.Setenv("ARBITRON_SERVER_HOST", "env-host")
	t.Setenv("ARBITRON_SERVER_PORT", "3000")
	t.Setenv("ARBITRON_DATABASE_PATH", "/tmp/env-arbitron.db")
	t.Setenv("ARBITRON_LOGGING_LEVEL", "warn")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "env-host", cfg.Server.Host)
	assert.Equal(t, 3000, cfg.Server.Port)
	assert.Equal(t, "/tmp/env-arbitron.db", cfg.Database.Path)
	assert.Equal(t, "warn", cfg.Logging.Level)

	// Non-overridden fields should remain at defaults.
	defaults := expectedDefaults()
	assert.Equal(t, defaults.Workspace.Path, cfg.Workspace.Path)
	assert.Equal(t, defaults.LLM.Provider, cfg.LLM.Provider)
}

func TestLoad_EnvVarOverridesYAML(t *testing.T) {
	// Env vars should take precedence over values set in YAML.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
server:
  host: "yaml-host"
  port: 9090
logging:
  level: "debug"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)

	t.Setenv("ARBITRON_SERVER_HOST", "env-host")
	t.Setenv("ARBITRON_LOGGING_LEVEL", "error")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Env should win over YAML.
	assert.Equal(t, "env-host", cfg.Server.Host)
	assert.Equal(t, "error", cfg.Logging.Level)

	// YAML should still win over defaults for non-env-overridden fields.
	assert.Equal(t, 9090, cfg.Server.Port)
}

func TestLoad_AnthropicAPIKeyEnvVar(t *testing.T) {
	// The standard ANTHROPIC_API_KEY env var should map to LLM.APIKey.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key-123")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "sk-ant-test-key-123", cfg.LLM.APIKey)
}

func TestLoad_QueueEnvVarOverride(t *testing.T) {
	// ARBITRON_QUEUE_GLOBALCAP and ARBITRON_QUEUE_PERUSERCAP should override defaults.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_QUEUE_GLOBALCAP", "50")
	t.Setenv("ARBITRON_QUEUE_PERUSERCAP", "10")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 50, cfg.Queue.GlobalCap)
	assert.Equal(t, 10, cfg.Queue.PerUserCap)
}

func TestLoad_MalformedYAML(t *testing.T) {
	// A malformed YAML file should produce an error.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	malformedYAML := `
server:
  host: "ok"
  port: [invalid yaml
    this is broken
`
	err := os.WriteFile(configPath, []byte(malformedYAML), 0644)
	require.NoError(t, err)

	cfg, err := adapterconfig.Load(configPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoad_PartialYAML_MissingFieldsGetDefaults(t *testing.T) {
	// A YAML file with only some fields should get defaults for the rest.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ANTHROPIC_API_KEY", "")

	partialYAML := `
server:
  port: 9090
`
	require.NoError(t, os.WriteFile(configPath, []byte(partialYAML), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Overridden field.
	assert.Equal(t, 9090, cfg.Server.Port)
	// Non-overridden fields get defaults.
	assert.Equal(t, "127.0.0.1", cfg.Server.Host)
	assert.Equal(t, "./arbitron.db", cfg.Database.Path)
	assert.Equal(t, "info", cfg.Logging.Level)
}

func TestLoad_AnthropicAPIKeyHasHighestPrecedence(t *testing.T) {
	// ANTHROPIC_API_KEY is loaded after ARBITRON_LLM_APIKEY, so it wins.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ARBITRON_LLM_APIKEY", "arbitron-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-wins")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "anthropic-wins", cfg.LLM.APIKey)
}

func TestLoad_MissingFileCreatesDefault(t *testing.T) {
	// When the config file does not exist, Load should create it with default
	// content, and subsequent loads of that file should produce the same config.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Isolate from ambient environment.
	t.Setenv("ANTHROPIC_API_KEY", "")

	// File should not exist yet.
	_, err := os.Stat(configPath)
	require.True(t, os.IsNotExist(err))

	cfg1, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg1)

	// File should now exist.
	_, err = os.Stat(configPath)
	require.NoError(t, err)

	// Loading again from the created file should produce the same defaults.
	cfg2, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg2)

	assert.Equal(t, cfg1.Server, cfg2.Server)
	assert.Equal(t, cfg1.Database, cfg2.Database)
	assert.Equal(t, cfg1.Workspace, cfg2.Workspace)
	assert.Equal(t, cfg1.LLM, cfg2.LLM)
	assert.Equal(t, cfg1.Logging, cfg2.Logging)
	assert.Equal(t, cfg1.Tracing, cfg2.Tracing)
	assert.Equal(t, cfg1.Queue, cfg2.Queue)
}

func TestLoad_ScriptEnvPassthrough_Default(t *testing.T) {
	dir := t.TempDir()
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Script.EnvPassthrough)
}

func TestLoad_ScriptEnvPassthrough_FromEnv(t *testing.T) {
	t.Setenv("ARBITRON_SCRIPT_ENVPASSTHROUGH", "LLM_API_KEY, LLM_ROUTER_URL ,,OPENAI_API_KEY")
	dir := t.TempDir()
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"LLM_API_KEY", "LLM_ROUTER_URL", "OPENAI_API_KEY"}, cfg.Script.EnvPassthrough)
}

func TestLoad_QueueYAMLOverride(t *testing.T) {
	// A YAML config file should override queue defaults.
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := `
queue:
  globalcap: 100
  perusercap: 25
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 100, cfg.Queue.GlobalCap, "YAML globalcap must override default 20")
	assert.Equal(t, 25, cfg.Queue.PerUserCap, "YAML perusercap must override default 5")
}

func TestLoadClaudeCLIDefaults(t *testing.T) {
	t.Run("claude-cli config fields have safe defaults", func(t *testing.T) {
		dir := t.TempDir()
		cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		// SECURITY: default timeout is 1800 (30 min), NOT 0 (unbounded).
		// PR #8 security thread 3291944120.
		assert.Equal(t, 1800, cfg.LLM.TimeoutSeconds)
		assert.Equal(t, "", cfg.LLM.BinaryPath) // default: auto-resolve
		assert.Equal(t, "", cfg.LLM.Model)      // default: provider default
		// SECURITY: skip-permissions defaults to false. Thread 3291940088.
		assert.False(t, cfg.LLM.SkipPermissions)
		assert.Empty(t, cfg.LLM.AllowedTools)
		// SECURITY: 256 MB per-request stdout cap. Thread 3291941918.
		assert.Equal(t, int64(256*1024*1024), cfg.LLM.MaxResponseBytes)
	})

	t.Run("YAML can override claude-cli policy fields", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
llm:
  skippermissions: true
  allowedtools:
    - "Read"
    - "Bash(git status:*)"
  maxresponsebytes: 1048576
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

		cfg, err := adapterconfig.Load(configPath)
		require.NoError(t, err)
		assert.True(t, cfg.LLM.SkipPermissions)
		assert.Equal(t, []string{"Read", "Bash(git status:*)"}, cfg.LLM.AllowedTools)
		assert.Equal(t, int64(1048576), cfg.LLM.MaxResponseBytes)
	})
}

// Edge: ARBITRON_SCRIPT_ENVPASSTHROUGH with only commas and whitespace yields
// an empty slice (all entries are empty after trimming), not a slice containing
// empty strings.
func TestLoad_ScriptEnvPassthrough_AllEmptyEntriesDropped(t *testing.T) {
	t.Setenv("ARBITRON_SCRIPT_ENVPASSTHROUGH", " ,,  , ")
	dir := t.TempDir()
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Script.EnvPassthrough,
		"whitespace-only/comma-only passthrough must produce an empty slice, not empty strings")
}

// SECURITY: a non-positive llm.timeoutseconds is floored at config load to the
// sane default so a misconfigured 0 cannot silently disable every per-call
// deadline (a hang-forever DoS on the credential-bearing HTTP adapters and the
// CLI subprocesses).
func TestLoad_TimeoutSecondsFloor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaml    string
		wantSec int
	}{
		{
			name:    "zero is floored to the default",
			yaml:    "llm:\n  timeoutseconds: 0\n",
			wantSec: adapterconfig.DefaultTimeoutSeconds,
		},
		{
			name:    "negative is floored to the default",
			yaml:    "llm:\n  timeoutseconds: -5\n",
			wantSec: adapterconfig.DefaultTimeoutSeconds,
		},
		{
			name:    "a positive value is preserved",
			yaml:    "llm:\n  timeoutseconds: 90\n",
			wantSec: 90,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			t.Setenv("ANTHROPIC_API_KEY", "")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.yaml), 0o600))

			cfg, err := adapterconfig.Load(configPath)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSec, cfg.LLM.TimeoutSeconds)
			assert.Positive(t, cfg.LLM.TimeoutSeconds,
				"the timeout must always be positive so deadlines cannot be disabled")
		})
	}
}

// seam (config → Factory Deps → all adapters; config → buildValidateDeps):
// both initDomain (serve) and buildValidateDeps (connection validate) derive
// their adapter/probe timeout from cfg.LLM.TimeoutSeconds via
//
//	time.Duration(cfg.LLM.TimeoutSeconds) * time.Second
//
// If an operator sets ARBITRON_LLM_TIMEOUTSECONDS=0 in a containerized
// environment to make things "fast", both paths must still floor to
// DefaultTimeoutSeconds — the YAML floor path is tested by TestLoad_TimeoutSecondsFloor;
// this test closes the env-var path.
func TestLoad_TimeoutSecondsFloor_EnvVarZeroFloored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_LLM_TIMEOUTSECONDS", "0")
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, adapterconfig.DefaultTimeoutSeconds, cfg.LLM.TimeoutSeconds,
		"ARBITRON_LLM_TIMEOUTSECONDS=0 must be floored at load — a zero timeout disables all deadlines for both serve and connection validate")
	assert.Positive(t, cfg.LLM.TimeoutSeconds)
}

// seam (config → buildValidateDeps): a positive ARBITRON_LLM_TIMEOUTSECONDS
// overrides the default and is preserved (not floored). Both serve and
// connection validate should honour a deliberate operator timeout value.
func TestLoad_TimeoutSecondsFloor_EnvVarPositivePreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_LLM_TIMEOUTSECONDS", "120")
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 120, cfg.LLM.TimeoutSeconds,
		"a positive ARBITRON_LLM_TIMEOUTSECONDS must be preserved so operators can tune adapter timeouts")
}

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

// TestLoad_DefaultProfileRegistry_IncludesCommittedWorkflowAliases verifies the
// in-repo default profile registry satisfies the contracts of the workflows
// shipped in this repo (PR #18 Must Fix item 4, comment 4614860006). A fresh
// install with no deployment config.yaml must validate workspaces/pr-review and
// workspaces/feature-pipeline honestly — those reference reviewer/coder/planner
// (agent), interviewer (agent), and assessor (chat). Without these defaults a
// clean clone passes workflow_validate and then dies mid-run with "unknown
// profile alias".
func TestLoad_DefaultProfileRegistry_IncludesCommittedWorkflowAliases(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	byAlias := make(map[string]string, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		byAlias[p.Alias] = p.Kind
	}

	wantKinds := map[string]string{
		"assessor":    "chat",
		"interviewer": "agent",
		"reviewer":    "agent",
		"coder":       "agent",
		"planner":     "agent",
	}
	for alias, kind := range wantKinds {
		got, ok := byAlias[alias]
		require.True(t, ok, "default profile registry must include alias %q", alias)
		assert.Equal(t, kind, got, "alias %q must be kind %q", alias, kind)
	}
}

// TestLoad_DefaultProfileRegistry_AgentToolScopes verifies the in-repo default
// registry ships an explicit minimal AllowedTools per agent profile (PR #18
// item 1b). Reviewers are read + comment only (no Write/Edit); coder gets full
// dev; interviewer/planner get write-capable scopes for artifact authoring.
// Chat profiles (assessor) stay tool-suppressed (no AllowedTools).
func TestLoad_DefaultProfileRegistry_AgentToolScopes(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)

	byAlias := map[string][]string{}
	for _, p := range cfg.Profiles {
		byAlias[p.Alias] = p.AllowedTools
	}

	assert.Equal(t, []string{"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"}, byAlias["reviewer"],
		"reviewer is read + comment only; no Write/Edit, no bare Bash")
	assert.Equal(t, []string{"Read", "Write", "Edit", "Grep", "Glob", "Bash"}, byAlias["coder"])
	assert.Equal(t, []string{"Read", "Write", "Grep", "Glob", "Bash"}, byAlias["interviewer"])
	assert.Equal(t, []string{"Read", "Write", "Grep", "Glob", "Bash"}, byAlias["planner"])

	// Chat-kind assessor stays tool-suppressed: no per-profile allowlist.
	assert.Empty(t, byAlias["assessor"], "chat profiles must not carry a tool allowlist")
}

// ---- Phase 6 Task 1: AuthConfig loading & validation ----

func TestLoad_AuthDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "local", cfg.Auth.Mode)
	assert.Equal(t, "arbitron_session", cfg.Auth.Session.CookieName)
	assert.Equal(t, 43200, cfg.Auth.Session.TTLSeconds)
	assert.True(t, cfg.Auth.Session.CookieSecure)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Auth.OIDC.Scopes)
	assert.Empty(t, cfg.Auth.BootstrapAdmins)
}

func TestLoad_AuthOrgMode_Valid(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	yamlContent := `
auth:
  mode: "org"
  oidc:
    issuerurl: "https://login.example.com"
    clientid: "client-abc"
    clientsecret: "shhh"
    redirecturl: "https://app.example.com/auth/callback"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "org", cfg.Auth.Mode)
	assert.Equal(t, "https://login.example.com", cfg.Auth.OIDC.IssuerURL)
	assert.Equal(t, "client-abc", cfg.Auth.OIDC.ClientID)
	assert.Equal(t, "shhh", cfg.Auth.OIDC.ClientSecret)
	assert.Equal(t, "https://app.example.com/auth/callback", cfg.Auth.OIDC.RedirectURL)
}

func TestLoad_AuthMode_Normalized(t *testing.T) {
	t.Run("uppercase normalizes to org", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: "ORG"
  oidc:
    issuerurl: "https://i"
    clientid: "c"
    clientsecret: "s"
    redirecturl: "https://r/auth/callback"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "org", cfg.Auth.Mode)
	})

	t.Run("padded normalizes to org", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: " org "
  oidc:
    issuerurl: "https://i"
    clientid: "c"
    clientsecret: "s"
    redirecturl: "https://r/auth/callback"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.NoError(t, err)
		assert.Equal(t, "org", cfg.Auth.Mode)
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("auth:\n  mode: \"cloud\"\n"), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "invalid auth.mode")
	})
}

func TestLoad_AuthOrgMode_MissingOIDC(t *testing.T) {
	t.Run("missing clientsecret", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: "org"
  oidc:
    issuerurl: "https://i"
    clientid: "c"
    redirecturl: "https://r/auth/callback"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.oidc.clientsecret")
	})

	t.Run("missing issuerurl", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: "org"
  oidc:
    clientid: "c"
    clientsecret: "s"
    redirecturl: "https://r/auth/callback"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.oidc.issuerurl")
	})
}

func TestLoad_AuthSessionTTLFloor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yaml    string
		wantTTL int
	}{
		{"zero floors to default", "auth:\n  session:\n    ttlseconds: 0\n", adapterconfig.DefaultSessionTTLSeconds},
		{"negative floors to default", "auth:\n  session:\n    ttlseconds: -1\n", adapterconfig.DefaultSessionTTLSeconds},
		{"positive preserved", "auth:\n  session:\n    ttlseconds: 1\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			t.Setenv("ANTHROPIC_API_KEY", "")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.yaml), 0o600))
			cfg, err := adapterconfig.Load(configPath)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTTL, cfg.Auth.Session.TTLSeconds)
		})
	}
}

func TestLoad_AuthBootstrapAdmins_FromEnv(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_BOOTSTRAPADMINS", "a@x.com, b@x.com,, ")
	dir := t.TempDir()
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"a@x.com", "b@x.com"}, cfg.Auth.BootstrapAdmins)
}

func TestLoad_AuthOIDCScopes_FromEnv(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_OIDC_SCOPES", "openid, groups")
	dir := t.TempDir()
	cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"openid", "groups"}, cfg.Auth.OIDC.Scopes)
}

// TestLoad_AuthOrgMode_MissingOIDC_AllFields verifies that each of the four
// required OIDC fields is individually validated and that omitting all four at
// once produces an error that names all four.
func TestLoad_AuthOrgMode_MissingOIDC_AllFields(t *testing.T) {
	// Each sub-test omits exactly one required field; the error must name it.
	cases := []struct {
		name        string
		yaml        string
		wantMissing string
	}{
		{
			"missing clientid",
			"auth:\n  mode: \"org\"\n  oidc:\n    issuerurl: \"https://i\"\n    clientsecret: \"s\"\n    redirecturl: \"https://r/cb\"\n",
			"auth.oidc.clientid",
		},
		{
			"missing redirecturl",
			"auth:\n  mode: \"org\"\n  oidc:\n    issuerurl: \"https://i\"\n    clientid: \"c\"\n    clientsecret: \"s\"\n",
			"auth.oidc.redirecturl",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.yaml), 0o600))
			cfg, err := adapterconfig.Load(configPath)
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tc.wantMissing)
		})
	}

	t.Run("all four missing names all in error", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath,
			[]byte("auth:\n  mode: \"org\"\n"), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.oidc.issuerurl")
		assert.Contains(t, err.Error(), "auth.oidc.clientid")
		assert.Contains(t, err.Error(), "auth.oidc.clientsecret")
		assert.Contains(t, err.Error(), "auth.oidc.redirecturl")
	})
}

// TestLoad_AuthMode_FromEnv verifies that ARBITRON_AUTH_MODE switches the mode
// via the environment-variable overlay (the env provider maps _ to . so
// ARBITRON_AUTH_MODE → auth.mode).
func TestLoad_AuthMode_FromEnv(t *testing.T) {
	t.Run("env switches to org with complete OIDC also from env", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ARBITRON_AUTH_MODE", "org")
		t.Setenv("ARBITRON_AUTH_OIDC_ISSUERURL", "https://idp.example.com")
		t.Setenv("ARBITRON_AUTH_OIDC_CLIENTID", "env-client")
		t.Setenv("ARBITRON_AUTH_OIDC_CLIENTSECRET", "env-secret")
		t.Setenv("ARBITRON_AUTH_OIDC_REDIRECTURL", "https://app.example.com/auth/callback")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
		require.NoError(t, err)
		assert.Equal(t, "org", cfg.Auth.Mode)
		assert.Equal(t, "https://idp.example.com", cfg.Auth.OIDC.IssuerURL)
		assert.Equal(t, "env-client", cfg.Auth.OIDC.ClientID)
	})

	t.Run("env switches to org but missing OIDC still fails fast", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ARBITRON_AUTH_MODE", "org")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "org mode requires OIDC config")
	})
}

// TestLoad_AuthBootstrapAdmins_FromYAML verifies that bootstrap admins load
// correctly from a YAML list. The env path is already tested; this covers the
// YAML-native list form which is the typical deployment pattern.
// Bootstrap admins are promoted to Admin on first login, making the YAML path
// security-critical: a silent regression would leave no admins after fresh setup.
func TestLoad_AuthBootstrapAdmins_FromYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	yamlContent := `
auth:
  bootstrapadmins:
    - "alice@example.com"
    - "bob@example.com"
    - "sub|abc123"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, []string{"alice@example.com", "bob@example.com", "sub|abc123"},
		cfg.Auth.BootstrapAdmins,
		"bootstrap admins must load from YAML list with preserved order and values")
}

// TestLoad_AuthSession_CookieSecure_Override verifies that CookieSecure can be
// explicitly set to false via YAML, which is the supported path for local HTTP
// dev (the config template comments say so). The default (true) is tested
// elsewhere; this closes the coverage gap on the false override.
func TestLoad_AuthSession_CookieSecure_Override(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	yamlContent := "auth:\n  session:\n    cookiesecure: false\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.False(t, cfg.Auth.Session.CookieSecure,
		"CookieSecure must be overridable to false via YAML for local HTTP dev")
}

// TestLoad_AuthSession_EmptyCookieNameFallback verifies that an explicitly
// blank session.cookiename in YAML is replaced with the hardcoded default
// during normalization — an empty cookie name would silently break all session
// auth in org mode.
func TestLoad_AuthSession_EmptyCookieNameFallback(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("auth:\n  session:\n    cookiename: \"\"\n"), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "arbitron_session", cfg.Auth.Session.CookieName,
		"blank cookiename must fall back to the default to prevent silent session-auth breakage")
}

// ---- Phase 6 Focus Area 1 Pass 2: robustness & security ----

// TestLoad_AuthOrgMode_WhitespaceOnlyOIDCField verifies that a field set to
// all-whitespace is rejected the same as an absent field. This locks the
// TrimSpace pre-check in validateAuth so a YAML value of "   " cannot sneak
// past org-mode validation and produce a runtime error on the first login.
func TestLoad_AuthOrgMode_WhitespaceOnlyOIDCField(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string // expected substring in error
	}{
		{
			"whitespace clientsecret rejected",
			"auth:\n  mode: \"org\"\n  oidc:\n    issuerurl: \"https://i\"\n    clientid: \"c\"\n    clientsecret: \"   \"\n    redirecturl: \"https://r/cb\"\n",
			"auth.oidc.clientsecret",
		},
		{
			"whitespace issuerurl rejected",
			"auth:\n  mode: \"org\"\n  oidc:\n    issuerurl: \"   \"\n    clientid: \"c\"\n    clientsecret: \"s\"\n    redirecturl: \"https://r/cb\"\n",
			"auth.oidc.issuerurl",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(tc.yaml), 0o600))
			cfg, err := adapterconfig.Load(configPath)
			require.Error(t, err, "whitespace-only OIDC field must fail validation the same as absent")
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestLoad_LocalMode_IgnoresOIDCConfig verifies that local mode stays
// frictionless even when OIDC fields are populated (e.g. a shared config file
// that switches modes via an env override). Local mode must never require OIDC.
func TestLoad_LocalMode_IgnoresOIDCConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Local mode with fully populated (but bogus) OIDC fields — must succeed.
	yamlContent := `
auth:
  mode: "local"
  oidc:
    issuerurl: "https://idp.example.com"
    clientid: "some-client"
    clientsecret: "some-secret"
    redirecturl: "https://app.example.com/auth/callback"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err, "local mode must not validate or require OIDC config")
	require.NotNil(t, cfg)
	assert.Equal(t, "local", cfg.Auth.Mode)
}

// writeConfigFile writes YAML to a temp config path and returns it.
func writeConfigFile(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0600))
	return p
}

func TestLoad_TLSValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "disabled is ok",
			yaml: "server:\n  tls:\n    enabled: false\n",
		},
		{
			name: "cert and key ok",
			yaml: "server:\n  tls:\n    enabled: true\n    certfile: /etc/a/cert.pem\n    keyfile: /etc/a/key.pem\n",
		},
		{
			name:    "only certfile is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n    certfile: /etc/a/cert.pem\n",
			wantErr: "certfile and keyfile",
		},
		{
			name: "autocert with cache dir ok",
			yaml: "server:\n  port: 443\n  tls:\n    enabled: true\n    autocertdomain: a.example\n    autocertcachedir: /var/lib/arbitron/acme\n",
		},
		{
			name:    "autocert without cache dir is an error",
			yaml:    "server:\n  port: 443\n  tls:\n    enabled: true\n    autocertdomain: a.example\n",
			wantErr: "autocertcachedir",
		},
		{
			name:    "cert and autocert together is an error",
			yaml:    "server:\n  port: 443\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n    autocertdomain: a.example\n    autocertcachedir: /cache\n",
			wantErr: "either",
		},
		{
			name:    "enabled with nothing is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n",
			wantErr: "requires",
		},
		{
			name:    "autocert on non-443 port is an error",
			yaml:    "server:\n  port: 8443\n  tls:\n    enabled: true\n    autocertdomain: a.example\n    autocertcachedir: /cache\n",
			wantErr: "443",
		},
		{
			name: "autocert on port 443 is ok",
			yaml: "server:\n  port: 443\n  tls:\n    enabled: true\n    autocertdomain: a.example\n    autocertcachedir: /cache\n",
		},
		{
			name: "cert/key on a non-443 port is ok (port stays free)",
			yaml: "server:\n  port: 8443\n  tls:\n    enabled: true\n    certfile: /etc/a/cert.pem\n    keyfile: /etc/a/key.pem\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestLoad_TLSLocalModePublicHost asserts the fail-fast guard against the
// highest-risk misconfiguration: local mode (no authentication) serving direct
// TLS on a non-loopback host. A real cert and a green padlock would otherwise
// front a fully unauthenticated admin API. The combination is rejected unless
// the operator opts in via the explicit escape hatch.
func TestLoad_TLSLocalModePublicHost(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "local mode + tls on public host is rejected",
			yaml:    "server:\n  host: 0.0.0.0\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n",
			wantErr: "local mode",
		},
		{
			name: "local mode + tls on loopback is ok",
			yaml: "server:\n  host: 127.0.0.1\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n",
		},
		{
			name: "local mode + tls on localhost is ok",
			yaml: "server:\n  host: localhost\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n",
		},
		{
			name: "org mode + tls on public host is ok (authenticated)",
			yaml: "server:\n  host: 0.0.0.0\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n" +
				"auth:\n  mode: org\n  oidc:\n    issuerurl: https://i.example\n    clientid: cid\n    clientsecret: sec\n    redirecturl: https://i.example/auth/callback\n",
		},
		{
			name: "local mode + tls on public host with escape hatch is ok",
			yaml: "server:\n  host: 0.0.0.0\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n    allowunauthenticated: true\n",
		},
		{
			name: "local mode + plain http on public host is not a tls error",
			yaml: "server:\n  host: 0.0.0.0\n  tls:\n    enabled: false\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestLoad_CreatedConfigFile_HasRestrictivePermissions verifies that the
// default config file created by Load (when none exists) uses 0600 permissions
// so only the process owner can read the OIDC client secret or session key if
// they are later added to the file.
func TestLoad_CreatedConfigFile_HasRestrictivePermissions(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// File must not exist yet.
	_, err := os.Stat(configPath)
	require.True(t, os.IsNotExist(err))

	_, err = adapterconfig.Load(configPath)
	require.NoError(t, err)

	info, err := os.Stat(configPath)
	require.NoError(t, err)
	// Mask to the permission bits only (ignore file-type bits).
	perm := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0600), perm,
		"default config file must be 0600 (owner-only r/w) to protect future secrets written into it")
}

// TestValidateAuth_EdgeMode mirrors the org-mode validation tests: edge mode
// requires BOTH auth.edge.teamdomain and auth.edge.audiences; either missing is
// a fail-fast load error (an edge deployment missing its team domain cannot
// verify a single token).
func TestValidateAuth_EdgeMode(t *testing.T) {
	t.Run("happy: both fields present validates", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		t.Setenv("ANTHROPIC_API_KEY", "")
		yamlContent := `
auth:
  mode: "edge"
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, "edge", cfg.Auth.Mode)
		assert.Equal(t, "https://acme.cloudflareaccess.com", cfg.Auth.Edge.TeamDomain)
		assert.Equal(t, []string{"app-aud"}, cfg.Auth.Edge.Audiences)
	})

	t.Run("missing teamdomain → error names it", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: "edge"
  edge:
    audiences: ["app-aud"]
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.edge.teamdomain")
	})

	t.Run("missing audiences → error names it", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := `
auth:
  mode: "edge"
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
`
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.edge.audiences")
	})

	t.Run("both missing → error names both", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("auth:\n  mode: \"edge\"\n"), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.edge.teamdomain")
		assert.Contains(t, err.Error(), "auth.edge.audiences")
	})

	t.Run("unknown mode error lists all three modes", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte("auth:\n  mode: \"cloud\"\n"), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), domainconfig.AuthModeLocal)
		assert.Contains(t, err.Error(), domainconfig.AuthModeOrg)
		assert.Contains(t, err.Error(), domainconfig.AuthModeEdge)
	})
}

// TestLoad_EdgeDefaultsAndEnvOverlay verifies the edge defaults exist (empty,
// not selected under local mode) and that the env overlay selects edge mode and
// parses the audiences CSV into a real []string (mirrors the oidc.scopes /
// bootstrapadmins overlay trick).
func TestLoad_EdgeDefaultsAndEnvOverlay(t *testing.T) {
	t.Run("defaults present, edge not selected under local mode", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ANTHROPIC_API_KEY", "")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, "", cfg.Auth.Edge.TeamDomain)
		assert.Len(t, cfg.Auth.Edge.Audiences, 0)
	})

	t.Run("env overlay selects edge and parses audiences list", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("ARBITRON_AUTH_MODE", "edge")
		t.Setenv("ARBITRON_AUTH_EDGE_TEAMDOMAIN", "https://acme.cloudflareaccess.com")
		t.Setenv("ARBITRON_AUTH_EDGE_AUDIENCES", "aud-one,aud-two")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		require.NotNil(t, cfg)
		assert.Equal(t, "edge", cfg.Auth.Mode)
		assert.Equal(t, "https://acme.cloudflareaccess.com", cfg.Auth.Edge.TeamDomain)
		assert.Equal(t, []string{"aud-one", "aud-two"}, cfg.Auth.Edge.Audiences)
	})

	t.Run("boundary: single-element audiences CSV", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("ARBITRON_AUTH_MODE", "edge")
		t.Setenv("ARBITRON_AUTH_EDGE_TEAMDOMAIN", "https://acme.cloudflareaccess.com")
		t.Setenv("ARBITRON_AUTH_EDGE_AUDIENCES", "solo")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		assert.Equal(t, []string{"solo"}, cfg.Auth.Edge.Audiences)
	})

	t.Run("boundary: whitespace/trailing audiences CSV trimmed", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("ARBITRON_AUTH_MODE", "edge")
		t.Setenv("ARBITRON_AUTH_EDGE_TEAMDOMAIN", "https://acme.cloudflareaccess.com")
		t.Setenv("ARBITRON_AUTH_EDGE_AUDIENCES", " a , b ")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "config.yaml"))
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, cfg.Auth.Edge.Audiences)
	})
}

// TestValidateAuth_EdgeMode_WhitespaceFields verifies that edge-mode config
// fields set to whitespace-only in YAML are rejected at load time with the
// config key name in the error, not silently passed on to edge.New where the
// error message would be less actionable. Mirrors the org-mode whitespace check
// in TestLoad_AuthOrgMode_WhitespaceOnlyOIDCField.
func TestValidateAuth_EdgeMode_WhitespaceFields(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	t.Run("whitespace-only teamdomain is rejected", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		yamlContent := "auth:\n  mode: \"edge\"\n  edge:\n    teamdomain: \"   \"\n    audiences: [\"app-aud\"]\n"
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err, "whitespace-only teamdomain must fail edge mode validation")
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.edge.teamdomain")
	})

	t.Run("whitespace-only audiences in YAML are rejected", func(t *testing.T) {
		dir := t.TempDir()
		configPath := filepath.Join(dir, "config.yaml")
		// YAML audiences: ["  "] has length 1 so the len==0 check passes —
		// we must also check that at least one entry is non-whitespace.
		yamlContent := "auth:\n  mode: \"edge\"\n  edge:\n    teamdomain: \"https://acme.cloudflareaccess.com\"\n    audiences:\n      - \"  \"\n"
		require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
		cfg, err := adapterconfig.Load(configPath)
		require.Error(t, err, "all-whitespace YAML audiences must fail edge mode validation")
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "auth.edge.audiences")
	})
}

// TestLoad_AuthMode_Edge_Normalized verifies that "EDGE" (uppercase) normalizes
// to "edge" the same way "ORG" normalizes to "org" (normalize() lowercases the
// mode string). Mirrors TestLoad_AuthMode_Normalized for the edge case.
func TestLoad_AuthMode_Edge_Normalized(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	yamlContent := `
auth:
  mode: "EDGE"
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "edge", cfg.Auth.Mode,
		`mode "EDGE" must normalize to "edge" the same way "ORG" normalizes to "org"`)
}

// TestLoad_EdgeMode_IgnoresOIDCConfig verifies that edge mode succeeds even
// when OIDC config is also present (e.g. a shared config file that switches
// modes via an env var). Mirrors TestLoad_LocalMode_IgnoresOIDCConfig.
func TestLoad_EdgeMode_IgnoresOIDCConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	yamlContent := `
auth:
  mode: "edge"
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
  oidc:
    issuerurl: "https://idp.example.com"
    clientid: "some-client"
    clientsecret: "some-secret"
    redirecturl: "https://app.example.com/auth/callback"
`
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err, "edge mode must not require or validate OIDC config")
	require.NotNil(t, cfg)
	assert.Equal(t, "edge", cfg.Auth.Mode)
}

// TestLoad_EdgeMode_TeamDomainTrimmed verifies that a YAML teamdomain value with
// surrounding whitespace is normalized by Load so the caller (edge.New) always
// receives a clean URL. Without normalization, a padded teamdomain would pass
// validateAuth (non-empty after TrimSpace) but then fail at edge.New with a
// confusing "invalid URL" error instead of a clear config error.
func TestLoad_EdgeMode_TeamDomainTrimmed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	// Note the spaces around the URL value.
	yamlContent := "auth:\n  mode: \"edge\"\n  edge:\n    teamdomain: \"  https://acme.cloudflareaccess.com  \"\n    audiences: [\"app-aud\"]\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0o600))
	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "https://acme.cloudflareaccess.com", cfg.Auth.Edge.TeamDomain,
		"teamdomain must be trimmed so edge.New receives a clean URL, not a padded one")
}

// TestLoad_AuthMode_FromEnv_EdgeMode is the edge-mode parallel of the org-mode
// env tests in TestLoad_AuthMode_FromEnv: it verifies that (a) an env-only edge
// deployment can boot (complete config), and (b) ARBITRON_AUTH_MODE=edge without
// the edge config still fails fast at load (not silently degraded).
func TestLoad_AuthMode_FromEnv_EdgeMode(t *testing.T) {
	t.Run("env switches to edge with complete edge config", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ARBITRON_AUTH_MODE", "edge")
		t.Setenv("ARBITRON_AUTH_EDGE_TEAMDOMAIN", "https://acme.cloudflareaccess.com")
		t.Setenv("ARBITRON_AUTH_EDGE_AUDIENCES", "app-aud")
		cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
		require.NoError(t, err)
		assert.Equal(t, "edge", cfg.Auth.Mode)
		assert.Equal(t, "https://acme.cloudflareaccess.com", cfg.Auth.Edge.TeamDomain)
		assert.Equal(t, []string{"app-aud"}, cfg.Auth.Edge.Audiences)
	})

	t.Run("env switches to edge but missing edge config fails fast", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("ARBITRON_AUTH_MODE", "edge")
		// No teamdomain or audiences → must fail at Load, not silently at runtime.
		cfg, err := adapterconfig.Load(filepath.Join(dir, "c.yaml"))
		require.Error(t, err)
		assert.Nil(t, cfg)
		assert.Contains(t, err.Error(), "edge mode requires edge config")
		assert.Contains(t, err.Error(), "auth.edge.teamdomain")
		assert.Contains(t, err.Error(), "auth.edge.audiences")
	})
}
