package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The process leaf checks the actual launch, then writes separate account
// evidence at process exit. Neither argv nor stdout supplies the model proof.
type codexEvidenceRunner struct {
	t *testing.T
	multiRunner
	home, workdir string
}

func (r *codexEvidenceRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.t.Helper()
	require.Equal(r.t, "codex", spec.Bin)
	require.Contains(r.t, spec.Env, "CODEX_HOME="+r.home)
	_, hasKey := envValue(spec.Env, "OPENAI_API_KEY")
	require.False(r.t, hasKey)
	if len(spec.Args) != 1 || spec.Args[0] != "--version" {
		require.Equal(r.t, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5-sol", "--cd", r.workdir, "--json"}, spec.Args)
		require.Equal(r.t, "reply ok", spec.Stdin)
		require.Equal(r.t, r.workdir, spec.Dir)
		require.EqualValues(r.t, 1<<20, spec.MaxResponseBytes)
		_, bounded := ctx.Deadline()
		require.True(r.t, bounded)
	}
	return r.multiRunner.Run(ctx, spec)
}

func TestModelProbeCodexExactExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lines  []string
		stream []string
		valid  bool
	}{
		{"exact", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, nil, true},
		{"fallback", []string{metaLine(rolloutThread), ctxLine("gpt-fallback")}, nil, false},
		{"missing rollout", nil, nil, false},
		{"missing model", []string{metaLine(rolloutThread), ctxLine("")}, nil, false},
		{"another session", []string{metaLine(rolloutThread2), ctxLine("gpt-5-sol")}, nil, false},
		{"request echo", []string{metaLine(rolloutThread), `{"type":"response_item","payload":{"model":"gpt-5-sol"}}`}, nil, false},
		{"fallback then exact", []string{metaLine(rolloutThread), ctxLine("gpt-fallback"), ctxLine("gpt-5-sol")}, nil, false},
		{"exact then fallback", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol"), ctxLine("gpt-fallback")}, nil, false},
		{"truncated evidence", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol"), `{"type":"turn_context","payload":`}, nil, false},
		{"contradictory session", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol"), metaLine(rolloutThread2)}, nil, false},
		{"whitespace model", []string{metaLine(rolloutThread), ctxLine(" gpt-5-sol ")}, nil, false},
		{"escaped type", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol"), `{"type":"turn_\u0063ontext","payload":{"model":"fallback"}}`}, nil, false},
		{"failed turn", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, []string{`{"type":"thread.started","thread_id":"` + rolloutThread + `"}`, `{"type":"turn.failed","error":{"message":"refused"}}`}, false},
		{"incomplete turn", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, []string{`{"type":"thread.started","thread_id":"` + rolloutThread + `"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, workdir := t.TempDir(), t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("OPENAI_API_KEY", "must-not-reach-child")
			stream := tc.stream
			if stream == nil {
				stream = execLines(rolloutThread, "ok", 1, 1)
			}
			runner := &codexEvidenceRunner{t: t, home: home, workdir: workdir, multiRunner: multiRunner{scripts: []runScript{
				compatibleProbe(), {lines: stream, onWait: func() {
					if tc.lines != nil {
						writeRollout(t, home, rolloutThread, tc.lines...)
					}
				}},
			}}}
			binding := codexBinding("gpt-5-sol", nil)
			binding.Pinned = true
			p := New(Config{Binding: binding, BinaryPath: "codex", Runner: runner, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20})
			result, err := p.RunAgent(context.Background(), engine.AgentMission{Model: binding.Model, Prompt: "reply ok", WorkDir: workdir})
			if tc.valid {
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "ok", result.Summary)
				assert.Equal(t, binding.Model, result.Model)
			} else {
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.Nil(t, result, "unverified output must not reach the result sink")
			}
			require.Len(t, runner.calls, 2)
		})
	}
}

func TestModelProbeCodexLegacyDoesNotRequireRollout(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "no-account"))
	runner := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: execLines(rolloutThread, "legacy", 1, 1)}}}
	result, err := New(Config{Binding: provider.ResolvedBinding{Model: "gpt-5-sol"}, Runner: runner}).RunAgent(context.Background(), engine.AgentMission{WorkDir: t.TempDir(), Prompt: "go"})
	require.NoError(t, err)
	assert.Equal(t, "legacy", result.Summary)
	_, err = os.Stat(filepath.Join(os.Getenv("CODEX_HOME"), "sessions"))
	require.True(t, os.IsNotExist(err))
}
