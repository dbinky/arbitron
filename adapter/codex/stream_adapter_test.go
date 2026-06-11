package codex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

func TestStreamProviderImplementsPort(t *testing.T) {
	var _ engine.ChatProvider = (*StreamProvider)(nil)
}

// collectChunks drains a ChatChunk channel into a slice. Mirrors the claudecli
// stream-test helper.
func collectChunks(t *testing.T, ch <-chan engine.ChatChunk) []engine.ChatChunk {
	t.Helper()
	var out []engine.ChatChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

// streamBinding builds a subscription binding for the stream provider. Its
// RevealKey is a SENTINEL that records whether it was called — the codex stream
// path must NEVER reveal a key, so a recorded call is a security failure.
func streamBinding(model string, revealed *bool) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "openai",
		Model:    model,
		RevealKey: func(context.Context) (string, error) {
			if revealed != nil {
				*revealed = true
			}
			return "sk-should-never-be-revealed", nil
		},
	}
}

// streamHappyMission is the canned `codex exec --json` stream for a successful
// turn: thread/turn start, a command tool call, two incremental agent_message
// items, and a terminal turn.completed carrying usage. Verified against the real
// binary's event vocabulary (codex emits whole agent_message items, not token
// deltas, so each item is one ChatChunk token).
func streamHappyMission() runScript {
	return runScript{lines: []string{
		`{"type":"thread.started","thread_id":"thr-stream-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"echo hi"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"hello "}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"world"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":11,"output_tokens":5}}`,
	}}
}

// HAPPY (StreamChat) — incremental agent_message items arrive in order as
// ChatChunk tokens, followed by a terminal Done chunk carrying the thread id and
// token counts. Tool-call items contribute no token (no assistant text) and do
// not corrupt accumulation.
func TestStreamChat_HappyPath(t *testing.T) {
	revealed := false
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), streamHappyMission()}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", &revealed), BinaryPath: "codex", Runner: mr})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "edit hello.txt", WorkDir: "/tmp/ws", Model: "gpt-5-codex",
	})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)
	require.NotEmpty(t, chunks)

	// Ordered token accumulation excludes the command item.
	var text string
	for _, c := range chunks {
		require.NoError(t, c.Error, "no chunk may carry an error on the happy path")
		text += c.Token
	}
	assert.Equal(t, "hello world", text, "agent_message items stream in order; tool items add no text")

	last := chunks[len(chunks)-1]
	assert.True(t, last.Done, "the terminal chunk must be Done")
	assert.Equal(t, "thr-stream-1", last.SessionID, "codex thread id surfaced on the terminal chunk")
	assert.Equal(t, 11, last.TokensIn)
	assert.Equal(t, 5, last.TokensOut)

	// SECURITY: the subscription path never reveals a key.
	assert.False(t, revealed, "codex (subscription) stream must NEVER call binding.RevealKey")
}

// SECURITY — the launched stream env carries NO OPENAI_API_KEY and preserves
// HOME (so codex resolves its on-disk ~/.codex/auth.json subscription
// credential). Prompt is delivered via stdin, never argv.
func TestStreamChat_EnvAndStdinInvariants(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-be-stripped")
	t.Setenv("HOME", "/home/codex-user")
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), streamHappyMission()}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{
		Prompt: "edit hello.txt to say goodbye", WorkDir: "/tmp/ws", Model: "gpt-5-codex",
	})
	require.NoError(t, err)
	collectChunks(t, ch)

	require.GreaterOrEqual(t, len(mr.calls), 2, "probe then run")
	runSpec := mr.calls[1]
	assert.Equal(t, execSubcommand, runSpec.Args[0])
	assert.Equal(t, "/tmp/ws", runSpec.Dir, "WorkDir is the cwd pin")
	assert.Equal(t, "edit hello.txt to say goodbye", runSpec.Stdin, "prompt via stdin")
	for _, a := range runSpec.Args {
		assert.NotContains(t, a, "edit hello.txt", "prompt must never appear in argv")
	}

	_, hasKey := envValue(runSpec.Env, "OPENAI_API_KEY")
	assert.False(t, hasKey, "the launched codex stream env must carry NO OPENAI_API_KEY")
	home, ok := envValue(runSpec.Env, "HOME")
	require.True(t, ok, "HOME must be preserved in the stream env")
	assert.Equal(t, "/home/codex-user", home)
}

// INPUT-VALIDATION — empty WorkDir is rejected before any launch (not even the
// version probe).
func TestStreamChat_EmptyWorkDir(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), streamHappyMission()}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls, "no launch on empty WorkDir")
}

// INPUT-VALIDATION — empty model is rejected (fail-closed) before any launch.
func TestStreamChat_EmptyModelFailsClosed(t *testing.T) {
	mr := &multiRunner{}
	p := NewStreamProvider(Config{Binding: streamBinding("", nil), BinaryPath: "codex", Runner: mr})
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Empty(t, mr.calls, "no launch (not even the probe) on empty model")
}

// ERROR — an incompatible binary fails the probe; StreamChat returns the error
// before opening a channel, and the run never launches.
func TestStreamChat_IncompatibleBinary(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"codex-cli 0.0.1"}}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrIncompatibleCodex)
	assert.Len(t, mr.calls, 1, "only the probe ran; the run never launched")
}

// ERROR — a malformed mid-stream line closes the channel with a typed ErrParse
// chunk and NO terminal Done (no silent partial success).
func TestStreamChat_MalformedEventClosesWithError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		`{"type":"item.completed","item":{"type":"agent_message","text":"partial "}}`,
		`{"type":"item.completed", BROKEN`,
	}}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.Error(t, last.Error, "a malformed event must close the channel with an error chunk")
	assert.ErrorIs(t, last.Error, ErrParse)
	assert.False(t, last.Done, "a failed stream must NOT emit a Done chunk (no partial-success masquerade)")
}

// ERROR — a mid-stream runner (process exit) error closes the channel with an
// error chunk naming the failure and NO Done.
func TestStreamChat_RunnerExitErrorClosesWithError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {
		lines:   []string{`{"type":"turn.started"}`},
		waitErr: errors.New("exit status 1"),
	}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.Error(t, last.Error)
	assert.Contains(t, last.Error.Error(), "exit status 1")
	assert.False(t, last.Done)
}

// ERROR — a turn.failed event closes the channel with an error chunk carrying
// the failure message and NO Done (a failed turn is never a silent success).
func TestStreamChat_TurnFailedClosesWithError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"error","message":"model not supported for this account"}`,
		`{"type":"turn.failed","error":{"message":"model not supported for this account"}}`,
	}}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)

	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.Error(t, last.Error, "a failed turn must close the channel with an error chunk")
	assert.Contains(t, last.Error.Error(), "model not supported")
	assert.False(t, last.Done)
}

// ERROR — a start failure (binary not found) is returned synchronously from
// StreamChat (no channel).
func TestStreamChat_StartFailure(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {startErr: errors.New(`exec: "codex": not found`)}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.Contains(t, err.Error(), "not found")
}

// Chat (non-streaming) — runs to completion and returns the terminal result.
func TestStreamProviderChat_HappyPath(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), streamHappyMission()}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "world", resp.Output, "Output is the terminal agent_message text")
	assert.Equal(t, "gpt-5-codex", resp.Model)
	assert.Equal(t, "thr-stream-1", resp.SessionID)
	assert.Equal(t, 11, resp.TokensIn)
	assert.Equal(t, 5, resp.TokensOut)
}

// ERROR (Chat) — a turn.failed / error event returns a hard error from Chat,
// never a successful response. Mirrors TestStreamChat_TurnFailedClosesWithError
// for the non-streaming path: out.failed must be checked before returning OK.
func TestStreamProviderChat_TurnFailedReturnsError(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"error","message":"model not supported for this account"}`,
		`{"type":"turn.failed","error":{"message":"model not supported for this account"}}`,
	}}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.Error(t, err, "Chat must not return OK on a failed turn")
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "model not supported", "error must surface the failure message")
}

// Chat — empty WorkDir rejected before launch.
func TestStreamProviderChat_EmptyWorkDir(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), streamHappyMission()}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "", Model: "gpt-5-codex"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls)
}

// REGRESSION (shared decoder) — batch RunAgent and the stream Chat fold the SAME
// event fixture into consistent summaries/sessions/tokens. This proves the
// shared decodeLine + foldEvent primitives keep the two paths from diverging.
func TestStreamAndBatch_ConsistentFromSameFixture(t *testing.T) {
	fixture := streamHappyMission()

	// Batch path.
	batchRunner := &multiRunner{scripts: []runScript{compatibleProbe(), fixture}}
	batch := New(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: batchRunner})
	bres, err := batch.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)

	// Stream path (Chat) over the SAME fixture.
	streamRunner := &multiRunner{scripts: []runScript{compatibleProbe(), fixture}}
	stream := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: streamRunner})
	sres, err := stream.Chat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)

	assert.Equal(t, bres.Summary, sres.Output, "batch Summary and stream Output must match")
	assert.Equal(t, bres.SessionID, sres.SessionID, "session ids must match")
	assert.Equal(t, bres.TokensIn, sres.TokensIn)
	assert.Equal(t, bres.TokensOut, sres.TokensOut)
}

// ERROR — context cancel mid-stream closes the channel without a Done (the
// terminal chunk carries the cancellation error).
func TestStreamChat_ContextCancel(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := NewStreamProvider(Config{Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr})
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := p.StreamChat(ctx, engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	cancel()

	chunks := collectChunks(t, ch)
	for _, c := range chunks {
		assert.False(t, c.Done, "a cancelled stream must not emit a clean Done")
	}
}

// ERROR — a per-request Timeout that elapses closes the channel with an error
// chunk (no Done).
func TestStreamChat_Timeout(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	p := NewStreamProvider(Config{
		Binding: streamBinding("gpt-5-codex", nil), BinaryPath: "codex", Runner: mr,
		Timeout: 10 * time.Millisecond,
	})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-5-codex"})
	require.NoError(t, err)
	chunks := collectChunks(t, ch)
	for _, c := range chunks {
		assert.False(t, c.Done, "a timed-out stream must not emit a clean Done")
	}
}
