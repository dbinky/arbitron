package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/openrouter"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// ErrUnknownProvider is returned when a ResolvedBinding names a chat provider
// that has no adapter mapping. (Agent bindings only ever route to claude-cli or
// opencode, so they cannot produce this error.)
var ErrUnknownProvider = errors.New("provider: no adapter for provider")

// Canonical provider strings, after normalization (trim + lowercase).
const (
	providerClaudeCLI    = "claude-cli"
	providerOpenRouter   = "openrouter"
	providerLiteLLM      = "litellm"
	providerAzureOpenAI  = "azure-openai"
	providerAzureFoundry = "azure-foundry"
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
	// ClaudeAgent is the tools-enabled claude-cli agent adapter used for the
	// empty/claude-cli provider path. Required for that path.
	ClaudeAgent engine.AgentProvider
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
	// MaxResponseBytes caps the per-request response body read in HTTP adapters
	// (openrouter, azurefoundry). 0 means no cap.
	MaxResponseBytes int64
}

// Factory holds the shared construction inputs and selects a concrete backend
// per resolved binding. It implements engine.ChatProviderSelector and
// engine.AgentProviderSelector, so the executors depend on the domain selector
// ports and never import these adapter packages.
type Factory struct {
	deps Deps
}

// NewFactory constructs a Factory from deps.
func NewFactory(deps Deps) *Factory {
	return &Factory{deps: deps}
}

var (
	_ engine.ChatProviderSelector  = (*Factory)(nil)
	_ engine.AgentProviderSelector = (*Factory)(nil)
)

// normalizeProvider trims surrounding whitespace and lowercases the provider
// string so configured values are matched case- and whitespace-insensitively.
// A whitespace-only value normalizes to empty (the subscription path).
func normalizeProvider(p string) string {
	return strings.ToLower(strings.TrimSpace(p))
}

// ChatProviderFor returns the concrete engine.ChatProvider for a chat-kind
// binding, selected by binding.Provider. An empty (or "claude-cli") provider
// returns the claude-cli ChatAdapter; openrouter/litellm map to adapter/openrouter;
// azure-openai/azure-foundry map to adapter/azurefoundry. Unknown providers
// return ErrUnknownProvider.
func (f *Factory) ChatProviderFor(b domprovider.ResolvedBinding) (engine.ChatProvider, error) {
	switch normalizeProvider(b.Provider) {
	case "", providerClaudeCLI:
		if f.deps.ClaudeChat == nil {
			return nil, errors.New("provider: claude-cli chat adapter is not configured for the empty-provider path")
		}
		return f.deps.ClaudeChat, nil
	case providerOpenRouter, providerLiteLLM:
		return openrouter.New(openrouter.Config{
			Binding:          b,
			HTTPClient:       f.deps.HTTPClient,
			Timeout:          f.deps.HTTPTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
		}), nil
	case providerAzureOpenAI, providerAzureFoundry:
		return azurefoundry.New(azurefoundry.Config{
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
// binding: an empty (or "claude-cli") provider returns the claude-cli
// AgentAdapter; any other (non-Claude) provider routes to adapter/opencode,
// which carries the per-provider credential env contract.
func (f *Factory) AgentProviderFor(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
	switch normalizeProvider(b.Provider) {
	case "", providerClaudeCLI:
		if f.deps.ClaudeAgent == nil {
			return nil, errors.New("provider: claude-cli agent adapter is not configured for the empty-provider path")
		}
		return f.deps.ClaudeAgent, nil
	default:
		return opencode.New(opencode.Config{
			Binding:    b,
			BinaryPath: f.deps.OpencodeBinaryPath,
			Runner:     f.deps.Runner,
			Timeout:    f.deps.OpencodeTimeout,
		}), nil
	}
}
