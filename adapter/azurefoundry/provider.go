// Package azurefoundry implements a native engine.ChatProvider for Azure that
// serves BOTH Azure topologies through a single adapter with no provider-name
// special-casing — the difference lives entirely in the consumed binding:
//
//   - Azure OpenAI resource (GPT-family): one resource endpoint + key shared
//     across many deployments. The request path is
//     {base-url}/openai/deployments/{deployment}/chat/completions?api-version=<v>
//     with the deployment taken from the binding Model and api-version from
//     Extras.
//   - Azure AI Foundry serverless (open-weight catalog): each deployment has its
//     own endpoint + key, so base-url IS the deployment endpoint.
//
// Streaming (SSE -> ChatChunk), structured output (response_format json_schema),
// and token usage are all native and mirror the openrouter adapter's contract.
package azurefoundry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelExecutionEvidenceUnavailable rejects required exact-model execution:
// an Azure deployment name is not proof of the model behind it, and this
// adapter has no verified producer of the executed model identity (B5).
var ErrModelExecutionEvidenceUnavailable = errors.New("azurefoundry: model execution evidence unavailable")

// defaultMaxResponseBytes bounds a non-streaming response read when the caller
// does not set Config.MaxResponseBytes.
const defaultMaxResponseBytes int64 = 8 << 20 // 8 MiB

// Config configures a Provider. The HTTPClient transport is the test seam:
// inject a custom http.RoundTripper so tests exercise the adapter with no live
// network.
type Config struct {
	Binding          provider.ResolvedBinding // BaseURL, Extras (api-version), Model (deployment), RevealKey
	HTTPClient       *http.Client             // nil -> default; transport is the test seam
	Timeout          time.Duration
	MaxResponseBytes int64
}

// Provider implements engine.ChatProvider over the Azure chat-completions API.
type Provider struct {
	binding          provider.ResolvedBinding
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
}

var _ engine.ChatProvider = (*Provider)(nil)

// New constructs a Provider from cfg. A nil HTTPClient uses http.DefaultClient.
// A zero Timeout means no per-call deadline; a zero MaxResponseBytes uses the
// package default.
func New(cfg Config) *Provider {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	maxBytes := cfg.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	return &Provider{
		binding:          cfg.Binding,
		httpClient:       client,
		timeout:          cfg.Timeout,
		maxResponseBytes: maxBytes,
	}
}

// revealKey fetches the connection key via the lazy binding RevealKey. A nil
// RevealKey (no-key path) yields an empty key.
func (p *Provider) revealKey(ctx context.Context) (string, error) {
	if p.binding.RevealKey == nil {
		return "", nil
	}
	return p.binding.RevealKey(ctx)
}

// Chat performs a synchronous chat-completions request and returns the terminal
// response with native token usage.
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if req.RequireExecutedModel || p.binding.RequiresModelEvidence() ||
		(p.binding.Pinned && p.binding.OpaquePinRoute != provider.CanonicalAzure) {
		return nil, ErrModelExecutionEvidenceUnavailable
	}
	urlStr, err := endpointURL(p.binding)
	if err != nil {
		return nil, err
	}
	body, err := buildBody(req, p.binding.Model, false)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()

	httpReq, err := p.newRequest(callCtx, urlStr, body)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azurefoundry: chat request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := readBounded(resp.Body, p.maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("azurefoundry: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiErrorFromBody(resp.StatusCode, raw)
	}

	decoded, err := decodeChatResponse(raw)
	if err != nil {
		return nil, err
	}

	return &engine.ChatResponse{
		Output:    decoded.content(),
		Model:     modelOr(decoded.Model, p.binding.Model),
		TokensIn:  decoded.Usage.PromptTokens,
		TokensOut: decoded.Usage.CompletionTokens,
	}, nil
}

// StreamChat performs a streaming chat-completions request and returns a channel
// of token chunks. The final chunk has Done=true and carries token usage from
// the SSE usage frame. Any failure is delivered as a chunk with a non-nil Error
// before the channel closes.
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if req.RequireExecutedModel || p.binding.RequiresModelEvidence() ||
		(p.binding.Pinned && p.binding.OpaquePinRoute != provider.CanonicalAzure) {
		return nil, ErrModelExecutionEvidenceUnavailable
	}
	urlStr, err := endpointURL(p.binding)
	if err != nil {
		return nil, err
	}
	body, err := buildBody(req, p.binding.Model, true)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	httpReq, err := p.newRequest(callCtx, urlStr, body)
	if err != nil {
		cancel()
		return nil, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("azurefoundry: stream request: %w", err)
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := readBounded(resp.Body, p.maxResponseBytes)
			sendChunk(callCtx, out, engine.ChatChunk{Error: apiErrorFromBody(resp.StatusCode, raw)})
			return
		}

		streamSSE(callCtx, resp.Body, p.maxResponseBytes, out)
	}()

	return out, nil
}

// modelOr returns echoed if non-empty, else fallback.
func modelOr(echoed, fallback string) string {
	if echoed != "" {
		return echoed
	}
	return fallback
}

// sendChunk delivers a chunk unless ctx is done, in which case it is dropped.
func sendChunk(ctx context.Context, out chan<- engine.ChatChunk, c engine.ChatChunk) {
	select {
	case out <- c:
	case <-ctx.Done():
	}
}

// readBounded reads up to max bytes from r and errors if the source exceeds the
// cap. The error never contains any request data.
func readBounded(r io.Reader, max int64) ([]byte, error) {
	limited := io.LimitReader(r, max+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("azurefoundry: response exceeds max bytes")
	}
	return b, nil
}
