package pi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.AgentProvider = (*Provider)(nil)
}

// piBinding is a ready-to-run anthropic binding.
func piBinding(model string, effort provider.Effort) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider:  provider.CanonicalAnthropicAPI,
		Backend:   "pi",
		Model:     model,
		Effort:    effort,
		RevealKey: func(context.Context) (string, error) { return "sk-ant-test", nil },
	}
}

func happyMission() runScript {
	return runScript{lines: []string{
		sessionLine,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"bash","arguments":{}}],"usage":{"input":50,"output":5,"totalTokens":55},"stopReason":"toolUse","timestamp":1}}`,
		assistantLine("Done: edited the workspace.", "stop", 1200, 45),
		`{"type":"agent_settled"}`,
	}}
}

func newProvider(mr *multiRunner, b provider.ResolvedBinding) *Provider {
	return New(Config{Binding: b, BinaryPath: "pi", Runner: mr})
}

func mission() engine.AgentMission {
	return engine.AgentMission{Prompt: "do the thing", WorkDir: "/tmp/ws", Model: "ignored"}
}

// HAPPY — probe passes, the mission runs, the JSON stream folds into an
// AgentResult, and the prompt is delivered via stdin (never argv).
func TestRunAgent_HappyPath(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := newProvider(mr, piBinding("anthropic/claude-sonnet-5", provider.EffortHigh))

	res, err := p.RunAgent(context.Background(), mission())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "Done: edited the workspace.", res.Summary)
	assert.Equal(t, "anthropic/claude-sonnet-5", res.Model, "result names the model that fed argv")
	assert.Equal(t, 1200, res.TokensIn)
	assert.Equal(t, 45, res.TokensOut)
	assert.Equal(t, "01a0c55a-44ef-71da-96be-a1ab13d85460", res.SessionID)
	require.Len(t, res.Steps, 2)
	assert.Equal(t, "tool_call", res.Steps[0].Kind)
	assert.Equal(t, "assistant", res.Steps[1].Kind)

	runSpec := mr.calls[1]
	assert.Equal(t, "do the thing", runSpec.Stdin, "prompt rides stdin")
	assert.Equal(t, "/tmp/ws", runSpec.Dir, "pi takes its working dir from the process cwd")
	for _, a := range runSpec.Args {
		assert.NotContains(t, a, "do the thing", "prompt must never reach argv")
	}
}

// FAILURE — THE defining pi behaviour: a failed turn still exits 0, with the
// cause only in the event stream. The adapter must fail the mission on the
// stream's verdict, never on the exit code, or every quota refusal would land
// as a silent empty success.
func TestRunAgent_TurnFailureOnZeroExitIsAnError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines: []string{
			sessionLine,
			`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"400 You're out of extra usage.","timestamp":1}}`,
		},
		waitErr: nil, // pi exits 0 even though the turn failed
	}}}
	p := newProvider(mr, piBinding("anthropic/claude-haiku-4-5", provider.EffortDefault))

	res, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err, "a failed turn must not be reported as success just because pi exited 0")
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrTurnFailed)
	assert.Contains(t, err.Error(), "You're out of extra usage.", "the provider's cause must reach the caller")
	assert.Contains(t, err.Error(), "claude-haiku-4-5", "error names the model that ran")
}

// FAILURE — a failure that states no message (an aborted turn) still fails the
// mission rather than returning an empty success.
func TestRunAgent_MessagelessTurnFailureStillFails(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		sessionLine,
		`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input":1,"output":0,"totalTokens":1},"stopReason":"aborted","timestamp":1}}`,
	}}}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	_, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTurnFailed)
}

// ERROR — a non-zero exit is still an error, and when the stream also states a
// cause that cause is surfaced alongside the exit status (the lesson the codex
// adapter learned the hard way).
func TestRunAgent_NonZeroExitSurfacesStreamCause(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines: []string{
			sessionLine,
			`{"type":"message_end","message":{"role":"assistant","content":[],"usage":{"input":0,"output":0,"totalTokens":0},"stopReason":"error","errorMessage":"provider refused","timestamp":1}}`,
		},
		waitErr: errors.New("exit status 1"),
	}}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	_, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
	assert.Contains(t, err.Error(), "provider refused")
}

// ERROR — malformed stdout fails closed with a typed parse error.
func TestRunAgent_MalformedOutputFailsClosed(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{`{"type":"message_end", BROKEN`}}}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	res, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrParse)
}

// INPUT-VALIDATION — an empty WorkDir is rejected before ANY launch, not even
// the version probe. An empty cmd.Dir would inherit the engine's launch
// directory, which holds arbitron.db and config.yaml.
func TestRunAgent_EmptyWorkDirLaunchesNothing(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "x", WorkDir: ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls, "no subprocess on an empty WorkDir")
}

// INPUT-VALIDATION — an empty model fails closed before any launch, so a
// mission never runs on pi's own default model.
func TestRunAgent_EmptyModelLaunchesNothing(t *testing.T) {
	mr := &multiRunner{}
	p := newProvider(mr, piBinding("", provider.EffortDefault))

	_, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Empty(t, mr.calls, "no subprocess (not even the probe) on an empty model")
}

// ERROR — an unknown provider fails closed before any launch: no key is
// revealed and pi never starts with a fallback credential.
func TestRunAgent_UnknownProviderLaunchesNothing(t *testing.T) {
	mr := &multiRunner{}
	b := piBinding("m", provider.EffortDefault)
	b.Provider = "not-a-provider"
	p := newProvider(mr, b)

	_, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Empty(t, mr.calls)
}

// ERROR — an incompatible binary fails closed and the mission never launches.
func TestRunAgent_IncompatibleBinary(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"0.0.1"}}}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	_, err := p.RunAgent(context.Background(), mission())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatiblePi)
	assert.Len(t, mr.calls, 1, "only the probe ran")
}

// EDGE — the version probe is memoized: a second mission does not re-probe, and
// a probe FAILURE is memoized too rather than silently retried.
func TestRunAgent_ProbeMemoized(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission(), happyMission()}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))
	for i := 0; i < 2; i++ {
		_, err := p.RunAgent(context.Background(), mission())
		require.NoError(t, err)
	}
	require.Len(t, mr.calls, 3)
	assert.Contains(t, mr.calls[0].Args, "--version")
	assert.NotContains(t, mr.calls[1].Args, "--version")

	bad := &multiRunner{scripts: []runScript{{lines: []string{"0.0.1"}}}}
	p2 := newProvider(bad, piBinding("m", provider.EffortDefault))
	_, e1 := p2.RunAgent(context.Background(), mission())
	_, e2 := p2.RunAgent(context.Background(), mission())
	require.ErrorIs(t, e1, ErrIncompatiblePi)
	require.ErrorIs(t, e2, ErrIncompatiblePi, "memoized probe error must persist")
	assert.Len(t, bad.calls, 1, "probe runs exactly once; the failure is cached")
}

// EDGE — a clean run that produced no assistant text is NOT an error: pi can
// legitimately end a turn with tool work and no closing message.
func TestRunAgent_EmptySummaryOnCleanTurnIsNotAnError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		sessionLine,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"t","name":"bash","arguments":{}}],"usage":{"input":7,"output":1,"totalTokens":8},"stopReason":"stop","timestamp":1}}`,
	}}}}
	p := newProvider(mr, piBinding("m", provider.EffortDefault))

	res, err := p.RunAgent(context.Background(), mission())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Empty(t, res.Summary)
	assert.Len(t, res.Steps, 1)
}
