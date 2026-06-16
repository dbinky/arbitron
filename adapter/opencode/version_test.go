package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// versionRunner is a fakeRunner specialization that returns canned --version output.
func versionRunner(line string, startErr error) *fakeRunner {
	return &fakeRunner{lines: []string{line}, startErr: startErr}
}

// HAPPY — a version at/above the minimum probes OK and records `--version` argv.
func TestProbeVersion_Compatible(t *testing.T) {
	fr := versionRunner("opencode "+minOpencodeVersion, nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.NoError(t, err)
	assert.Contains(t, fr.gotSpec.Args, "--version")
}

// ERROR — a version below the minimum fails with ErrIncompatibleOpencode naming
// the found + required versions.
func TestProbeVersion_TooOld(t *testing.T) {
	fr := versionRunner("opencode 0.0.55", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
	assert.Contains(t, err.Error(), "0.0.55")
	assert.Contains(t, err.Error(), minOpencodeVersion)
}

// ERROR — binary missing (start error) surfaces a clear error.
func TestProbeVersion_BinaryMissing(t *testing.T) {
	fr := versionRunner("", errors.New(`exec: "opencode": not found`))
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// EDGE — unparseable version output fails closed (incompatible), not a silent pass.
func TestProbeVersion_Unparseable(t *testing.T) {
	fr := versionRunner("not-a-version", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
}

// HAPPY — a newer version (above the minimum) is accepted.
func TestProbeVersion_Newer(t *testing.T) {
	fr := versionRunner("opencode 99.0.0", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.NoError(t, err)
}

// BOUNDARY — 0.4.45 is rejected: the 0.x line used a different CLI contract.
func TestProbeVersion_0_4_45_Rejected(t *testing.T) {
	fr := versionRunner("opencode 0.4.45", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err, "0.4.45 (old contract) must be rejected")
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
}

// BOUNDARY — 1.16.9 is rejected: below the 1.17.0 floor.
func TestProbeVersion_1_16_9_Rejected(t *testing.T) {
	fr := versionRunner("opencode 1.16.9", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err, "1.16.9 (below floor) must be rejected")
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
}

// BOUNDARY — 1.17.0 is the exact floor and must be accepted.
func TestProbeVersion_1_17_0_Accepted(t *testing.T) {
	fr := versionRunner("opencode 1.17.0", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.NoError(t, err, "1.17.0 (floor) must be accepted")
}

// BOUNDARY — 1.17.7 is above the floor and must be accepted.
func TestProbeVersion_1_17_7_Accepted(t *testing.T) {
	fr := versionRunner("opencode 1.17.7", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.NoError(t, err, "1.17.7 must be accepted")
}

var _ claudecli.CommandRunner = (*fakeRunner)(nil)
