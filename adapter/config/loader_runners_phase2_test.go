package config_test

import (
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var runnersPhase2Env = []string{
	"ARBITRON_RUNNERS_HEARTBEATSECONDS", "ARBITRON_RUNNERS_SUSPECTAFTERSECONDS", "ARBITRON_RUNNERS_LOSTAFTERSECONDS",
	"ARBITRON_RUNNERS_OFFERTTLSECONDS", "ARBITRON_RUNNERS_OUTPUTBUFFERFRAMES", "ARBITRON_RUNNERS_TELEMETRYWINDOW",
	"ARBITRON_RUNNERS_SCRATCHROOT", "ANTHROPIC_API_KEY",
}

func clearRunnersPhase2Env(t *testing.T) {
	t.Helper()
	for _, k := range runnersPhase2Env {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func loadYAML(t *testing.T, body string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	_, err := adapterconfig.Load(path)
	return path, err
}

func TestLoad_RunnersPhase2Defaults(t *testing.T) {
	clearRunnersPhase2Env(t)
	cfg, err := adapterconfig.Load(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	r := cfg.Runners
	assert.Equal(t, 5, r.HeartbeatSeconds)
	assert.Equal(t, 30, r.SuspectAfterSeconds)
	assert.Equal(t, 120, r.LostAfterSeconds)
	assert.Equal(t, 30, r.OfferTTLSeconds)
	assert.Equal(t, 4096, r.OutputBufferFrames)
	assert.Equal(t, 60, r.TelemetryWindow)
	assert.Equal(t, filepath.Join("./workspaces", ".runner-scratch"), r.ScratchRoot)
}

func TestLoad_RunnersPhase2YAMLAndEnvOverride(t *testing.T) {
	clearRunnersPhase2Env(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runners:
  heartbeatseconds: 2
  suspectafterseconds: 10
  lostafterseconds: 40
  offerttlseconds: 8
  outputbufferframes: 128
  telemetrywindow: 30
  scratchroot: /var/tmp/arbitron-scratch
`), 0o600))
	t.Setenv("ARBITRON_RUNNERS_LOSTAFTERSECONDS", "90")
	cfg, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.Runners.HeartbeatSeconds)
	assert.Equal(t, 10, cfg.Runners.SuspectAfterSeconds)
	assert.Equal(t, 90, cfg.Runners.LostAfterSeconds, "env wins over YAML")
	assert.Equal(t, 8, cfg.Runners.OfferTTLSeconds)
	assert.Equal(t, 128, cfg.Runners.OutputBufferFrames)
	assert.Equal(t, 30, cfg.Runners.TelemetryWindow)
	assert.Equal(t, "/var/tmp/arbitron-scratch", cfg.Runners.ScratchRoot)
}

func TestLoad_RunnersPhase2RejectsInvalid(t *testing.T) {
	clearRunnersPhase2Env(t)
	cases := map[string]struct{ yaml, wantKey string }{
		"lost not above suspect":   {"runners:\n  suspectafterseconds: 60\n  lostafterseconds: 60\n", "runners.lostafterseconds"},
		"suspect not above beat":   {"runners:\n  heartbeatseconds: 30\n  suspectafterseconds: 30\n", "runners.suspectafterseconds"},
		"heartbeat zero":           {"runners:\n  heartbeatseconds: 0\n", "runners.heartbeatseconds"},
		"offer ttl not below lost": {"runners:\n  offerttlseconds: 120\n", "runners.offerttlseconds"},
		"offer ttl zero":           {"runners:\n  offerttlseconds: 0\n", "runners.offerttlseconds"},
		"tiny output buffer":       {"runners:\n  outputbufferframes: 10\n", "runners.outputbufferframes"},
		"telemetry window zero":    {"runners:\n  telemetrywindow: 0\n", "runners.telemetrywindow"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadYAML(t, tc.yaml)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantKey)
		})
	}
}

func TestLoad_RunnersPhase2ReportsEveryViolation(t *testing.T) {
	clearRunnersPhase2Env(t)
	_, err := loadYAML(t, "runners:\n  telemetrywindow: 0\n  outputbufferframes: 1\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runners.telemetrywindow")
	assert.Contains(t, err.Error(), "runners.outputbufferframes")
}
