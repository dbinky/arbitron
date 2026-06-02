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

