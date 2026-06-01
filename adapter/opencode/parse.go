package opencode

import "strings"

// parseOutput maps opencode's headless stdout lines into an AgentResult.
//
// The installed v0.0.55 binary's `-f text` mode emits the final assistant
// message as plain text lines. We therefore:
//   - fold each non-empty line into an AgentStep (Kind "assistant") for the
//     trace, in order;
//   - join the lines as the Summary (best-effort final text);
//   - account tokens COARSELY (whitespace-word count of the output as TokensOut;
//     TokensIn is left to the caller's prompt accounting / zero) since this
//     build emits no structured usage in text mode.
//
// Parsing is tolerant: empty / whitespace-only output yields an empty Summary
// and no Steps, never an error or panic. SessionID is always empty in the
// single-shot contract.
func parseOutput(lines []string) (summary string, steps []agentStep, tokensOut int) {
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		kept = append(kept, l)
		steps = append(steps, agentStep{Kind: "assistant", Summary: l})
		tokensOut += len(strings.Fields(l))
	}
	summary = strings.Join(kept, "\n")
	return summary, steps, tokensOut
}

// agentStep mirrors engine.AgentStep so parse.go stays independent of the
// engine import; provider.go converts it.
type agentStep struct {
	Kind    string
	Summary string
}
