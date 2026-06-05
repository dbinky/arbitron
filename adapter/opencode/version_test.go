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

var _ claudecli.CommandRunner = (*fakeRunner)(nil)
