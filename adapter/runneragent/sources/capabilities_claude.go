package sources

import (
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"slices"
)

// claudeChatProvider is the provider id the server stamps on no-connection
// (subscription) bindings; the chat path's backend is "".
const claudeChatProvider = "claude-cli"

// claudeChatCapabilities advertises the natively verified exact Claude models on
// the local-subscription route. Axes are the loaded claude-code rules (the chat
// adapter's own backend, adapter/claudecli/shared.go:claudeBackend), grouped so
// a model override never shares another model's axes.
func claudeChatCapabilities(in DiscoverInput) ([]runner.ExecutionCapability, string) {
	bc, ok := in.Rules.Describe()["claude-code"]
	if !ok {
		return nil, ReasonCapabilityEvidenceUnproven
	}
	var models []string
	for _, m := range in.ClaudeModels {
		if !slices.Contains(models, m) {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		return nil, ReasonCapabilityEvidenceUnproven
	}
	slices.Sort(models)
	if len(models) > maxCapabilityModels {
		models = models[:maxCapabilityModels]
	}
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: claudeChatProvider})
	if err != nil {
		return nil, ReasonCapabilityEvidenceUnproven
	}
	type axes struct {
		tiers  []provider.ContextWindow
		levels []provider.Effort
	}
	var groups []axes
	var members [][]string
	for _, m := range models {
		a := axes{bc.ContextTiers, bc.EffortLevels}
		if mc, ok := bc.ModelOverrides[m]; ok {
			if mc.ContextTiers != nil {
				a.tiers = mc.ContextTiers
			}
			if mc.EffortLevels != nil {
				a.levels = mc.EffortLevels
			}
		}
		i := slices.IndexFunc(groups, func(g axes) bool { return slices.Equal(g.tiers, a.tiers) && slices.Equal(g.levels, a.levels) })
		if i < 0 {
			groups, members = append(groups, a), append(members, nil)
			i = len(groups) - 1
		}
		members[i] = append(members[i], m)
	}
	var out []runner.ExecutionCapability
	for i, g := range groups {
		out = append(out, chatAndStream(runner.ExecutionCapability{Provider: claudeChatProvider, EndpointFingerprint: fp,
			Families: []modelidentity.Family{"anthropic"}, Models: members[i], ContextWindows: g.tiers, Efforts: g.levels,
			Tools: ClaudeCodeTools, ExecutedModel: true})...)
	}
	return out, ""
}
