package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// AgentCallExecutor reads a mission template, resolves substitutions, resolves
// an agent profile for its model, selects the concrete AgentProvider for the
// resolved binding, runs the mission inside the instance {{workspace}}, and
// records the result's steps/summary/tokens.
//
// Phase 3: the executor holds an AgentProviderSelector (not a single raw
// provider), mirroring the LLMCallExecutor. After resolving the alias to a
// binding it selects the backend per node from binding.Provider — claude-cli
// (subscription) or opencode (non-Claude) — without importing either adapter.
type AgentCallExecutor struct {
	fileReader FileReader
	selector   AgentProviderSelector
	resolver   provider.ProfileResolver
	// defaultAgentBackend is the deployment-wide fallback agent backend
	// (org-readiness Phase 2) applied when neither the node nor its profile names
	// one. It is the LAST step of the precedence chain node > profile > default.
	// An empty value here is intentional: the executor never invents a backend
	// itself, leaving the final fallback to the composition root / factory so the
	// deployment policy lives in exactly one place.
	defaultAgentBackend string
}

// NewAgentCallExecutor creates an AgentCallExecutor with the given dependencies.
// The selector chooses the concrete AgentProvider per resolved binding.
// defaultAgentBackend is the deployment fallback used when neither the node nor
// its profile selects a backend (precedence node > profile > default).
func NewAgentCallExecutor(fr FileReader, selector AgentProviderSelector, resolver provider.ProfileResolver, defaultAgentBackend string) *AgentCallExecutor {
	return &AgentCallExecutor{
		fileReader:          fr,
		selector:            selector,
		resolver:            resolver,
		defaultAgentBackend: defaultAgentBackend,
	}
}

// firstNonEmpty returns the first non-empty string from vals, or "" if all are
// empty. It encodes the backend precedence chain (node > profile > default).
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Execute reads the mission template, resolves variable substitutions, resolves
// the agent profile, runs the agent mission seeded from {{workspace}}, and
// records the steps/summary/tokens on the result.
func (e *AgentCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// Read mission template file.
	templateBytes, err := e.fileReader.ReadFile(ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: read template: %w", node.ID, err)
	}

	// Resolve substitutions in template.
	prompt := variable.Resolve(string(templateBytes), vars)

	// Resolve the agent profile alias to a concrete model.
	binding, err := e.resolver.Resolve(ctx, node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: resolve profile: %w", node.ID, err)
	}

	// Compute the EFFECTIVE agent backend with precedence node > profile >
	// deployment default (org-readiness Phase 2). The backend axis is INDEPENDENT
	// of binding.Provider; set it on the binding so the selector dispatches on it.
	// An all-empty chain leaves Backend "" — the factory then applies its own
	// deployment default (single fallback seam).
	binding.Backend = firstNonEmpty(node.Config.Backend, binding.Backend, e.defaultAgentBackend)

	// Select the concrete agent backend for this node from binding.Backend
	// (claude-code -> claude-cli adapter / opencode). No adapter is imported here.
	agentProvider, err := e.selector.AgentProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: select provider: %w", node.ID, err)
	}

	// Seed WorkDir from the reserved {{workspace}} variable. An unseeded
	// workspace yields an empty WorkDir, which the AgentProvider (claude-cli
	// AgentAdapter) rejects via ErrWorkDirRequired — propagate that rather than
	// silently running.
	workDirStr, _ := WorkDirFromVars(vars)

	// Tool scope precedence (PR #18 item 1b): the resolved profile's per-profile
	// AllowedTools is authoritative for the mission, so reviewer/coder/planner
	// run against their explicit minimal allowlist rather than the adapter's
	// YOLO all-tools default. node.Config.ToolScope remains the fallback when a
	// profile carries no allowlist (back-compat for nodes that scoped tools
	// directly). The adapter applies its own global scope only if both are empty.
	toolScope := binding.AllowedTools
	if len(toolScope) == 0 {
		toolScope = node.Config.ToolScope
	}

	// Build the mission. SessionID is left empty in P1 — single-shot mission per
	// step; cross-step resume is a Phase-3 decision pending the opencode spike.
	mission := AgentMission{
		Model:     binding.Model,
		Prompt:    prompt,
		WorkDir:   workDirStr,
		ToolScope: toolScope,
		Timeout:   time.Duration(node.Config.TimeoutSeconds) * time.Second,
	}

	res, err := agentProvider.RunAgent(ctx, mission)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: run agent: %w", node.ID, err)
	}

	output := processAgentOutput(res)

	return &NodeResult{
		Output: output,
		// Record the agent trace (steps + summary) on LLMConversation, keeping
		// the executor adapter-free; NodeResult is not widened for the agent
		// trace in P1.
		LLMConversation: map[string]any{
			"steps":   res.Steps,
			"summary": res.Summary,
		},
		LLMModel:     res.Model,
		LLMTokensIn:  res.TokensIn,
		LLMTokensOut: res.TokensOut,
	}, nil
}

// processAgentOutput shapes the final output from an agent result. The agent
// backend always returns its result as Summary text, so both the schema and
// no-schema cases parse-or-wrap that text: a JSON summary (the schema-shaped
// result) becomes structured output, a plain summary is wrapped in
// {"response": ...}. This mirrors processLLMOutput's parseOrWrapText branch.
func processAgentOutput(res *AgentResult) any {
	return parseOrWrapText(res.Summary)
}
