package opencode

import (
	"encoding/json"
	"strings"
)

// ocEvent is the subset of opencode 1.17.x `--format json` NDJSON events we
// consume. Each line is {type, timestamp, sessionID, part{...}} except `error`
// which carries a top-level `error` object.
type ocEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string `json:"type"`
		Text   string `json:"text"`
		Tool   string `json:"tool"`
		Tokens struct {
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
// Non-JSON lines (human log output) are skipped. `text` events accumulate into the
// summary and an assistant step; `tool_use` events become tool steps; `step_finish`
// token usage sums into tokensOut; an `error` event is surfaced into a step and,
// when there is no assistant text, into the summary. Tolerant: junk/empty yields an
// empty result, never a panic.
func parseOutput(lines []string) (summary string, steps []agentStep, tokensOut int) {
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
			tokensOut += ev.Part.Tokens.Output
		case "error":
			if ev.Error != nil {
				msg := ev.Error.Data.Message
				steps = append(steps, agentStep{Kind: "error", Summary: msg})
				if len(texts) == 0 && msg != "" {
					texts = append(texts, msg)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), steps, tokensOut
}

// agentStep mirrors engine.AgentStep so parse.go stays independent of the
// engine import; provider.go converts it.
type agentStep struct {
	Kind    string
	Summary string
}
