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
	// A pinned batch result requires a completed turn without any failure.
	completed bool
	failed    bool
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

// decodeLine decodes ONE `codex exec --json` stdout line into a tolerant
// codexEvent. It is the SHARED entry point for both the batch fold (parseOutput)
// and the incremental stream path (stream_adapter.go's StreamChat), so the two
// consume ONE event vocabulary and can never diverge on what a line means.
//
// Returns (ev, true, nil) for a decoded event; (zero, false, nil) for a
// blank/whitespace-only line (codex interleaves occasional non-JSON log noise on
// stdout — skipped, not an error); and (zero, false, ErrParse) for a non-blank
// line that is not valid JSON (fail-closed: we never silently drop a line we
// cannot read). Unknown event "type" values decode without error and are folded
// as no-ops by foldEvent (forward-compat).
func decodeLine(line string) (codexEvent, bool, error) {
	if strings.TrimSpace(line) == "" {
		return codexEvent{}, false, nil
	}
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return codexEvent{}, false, fmt.Errorf("%w: %v", ErrParse, err)
	}
	return ev, true, nil
}

// foldEvent folds ONE decoded event into the running parsed accumulator — the
// SHARED reducer used by both the batch parseOutput loop and the stream loop, so
// Summary/Steps/token/session bookkeeping is identical on both paths. It returns
// the agent_message text newly produced by this event (the empty string for any
// event that yields no fresh assistant text), so the stream path can emit that
// exact increment as a ChatChunk token WITHOUT re-deriving the vocabulary.
//
// Vocabulary (VERIFIED against codex-cli 0.137.0, see codexEvent):
//   - thread.started → records the thread id (the session id).
//   - item.completed → foldItem (agent_message → "assistant" step + Summary;
//     command_execution → "tool_call" step; unknown item → dropped).
//   - turn.completed → best-effort token usage.
//   - error / turn.failed → the failure message becomes the Summary and a single
//     "result" step (deduped: codex emits the SAME message on BOTH events) so a
//     failed turn is never a silent empty result.
//   - any other (e.g. turn.started, item.started, a future event) → ignored
//     (forward-compat).
func foldEvent(out *parsed, ev codexEvent) (newText string) {
	switch ev.Type {
	case "thread.started":
		if ev.ThreadID != "" {
			out.sessionID = ev.ThreadID
		}
	case "item.completed":
		return foldItem(out, ev.Item)
	case "turn.completed":
		out.completed = true
		if ev.Usage != nil {
			out.tokensIn = ev.Usage.InputTokens
			out.tokensOut = ev.Usage.OutputTokens
		}
	case "error", "turn.failed":
		msg := failureMessage(ev)
		// A failed turn must surface its cause, not a silent empty result.
		// codex emits BOTH an `error` and a `turn.failed` event carrying the
		// SAME message, so dedupe an identical consecutive message into one
		// "result" step rather than two.
		if msg != "" && msg != out.summary {
			out.summary = msg
			out.steps = append(out.steps, agentStep{Kind: "result", Summary: msg})
		}
		// Mark the turn failed regardless of dedupe, so the stream path's
		// Finalize fails closed even when the message duplicates a prior one.
		out.failed = true
	default:
		// Unknown event type (e.g. turn.started, item.started, or a future
		// event): ignored for forward-compat.
	}
	return ""
}

// failureMessage extracts the message an `error` / `turn.failed` event carries.
// codex spells it `message` on `error` and `error.message` on `turn.failed`, so
// both are read here — the ONE place that mapping lives, shared by foldEvent and
// failureCause so the two can never disagree on what a failure event says.
func failureMessage(ev codexEvent) string {
	if ev.Message != "" {
		return ev.Message
	}
	if ev.Error != nil {
		return ev.Error.Message
	}
	return ""
}

// eventFailureCause returns the failure message ONE event states, or "" when the
// event is not a failure event. It is the per-event form of failureCause, for the
// streaming path (foldExecStream) that sees one event at a time — so batch and
// stream recover a cause through the SAME vocabulary and can never diverge.
func eventFailureCause(ev codexEvent) string {
	switch ev.Type {
	case "error", "turn.failed":
		return failureMessage(ev)
	}
	return ""
}

// failureCause scans a `codex exec --json` stdout stream for the message of a
// failure event and returns it ("" when the stream reports no failure cause).
// The LAST distinct message wins, so the cause reflects how the turn ended.
//
// It is the recovery path for a NON-ZERO EXIT, where codex puts the real reason
// on stdout (e.g. `turn.failed` with "You've hit your usage limit") while stderr
// carries only incidental noise. It therefore differs from parseOutput in two
// deliberate ways:
//
//   - BEST-EFFORT, never fail-closed: an unreadable line is skipped rather than
//     returned as ErrParse. The process exit error is already the headline on
//     this path, so a garbled stream may only mean "no cause found" — it must
//     never mask the failure it is trying to explain.
//   - Reads the failure MESSAGE only, never the running summary: a messageless
//     failure event must not promote preceding assistant text into the cause.
func failureCause(lines []string) string {
	cause := ""
	for _, l := range lines {
		ev, ok, err := decodeLine(l)
		if err != nil || !ok {
			continue
		}
		if msg := eventFailureCause(ev); msg != "" {
			cause = msg
		}
	}
	return cause
}

// parseOutput folds a `codex exec --json` JSONL event stream into a parsed
// result: Summary is the terminal agent_message text (or the error message of a
// failed turn so a failure is never a silent empty result); Steps are the
// completed items in order (command_execution → "tool_call", agent_message →
// "assistant", a turn error → "result"); token counts come best-effort from the
// terminal turn.completed usage; SessionID is the thread id.
//
// It is now a thin loop over the SHARED decodeLine + foldEvent primitives that
// the stream path also uses, so batch and stream can never disagree on a
// summary/step/token derivation (the shared-decoder invariant). Tolerance and
// fail-closed behaviour are inherited from decodeLine.
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

// foldItem folds one completed item into the result and returns any new
// assistant text it produced (the empty string otherwise). agent_message updates
// the running Summary (last one wins), adds an "assistant" step, and returns its
// text so the stream path can emit it as a token; command_execution adds a
// "tool_call" step (no assistant text); any other (or unknown) item type is
// dropped so a newer codex item kind does not pollute the trace.
func foldItem(out *parsed, item *codexItem) (newText string) {
	if item == nil {
		return ""
	}
	switch item.Type {
	case "agent_message":
		if item.Text != "" {
			out.summary = item.Text
			out.steps = append(out.steps, agentStep{Kind: "assistant", Summary: item.Text})
			return item.Text
		}
	case "command_execution":
		out.steps = append(out.steps, agentStep{Kind: "tool_call", Summary: item.Command})
	default:
		// Unknown / file-change item kind: dropped (forward-compat).
	}
	return ""
}
