//go:build linux

package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A healthy Linux built-in reports live telemetry with no runner setup.
func TestNewSamplesThisLinuxHost(t *testing.T) {
	m := NewForRoot(t.TempDir())
	_, isLinux := m.(LinuxSampler)
	require.True(t, isLinux, "linux builds use the Linux sampler, never Null")
	tel, err := m.Sample(context.Background())
	require.NoError(t, err)
	require.NotNil(t, tel.Cores)
	assert.Greater(t, *tel.Cores, 0)
	require.NotNil(t, tel.RAMBytes)
	require.NotNil(t, tel.MemUsedBytes)
	require.NotNil(t, tel.UptimeSeconds)
	require.NotNil(t, tel.DiskFreeBytes)
	require.NotNil(t, tel.Load1Pct)
	require.NotNil(t, tel.CPUPercent)
	if tel.MemPressure != nil {
		assert.Contains(t, []string{"normal", "warn", "critical"}, *tel.MemPressure)
	}
	if tel.Thermal != nil {
		assert.Contains(t, []string{"nominal", "fair", "serious", "critical"}, *tel.Thermal)
	}
}
