package config_test

import (
	"os"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var runnersEnvKeys = []string{
	"ARBITRON_RUNNERS_DEFAULTSTEPLIMIT",
	"ARBITRON_RUNNERS_PROBEINTERVALSECONDS",
	"ARBITRON_RUNNERS_PROBEJITTERSECONDS",
	"ARBITRON_RUNNERS_TRANSIENTMAXATTEMPTS",
	"ARBITRON_RUNNERS_TRANSIENTBACKOFFMINMS",
	"ARBITRON_RUNNERS_TRANSIENTBACKOFFMAXMS",
	"ARBITRON_RUNNERS_EVENTRETENTION",
}

// Unset rather than blank: koanf weak-decodes an empty override to zero.
func isolateRunnersEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, key := range runnersEnvKeys {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
}

func TestLoad_Runners_DefaultsWhenBlockAbsent(t *testing.T) {
	isolateRunnersEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "server:\n  port: 9999\n"))
	require.NoError(t, err)
	r := cfg.Runners
	assert.Equal(t, 8, r.DefaultStepLimit)
	assert.Equal(t, 60, r.ProbeIntervalSeconds)
	assert.Equal(t, 10, r.ProbeJitterSeconds)
	assert.Equal(t, 3, r.TransientMaxAttempts)
	assert.Equal(t, 1000, r.TransientBackoffMinMs)
	assert.Equal(t, 30000, r.TransientBackoffMaxMs)
	assert.Equal(t, 1000, r.EventRetention)
	require.NoError(t, cfg.Runners.Validate())
}

func TestLoad_Runners_GeneratedDefaultFileLoads(t *testing.T) {
	isolateRunnersEnv(t)
	// A missing file makes Load write defaultYAMLContent; the commented runners
	// block must not change the defaults.
	path := t.TempDir() + "/fresh/config.yaml"
	cfg, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 8, cfg.Runners.DefaultStepLimit)
}

func TestLoad_Runners_YAMLOverrides(t *testing.T) {
	isolateRunnersEnv(t)
	cfg, err := adapterconfig.Load(writeConfigFile(t, "runners:\n"+
		"  defaultsteplimit: 4\n"+
		"  probeintervalseconds: 30\n"+
		"  probejitterseconds: 0\n"+
		"  transientmaxattempts: 5\n"+
		"  transientbackoffminms: 250\n"+
		"  transientbackoffmaxms: 4000\n"+
		"  eventretention: 500\n"))
	require.NoError(t, err)
	r := cfg.Runners
	assert.Equal(t, 4, r.DefaultStepLimit)
	assert.Equal(t, 30, r.ProbeIntervalSeconds)
	assert.Equal(t, 0, r.ProbeJitterSeconds)
	assert.Equal(t, 5, r.TransientMaxAttempts)
	assert.Equal(t, 250, r.TransientBackoffMinMs)
	assert.Equal(t, 4000, r.TransientBackoffMaxMs)
	assert.Equal(t, 500, r.EventRetention)
}

func TestLoad_Runners_EnvOverridesYAML(t *testing.T) {
	isolateRunnersEnv(t)
	path := writeConfigFile(t, "runners:\n  defaultsteplimit: 4\n")
	t.Setenv("ARBITRON_RUNNERS_DEFAULTSTEPLIMIT", "12")
	t.Setenv("ARBITRON_RUNNERS_EVENTRETENTION", "2500")
	cfg, err := adapterconfig.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 12, cfg.Runners.DefaultStepLimit)
	assert.Equal(t, 2500, cfg.Runners.EventRetention)
	assert.Equal(t, 60, cfg.Runners.ProbeIntervalSeconds, "untouched keys keep their defaults")
}

func TestLoad_Runners_InvalidValuesRefuseToLoad(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, key string
	}{
		{"zero step limit", "runners:\n  defaultsteplimit: 0\n", "runners.defaultsteplimit"},
		{"step limit above 256", "runners:\n  defaultsteplimit: 300\n", "runners.defaultsteplimit"},
		{"probe interval too short", "runners:\n  probeintervalseconds: 1\n", "runners.probeintervalseconds"},
		{"negative jitter", "runners:\n  probejitterseconds: -5\n", "runners.probejitterseconds"},
		{"zero transient attempts", "runners:\n  transientmaxattempts: 0\n", "runners.transientmaxattempts"},
		{"backoff max below min", "runners:\n  transientbackoffminms: 5000\n  transientbackoffmaxms: 100\n", "runners.transientbackoffmaxms"},
		{"retention too small", "runners:\n  eventretention: 10\n", "runners.eventretention"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateRunnersEnv(t)
			cfg, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), "invalid runners config")
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestLoad_Runners_InvalidEnvRefusesToLoad(t *testing.T) {
	isolateRunnersEnv(t)
	t.Setenv("ARBITRON_RUNNERS_TRANSIENTMAXATTEMPTS", "42")
	_, err := adapterconfig.Load(writeConfigFile(t, "server:\n  port: 9999\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runners.transientmaxattempts")
}
