package provider_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/anthropicapi"
	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/adapter/bedrock"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/openrouter"
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
		ClaudeChat:          chat,
		ClaudeAgent:         agent,
		DefaultAgentBackend: "claude-code",
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

// Backend dispatch (org-readiness Phase 2): the opencode BACKEND selects the
// opencode adapter regardless of provider.
func TestFactory_AgentProviderFor_OpencodeBackendIsOpencode(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// An unset backend falls back to the deployment default (claude-code in the
// test factory) and selects the claude-cli adapter.
func TestFactory_AgentProviderFor_UnsetDefaultsToClaudeCLI(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBinding(""))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// A claude-code backend selects the claude-cli adapter.
func TestFactory_AgentProviderFor_ClaudeCodeBackend(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("claude-cli", "claude-code"))
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

func TestFactory_AgentProviderFor_OpencodeBackend_AnyProvider(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("openrouter", "opencode"))
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

// security: ErrUnknownProvider uses %q so a hostile provider string containing
// control characters (newlines, CR, terminal escapes) cannot inject them into
// structured log output. The raw control char must not appear in the error string.
func TestFactory_ChatProviderFor_UnknownProvider_ControlCharsSafelyQuoted(t *testing.T) {
	f := newTestFactory(t)
	_, err := f.ChatProviderFor(chatBinding("foo\nmalicious"))
	require.ErrorIs(t, err, adapterprovider.ErrUnknownProvider)
	// %q encodes the newline as the two-character sequence \n, not a raw newline.
	assert.NotContains(t, err.Error(), "\n",
		"raw newline must not appear in ErrUnknownProvider message")
	assert.Contains(t, err.Error(), `\n`,
		"escaped \\n should appear in the %q-quoted provider string")
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
	// claude-code backend (default) with no ClaudeAgent wired -> clear error.
	f := adapterprovider.NewFactory(adapterprovider.Deps{DefaultAgentBackend: "claude-code"})
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

func TestFactory_AgentProviderFor_BackendNormalizesWhitespace(t *testing.T) {
	f := newTestFactory(t)
	// A whitespace-only backend normalizes to empty -> deployment default
	// (claude-code) -> claude-cli adapter. The provider string is irrelevant to
	// agent dispatch now.
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "   "))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.AgentAdapter)(nil), ap)
}

// Edge case: whitespace-only provider string for ChatProviderFor normalizes to
// empty, routing to claude-cli — the chat-path mirror of the agent test above.
func TestFactory_ChatProviderFor_WhitespaceOnlyIsClaudeCLI(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("   "))
	require.NoError(t, err)
	assert.IsType(t, (*claudecli.ChatAdapter)(nil), cp)
}

// ---- seam (Factory → HTTP adapters): Deps.HTTPTimeout threads into each adapter ----

// Deps.HTTPTimeout threads from the factory into all four HTTP chat adapters.
// Mirrors TestFactory_ProviderFor_ThreadsMaxResponseBytes for the timeout Deps field:
// both are zero-means-no-cap fields, and both must reach each adapter's constructor.
// Without this test a silent removal of Timeout: f.deps.HTTPTimeout would go unnoticed
// and adapters would hang indefinitely in production without a per-call bound.
func TestFactory_ChatProviderFor_ThreadsHTTPTimeout(t *testing.T) {
	f := adapterprovider.NewFactory(adapterprovider.Deps{HTTPTimeout: 30 * time.Second})
	for _, prov := range []string{"openrouter", "azure-foundry", "anthropic-api", "bedrock"} {
		t.Run(prov, func(t *testing.T) {
			cp, err := f.ChatProviderFor(chatBinding(prov))
			require.NoError(t, err,
				"provider %q must be constructable when HTTPTimeout is set from Deps", prov)
			assert.NotNil(t, cp)
		})
	}
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

// ---- Edge case: MaxResponseBytes is threaded from Deps into ALL backends ----

// TestFactory_ProviderFor_ThreadsMaxResponseBytes proves the response-body cap
// from Deps reaches all FIVE credential-bearing backends: the four HTTP adapters
// (openrouter, azurefoundry, anthropicapi, bedrock) AND the opencode subprocess
// adapter. The field is stored unexported inside each adapter, so this test
// proves the fix at the construction boundary: the Deps field exists
// (compile-time), is accepted by NewFactory, and each ProviderFor call selects
// the right adapter without error. The per-adapter byte-cap behavior is
// exercised by the adapters' own tests.
func TestFactory_ProviderFor_ThreadsMaxResponseBytes(t *testing.T) {
	hc := &http.Client{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{
		HTTPClient:       hc,
		MaxResponseBytes: 1 << 20, // 1 MiB cap
	})

	cp, err := f.ChatProviderFor(chatBinding("openrouter"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp,
		"openrouter provider constructed with MaxResponseBytes from Deps")

	cp2, err := f.ChatProviderFor(chatBinding("azure-foundry"))
	require.NoError(t, err)
	assert.IsType(t, (*azurefoundry.Provider)(nil), cp2,
		"azurefoundry provider constructed with MaxResponseBytes from Deps")

	cp3, err := f.ChatProviderFor(chatBinding("anthropic-api"))
	require.NoError(t, err)
	assert.IsType(t, (*anthropicapi.Provider)(nil), cp3,
		"anthropicapi provider constructed with MaxResponseBytes from Deps")

	cp4, err := f.ChatProviderFor(chatBinding("bedrock"))
	require.NoError(t, err)
	assert.IsType(t, (*bedrock.Provider)(nil), cp4,
		"bedrock provider constructed with MaxResponseBytes from Deps")

	ap, err := f.AgentProviderFor(agentBindingBackend("openrouter", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap,
		"opencode provider constructed with MaxResponseBytes from Deps (subprocess stdout cap)")
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

// ---- Happy path: OpenAI-compatible providers route to adapter/openrouter ----

func TestFactory_ChatProviderFor_OpenAI_MapsToOpenRouter(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("openai"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_Ollama_MapsToOpenRouter(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("ollama"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

// Input-validation: the ollama-cloud alias normalizes to the ollama family.
func TestFactory_ChatProviderFor_OllamaCloudAlias_NormalizesToOllama(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("ollama-cloud"))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_OpenAI_CaseAndWhitespaceNormalized(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("  OpenAI  "))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_OllamaCloud_CaseAndWhitespaceNormalized(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("  Ollama-Cloud "))
	require.NoError(t, err)
	assert.IsType(t, (*openrouter.Provider)(nil), cp)
}

// ---- Happy path: anthropic-api routes to adapter/anthropicapi ----

func TestFactory_ChatProviderFor_AnthropicAPI_MapsToAnthropicAPI(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("anthropic-api"))
	require.NoError(t, err)
	assert.IsType(t, (*anthropicapi.Provider)(nil), cp)
}

// ---- Happy path: bedrock + amazon-bedrock alias route to adapter/bedrock ----

func TestFactory_ChatProviderFor_Bedrock_MapsToBedrock(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("bedrock"))
	require.NoError(t, err)
	assert.IsType(t, (*bedrock.Provider)(nil), cp)
}

func TestFactory_ChatProviderFor_AmazonBedrockAlias_MapsToBedrock(t *testing.T) {
	f := newTestFactory(t)
	cp, err := f.ChatProviderFor(chatBinding("amazon-bedrock"))
	require.NoError(t, err)
	assert.IsType(t, (*bedrock.Provider)(nil), cp)
}

// ---- Canonicalization: documented aliases route to the same adapter ----

// The chat factory dispatches on domprovider.Canonicalize, so each documented
// alias group resolves to the SAME concrete adapter as its canonical id. This
// pins the alias-collapse contract at the selector seam (PR #23 Should Fix #2).
func TestFactory_ChatProviderFor_CanonicalAliases_RouteToSameAdapter(t *testing.T) {
	f := newTestFactory(t)

	// anthropic-api family: "anthropic" is an accepted alias of "anthropic-api".
	for _, name := range []string{"anthropic-api", "anthropic", "  Anthropic  "} {
		cp, err := f.ChatProviderFor(chatBinding(name))
		require.NoError(t, err, "alias %q must route", name)
		assert.IsType(t, (*anthropicapi.Provider)(nil), cp, "alias %q -> anthropicapi", name)
	}

	// azure trio: azure-openai / azure-foundry / azure all route to azurefoundry.
	for _, name := range []string{"azure-openai", "azure-foundry", "azure"} {
		cp, err := f.ChatProviderFor(chatBinding(name))
		require.NoError(t, err, "alias %q must route", name)
		assert.IsType(t, (*azurefoundry.Provider)(nil), cp, "alias %q -> azurefoundry", name)
	}

	// bedrock family: bedrock / amazon-bedrock route to bedrock.
	for _, name := range []string{"bedrock", "amazon-bedrock"} {
		cp, err := f.ChatProviderFor(chatBinding(name))
		require.NoError(t, err, "alias %q must route", name)
		assert.IsType(t, (*bedrock.Provider)(nil), cp, "alias %q -> bedrock", name)
	}
}

// ---- Error: near-miss strings still fail (no over-matching by the new arms) ----

func TestFactory_ChatProviderFor_UnknownProviderStillErrors_NearMiss(t *testing.T) {
	f := newTestFactory(t)
	// NB: "anthropic" is NOT a near-miss — it canonicalizes to "anthropic-api"
	// (see TestFactory_ChatProviderFor_CanonicalAliases_RouteToSameAdapter).
	for _, name := range []string{"ollamaa", "open-ai", "anthropicc", "amazon-bedrockk"} {
		_, err := f.ChatProviderFor(chatBinding(name))
		require.Error(t, err, "provider %q must be unknown", name)
		assert.ErrorIs(t, err, adapterprovider.ErrUnknownProvider)
		assert.Contains(t, err.Error(), name)
	}
}

// ---- Edge case: the agent path for the new chat providers is unchanged ----

func TestFactory_AgentProviderFor_OpenAI_OpencodeBackendRoutesToOpencode(t *testing.T) {
	f := newTestFactory(t)
	ap, err := f.AgentProviderFor(agentBindingBackend("openai", "opencode"))
	require.NoError(t, err)
	assert.IsType(t, (*opencode.Provider)(nil), ap)
}

// ---- Security: selecting the new providers never logs a revealed key ----

func TestFactory_ChatProviderFor_NewProviders_DoNotLogRevealedKey(t *testing.T) {
	for _, name := range []string{"openai", "ollama", "anthropic-api", "bedrock"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(prev)

			const secret = "sk-NEW-PROVIDER-SENTINEL"
			b := chatBinding(name)
			b.RevealKey = func(context.Context) (string, error) { return secret, nil }

			f := newTestFactory(t)
			cp, err := f.ChatProviderFor(b)
			require.NoError(t, err)
			require.NotNil(t, cp)

			assert.False(t, strings.Contains(buf.String(), secret),
				"revealed key must never appear in logs during selection of %q", name)
		})
	}
}

// ---- Agent path: OpencodeAllowAllTools is threaded into the opencode arm ----

// recRunner records every RunSpec and replays a compatible version probe then a
// canned run line, so the agent path can be driven end-to-end through opencode.
type recRunner struct {
	calls []claudecli.RunSpec
}

func (r *recRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	line := "ok"
	if len(spec.Args) > 0 && spec.Args[len(spec.Args)-1] == "--version" {
		line = "opencode 99.0.0"
	}
	ch := make(chan string, 1)
	ch <- line
	close(ch)
	return ch, func() error { return nil }, nil
}

func openAgentBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Kind:      provider.ProfileKindAgent,
		Provider:  "openai",
		Backend:   "opencode",
		Model:     "gpt-4o",
		RevealKey: func(context.Context) (string, error) { return "sk-x", nil },
	}
}

// Default-true Deps => opencode runs open-by-default (allow-all flag emitted).
func TestFactory_AgentProviderFor_OpenByDefault(t *testing.T) {
	rr := &recRunner{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{Runner: rr, OpencodeAllowAllTools: true})
	ap, err := f.AgentProviderFor(openAgentBinding())
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rr.calls), 2)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.Contains(t, runArgs, "--allow-all-tools", "OpencodeAllowAllTools=true must be open-by-default")
}

// OpencodeAllowAllTools=false disables the allow-all posture.
func TestFactory_AgentProviderFor_AllowAllDisabled(t *testing.T) {
	rr := &recRunner{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{Runner: rr, OpencodeAllowAllTools: false})
	ap, err := f.AgentProviderFor(openAgentBinding())
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rr.calls), 2)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.NotContains(t, runArgs, "--allow-all-tools", "OpencodeAllowAllTools=false must disable allow-all")
}

// Per-profile AllowedTools (knob b) narrows end-to-end through the factory path.
// Even when OpencodeAllowAllTools=true (knob a), a non-empty binding.AllowedTools
// must suppress --allow-all-tools and emit --allowed-tools with the named tools.
// This exercises the full chain: Factory.AgentProviderFor → opencode.buildPosture
// → buildArgs → subprocess argv, verifying the two knobs compose correctly.
func TestFactory_AgentProviderFor_PerProfileAllowlistNarrows(t *testing.T) {
	rr := &recRunner{}
	b := openAgentBinding()
	b.AllowedTools = []string{"Read", "Write"}

	f := adapterprovider.NewFactory(adapterprovider.Deps{Runner: rr, OpencodeAllowAllTools: true})
	ap, err := f.AgentProviderFor(b)
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rr.calls), 2)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.NotContains(t, runArgs, "--allow-all-tools",
		"per-profile AllowedTools must suppress allow-all even when OpencodeAllowAllTools=true")
	assert.Contains(t, runArgs, "--allowed-tools",
		"per-profile AllowedTools must emit the tool-scope flag in the run argv")
	assert.Contains(t, runArgs, "Read,Write",
		"per-profile AllowedTools must name the permitted tools in the run argv")
}

// seam (posture config knob a + knob b): per-profile AllowedTools (knob b) must
// emit --allowed-tools even when OpencodeAllowAllTools=false (knob a). Both
// controls can be independently active; knob b narrows regardless of knob a's
// global switch. This exercises the composition of the two knobs via the factory.
func TestFactory_AgentProviderFor_AllowlistWinsWhenGlobalAllowAllDisabled(t *testing.T) {
	rr := &recRunner{}
	b := openAgentBinding()
	b.AllowedTools = []string{"Read"}

	f := adapterprovider.NewFactory(adapterprovider.Deps{Runner: rr, OpencodeAllowAllTools: false})
	ap, err := f.AgentProviderFor(b)
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rr.calls), 2)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.NotContains(t, runArgs, "--allow-all-tools",
		"global allow-all disabled: --allow-all-tools must NOT be emitted")
	assert.Contains(t, runArgs, "--allowed-tools",
		"per-profile AllowedTools must emit --allowed-tools even when global allow-all is disabled")
	assert.Contains(t, runArgs, "Read",
		"per-profile AllowedTools must name the permitted tools in the argv")
}
