package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
)

// chatPath is the OpenAI-compatible chat-completions path appended to the
// connection's base-url. A LiteLLM router exposes the same path, so pointing a
// connection's base-url at the router needs no special-casing here.
const chatPath = "/chat/completions"

// buildBody assembles the OpenAI-compatible chat-completions JSON body from a
// ChatRequest. The system message is omitted when SystemPrompt is empty; zero
// max_tokens / temperature are omitted; response_format is set when the
// request carries an OutputSchema (structured output).
func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error) {
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
	url := strings.TrimRight(p.binding.BaseURL, "/") + chatPath

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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

// revealKey invokes the binding's lazy RevealKey, tolerating a nil function
// (the no-key path) by returning an empty string.
func (p *Provider) revealKey(ctx context.Context) (string, error) {
	if p.binding.RevealKey == nil {
		return "", nil
	}
	return p.binding.RevealKey(ctx)
}
