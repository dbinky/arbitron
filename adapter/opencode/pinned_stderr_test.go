package opencode

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingRunRunner answers --version and fails the run with a stderr tail.
type failingRunRunner struct{ waitErr error }

func (r failingRunRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	ch := make(chan string, 1)
	if slices.Equal(spec.Args, []string{"--version"}) {
		ch <- "opencode " + minOpencodeVersion
		close(ch)
		return ch, func() error { return nil }, nil
	}
	close(ch)
	return ch, func() error { return r.waitErr }, nil
}

// SECURITY — a pinned run must not copy untrusted harness stderr (which can echo
// a credential) into its error or the operator log, matching the claudecli and
// codex pinned paths. Ordinary runs keep their diagnostic summary.
func TestPinnedOpenCodeProcessFailureHidesStderr(t *testing.T) {
	const canary = "stderr-canary-sk-ant-leak"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, pinned := range []bool{true, false} {
		logs.Reset()
		binding := anthropicBinding("key", nil)
		binding.Model, binding.Pinned = "anthropic/claude-sonnet-4-5", pinned
		runner := failingRunRunner{waitErr: &claudecli.StderrError{
			Err: errors.New("exit status 1"), Summary: "auth rejected " + canary, Tail: []byte("auth rejected " + canary)}}
		_, err := New(Config{Binding: binding, BinaryPath: "opencode", Runner: runner}).
			RunAgent(context.Background(), engine.AgentMission{Prompt: "do the thing", WorkDir: t.TempDir(), Model: binding.Model})
		require.Error(t, err)
		if pinned {
			assert.NotContains(t, err.Error(), canary)
			assert.NotContains(t, logs.String(), canary)
		} else {
			assert.Contains(t, err.Error(), canary, "ordinary runs keep the redacted summary")
		}
	}
}
