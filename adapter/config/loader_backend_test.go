package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// happy — an omitted llm.defaultagentbackend floors to claude-code (the
// deployment default for this org).
func TestLoad_DefaultAgentBackend_DefaultsClaudeCode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "claude-code", cfg.LLM.DefaultAgentBackend,
		"omitted llm.defaultagentbackend must floor to claude-code")
}

// happy — an explicit opencode in YAML overrides the default.
func TestLoad_DefaultAgentBackend_OverrideOpencode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := "llm:\n  defaultagentbackend: opencode\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "opencode", cfg.LLM.DefaultAgentBackend)
}

// boundary — an explicitly empty value still floors to claude-code (the floor
// is the single deployment policy seam).
func TestLoad_DefaultAgentBackend_EmptyFloorsClaudeCode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")

	yamlContent := "llm:\n  defaultagentbackend: \"\"\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "claude-code", cfg.LLM.DefaultAgentBackend)
}

// integration — env var ARBITRON_LLM_DEFAULTAGENTBACKEND overrides YAML.
func TestLoad_DefaultAgentBackend_EnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_LLM_DEFAULTAGENTBACKEND", "opencode")

	yamlContent := "llm:\n  defaultagentbackend: claude-code\n"
	require.NoError(t, os.WriteFile(configPath, []byte(yamlContent), 0644))

	cfg, err := adapterconfig.Load(configPath)
	require.NoError(t, err)
	assert.Equal(t, "opencode", cfg.LLM.DefaultAgentBackend,
		"env ARBITRON_LLM_DEFAULTAGENTBACKEND must win over YAML")
}
