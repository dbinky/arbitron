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
// an agent profile for its model, runs an AgentProvider mission inside the
// instance {{workspace}}, and records the result's steps/summary/tokens.
//
// Forward note (Phase 3): the executor holds a single AgentProvider (claude-cli
// is the only P1 backend) and uses the resolver only for prof.Model. It evolves
// symmetrically to the LLMCallExecutor's ChatProviderSelector — Phase 3 swaps
// the single-provider field for an AgentProviderSelector chosen per node from
// the resolved binding. The resolve call and the RunAgent call are kept cleanly
// separable so that change stays a localized field swap.
type AgentCallExecutor struct {
	fileReader    FileReader
	agentProvider AgentProvider
	resolver      provider.ProfileResolver
}

// NewAgentCallExecutor creates an AgentCallExecutor with the given dependencies.
func NewAgentCallExecutor(fr FileReader, ap AgentProvider, resolver provider.ProfileResolver) *AgentCallExecutor {
	return &AgentCallExecutor{
		fileReader:    fr,
		agentProvider: ap,
		resolver:      resolver,
	}
}

// Execute reads the mission template, resolves variable substitutions, resolves
// the agent profile, runs the agent mission seeded from {{workspace}}, and
// records the steps/summary/tokens on the result.
func (e *AgentCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// Read mission template file.
	templateBytes, err := e.fileReader.ReadFile(workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: read template: %w", node.ID, err)
	}

	// Resolve substitutions in template.
	prompt := variable.Resolve(string(templateBytes), vars)

	// Resolve the agent profile alias to a concrete model.
	prof, err := e.resolver.Resolve(node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("agent_call %q: resolve profile: %w", node.ID, err)
	}

	// Seed WorkDir from the reserved {{workspace}} variable. An unseeded
	// workspace yields an empty WorkDir, which the AgentProvider (claude-cli
	// AgentAdapter) rejects via ErrWorkDirRequired — propagate that rather than
	// silently running.
	workDirStr, _ := WorkDirFromVars(vars)

	// Build the mission. SessionID is left empty in P1 — single-shot mission per
	// step; cross-step resume is a Phase-3 decision pending the opencode spike.
	mission := AgentMission{
		Model:     prof.Model,
		Prompt:    prompt,
		WorkDir:   workDirStr,
		ToolScope: node.Config.ToolScope,
		Timeout:   time.Duration(node.Config.TimeoutSeconds) * time.Second,
	}

	res, err := e.agentProvider.RunAgent(ctx, mission)
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
