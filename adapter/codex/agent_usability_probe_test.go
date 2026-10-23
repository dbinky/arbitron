package codex

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func probeProvider(t *testing.T, runner claudecli.CommandRunner, pinned bool) *Provider {
	t.Helper()
	b := codexBinding("gpt-5-sol", nil)
	b.Pinned = pinned
	return New(Config{Binding: b, BinaryPath: "codex", Runner: runner, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20})
}

// The probe launches the same binary and subscription env a mission does, but
// under the read-only sandbox instead of the bypass posture, and trusts only the
// account's stored rollout for the model that ran.
func TestUsabilityCodexProbeConfirmsFromRollout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lines  []string
		stream []string
		want   string
		valid  bool
	}{
		{"exact", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, nil, "gpt-5-sol", true},
		{"silent fallback", []string{metaLine(rolloutThread), ctxLine("gpt-fallback")}, nil, "", false},
		{"missing rollout", nil, nil, "", false},
		{"missing model", []string{metaLine(rolloutThread), ctxLine("")}, nil, "", false},
		{"another session", []string{metaLine(rolloutThread2), ctxLine("gpt-5-sol")}, nil, "", false},
		{"fallback then exact", []string{metaLine(rolloutThread), ctxLine("gpt-fallback"), ctxLine("gpt-5-sol")}, nil, "", false},
		{"failed turn", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, []string{`{"type":"thread.started","thread_id":"` + rolloutThread + `"}`, `{"type":"turn.failed","error":{"message":"sk-secret refused"}}`}, "", false},
		{"incomplete turn", []string{metaLine(rolloutThread), ctxLine("gpt-5-sol")}, []string{`{"type":"thread.started","thread_id":"` + rolloutThread + `"}`}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, workdir := t.TempDir(), t.TempDir()
			t.Setenv("CODEX_HOME", home)
			t.Setenv("OPENAI_API_KEY", "must-not-reach-child")
			writeModelsCache(t, home, "gpt-5-sol")
			stream := tc.stream
			if stream == nil {
				stream = execLines(rolloutThread, "ok", 1, 1)
			}
			runner := &multiRunner{scripts: []runScript{compatibleProbe(), emptyMCPProbe(), {lines: stream, onWait: func() {
				if tc.lines != nil {
					writeRollout(t, home, rolloutThread, tc.lines...)
				}
			}}}}
			got, session, err := probeProvider(t, runner, true).ProbeExecutedModel(context.Background(), "reply ok", workdir)
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
				assert.Equal(t, rolloutThread, session, "proof carries the verified rollout's thread id")
			} else {
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.Empty(t, got)
				assert.Empty(t, session)
				assert.NotContains(t, err.Error(), "sk-secret")
			}
			require.Len(t, runner.calls, 3)
			spec := runner.calls[2]
			assert.Equal(t, []string{"exec", "--sandbox", "read-only", "-m", "gpt-5-sol", "--cd", workdir, "--json", "--strict-config", "-c", "features.plugins=false", "-c", "features.apps=false", "-c", `web_search="disabled"`, "-c", execNetworkOff, "-c", "mcp_servers={}"}, spec.Args)
			assert.NotContains(t, spec.Args, bypassFlag, "the probe never runs under the bypass posture")
			assert.Equal(t, d10IdentityPrompt, spec.Stdin)
			assert.Equal(t, workdir, spec.Dir)
			assert.Contains(t, spec.Env, "CODEX_HOME="+home)
			_, hasKey := envValue(spec.Env, "OPENAI_API_KEY")
			assert.False(t, hasKey)
		})
	}
}

func TestUsabilityCodexProbeRefusesBeforeLaunch(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, tc := range []struct {
		name    string
		model   string
		workDir string
		want    error
	}{
		{"empty work dir", "gpt-5-sol", "", ErrWorkDirRequired},
		{"empty model", "", "w", ErrModelRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &multiRunner{}
			b := provider.ResolvedBinding{Provider: "openai", Model: tc.model, Pinned: true}
			p := New(Config{Binding: b, BinaryPath: "codex", Runner: runner})
			_, _, err := p.ProbeExecutedModel(context.Background(), "reply ok", tc.workDir)
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, runner.calls, "nothing launches, not even the version probe")
		})
	}
}

func TestUsabilityCodexProbeLaunchAndExitFaultsAreEvidenceErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeModelsCache(t, home, "gpt-5-sol")
	for name, script := range map[string]runScript{
		"launch failure": {startErr: assert.AnError},
		"non-zero exit":  {lines: execLines(rolloutThread, "ok", 1, 1), waitErr: assert.AnError},
	} {
		t.Run(name, func(t *testing.T) {
			runner := &multiRunner{scripts: []runScript{compatibleProbe(), emptyMCPProbe(), script}}
			_, _, err := probeProvider(t, runner, true).ProbeExecutedModel(context.Background(), "reply ok", t.TempDir())
			require.ErrorIs(t, err, ErrModelEvidence)
			require.Len(t, runner.calls, 3, "the fault comes from the launched turn, not the account/config filters")
		})
	}
}
