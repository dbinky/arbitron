package provider_test

import (
	"context"
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
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

// streamsByBackend assembles a fresh backend→stream map plus the per-backend
// sentinels, so each test asserts the exact provider routed by backend.
func streamsByBackend() (claude, codex *sentinelChatProvider, m map[string]engine.ChatProvider) {
	claude = &sentinelChatProvider{name: "claude-stream"}
	codex = &sentinelChatProvider{name: "codex-stream"}
	return claude, codex, map[string]engine.ChatProvider{
		workflow.BackendClaudeCode: claude,
		workflow.BackendCodex:      codex,
	}
}

// TestInteractiveStreamSelector_ChatKind verifies that a chat-kind binding
// delegates to the injected ChatProviderSelector and returns its result
// (unchanged by the backend-dispatch generalization).
func TestInteractiveStreamSelector_ChatKind(t *testing.T) {
	chatSentinel := &sentinelChatProvider{name: "chat"}
	fakeSelector := &fakeChatProviderSelector{returns: chatSentinel}
	_, _, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

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

// TestInteractiveStreamSelector_AgentKind_ClaudeCode verifies that an agent-kind
// binding with Backend "claude-code" returns the claude stream adapter.
func TestInteractiveStreamSelector_AgentKind_ClaudeCode(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	claude, _, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: workflow.BackendClaudeCode,
		Model:   "claude-3-5-sonnet",
	})
	require.NoError(t, err)
	assert.Same(t, claude, got, "agent/claude-code binding must return the claude stream adapter")
}

// TestInteractiveStreamSelector_AgentKind_Codex verifies that an agent-kind
// binding with Backend "codex" returns the codex stream adapter.
func TestInteractiveStreamSelector_AgentKind_Codex(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, codex, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: workflow.BackendCodex,
		Model:   "gpt-5-codex",
	})
	require.NoError(t, err)
	assert.Same(t, codex, got, "agent/codex binding must return the codex stream adapter")
}

// TestInteractiveStreamSelector_AgentKind_EmptyBackendFloored verifies that an
// agent-kind binding with an empty Backend is floored to the injected default
// backend and then dispatched (here: default codex → codex stream).
func TestInteractiveStreamSelector_AgentKind_EmptyBackendFloored(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, codex, m := streamsByBackend()

	// Default floor = codex, so an empty backend must route to the codex stream.
	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendCodex)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: "", // empty → floored to the default backend
		Model:   "gpt-5-codex",
	})
	require.NoError(t, err)
	assert.Same(t, codex, got, "empty backend must be floored to the default backend, then dispatched")
}

// TestInteractiveStreamSelector_AgentKind_Opencode verifies that an agent-kind
// binding with Backend "opencode" fails closed: opencode has no stream adapter
// and interactive streaming is explicitly unsupported for it.
func TestInteractiveStreamSelector_AgentKind_Opencode(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, _, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: workflow.BackendOpencode,
		Model:   "anthropic/claude-3-5-sonnet",
	})
	require.Error(t, err, "opencode agent streaming must fail closed")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "opencode")
	assert.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)
}

// TestInteractiveStreamSelector_AgentKind_UnknownBackend verifies that an
// unknown/typo backend (incl. a case-variant) fails closed with the sentinel.
func TestInteractiveStreamSelector_AgentKind_UnknownBackend(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, _, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	for _, backend := range []string{"Codex", "not-a-backend", "CLAUDE-CODE"} {
		got, err := sel.StreamProviderFor(provider.ResolvedBinding{
			Kind:    provider.ProfileKindAgent,
			Backend: backend,
			Model:   "m",
		})
		require.Errorf(t, err, "unknown backend %q must fail closed", backend)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)
		assert.Contains(t, err.Error(), backend, "the error must quote the offending backend")
	}
}

// TestInteractiveStreamSelector_UnknownKind verifies that a binding with an
// unknown/empty kind returns an error (unchanged).
func TestInteractiveStreamSelector_UnknownKind(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, _, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKind("unknown-kind"),
		Backend: workflow.BackendClaudeCode,
		Model:   "claude-3-5-sonnet",
	})
	require.Error(t, err, "unknown kind must return an error")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "unknown")
}
