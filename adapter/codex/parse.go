package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrParse is returned by parseOutput when a stdout line is not valid JSON. We
// fail closed on a line we cannot read rather than silently dropping it, so a
// truncated/garbled stream surfaces as an error to the caller (mirrors the
// fail-closed posture of the rest of the adapter).
var ErrParse = errors.New("codex: malformed JSON event")

// parsed is the folded outcome of a `codex exec --json` event stream. It stays
// independent of the engine import (provider.go converts steps to
// engine.AgentStep), mirroring opencode's parseOutput contract.
type parsed struct {
	summary   string
	steps     []agentStep
	tokensIn  int
	tokensOut int
	sessionID string
}

// agentStep mirrors engine.AgentStep so parse.go stays independent of the engine
// import; provider.go converts it.
type agentStep struct {
	Kind    string
	Summary string
}

// codexEvent is the tolerant envelope for one `codex exec --json` JSONL line.
// VERIFIED against codex-cli 0.137.0. Only the fields we fold are decoded; every
// other field (and every unknown event "type") is ignored so a newer codex that
// adds events/items does not break parsing (forward-compat).
type codexEvent struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"` // thread.started
	Item     *codexItem     `json:"item"`      // item.started / item.completed
	Usage    *codexUsage    `json:"usage"`     // turn.completed
	Message  string         `json:"message"`   // error
	Error    *codexErrorObj `json:"error"`     // turn.failed
}

// codexItem is one work item inside a turn. type is "agent_message" (an
// assistant message; text), "command_execution" (a shell tool call; command),
// or a file-change/unknown item (dropped).
type codexItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`    // agent_message
	Command string `json:"command"` // command_execution
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type codexErrorObj struct {
	Message string `json:"message"`
}

// parseOutput folds a `codex exec --json` JSONL event stream into a parsed
// result: Summary is the terminal agent_message text (or the error message of a
// failed turn so a failure is never a silent empty result); Steps are the
// completed items in order (command_execution → "tool_call", agent_message →
// "assistant", a turn error → "result"); token counts come best-effort from the
// terminal turn.completed usage; SessionID is the thread id.
//
// Tolerance: blank/whitespace-only lines are skipped (codex interleaves
// occasional non-JSON log noise on stdout), and unknown event types / item types
// are ignored (forward-compat). A line that is non-blank but not valid JSON is a
// hard ErrParse (fail-closed), not a silent drop.
func parseOutput(lines []string) (*parsed, error) {
	out := &parsed{}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrParse, err)
		}
		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				out.sessionID = ev.ThreadID
			}
		case "item.completed":
			foldItem(out, ev.Item)
		case "turn.completed":
			if ev.Usage != nil {
				out.tokensIn = ev.Usage.InputTokens
				out.tokensOut = ev.Usage.OutputTokens
			}
		case "error", "turn.failed":
			msg := ev.Message
			if msg == "" && ev.Error != nil {
				msg = ev.Error.Message
			}
			// A failed turn must surface its cause, not a silent empty result.
			// codex emits BOTH an `error` and a `turn.failed` event carrying the
			// SAME message, so dedupe an identical consecutive message into one
			// "result" step rather than two.
			if msg != "" && msg != out.summary {
				out.summary = msg
				out.steps = append(out.steps, agentStep{Kind: "result", Summary: msg})
			}
		default:
			// Unknown event type (e.g. turn.started, item.started, or a future
			// event): ignored for forward-compat.
		}
	}
	return out, nil
}

// foldItem folds one completed item into the result. agent_message updates the
// running Summary (last one wins) and adds an "assistant" step;
// command_execution adds a "tool_call" step; any other (or unknown) item type is
// dropped so a newer codex item kind does not pollute the trace.
func foldItem(out *parsed, item *codexItem) {
	if item == nil {
		return
	}
	switch item.Type {
	case "agent_message":
		if item.Text != "" {
			out.summary = item.Text
			out.steps = append(out.steps, agentStep{Kind: "assistant", Summary: item.Text})
		}
	case "command_execution":
		out.steps = append(out.steps, agentStep{Kind: "tool_call", Summary: item.Command})
	default:
		// Unknown / file-change item kind: dropped (forward-compat).
	}
}
