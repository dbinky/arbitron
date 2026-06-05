package bedrock

import (
	"encoding/json"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// buildBody assembles the Bedrock Converse request JSON from a ChatRequest:
//
//	{"messages":[{"role":"user","content":[{"text":...}]}],
//	 "system":[{"text":...}]?,                              // omitted when empty
//	 "inferenceConfig":{"maxTokens":N,"temperature":T}?}    // fields omitted when zero
//
// Converse takes the model id in the URL path, NOT the body, so model is not
// embedded here. OutputSchema is not part of Converse's body; it is ignored at
// the adapter (the domain executor remains the sole structured-output parser).
func buildBody(req engine.ChatRequest) ([]byte, error) {
	type textBlock struct {
		Text string `json:"text"`
	}
	type message struct {
		Role    string      `json:"role"`
		Content []textBlock `json:"content"`
	}

	body := map[string]any{
		"messages": []message{
			{Role: "user", Content: []textBlock{{Text: req.Prompt}}},
		},
	}
	if req.SystemPrompt != "" {
		body["system"] = []textBlock{{Text: req.SystemPrompt}}
	}
	inference := map[string]any{}
	if req.MaxTokens > 0 {
		inference["maxTokens"] = req.MaxTokens
	}
	if req.Temperature != 0 {
		inference["temperature"] = req.Temperature
	}
	if len(inference) > 0 {
		body["inferenceConfig"] = inference
	}

	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: marshal converse body: %w", err)
	}
	return out, nil
}
