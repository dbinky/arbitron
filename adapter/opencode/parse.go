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
// Token semantics (per the 1.17.x adapter design,
// docs/superpowers/specs/2026-06-15-opencode-1.17-adapter-modernization-design.md):
//   - `text` events are WHOLE-MESSAGE, not streaming token deltas ("Token-by-token
//     streaming — still whole-message"), so each carries a complete assistant
//     message and is appended as one summary line + one assistant step.
//   - `step_finish` tokens are PER-STEP (input/output for that step), not a running
//     cumulative total, so they are SUMMED across steps for the mission total.
//
// NOTE: these semantics are taken from the design doc; the plan's Step-1 capture of
// a real /tmp/oc-success.ndjson transcript was not folded back into the repo, so the
// assumptions are encoded as tests (parse_test.go) rather than verified against a
// captured run. If a future real transcript shows deltas/cumulative, coalesce text
// on the step boundary and stop summing step_finish.
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
