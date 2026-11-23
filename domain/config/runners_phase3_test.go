package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validPhase3() RunnersConfig {
	return RunnersConfig{
		MoveLoadThresholdPct:    DefaultMoveLoadThresholdPct,
		MoveSizeCapBytes:        DefaultMoveSizeCapBytes,
		RestartGraceSeconds:     DefaultRestartGraceSeconds,
		TransferPartBytes:       DefaultTransferPartBytes,
		StagingDir:              "/var/lib/arbitron/runner-staging",
		CheckpointDir:           "/var/lib/arbitron/checkpoints",
		StagingRetentionSeconds: DefaultStagingRetentionSeconds,
	}
}

func TestRunnersPhase3_ValidDefaultsPass(t *testing.T) {
	assert.Empty(t, validPhase3().validatePhase3())
}

func TestRunnersPhase3_EveryViolationReported(t *testing.T) {
	c := RunnersConfig{
		MoveLoadThresholdPct:    0,
		MoveSizeCapBytes:        -1,
		RestartGraceSeconds:     5,
		TransferPartBytes:       200 << 20,
		StagingDir:              "",
		CheckpointDir:           "",
		StagingRetentionSeconds: 10,
	}
	errs := c.validatePhase3()
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	for _, key := range []string{
		"runners.moveloadthresholdpct", "runners.movesizecapbytes", "runners.restartgraceseconds",
		"runners.transferpartbytes", "runners.stagingdir", "runners.checkpointdir", "runners.stagingretentionseconds",
	} {
		assert.Contains(t, joined, key)
	}
	assert.Len(t, errs, 7)
}

func TestRunnersPhase3_Edges(t *testing.T) {
	c := validPhase3()
	c.MoveLoadThresholdPct = 101
	require.Len(t, c.validatePhase3(), 1)

	c = validPhase3()
	c.MoveSizeCapBytes = 0 // 0 disables load moves; legal
	assert.Empty(t, c.validatePhase3())

	c = validPhase3()
	c.TransferPartBytes = MinTransferPartBytes - 1
	require.Len(t, c.validatePhase3(), 1)

	c = validPhase3()
	c.TransferPartBytes = MaxTransferPartBytes
	assert.Empty(t, c.validatePhase3())

	c = validPhase3()
	c.CheckpointDir = c.StagingDir
	errs := c.validatePhase3()
	require.Len(t, errs, 1)
	assert.True(t, strings.Contains(errs[0].Error(), "must differ"))
}

func TestRunnersPhase3_ValidateIncludesPhase3(t *testing.T) {
	// Validate (P1 T1) must surface Phase-3 violations through errors.Join.
	c := validPhase3()
	c.MoveLoadThresholdPct = 0
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runners.moveloadthresholdpct")
}
