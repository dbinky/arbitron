package sources

import (
	"context"
	"slices"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

func TestBuildCapabilities_AdvertisesConfirmOnlyWithVerifier(t *testing.T) {
	in := DiscoverInput{ClaudeBinary: "/local/claude", Runner: &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.400 (Claude Code)"}}}}, Rules: claudeTestRules(t), ClaudeModels: []string{"claude-opus-5-5", "sonnet-4.6"}, Confirmations: &scriptedConfirm{}}
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "claude-code"})
	require.NoError(t, err)
	require.NotEmpty(t, fp)
	check := func(in DiscoverInput, want int) {
		t.Helper()
		specs, _ := Discover(context.Background(), in)
		var chats, confirms []runner.ExecutionCapability
		for _, sp := range specs {
			for _, c := range sp.Source.Capabilities {
				switch c.Op {
				case runner.OpChat:
					chats = append(chats, c)
				case runner.OpConfirm:
					confirms = append(confirms, c)
				}
			}
		}
		require.Len(t, confirms, want)
		for i, c := range confirms {
			assert.Equal(t, "claude-code", c.Backend)
			assert.Equal(t, fp, c.EndpointFingerprint)
			assert.True(t, c.ExecutedModel)
			assert.Equal(t, chats[i].Provider, c.Provider)
			assert.Equal(t, chats[i].Families, c.Families)
			assert.Equal(t, chats[i].Models, c.Models)
			assert.Equal(t, chats[i].ContextWindows, c.ContextWindows)
			assert.Equal(t, chats[i].Efforts, c.Efforts)
			req := runner.ExecRequirements{Op: runner.OpConfirm, Backend: "claude-code", Provider: "claude-cli", EndpointFingerprint: fp, Model: c.Models[0], Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: modelidentity.Family("anthropic")}, RequireExecutedModel: true}
			assert.True(t, runner.CapabilitySatisfies(c, req))
			req.Model = "claude-haiku-4-5"
			assert.False(t, runner.CapabilitySatisfies(c, req))
		}
	}
	check(in, 2)
	in.Confirmations = nil
	check(in, 0)
	in.Confirmations = &scriptedConfirm{}
	in.ClaudeModels = nil
	check(in, 0)
	in.ClaudeModels = []string{"claude-opus-5-5"}
	in.ClaudeBinary = "/missing/claude"
	in.Runner = &fakeRunner{}
	check(in, 0)
	in.ClaudeBinary = "/local/claude"
	in.Runner = &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"1.0.0 (Claude Code)"}}}}
	check(in, 0)
	caps, reason := BuildCapabilities(runner.CredOpenAISubscription, "0.171.0", in)
	assert.Empty(t, caps)
	assert.NotEmpty(t, reason)
}

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
