// Package pi implements engine.AgentProvider by driving the Pi coding agent
// (`pi -p --mode json`) — a minimal harness whose system prompt and four-tool
// surface (read/write/edit/bash) cost roughly a tenth of a vendor CLI's
// per-request context. See args.go (the invocation contract), env.go (per-provider
// credentials), parse.go (JSONL event folding) and version.go (binary probe).
package pi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrParse is returned by parseOutput when a stdout line is not valid JSON. We
// fail closed on a line we cannot read rather than silently dropping it, so a
// truncated/garbled stream surfaces as an error (the same posture as the codex
// and opencode adapters).
var ErrParse = errors.New("pi: malformed JSON event")

// parsed is the folded outcome of a `pi --mode json` event stream. It stays
// independent of the engine import (provider.go converts steps to
// engine.AgentStep), mirroring the codex/opencode parseOutput contract.
type parsed struct {
	summary   string
	steps     []agentStep
	tokensIn  int
	tokensOut int
	sessionID string
	// executedModels records every authoritative assistant message, including
	// absent identity fields, so a later match cannot hide an earlier fallback.
	executedModels []executedModel
	// failed records that the turn ended in a failure stop reason. pi exits 0
	// even when the provider call fails, so this — NOT the process exit code —
	// is the authoritative success signal for the whole adapter.
	failed bool
	// failureMsg is the provider's own explanation when it supplied one (pi puts
	// it on the assistant message as errorMessage). Empty for a failure that
	// states no message, e.g. an aborted turn.
	failureMsg string
}

// agentStep mirrors engine.AgentStep so parse.go stays independent of the engine
// import; provider.go converts it.
type agentStep struct {
	Kind    string
	Summary string
}

// piEvent is the tolerant envelope for one `pi --mode json` JSONL line.
// VERIFIED against pi 0.87.0 (docs/json.md + the pi-ai type definitions). Only
// the fields we fold are decoded; every other field and every unknown event
// "type" is ignored so a newer pi that adds events does not break parsing.
type piEvent struct {
	Type string `json:"type"`
	// ID is the session id, present on the leading "session" header line.
	ID string `json:"id"`
	// Message carries the authoritative message on message_start/message_end.
	// Only message_end is folded (message_start's content is still empty).
	Message *piMessage `json:"message"`
}

// piMessage is one message in the stream. Role is "system", "user", "assistant"
// or "toolResult"; only assistant messages carry work we fold.
//
// Content is deliberately json.RawMessage rather than a typed slice: a system
// message spells `content` as a plain STRING while user/assistant messages spell
// it as an ARRAY of content blocks. Decoding it lazily (and only for assistant
// messages) keeps one envelope tolerant of both shapes instead of failing closed
// on a system line that is not ours to read.
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Usage      *piUsage        `json:"usage"`
	StopReason string          `json:"stopReason"`
	ErrorMsg   string          `json:"errorMessage"`
	Model      string          `json:"model"`
	ProviderID string          `json:"provider"`
}

// piContent is one block inside an assistant message's content array: text,
// thinking, or a tool call.
type piContent struct {
	Type string `json:"type"`
	Text string `json:"text"` // text
	Name string `json:"name"` // toolCall
}

// piUsage is pi's per-message token accounting. cacheRead/cacheWrite and the
// cost breakdown are deliberately not folded: AgentResult carries coarse
// in/out counts only.
type piUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// Stop reasons that mean the turn did NOT complete its work. "length" is
// deliberately absent: a truncated turn still produced real output, so it is
// surfaced as a normal (if short) result rather than a failure.
const (
	stopReasonError   = "error"
	stopReasonAborted = "aborted"
)

// decodeLine decodes ONE `pi --mode json` stdout line into a tolerant piEvent.
//
// Returns (ev, true, nil) for a decoded event; (zero, false, nil) for a
// blank/whitespace-only line (skipped, not an error); and (zero, false,
// ErrParse) for a non-blank line that is not valid JSON (fail-closed: we never
// silently drop a line we cannot read). Unknown event "type" values decode
// without error and are folded as no-ops by foldEvent.
func decodeLine(line string) (piEvent, bool, error) {
	if strings.TrimSpace(line) == "" {
		return piEvent{}, false, nil
	}
	var ev piEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return piEvent{}, false, fmt.Errorf("%w: %v", ErrParse, err)
	}
	return ev, true, nil
}

// foldEvent folds ONE decoded event into the running accumulator.
//
// Vocabulary (VERIFIED against pi 0.87.0 docs/json.md):
//   - session      → the leading header line; records the session id.
//   - message_end  → the authoritative final message. Assistant messages
//     contribute steps, summary, tokens and the failure verdict; system, user
//     and toolResult messages are ignored.
//   - anything else → ignored. That deliberately includes:
//     message_update (delta-only, and its cumulative usage can lag the final
//     message), tool_execution_* (the same call already appears as a toolCall
//     block in the assistant message — folding both would double every step),
//     and agent_end (it REPEATS the entire transcript, so folding it would
//     double every step and re-count every token).
func foldEvent(out *parsed, ev piEvent) {
	switch ev.Type {
	case "session":
		if ev.ID != "" {
			out.sessionID = ev.ID
		}
	case "message_end":
		foldMessage(out, ev.Message)
	default:
		// message_update / tool_execution_* / agent_end / agent_start /
		// turn_start / turn_end / agent_settled / a future event: no-op.
	}
}

// foldMessage folds one terminal message. Only assistant messages carry work:
// each text block updates the running summary (last one wins) and adds an
// "assistant" step; each toolCall block adds a "tool_call" step named for the
// tool. Token counts come from the message's own usage (last message wins), and
// a failure stop reason marks the fold failed so a turn that errored can never
// read as a clean empty success.
func foldMessage(out *parsed, m *piMessage) {
	if m == nil || m.Role != "assistant" {
		return
	}
	out.executedModels = append(out.executedModels, executedModel{provider: m.ProviderID, model: m.Model})
	for _, c := range decodeContent(m.Content) {
		switch c.Type {
		case "text":
			if c.Text != "" {
				out.summary = c.Text
				out.steps = append(out.steps, agentStep{Kind: "assistant", Summary: c.Text})
			}
		case "toolCall":
			if c.Name != "" {
				out.steps = append(out.steps, agentStep{Kind: "tool_call", Summary: c.Name})
			}
		default:
			// "thinking" and any future block kind: not trace material.
		}
	}
	if m.Usage != nil {
		out.tokensIn = m.Usage.Input
		out.tokensOut = m.Usage.Output
	}
	if m.StopReason == stopReasonError || m.StopReason == stopReasonAborted {
		out.failed = true
		if m.ErrorMsg != "" {
			out.failureMsg = m.ErrorMsg
		}
	}
}

// decodeContent decodes an assistant message's content array. A system message
// spells content as a bare string, so a shape that is not an array yields no
// blocks rather than a parse failure — callers only reach here for assistant
// messages, and tolerance costs nothing.
func decodeContent(raw json.RawMessage) []piContent {
	if len(raw) == 0 {
		return nil
	}
	var blocks []piContent
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
}

// parseOutput folds a `pi --mode json` JSONL event stream into a parsed result.
// Tolerance and fail-closed behaviour are inherited from decodeLine.
func parseOutput(lines []string) (*parsed, error) {
	out := &parsed{}
	for _, l := range lines {
		ev, ok, err := decodeLine(l)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		foldEvent(out, ev)
	}
	return out, nil
}
