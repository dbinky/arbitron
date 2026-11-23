package config_test

import (
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_RunnersPhase4Defaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := adapterconfig.Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 60, cfg.Runners.TurnLeaseSeconds)
	assert.Equal(t, 1, cfg.Runners.SessionBundleRetention)
	assert.Equal(t, 2, cfg.Runners.RebuildMaxAttempts)
}

func TestLoad_RunnersPhase4EnvOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_RUNNERS_TURNLEASESECONDS", "90")
	t.Setenv("ARBITRON_RUNNERS_SESSIONBUNDLERETENTION", "3")
	t.Setenv("ARBITRON_RUNNERS_REBUILDMAXATTEMPTS", "4")
	cfg, err := adapterconfig.Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 90, cfg.Runners.TurnLeaseSeconds)
	assert.Equal(t, 3, cfg.Runners.SessionBundleRetention)
	assert.Equal(t, 4, cfg.Runners.RebuildMaxAttempts)
}

func TestLoad_RunnersPhase4InvalidLeaseFailsLoad(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ARBITRON_RUNNERS_TURNLEASESECONDS", "5")
	_, err := adapterconfig.Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runners.turnleaseseconds")
}
