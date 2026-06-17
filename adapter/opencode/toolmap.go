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
// preserving first-seen order and de-casing. The returned `mapped` set is
// DEDUPLICATED so it is the exact canonical permitted set (two arbitron names
// mapping to the same opencode tool, e.g. duplicate "Read", yield one entry);
// the permission block built from it then names each tool once. Unknown names
// are returned in `dropped` so the caller can log them (never silently swallow a
// requested tool, which would narrow the effective security posture).
func mapTools(allowed []string) (mapped []string, dropped []string) {
	seen := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		key := strings.ToLower(strings.TrimSpace(a))
		oc, ok := claudeToOpencodeTool[key]
		if !ok {
			dropped = append(dropped, a)
			continue
		}
		if _, dup := seen[oc]; dup {
			continue
		}
		seen[oc] = struct{}{}
		mapped = append(mapped, oc)
	}
	return mapped, dropped
}
