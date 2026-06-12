package claudecli

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
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

	// Error — configured Timeout cancels a hung runner; Chat returns an error
	// instead of hanging. Mirrors the equivalent test on AgentAdapter.
	t.Run("configured Timeout cancels a hung runner", func(t *testing.T) {
		br := &blockingRunner{}
		a := NewChatAdapter(Config{
			BinaryPath: "claude",
			Timeout:    20 * time.Millisecond,
			Runner:     br,
		})

		done := make(chan error, 1)
		go func() {
			_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
			done <- err
		}()

		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("Chat did not return after Timeout elapsed")
		}
		assert.NotNil(t, br.gotCtx)
		_, hasDeadline := br.gotCtx.Deadline()
		assert.True(t, hasDeadline, "a configured Timeout must set a context deadline on the runner ctx")
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

	// Error — configured Timeout cancels a hung StreamChat; the goroutine
	// returns and the channel closes instead of leaking forever.
	t.Run("configured Timeout cancels a hung StreamChat", func(t *testing.T) {
		br := &blockingRunner{}
		a := NewChatAdapter(Config{
			BinaryPath: "claude",
			Timeout:    20 * time.Millisecond,
			Runner:     br,
		})

		ch, err := a.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
		require.NoError(t, err)

		// The channel must close once the timeout fires; drain it and assert
		// the goroutine returned within a generous wall-clock margin.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range ch { //nolint:revive // intentional drain
			}
		}()

		select {
		case <-drained:
			// channel closed; goroutine returned — pass
		case <-time.After(2 * time.Second):
			t.Fatal("StreamChat goroutine did not return after Timeout elapsed")
		}
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

func TestChatAdapterStreamChat_NoGoroutineLeakWhenConsumerStops(t *testing.T) {
	// MF9 parity for ChatAdapter. The producer-side select{case out<-c:
	// case <-runCtx.Done():} in streamState.makeSendChunk must also hold for
	// the ChatAdapter.StreamChat path so that a consumer that bails mid-
	// stream cannot pin the producer goroutine (and therefore the subprocess).
	//
	// goleak.VerifyNone fails this test if any goroutine started here is
	// still running at cleanup — catching any regression in the non-blocking
	// send or context-propagation on the ChatAdapter path.
	defer goleak.VerifyNone(t)

	runner := newCtxAwareFakeRunner([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"a"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"b"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"c"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"d"}]}}`,
		`{"type":"result","result":"abcd","session_id":"s","usage":{"input_tokens":1,"output_tokens":1}}`,
	})
	a := NewChatAdapter(Config{BinaryPath: "claude", Runner: runner})

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := a.StreamChat(ctx, engine.ChatRequest{Prompt: "go", WorkDir: "/tmp"})
	require.NoError(t, err)

	// Read one chunk then cancel — the producer must observe the cancellation
	// and exit without the consumer draining the remainder.
	<-ch
	cancel()

	// Drain the channel so the goroutine can unblock and exit.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range ch { //nolint:revive // intentional drain
		}
	}()

	timeout := time.After(2 * time.Second)
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		// producer goroutine returned — pass
	case <-timeout:
		t.Fatal("producer goroutine did not return after consumer cancelled")
	}
}

// capability (Phase 3 chat parity) — NewChatAdapterForBinding wires the
// binding's axes so a chat-kind profile with 200k + high effort emits
// CLAUDE_CODE_DISABLE_1M_CONTEXT, CLAUDE_CODE_AUTO_COMPACT_WINDOW, and
// CLAUDE_CODE_EFFORT_LEVEL on the chat subprocess, just like the agent path.
func TestChatAdapterForBinding_EmitsContextAndEffort(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{
		Backend:       "claude-code",
		Model:         "opus-4.8",
		ContextWindow: provider.Context200K,
		Effort:        provider.EffortHigh,
	}
	fr := &fakeRunner{lines: []string{
		`{"type":"result","result":"ok","session_id":"s"}`,
	}}
	a := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: fr, CapabilityRules: rules}, binding)

	_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
	require.NoError(t, err)

	assertEnv(t, fr.gotSpec.Env, "CLAUDE_CODE_DISABLE_1M_CONTEXT", "1")
	assertEnv(t, fr.gotSpec.Env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200000")
	assertEnv(t, fr.gotSpec.Env, "CLAUDE_CODE_EFFORT_LEVEL", "high")
}

// capability (Phase 3 chat parity defaults) — NewChatAdapterForBinding with a
// default-tier, default-effort binding adds NO capability vars on the chat
// subprocess (defaults emit nothing, mirroring the agent path).
func TestChatAdapterForBinding_DefaultAxesAddNoVars(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "claude-code", Model: "opus-4.8"}
	fr := &fakeRunner{lines: []string{
		`{"type":"result","result":"ok","session_id":"s"}`,
	}}
	a := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: fr, CapabilityRules: rules}, binding)

	_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
	require.NoError(t, err)

	_, has := envValue(fr.gotSpec.Env, "CLAUDE_CODE_DISABLE_1M_CONTEXT")
	assert.False(t, has, "default tier must not emit context vars")
	_, has = envValue(fr.gotSpec.Env, "CLAUDE_CODE_EFFORT_LEVEL")
	assert.False(t, has, "default effort must not emit effort vars")
}

// back-compat — NewChatAdapter (no binding) still produces no capability vars
// even with rules wired: without a binding the axes are zero (= default) and
// defaults emit nothing. This guards against the fix accidentally breaking the
// subscription path.
func TestChatAdapter_NoBindingNoCapabilityVars(t *testing.T) {
	rules := testCapRules()
	fr := &fakeRunner{lines: []string{
		`{"type":"result","result":"ok","session_id":"s"}`,
	}}
	a := NewChatAdapter(Config{BinaryPath: "claude", Runner: fr, CapabilityRules: rules})

	_, err := a.Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: "/tmp"})
	require.NoError(t, err)

	_, has := envValue(fr.gotSpec.Env, "CLAUDE_CODE_DISABLE_1M_CONTEXT")
	assert.False(t, has, "no-binding NewChatAdapter must not emit context vars")
	_, has = envValue(fr.gotSpec.Env, "CLAUDE_CODE_EFFORT_LEVEL")
	assert.False(t, has, "no-binding NewChatAdapter must not emit effort vars")
}
