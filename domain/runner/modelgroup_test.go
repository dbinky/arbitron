package runner_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelGroup_String(t *testing.T) {
	assert.Equal(t, "anthropic_subscription:anthropic", runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}.String())
	assert.Equal(t, "server_connection:azure-east:openai", runner.ModelGroup{Kind: runner.CredServerConnection, ConnectionRef: "azure-east", Family: "openai"}.String())
	assert.Equal(t, "lunaroute:lr:openai", runner.ModelGroup{Kind: runner.CredLunaRoute, ConnectionRef: "lr", Family: "openai"}.String())
	assert.Equal(t, "none", runner.ModelGroup{Kind: runner.CredNone}.String())
}

func TestGroupFor_Table(t *testing.T) {
	cases := []struct {
		name string
		b    provider.ResolvedBinding
		op   runner.OpType
		want runner.ModelGroup
	}{
		{"subscription chat, empty backend", provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: "claude-sonnet-4-5"}, runner.OpChat,
			runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}},
		{"subscription stream, empty backend", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-opus-4-1"}, runner.OpStream,
			runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}},
		{"claude-code agent", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5", Backend: "claude-code"}, runner.OpAgent,
			runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}},
		{"codex agent no connection", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "gpt-5.1-codex", Backend: "codex"}, runner.OpAgent,
			runner.ModelGroup{Kind: runner.CredOpenAISubscription, Family: "openai"}},
		{"codex interactive stream", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "gpt-5.1", Backend: "codex"}, runner.OpStream,
			runner.ModelGroup{Kind: runner.CredOpenAISubscription, Family: "openai"}},
		{"server connection chat", provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "openrouter", Model: "gpt-4o", ConnectionRef: "or-main"}, runner.OpChat,
			runner.ModelGroup{Kind: runner.CredServerConnection, ConnectionRef: "or-main", Family: "openai"}},
		{"server connection on claude-code backend stays server_connection", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "anthropic", Model: "claude-sonnet-4-5", Backend: "claude-code", ConnectionRef: "anthropic-key"}, runner.OpAgent,
			runner.ModelGroup{Kind: runner.CredServerConnection, ConnectionRef: "anthropic-key", Family: "anthropic"}},
		{"server connection with opaque model keeps unresolved family", provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "azure-foundry", Model: "my-deployment-7", ConnectionRef: "azure-east"}, runner.OpChat,
			runner.ModelGroup{Kind: runner.CredServerConnection, ConnectionRef: "azure-east", Family: modelidentity.FamilyUnresolved}},
		{"pi local harness", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "glm-5.3", Backend: "pi"}, runner.OpAgent,
			runner.ModelGroup{Kind: runner.CredLocalHarness, ConnectionRef: "pi", Family: "zhipuai"}},
		{"opencode local harness, opaque model allowed", provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Model: "local-model", Backend: "opencode"}, runner.OpAgent,
			runner.ModelGroup{Kind: runner.CredLocalHarness, ConnectionRef: "opencode", Family: modelidentity.FamilyUnresolved}},
		{"process step is slot-only", provider.ResolvedBinding{}, runner.OpProcess,
			runner.ModelGroup{Kind: runner.CredNone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runner.GroupFor(c.b, c.op)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestGroupFor_LunaRouteOnCodexIsNeverSubscription(t *testing.T) {
	for _, backend := range []string{"codex", "pi", ""} {
		for _, op := range []runner.OpType{runner.OpAgent, runner.OpStream, runner.OpChat} {
			if backend == "" && op != runner.OpChat {
				continue
			}
			b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: provider.CanonicalLunaRoute, Model: "gpt-5.1", Backend: backend, ConnectionRef: "lr-prod"}
			got, err := runner.GroupFor(b, op)
			require.NoError(t, err, "backend %q op %s", backend, op)
			assert.Equal(t, runner.CredLunaRoute, got.Kind, "backend %q op %s must never be a subscription", backend, op)
			assert.Equal(t, "lr-prod", got.ConnectionRef)
			assert.Equal(t, modelidentity.Family("openai"), got.Family)
		}
	}
	// Case-insensitive canonical recognition ("LunaRoute" display form).
	got, err := runner.GroupFor(provider.ResolvedBinding{Provider: "LunaRoute", Model: "gpt-5.1", Backend: "codex", ConnectionRef: "lr"}, runner.OpAgent)
	require.NoError(t, err)
	assert.Equal(t, runner.CredLunaRoute, got.Kind)
}

func TestGroupFor_UnresolvedFamilyRefused(t *testing.T) {
	cases := map[string]struct {
		b  provider.ResolvedBinding
		op runner.OpType
	}{
		"subscription chat with unknown model": {provider.ResolvedBinding{Provider: "claude-cli", Model: "mystery-model-xyz"}, runner.OpChat},
		"subscription chat with empty model":   {provider.ResolvedBinding{Provider: "claude-cli", Model: ""}, runner.OpChat},
		"codex with opaque model":              {provider.ResolvedBinding{Model: "house-model", Backend: "codex"}, runner.OpAgent},
		"lunaroute claude-shaped alias (R3)":   {provider.ResolvedBinding{Provider: "lunaroute", Model: "claude-sonnet-lr2", Backend: "codex", ConnectionRef: "lr"}, runner.OpAgent},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runner.GroupFor(c.b, c.op)
			assert.ErrorIs(t, err, runner.ErrUnresolvedFamily)
		})
	}
}

func TestGroupFor_UnresolvedBackendFailsClosed(t *testing.T) {
	_, err := runner.GroupFor(provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "claude-sonnet-4-5"}, runner.OpAgent)
	assert.ErrorIs(t, err, runner.ErrUnresolvedBackend, "an unfloored agent backend is never guessed")
	_, err = runner.GroupFor(provider.ResolvedBinding{Model: "claude-sonnet-4-5", Backend: "gemini-cli"}, runner.OpAgent)
	assert.ErrorIs(t, err, runner.ErrUnresolvedBackend)
}
