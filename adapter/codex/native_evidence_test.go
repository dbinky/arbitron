package codex

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_PinnedSubscriptionReportsRolloutEvidence(t *testing.T) {
	run := func(t *testing.T, ran string) (*engine.AgentResult, error) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		mr := &multiRunner{scripts: []runScript{
			compatibleProbe(),
			{lines: execLines(rolloutThread, "ok", 1, 1), onWait: func() {
				writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine(ran))
			}},
		}}
		p := New(Config{BinaryPath: "/x/codex", Runner: mr, Binding: provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "codex", Model: "gpt-6.1-sol", Pinned: true}})
		return p.RunAgent(context.Background(), engine.AgentMission{Model: "gpt-6.1-sol", Prompt: "p", WorkDir: t.TempDir()})
	}
	res, err := run(t, "gpt-6.1-sol")
	require.NoError(t, err)
	assert.Equal(t, []engine.ModelEvidence{{Model: "gpt-6.1-sol", Source: "codex_rollout", Kind: engine.EvidenceReportedExecution}}, res.ModelEvidence)
	res, err = run(t, "gpt-fallback")
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.Nil(t, res)
}
