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

func TestModelProbeClaudeModelSuffix(t *testing.T) {
	for _, tc := range []struct {
		name, requested, actual string
		matches                 bool
	}{
		{"exact", proofModel, proofModel, true},
		{"actual context window", proofModel, proofModel + "[1m]", true},
		{"requested context window", proofModel + "[1m]", proofModel, true},
		{"both context window", proofModel + "[1m]", proofModel + "[1m]", true},
		{"unsupported actual", proofModel, proofModel + "[proof-secret]", false},
		{"unterminated actual", proofModel, proofModel + "[1m", false},
		{"trailing actual", proofModel, proofModel + "[1m]-fallback", false},
		{"repeated actual", proofModel, proofModel + "[1m][1m]", false},
		{"unsupported requested", proofModel + "[proof-secret]", proofModel, false},
		{"unterminated requested", proofModel + "[1m", proofModel, false},
		{"trailing requested", proofModel + "[1m]-fallback", proofModel, false},
		{"repeated requested", proofModel + "[1m][1m]", proofModel, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.matches, executedModelMatches(tc.requested, []string{"claude-haiku-4-5", tc.actual}))
		})
	}
}

func TestModelProbeClaudeStreamingModelSuffix(t *testing.T) {
	for _, adapterName := range []string{"chat", "agent", "deprecated-provider"} {
		for _, tc := range []struct {
			name, suffix string
			wantErr      bool
		}{
			{"exact", "", false},
			{"context window", "[1m]", false},
			{"unsupported", "[proof-secret]", true},
			{"unterminated", "[1m", true},
			{"trailing data", "[1m]-fallback", true},
			{"repeated", "[1m][1m]", true},
		} {
			t.Run(adapterName+"/"+tc.name, func(t *testing.T) {
				r := &modelEvidenceRunner{t: t, agent: adapterName == "agent", legacy: adapterName == "deprecated-provider"}
				r.lines = []string{fmt.Sprintf(`{"type":"result","result":"verified output","session_id":"suffix-session","modelUsage":{%q:{},"claude-haiku-4-5":{}}}`, proofModel+tc.suffix)}
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
				if tc.wantErr {
					require.Error(t, chunks[0].Error)
					assert.NotContains(t, chunks[0].Error.Error(), "proof-secret")
					assert.False(t, chunks[0].Done)
				} else {
					require.NoError(t, chunks[0].Error)
					assert.True(t, chunks[0].Done)
					assert.Equal(t, "suffix-session", chunks[0].SessionID)
				}
				assert.True(t, r.runCalled)
			})
		}
	}
}
