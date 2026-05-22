package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// InteractiveResultSentinelOpen / Close delimit the JSON result an
	// interactive agent emits to signal session completion. They replace the
	// Messages-API `complete_session` tool.
	InteractiveResultSentinelOpen  = "<arbitron:result>"
	InteractiveResultSentinelClose = "</arbitron:result>"
)

// extractSentinelResult scans agent output for the last completion sentinel
// block and parses its JSON payload. ok is false when no sentinel is present.
// A non-nil error means a sentinel was present but its payload was not valid
// JSON.
func extractSentinelResult(text string) (map[string]any, bool, error) {
	openIdx := strings.LastIndex(text, InteractiveResultSentinelOpen)
	if openIdx == -1 {
		return nil, false, nil
	}
	rest := text[openIdx+len(InteractiveResultSentinelOpen):]
	closeIdx := strings.Index(rest, InteractiveResultSentinelClose)
	if closeIdx == -1 {
		return nil, false, nil
	}
	payload := strings.TrimSpace(rest[:closeIdx])

	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return nil, false, fmt.Errorf("engine: parse interactive result sentinel: %w", err)
	}
	return out, true, nil
}

// sentinelInstruction returns the text appended to an interactive agent's
// prompt telling it how to complete the session. schema is the node's
// outputSchema (may be nil).
func sentinelInstruction(schema any) string {
	var b strings.Builder
	b.WriteString("\n\nWhen the conversation has reached its conclusion and you ")
	b.WriteString("have the final result, emit exactly one line:\n")
	b.WriteString(InteractiveResultSentinelOpen)
	b.WriteString("{...}")
	b.WriteString(InteractiveResultSentinelClose)
	b.WriteString("\nwhere {...} is a JSON object")
	if schema != nil {
		if schemaJSON, err := json.Marshal(schema); err == nil {
			b.WriteString(" conforming to this schema:\n")
			b.Write(schemaJSON)
		}
	}
	b.WriteString("\n")
	return b.String()
}
