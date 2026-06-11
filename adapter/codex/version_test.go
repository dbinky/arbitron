package codex

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// HAPPY — a compatible `codex --version` line passes the probe.
func TestProbeVersion_Compatible(t *testing.T) {
	fr := &fakeRunner{lines: []string{"codex-cli " + minCodexVersion}}
	err := probeVersion(context.Background(), fr, "codex")
	require.NoError(t, err)
	require.True(t, fr.runCalled)
	assert.Contains(t, fr.gotSpec.Args, "--version")
}

// ERROR — an older binary is rejected with ErrIncompatibleCodex.
func TestProbeVersion_TooOld(t *testing.T) {
	fr := &fakeRunner{lines: []string{"codex-cli 0.0.1"}}
	err := probeVersion(context.Background(), fr, "codex")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
}

// ERROR — an unparseable version line is rejected (fail-closed), not assumed OK.
func TestProbeVersion_Unparseable(t *testing.T) {
	fr := &fakeRunner{lines: []string{"codex of unknown vintage"}}
	err := probeVersion(context.Background(), fr, "codex")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
}

// ERROR — the probe subprocess failing to start (missing binary) surfaces an
// error before any mission can run.
func TestProbeVersion_StartError(t *testing.T) {
	fr := &fakeRunner{startErr: errors.New(`exec: "codex": not found`)}
	err := probeVersion(context.Background(), fr, "codex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ERROR — a nonzero exit from the probe (wait error) surfaces as an error.
func TestProbeVersion_WaitError(t *testing.T) {
	fr := &fakeRunner{lines: []string{"codex-cli " + minCodexVersion}, waitErr: errors.New("exit status 1")}
	err := probeVersion(context.Background(), fr, "codex")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
}

// guard — the shared seam is reused (compile-time check the fake satisfies it).
var _ claudecli.CommandRunner = (*fakeRunner)(nil)
