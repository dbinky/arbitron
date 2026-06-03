package claudecli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
)

func TestStreamAgentAdapterImplementsPort(t *testing.T) {
	var _ engine.ChatProvider = (*StreamAgentAdapter)(nil)
}

func TestStreamAgentAdapterChat(t *testing.T) {
	// Happy path — arg threading: skip-permissions present, no tool-suppression.
	t.Run("emits --dangerously-skip-permissions and not --allowedTools empty", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{
			BinaryPath:   "claude",
			AllowedTools: []string{"Read", "Bash"},
			Runner:       fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "do it", WorkDir: "/tmp/ws"})
		require.NoError(t, err)

		// Must contain --dangerously-skip-permissions.
		assert.Contains(t, fr.gotSpec.Args, "--dangerously-skip-permissions",
			"stream agent must always emit --dangerously-skip-permissions")

		// Must NOT suppress tools: --allowedTools "" must not appear.
		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		if ok {
			// If --allowedTools is present, it must not be the empty suppress-tools value.
			assert.NotEqual(t, "", val,
				"stream agent must not suppress tools with an empty --allowedTools set")
		}

		// WorkDir threads through to RunSpec.Dir.
		assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
	})

	// Happy path — AllowedTools from config are forwarded.
	t.Run("forwards configured AllowedTools to --allowedTools", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{
			BinaryPath:   "claude",
			AllowedTools: []string{"Read", "Bash"},
			Runner:       fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)

		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok, "--allowedTools must be present when AllowedTools configured")
		assert.Equal(t, "Read,Bash", val)
	})

	// PR #18 item 1b: a per-request ToolScope (from the resolved profile's
	// AllowedTools) takes precedence over the adapter's global AllowedTools, so
	// the emitted --allowedTools reflects the profile's declared scope. NOTE
	// (VERIFIED, PR #18): this scope is defense-in-depth / intent only — under
	// --dangerously-skip-permissions the CLI ignores --allowedTools. This test
	// pins the precedence/emission, not a runtime security guarantee.
	t.Run("per-request ToolScope overrides the adapter's global AllowedTools", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{
			BinaryPath:   "claude",
			AllowedTools: []string{"Read", "Write", "Bash"}, // global default
			Runner:       fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{
			Prompt:    "go",
			WorkDir:   "/tmp",
			ToolScope: []string{"Read", "Grep", "Glob", "Bash(gh:*)"},
		})
		require.NoError(t, err)

		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok)
		assert.Equal(t, "Read,Grep,Glob,Bash(gh:*)", val,
			"the per-request ToolScope must win over the adapter's global AllowedTools")
	})

	// SECURITY (PR #18 CRITICAL, comment 4614956545): with no AllowedTools in
	// config the agent path must FAIL CLOSED, emitting --allowedTools "" (the
	// empty set) rather than omitting the flag — an absent --allowedTools under
	// --dangerously-skip-permissions means "all tools" and is RCE-equivalent.
	t.Run("FAIL-CLOSED: empty config AllowedTools emits --allowedTools \"\" (no tools)", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{
			BinaryPath: "claude",
			Runner:     fr,
		})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)

		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok, "--allowedTools must be present (fail closed) when AllowedTools is empty")
		assert.Equal(t, "", val, "empty AllowedTools must fail closed to the empty set, never fall through to all-tools")
	})

	// Edge — WorkDir is forwarded to RunSpec.Dir.
	t.Run("WorkDir threads through to RunSpec.Dir", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp/mydir"})
		require.NoError(t, err)
		assert.Equal(t, "/tmp/mydir", fr.gotSpec.Dir)
	})

	// Error — empty WorkDir is rejected without invoking the runner.
	t.Run("empty WorkDir is rejected without invoking runner", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirRequired)
		assert.False(t, fr.runCalled)
	})
}

func TestStreamAgentAdapterStreamChat(t *testing.T) {
	// Happy path — streaming returns assistant text then Done chunk.
	t.Run("emits assistant text then a terminal Done chunk", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}`,
			`{"type":"result","is_error":false,"result":"hello world","session_id":"s1","usage":{"input_tokens":3,"output_tokens":5}}`,
		}}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks)
		var text string
		for _, c := range chunks {
			text += c.Token
		}
		assert.Equal(t, "hello world", text)

		last := chunks[len(chunks)-1]
		assert.True(t, last.Done)
		assert.Equal(t, "s1", last.SessionID)
		assert.Equal(t, 3, last.TokensIn)
		assert.Equal(t, 5, last.TokensOut)
	})

	// SECURITY (PR #18 CRITICAL, comment 4614956545): the stream path emits
	// skip-permissions and, with no configured AllowedTools, fails closed to
	// --allowedTools "" rather than the (RCE-equivalent) all-tools fall-through.
	t.Run("stream path emits --dangerously-skip-permissions and FAILS CLOSED to empty --allowedTools", func(t *testing.T) {
		fr := &fakeRunner{lines: []string{
			`{"type":"result","result":"ok","session_id":"s"}`,
		}}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.NoError(t, err)
		collectChunks(t, ch)

		assert.Contains(t, fr.gotSpec.Args, "--dangerously-skip-permissions")
		val, ok := argValue(fr.gotSpec.Args, "--allowedTools")
		require.True(t, ok, "--allowedTools must be present (fail closed) under skip-permissions with empty AllowedTools")
		assert.Equal(t, "", val, "empty AllowedTools must fail closed to the empty set, never fall through to all-tools")
	})

	// Edge — tool_use + tool_result frames in stream do not corrupt text or crash.
	t.Run("tool frames in stream do not corrupt text accumulation or crash", func(t *testing.T) {
		// Simulate: assistant text, then an assistant message with a tool_use
		// block (non-text content), then a tool_result (user event), then more
		// assistant text, then the final result.
		fr := &fakeRunner{lines: []string{
			`{"type":"system","subtype":"init","session_id":"s1"}`,
			// First text chunk.
			`{"type":"assistant","message":{"content":[{"type":"text","text":"before "}]}}`,
			// Tool-use block: assistant message with tool_use content (no text block).
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Read","input":{}}]}}`,
			// Tool result: user event carrying the tool result.
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"file contents"}]}}`,
			// Second text chunk after tool round-trip.
			`{"type":"assistant","message":{"content":[{"type":"text","text":"after"}]}}`,
			// Terminal result.
			`{"type":"result","is_error":false,"result":"before after","session_id":"s1","usage":{"input_tokens":10,"output_tokens":4}}`,
		}}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "use tool", WorkDir: "/tmp"})
		require.NoError(t, err)
		chunks := collectChunks(t, ch)

		require.NotEmpty(t, chunks, "must emit at least the Done chunk")

		// Assert no chunk carries an error.
		for _, c := range chunks {
			assert.NoError(t, c.Error, "tool frames must not produce error chunks")
		}

		// Assert assistant TEXT accumulated correctly (tool frames contribute no text).
		var text string
		for _, c := range chunks {
			text += c.Token
		}
		assert.Equal(t, "before after", text,
			"tool_use and tool_result frames must not contribute to text accumulation")

		// Assert terminal Done chunk.
		last := chunks[len(chunks)-1]
		assert.True(t, last.Done, "last chunk must be Done")
		assert.Equal(t, "s1", last.SessionID)
	})

	// Error — start failure returns error from StreamChat.
	t.Run("start failure returns an error from StreamChat", func(t *testing.T) {
		fr := &fakeRunner{startErr: errors.New("binary not found")}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
		require.Error(t, err)
	})

	// Error — empty WorkDir is rejected.
	t.Run("empty WorkDir is rejected", func(t *testing.T) {
		fr := &fakeRunner{}
		a := NewStreamAgentAdapter(Config{BinaryPath: "claude", Runner: fr})

		_, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrWorkDirRequired)
		assert.False(t, fr.runCalled)
	})
}
