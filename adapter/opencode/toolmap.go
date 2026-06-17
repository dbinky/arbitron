package opencode

import "strings"

// claudeToOpencodeTool maps Claude-style tool names (used in arbitron profile
// AllowedTools) to opencode 1.17.x tool names. Names with no 1.17.x equivalent
// (MultiEdit, TodoRead) are absent and dropped by mapTools.
var claudeToOpencodeTool = map[string]string{
	"read":      "read",
	"write":     "write",
	"edit":      "edit",
	"grep":      "grep",
	"glob":      "glob",
	"bash":      "bash",
	"ls":        "list",
	"webfetch":  "webfetch",
	"websearch": "websearch",
	"todowrite": "todowrite",
	"task":      "task",
}

// mapTools translates an arbitron AllowedTools list to opencode tool names,
// preserving order and de-casing. Unknown names are returned in `dropped` so the
// caller can log them once (never silently swallow a requested tool).
func mapTools(allowed []string) (mapped []string, dropped []string) {
	for _, a := range allowed {
		key := strings.ToLower(strings.TrimSpace(a))
		if oc, ok := claudeToOpencodeTool[key]; ok {
			mapped = append(mapped, oc)
		} else {
			dropped = append(dropped, a)
		}
	}
	return mapped, dropped
}
