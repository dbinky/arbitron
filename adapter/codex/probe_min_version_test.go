package codex

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An older Codex still runs ordinary missions (minCodexVersion), but cannot
// carry the D10 restriction recipe: the probe reports unknown before it lists
// MCP servers or launches any turn, and ordinary runs on that binary still work.
func TestProbeMinimumVersionRefusesOldCodexWithoutAffectingOrdinaryRuns(t *testing.T) {
	home, workdir := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeModelsCache(t, home, "gpt-5-sol")
	old := runScript{lines: []string{"codex-cli 0.162.0"}}
	newProvider := func(scripts ...runScript) (*Provider, *multiRunner) {
		runner := &multiRunner{scripts: append([]runScript{old}, scripts...)}
		return New(Config{Binding: codexBinding("gpt-5-sol", nil), BinaryPath: "/opt/codex", Runner: runner, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20}), runner
	}

	p, runner := newProvider()
	got, _, err := p.ProbeExecutedModel(context.Background(), "", workdir)
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.Empty(t, got)
	require.Len(t, runner.calls, 1, "only the version check ran: no config listing, no turn")
	assert.NotContains(t, err.Error(), "gpt-5-sol", "errors never echo the requested model's evidence")

	p, runner = newProvider(runScript{lines: execLines(rolloutThread, "ok", 1, 1)})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "reply ok", WorkDir: workdir})
	require.NoError(t, err, "ordinary runs only need minCodexVersion")
	assert.Equal(t, "ok", res.Summary)
	require.Len(t, runner.calls, 2)
	assert.Contains(t, runner.calls[1].Args, bypassFlag)
}
