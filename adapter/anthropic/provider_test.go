package anthropic_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/adapter/anthropic"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time assertion: *anthropic.Provider must satisfy engine.ChatProvider.
var _ engine.ChatProvider = (*anthropic.Provider)(nil)

func TestProvider_Call_EmptyKey_ReturnsErrNotConfigured(t *testing.T) {
	p := anthropic.New("")
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hello"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, anthropic.ErrNotConfigured), "expected ErrNotConfigured, got: %v", err)
}

func TestProvider_StreamChat_EmptyKey_ReturnsErrNotConfigured(t *testing.T) {
	p := anthropic.New("")
	_, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hello"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, anthropic.ErrNotConfigured), "expected ErrNotConfigured, got: %v", err)
}

func TestProvider_Call_NonEmptyKey_ReturnsStubError(t *testing.T) {
	p := anthropic.New("sk-test-key")
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hello"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, anthropic.ErrNotConfigured), "stub error must not be ErrNotConfigured")
}

func TestProvider_StreamChat_NonEmptyKey_ReturnsStubError(t *testing.T) {
	p := anthropic.New("sk-test-key")
	_, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, anthropic.ErrNotConfigured), "stub error must not be ErrNotConfigured")
}
