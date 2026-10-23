package pi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SECURITY — a pinned run must not copy untrusted harness stderr (which can echo
// a credential) into its error or the operator log, matching the claudecli and
// codex pinned paths. Ordinary runs keep their diagnostic summary.
func TestPinnedPiProcessFailureHidesStderr(t *testing.T) {
	const canary = "stderr-canary-sk-ant-leak"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, pinned := range []bool{true, false} {
		logs.Reset()
		workdir := t.TempDir()
		runner := &modelRunner{t: t, workdir: workdir, waitErr: &claudecli.StderrError{
			Err: errors.New("exit status 1"), Summary: "auth rejected " + canary, Tail: []byte("auth rejected " + canary)}}
		binding := piBinding("anthropic/uncatalogued-model", provider.EffortDefault)
		binding.Pinned = pinned
		m := mission()
		m.WorkDir = workdir
		_, err := New(Config{Binding: binding, BinaryPath: "pi", Runner: runner}).RunAgent(context.Background(), m)
		require.Error(t, err)
		if pinned {
			assert.NotContains(t, err.Error(), canary)
			assert.NotContains(t, logs.String(), canary)
		} else {
			assert.Contains(t, err.Error(), canary, "ordinary runs keep the redacted summary")
		}
	}
}

// Cancellation stays classifiable through the sanitized pinned error.
func TestPinnedPiSanitizedErrorKeepsIsChain(t *testing.T) {
	workdir := t.TempDir()
	runner := &modelRunner{t: t, workdir: workdir, waitErr: &claudecli.StderrError{Err: context.Canceled, Summary: "secret"}}
	binding := piBinding("anthropic/uncatalogued-model", provider.EffortDefault)
	binding.Pinned = true
	m := mission()
	m.WorkDir = workdir
	_, err := New(Config{Binding: binding, BinaryPath: "pi", Runner: runner}).RunAgent(context.Background(), m)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "secret")
}
