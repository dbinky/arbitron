package provider_test

import (
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInteractiveStreamSelector_RefusesLunaRouteAgent(t *testing.T) {
	for _, defaultBackend := range []string{"claude-code", ""} {
		t.Run("default="+defaultBackend, func(t *testing.T) {
			built := 0
			claude, _, streams := streamsByBackend()
			for k, f := range streams {
				inner := f
				streams[k] = func(b provider.ResolvedBinding) engine.ChatProvider {
					built++
					require.Equal(t, "claude-cli", b.Provider)
					require.Equal(t, "claude-code", b.Backend)
					return inner(b)
				}
			}
			chatSentinel := &sentinelChatProvider{name: "chat"}
			chat := &fakeChatProviderSelector{returns: chatSentinel}
			sel := adapterprovider.NewInteractiveStreamSelector(chat, streams, defaultBackend)

			for _, backend := range []string{"claude-code", "codex", "opencode", "pi", "", "unknown"} {
				for _, explicit := range []bool{true, false} {
					got, err := sel.StreamProviderFor(provider.ResolvedBinding{BackendExplicit: explicit, Kind: provider.ProfileKindAgent, Provider: "lunaroute", Backend: backend, Model: "glm-5.3"})
					require.ErrorIs(t, err, adapterprovider.ErrUnsupportedAgentBackend, "backend %q, explicit %t", backend, explicit)
					assert.Nil(t, got)
					assert.Contains(t, err.Error(), "LunaRoute agent profiles have no interactive streaming path")
					assert.Contains(t, err.Error(), "use agent_call")
				}
			}
			assert.Zero(t, built, "no stream adapter (subscription or otherwise) was constructed")
			assert.Empty(t, chat.got, "agent refusal never delegates to chat")

			binding := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "lunaroute", Model: "glm-5.3"}
			got, err := sel.StreamProviderFor(binding)
			require.NoError(t, err)
			assert.Same(t, chatSentinel, got, "LunaRoute chat still streams through the chat selector")
			assert.Equal(t, binding, chat.got, "chat binding must arrive unchanged")

			got, err = sel.StreamProviderFor(provider.ResolvedBinding{BackendExplicit: true, Kind: provider.ProfileKindAgent, Provider: "claude-cli", Backend: "claude-code"})
			require.NoError(t, err)
			assert.Same(t, claude, got, "non-LunaRoute agents unchanged")
			assert.Equal(t, 1, built)
		})
	}
}
