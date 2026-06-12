package openrouter

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
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrUnsupportedScheme is returned when the binding's BaseURL uses a scheme
// other than http/https. The write-path StrictURLPolicy is the primary SSRF
// guard (https-only, no internal hosts); this is a defensive belt-and-suspenders
// check at request build so a non-web scheme (file, gopher, ...) can never reach
// the HTTP client even if a binding bypassed the write path.
var ErrUnsupportedScheme = errors.New("openrouter: base_url scheme must be http or https")

// chatPath is the OpenAI-compatible chat-completions path appended to the
// connection's base-url. A LiteLLM router exposes the same path, so pointing a
// connection's base-url at the router needs no special-casing here.
const chatPath = "/chat/completions"

// buildBody assembles the OpenAI-compatible chat-completions JSON body from a
// ChatRequest. The system message is omitted when SystemPrompt is empty; zero
// max_tokens / temperature are omitted; response_format is set when the
// request carries an OutputSchema (structured output).
func buildBody(req engine.ChatRequest, model string, stream bool, effort provider.Effort) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	msgs := make([]message, 0, 2)
	if req.SystemPrompt != "" {
		msgs = append(msgs, message{Role: "system", Content: req.SystemPrompt})
	}
	msgs = append(msgs, message{Role: "user", Content: req.Prompt})

	body := map[string]any{
		"model":    model,
		"messages": msgs,
	}
	if stream {
		body["stream"] = true
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if req.OutputSchema != nil {
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "output",
				"schema": req.OutputSchema,
				"strict": true,
			},
		}
	}
	// Reasoning-effort axis: a graded/none effort is emitted as the
	// OpenAI-compatible `reasoning_effort` field (xhigh floors to high via the
	// shared domain mapping, so this matches the value ProfileService validated at
	// write time). default/unset emits nothing — the model uses its own default.
	if _, value, emit := provider.ChatReasoningEffort(effort); emit {
		body["reasoning_effort"] = value
	}

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal request body: %w", err)
	}
	return out, nil
}

// newRequest builds the authenticated POST request to {base-url}/chat/completions.
// The revealed key is fetched from the binding's lazy RevealKey here and used
// only to populate the Authorization header; it is never logged.
func (p *Provider) newRequest(ctx context.Context, body []byte) (*http.Request, error) {
	target := strings.TrimRight(p.binding.BaseURL, "/") + chatPath

	if err := checkScheme(target); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	key, err := p.revealKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("openrouter: reveal key: %w", err)
	}
	if key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	return httpReq, nil
}

// checkScheme rejects a target whose scheme is not http or https. This is a
// defensive request-build guard; the authoritative SSRF policy (https-only, no
// internal hosts) is enforced at the connection write path.
func checkScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("openrouter: parse base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: got %q", ErrUnsupportedScheme, u.Scheme)
	}
	return nil
}

// revealKey invokes the binding's lazy RevealKey, tolerating a nil function
// (the no-key path) by returning an empty string.
func (p *Provider) revealKey(ctx context.Context) (string, error) {
	if p.binding.RevealKey == nil {
		return "", nil
	}
	return p.binding.RevealKey(ctx)
}
