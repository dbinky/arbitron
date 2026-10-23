package probe

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type streamCall struct {
	binding domprovider.ResolvedBinding
	req     engine.ChatRequest
}

// fakeStream replays a fixed chunk script; open fails the StreamChat call itself.
type fakeStream struct {
	call   *streamCall
	chunks []engine.ChatChunk
	open   error
}

func (f fakeStream) Chat(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
	return nil, errors.New("batch chat must not be used by an interactive probe")
}
func (f fakeStream) StreamChat(_ context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	f.call.req = req
	if f.open != nil {
		return nil, f.open
	}
	out := make(chan engine.ChatChunk, len(f.chunks))
	for _, c := range f.chunks {
		out <- c
	}
	close(out)
	return out, nil
}

type fakeStreamSelector struct {
	call   *streamCall
	script func(domprovider.ResolvedBinding) fakeStream
	err    error
}

func (s fakeStreamSelector) StreamProviderFor(b domprovider.ResolvedBinding) (engine.ChatProvider, error) {
	s.call.binding = b
	if s.err != nil {
		return nil, s.err
	}
	f := s.script(b)
	f.call = s.call
	return f, nil
}

func interactiveChatPath() modelcatalog.ExecutionPath {
	p := usabilityPath()
	p.Mode = modelcatalog.ExecutionModeInteractive
	return p
}

func interactiveLaneFixture(t *testing.T, script func(domprovider.ResolvedBinding) fakeStream) (*InteractiveChatUsabilityLane, *streamCall) {
	t.Helper()
	call := &streamCall{}
	lane, err := NewInteractiveChatUsabilityLane(InteractiveChatLaneDeps{
		Streams: fakeStreamSelector{call: call, script: script},
		WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, model string) (domprovider.ResolvedBinding, error) {
			if model == "" || p.Binding.RevealKey == nil {
				return domprovider.ResolvedBinding{}, errors.New("not an inventory path")
			}
			b := p.Binding
			b.Model = model
			return b, nil
		},
	})
	require.NoError(t, err)
	return lane, call
}

func doneStream(domprovider.ResolvedBinding) fakeStream {
	return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}, {Done: true}}}
}

// Happy: the exact candidate spelling streams pinned with the executed-model
// requirement through the production stream selector; a clean Done confirms.
func TestUsabilityInteractiveChatLaneConfirmsOnVerifiedStream(t *testing.T) {
	lane, call := interactiveLaneFixture(t, doneStream)
	require.True(t, lane.Supports(interactiveChatPath()))
	res, err := lane.Check(context.Background(), interactiveChatPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5-20261001", res.NativeModel)
	assert.Equal(t, "chat-stream:anthropic-api:claude-sonnet-5-5-20261001", res.ProofRef)
	assert.NotContains(t, res.ProofRef, "sk-secret")
	assert.False(t, call.binding.Pinned, "RequireExecutedModel, not pin grammar, enforces probes")
	assert.Equal(t, "claude-sonnet-5-5-20261001", call.binding.Model)
	assert.True(t, call.req.RequireExecutedModel)
	assert.Equal(t, "claude-sonnet-5-5-20261001", call.req.Model)
	assert.Empty(t, call.req.ToolScope)
	assert.Empty(t, call.req.SessionID)
	assert.Greater(t, call.req.MaxTokens, 0)
	assert.NotEmpty(t, call.req.Prompt)
}

// Failure/error: anything but a clean terminal Done is unknown — an evidence
// error chunk, an error after tokens, a stream that ends without Done, an open
// failure, a selector refusal, a binding refusal, and a cancelled context.
func TestUsabilityInteractiveChatLaneUnverifiedStreamsNeverConfirm(t *testing.T) {
	evidence := errors.New("model execution evidence missing")
	for name, script := range map[string]func(domprovider.ResolvedBinding) fakeStream{
		"evidence error chunk": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Error: evidence}}}
		},
		"error after tokens": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}, {Error: evidence}}}
		},
		"error after done": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Done: true}, {Error: evidence}}}
		},
		"ends without done": func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}}}
		},
		"empty stream": func(domprovider.ResolvedBinding) fakeStream { return fakeStream{} },
		"open failure": func(domprovider.ResolvedBinding) fakeStream { return fakeStream{open: errors.New("dial refused")} },
	} {
		t.Run(name, func(t *testing.T) {
			lane, _ := interactiveLaneFixture(t, script)
			res, err := lane.Check(context.Background(), interactiveChatPath(), usabilityEntry())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
			assert.NotContains(t, err.Error(), "dial refused")
			assert.NotContains(t, err.Error(), evidence.Error())
			assert.Empty(t, res.ProofRef)
		})
	}
	t.Run("selector refusal", func(t *testing.T) {
		call := &streamCall{}
		lane, err := NewInteractiveChatUsabilityLane(InteractiveChatLaneDeps{
			Streams: fakeStreamSelector{call: call, err: errors.New("no stream adapter")},
			WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, m string) (domprovider.ResolvedBinding, error) {
				b := p.Binding
				b.Model = m
				return b, nil
			},
		})
		require.NoError(t, err)
		_, err = lane.Check(context.Background(), interactiveChatPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
	t.Run("binding refusal", func(t *testing.T) {
		lane, err := NewInteractiveChatUsabilityLane(InteractiveChatLaneDeps{
			Streams: fakeStreamSelector{call: &streamCall{}, script: doneStream},
			WithBinding: func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error) {
				return domprovider.ResolvedBinding{}, errors.New("unsupported route")
			},
		})
		require.NoError(t, err)
		_, err = lane.Check(context.Background(), interactiveChatPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
	t.Run("cancelled context", func(t *testing.T) {
		lane, _ := interactiveLaneFixture(t, func(domprovider.ResolvedBinding) fakeStream {
			return fakeStream{chunks: []engine.ChatChunk{{Token: "ok"}}} // never terminates
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := lane.Check(ctx, interactiveChatPath(), usabilityEntry())
		assert.ErrorIs(t, err, ErrUsabilityNotConfirmed)
	})
}

// Edge: only interactive, backend-less chat routes on an evidenced provider;
// batch paths and agent streams belong to other lanes, and claude-cli needs the
// scratch directory.
func TestUsabilityInteractiveChatLaneSupportsOnlyInteractiveEvidencedChat(t *testing.T) {
	lane, _ := interactiveLaneFixture(t, doneStream)
	with := func(mut func(*modelcatalog.ExecutionPath)) modelcatalog.ExecutionPath {
		p := interactiveChatPath()
		mut(&p)
		return p
	}
	for provider, want := range map[string]bool{
		"anthropic-api": true, "openai": true, "openrouter": true, "litellm": true,
		"ollama": true, "ollama-cloud": true, "azure": false, "bedrock": false, "claude-cli": false, "": false,
	} {
		p := with(func(p *modelcatalog.ExecutionPath) { p.Binding.Provider = provider })
		assert.Equal(t, want, lane.Supports(p), "provider %q", provider)
	}
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Mode = modelcatalog.ExecutionModeBatch })))
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Backend = "claude-code" })))
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Kind = string(domprovider.ProfileKindAgent) })))
}

func TestUsabilityInteractiveChatLaneRequiresCollaborators(t *testing.T) {
	_, err := NewInteractiveChatUsabilityLane(InteractiveChatLaneDeps{})
	assert.Error(t, err)
}
