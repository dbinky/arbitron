package claudecli

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProbeClaudeStreamingEvidenceErrorsAreSecretFree(t *testing.T) {
	for _, adapterName := range []string{"chat", "agent", "deprecated-provider"} {
		for _, failure := range []string{"wrong model", "failed result"} {
			t.Run(fmt.Sprintf("%s/%s", adapterName, failure), func(t *testing.T) {
				r := &modelEvidenceRunner{t: t, agent: adapterName == "agent", legacy: adapterName == "deprecated-provider"}
				result := `{"type":"result","result":"unaccepted output","modelUsage":{"proof-secret":{}}}`
				if failure == "failed result" {
					result = `{"type":"result","is_error":true,"result":"credential rejected: proof-secret","modelUsage":{"claude-sonnet-4-6":{}}}`
				}
				// A later plausible success must not replace the failed proof.
				r.lines = []string{result, `{"type":"result","result":"later output","modelUsage":{"claude-sonnet-4-6":{}}}`}
				cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read"}}
				binding := provider.ResolvedBinding{Model: proofModel, Pinned: true, RevealKey: func(context.Context) (string, error) { return "proof-secret", nil }}
				var adapter engine.ChatProvider = NewChatAdapterForBinding(cfg, binding)
				switch adapterName {
				case "agent":
					adapter = NewStreamAgentAdapterForBinding(cfg, binding)
				case "deprecated-provider":
					cfg.AllowedTools = nil
					adapter = New(cfg)
				}
				ch, err := adapter.StreamChat(context.Background(), engine.ChatRequest{
					Model: proofModel, RequireExecutedModel: true, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace",
				})
				require.NoError(t, err)
				chunks := collectChunks(t, ch)
				require.Len(t, chunks, 1)
				require.Error(t, chunks[0].Error)
				assert.NotContains(t, chunks[0].Error.Error(), "proof-secret")
				assert.NotContains(t, chunks[0].Error.Error(), "credential rejected")
				assert.False(t, chunks[0].Done)
				assert.Empty(t, chunks[0].Token)
				assert.True(t, r.runCalled)
			})
		}
	}
}
