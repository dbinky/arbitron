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

// A turn may carry several results: Claude Code re-wakes a print session when a
// background subagent finishes, emitting one result per wake. Every result must
// independently prove the pinned model; any unproven, fallback or failed result
// fails the turn. Exercise the public adapters so the binding, parser and finalizer stay real.
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
			{"duplicate exact", []string{exact, exact}, false},
			{"exact then fallback", []string{exact, fallback}, true},
			{"fallback then exact", []string{fallback, exact}, true},
			{"missing then exact", []string{missing, exact}, true},
			{"failed then exact", []string{failed, exact}, true},
			{"exact then failed", []string{exact, failed}, true},
			{"exact then missing", []string{exact, missing}, true},
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

// Regression: instance eab17ff4 (2026-11-10). An interactive_agent turn launched
// two background Explore subagents; each completion re-woke the session and
// emitted another proven result. The turn's text spans every wake, and the
// final wake carries the real answer, so all of it must reach the consumer.
func TestModelProbeClaudeStreamingBackgroundWakes(t *testing.T) {
	text := func(s string) string {
		return `{"type":"assistant","session_id":"s1","message":{"content":[{"type":"text","text":"` + s + `"}]}}`
	}
	result := func(in, out int) string {
		return fmt.Sprintf(`{"type":"result","session_id":"s1","result":"r","usage":{"input_tokens":%d,"output_tokens":%d},"modelUsage":{"claude-sonnet-4-6":{}}}`, in, out)
	}
	for _, adapterName := range []string{"chat", "agent"} {
		t.Run(adapterName, func(t *testing.T) {
			r := &modelEvidenceRunner{t: t, agent: adapterName == "agent"}
			r.lines = []string{
				text("launching agents"), result(10, 1),
				text("first agent back"), result(20, 2),
				text("the real question"), result(30, 3),
			}
			cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read"}}
			binding := provider.ResolvedBinding{Model: proofModel, Pinned: true, RevealKey: func(context.Context) (string, error) { return "proof-secret", nil }}
			var adapter engine.ChatProvider = NewChatAdapterForBinding(cfg, binding)
			if adapterName == "agent" {
				adapter = NewStreamAgentAdapterForBinding(cfg, binding)
			}
			ch, err := adapter.StreamChat(context.Background(), engine.ChatRequest{
				Model: proofModel, RequireExecutedModel: true, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace",
			})
			require.NoError(t, err)
			chunks := collectChunks(t, ch)
			var tokens []string
			for _, c := range chunks[:len(chunks)-1] {
				require.NoError(t, c.Error)
				tokens = append(tokens, c.Token)
			}
			assert.Equal(t, []string{"launching agents", "first agent back", "the real question"}, tokens)
			last := chunks[len(chunks)-1]
			require.NoError(t, last.Error)
			assert.True(t, last.Done)
			assert.Equal(t, "s1", last.SessionID)
			assert.Equal(t, 60, last.TokensIn, "usage sums across wakes")
			assert.Equal(t, 6, last.TokensOut, "usage sums across wakes")
		})
	}
}
