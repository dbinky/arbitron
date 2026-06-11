package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.AgentProvider = (*Provider)(nil)
}

// codexBinding builds a subscription binding. Its RevealKey is a SENTINEL that
// records whether it was called — the codex path must NEVER reveal a key, so a
// recorded call is a security failure.
func codexBinding(model string, revealed *bool) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "openai",
		Model:    model,
		RevealKey: func(context.Context) (string, error) {
			if revealed != nil {
				*revealed = true
			}
			return "sk-should-never-be-revealed", nil
		},
	}
}

// runScript for the canned happy mission: thread/turn start, a command tool call,
// the terminal agent_message, and turn.completed usage.
func happyMission() runScript {
	return runScript{lines: []string{
		`{"type":"thread.started","thread_id":"thr-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"echo hi"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"Done: edited the workspace."}}`,
		`{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":5}}`,
	}}
}

// HAPPY — probe passes, then `codex exec` runs; the JSON stream folds into an
// AgentResult, the prompt is delivered via stdin (never argv), and the launched
// env carries NO OPENAI_API_KEY.
func TestRunAgent_ExecContract(t *testing.T) {
	revealed := false
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", &revealed), BinaryPath: "codex", Runner: mr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "edit hello.txt to say goodbye",
		WorkDir: "/tmp/ws",
		Model:   "gpt-5-codex",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "Done: edited the workspace.", res.Summary)
	assert.Equal(t, "gpt-5-codex", res.Model)
	assert.Equal(t, "thr-1", res.SessionID, "codex thread id surfaced on the result")
	assert.Equal(t, 11, res.TokensIn)
	assert.Equal(t, 5, res.TokensOut)
	require.Len(t, res.Steps, 2)
	assert.Equal(t, "tool_call", res.Steps[0].Kind)
	assert.Equal(t, "assistant", res.Steps[1].Kind)

	// Second call is the run; assert the exec argv + stdin + env.
	require.GreaterOrEqual(t, len(mr.calls), 2)
	runSpec := mr.calls[1]
	assert.Equal(t, execSubcommand, runSpec.Args[0])
	assert.Equal(t, "/tmp/ws", runSpec.Dir, "WorkDir is the cwd pin")
	assert.Equal(t, "edit hello.txt to say goodbye", runSpec.Stdin, "prompt delivered via stdin")

	// SECURITY: the prompt is NOT in argv.
	for _, a := range runSpec.Args {
		assert.NotContains(t, a, "edit hello.txt", "prompt must never appear in argv")
	}
	// SECURITY: NO OPENAI_API_KEY in the launched env; HOME preserved.
	_, hasKey := envValue(runSpec.Env, "OPENAI_API_KEY")
	assert.False(t, hasKey, "the launched codex env must carry NO OPENAI_API_KEY")

	// SECURITY: the subscription path must never reveal a key.
	assert.False(t, revealed, "codex (subscription) must NEVER call binding.RevealKey")
}

// INPUT-VALIDATION — empty WorkDir rejected before env build and before any
// launch (not even the version probe).
func TestRunAgent_EmptyWorkDir(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls, "no launch on empty WorkDir")
}

// INPUT-VALIDATION — empty model rejected (fail-closed) before any launch, not
// even the version probe.
func TestRunAgent_EmptyModelFailsClosed(t *testing.T) {
	mr := &multiRunner{}
	p := New(Config{Binding: codexBinding("", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Empty(t, mr.calls, "no launch (not even the probe) on empty model")
}

// ERROR — incompatible binary: probe fails => mission fails closed, the run never
// launches.
func TestRunAgent_IncompatibleBinary(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"codex-cli 0.0.1"}}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
	assert.Len(t, mr.calls, 1, "only the probe ran; the run never launched")
}

// SECURITY — the version probe is memoized: a second mission does NOT re-probe.
func TestRunAgent_ProbeMemoized(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission(), happyMission()}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	for i := 0; i < 2; i++ {
		_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
		require.NoError(t, err)
	}
	require.Len(t, mr.calls, 3)
	assert.Contains(t, mr.calls[0].Args, "--version")
	assert.NotContains(t, mr.calls[1].Args, "--version")
	assert.NotContains(t, mr.calls[2].Args, "--version")
}

// EDGE — a probe failure is memoized: a second mission fails with the same
// ErrIncompatibleCodex without triggering a second probe. The sync.Once must
// retain the error, not silently re-probe on the next call.
func TestRunAgent_ProbeMemoizedError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"codex-cli 0.0.1"}}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	_, err1 := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.ErrorIs(t, err1, ErrIncompatibleCodex)

	_, err2 := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.ErrorIs(t, err2, ErrIncompatibleCodex, "memoized probe error must persist")

	assert.Len(t, mr.calls, 1, "probe runs exactly once; failure is cached, not retried")
}

// ERROR — non-zero process exit (run wait() error) → wrapped error naming codex
// and the model.
func TestRunAgent_NonZeroExit(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{`{"type":"turn.started"}`}, waitErr: errors.New("exit status 1")}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "gpt-5-codex", "error names the model")
}

// ERROR — run binary-not-found (runner Run returns error on the run invocation).
func TestRunAgent_BinaryNotFound(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {startErr: errors.New(`exec: "codex": not found`)}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found")
}

// ERROR — malformed JSON output → a typed parse error surfaces (fail-closed),
// never a silent empty result.
func TestRunAgent_MalformedOutput(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{`{"type":"item.completed", BROKEN`}}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrParse)
}

// ERROR — context cancel → returns a context error.
func TestRunAgent_ContextCancel(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := p.RunAgent(ctx, engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.Canceled)
}

// ERROR — mission-level timeout → context deadline error.
func TestRunAgent_Timeout(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "go",
		WorkDir: "/tmp/ws",
		Model:   "gpt-5-codex",
		Timeout: 10 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// EDGE — the provider-level Config.Timeout is the fallback when the mission
// carries no timeout (mission.Timeout <= 0). Without this test, an operator
// who sets Config.Timeout to bound long-running missions would have no coverage
// that the fallback applies (only the mission-level path is verified above).
func TestRunAgent_ProviderTimeoutApplies(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := New(Config{
		Binding:    codexBinding("gpt-5-codex", nil),
		BinaryPath: "codex",
		Runner:     mr,
		Timeout:    10 * time.Millisecond, // provider-level default; no mission timeout
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex",
		// no Timeout field — must fall back to Config.Timeout
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "Config.Timeout must apply when mission carries no timeout")
}

// EDGE — empty output is parsed tolerantly (no panic, empty result).
func TestRunAgent_EmptyOutput(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: nil}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Summary)
	assert.Empty(t, res.Steps)
}

// FORWARD-COMPAT — an event stream containing unknown event/item kinds runs
// without error end-to-end through RunAgent (parse tolerance proven at the
// provider boundary, not only the pure parser).
func TestRunAgent_UnknownEventsTolerated(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		`{"type":"thread.started","thread_id":"t9"}`,
		`{"type":"some.future.event","data":1}`,
		`{"type":"item.completed","item":{"type":"some_future_item","blob":"x"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"all good"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "all good", res.Summary)
	require.Len(t, res.Steps, 1, "only the known agent_message folds; unknown item dropped")
}

// SECURITY / log-capture — no secret leaks: the codex path reveals no key, and a
// failed run never surfaces a credential. (codex carries no injected secret, but
// the property is pinned to mirror opencode's secrecy assertion.)
func TestRunAgent_NoSecretInLogsOrEnv(t *testing.T) {
	const sentinel = "sk-should-never-be-revealed"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	revealed := false
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{`{"type":"turn.started"}`}, waitErr: errors.New("boom")}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", &revealed), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.NotContains(t, buf.String(), sentinel)
	assert.NotContains(t, err.Error(), sentinel)
	assert.False(t, revealed, "subscription path must never reveal a key")

	// The launched env carries no OPENAI_API_KEY.
	require.GreaterOrEqual(t, len(mr.calls), 2)
	_, hasKey := envValue(mr.calls[1].Env, "OPENAI_API_KEY")
	assert.False(t, hasKey)
}
