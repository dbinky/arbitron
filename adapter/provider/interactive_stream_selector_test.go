package provider_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/adapter/opencode"
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

// TestInteractiveStreamSelector_AgentKind_PaddedBackend verifies that a
// whitespace-padded valid backend is trimmed and dispatched correctly, matching
// TestFactoryBackend_PaddedValidBackend_Routed on the agent batch path.
// agentStreamFor calls strings.TrimSpace before the map lookup, so " codex "
// must reach the codex stream adapter, not fail closed as an unknown backend.
func TestInteractiveStreamSelector_AgentKind_PaddedBackend(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	_, codexSentinel, m := streamsByBackend()

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, m, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: " codex ", // whitespace-padded valid backend
		Model:   "gpt-5-codex",
	})
	require.NoError(t, err, "whitespace-padded codex backend must be trimmed and routed")
	assert.Same(t, codexSentinel, got, "trimmed codex backend must reach the codex stream adapter")
}

// TestInteractiveStreamSelector_AgentKind_BackendAbsentFromMap verifies that a
// valid (non-opencode) backend that is simply absent from the agentStreams map
// fails closed with ErrUnsupportedAgentBackend. This tests the fall-through
// branch in agentStreamFor — a misconfigured deployment where, e.g., the codex
// stream provider was not wired into the composition root.
func TestInteractiveStreamSelector_AgentKind_BackendAbsentFromMap(t *testing.T) {
	fakeSelector := &fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}}
	// Build a map with ONLY claude-code; codex is deliberately absent.
	claude := &sentinelChatProvider{name: "claude-stream"}
	sparseMap := map[string]engine.ChatProvider{workflow.BackendClaudeCode: claude}

	sel := adapterprovider.NewInteractiveStreamSelector(fakeSelector, sparseMap, workflow.BackendClaudeCode)

	got, err := sel.StreamProviderFor(provider.ResolvedBinding{
		Kind:    provider.ProfileKindAgent,
		Backend: workflow.BackendCodex,
		Model:   "gpt-5-codex",
	})
	require.Error(t, err, "a backend absent from the stream map must fail closed")
	assert.Nil(t, got)
	assert.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend,
		"fall-through from absent map entry must wrap ErrUnsupportedAgentBackend")
}

// FA15 PAIRED (§8+§9) — the factory (AgentProviderFor) and the stream selector
// (StreamProviderFor) must agree on the backend dispatch vocabulary:
//   - "codex": factory → *codex.Provider; selector → codex stream sentinel.
//     Codex is reachable on BOTH agent_call (factory) and interactive_agent (selector).
//   - "opencode": factory → *opencode.Provider (success); selector → ErrUnsupportedAgentBackend.
//     Opencode is BATCH-ONLY; interactive streaming is explicitly unsupported.
//   - unknown backend: both fail closed (ErrUnknownBackend / ErrUnsupportedAgentBackend).
//
// If the two units used different string comparisons (e.g. factory trimmed but
// selector did not), a backend that passes the factory check could silently fail
// at the selector without a clear error.
func TestFactoryAndSelector_SharedBackendVocabulary(t *testing.T) {
	f := newBackendFactory(t)
	_, codexStream, m := streamsByBackend()
	sel := adapterprovider.NewInteractiveStreamSelector(
		&fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}},
		m,
		workflow.BackendClaudeCode,
	)

	agentBinding := func(backend string) provider.ResolvedBinding {
		return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Backend: backend, Model: "m"}
	}

	// codex: reachable on both batch (factory) and stream (selector).
	ap, err := f.AgentProviderFor(agentBinding(workflow.BackendCodex))
	require.NoError(t, err, "codex must be reachable via factory (agent_call path)")
	assert.IsType(t, (*codex.Provider)(nil), ap)

	sp, err := sel.StreamProviderFor(agentBinding(workflow.BackendCodex))
	require.NoError(t, err, "codex must be reachable via stream selector (interactive_agent path)")
	assert.Same(t, codexStream, sp, "codex stream must return the codex sentinel")

	// opencode: batch only (factory OK, selector fails closed).
	ap2, err := f.AgentProviderFor(agentBinding(workflow.BackendOpencode))
	require.NoError(t, err, "opencode must be reachable via factory (agent_call path)")
	assert.IsType(t, (*opencode.Provider)(nil), ap2)

	sp2, err := sel.StreamProviderFor(agentBinding(workflow.BackendOpencode))
	require.Error(t, err, "opencode must fail closed on stream (interactive streaming unsupported)")
	assert.Nil(t, sp2)
	assert.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend)

	// unknown backend: both fail closed.
	_, errF := f.AgentProviderFor(agentBinding("ghost-backend"))
	require.Error(t, errF)
	assert.ErrorIs(t, errF, adapterprovider.ErrUnknownBackend)

	_, errS := sel.StreamProviderFor(agentBinding("ghost-backend"))
	require.Error(t, errS)
	assert.ErrorIs(t, errS, adapterprovider.ErrUnsupportedAgentBackend)
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

// FA18 PAIRED (§12+§8+§9) — the "interviewer" profile from the default config
// must route through BOTH the agent factory (§8, AgentProviderFor → codex.Provider)
// AND the interactive stream selector (§9, StreamProviderFor → codex stream sentinel).
// Similarly, "planner" and "coder" route to claude-code in both directions.
//
// This test exercises the full pipeline:
//   1. Load default config → ProfilesFromConfig → StaticProfileResolver
//   2. Resolve "interviewer" → ResolvedBinding{Backend: "codex", ...}
//   3. AgentProviderFor(binding) → *codex.Provider        (agent_call path)
//   4. StreamProviderFor(binding) → codex stream sentinel (interactive_agent path)
//   5. Repeat for "planner"/"coder" → *claudecli.AgentAdapter / claude stream sentinel
func TestFA18_SampleProfileDispatchThroughBothArms(t *testing.T) {
	// Load the default profile registry (no deployment config.yaml).
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(tmpDir, 0o755))
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg, err := adapterconfig.Load(filepath.Join(tmpDir, "config.yaml"))
	require.NoError(t, err)

	profiles := adapterconfig.ProfilesFromConfig(cfg.Profiles)
	resolver, err := adapterprovider.NewStaticProfileResolver(profiles, nil)
	require.NoError(t, err)

	// Build factory + stream selector with sentinels.
	f := newBackendFactory(t)
	claudeStream, codexStream, m := streamsByBackend()
	sel := adapterprovider.NewInteractiveStreamSelector(
		&fakeChatProviderSelector{returns: &sentinelChatProvider{name: "chat"}},
		m,
		workflow.BackendClaudeCode,
	)

	// --- interviewer → codex ---
	iBinding, err := resolver.Resolve(context.Background(), "interviewer")
	require.NoError(t, err)
	require.Equal(t, "codex", iBinding.Backend)

	ap, err := f.AgentProviderFor(iBinding)
	require.NoError(t, err, "interviewer must route through factory (agent_call path)")
	assert.IsType(t, (*codex.Provider)(nil), ap, "interviewer must produce a codex.Provider")

	sp, err := sel.StreamProviderFor(iBinding)
	require.NoError(t, err, "interviewer must route through stream selector (interactive_agent path)")
	assert.Same(t, codexStream, sp, "interviewer stream must return the codex stream sentinel")

	// --- planner / coder → claude-code ---
	for _, alias := range []string{"planner", "coder"} {
		alias := alias
		t.Run(alias, func(t *testing.T) {
			b, err := resolver.Resolve(context.Background(), alias)
			require.NoError(t, err)
			require.Equal(t, "claude-code", b.Backend, "%s must resolve to claude-code", alias)

			ap2, err := f.AgentProviderFor(b)
			require.NoError(t, err, "%s must route through factory", alias)
			// newBackendFactory wires ClaudeAgent (not ClaudeAgentConfig), so the factory
			// returns the pre-built *claudecli.AgentAdapter on the claude-code arm.
			assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap2,
				"%s must produce a claudecli.AgentAdapter from the factory", alias)

			sp2, err := sel.StreamProviderFor(b)
			require.NoError(t, err, "%s must route through stream selector", alias)
			assert.Same(t, claudeStream, sp2, "%s stream must return the claude stream sentinel", alias)
		})
	}
}

