package runner

import (
	"slices"
	"strings"
)

// ToolScopeSatisfied checks the requested scope against the advertised tool
// posture. Placement and Reserve's atomic recheck share this predicate.
func ToolScopeSatisfied(c ExecutionCapability, scope []string) bool {
	switch c.ToolPolicy {
	case ToolPolicyUnrestricted:
		return len(scope) == 0
	case ToolPolicyAllowlist:
		if len(scope) == 0 {
			return false
		}
	case "":
		// Chat/stream/confirm retain the exact subset rule.
	default:
		return false
	}
	for _, tool := range scope {
		if slices.Contains(c.Tools, tool) {
			continue
		}
		if base, ok := patternBase(tool); ok && c.ToolPolicy == ToolPolicyAllowlist && slices.Contains(c.Tools, base) {
			continue
		}
		return false
	}
	return true
}

// patternBase recognizes a nonempty native Claude tool permission pattern.
func patternBase(tool string) (string, bool) {
	open := strings.IndexByte(tool, '(')
	if open <= 0 || !strings.HasSuffix(tool, ")") || len(tool) <= open+2 {
		return "", false
	}
	return tool[:open], true
}

func validToolPolicy(c ExecutionCapability) bool {
	if c.Op != OpAgent {
		return c.ToolPolicy == ""
	}
	switch c.ToolPolicy {
	case ToolPolicyAllowlist:
		return c.Backend != "codex" && len(c.Tools) > 0
	case ToolPolicyUnrestricted:
		return c.Backend == "codex" && len(c.Tools) == 0
	}
	return false
}
