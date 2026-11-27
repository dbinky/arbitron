package main

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkspaceMobility_RefusesMissingInputs(t *testing.T) {
	_, err := buildWorkspaceMobility(workspaceMobilityInputs{})
	require.Error(t, err)
	assert.Equal(t, "workspace mobility: missing DB, Homes, Hub, Keys, Registry, Ledger, Events, Status, Notifier, Workspaces, Supervisor, Sessions, BootID, Audit", err.Error())
}

func TestPlacementPolicy_ComesFromRunnersConfig(t *testing.T) {
	got := placementPolicy(config.RunnersConfig{MoveLoadThresholdPct: 70, MoveSizeCapBytes: 1 << 20})
	assert.Equal(t, runner.PlacementPolicy{MoveLoadThresholdPct: 70, MoveSizeCapBytes: 1 << 20}, got)

	got = placementPolicy(config.RunnersConfig{MoveLoadThresholdPct: 70, MoveSizeCapBytes: 1 << 20, HeartbeatSeconds: 5})
	assert.Equal(t, 15*time.Second, got.TelemetryMaxAge, "telemetry is fresh for three heartbeats")
}
