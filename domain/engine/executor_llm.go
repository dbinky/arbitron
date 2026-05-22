package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// LLMCallExecutor reads a prompt template, resolves substitutions, calls an
// LLM provider, and returns the structured or text response.
type LLMCallExecutor struct {
	fileReader  FileReader
	llmProvider LLMProvider
}

// NewLLMCallExecutor creates an LLMCallExecutor with the given dependencies.
func NewLLMCallExecutor(fr FileReader, provider LLMProvider) *LLMCallExecutor {
	return &LLMCallExecutor{
		fileReader:  fr,
		llmProvider: provider,
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

	workDir, _ := vars.Get("workspace")
	workDirStr, _ := workDir.(string)

	// Build LLM request.
	req := LLMRequest{
		Model:        node.Config.Model,
		Prompt:       prompt,
		MaxTokens:    node.Config.MaxTokens,
		Temperature:  node.Config.Temperature,
		OutputSchema: node.Config.OutputSchema,
		WorkDir:      workDirStr,
	}

	// Call LLM provider.
	resp, err := e.llmProvider.Call(ctx, req)
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
func processLLMOutput(resp *LLMResponse, outputSchema any) any {
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
