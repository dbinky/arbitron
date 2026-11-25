package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/adapter/codex"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/arbitron/arbitron/domain/workflow"
)

// NativeExecutionInput is what the runner's native agent sources are built
// from. Codex uses the same config, command runner and environment as
// execution and auth; Rules are the same loaded rules WorkspaceJobs enforces.
type NativeExecutionInput struct {
	Codex            codex.Config
	Rules            provider.CapabilityRules
	WorkspaceEnabled bool
	SessionsEnabled  bool
	CodexInteractive bool
}

const (
	ReasonCodexModelInventory    = "codex account model list unavailable; run codex once so it refreshes models_cache.json"
	ReasonCodexWorkspaceDisabled = "codex agent jobs need workspace execution on this runner"
	ReasonNativeRulesMissing     = "capability rules for this backend are not loaded"
	// MaxCapabilityModels is the per-capability model bound (runnerproto maxCapabilities).
	MaxCapabilityModels = 32
)

var openAIFamily = []modelidentity.Family{"openai"}

// discoverCodex builds the single openai_subscription source. A missing
// binary advertises nothing; an installed one is always visible, with an
// UnavailableReason until version, rules, workspace execution and the local
// account model list all prove it.
func discoverCodex(ctx context.Context, in DiscoverInput) (Spec, string, bool) {
	n := in.Native
	version, compatible, reason, err := codex.VersionEvidence(ctx, n.Codex)
	if errors.Is(err, codex.ErrCodexMissing) {
		return Spec{}, "", false
	}
	src := runneragent.Source{Kind: runner.CredOpenAISubscription, Families: append([]modelidentity.Family(nil), openAIFamily...), Backends: []string{workflow.BackendCodex}}
	switch {
	case err != nil:
		src.UnavailableReason = "codex version unknown"
	case !compatible:
		src.UnavailableReason = reason
	case !n.WorkspaceEnabled:
		src.UnavailableReason = ReasonCodexWorkspaceDisabled
	default:
		models, merr := codex.LocalAccountModels()
		if merr != nil {
			src.UnavailableReason = ReasonCodexModelInventory
			break
		}
		caps, cerr := CodexAgentCapabilities(n.Rules, models, in.Confirmations != nil, n.SessionsEnabled && n.CodexInteractive)
		if cerr != nil {
			src.UnavailableReason = ReasonNativeRulesMissing
			break
		}
		src.Models, src.Capabilities = models, caps
	}
	return Spec{Source: src, Prober: runnerprobe.NewCodexProber(n.Codex)}, version, true
}

// CodexAgentCapabilities advertises exactly what WorkspaceJobs launches for a
// codex subscription: the agent op (plus confirm/stream when enabled), the
// no-connection codex route, every listed model, the rules' axes per model
// group, no tool restriction (codex has none) and executed-model proof.
func CodexAgentCapabilities(rules provider.CapabilityRules, models []string, confirm, stream bool) ([]runner.ExecutionCapability, error) {
	ops := []runner.OpType{runner.OpAgent}
	if confirm {
		ops = append(ops, runner.OpConfirm)
	}
	if stream {
		ops = append(ops, runner.OpStream)
	}
	return nativeCapabilities(rules, workflow.BackendCodex, openAIFamily, models, nil, ops)
}

// ClaudeAgentCapabilities adds claude-code agent support to the Claude source.
// Models and tools are exactly those P2 T9 BuildCapabilities proved for the
// same harness's chat capabilities; axes come from the rules' claude-code list.
func ClaudeAgentCapabilities(rules provider.CapabilityRules, chat []runner.ExecutionCapability, confirm, stream bool) ([]runner.ExecutionCapability, error) {
	var models, tools []string
	var fams []modelidentity.Family
	for _, c := range chat {
		if c.Op != runner.OpChat {
			continue
		}
		for _, m := range c.Models {
			if !slices.Contains(models, m) {
				models = append(models, m)
			}
		}
		for _, tool := range c.Tools {
			if !slices.Contains(tools, tool) {
				tools = append(tools, tool)
			}
		}
		for _, f := range c.Families {
			if !slices.Contains(fams, f) {
				fams = append(fams, f)
			}
		}
	}
	if len(models) == 0 {
		return nil, errors.New("sources: no proven claude models to extend to agent work")
	}
	if len(tools) == 0 {
		// allowlist (G2): an empty advertised list never means "all tools".
		return nil, errors.New("sources: no proven claude tools for an allowlist agent capability")
	}
	ops := []runner.OpType{runner.OpAgent}
	if confirm {
		ops = append(ops, runner.OpConfirm)
	}
	if stream {
		ops = append(ops, runner.OpStream)
	}
	return nativeCapabilities(rules, workflow.BackendClaudeCode, fams, models, tools, ops)
}

type axesGroup struct {
	tiers  []provider.ContextWindow
	levels []provider.Effort
	models []string
}

func nativeCapabilities(rules provider.CapabilityRules, backend string, fams []modelidentity.Family, models, tools []string, ops []runner.OpType) ([]runner.ExecutionCapability, error) {
	if rules.Empty() || !rules.Has(backend) {
		return nil, fmt.Errorf("sources: %s", ReasonNativeRulesMissing)
	}
	fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: providersadapter.ProviderClaudeCLI, Backend: backend})
	if err != nil {
		return nil, err
	}
	bc := rules.Describe()[backend]
	var groups []*axesGroup
	for _, m := range models {
		tiers, levels := bc.ContextTiers, bc.EffortLevels
		if o, ok := bc.ModelOverrides[m]; ok {
			if o.ContextTiers != nil {
				tiers = o.ContextTiers
			}
			if o.EffortLevels != nil {
				levels = o.EffortLevels
			}
		}
		var g *axesGroup
		for _, cand := range groups {
			if slices.Equal(cand.tiers, tiers) && slices.Equal(cand.levels, levels) {
				g = cand
				break
			}
		}
		if g == nil {
			g = &axesGroup{tiers: tiers, levels: levels}
			groups = append(groups, g)
		}
		g.models = append(g.models, m)
	}
	var out []runner.ExecutionCapability
	for _, op := range ops {
		for _, g := range groups {
			for chunk := range slices.Chunk(g.models, MaxCapabilityModels) {
				c := runner.ExecutionCapability{
					Op: op, Backend: backend, Provider: providersadapter.ProviderClaudeCLI, EndpointFingerprint: fp,
					Families: append([]modelidentity.Family(nil), fams...), Models: append([]string(nil), chunk...),
					ContextWindows: append([]provider.ContextWindow(nil), g.tiers...), Efforts: append([]provider.Effort(nil), g.levels...),
					Tools: append([]string(nil), tools...), ExecutedModel: true,
				}
				if op == runner.OpAgent {
					// G2 (user decision 2026-11-23): codex cannot restrict tools;
					// claude-code enforces an explicit allowlist.
					c.ToolPolicy = runner.ToolPolicyAllowlist
					if backend == workflow.BackendCodex {
						c.ToolPolicy, c.Tools = runner.ToolPolicyUnrestricted, nil
					}
				}
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// inventorySignature identifies a discovery result: sources, evidence,
// reasons and harness versions. Health is excluded (it is the prober's).
func inventorySignature(specs []Spec, harness map[string]string) string {
	var b strings.Builder
	for _, sp := range specs {
		s := sp.Source
		fmt.Fprintf(&b, "%s|%s|%v|%v|%v|%s|%v\n", s.Kind, s.CredentialRef, s.Families, s.Backends, s.Models, s.UnavailableReason, s.Capabilities)
	}
	keys := make([]string, 0, len(harness))
	for k := range harness {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, harness[k])
	}
	return b.String()
}
