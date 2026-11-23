//go:build darwin || linux

package telemetry_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/adapter/telemetry"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/require"
)

// This is a sampler-to-hub sink check, not application-root acceptance (P2 T10).
func TestTelemetry_DiskUsesCanonicalWorkspaceVolume(t *testing.T) {
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	root := t.TempDir()
	loop := runner.NewBuiltInTelemetryLoop(telemetry.NewForRoot(root), h, time.Second)
	require.NoError(t, loop.Tick(context.Background()))
	got := h.Latest()[runner.BuiltInRunnerID]
	require.NotNil(t, got.DiskFreeBytes)
	require.Greater(t, *got.DiskFreeBytes, uint64(0))
	require.NotNil(t, got.Cores)
	require.NotNil(t, got.CPUPercent)
	require.False(t, got.SampledAt.IsZero())
	require.Equal(t, got, h.Window(runner.BuiltInRunnerID)[0])

	// A nonexistent workspace volume must not silently measure daemon cwd.
	loop = runner.NewBuiltInTelemetryLoop(telemetry.NewForRoot(filepath.Join(root, "missing-volume")), h, time.Second)
	require.NoError(t, loop.Tick(context.Background()))
	got = h.Latest()[runner.BuiltInRunnerID]
	require.Nil(t, got.DiskFreeBytes)
	require.NotNil(t, got.Cores, "only the failed disk source is unavailable")
	require.Equal(t, got, h.Window(runner.BuiltInRunnerID)[1])
}
