package openrouter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrUpstreamStatus is the sentinel wrapping a non-2xx response from the
// router. Callers match it with errors.Is. The wrapped message includes the
// status code and a bounded snippet of the error body but NEVER the
// Authorization header or the revealed key.
var ErrUpstreamStatus = errors.New("openrouter: upstream returned non-2xx status")

// usageBlock mirrors the OpenAI-compatible `usage` object.
type usageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// chatMessage is one message in a choice (non-streaming response).
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatChoice is one choice in a non-streaming completion.
type chatChoice struct {
	Index   int         `json:"index"`
	Message chatMessage `json:"message"`
}

// chatCompletion is the full non-streaming chat-completions response.
type chatCompletion struct {
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   usageBlock   `json:"usage"`
}

// streamDelta is the incremental content in a streamed choice.
type streamDelta struct {
	Content string `json:"content"`
}

// streamChoice is one choice in a streamed frame.
type streamChoice struct {
	Index int         `json:"index"`
	Delta streamDelta `json:"delta"`
}

// streamFrame is a single `data: {...}` SSE frame in the stream.
type streamFrame struct {
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *usageBlock    `json:"usage"`
}

// decodeCompletion parses a non-streaming chat-completions body. It rejects an
// empty body or a response with no choices.
func decodeCompletion(body []byte) (*chatCompletion, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("openrouter: empty response body")
	}
	var c chatCompletion
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("openrouter: decode response: %w", err)
	}
	if len(c.Choices) == 0 {
		return nil, errors.New("openrouter: response has no choices")
	}
	return &c, nil
}

// parseSSELine returns a data frame, the [DONE] sentinel, or an error for
// malformed data. Non-data lines (blank, comments, event:) return nil/false.
// Retain the response model alongside content for verification before emission.
func parseSSELine(line []byte) (frame *streamFrame, done bool, err error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
		return nil, false, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false, nil
	}
	data := bytes.TrimSpace(line[len("data:"):])
	if string(data) == "[DONE]" {
		return nil, true, nil
	}
	var f streamFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, false, fmt.Errorf("openrouter: decode stream frame: %w", err)
	}
	return &f, false, nil
}
