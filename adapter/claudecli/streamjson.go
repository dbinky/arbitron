// Package claudecli implements engine.ChatProvider by shelling out to the
// `claude` CLI in headless (`--print`) mode.
package claudecli

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// maxContentBlockChars caps the length of a single text content block we
// retain in memory. Defence-in-depth alongside RunSpec.MaxResponseBytes:
// MaxResponseBytes bounds the whole request; this bounds a single block
// within a line so a maliciously huge single text block does not balloon
// the runtime string-handling cost before the bytes cap trips.
//
// Hostile blocks are truncated (not rejected) so legitimate output still
// flows; the consumer sees the truncated text. SECURITY: PR #8 thread
// 3291945042.
//
// 1 MiB: a single assistant turn (interactive_agent design docs, plans, code)
// is routinely tens of KB — the previous 4 KiB cap silently chopped real
// output mid-word. 1 MiB leaves generous headroom for legitimate turns while
// staying 256x below the per-request MaxResponseBytes backstop.
const maxContentBlockChars = 1 << 20

// NOTE on json.Decoder.DisallowUnknownFields(): the stream-json envelope
// from the claude CLI carries forward-compat fields the engine intentionally
// ignores (subtype, model, parent_tool_use_id, ...). Enabling
// DisallowUnknownFields here would reject every real claude line. The
// recommendation from PR #8 thread 3291945042 applies to JSON payloads
// whose schema arbitron fully controls; for the wire envelope, we
// constrain the surface via narrow rawEnvelope fields + maxContentBlockChars
// instead.

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
//
// Message stays raw until the event type is known: only an assistant event's
// message is an object carrying text, and Claude Code 2.1.300 emits other event
// types whose `message` is a plain string. Decoding it eagerly failed the whole
// line (and the node) on an event that never needed it.
type rawEnvelope struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	Message   json.RawMessage `json:"message"`
	Usage     *rawUsage       `json:"usage"`
}

type rawMessage struct {
	Content []rawContentBlock `json:"content"`
}

type rawContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// rawUsage is the usage block on a stream-json result event.
//
// Anthropic splits INPUT across three additive buckets and `input_tokens`
// carries only the uncached remainder — which on a warm turn is nearly nothing.
// A real Claude Code 2.1.278 result reads input_tokens:2 alongside
// cache_creation_input_tokens:27208 and cache_read_input_tokens:26579, so
// reading input_tokens alone reports 2 of 53,789 actual input tokens.
//
// This differs from the codex convention, where `input_tokens` is already the
// TOTAL and `cached_input_tokens` is a subset of it. Summing here is what makes
// the two backends' recorded TokensIn mean the same thing and therefore be
// comparable in a trace.
type rawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// CacheCreationInputTokens are input tokens written INTO the cache on this
	// turn. Billed (at a premium) and genuinely processed, so they count.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// CacheReadInputTokens are input tokens served FROM the cache. Billed at a
	// discount, but they are still input the model processed — and on an agent
	// turn they are the bulk of it, including the whole harness prompt.
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

// totalInput returns every input bucket summed. A usage block from an older CLI
// (or a provider that does not cache) carries no cache fields, so they decode
// as zero and the total degrades to input_tokens unchanged.
func (u rawUsage) totalInput() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
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
		// An assistant message that is not an object fails closed: its text is
		// the agent's output, and silently dropping it would lose a turn.
		var msg *rawMessage
		if len(env.Message) > 0 {
			if err := json.Unmarshal(env.Message, &msg); err != nil {
				return streamEvent{}, false, fmt.Errorf("claudecli: parse stream-json assistant message: %w", err)
			}
		}
		return streamEvent{
			Kind:      eventAssistant,
			SessionID: env.SessionID,
			Text:      assistantText(msg),
		}, true, nil
	case eventResult:
		ev := streamEvent{
			Kind:      eventResult,
			SessionID: env.SessionID,
			Text:      env.Result,
			IsError:   env.IsError,
		}
		if env.Usage != nil {
			ev.TokensIn = env.Usage.totalInput()
			ev.TokensOut = env.Usage.OutputTokens
		}
		return ev, true, nil
	default:
		return streamEvent{}, false, nil
	}
}

// assistantText concatenates the text blocks of an assistant message. Each
// text block is truncated at maxContentBlockChars characters.
func assistantText(msg *rawMessage) string {
	if msg == nil {
		return ""
	}
	var b bytes.Buffer
	for _, block := range msg.Content {
		if block.Type != "text" {
			continue
		}
		txt := block.Text
		if len(txt) > maxContentBlockChars {
			txt = txt[:maxContentBlockChars]
		}
		b.WriteString(txt)
	}
	return b.String()
}
