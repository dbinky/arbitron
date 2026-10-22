package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var layoutEnvKeys = []string{
	"ARBITRON_LAYOUT_MAXATTEMPTS",
	"ARBITRON_LAYOUT_BROWSERPATH",
	"ARBITRON_LAYOUT_ARTIFACTDIR",
	"ARBITRON_LAYOUT_BROWSERNOSANDBOX",
	"ARBITRON_LOADWARNINGS",
}

// Unset rather than blank: koanf weak-decodes an empty budget override to zero.
func isolateLayoutEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, key := range layoutEnvKeys {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
}

func TestLoad_Layout_DefaultsWhenBlockAbsent(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "server:\n  port: 9999\n"))
	require.NoError(t, err)
	assert.Equal(t, 5, adapterconfig.DefaultLayoutMaxAttempts)
	assert.Equal(t, adapterconfig.DefaultLayoutMaxAttempts, cfg.Layout.MaxAttempts)
	assert.Empty(t, cfg.Layout.BrowserPath)
	assert.Empty(t, cfg.Layout.ArtifactDir)
	assert.False(t, cfg.Layout.BrowserNoSandbox)
	assert.Empty(t, cfg.LoadWarnings)
}

func TestLoad_Layout_YAMLValues(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "layout:\n"+
		"  maxattempts: 3\n"+
		"  browserpath: /opt/chrome/chrome\n"+
		"  artifactdir: /var/lib/arbitron/layout-artifacts\n"+
		"  browsernosandbox: true\n"))
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Layout.MaxAttempts)
	assert.Equal(t, "/opt/chrome/chrome", cfg.Layout.BrowserPath)
	assert.Equal(t, "/var/lib/arbitron/layout-artifacts", cfg.Layout.ArtifactDir)
	assert.True(t, cfg.Layout.BrowserNoSandbox)
	assert.Empty(t, cfg.LoadWarnings)
}

func TestLoad_Layout_EnvOverridesYAML(t *testing.T) {
	isolateLayoutEnv(t)
	path := writeConfigFile(t, "layout:\n"+
		"  maxattempts: 3\n"+
		"  browserpath: /yaml/chrome\n"+
		"  artifactdir: /yaml/artifacts\n"+
		"  browsernosandbox: false\n")
	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "7")
	t.Setenv("ARBITRON_LAYOUT_BROWSERPATH", "/env/chromium")
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", "/env/artifacts")
	t.Setenv("ARBITRON_LAYOUT_BROWSERNOSANDBOX", "true")
	cfg, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.Layout.MaxAttempts)
	assert.Equal(t, "/env/chromium", cfg.Layout.BrowserPath)
	assert.Equal(t, "/env/artifacts", cfg.Layout.ArtifactDir)
	assert.True(t, cfg.Layout.BrowserNoSandbox)
}

func TestLoad_Layout_EnvOnlyOverridesDefault(t *testing.T) {
	isolateLayoutEnv(t)
	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "9")
	cfg, err := adapterconfig.Load(writeConfigFile(t, "server:\n  port: 9999\n"))
	require.NoError(t, err)
	assert.Equal(t, 9, cfg.Layout.MaxAttempts)
}

func TestLoad_Layout_MaxAttemptsBelowOneClampsWithWarning(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env, warning string
	}{
		{"yaml zero", "layout:\n  maxattempts: 0\n", "", "layout.maxattempts 0 is below 1; using 1"},
		{"yaml negative", "layout:\n  maxattempts: -2\n", "", "layout.maxattempts -2 is below 1; using 1"},
		{"env zero over valid yaml", "layout:\n  maxattempts: 4\n", "0", "layout.maxattempts 0 is below 1; using 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLayoutEnv(t)
			if tc.env != "" {
				t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", tc.env)
			}
			cfg, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			require.NoError(t, err, "too-small budgets are nonfatal")
			assert.Equal(t, 1, cfg.Layout.MaxAttempts)
			assert.Equal(t, []string{tc.warning}, cfg.LoadWarnings)
		})
	}
}

func TestLoad_Layout_MaxAttemptsOneIsAcceptedSilently(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "layout:\n  maxattempts: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Layout.MaxAttempts)
	assert.Empty(t, cfg.LoadWarnings)
}

func TestLoad_Layout_LoadWarningsCannotBeInjectedFromConfig(t *testing.T) {
	isolateLayoutEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "loadwarnings: [injected]\nlayout:\n  maxattempts: 2\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.LoadWarnings)
}

func TestLoad_Layout_InvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, envKey, env, wantError string
	}{
		{"malformed yaml", "layout: [\n", "", "", "loading config file"},
		{"nonnumeric yaml budget", "layout:\n  maxattempts: many\n", "", "", "unmarshalling config"},
		{"invalid yaml boolean", "layout:\n  browsernosandbox: perhaps\n", "", "", "unmarshalling config"},
		{"nonnumeric env budget", "layout:\n  maxattempts: 3\n", "ARBITRON_LAYOUT_MAXATTEMPTS", "many", "unmarshalling config"},
		{"invalid env boolean", "layout:\n  browsernosandbox: false\n", "ARBITRON_LAYOUT_BROWSERNOSANDBOX", "perhaps", "unmarshalling config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateLayoutEnv(t)
			if tc.envKey != "" {
				t.Setenv(tc.envKey, tc.env)
			}
			cfg, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			require.ErrorContains(t, err, tc.wantError)
			assert.Nil(t, cfg, "invalid input must not install a fallback configuration")
		})
	}
}

func TestLoad_Layout_TrimPaths(t *testing.T) {
	for _, source := range []string{"yaml", "env"} {
		t.Run(source, func(t *testing.T) {
			isolateLayoutEnv(t)
			path := writeConfigFile(t, "layout:\n  browserpath: '  /opt/Chrome Browser/chrome  '\n  artifactdir: '  /var/layout artifacts  '\n")
			if source == "env" {
				t.Setenv("ARBITRON_LAYOUT_BROWSERPATH", "\t/opt/Chrome Browser/chrome \n")
				t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", "\t/var/layout artifacts \n")
			}
			cfg, err := adapterconfig.Load(path)
			require.NoError(t, err)
			assert.Equal(t, "/opt/Chrome Browser/chrome", cfg.Layout.BrowserPath)
			assert.Equal(t, "/var/layout artifacts", cfg.Layout.ArtifactDir)
		})
	}
}

func TestLoad_Layout_NegativeEnvAndRepeatedLoads(t *testing.T) {
	isolateLayoutEnv(t)
	path := writeConfigFile(t, "layout:\n  maxattempts: 3\n")
	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "-8")
	t.Setenv("ARBITRON_LOADWARNINGS", "injected")
	first, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Layout.MaxAttempts)
	assert.Equal(t, []string{"layout.maxattempts -8 is below 1; using 1"}, first.LoadWarnings)

	t.Setenv("ARBITRON_LAYOUT_MAXATTEMPTS", "1")
	second, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 1, second.Layout.MaxAttempts)
	assert.Empty(t, second.LoadWarnings, "valid loads discard injected and previous warnings")
	assert.Equal(t, []string{"layout.maxattempts -8 is below 1; using 1"}, first.LoadWarnings)

	require.NoError(t, os.Unsetenv("ARBITRON_LAYOUT_MAXATTEMPTS"))
	third, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 3, third.Layout.MaxAttempts)
	assert.Empty(t, third.LoadWarnings)
}

func TestLoad_Layout_FreshDefaultFileShipsLiveMaxAttempts(t *testing.T) {
	isolateLayoutEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.Layout.MaxAttempts)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(raw)
	assert.Contains(t, content, "layout:\n  maxattempts: 5\n")
	assert.Contains(t, content, "# browserpath:")
	assert.Contains(t, content, "# artifactdir:")
	assert.Contains(t, content, "# browsernosandbox:")
	edited := strings.Replace(content, "  maxattempts: 5\n", "  maxattempts: 2\n", 1)
	require.NotEqual(t, content, edited)
	require.NoError(t, os.WriteFile(path, []byte(edited), 0o600))
	cfg, err = adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.Layout.MaxAttempts, "editing the shipped key changes the loaded budget")
}
