package opencode

import (
	"encoding/json"
	"strings"
)

// maxErrorSummaryLen caps the length of a provider error message folded into the
// user-facing summary, so a verbose upstream error (which may echo a prompt,
// internal host, or other provider detail) cannot bloat or leak through the
// summary unbounded. See parseOutput's error case.
const maxErrorSummaryLen = 512

// ocEvent is the subset of opencode 1.17.x `--format json` NDJSON events we
// consume. Each line is {type, timestamp, sessionID, part{...}} except `error`
// which carries a top-level `error` object.
//
// Event/token semantics — VERIFIED against opencode 1.17.x: a live-captured
// `error` event (its envelope matches the Error struct below) plus the documented
// `--format json` contract (sst/opencode issue #26855, which shows the real
// step_start/text/step-finish lines). The envelope uses an underscore top-level
// type (step_start, step_finish) wrapping a hyphenated part.type (step-start,
// step-finish):
//   - `text` events are WHOLE-MESSAGE (one completed text part carrying part.text),
//     not per-token deltas, so each is appended as one summary line + one assistant
//     step. (opencode does not stream token deltas to the JSON output.)
//   - `step_finish` tokens are PER-STEP (input/output for that step, under
//     part.tokens), NOT a running cumulative total, so they are SUMMED across steps.
//
// KNOWN UPSTREAM LIMITATION (issue #26855): the run loop can observe
// session.status=idle and exit BEFORE draining the final step-finish event to
// stdout. Those complete totals live only in opencode's SQLite session DB, never
// reach our stdout stream, and cannot be recovered here. TokensIn/TokensOut are
// therefore a FLOOR (the final step may be missing) — acceptable for the "coarse;
// subprocess accounting" contract on AgentResult, but downstream cost reporting
// must not treat them as exact.
type ocEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Tool   string `json:"tool"`
		Tokens struct {
			Input  int `json:"input"`
			Output int `json:"output"`
		} `json:"tokens"`
	} `json:"part"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// parseOutput folds opencode's `--format json` NDJSON stream into an AgentResult.
// Non-JSON lines (human log output) are skipped. `text` events (whole-message;
// see ocEvent) accumulate into the summary and an assistant step; `tool_use`
// events become tool steps; `step_finish` PER-STEP token usage sums into both
// tokensIn and tokensOut; an `error` event is surfaced into a step and, when there
// is no assistant text, into the summary (length-capped to bound any provider
// info leak). Tolerant: junk/empty yields an empty result, never a panic.
func parseOutput(lines []string) (summary string, steps []agentStep, tokensIn, tokensOut int) {
	var texts []string
	for _, l := range lines {
		s := strings.TrimSpace(l)
		if s == "" || s[0] != '{' {
			continue
		}
		var ev ocEvent
		if err := json.Unmarshal([]byte(s), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "text":
			if ev.Part.Text != "" {
				texts = append(texts, ev.Part.Text)
				steps = append(steps, agentStep{Kind: "assistant", Summary: ev.Part.Text})
			}
		case "tool_use":
			if ev.Part.Tool != "" {
				steps = append(steps, agentStep{Kind: "tool", Summary: ev.Part.Tool})
			}
		case "step_finish":
			// PER-STEP usage: sum across steps for the mission total (see ocEvent).
			tokensIn += ev.Part.Tokens.Input
			tokensOut += ev.Part.Tokens.Output
		case "error":
			if ev.Error != nil {
				msg := capErrorSummary(ev.Error.Data.Message)
				steps = append(steps, agentStep{Kind: "error", Summary: msg})
				if len(texts) == 0 && msg != "" {
					texts = append(texts, msg)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), steps, tokensIn, tokensOut
}

// capErrorSummary bounds a provider error message folded into the user-facing
// summary to maxErrorSummaryLen runes, appending an ellipsis when truncated. A
// verbose upstream error may carry prompt fragments or internal detail; capping
// it keeps the summary bounded without dropping the (useful) leading context.
func capErrorSummary(msg string) string {
	r := []rune(msg)
	if len(r) <= maxErrorSummaryLen {
		return msg
	}
	return string(r[:maxErrorSummaryLen]) + "…"
}

// agentStep mirrors engine.AgentStep so parse.go stays independent of the
// engine import; provider.go converts it.
type agentStep struct {
	Kind    string
	Summary string
}
