// Package claudecli implements engine.LLMProvider by shelling out to the
// `claude` CLI in headless (`--print`) mode.
package claudecli

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// eventKind identifies a claude stream-json event type.
type eventKind string

const (
	eventSystem    eventKind = "system"
	eventAssistant eventKind = "assistant"
	eventUser      eventKind = "user"
	eventResult    eventKind = "result"
)

// streamEvent is a parsed claude stream-json event reduced to the fields
// arbitron needs.
type streamEvent struct {
	Kind      eventKind
	SessionID string
	Text      string // assistant text content, or the result event's final text
	IsError   bool   // result events only
	TokensIn  int    // result events only
	TokensOut int    // result events only
}

// rawEnvelope is the on-the-wire shape of a stream-json line.
type rawEnvelope struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	IsError   bool        `json:"is_error"`
	Result    string      `json:"result"`
	Message   *rawMessage `json:"message"`
	Usage     *rawUsage   `json:"usage"`
}

type rawMessage struct {
	Content []rawContentBlock `json:"content"`
}

type rawContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// parseEvent parses a single stream-json line. ok is false for blank lines and
// unrecognised event types (both are skipped without error). A non-nil error
// means the line was not valid JSON.
func parseEvent(line []byte) (streamEvent, bool, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return streamEvent{}, false, nil
	}

	var env rawEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return streamEvent{}, false, fmt.Errorf("claudecli: parse stream-json line: %w", err)
	}

	switch eventKind(env.Type) {
	case eventSystem:
		return streamEvent{Kind: eventSystem, SessionID: env.SessionID}, true, nil
	case eventUser:
		return streamEvent{Kind: eventUser, SessionID: env.SessionID}, true, nil
	case eventAssistant:
		return streamEvent{
			Kind:      eventAssistant,
			SessionID: env.SessionID,
			Text:      assistantText(env.Message),
		}, true, nil
	case eventResult:
		ev := streamEvent{
			Kind:      eventResult,
			SessionID: env.SessionID,
			Text:      env.Result,
			IsError:   env.IsError,
		}
		if env.Usage != nil {
			ev.TokensIn = env.Usage.InputTokens
			ev.TokensOut = env.Usage.OutputTokens
		}
		return ev, true, nil
	default:
		return streamEvent{}, false, nil
	}
}

// assistantText concatenates the text blocks of an assistant message.
func assistantText(msg *rawMessage) string {
	if msg == nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
