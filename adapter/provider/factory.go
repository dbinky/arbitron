package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/anthropicapi"
	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/adapter/bedrock"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/openrouter"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrUnknownProvider is returned when a ResolvedBinding names a chat provider
// that has no adapter mapping. (Agent bindings route by backend, not provider.)
var ErrUnknownProvider = errors.New("provider: no adapter for provider")

// ErrUnknownBackend is returned by AgentProviderFor when a binding's effective
// agent backend is neither claude-code nor opencode. It is defense-in-depth: the
// workflow validator already rejects a bad backend at load (fail-closed), but
// the factory must never silently fall through to a wrong adapter.
var ErrUnknownBackend = errors.New("provider: unknown agent backend")

// Chat-dispatch provider ids, matched after domprovider.Canonicalize. The
// azure/bedrock/anthropic-api alias groups are collapsed to a single canonical
// id by Canonicalize (so the arms below use domprovider.Canonical* rather than
// re-listing each spelling); the OpenAI-compatible ids canonicalize to
// themselves and are listed explicitly here.
const (
	providerClaudeCLI  = ProviderClaudeCLI
	providerOpenRouter = "openrouter"
	providerLiteLLM    = "litellm"
	// providerOpenAI and providerOllama route to the OpenAI-compatible
	// adapter/openrouter chat adapter. Local vs cloud Ollama are distinguished
	// by the connection's BaseURL + presence/absence of a key, not by the
	// provider string.
	providerOpenAI = "openai"
	providerOllama = "ollama"
	// providerOllamaCloud is an accepted alias for the ollama family.
	providerOllamaCloud = "ollama-cloud"
)

// Deps carries the shared construction inputs the Factory threads into each
// concrete adapter. The claude-cli adapters are pre-constructed (they share the
// workspace-pin / runner config built once in the composition root); the HTTP
// client and CommandRunner are injected into the per-binding adapters the
// Factory builds on demand.
type Deps struct {
	// ClaudeChat is the tool-suppressed claude-cli chat adapter used for the
	// empty/claude-cli provider path. Required for that path.
	ClaudeChat engine.ChatProvider
	// ClaudeAgent is a pre-built tools-enabled claude-cli agent adapter used as
	// the fallback for the claude-code backend when ClaudeAgentConfig is not
	// supplied (subscription-only path). org-readiness Phase 2 prefers
	// ClaudeAgentConfig so the claude-code arm builds a PER-BINDING adapter that
	// injects the endpoint+key; this field is retained for back-compat / tests.
	ClaudeAgent engine.AgentProvider
	// ClaudeAgentConfig is the claude-cli agent Config (binary, workspace root,
	// timeout, allowed-tools, skip-permissions, max-response-bytes, runner). When
	// supplied (org-readiness Phase 2), the claude-code arm constructs a
	// per-binding AgentAdapter (claudecli.NewAgentAdapterForBinding) so a
	// claude-code backend can target the org gateway or any configured provider:
	// ANTHROPIC_BASE_URL + key are injected into the subprocess env per call. An
	// empty binding through this path degrades to the subscription env.
	ClaudeAgentConfig claudecli.Config
	// HTTPClient is injected into the openrouter / azurefoundry adapters. A nil
	// client lets each adapter fall back to its own default.
	HTTPClient *http.Client
	// Runner is the subprocess seam injected into the opencode adapter. A nil
	// runner lets opencode fall back to the shared production os/exec runner.
	Runner claudecli.CommandRunner
	// HTTPTimeout bounds each HTTP adapter call; 0 means no per-call timeout.
	HTTPTimeout time.Duration
	// OpencodeBinaryPath overrides PATH lookup for the opencode binary; "" lets
	// the adapter resolve it.
	OpencodeBinaryPath string
	// OpencodeTimeout is the default per-mission opencode timeout; <=0 means no
	// default.
	OpencodeTimeout time.Duration
	// MaxResponseBytes caps the per-request response/stdout read across ALL the
	// credential-bearing backends: the HTTP adapters (openrouter, azurefoundry,
	// anthropicapi, bedrock) bound their response-body read, and opencode bounds
	// its subprocess stdout. 0 means no cap. SECURITY: a non-zero cap bounds the
	// OOM DoS from an attacker-pointed base_url (or hostile subprocess) streaming
	// an unbounded body.
	MaxResponseBytes int64
	// OpencodeAllowAllTools is the operator knob (a) for the agent path's
	// permission posture. SECURITY: it defaults to false (CLOSED) in the
	// composition root; a true value is an explicit operator opt-in that grants
	// every opencode-driven provider an allow-all tool posture (RCE-equivalent on
	// model-influenced prompts). Per-profile ResolvedBinding.AllowedTools
	// (knob (b)) narrows independently of this switch.
	OpencodeAllowAllTools bool
}

// Factory holds the shared construction inputs and selects a concrete backend
// per resolved binding. It implements engine.ChatProviderSelector and
// engine.AgentProviderSelector, so the executors depend on the domain selector
// ports and never import these adapter packages.
//
// INVARIANT (tier routing): the native HTTP adapters (openrouter/azurefoundry/
// anthropicapi/bedrock) are CHAT-TIER ONLY — they are reachable only through
// ChatProviderFor, which dispatches on the PROVIDER. The AGENT tier
// (AgentProviderFor) dispatches on the BACKEND axis, NOT the provider
// (org-readiness Phase 2): claude-code -> the claude-cli agent adapter (which
// may now carry a non-claude connection; endpoint+key injected in the adapter),
// opencode -> adapter/opencode. There is no native-HTTP agent adapter, so a
// provider's chat credentials/env flow through the HTTP adapter on the chat path
// but through opencode's subprocess env on the agent path — the two opencode
// paths must agree on the provider vocabulary (see domprovider.Canonicalize),
// which is why an env/-m mismatch in opencode is a live bug rather than a dead
// branch. The backend and provider are now ORTHOGONAL.
type Factory struct {
	deps Deps
}

// NewFactory constructs a Factory from deps.
func NewFactory(deps Deps) *Factory {
	return &Factory{deps: deps}
}

// claudeAgentConfigSupplied reports whether a usable claude agent Config was
// wired (so the claude-code arm builds a per-binding adapter). A bare zero-value
// Config (no runner, no binary path, no workspace root) is treated as "not
// supplied" so test factories that wire only Deps.ClaudeAgent keep the pre-built
// fallback. Production always sets at least a Runner / BinaryPath / WorkspaceRoot.
func claudeAgentConfigSupplied(cfg claudecli.Config) bool {
	return cfg.Runner != nil || cfg.BinaryPath != "" || cfg.WorkspaceRoot != ""
}

var (
	_ engine.ChatProviderSelector  = (*Factory)(nil)
	_ engine.AgentProviderSelector = (*Factory)(nil)
)

// ChatProviderFor returns the concrete engine.ChatProvider for a chat-kind
// binding, selected by binding.Provider. An empty (or "claude-cli") provider
// returns the claude-cli ChatAdapter; openrouter/litellm and the
// OpenAI-compatible openai/ollama (incl. the ollama-cloud alias) map to
// adapter/openrouter; azure-openai/azure-foundry map to adapter/azurefoundry;
// anthropic-api maps to adapter/anthropicapi; bedrock (incl. the amazon-bedrock
// alias) maps to adapter/bedrock. Unknown providers return ErrUnknownProvider.
func (f *Factory) ChatProviderFor(b domprovider.ResolvedBinding) (engine.ChatProvider, error) {
	// Dispatch on the domain-canonical id so the chat/agent/probe selectors and
	// the opencode path all share ONE provider vocabulary (alias collapse lives
	// once in domprovider.Canonicalize, not forked per selector).
	switch domprovider.Canonicalize(b.Provider) {
	case "", providerClaudeCLI:
		if f.deps.ClaudeChat == nil {
			return nil, errors.New("provider: claude-cli chat adapter is not configured for the empty-provider path")
		}
		return f.deps.ClaudeChat, nil
	case providerOpenRouter, providerLiteLLM, providerOpenAI, providerOllama, providerOllamaCloud:
		return openrouter.New(openrouter.Config{
			Binding:          b,
			HTTPClient:       f.deps.HTTPClient,
			Timeout:          f.deps.HTTPTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
		}), nil
	case domprovider.CanonicalAzure:
		return azurefoundry.New(azurefoundry.Config{
			Binding:          b,
			HTTPClient:       f.deps.HTTPClient,
			Timeout:          f.deps.HTTPTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
		}), nil
	case domprovider.CanonicalAnthropicAPI:
		return anthropicapi.New(anthropicapi.Config{
			Binding:          b,
			HTTPClient:       f.deps.HTTPClient,
			Timeout:          f.deps.HTTPTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
		}), nil
	case domprovider.CanonicalBedrock:
		// Now is left nil here so the adapter uses time.Now in production; tests
		// inject a fixed clock through the adapter's own Config.
		return bedrock.New(bedrock.Config{
			Binding:          b,
			HTTPClient:       f.deps.HTTPClient,
			Timeout:          f.deps.HTTPTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
		}), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, b.Provider)
	}
}

// AgentProviderFor returns the concrete engine.AgentProvider for an agent-kind
// binding, dispatching on the BACKEND axis (b.Backend), NOT the provider
// (org-readiness Phase 2). The backend is INDEPENDENT of the provider: a
// claude-code binding may carry any connection (endpoint+key injected in the
// claude-cli adapter), and an opencode binding may carry a claude-cli provider.
//
//   - claude-code -> the claude-cli AgentAdapter.
//   - opencode    -> adapter/opencode, with the binding's per-provider env.
//   - "" (unset) / any other value -> ErrUnknownBackend (fail-closed; never a
//     silent fallback). The deployment default is owned by ONE place — the config
//     floor (adapter/config loader) — and is applied by the AgentCallExecutor
//     before it reaches the factory, so a real-path binding is never empty here.
//     The factory deliberately does NOT re-default: a future non-executor caller
//     gets a clear ErrUnknownBackend telling it to set a backend, rather than a
//     second silent default to hunt down when the policy changes.
func (f *Factory) AgentProviderFor(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
	// Trim whitespace only (NOT lowercase): the allowed vocabulary is exact-match
	// lowercase, identical to the workflow validator. A near-miss case (e.g.
	// "OPENCODE") was already rejected at load and must stay unknown here so the
	// factory and validator share one strict vocabulary (fail-closed).
	backend := strings.TrimSpace(b.Backend)
	switch backend {
	case workflow.BackendClaudeCode:
		// org-readiness Phase 2: when a claude agent Config is supplied, build a
		// PER-BINDING adapter so ANTHROPIC_BASE_URL + key (from b) are injected
		// into the subprocess env per call (NewAgentAdapterForBinding). An empty
		// binding through this path degrades to the subscription env. The
		// pre-built Deps.ClaudeAgent is the back-compat fallback (subscription
		// only) when no Config is wired.
		if claudeAgentConfigSupplied(f.deps.ClaudeAgentConfig) {
			return claudecli.NewAgentAdapterForBinding(f.deps.ClaudeAgentConfig, b), nil
		}
		if f.deps.ClaudeAgent == nil {
			return nil, errors.New("provider: claude-cli agent adapter is not configured for the claude-code backend")
		}
		return f.deps.ClaudeAgent, nil
	case workflow.BackendOpencode:
		// Fail-closed: a provider opencode cannot drive (unknown to its provider
		// map) surfaces a clear agent error at RunAgent time naming the provider;
		// it is NEVER a silent tool-less run. AllowAllTools carries the
		// open-by-default posture (knob (a)); per-profile AllowedTools (knob (b))
		// narrows independently inside opencode.
		return opencode.New(opencode.Config{
			Binding:          b,
			BinaryPath:       f.deps.OpencodeBinaryPath,
			Runner:           f.deps.Runner,
			Timeout:          f.deps.OpencodeTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
			AllowAllTools:    f.deps.OpencodeAllowAllTools,
		}), nil
	default:
		// Fail-closed: an unrecognized (or empty-with-empty-default) backend never
		// silently picks an adapter. %q safely quotes a hostile value.
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, b.Backend)
	}
}
