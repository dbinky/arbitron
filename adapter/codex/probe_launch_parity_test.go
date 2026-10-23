package codex

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The usability probe is accepted only when it travels the launch path a real
// run uses. This pins every difference between the probe's `codex exec` launch
// and a pinned RunAgent's on the same binding (effort rules included): the
// recorded restrictions and identity-only stdin are the only differences;
// binary, env, cwd, model, effort and correlated evidence remain identical.
func TestUsabilityCodexProbeLaunchMatchesPinnedRunExceptRecordedRestrictions(t *testing.T) {
	home, workdir := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "must-not-reach-child")
	writeModelsCache(t, home, "gpt-5-sol")

	binding := codexBinding("gpt-5-sol", nil)
	binding.Backend = "codex"
	binding.Pinned = true
	binding.Effort = provider.EffortMedium
	launch := func(probe bool, run func(*Provider) error) claudecli.RunSpec {
		scripts := []runScript{compatibleProbe()}
		if probe {
			scripts = append(scripts, emptyMCPProbe())
		}
		scripts = append(scripts, runScript{
			lines: execLines(rolloutThread, "ok", 1, 1),
			onWait: func() {
				writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5-sol"))
			},
		})
		runner := &multiRunner{scripts: scripts}
		p := New(Config{Binding: binding, BinaryPath: "/opt/codex", Runner: runner, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20, CapabilityRules: testCapRules()})
		require.NoError(t, run(p))
		require.Len(t, runner.calls, len(scripts), "version/config probes, then the exec launch")
		return runner.calls[len(scripts)-1]
	}

	real := launch(false, func(p *Provider) error {
		_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "reply ok", WorkDir: workdir})
		return err
	})
	probe := launch(true, func(p *Provider) error {
		got, _, err := p.ProbeExecutedModel(context.Background(), "reply ok", workdir)
		assert.Equal(t, "gpt-5-sol", got)
		return err
	})

	require.Contains(t, real.Args, bypassFlag)
	require.Contains(t, real.Args, "model_reasoning_effort=medium", "the run carries the binding's effort")
	restrictions := []string{"--strict-config", "-c", "features.plugins=false", "-c", "features.apps=false", "-c", `web_search="disabled"`, "-c", "sandbox_workspace_write.network_access=false", "-c", "mcp_servers={}"}
	require.Greater(t, len(probe.Args), len(restrictions))
	assert.Equal(t, restrictions, probe.Args[len(probe.Args)-len(restrictions):])
	assert.Equal(t, real.Args, swapSandbox(probe.Args[:len(probe.Args)-len(restrictions)]))
	assert.NotContains(t, probe.Args, bypassFlag)
	assert.Equal(t, real.Bin, probe.Bin)
	assert.Equal(t, real.Env, probe.Env)
	assert.Equal(t, real.Dir, probe.Dir)
	assert.Equal(t, "reply ok", real.Stdin)
	assert.Equal(t, d10IdentityPrompt, probe.Stdin)
	assert.Equal(t, real.MaxResponseBytes, probe.MaxResponseBytes)
}

// swapSandbox rewrites a probe argv's `--sandbox read-only` pair back to the
// bypass flag so it can be compared with a real run's argv.
func swapSandbox(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == sandboxFlag && i+1 < len(args) && args[i+1] == readOnlySandbox {
			out = append(out, bypassFlag)
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}
