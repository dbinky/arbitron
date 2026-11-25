package sources

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func claudeTestRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	return provider.NewCapabilityRules(map[string]provider.BackendCapability{"claude-code": {
		ContextTiers: []provider.ContextWindow{"default", "200k", "1m"}, EffortLevels: []provider.Effort{"default", "low", "medium", "high", "xhigh", "max"},
		ModelOverrides: map[string]provider.ModelCapability{"sonnet-4.6": {EffortLevels: []provider.Effort{"default", "low", "medium", "high", "max"}}},
	}})
}

func TestDiscover_CapabilitiesComeFromVerifiedModelsAndRules(t *testing.T) {
	in := DiscoverInput{ClaudeBinary: "/local/claude", Runner: &fakeRunner{answers: map[string]cmdAnswer{"--version": {lines: []string{"2.1.400 (Claude Code)"}}}}, Rules: claudeTestRules(t), ClaudeModels: []string{"sonnet-4.6", "claude-opus-5-5", "claude-opus-5-5"}}
	specs, _ := Discover(context.Background(), in)
	require.Len(t, specs, 1)
	caps := specs[0].Source.Capabilities
	require.Len(t, caps, 4)
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli"})
	require.NoError(t, err)
	for _, c := range caps {
		assert.Equal(t, "claude-cli", c.Provider)
		assert.Equal(t, fp, c.EndpointFingerprint)
		assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte("claude-cli\nlocal-subscription:"))), fp)
		assert.Empty(t, c.Backend)
		assert.Equal(t, ClaudeCodeTools, c.Tools)
		assert.True(t, c.ExecutedModel)
		assert.Equal(t, []provider.ContextWindow{"default", "200k", "1m"}, c.ContextWindows)
		assert.Equal(t, c.Models[0] == "claude-opus-5-5", containsEffort(c.Efforts, "xhigh"))
	}
	req := runner.ExecRequirements{Op: runner.OpChat, Provider: "claude-cli", EndpointFingerprint: fp, Model: "claude-opus-5-5", Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: modelidentity.Family("anthropic")}, Effort: "xhigh"}
	matches := func() bool {
		for _, c := range caps {
			if runner.CapabilitySatisfies(c, req) {
				return true
			}
		}
		return false
	}
	assert.True(t, matches())
	req.Model = "sonnet-4.6"
	assert.False(t, matches())
	req.Model = "claude-haiku-4-5"
	assert.False(t, matches())
	in.ClaudeModels = nil
	specs, _ = Discover(context.Background(), in)
	assert.Empty(t, specs[0].Source.Capabilities)
	assert.Equal(t, ReasonCapabilityEvidenceUnproven, specs[0].Source.UnavailableReason)
}
func containsEffort(es []provider.Effort, e provider.Effort) bool {
	for _, v := range es {
		if v == e {
			return true
		}
	}
	return false
}
