package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestChatAdapterImplementsPort(t *testing.T) {
	var _ engine.ChatProvider = (*ChatAdapter)(nil)
}

// argValue returns the value following flag in args, and whether flag was found.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

func TestChatAdapterChat(t *testing.T) {
	// Happy path.
	t.Run("returns result text, session id, tokens, model", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`,
			`{"type":"result","is_error":false,"result":"final answer","session_id":"s1","usage":{"input_tokens":7,"output_tokens":3}}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		resp, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "hi", WorkDir: "/tmp", Model: "claude-x"})
		require.NoError(t, err)
		assert.Equal(t, "final answer", resp.Output)
		assert.Equal(t, "s1", resp.SessionID)
		assert.Equal(t, 7, resp.TokensIn)
		assert.Equal(t, 3, resp.TokensOut)
		assert.Equal(t, "claude-x", resp.Model)
	})

	// Happy path — arg threading: tool-suppressed, no skip-permissions.
	t.Run("invokes claude tool-suppressed with no skip-permissions", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{
			BinaryPath:      "claude",
			SkipPermissions: true,                     // even if mis-set on config,
			AllowedTools:    []string{"Read", "Bash"}, // these must never leak into chat.
			Runner:          fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "do it", WorkDir: "/tmp/ws"})
		require.NoError(t, err)

		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok, "--allowedTools must be present (empty set)")
		assert.Equal(t, "", val, "chat must suppress tools with an empty allowed-tools set")
		assert.NotContains(t, fr.gotSpec.Args, "--dangerously-skip-permissions",
			"chat must never emit --dangerously-skip-permissions")
		assert.NotContains(t, fr.gotSpec.Args, "Read,Bash")

		assert.Equal(t, "do it", fr.gotSpec.Stdin)
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	// Failure — is_error result.
	t.Run("result event with is_error true returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model failure")
	})

	// Error — process exit non-zero.
	t.Run("process exit error is surfaced", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.Error(t, err)
	})

	// Error — binary not found / start failure.
	t.Run("start failure (binary not found) is surfaced", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "binary not found")
	})

	// Error — missing result event.
	t.Run("missing result event returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no result event")
	})

	// Edge — malformed stream-json line.
	t.Run("malformed stream-json line returns an error", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{bad json`,
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.Error(t, err)
	})

	// Edge — blank lines tolerated and empty assistant output is a valid response.
	t.Run("blank lines tolerated, empty output yields a valid response", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			``,
			`   `,
			`{"type":"result","is_error":false,"result":"","session_id":"s3"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		resp, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.NoError(t, err)
		assert.Equal(t, "", resp.Output)
		assert.Equal(t, "s3", resp.SessionID)
	})

	// Edge — WorkDir invariants.
	t.Run("empty WorkDir is rejected without invoking the runner", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirRequired)
		assert.False(t, fr.runCalled)
	})

	t.Run("WorkDir outside WorkspaceRoot is rejected", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewChatAdapter(Config{
			BinaryPath:    "claude",
			WorkspaceRoot: "/var/lib/arbitron/workspaces",
			Runner:        fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/etc"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirEscapesRoot)
		assert.False(t, fr.runCalled)
	})
}

func TestChatAdapterStreamChat(t *testing.T) {
	// Success — streaming assistant text then a terminal Done chunk.
	t.Run("emits assistant text then a terminal chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part one "}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"part two"}]}}`,
			`{"type":"result","is_error":false,"result":"part one part two","session_id":"s1","usage":{"input_tokens":4,"output_tokens":9}}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
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

	// Success — arg threading on the streaming path too.
	t.Run("stream path is tool-suppressed with no skip-permissions", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", SkipPermissions: true, Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		collectChunks(t, ch)

		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok)
		assert.Equal(t, "", val)
		assert.NotContains(t, fr.gotSpec.Args, "--dangerously-skip-permissions")
	})

	// Failure — is_error result yields an error chunk.
	t.Run("result event with is_error true yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","is_error":true,"result":"model failure","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		require.NotEmpty(t, chunks)
		require.Error(t, chunks[len(chunks)-1].Error)
		assert.Contains(t, chunks[len(chunks)-1].Error.Error(), "model failure")
	})

	// Error — process exit yields an error chunk.
	t.Run("process exit error yields an error chunk", func(t *testing.T) {
		fr := &fakeRunner{
			lines:   []string{`{"type":"result","result":"ok","session_id":"s"}`},
			waitErr: errors.New("exit status 1"),
		}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	// Error — start failure returns an error from StreamChat itself.
	t.Run("start failure returns an error from StreamChat", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("no binary")}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.Error(t, err)
	})

	// Edge — malformed line yields an error chunk and still drains.
	t.Run("malformed line yields an error chunk and still drains", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{bad`,
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)
		require.NotEmpty(t, chunks)
		assert.Error(t, chunks[len(chunks)-1].Error)
	})

	// Edge — empty WorkDir rejected before invoking the runner.
	t.Run("empty WorkDir is rejected", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirRequired)
		assert.False(t, fr.runCalled)
	})
}
