package sources

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func TestClaudeCodeTools_PinnedVocabulary(t *testing.T) {
	assert.Equal(t, []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch", "Write"}, ClaudeCodeTools)
	assert.True(t, slices.IsSorted(ClaudeCodeTools))
	assert.Subset(t, ClaudeCodeTools, []string{"Read", "Grep"})
	for _, tool := range ClaudeCodeTools {
		assert.NotContains(t, tool, "(", "patterns are request-side, never advertised")
	}
}

func TestBuildCapabilities_SignatureAndRefusals(t *testing.T) {
	var _ func(runner.CredentialKind, string, DiscoverInput) ([]runner.ExecutionCapability, string) = BuildCapabilities
	var in DiscoverInput
	var _ provider.CapabilityRules = in.Rules
	var _ runner.ConfirmationExecutor = in.Confirmations

	caps, reason := BuildCapabilities(runner.CredAnthropicSubscription, "2.1.400", DiscoverInput{})
	assert.Empty(t, caps, "no rules, no capability")
	assert.Equal(t, ReasonCapabilityRulesMissing, reason)

	caps, reason = BuildCapabilities(runner.CredOpenAISubscription, "0.171.0", DiscoverInput{Rules: provider.NewCapabilityRules(map[string]provider.BackendCapability{"codex": {}})})
	assert.Empty(t, caps, "no openai chat source in Phase 2")
	assert.NotEmpty(t, reason)

	for _, k := range []runner.CredentialKind{runner.CredAnthropicSubscription, runner.CredLunaRoute} {
		caps, reason = BuildCapabilities(k, "", DiscoverInput{Rules: provider.NewCapabilityRules(map[string]provider.BackendCapability{"claude-code": {}})})
		assert.True(t, len(caps) > 0 || reason != "", "%s: an empty list is never unrestricted", k)
		for _, c := range caps {
			assert.NotEqual(t, runner.OpConfirm, c.Op, "%s: nil Confirmations never advertises OpConfirm", k)
		}
	}
}
