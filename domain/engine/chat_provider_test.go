package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChat is an in-package fake satisfying engine.ChatProvider. It lets us
// exercise the ChatProvider port surface without a real subprocess-backed
// adapter (mirroring the fakeAgent pattern in agent_provider_test.go).
type fakeChat struct {
	chatFn       func(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error)
	streamChatFn func(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error)
}

func (f *fakeChat) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	return f.chatFn(ctx, req)
}

func (f *fakeChat) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if f.streamChatFn != nil {
		return f.streamChatFn(ctx, req)
	}
	ch := make(chan engine.ChatChunk)
	close(ch)
	return ch, nil
}

// Compile-time assertion: a fake can satisfy the new ChatProvider port.
var _ engine.ChatProvider = (*fakeChat)(nil)

// Happy path: a fake implementing engine.ChatProvider is accepted through the
// port and Chat returns a populated response for a normal request.
func TestChatProvider_HappyPath(t *testing.T) {
	var p engine.ChatProvider = &fakeChat{
		chatFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{
				Output:    "the answer",
				Model:     req.Model,
				TokensIn:  10,
				TokensOut: 20,
			}, nil
		},
	}

	resp, err := p.Chat(context.Background(), engine.ChatRequest{
		Model:  "claude-sonnet-4-6",
		Prompt: "what is 2+2?",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "the answer", resp.Output)
	assert.Equal(t, "claude-sonnet-4-6", resp.Model)
	assert.Equal(t, 10, resp.TokensIn)
	assert.Equal(t, 20, resp.TokensOut)
}

// Success: a full ChatRequest round-trips all fields into the fake and a full
// ChatResponse round-trips all fields back, including SessionID.
func TestChatProvider_RoundTripsAllFields(t *testing.T) {
	req := engine.ChatRequest{
		Model:        "gpt-4o",
		SystemPrompt: "You are a helpful assistant.",
		Prompt:       "summarize this",
		MaxTokens:    1024,
		Temperature:  0.7,
		OutputSchema: map[string]any{"type": "object"},
		WorkDir:      "/workspace/inst-42",
		SessionID:    "sess-99",
	}

	var captured engine.ChatRequest
	p := &fakeChat{
		chatFn: func(_ context.Context, r engine.ChatRequest) (*engine.ChatResponse, error) {
			captured = r
			return &engine.ChatResponse{
				Output:    map[string]any{"summary": "done"},
				Model:     r.Model,
				TokensIn:  100,
				TokensOut: 50,
				SessionID: r.SessionID,
			}, nil
		},
	}

	resp, err := p.Chat(context.Background(), req)
	require.NoError(t, err)

	// All request fields propagate unchanged.
	assert.Equal(t, req, captured)

	// All response fields are readable.
	assert.Equal(t, map[string]any{"summary": "done"}, resp.Output)
	assert.Equal(t, "gpt-4o", resp.Model)
	assert.Equal(t, 100, resp.TokensIn)
	assert.Equal(t, 50, resp.TokensOut)
	assert.Equal(t, "sess-99", resp.SessionID)
}

// Failure: a domain-level failure from the chat backend (non-nil error, nil
// result) surfaces through the port to the caller.
func TestChatProvider_FailurePropagates(t *testing.T) {
	wantErr := errors.New("context length exceeded")
	p := &fakeChat{
		chatFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return nil, wantErr
		},
	}

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, resp)
}

// Error: a cancelled context is honoured — the port forwards the cancellation
// to the caller rather than swallowing it.
func TestChatProvider_ContextCancellation(t *testing.T) {
	p := &fakeChat{
		chatFn: func(ctx context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return nil, ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := p.Chat(ctx, engine.ChatRequest{Prompt: "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, resp)
}

// Edge case: zero-value ChatRequest/ChatResponse/ChatChunk shapes are valid
// and carry their zero-value defaults — no panics, no hidden requirements.
func TestChatProvider_ZeroValueShapes(t *testing.T) {
	t.Run("zero-value ChatRequest", func(t *testing.T) {
		var r engine.ChatRequest
		assert.Empty(t, r.Model)
		assert.Empty(t, r.SystemPrompt)
		assert.Empty(t, r.Prompt)
		assert.Equal(t, 0, r.MaxTokens)
		assert.Equal(t, float64(0), r.Temperature)
		assert.Nil(t, r.OutputSchema)
		assert.Empty(t, r.WorkDir)
		assert.Empty(t, r.SessionID)
	})

	t.Run("zero-value ChatResponse", func(t *testing.T) {
		var r engine.ChatResponse
		assert.Nil(t, r.Output)
		assert.Nil(t, r.Conversation)
		assert.Empty(t, r.Model)
		assert.Equal(t, 0, r.TokensIn)
		assert.Equal(t, 0, r.TokensOut)
		assert.Empty(t, r.SessionID)
	})

	t.Run("zero-value ChatChunk", func(t *testing.T) {
		var c engine.ChatChunk
		assert.Empty(t, c.Token)
		assert.False(t, c.Done)
		assert.Nil(t, c.Error)
		assert.Equal(t, 0, c.TokensIn)
		assert.Equal(t, 0, c.TokensOut)
		assert.Empty(t, c.SessionID)
	})
}

// --- StreamChat port contract ------------------------------------------------

// Success: StreamChat emits token chunks and a terminal Done chunk over the
// returned channel; each chunk carries the Token string.
func TestChatProvider_StreamChat_EmitsChunks(t *testing.T) {
	tokens := []string{"hello", " ", "world"}
	p := &fakeChat{
		chatFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("Chat should not be called in a streaming test")
			return nil, nil
		},
		streamChatFn: func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, len(tokens)+1)
			for _, tok := range tokens {
				ch <- engine.ChatChunk{Token: tok}
			}
			ch <- engine.ChatChunk{Done: true, TokensIn: 5, TokensOut: 3, SessionID: "sess-stream"}
			close(ch)
			return ch, nil
		},
	}

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var collected []string
	var terminal engine.ChatChunk
	for chunk := range ch {
		if chunk.Done {
			terminal = chunk
		} else {
			collected = append(collected, chunk.Token)
		}
	}

	assert.Equal(t, tokens, collected)
	assert.True(t, terminal.Done)
	assert.Equal(t, 5, terminal.TokensIn)
	assert.Equal(t, 3, terminal.TokensOut)
	assert.Equal(t, "sess-stream", terminal.SessionID)
}

// Error: StreamChat returns an error (startup failure — bad auth, unreachable
// endpoint) before any channel is opened. The function return is the only
// error mechanism in this case; `ch` is nil.
func TestChatProvider_StreamChat_ErrorReturn(t *testing.T) {
	wantErr := errors.New("unauthorized")
	p := &fakeChat{
		streamChatFn: func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			return nil, wantErr
		},
	}

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, ch)
}

// Error (mid-stream): ChatProvider.StreamChat opens a channel successfully but
// then delivers a chunk with Error set (e.g. rate-limit hit mid-response,
// connection reset). This is the second error channel in the streaming
// contract. Consumers MUST range over the channel and inspect chunk.Error on
// every chunk, not just check the function's return error.
//
// This documents the dual-error contract for future adapter authors:
//   - startup failure  → (nil, error) from StreamChat itself
//   - mid-stream error → (ch, nil) from StreamChat; chunk.Error ≠ nil in ch
func TestChatProvider_StreamChat_MidStreamError(t *testing.T) {
	wantErr := errors.New("rate limit hit mid-stream")
	p := &fakeChat{
		streamChatFn: func(_ context.Context, _ engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, 2)
			ch <- engine.ChatChunk{Token: "partial output"}
			ch <- engine.ChatChunk{Error: wantErr}
			close(ch)
			return ch, nil // no startup error; the channel IS valid
		},
	}

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err, "channel opened successfully — no startup error")
	require.NotNil(t, ch)

	var partial string
	var midErr error
	for chunk := range ch {
		if chunk.Error != nil {
			midErr = chunk.Error
		}
		if chunk.Token != "" {
			partial += chunk.Token
		}
	}

	assert.Equal(t, "partial output", partial, "tokens before the error were delivered")
	require.Error(t, midErr)
	assert.ErrorIs(t, midErr, wantErr)
}

// --- ChatProviderSelector port tests ----------------------------------------

// Happy path: ChatProviderSelector maps a binding to the expected ChatProvider
// and the provider is callable through the returned interface.
func TestChatProviderSelector_HappyPath(t *testing.T) {
	wantProvider := &fakeChat{
		chatFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: "ok"}, nil
		},
	}
	sel := chatSelector(wantProvider)

	got, err := sel.ChatProviderFor(provider.ResolvedBinding{
		Kind:     provider.ProfileKindChat,
		Provider: "claude-cli",
		Model:    "claude-sonnet-4-6",
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	resp, err := got.Chat(context.Background(), engine.ChatRequest{Model: "claude-sonnet-4-6", Prompt: "p"})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Output)
}

// Failure: ChatProviderSelector returns an error for an unsupported provider
// string; the caller gets the error, not a nil provider.
func TestChatProviderSelector_ErrorPath(t *testing.T) {
	wantErr := errors.New("no chat adapter for provider: unknown-llm")
	sel := staticChatSelector{err: wantErr}

	got, err := sel.ChatProviderFor(provider.ResolvedBinding{
		Kind:     provider.ProfileKindChat,
		Provider: "unknown-llm",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}

// Edge case: ChatProviderSelector with a zero-value ResolvedBinding does not
// panic — the static fake returns the provider regardless of binding content.
func TestChatProviderSelector_ZeroValueBinding(t *testing.T) {
	p := &fakeChat{
		chatFn: func(_ context.Context, _ engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{}, nil
		},
	}
	sel := chatSelector(p)

	got, err := sel.ChatProviderFor(provider.ResolvedBinding{})
	require.NoError(t, err)
	require.NotNil(t, got)
}

// --- AgentProviderSelector port tests ---------------------------------------

// Happy path: AgentProviderSelector maps a binding to the expected AgentProvider
// and the provider is callable through the returned interface.
func TestAgentProviderSelector_HappyPath(t *testing.T) {
	wantProvider := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{Summary: "done"}, nil
		},
	}
	sel := agentSelector(wantProvider)

	got, err := sel.AgentProviderFor(provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: "claude-cli",
		Model:    "claude-opus-4-8",
	})

	require.NoError(t, err)
	require.NotNil(t, got)
	res, err := got.RunAgent(context.Background(), engine.AgentMission{Model: "claude-opus-4-8", Prompt: "p"})
	require.NoError(t, err)
	assert.Equal(t, "done", res.Summary)
}

// Failure: AgentProviderSelector returns an error for an unsupported provider
// string; the caller gets the error, not a nil provider.
func TestAgentProviderSelector_ErrorPath(t *testing.T) {
	wantErr := errors.New("no agent adapter for provider: opencode-unknown")
	sel := staticAgentSelector{err: wantErr}

	got, err := sel.AgentProviderFor(provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: "opencode-unknown",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}

// Edge case: AgentProviderSelector with a zero-value ResolvedBinding does not
// panic — the static fake returns the provider regardless of binding content.
func TestAgentProviderSelector_ZeroValueBinding(t *testing.T) {
	ap := &fakeAgent{
		runFn: func(_ context.Context, _ engine.AgentMission) (*engine.AgentResult, error) {
			return &engine.AgentResult{}, nil
		},
	}
	sel := agentSelector(ap)

	got, err := sel.AgentProviderFor(provider.ResolvedBinding{})
	require.NoError(t, err)
	require.NotNil(t, got)
}
