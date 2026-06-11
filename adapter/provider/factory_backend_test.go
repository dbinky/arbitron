package provider_test

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
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

// newBackendFactory builds a Factory with pre-built claude-cli adapters. The
// deployment default is no longer a factory concern (the config floor owns it
// and the executor applies it before the factory runs), so the factory always
// dispatches on the binding's Backend exactly.
func newBackendFactory(t *testing.T) *adapterprovider.Factory {
	t.Helper()
	agent := claudecli.NewAgentAdapter(claudecli.Config{Runner: &noopRunner{}})
	chat := claudecli.NewChatAdapter(claudecli.Config{Runner: &noopRunner{}})
	return adapterprovider.NewFactory(adapterprovider.Deps{
		ClaudeAgent: agent,
		ClaudeChat:  chat,
	})
}

// happy — claude-code backend selects the claude-cli agent adapter.
func TestFactoryBackend_ClaudeCode_SelectsClaudeCLI(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "claude-code"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// happy — claude-code backend with a non-claude connection STILL selects the
// claude-cli adapter (the core decoupling).
func TestFactoryBackend_ClaudeCode_WithLiteLLMConnection(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", "claude-code"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// happy — opencode backend selects the opencode adapter.
func TestFactoryBackend_Opencode_SelectsOpencode(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// happy — opencode backend with a claude-cli provider STILL selects opencode
// (backend, not provider, drives the choice).
func TestFactoryBackend_Opencode_WithClaudeProvider(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// happy — codex backend selects the codex adapter, regardless of the provider
// carried on the binding (backend, not provider, drives the choice). The codex
// path is subscription-only, so no key is configured on the binding.
func TestFactoryBackend_Codex_SelectsCodex(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "codex"))
	require.NoError(t, err)
	assert.IsType(t, (*codex.Provider)(nil), ap)
}

// happy — codex backend with a non-openai provider (e.g. claude-cli) STILL
// selects the codex adapter (backend, not provider, drives the choice — same
// decoupling as TestFactoryBackend_Opencode_WithClaudeProvider). Codex is
// subscription-only, so the provider field on the binding is irrelevant.
func TestFactoryBackend_Codex_WithClaudeProvider(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "codex"))
	require.NoError(t, err)
	assert.IsType(t, (*codex.Provider)(nil), ap)
}

// boundary — a near-miss case on the codex backend ("Codex") is rejected by the
// factory's exact-match vocabulary (consistent with the workflow validator).
func TestFactoryBackend_Codex_UpperCaseFailsClosed(t *testing.T) {
	f := newBackendFactory(t)
	_, err := f.AgentProviderFor(agentBindingBackend("openai", "Codex"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}

// fail-closed — an unset (empty) backend is NEVER silently re-defaulted by the
// factory. The deployment default is owned by the config floor and applied by
// the executor before the factory runs, so an empty Backend reaching the factory
// is a code path that bypassed the executor and must get a clear ErrUnknownBackend
// (not a second silent default). PR #25 review: deleted the factory re-default.
func TestFactoryBackend_Unset_FailsClosed(t *testing.T) {
	f := newBackendFactory(t)
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", ""))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}

// error — an unknown backend fails closed with ErrUnknownBackend.
func TestFactoryBackend_Unknown_FailsClosed(t *testing.T) {
	f := newBackendFactory(t)
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", "gpt-cli"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
	assert.Contains(t, err.Error(), "gpt-cli")
}

// error — claude-code arm with no ClaudeAgent configured returns a clear error.
func TestFactoryBackend_ClaudeCode_NilAdapter(t *testing.T) {
	f := adapterprovider.NewFactory(adapterprovider.Deps{})
	_, err := f.AgentProviderFor(agentBindingBackend("litellm", "claude-code"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-cli")
}

// boundary — surrounding whitespace on a VALID backend value is trimmed and
// routed correctly. The workflow validator uses exact-match so a
// whitespace-padded backend is rejected at load; this factory-level TrimSpace
// is defense-in-depth for any programmatic path that omits trimming.
func TestFactoryBackend_PaddedValidBackend_Routed(t *testing.T) {
	f := newBackendFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("litellm", " opencode "))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// boundary — case/whitespace is rejected, consistent with Task 1's validator
// (exact lowercase). The validator already blocked this at load; the factory
// stays strict and fails closed.
func TestFactoryBackend_UpperCase_FailsClosed(t *testing.T) {
	f := newBackendFactory(t)
	_, err := f.AgentProviderFor(agentBindingBackend("openai", "OPENCODE"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownBackend)
}

// regression — ChatProviderFor ignores Backend (backend has no effect on chat).
func TestFactoryBackend_ChatIgnoresBackend(t *testing.T) {
	f := newBackendFactory(t)
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "litellm", Backend: "opencode", Model: "m"}
	cp, err := f.ChatProviderFor(b)
	require.NoError(t, err)
	// litellm routes to the openrouter chat adapter regardless of backend.
	assert.NotNil(t, cp)
}
