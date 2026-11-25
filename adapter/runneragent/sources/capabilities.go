package sources

import (
	"slices"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
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
		return lunaRouteChatCapabilities(in)
	default:
		return nil, "credential kind " + string(kind) + " has no remote chat capability"
	}
}

const ReasonLunaRouteModelsUnproven = "no configured LunaRoute model has chat support evidence"

// maxCapabilityModels mirrors the hub's per-capability bound; a longer list is
// refused as malformed, so the runner never sends one.
const maxCapabilityModels = 32

// lunaRouteChatCapabilities advertises the hosted route for the configured
// models the loaded rules prove chat-capable (all chat-capable rules models
// when none are configured). The key never appears in the evidence.
func lunaRouteChatCapabilities(in DiscoverInput) ([]runner.ExecutionCapability, string) {
	if !in.LunaRoute.Enabled() {
		return nil, ReasonCapabilityEvidenceUnproven
	}
	candidates := in.LunaRoute.Models
	if len(candidates) == 0 {
		for _, m := range in.Rules.LunaRouteModels() {
			candidates = append(candidates, m.ID)
		}
	}
	var models []string
	for _, id := range candidates {
		m, ok := in.Rules.LunaRouteModel(id)
		if ok && m.Task(provider.ProfileKindChat).Support == provider.SupportSupported && !slices.Contains(models, id) {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, ReasonLunaRouteModelsUnproven
	}
	slices.Sort(models)
	if len(models) > maxCapabilityModels {
		models = models[:maxCapabilityModels]
	}
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute})
	if err != nil {
		return nil, ReasonCapabilityEvidenceUnproven
	}
	return chatAndStream(runner.ExecutionCapability{Provider: provider.CanonicalLunaRoute, EndpointFingerprint: fp,
		Families: in.LunaRoute.Families, Models: models, ExecutedModel: true}), ""
}

// chatAndStream returns independent chat and stream copies of base.
func chatAndStream(base runner.ExecutionCapability) []runner.ExecutionCapability {
	out := make([]runner.ExecutionCapability, 0, 2)
	for _, op := range []runner.OpType{runner.OpChat, runner.OpStream} {
		c := base
		c.Op = op
		c.Families = slices.Clone(base.Families)
		c.Models = slices.Clone(base.Models)
		c.ContextWindows = slices.Clone(base.ContextWindows)
		c.Efforts = slices.Clone(base.Efforts)
		c.Tools = slices.Clone(base.Tools)
		out = append(out, c)
	}
	return out
}
