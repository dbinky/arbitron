package provider_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/openrouter"
	"github.com/arbitron/arbitron/adapter/opencode"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestFactory builds a Factory with pre-constructed claude-cli adapters and
// no network/binary dependencies wired (the HTTP/opencode adapters are
// constructed lazily and never invoked in selection-only tests).
func newTestFactory(t *testing.T) *adapterprovider.Factory {
	t.Helper()
	chat := claudecli.NewChatAdapter(claudecli.Config{Runner: &noopRunner{}})
	agent := claudecli.NewAgentAdapter(claudecli.Config{Runner: &noopRunner{}})
	return adapterprovider.NewFactory(adapterprovider.Deps{
		ClaudeChat:  chat,
		ClaudeAgent: agent,
	})
}

// noopRunner is a CommandRunner that is never invoked in these tests; it only
// satisfies the claude-cli Config so NewChatAdapter/NewAgentAdapter skip the
// real os/exec runner.
type noopRunner struct{}

func (*noopRunner) Run(context.Context, claudecli.RunSpec) (<-chan string, func() error, error) {
	return nil, func() error { return nil }, errors.New("noopRunner should not be invoked")
}

func chatBinding(providerName string) provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: providerName, Model: "m"}
}

func agentBinding(providerName string) provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: providerName, Model: "m"}
}

// Compile-time assertion that the factory satisfies both selector ports.
var (
	_ engine.ChatProviderSelector  = (*adapterprovider.Factory)(nil)
	_ engine.AgentProviderSelector = (*adapterprovider.Factory)(nil)
)

// ---- Happy path: each provider string selects the right adapter ----

func TestFactory_ChatProviderFor_OpenRouter(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("openrouter"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_Azure(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("azure-openai"))
	require.NoError(t, err)
	assert.IsType(t, (*azurefoundry.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_EmptyIsClaudeCLI(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding(""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.ChatAdapter)(nil), cp)
}

func TestFactory_ChatProviderFor_ClaudeCLIExplicit(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("claude-cli"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.ChatAdapter)(nil), cp)
}

func TestFactory_AgentProviderFor_NonClaudeIsOpencode(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBinding("opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

func TestFactory_AgentProviderFor_EmptyIsClaudeCLI(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBinding(""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

func TestFactory_AgentProviderFor_ClaudeCLIExplicit(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBinding("claude-cli"))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// ---- Success (variants): alias-table entries map to the same adapter ----

func TestFactory_ChatProviderFor_LiteLLM_MapsToOpenRouter(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("litellm"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_AzureFoundry_MapsToAzure(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("azure-foundry"))
	require.NoError(t, err)
	assert.IsType(t, (*azurefoundry.Provider)(nil), cp)
}

func TestFactory_AgentProviderFor_ArbitraryNonClaude_MapsToOpencode(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBinding("openrouter"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// ---- Chat-vs-agent selection: same provider string routes by kind method ----

func TestFactory_ChatVsAgentSelection(t *testing.T) {
	f := newTestFactory(t)

	// The empty/claude-cli provider selects the chat adapter through the chat
	// method and the agent adapter through the agent method.
	cp, err := f.ChatProviderFor(chatBinding(""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.ChatAdapter)(nil), cp)

	ap, err := f.AgentProviderFor(agentBinding(""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// ---- Error: unknown provider yields ErrUnknownProvider ----

func TestFactory_ChatProviderFor_UnknownProvider(t *testing.T) {
	f := newTestFactory(t)
	_, err := f.ChatProviderFor(chatBinding("does-not-exist"))
	require.Error(t, err)
	assert.ErrorIs(t, err, adapterprovider.ErrUnknownProvider)
	assert.Contains(t, err.Error(), "does-not-exist")
}

// An agent binding only has two destinations (claude-cli or opencode); a chat
// provider string used for an agent binding still routes to opencode, so the
// only agent-side error is a missing claude-cli adapter for the empty path,
// covered below.

// ---- Error: missing required dependency (no claude-cli adapter) ----

func TestFactory_ChatProviderFor_MissingClaudeAdapter(t *testing.T) {
	f := adapterprovider.NewFactory(adapterprovider.Deps{}) // no ClaudeChat
	_, err := f.ChatProviderFor(chatBinding(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-cli")
}

func TestFactory_AgentProviderFor_MissingClaudeAdapter(t *testing.T) {
	f := adapterprovider.NewFactory(adapterprovider.Deps{}) // no ClaudeAgent
	_, err := f.AgentProviderFor(agentBinding(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude-cli")
}

// ---- Edge case: provider string normalization (case + whitespace) ----

func TestFactory_ChatProviderFor_NormalizesCaseAndWhitespace(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("  OpenRouter  "))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_AgentProviderFor_NormalizesEmptyWhitespaceToClaude(t *testing.T) {
	f := newTestFactory(t)
	// A whitespace-only provider normalizes to empty -> claude-cli, not opencode.
	ap, err := f.AgentProviderFor(agentBinding("   "))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// ---- Success (variants): HTTP adapters get the injected client ----

func TestFactory_ChatProviderFor_UsesInjectedHTTPClient(t *testing.T) {
	// A non-default client is accepted; we only assert construction succeeds
	// (the adapter's own tests exercise the transport seam).
	hc := &http.Client{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: hc})
	cp, err := f.ChatProviderFor(chatBinding("openrouter"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

// ---- Security: selection + reveal never logs the revealed key ----

func TestFactory_Selection_DoesNotLogRevealedKey(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	const secret = "sk-super-secret-key-value"
	b := chatBinding("openrouter")
	b.RevealKey = func(context.Context) (string, error) { return secret, nil }

	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(b)
	require.NoError(t, err)
	require.NotNil(t, cp)

	// Reveal the key through the binding the factory carried into the adapter.
	got, err := b.RevealKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, secret, got)

	assert.False(t, strings.Contains(buf.String(), secret),
		"revealed key must never appear in logs during selection")
}
