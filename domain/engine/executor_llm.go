package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// LLMCallExecutor reads a prompt template, resolves substitutions, resolves a
// chat profile for its model, calls a ChatProvider with no tools, and returns
// the structured or text response.
//
// Forward note (Phase 3): the executor holds a single ChatProvider (claude-cli
// is the only P1 backend) and uses the resolver only for prof.Model. Phase 3
// swaps the single-provider field for a ChatProviderSelector so the concrete
// backend is chosen per node from the resolved binding's Provider. The resolver
// call and the Chat call are kept cleanly separable so that change stays a
// localized field swap.
type LLMCallExecutor struct {
	fileReader  FileReader
	llmProvider ChatProvider
	resolver    provider.ProfileResolver
}

// NewLLMCallExecutor creates an LLMCallExecutor with the given dependencies.
func NewLLMCallExecutor(fr FileReader, chatProvider ChatProvider, resolver provider.ProfileResolver) *LLMCallExecutor {
	return &LLMCallExecutor{
		fileReader:  fr,
		llmProvider: chatProvider,
		resolver:    resolver,
	}
}

// Execute reads the template file, resolves variable substitutions, calls the
// LLM provider, and processes the response.
func (e *LLMCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*NodeResult, error) {
	// Read template file.
	templateBytes, err := e.fileReader.ReadFile(workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: read template: %w", node.ID, err)
	}

	// Resolve substitutions in template.
	prompt := variable.Resolve(string(templateBytes), vars)

	// Resolve the chat profile alias to a concrete model. The request stays
	// tool-suppressed by construction (the ChatProvider never has tools), so
	// llm_call can no longer edit files.
	prof, err := e.resolver.Resolve(node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: resolve profile: %w", node.ID, err)
	}

	workDirStr, _ := WorkDirFromVars(vars)

	// Build LLM request.
	req := ChatRequest{
		Model:        prof.Model,
		Prompt:       prompt,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		OutputSchema: node.Config.OutputSchema,
		WorkDir:      workDirStr,
	}

	// Call LLM provider.
	resp, err := e.llmProvider.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: provider call: %w", node.ID, err)
	}

	// Process output.
	output := processLLMOutput(resp, node.Config.OutputSchema)

	return &NodeResult{
		Output:          output,
		LLMConversation: resp.Conversation,
		LLMModel:        resp.Model,
		LLMTokensIn:     resp.TokensIn,
		LLMTokensOut:    resp.TokensOut,
	}, nil
}

// processLLMOutput determines the final output value based on whether an
// output schema was provided and the response type.
func processLLMOutput(resp *ChatResponse, outputSchema any) any {
	// If output schema provided, use the structured output directly.
	if outputSchema != nil {
		return resp.Output
	}

	// No schema: attempt to parse text as JSON, otherwise wrap.
	switch v := resp.Output.(type) {
	case string:
		return parseOrWrapText(v)
	default:
		// Already structured (shouldn't happen without schema, but handle gracefully).
		return v
	}
}

// parseOrWrapText tries to parse a string as JSON. If it fails, wraps the
// text in {"response": "..."}.
func parseOrWrapText(text string) any {
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		return parsed
	}
	return map[string]any{"response": text}
}
