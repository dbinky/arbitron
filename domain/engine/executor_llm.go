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
// chat profile for its model, selects the concrete ChatProvider for the
// resolved binding, calls it with no tools, and returns the structured or text
// response.
//
// Phase 3: the executor holds a ChatProviderSelector (not a single raw
// provider). After resolving the alias to a binding it selects the backend per
// node from binding.Provider — claude-cli (subscription), openrouter/litellm,
// or azure — keeping the executor free of any concrete adapter import.
type LLMCallExecutor struct {
	fileReader FileReader
	selector   ChatProviderSelector
	resolver   provider.ProfileResolver
}

// NewLLMCallExecutor creates an LLMCallExecutor with the given dependencies.
// The selector chooses the concrete ChatProvider per resolved binding.
func NewLLMCallExecutor(fr FileReader, selector ChatProviderSelector, resolver provider.ProfileResolver) *LLMCallExecutor {
	return &LLMCallExecutor{
		fileReader: fr,
		selector:   selector,
		resolver:   resolver,
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
	binding, err := e.resolver.Resolve(node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: resolve profile: %w", node.ID, err)
	}

	// Select the concrete chat backend for this node from binding.Provider
	// (claude-cli / openrouter / azure). No adapter is imported here.
	chatProvider, err := e.selector.ChatProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: select provider: %w", node.ID, err)
	}

	workDirStr, _ := WorkDirFromVars(vars)

	// Build LLM request.
	req := ChatRequest{
		Model:        binding.Model,
		Prompt:       prompt,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		OutputSchema: node.Config.OutputSchema,
		WorkDir:      workDirStr,
	}

	// Call LLM provider.
	resp, err := chatProvider.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: provider call: %w", node.ID, err)
	}

	// Process output. With a schema, normalise the provider's output to a map
	// so node.field references (e.g. assessor.done) resolve uniformly REGARDLESS
	// of which ChatProvider produced it.
	output, err := processLLMOutput(resp, node.Config.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: %w", node.ID, err)
	}

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
//
// PROVIDER-AGNOSTIC structured output: when a schema is present, the result
// MUST be a map so downstream node.field references resolve identically no
// matter which ChatProvider produced it. Native HTTP adapters (openrouter)
// already parse the JSON into a map; subprocess/text adapters (claude-cli,
// azure) deliver it as a raw JSON string. This function bridges both: a string
// is parsed here; a map is passed through (no double-parse). An empty or
// non-JSON string under a schema is a clear error, not a silent passthrough.
func processLLMOutput(resp *ChatResponse, outputSchema any) (any, error) {
	if outputSchema != nil {
		switch v := resp.Output.(type) {
		case string:
			var parsed any
			if err := json.Unmarshal([]byte(v), &parsed); err != nil {
				return nil, fmt.Errorf("parse structured output: %w", err)
			}
			return parsed, nil
		default:
			// Already structured (e.g. openrouter mapped it); pass through.
			return v, nil
		}
	}

	// No schema: attempt to parse text as JSON, otherwise wrap.
	switch v := resp.Output.(type) {
	case string:
		return parseOrWrapText(v), nil
	default:
		// Already structured (shouldn't happen without schema, but handle gracefully).
		return v, nil
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
