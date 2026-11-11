package claudecli

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// LR-05's single environment-name table. P3-T5 verifies these controls against
// the installed binary; readiness remains gated independently of emission.
const (
	lunaRouteAuthTokenVar  = "ANTHROPIC_AUTH_TOKEN"
	lunaRouteHintVar       = "CLAUDE_CODE_GATEWAY_HINT_HEADERS"
	lunaRouteMaxContextVar = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"
	lunaRouteMaxOutputVar  = "CLAUDE_CODE_MAX_OUTPUT_TOKENS"
	claudeEffortLevelVar   = "CLAUDE_CODE_EFFORT_LEVEL"
	claudeMaxThinkingVar   = "MAX_THINKING_TOKENS"
)

var lunaRouteModelSelectorVars = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}
var lunaRouteSuppressedVars = []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_DISABLE_1M_CONTEXT"}

// lunaRouteAgentEnv refuses endpoint, tier, limits and effort (in that order)
// before revealing credentials. Pure builders may use explicit fixture ids;
// real entry points additionally enforce reviewed identity and compatibility.
func lunaRouteAgentEnv(ctx context.Context, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	refuse := func(reason error, detail string) error {
		return &provider.LunaRouteRefusal{Reason: reason, Model: binding.Model, Backend: workflow.BackendClaudeCode, Detail: detail}
	}
	base, err := provider.LunaRouteMessagesBaseURL(binding.BaseURL)
	if err != nil {
		return nil, err
	}
	if binding.ContextWindow != "" && binding.ContextWindow != provider.ContextDefault {
		return nil, refuse(provider.ErrLunaRouteAxisUnsupported, fmt.Sprintf("context %q is a Claude-native switch, not a LunaRoute model limit; set context to default", binding.ContextWindow))
	}
	m, _ := rules.LunaRouteModel(binding.Model)
	if !m.ContextTokens.Known() || !m.OutputTokens.Known() {
		return nil, refuse(provider.ErrLunaRouteLimitsUnknown, "claude-code on LunaRoute needs known context and output limits for this model in the capability table (lunaroute_models)")
	}
	effortEnv, err := lunaRouteEffortEnv(binding.Effort, m, refuse)
	if err != nil {
		return nil, err
	}
	guarded := provider.GuardLunaRouteReveal(binding)
	env, err := procenv.CredentialEnv(ctx, procenv.CredentialSpec{Runner: "claudecli", KeyVar: lunaRouteAuthTokenVar, BaseURLVar: anthropicBaseURLVar, BaseURL: base, Reveal: guarded.RevealKey, EmptyKey: procenv.EmptyKeyReject})
	if err != nil {
		return nil, err
	}
	strip := slices.Concat(lunaRouteModelSelectorVars, lunaRouteSuppressedVars, []string{anthropicKeyVar, lunaRouteHintVar, lunaRouteMaxContextVar, lunaRouteMaxOutputVar, claudeEffortLevelVar, claudeMaxThinkingVar})
	for _, name := range strip {
		env = procenv.RemoveVar(env, name)
	}
	for _, name := range lunaRouteModelSelectorVars {
		env = append(env, name+"="+binding.Model)
	}
	env = append(env, lunaRouteHintVar+"=1", lunaRouteMaxContextVar+"="+strconv.Itoa(m.ContextTokens.Tokens), lunaRouteMaxOutputVar+"="+strconv.Itoa(m.OutputTokens.Tokens))
	return append(env, effortEnv...), nil
}
func lunaRouteEffortEnv(e provider.Effort, m provider.LunaRouteModel, refuse func(error, string) error) ([]string, error) {
	if e == "" || e == provider.EffortDefault {
		return nil, nil
	}
	if !e.Valid() {
		return nil, provider.ErrInvalidEffort
	}
	if f := m.Effort(workflow.BackendClaudeCode); !f.Known || !slices.Contains(f.Levels, e) {
		return nil, refuse(provider.ErrLunaRouteAxisUnsupported, fmt.Sprintf("effort %q is not known to be supported for this model on claude-code; set effort to default or record evidence in the capability table", e))
	}
	if e == provider.EffortNone {
		return []string{claudeMaxThinkingVar + "=0"}, nil
	}
	return []string{claudeEffortLevelVar + "=" + string(e)}, nil
}

// prepareLunaRouteModel is shared by batch, interactive and probe entries. It
// runs before reveal or subprocess I/O, without bypassing the factory's gate.
func prepareLunaRouteModel(model string, b provider.ResolvedBinding, rules provider.CapabilityRules) (string, error) {
	if !provider.IsLunaRoute(b.Provider) {
		return model, nil
	}
	if model != "" && model != b.Model {
		return "", provider.LocalLunaRouteError("model selection", errModelExecutionEvidence)
	}
	if err := provider.CheckLunaRouteAdapterLaunch(rules, b, 0); err != nil {
		return "", provider.LocalLunaRouteError("preflight", err)
	}
	return b.Model, nil
}
