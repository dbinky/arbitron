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
			Host: "0.0.0.0",
			Port: 8080,
		},
		Database: domainconfig.DatabaseConfig{
			URL: "postgres://localhost:5432/arbitron?sslmode=disable",
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
	assert.Equal(t, expected.Database.URL, cfg.Database.URL)
	assert.Equal(t, expected.Workspace.Path, cfg.Workspace.Path)
	assert.Equal(t, expected.LLM.Provider, cfg.LLM.Provider)
	assert.Equal(t, expected.LLM.APIKey, cfg.LLM.APIKey)
	assert.Equal(t, expected.Logging.Level, cfg.Logging.Level)
	assert.Equal(t, expected.Logging.File, cfg.Logging.File)
	assert.Equal(t, expected.Tracing.Enabled, cfg.Tracing.Enabled)
	assert.Equal(t, expected.Tracing.ExportDir, cfg.Tracing.ExportDir)
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
  url: "postgres://db.example.com:5432/arbitron_prod"
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
	assert.Equal(t, "postgres://db.example.com:5432/arbitron_prod", cfg.Database.URL)
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
	t.Setenv("ARBITRON_DATABASE_URL", "postgres://envdb:5432/arbitron")
	t.Setenv("ARBITRON_LOGGING_LEVEL", "warn")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "env-host", cfg.Server.Host)
	assert.Equal(t, 3000, cfg.Server.Port)
	assert.Equal(t, "postgres://envdb:5432/arbitron", cfg.Database.URL)
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
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, "postgres://localhost:5432/arbitron?sslmode=disable", cfg.Database.URL)
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
}
