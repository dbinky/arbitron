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
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

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
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "do it", WorkDir: "/tmp/ws"})
		require.NoError(t, err)
		assert.Equal(t, "do it", fr.gotSpec.Stdin)
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	t.Run("result event with is_error true returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model failure")
	})

	t.Run("missing result event returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no result event")
	})

	t.Run("process exit error is surfaced", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})

	t.Run("start failure is surfaced", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary not found")
	})

	t.Run("malformed stream-json line returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{bad json`,
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.Call(context.Background(), engine.LLMRequest{Prompt: "x"})
		require.Error(t, err)
	})
}

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.LLMProvider = (*Provider)(nil)
}

func collectChunks(t *testing.T, ch <-chan engine.LLMStreamChunk) []engine.LLMStreamChunk {
	t.Helper()
	var out []engine.LLMStreamChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestProviderStreamCall(t *testing.T) {
	t.Run("emits assistant text then a terminal chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part one "}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part two"}]}}`,
			`{"type":"result","is_error":false,"result":"part one part two","session_id":"s1","usage":{"input_tokens":4,"output_tokens":9}}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks)
		var text string
		for _, c := range chunks {
			text += c.Token
		}
		assert.Equal(t, "part one part two", text)

		last := chunks[len(chunks)-1]
		assert.True(t, last.Done)
		assert.Equal(t, "s1", last.SessionID)
		assert.Equal(t, 4, last.TokensIn)
		assert.Equal(t, 9, last.TokensOut)
	})

	t.Run("captures session id even without a system event", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"done","session_id":"s2"}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		assert.Equal(t, "s2", chunks[len(chunks)-1].SessionID)
	})

	t.Run("malformed line yields an error chunk and still drains", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{bad`,
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	t.Run("result event with is_error true yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		require.NotEmpty(t, chunks)
		last := chunks[len(chunks)-1]
		require.Error(t, last.Error)
		assert.Contains(t, last.Error.Error(), "model failure")
	})

	t.Run("process exit error yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	t.Run("start failure returns an error from StreamCall itself", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("no binary")}
		p := New(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		_, err := p.StreamCall(context.Background(), engine.LLMRequest{Prompt: "go"})
		require.Error(t, err)
	})
}
