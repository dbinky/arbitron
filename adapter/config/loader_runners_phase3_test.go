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

var runnersPhase3EnvKeys = []string{
	"ARBITRON_RUNNERS_MOVELOADTHRESHOLDPCT", "ARBITRON_RUNNERS_MOVESIZECAPBYTES",
	"ARBITRON_RUNNERS_RESTARTGRACESECONDS", "ARBITRON_RUNNERS_TRANSFERPARTBYTES",
	"ARBITRON_RUNNERS_STAGINGDIR", "ARBITRON_RUNNERS_CHECKPOINTDIR",
	"ARBITRON_RUNNERS_STAGINGRETENTIONSECONDS", "ARBITRON_DATABASE_PATH",
}

func isolateRunnersPhase3Env(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "")
	for _, k := range runnersPhase3EnvKeys {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

func TestLoad_RunnersPhase3_Defaults(t *testing.T) {
	isolateRunnersPhase3Env(t)
	dbDir := t.TempDir()
	cfg, err := adapterconfig.Load(writeConfigFile(t, "database:\n  path: "+filepath.Join(dbDir, "arbitron.db")+"\n"))
	require.NoError(t, err)
	r := cfg.Runners
	assert.Equal(t, 50, r.MoveLoadThresholdPct)
	assert.Equal(t, int64(2147483648), r.MoveSizeCapBytes)
	assert.Equal(t, 180, r.RestartGraceSeconds)
	assert.Equal(t, int64(67108864), r.TransferPartBytes)
	assert.Equal(t, 3600, r.StagingRetentionSeconds)
	assert.Equal(t, filepath.Join(dbDir, "runner-staging"), r.StagingDir)
	assert.Equal(t, filepath.Join(dbDir, "checkpoints"), r.CheckpointDir)
}

func TestLoad_RunnersPhase3_YAMLAndEnvOverride(t *testing.T) {
	isolateRunnersPhase3Env(t)
	t.Setenv("ARBITRON_RUNNERS_RESTARTGRACESECONDS", "300")
	cfg, err := adapterconfig.Load(writeConfigFile(t, "runners:\n"+
		"  moveloadthresholdpct: 70\n"+
		"  movesizecapbytes: 1073741824\n"+
		"  restartgraceseconds: 120\n"+
		"  transferpartbytes: 33554432\n"+
		"  stagingdir: /srv/arb/stage\n"+
		"  checkpointdir: /srv/arb/ckpt\n"+
		"  stagingretentionseconds: 600\n"))
	require.NoError(t, err)
	r := cfg.Runners
	assert.Equal(t, 70, r.MoveLoadThresholdPct)
	assert.Equal(t, int64(1<<30), r.MoveSizeCapBytes)
	assert.Equal(t, 300, r.RestartGraceSeconds, "env beats YAML")
	assert.Equal(t, int64(32<<20), r.TransferPartBytes)
	assert.Equal(t, "/srv/arb/stage", r.StagingDir)
	assert.Equal(t, "/srv/arb/ckpt", r.CheckpointDir)
	assert.Equal(t, 600, r.StagingRetentionSeconds)
}

func TestLoad_RunnersPhase3_InvalidRejected(t *testing.T) {
	for name, yaml := range map[string]string{
		"threshold zero":  "runners:\n  moveloadthresholdpct: 0\n",
		"negative cap":    "runners:\n  movesizecapbytes: -5\n",
		"grace too short": "runners:\n  restartgraceseconds: 1\n",
		"part over cap":   "runners:\n  transferpartbytes: 209715200\n",
		"same dirs":       "runners:\n  stagingdir: /x\n  checkpointdir: /x\n",
		"retention short": "runners:\n  stagingretentionseconds: 5\n",
	} {
		t.Run(name, func(t *testing.T) {
			isolateRunnersPhase3Env(t)
			_, err := adapterconfig.Load(writeConfigFile(t, yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "runners.")
		})
	}
	assert.Equal(t, int64(95<<20), domainconfig.MaxTransferPartBytes)
}
