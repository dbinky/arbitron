package azurefoundry

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIError is the typed error returned for upstream non-2xx responses. It
// carries the status code and a short, sanitised message. It NEVER includes the
// api-key value (the key is only ever placed in a request header, never echoed
// here).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("azurefoundry: upstream status %d", e.StatusCode)
	}
	return fmt.Sprintf("azurefoundry: upstream status %d: %s", e.StatusCode, e.Message)
}

// chatResponse is the non-streaming OpenAI-compatible response body.
type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage usage `json:"usage"`
}

// streamFrame is one SSE data frame in a streaming response.
type streamFrame struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *usage `json:"usage"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// decodeChatResponse parses a non-streaming response body. An empty or malformed
// body yields a decode error that never contains the api-key.
func decodeChatResponse(b []byte) (*chatResponse, error) {
	var r chatResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("azurefoundry: decode response: %w", err)
	}
	return &r, nil
}

// content returns the assistant content from the first choice, or "".
func (r *chatResponse) content() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// apiErrorFromBody builds a typed APIError from a non-2xx response body. It
// extracts the upstream error.message when present but never the key.
func apiErrorFromBody(status int, body []byte) *APIError {
	msg := ""
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		msg = env.Error.Message
	}
	return &APIError{StatusCode: status, Message: msg}
}

// parseSSELine extracts the data payload from a single SSE line. It returns
// (payload, isData): isData is false for blank lines, comments, and non-data
// fields. The "data: " prefix is stripped and surrounding space trimmed.
func parseSSELine(line string) (payload string, isData bool) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "data:")), true
}
