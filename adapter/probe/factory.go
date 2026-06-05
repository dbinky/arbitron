package probe

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// Canonical provider strings after normalization (trim + lowercase). These
// mirror adapter/provider/factory.go's constants so the two selectors agree.
const (
	providerClaudeCLI     = "claude-cli"
	providerOpenAI        = "openai"
	providerOpenRouter    = "openrouter"
	providerLiteLLM       = "litellm"
	providerOllama        = "ollama"
	providerOllamaCloud   = "ollama-cloud"
	providerAzureOpenAI   = "azure-openai"
	providerAzureFoundry  = "azure-foundry"
	providerAnthropicAPI  = "anthropic-api"
	providerBedrock       = "bedrock"
	providerAmazonBedrock = "amazon-bedrock"
)

// Deps carries the shared construction inputs threaded into each probe.
type Deps struct {
	// HTTPClient is used by the HTTP probes; nil -> a default *http.Client.
	HTTPClient *http.Client
	// Timeout bounds each probe call; 0 -> no per-call timeout.
	Timeout time.Duration
	// MaxResponseBytes caps each probe's response read (OOM backstop); 0 -> no cap.
	MaxResponseBytes int64
	// Clock supplies the SigV4 signing time for the Bedrock probe; nil -> time.Now.
	Clock func() time.Time
	// Runner is the subprocess seam for the Claude CLI probe; nil -> the shared
	// production os/exec runner (claudecli.NewExecRunner()).
	Runner claudecli.CommandRunner
	// ClaudeBinaryPath overrides PATH lookup for the claude binary; "" -> "claude".
	ClaudeBinaryPath string
	// BedrockEndpoint is a TEST-ONLY seam: it derives the Bedrock control-plane
	// host for a region. nil -> the real https://bedrock.{region}.amazonaws.com
	// derivation. Set in tests to point the probe at an httptest server.
	BedrockEndpoint func(region string) string
}

// ProbeFactory selects a concrete ConnectionProbe per resolved binding. It
// implements provider.ProbeSelector, so the CLI depends on the domain port and
// never imports this package's probe types directly.
type ProbeFactory struct {
	deps Deps
}

// NewProbeFactory constructs a ProbeFactory from its shared dependencies.
func NewProbeFactory(deps Deps) *ProbeFactory { return &ProbeFactory{deps: deps} }

var _ domprovider.ProbeSelector = (*ProbeFactory)(nil)

func normalizeProvider(p string) string { return strings.ToLower(strings.TrimSpace(p)) }

// ProbeFor dispatches by binding.Provider. The empty/claude-cli path returns the
// reachability-only CLI probe; OpenAI-compatible families return the
// {base}/models probe; anthropic-api and bedrock return their dedicated probes.
// Unknown providers return ErrNoProbeForProvider.
//
// SECURITY/contract: dispatch is purely on Provider — Kind and Model are never
// read (a connection-built binding may leave both empty).
func (f *ProbeFactory) ProbeFor(b domprovider.ResolvedBinding) (domprovider.ConnectionProbe, error) {
	switch normalizeProvider(b.Provider) {
	case "", providerClaudeCLI:
		return newClaudeCLIProbe(f.deps), nil
	case providerOpenAI, providerOpenRouter, providerLiteLLM,
		providerOllama, providerOllamaCloud, providerAzureOpenAI, providerAzureFoundry:
		return newOpenAICompatProbe(f.deps), nil
	case providerAnthropicAPI:
		return newAnthropicProbe(f.deps), nil
	case providerBedrock, providerAmazonBedrock:
		return newBedrockProbe(f.deps), nil
	default:
		return nil, fmt.Errorf("%w: %q", domprovider.ErrNoProbeForProvider, b.Provider)
	}
}
