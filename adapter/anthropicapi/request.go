package anthropicapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// anthropicVersion is the pinned Anthropic API version header value. Bumping it
// is a deliberate, reviewed change — never derived from config or upstream.
const anthropicVersion = "2023-06-01"

// defaultBaseURL is used when the binding omits a BaseURL.
const defaultBaseURL = "https://api.anthropic.com"

// messagesPath is appended to the (trimmed) base-url.
const messagesPath = "/v1/messages"

// defaultMaxTokens is supplied when ChatRequest.MaxTokens == 0, because Anthropic
// REQUIRES max_tokens. Sane mid-range default.
const defaultMaxTokens = 4096

// forcedToolName is the name of the single tool emitted to force structured
// output (Anthropic has no response_format). The model is constrained to call
// this tool, whose input_schema is the requested OutputSchema; the decoder
// extracts that tool-use input as the raw output string.
const forcedToolName = "structured_output"

// ErrMissingAPIKey is the configuration-class sentinel returned (before any
// network call) when the binding has no usable API key. Anthropic always
// requires a key.
var ErrMissingAPIKey = errors.New("anthropicapi: missing API key (x-api-key required)")

// ErrUnsupportedScheme is returned when the binding's BaseURL uses a scheme other
// than http/https. Defensive belt-and-suspenders at request build so a non-web
// scheme (file, gopher, ...) can never reach the HTTP client even if a binding
// bypassed the write-path SSRF policy.
var ErrUnsupportedScheme = errors.New("anthropicapi: base_url scheme must be http or https")

// buildBody assembles the Anthropic Messages JSON body from a ChatRequest.
// max_tokens is ALWAYS present (default when zero). The top-level system field is
// set only when SystemPrompt is non-empty; temperature only when non-zero; stream
// only when streaming. Structured output is forced via a single tool whose
// input_schema is the OutputSchema (Anthropic has no response_format).
func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []message{
			{Role: "user", Content: req.Prompt},
		},
	}
	if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if stream {
		body["stream"] = true
	}
	if req.OutputSchema != nil {
		body["tools"] = []map[string]any{
			{
				"name":         forcedToolName,
				"description":  "Emit the structured output conforming to the provided schema.",
				"input_schema": req.OutputSchema,
			},
		}
		body["tool_choice"] = map[string]any{
			"type": "tool",
			"name": forcedToolName,
		}
	}

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropicapi: marshal request body: %w", err)
	}
	return out, nil
}

// newRequest builds the authenticated POST to {base-url}/v1/messages. The
// revealed key is fetched from the binding's lazy RevealKey here and used only to
// populate the x-api-key header; it is never logged. A missing key fails closed
// (ErrMissingAPIKey) BEFORE the transport is ever invoked.
func (p *Provider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	target := resolveBaseURL(p.binding.BaseURL) + messagesPath

	if err := checkScheme(target); err != nil {
		return nil, err
	}

	key, err := p.revealKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("anthropicapi: reveal key: %w", err)
	}
	if strings.TrimSpace(key) == "" {
		return nil, ErrMissingAPIKey
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropicapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	return httpReq, nil
}

// resolveBaseURL returns raw trimmed of trailing slashes, or defaultBaseURL when
// raw is empty.
func resolveBaseURL(raw string) string {
	trimmed := strings.TrimRight(raw, "/")
	if trimmed == "" {
		return defaultBaseURL
	}
	return trimmed
}

// checkScheme rejects a target whose scheme is not http or https. Defensive
// request-build guard; the authoritative SSRF policy is enforced at the
// connection write path.
func checkScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("anthropicapi: parse base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: got %q", ErrUnsupportedScheme, u.Scheme)
	}
	return nil
}

// revealKey invokes the binding's lazy RevealKey, tolerating a nil function (the
// no-key path) by returning an empty string. The missing-key guard in newRequest
// turns an empty result into ErrMissingAPIKey.
func (p *Provider) revealKey(ctx context.Context) (string, error) {
	if p.binding.RevealKey == nil {
		return "", nil
	}
	return p.binding.RevealKey(ctx)
}
