package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/opencode"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentBindingBackend builds an agent binding carrying both a provider and a
// backend so the factory's backend-vs-provider decoupling can be exercised.
func agentBindingBackend(providerName, backend string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Kind:     provider.ProfileKindAgent,
		Provider: providerName,
		Backend:  backend,
		Model:    "m",
	}
}

// newBackendFactory builds a Factory with pre-built claude-cli adapters and an
// explicit DefaultAgentBackend so the unset-backend arm is deterministic.
func newBackendFactory(t *testing.T, defaultBackend string) *adapterprovider.Factory {
	t.Helper()
	agent := claudecli.NewAgentAdapter(claudecli.Config{Runner: &noopRunner{}})
	chat := claudecli.NewChatAdapter(claudecli.Config{Runner: &noopRunner{}})
	return adapterprovider.NewFactory(adapterprovider.Deps{
		ClaudeAgent:         agent,
		ClaudeChat:          chat,
		DefaultAgentBackend: defaultBackend,
	})
}

// happy — claude-code backend selects the claude-cli agent adapter.
func TestFactoryBackend_ClaudeCode_SelectsClaudeCLI(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "claude-code"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// happy — claude-code backend with a non-claude connection STILL selects the
// claude-cli adapter (the core decoupling).
func TestFactoryBackend_ClaudeCode_WithLiteLLMConnection(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", "claude-code"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// happy — opencode backend selects the opencode adapter.
func TestFactoryBackend_Opencode_SelectsOpencode(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// happy — opencode backend with a claude-cli provider STILL selects opencode
// (backend, not provider, drives the choice).
func TestFactoryBackend_Opencode_WithClaudeProvider(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// default — unset backend uses the deployment default (claude-code).
func TestFactoryBackend_Unset_DefaultsClaudeCode(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", ""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// default — unset backend uses opencode when that is the configured default.
func TestFactoryBackend_Unset_DefaultsOpencode(t *testing.T) {
	f := newBackendFactory(t, "opencode")
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", ""))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// error — an unknown backend fails closed with ErrUnknownBackend.
func TestFactoryBackend_Unknown_FailsClosed(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", "gpt-cli"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
	assert.Contains(t, err.Error(), "gpt-cli")
}

// edge — empty backend + empty deployment default → ErrUnknownBackend.
// The executor's all-empty chain leaves binding.Backend = ""; if the
// composition root also wires an empty DefaultAgentBackend the factory must
// fail closed rather than silently running without a backend. This test
// exercises the bottom of the precedence chain: all three sources (node,
// profile, default) are unset.
func TestFactoryBackend_EmptyBackendEmptyDefault_FailsClosed(t *testing.T) {
	f := newBackendFactory(t, "")
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", ""))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}

// error — claude-code arm with no ClaudeAgent configured returns a clear error.
func TestFactoryBackend_ClaudeCode_NilAdapter(t *testing.T) {
	f := adapterprovider.NewFactory(adapterprovider.Deps{DefaultAgentBackend: "claude-code"})
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", "claude-code"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-cli")
}

// boundary — surrounding whitespace on a VALID backend value is trimmed and
// routed correctly. The workflow validator uses exact-match so a
// whitespace-padded backend is rejected at load; this factory-level TrimSpace
// is defense-in-depth for any programmatic path that omits trimming.
func TestFactoryBackend_PaddedValidBackend_Routed(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", " opencode "))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// boundary — case/whitespace is rejected, consistent with Task 1's validator
// (exact lowercase). The validator already blocked this at load; the factory
// stays strict and fails closed.
func TestFactoryBackend_UpperCase_FailsClosed(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	_, err := f.AgentProviderFor(agentBindingBackend("openai", "OPENCODE"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}

// regression — ChatProviderFor ignores Backend (backend has no effect on chat).
func TestFactoryBackend_ChatIgnoresBackend(t *testing.T) {
	f := newBackendFactory(t, "claude-code")
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "litellm", Backend: "opencode", Model: "m"}
	cp, err := f.ChatProviderFor(b)
	require.NoError(t, err)
	// litellm routes to the openrouter chat adapter regardless of backend.
	assert.NotNil(t, cp)
}
