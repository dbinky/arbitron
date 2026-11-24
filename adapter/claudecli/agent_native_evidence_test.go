package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentAdapter_PinnedSubscriptionReportsExecutedModelEvidence(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "run")
	require.NoError(t, os.MkdirAll(work, 0o700))
	line := `{"type":"result","is_error":false,"result":"done","session_id":"s1","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-opus-5-5":{},"claude-haiku-4-5":{}}}`
	binding := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code", Model: "claude-opus-5-5"}
	pinned := binding
	pinned.Pinned = true
	r := &fakeRunner{lines: []string{line}}
	res, err := NewAgentAdapterForBinding(Config{BinaryPath: "claude", Runner: r, WorkspaceRoot: root}, pinned).RunAgent(context.Background(), engine.AgentMission{Model: "claude-opus-5-5", Prompt: "p", WorkDir: work, ToolScope: []string{"Read"}})
	require.NoError(t, err)
	assert.Equal(t, []engine.ModelEvidence{
		{Model: "claude-haiku-4-5", Source: "claude_model_usage", Kind: engine.EvidenceReportedExecution},
		{Model: "claude-opus-5-5", Source: "claude_model_usage", Kind: engine.EvidenceReportedExecution},
	}, res.ModelEvidence)
	r = &fakeRunner{lines: []string{line}}
	res, err = NewAgentAdapterForBinding(Config{BinaryPath: "claude", Runner: r, WorkspaceRoot: root}, binding).RunAgent(context.Background(), engine.AgentMission{Model: "claude-opus-5-5", Prompt: "p", WorkDir: work, ToolScope: []string{"Read"}})
	require.NoError(t, err)
	assert.Empty(t, res.ModelEvidence)
}
