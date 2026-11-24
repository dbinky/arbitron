package sources

import (
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/runner"
)

// ClaudeCodeTools is the exact built-in tool vocabulary a claude-code source
// advertises (sorted). The claudecli adapter forwards these names verbatim to
// --allowedTools; pattern forms such as Bash(gh:*) are satisfied by "Bash".
var ClaudeCodeTools = []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch", "Write"}

const (
	ReasonCapabilityRulesMissing     = "capability rules are not loaded"
	ReasonCapabilityEvidenceUnproven = "capability evidence for this source is not proven"
)

// BuildCapabilities returns execution evidence or a visible refusal. An empty
// capability list is never evidence of unrestricted execution.
func BuildCapabilities(kind runner.CredentialKind, harnessVersion string, in DiscoverInput) ([]runner.ExecutionCapability, string) {
	if in.Rules.Empty() {
		return nil, ReasonCapabilityRulesMissing
	}
	switch kind {
	case runner.CredAnthropicSubscription:
		if strings.TrimSpace(harnessVersion) == "" {
			return nil, "harness_version_unknown"
		}
		if ok, _ := claudecli.CheckVersion(harnessVersion); !ok {
			return nil, "harness_unsupported"
		}
		return nil, ReasonCapabilityEvidenceUnproven
	case runner.CredLunaRoute:
		return nil, ReasonCapabilityEvidenceUnproven
	default:
		return nil, "credential kind " + string(kind) + " has no remote chat capability"
	}
}
