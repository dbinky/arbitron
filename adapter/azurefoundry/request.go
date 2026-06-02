package azurefoundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/arbitron/arbitron/domain/engine"
)

// chatRequestBody is the OpenAI-compatible request schema. Azure OpenAI and
// Azure AI Foundry serverless both accept this shape, so one body serves both
// topologies. It is reimplemented locally (not shared with other adapters) to
// keep the adapters independent.
type chatRequestBody struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	// StreamOptions asks Azure/OpenAI to include a final usage frame in the
	// stream so token accounting is native on the terminal chunk.
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string         `json:"type"`
	JSONSchema map[string]any `json:"json_schema,omitempty"`
}

// buildBody marshals an OpenAI-compatible chat-completions body. For the
// resource topology, model is the deployment name (the same value used in the
// URL path). When the request carries an OutputSchema, response_format is set to
// json_schema for native structured output.
func buildBody(req engine.ChatRequest, model string, stream bool) ([]byte, error) {
	body := chatRequestBody{
		Model:       model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if req.SystemPrompt != "" {
		body.Messages = append(body.Messages, chatMessage{Role: "system", Content: req.SystemPrompt})
	}
	body.Messages = append(body.Messages, chatMessage{Role: "user", Content: req.Prompt})

	if req.OutputSchema != nil {
		schema, err := toSchemaMap(req.OutputSchema)
		if err != nil {
			return nil, fmt.Errorf("azurefoundry: encode output schema: %w", err)
		}
		body.ResponseFormat = &responseFormat{
			Type: "json_schema",
			JSONSchema: map[string]any{
				"name":   "output",
				"strict": true,
				"schema": schema,
			},
		}
	}

	return json.Marshal(body)
}

// toSchemaMap normalises an arbitrary OutputSchema value into a JSON object map
// via a marshal/unmarshal round-trip, so callers may pass structs or maps.
func toSchemaMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// newRequest builds the HTTP request for the given URL and body. It sets the
// JSON content type and the Azure api-key header (NOT Authorization: Bearer),
// fetching the key from the lazy RevealKey at call time only. The key is never
// logged.
func (p *Provider) newRequest(ctx context.Context, urlStr string, body []byte) (*http.Request, error) {
	key, err := p.revealKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("azurefoundry: reveal key: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("azurefoundry: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", key)
	return httpReq, nil
}
