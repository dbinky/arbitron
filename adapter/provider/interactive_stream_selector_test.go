package provider_test

import (
	"context"
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sentinelChatProvider is a no-op engine.ChatProvider used as an identity
// sentinel: tests assert the exact pointer returned by a selector.
type sentinelChatProvider struct{ name string }

func (s *sentinelChatProvider) Chat(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
	return nil, errors.New("sentinel: not implemented")
}
func (s *sentinelChatProvider) StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return nil, errors.New("sentinel: not implemented")
}

// fakeChatProviderSelector is an engine.ChatProviderSelector that always
// returns the same sentinel provider and records the binding it was called with.
type fakeChatProviderSelector struct {
	returns engine.ChatProvider
	got     provider.ResolvedBinding
}

func (f *fakeChatProviderSelector) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	f.got = b
	return f.returns, nil
}

// TestInteractiveStreamSelector_ChatKind verifies that a chat-kind binding
// delegates to the injected ChatProviderSelector and returns its result.
func TestInteractiveStreamSelector_ChatKind(t *testing.T) {
	chatSentinel := &sentinelChatProvider{name: "chat"}
	fakeSelector := &fakeChatProviderSelector{returns: chatSentinel}
	agentSentinel := &sentinelChatProvider{name: "agent"}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, agentSentinel)

	binding := provider.ResolvedBinding{
		Kind:     provider.ProfileKindChat,
		Provider: "claude-cli",
		Model:    "claude-3-5-sonnet",
	}

	got, err := sel.StreamProviderFor(binding)
	require.NoError(t, err)
	assert.Same(t, chatSentinel, got, "chat binding must return the ChatProviderSelector's result")
	assert.Equal(t, binding, fakeSelector.got, "binding must be forwarded to ChatProviderSelector unchanged")
}

// TestInteractiveStreamSelector_AgentKind_ClaudeCLI verifies that an agent-kind
// binding with Provider "claude-cli" returns the injected agent-stream provider.
func TestInteractiveStreamSelector_AgentKind_ClaudeCLI(t *testing.T) {
	chatSentinel := &sentinelChatProvider{name: "chat"}
	fakeSelector := &fakeChatProviderSelector{returns: chatSentinel}
	agentSentinel := &sentinelChatProvider{name: "agent"}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, agentSentinel)

	binding := provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: "claude-cli",
		Model:    "claude-3-5-sonnet",
	}

	got, err := sel.StreamProviderFor(binding)
	require.NoError(t, err)
	assert.Same(t, agentSentinel, got, "agent/claude-cli binding must return the injected agentStream provider")
}

// TestInteractiveStreamSelector_AgentKind_EmptyProvider verifies that an
// agent-kind binding with an empty Provider (subscription path) also returns the
// injected agent-stream provider (empty == claude-cli in MVP).
func TestInteractiveStreamSelector_AgentKind_EmptyProvider(t *testing.T) {
	chatSentinel := &sentinelChatProvider{name: "chat"}
	fakeSelector := &fakeChatProviderSelector{returns: chatSentinel}
	agentSentinel := &sentinelChatProvider{name: "agent"}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, agentSentinel)

	binding := provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: "", // empty = subscription / claude-cli path
		Model:    "claude-3-5-sonnet",
	}

	got, err := sel.StreamProviderFor(binding)
	require.NoError(t, err)
	assert.Same(t, agentSentinel, got, "agent/empty-provider binding must return the injected agentStream provider")
}

// TestInteractiveStreamSelector_AgentKind_NonClaudeProvider verifies that an
// agent-kind binding with a non-claude-cli provider returns an error (MVP only
// supports claude-cli for interactive agent streaming).
func TestInteractiveStreamSelector_AgentKind_NonClaudeProvider(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	agentSentinel := &sentinelChatProvider{name: "agent"}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, agentSentinel)

	binding := provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: "openrouter",
		Model:    "openai/gpt-4o",
	}

	got, err := sel.StreamProviderFor(binding)
	require.Error(t, err, "non-claude-cli agent binding must return an error")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "openrouter")
	// SF3 (PR #18, comment 3350533149): the error must be a package-level
	// sentinel wrapped with %w so callers can distinguish unsupported-provider
	// from other failures via errors.Is.
	assert.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentProvider)
}

// TestInteractiveStreamSelector_UnknownKind verifies that a binding with an
// unknown/empty kind returns an error.
func TestInteractiveStreamSelector_UnknownKind(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	agentSentinel := &sentinelChatProvider{name: "agent"}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, agentSentinel)

	binding := provider.ResolvedBinding{
		Kind:     provider.ProfileKind("unknown-kind"),
		Provider: "claude-cli",
		Model:    "claude-3-5-sonnet",
	}

	got, err := sel.StreamProviderFor(binding)
	require.Error(t, err, "unknown kind must return an error")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "unknown")
}
