package claudecli

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D14: a pinned binding demands harness evidence on the stream lanes whatever
// the request says, exactly as the non-streaming Chat/RunAgent lanes already do.
func TestStreamChatPinnedBindingRequiresEvidence(t *testing.T) {
	pinned := provider.ResolvedBinding{Model: proofModel, Pinned: true}
	type streamer interface {
		StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error)
	}
	build := map[string]func(r CommandRunner) streamer{
		"chat": func(r CommandRunner) streamer {
			return NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, pinned)
		},
		"stream-agent": func(r CommandRunner) streamer {
			return NewStreamAgentAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, pinned)
		},
	}
	result := func(usage string) []string {
		return []string{`{"type":"result","result":"ok","session_id":"s","modelUsage":` + usage + `}`}
	}
	for name, mk := range build {
		t.Run(name+"/fallback without request flag is refused", func(t *testing.T) {
			fr := &fakeRunner{lines: result(`{"claude-opus-4-6":{}}`)}
			ch, err := mk(fr).StreamChat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "p", WorkDir: "/tmp"})
			require.NoError(t, err)
			out := collectChunks(t, ch)
			require.NotEmpty(t, out)
			for _, c := range out {
				assert.False(t, c.Done, "unverified fallback reached a clean Done")
			}
			assert.Error(t, out[len(out)-1].Error)
		})
		t.Run(name+"/exact model without request flag completes", func(t *testing.T) {
			fr := &fakeRunner{lines: result(`{"` + proofModel + `":{}}`)}
			ch, err := mk(fr).StreamChat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "p", WorkDir: "/tmp"})
			require.NoError(t, err)
			out := collectChunks(t, ch)
			assert.True(t, out[len(out)-1].Done)
		})
		t.Run(name+"/request model other than the pinned one never launches", func(t *testing.T) {
			for _, model := range []string{"claude-opus-4-6", ""} {
				fr := &fakeRunner{lines: result(`{"claude-opus-4-6":{}}`)}
				ch, err := mk(fr).StreamChat(context.Background(), engine.ChatRequest{Model: model, Prompt: "p", WorkDir: "/tmp"})
				require.ErrorIs(t, err, errModelExecutionEvidence)
				assert.Nil(t, ch)
				assert.False(t, fr.runCalled, "process started for model %q", model)
			}
		})
	}
}
