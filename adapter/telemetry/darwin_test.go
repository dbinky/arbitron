//go:build darwin

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSamplesThisMac(t *testing.T) {
	m := NewForRoot(t.TempDir())
	_, isMac := m.(MacSampler)
	require.True(t, isMac, "darwin builds use the macOS sampler, never Null")
	tel, err := m.Sample(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tel.Cores)
	assert.Greater(t, *tel.Cores, 0)
	require.NotNil(t, tel.RAMBytes)
	assert.Greater(t, *tel.RAMBytes, uint64(0))
	require.NotNil(t, tel.MemUsedBytes)
	require.NotNil(t, tel.UptimeSeconds)
	require.NotNil(t, tel.DiskFreeBytes)
	require.NotNil(t, tel.Load1Pct)
	require.NotNil(t, tel.CPUPercent)
	assert.GreaterOrEqual(t, *tel.CPUPercent, 0.0)
	assert.LessOrEqual(t, *tel.CPUPercent, 100.0)
	require.NotNil(t, tel.MemPressure)
	assert.Contains(t, []string{"normal", "warn", "critical"}, *tel.MemPressure)
	if tel.Thermal != nil {
		assert.Contains(t, []string{"nominal", "fair", "serious", "critical"}, *tel.Thermal)
	}
	_, isMac = New().(MacSampler)
	assert.True(t, isMac)
}
