package anthropicapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUpstreamStatus is the sentinel wrapping a non-2xx Messages response. Callers
// match it with errors.Is. The wrapped message includes the status code and a
// bounded, control-scrubbed snippet of the error body but NEVER the x-api-key
// value.
var ErrUpstreamStatus = errors.New("anthropicapi: upstream returned non-2xx status")

// usageBlock mirrors Anthropic's usage object.
type usageBlock struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// contentBlock is one element of the Messages `content` array. text blocks carry
// Text; tool_use blocks carry Name + Input (raw JSON for the forced-tool path).
type contentBlock struct {
	Type  string          `json:"type"`  // "text" | "tool_use"
	Text  string          `json:"text"`  // when Type == "text"
	Name  string          `json:"name"`  // when Type == "tool_use"
	Input json.RawMessage `json:"input"` // when Type == "tool_use"
}

// messagesResponse is the non-streaming /v1/messages response.
type messagesResponse struct {
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
	Usage   usageBlock     `json:"usage"`
}

// decodeMessages parses a non-streaming Messages body. It rejects an empty body
// or a response with no content.
func decodeMessages(body []byte) (*messagesResponse, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("anthropicapi: empty response body")
	}
	var m messagesResponse
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("anthropicapi: decode response: %w", err)
	}
	if len(m.Content) == 0 {
		return nil, errors.New("anthropicapi: response has no content")
	}
	return &m, nil
}

// output returns the assistant output string. If any tool_use block carries a
// non-empty input (forced structured output), its Input is returned verbatim as
// a raw JSON string (pre-parse — the domain executor is the sole parser).
// Otherwise all text blocks are concatenated in order.
func (m *messagesResponse) output() (string, error) {
	for _, b := range m.Content {
		if b.Type == "tool_use" && len(bytes.TrimSpace(b.Input)) > 0 {
			return string(b.Input), nil
		}
	}
	var sb strings.Builder
	var sawText bool
	for _, b := range m.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
			sawText = true
		}
	}
	if !sawText {
		return "", errors.New("anthropicapi: response has no text or tool_use content")
	}
	return sb.String(), nil
}

// streamEvent is the JSON payload on an Anthropic SSE `data:` line.
type streamEvent struct {
	Type  string `json:"type"` // "content_block_delta","message_start","message_delta","message_stop","ping","error"
	Delta struct {
		Type        string `json:"type"`         // "text_delta" | "input_json_delta"
		Text        string `json:"text"`         // text_delta payload
		PartialJSON string `json:"partial_json"` // input_json_delta payload (forced-tool streaming)
		StopReason  string `json:"stop_reason"`  // on message_delta
	} `json:"delta"`
	// Usage may appear at the top level (message_delta) or nested under message
	// (message_start). Both are captured.
	Usage   *usageBlock `json:"usage"`
	Message *struct {
		Usage *usageBlock `json:"usage"`
	} `json:"message"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseSSEEvent interprets ONE line of an Anthropic SSE stream, threading the
// current event name through curEvent across the event:/data: line pair. The data
// frame's self-describing `type` is authoritative; curEvent is a fallback/debug
// aid. Returns the token delta (text_delta.Text or input_json_delta.PartialJSON),
// a usage block when the frame carries one, done=true on message_stop, and an
// error for a malformed data frame or an upstream `error` event. Blank lines, `:`
// comments, and `event:` lines (which only update *curEvent) return all-zero.
func parseSSEEvent(line []byte, curEvent *string) (token string, usage *usageBlock, done bool, err error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
		return "", nil, false, nil
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		if curEvent != nil {
			*curEvent = strings.TrimSpace(string(line[len("event:"):]))
		}
		return "", nil, false, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return "", nil, false, nil
	}
	data := bytes.TrimSpace(line[len("data:"):])

	var ev streamEvent
	if uerr := json.Unmarshal(data, &ev); uerr != nil {
		return "", nil, false, fmt.Errorf("anthropicapi: decode stream frame: %w", uerr)
	}

	if ev.Type == "error" || ev.Error != nil {
		msg := ""
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		return "", nil, false, fmt.Errorf("anthropicapi: upstream stream error: %s", scrubControl(msg))
	}

	switch ev.Type {
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			token = ev.Delta.Text
		case "input_json_delta":
			token = ev.Delta.PartialJSON
		}
	case "message_start":
		if ev.Message != nil && ev.Message.Usage != nil {
			usage = ev.Message.Usage
		} else if ev.Usage != nil {
			usage = ev.Usage
		}
	case "message_delta":
		if ev.Usage != nil {
			usage = ev.Usage
		}
	case "message_stop":
		done = true
	}
	return token, usage, done, nil
}
