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

	"github.com/arbitron/arbitron/adapter/claudecli"
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

// CORRECTNESS — single model source. argv is built from p.binding.Model, so the
// result AND every error string must label the SAME model that fed argv — never
// mission.Model, which can diverge. This test pins that: when the binding model
// and the mission model differ, the AgentResult.Model carries the BINDING model
// (the one codex actually ran), so an operator is never sent chasing a phantom
// model the run never used.
func TestRunAgent_ResultModelMatchesArgvModel(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	// Binding drives argv (buildArgs uses binding.Model); the mission carries a
	// DIFFERENT model id to expose any args/label skew.
	p := New(Config{Binding: codexBinding("binding-model", nil), BinaryPath: "codex", Runner: mr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "go",
		WorkDir: "/tmp/ws",
		Model:   "mission-model-DIVERGENT",
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// argv must carry the binding model (buildExecArgs emits `-m <model>`).
	runSpec := mr.calls[1]
	assert.Contains(t, runSpec.Args, "binding-model", "argv must use the binding model")
	assert.NotContains(t, runSpec.Args, "mission-model-DIVERGENT")

	// The result must label the model that actually ran (the binding model), NOT
	// the divergent mission model.
	assert.Equal(t, "binding-model", res.Model,
		"AgentResult.Model must be the model that fed argv (binding), never mission.Model")
}

// CORRECTNESS — single model source on the FAILURE path. A non-zero exit must
// produce an error labeled with the model that fed argv (binding model), not the
// divergent mission model — so a failing run's error names the model codex ran.
func TestRunAgent_ErrorLabelsArgvModel(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{`{"type":"thread.started","thread_id":"t"}`}, waitErr: errors.New("exit status 7")},
	}}
	p := New(Config{Binding: codexBinding("binding-model", nil), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "go",
		WorkDir: "/tmp/ws",
		Model:   "mission-model-DIVERGENT",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binding-model", "error must name the model that fed argv")
	assert.NotContains(t, err.Error(), "mission-model-DIVERGENT",
		"error must never name the divergent mission model")
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

// ERROR — on a timeout kill, the surfaced error must STILL be errors.Is
// DeadlineExceeded AND carry the redacted stderr summary, with the full tail
// logged for operators only. Regression guard against the silent stderr discard
// on the timeout path.
func TestRunAgent_TimeoutSurfacesStderr(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	killErr := &claudecli.StderrError{
		Err:     errors.New("signal: killed"),
		Summary: "codex: stream closed by provider",
		Tail:    []byte("turn started\nprovider dropped stream\ncodex: stream closed by provider"),
	}
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true, ctxWaitErr: killErr}}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex", Timeout: 10 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Nil(t, res)

	assert.ErrorIs(t, err, context.DeadlineExceeded, "timeout semantics must be preserved")
	assert.Contains(t, err.Error(), "codex: stream closed by provider", "redacted stderr summary must be surfaced")

	assert.Contains(t, buf.String(), "provider dropped stream", "full stderr tail must be logged")
	assert.NotContains(t, err.Error(), "provider dropped stream", "full tail must not enter the error chain")
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

// FA13 PAIRED (§2+§3+§6) — the argv builder (args.go), the subscription env
// builder (env.go), and the batch provider (provider.go) all compose onto the
// SAME claudecli.RunSpec in a single RunAgent call. This test verifies:
//   - --cd <WorkDir> appears in runSpec.Args (from args.go, bridging §2 and §6)
//   - OPENAI_API_KEY is absent from runSpec.Env (from env.go, bridging §3 and §6)
//   - HOME is present in runSpec.Env (subscription env must preserve HOME for
//     codex to locate its on-disk auth.json)
//   - The prompt appears in runSpec.Stdin and NOT in any arg (§2 and §6 agree
//     on the stdin-vs-argv boundary)
//
// These three units share one vocabulary: if any breaks the shared contract
// (e.g. args.go drops --cd, env.go forwards OPENAI_API_KEY) the composed
// RunSpec reaching the runner will be wrong even if each unit alone looks clean.
func TestRunAgent_ArgvEnvSubscriptionContract(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("OPENAI_API_KEY", "sk-poison")

	const workDir = "/tmp/test-ws"
	const prompt = "do the task"
	const model = "gpt-5-codex"

	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := New(Config{Binding: codexBinding(model, nil), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  prompt,
		WorkDir: workDir,
		Model:   model,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mr.calls), 2, "probe + run calls expected")

	runSpec := mr.calls[1] // second call is the actual exec run

	// §2 + §6: args.go must place --cd <WorkDir> in argv.
	cdVal, cdOK := argValue(runSpec.Args, cdFlag)
	require.True(t, cdOK, "--cd flag must be present in run argv (args.go contribution)")
	assert.Equal(t, workDir, cdVal, "--cd value must equal mission.WorkDir")

	// §2 + §6: prompt via stdin, never in argv.
	assert.Equal(t, prompt, runSpec.Stdin, "prompt must be delivered via stdin")
	for _, a := range runSpec.Args {
		assert.NotContains(t, a, prompt, "prompt must never appear in argv (§2 boundary)")
	}

	// §3 + §6: subscription env must carry no OPENAI_API_KEY even when the parent env has one.
	_, hasKey := envValue(runSpec.Env, "OPENAI_API_KEY")
	assert.False(t, hasKey, "OPENAI_API_KEY must be absent from the launched env (§3 boundary)")

	// §3 + §6: HOME must be preserved so codex can locate ~/.codex/auth.json.
	homeVal, hasHome := envValue(runSpec.Env, "HOME")
	assert.True(t, hasHome, "HOME must be forwarded into the launched env (§3 boundary)")
	assert.Equal(t, "/home/testuser", homeVal, "HOME must carry the parent HOME value")
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

// usageLimitMission is the canned stream of a real quota refusal: codex emits a
// `turn.failed` carrying the cause on --json STDOUT and exits non-zero, while
// stderr holds only incidental noise (a shell-snapshot warning). Reproduced from
// the 2026-09-12 ralph-loop failures, where the surfaced error was a bare
// "exit status 1" and the real cause never reached the trace.
func usageLimitMission() runScript {
	return runScript{
		lines: []string{
			`{"type":"thread.started","thread_id":"thr-limited"}`,
			`{"type":"turn.started"}`,
			`{"type":"turn.failed","error":{"message":"You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 14th, 2026 8:05 PM."}}`,
		},
		waitErr: errors.New("exit status 1"),
	}
}

// HAPPY (the bug) — a non-zero exit whose stdout carried a `turn.failed` event
// must surface that event's message as the cause. Without it a quota refusal is
// indistinguishable from a crash.
func TestRunAgent_NonZeroExitSurfacesStdoutFailureCause(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), usageLimitMission()}}
	p := New(Config{Binding: codexBinding("gpt-5.6-sol", nil), BinaryPath: "codex", Runner: mr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "approve", WorkDir: "/tmp/ws", Model: "gpt-5.6-sol",
	})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "You've hit your usage limit",
		"the stdout turn.failed message is the real cause and must reach the surfaced error")
	// The exit status and model must survive alongside the cause.
	assert.Contains(t, err.Error(), "exit status 1", "the process exit must stay visible")
	assert.Contains(t, err.Error(), "gpt-5.6-sol", "error still names the model that ran")
}

// SUCCESS (no regression) — a CLEAN exit carrying an error event keeps its
// existing contract: the message folds into Summary and RunAgent returns a
// result, not an error. Surfacing the cause on the failure path must not change
// the clean path.
func TestRunAgent_CleanExitWithErrorEventStillReturnsResult(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		`{"type":"thread.started","thread_id":"thr-1"}`,
		`{"type":"turn.failed","error":{"message":"model refused"}}`,
	}}}}
	p := New(Config{Binding: codexBinding("gpt-5.6-sol", nil), BinaryPath: "codex", Runner: mr})

	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5.6-sol",
	})
	require.NoError(t, err, "a zero exit stays a success even when the turn failed")
	require.NotNil(t, res)
	assert.Equal(t, "model refused", res.Summary)
}

// FAILURE — a non-zero exit with NO failure event on stdout must report the bare
// exit status. The fix must not invent a cause from ordinary assistant text.
func TestRunAgent_NonZeroExitWithoutFailureEventStaysBare(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines: []string{
			`{"type":"thread.started","thread_id":"thr-1"}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":"Done: edited the workspace."}}`,
		},
		waitErr: errors.New("exit status 1"),
	}}}
	p := New(Config{Binding: codexBinding("gpt-5.6-sol", nil), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5.6-sol",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.NotContains(t, err.Error(), "Done: edited the workspace.",
		"assistant text is not a failure cause and must never be reported as one")
}

// ERROR — a non-zero exit whose stdout is garbled must still report the exit
// error. The best-effort cause scan must never mask the real failure with a
// parse error (the exit path does not fail closed on unreadable stdout).
func TestRunAgent_NonZeroExitWithMalformedStdoutStillReportsExit(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines:   []string{`{"type":"item.completed", BROKEN`},
		waitErr: errors.New("exit status 1"),
	}}}
	p := New(Config{Binding: codexBinding("gpt-5.6-sol", nil), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5.6-sol",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1", "the exit error must survive garbled stdout")
	assert.NotErrorIs(t, err, ErrParse, "a parse error must not displace the process exit error")
}

// EDGE — the underlying exit error stays unwrappable through the added cause, so
// errors.Is against an exec.ExitError (or any sentinel) keeps working for callers.
func TestRunAgent_NonZeroExitCauseKeepsWaitErrUnwrappable(t *testing.T) {
	sentinel := errors.New("exit status 1")
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines: []string{
			`{"type":"turn.failed","error":{"message":"You've hit your usage limit."}}`,
		},
		waitErr: sentinel,
	}}}
	p := New(Config{Binding: codexBinding("gpt-5.6-sol", nil), BinaryPath: "codex", Runner: mr})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5.6-sol",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "the wait error must remain in the chain via %w")
}

func TestRunAgent_ToolScopeNarrowsLaunchAndContextRidesArgv(t *testing.T) {
	rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
		"codex": {Backend: "codex", ContextTiers: []provider.ContextWindow{provider.Context200K}},
	})
	b := codexBinding("gpt-5-codex", nil)
	b.Backend, b.ContextWindow = "codex", provider.Context200K
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{`[{"name":"github"}]`}}, happyMission()}}
	p := New(Config{Binding: b, BinaryPath: "codex", Runner: mr, CapabilityRules: rules})

	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "inspect", WorkDir: "/tmp/ws", Model: "gpt-5-codex",
		ToolScope: []string{"Read", "Grep", "Glob", "Bash"}})
	require.NoError(t, err)
	require.Len(t, mr.calls, 3, "version probe, MCP enumeration, then exactly one launch")
	assert.Equal(t, []string{"features.plugins=false", "-c", "features.apps=false", "-c", `web_search="disabled"`, "-c", "sandbox_workspace_write.network_access=false", "mcp", "list", "--json"}, mr.calls[1].Args[1:])
	args := mr.calls[2].Args
	assert.Equal(t, []string{"exec", "--sandbox", "read-only", "-m", "gpt-5-codex"}, args[:5], "no write tool: read-only sandbox replaces the bypass posture")
	assert.NotContains(t, args, bypassFlag)
	for _, want := range []string{"--strict-config", "features.plugins=false", "features.apps=false", `web_search="disabled"`,
		`mcp_servers={"github"={enabled=false}}`, "model_context_window=200000"} {
		assert.Contains(t, args, want)
	}
}

func TestRunAgent_EmptyToolScopeKeepsTodaysArgv(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "x", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	assert.Equal(t, []string{"exec", bypassFlag, "-m", "gpt-5-codex", "--cd", "/tmp/ws", "--json"}, mr.calls[1].Args)
}

func TestRunAgent_UnenforceableScopeOrOldBinaryRefusesBeforeLaunch(t *testing.T) {
	for name, tc := range map[string]struct {
		probe runScript
		scope []string
	}{
		"no shell":      {compatibleProbe(), []string{"Read", "Write"}},
		"mcp tool":      {compatibleProbe(), []string{"Bash", "mcp__x__y"}},
		"codex too old": {runScript{lines: []string{"codex-cli 0.162.0"}}, []string{"Read", "Bash"}},
	} {
		t.Run(name, func(t *testing.T) {
			mr := &multiRunner{scripts: []runScript{tc.probe, happyMission()}}
			p := New(Config{Binding: codexBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
			_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "x", WorkDir: "/tmp/ws", Model: "gpt-5-codex", ToolScope: tc.scope})
			require.Error(t, err)
			if name != "codex too old" {
				require.ErrorIs(t, err, ErrToolScopeUnsupported)
			}
			for _, c := range mr.calls {
				assert.NotEqual(t, "exec", c.Args[0], "no codex exec launch on a refused scope")
			}
		})
	}
}
