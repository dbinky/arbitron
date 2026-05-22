package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestProviderCall(t *testing.T) {
	t.Run("happy path returns result text, session id, tokens", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`,
			`{"type":"result","is_error":false,"result":"final answer","session_id":"s1","usage":{"input_tokens":7,"output_tokens":3}}`,
		}}
		p := New("claude", "", 0, fr)

		resp, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "hi"})
		require.NoError(t, err)
		assert.Equal(t, "final answer", resp.Output)
		assert.Equal(t, "s1", resp.SessionID)
		assert.Equal(t, 7, resp.TokensIn)
		assert.Equal(t, 3, resp.TokensOut)
	})

	t.Run("prompt and workdir are passed to the runner", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "do it", WorkDir: "/tmp/ws"})
		require.NoError(t, err)
		assert.Equal(t, "do it", fr.gotSpec.Stdin)
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	t.Run("result event with is_error true returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model failure")
	})

	t.Run("missing result event returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no result event")
	})

	t.Run("process exit error is surfaced", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})

	t.Run("start failure is surfaced", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary not found")
	})

	t.Run("malformed stream-json line returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{`{bad json`}}
		p := New("claude", "", 0, fr)

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})
}

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.LLMProvider = (*Provider)(nil)
}
