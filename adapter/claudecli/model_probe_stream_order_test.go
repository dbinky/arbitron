package claudecli

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Accepting a second result would let duplicated terminal evidence publish Done.
// Exercise the public adapters so the binding, parser and finalizer stay real.
func TestModelProbeClaudeStreamingResultOrdering(t *testing.T) {
	const exact = `{"type":"result","result":"verified output","modelUsage":{"claude-sonnet-4-6":{},"claude-haiku-4-5":{}}}`
	const fallback = `{"type":"result","result":"fallback output","modelUsage":{"proof-secret":{}}}`
	const missing = `{"type":"result","result":"unverified output"}`
	const failed = `{"type":"result","is_error":true,"result":"proof-secret","modelUsage":{"claude-sonnet-4-6":{}}}`
	for _, adapterName := range []string{"chat", "agent", "deprecated-provider"} {
		for _, tc := range []struct {
			name    string
			lines   []string
			wantErr bool
		}{
			{"single exact with helper", []string{exact}, false},
			{"duplicate exact", []string{exact, exact}, true},
			{"exact then fallback", []string{exact, fallback}, true},
			{"fallback then exact", []string{fallback, exact}, true},
			{"missing then exact", []string{missing, exact}, true},
			{"failed then exact", []string{failed, exact}, true},
			{"duplicate then later output", []string{exact, exact, `{"type":"assistant","message":{"content":[{"type":"text","text":"must be ignored"}]}}`, exact}, true},
		} {
			t.Run(adapterName+"/"+tc.name, func(t *testing.T) {
				r := &modelEvidenceRunner{t: t, agent: adapterName == "agent", legacy: adapterName == "deprecated-provider"}
				r.lines = tc.lines
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
				assert.Empty(t, chunks[0].Token)
				if tc.wantErr {
					require.Error(t, chunks[0].Error)
					assert.NotContains(t, chunks[0].Error.Error(), "proof-secret")
					assert.False(t, chunks[0].Done)
				} else {
					require.NoError(t, chunks[0].Error)
					assert.True(t, chunks[0].Done)
				}
			})
		}
	}
}
