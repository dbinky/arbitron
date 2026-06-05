package bedrock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUpstreamStatus is the sentinel wrapped by statusError for any non-2xx
// Converse response, so callers can errors.Is it without parsing the message.
var ErrUpstreamStatus = errors.New("bedrock: upstream returned non-2xx status")

type converseText struct {
	Text string `json:"text"`
}
type converseMessage struct {
	Role    string         `json:"role"`
	Content []converseText `json:"content"`
}
type converseUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}
type converseResponse struct {
	Output struct {
		Message converseMessage `json:"message"`
	} `json:"output"`
	Usage converseUsage `json:"usage"`
}

// decodeConverse parses a non-streaming Converse response and returns the joined
// assistant text and token usage. It rejects an empty body or a response whose
// output.message has no text content.
//
//	output.message.content[].text  (joined), usage.inputTokens, usage.outputTokens
func decodeConverse(body []byte) (string, int, int, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", 0, 0, errors.New("bedrock: empty response body")
	}
	var r converseResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", 0, 0, fmt.Errorf("bedrock: decode converse response: %w", err)
	}
	var b strings.Builder
	found := false
	for _, c := range r.Output.Message.Content {
		if c.Text != "" {
			b.WriteString(c.Text)
			found = true
		}
	}
	if !found {
		return "", 0, 0, errors.New("bedrock: response has no text content")
	}
	return b.String(), r.Usage.InputTokens, r.Usage.OutputTokens, nil
}

type converseEvent struct {
	ContentBlockDelta *struct {
		Delta converseText `json:"delta"`
	} `json:"contentBlockDelta"`
	Metadata *struct {
		Usage converseUsage `json:"usage"`
	} `json:"metadata"`
	MessageStop *struct {
		StopReason string `json:"stopReason"`
	} `json:"messageStop"`
}

// parseConverseEvent decodes one Bedrock Converse event-stream JSON frame (one
// line of the simplified test stream) into a token delta, optional usage, and a
// done flag. The production Converse stream is AWS event-stream framed; the
// adapter consumes the decoded JSON event objects. Recognized events:
//
//	contentBlockDelta -> delta.text (token)
//	metadata          -> usage.inputTokens / outputTokens
//	messageStop       -> done=true
//
// A blank or unrecognized event returns an all-zero result with no error.
func parseConverseEvent(line []byte) (delta string, tokensIn, tokensOut int, done bool, err error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return "", 0, 0, false, nil
	}
	var e converseEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return "", 0, 0, false, fmt.Errorf("bedrock: decode stream event: %w", err)
	}
	if e.ContentBlockDelta != nil {
		delta = e.ContentBlockDelta.Delta.Text
	}
	if e.Metadata != nil {
		tokensIn = e.Metadata.Usage.InputTokens
		tokensOut = e.Metadata.Usage.OutputTokens
	}
	if e.MessageStop != nil {
		done = true
	}
	return delta, tokensIn, tokensOut, done, nil
}
