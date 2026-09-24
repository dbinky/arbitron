package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	templateText, err := loadTemplate(e.fileReader, ownerFromContext(ctx), workflowName, node.Config.TemplateFile)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: read template: %w", node.ID, err)
	}

	// Resolve substitutions in template.
	prompt := variable.Resolve(templateText, vars)

	// When the node declares an OutputSchema, instruct the model to emit ONLY
	// JSON conforming to it. The claude-cli adapter does not pass OutputSchema
	// to the model, so without this the model sees only the template prose and
	// intermittently wraps its JSON in a heading/prose/code fence — the root
	// cause of repeated structured-output parse failures (e.g. impl-assess).
	if node.Config.OutputSchema != nil {
		prompt += structuredOutputInstruction(node.Config.OutputSchema)
	}

	// Resolve the chat profile alias to a concrete model. The request stays
	// tool-suppressed by construction (the ChatProvider never has tools), so
	// llm_call can no longer edit files.
	binding, err := e.resolver.Resolve(ctx, node.Config.ModelProfile)
	if err != nil {
		return nil, fmt.Errorf("llm_call %q: resolve profile: %w", node.ID, err)
	}

	// FAIL CLOSED on a binding.Kind <-> node.Type mismatch (mirrors the
	// interactive executor). An llm_call must resolve to a chat (tool-suppressed)
	// binding; an agent-kind binding here would be a posture mismatch. Guard
	// before selecting/calling any provider — see ErrBindingKindMismatch.
	if err := assertBindingKind(node.ID, node.Type, binding.Kind); err != nil {
		return nil, fmt.Errorf("llm_call %q: %w", node.ID, err)
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
// PROVIDER-AGNOSTIC structured output: this is the SOLE parser of structured
// output. Every ChatProvider returns the model's raw assistant text in
// resp.Output (a string) — adapters never pre-parse — so ChatResponse.Output
// means exactly one thing pre-parse. When a schema is present the string is
// parsed into a map here so downstream node.field references resolve identically
// no matter which ChatProvider produced it. An empty or non-JSON string under a
// schema is a clear error. A non-string Output under a schema is a contract
// violation (an adapter pre-parsed) and is reported, not silently passed through.
func processLLMOutput(resp *ChatResponse, outputSchema any) (any, error) {
	if outputSchema != nil {
		s, ok := resp.Output.(string)
		if !ok {
			return nil, fmt.Errorf("structured output: expected raw string from provider, got %T", resp.Output)
		}
		cleaned := stripCodeFence(s)
		var parsed any
		if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
			// Tolerate prose/markdown around the JSON: extract the first
			// balanced object and parse that. Models intermittently wrap the
			// verdict in a heading or sentence even when asked for raw JSON.
			obj, found := extractJSONObject(cleaned)
			if !found {
				// LOUD FAILURE (PR #18, comment 3350531055): a node declaring an
				// OutputSchema must NOT get a silent {"response": rawtext}
				// fallback when nothing parseable is recovered — that would
				// persist a malformed verdict as a valid structured result.
				return nil, fmt.Errorf("parse structured output: no JSON object found in model output: %w", err)
			}
			if err2 := json.Unmarshal([]byte(obj), &parsed); err2 != nil {
				return nil, fmt.Errorf("parse structured output: %w", err2)
			}
		}
		// STRICT SCHEMA VALIDATION (PR #18 HIGH, comment 4614959041): fail
		// closed on any object that does not conform to the declared schema.
		// This stops a forged verdict lifted from attacker-influenced prose
		// (e.g. {"done":true}) from being accepted as the authoritative,
		// control-flow-gating result.
		if err := validateAgainstSchema(parsed, outputSchema); err != nil {
			return nil, fmt.Errorf("structured output does not conform to schema: %w", err)
		}
		return parsed, nil
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
	if err := json.Unmarshal([]byte(stripCodeFence(text)), &parsed); err == nil {
		return parsed
	}
	return map[string]any{"response": text}
}

// structuredOutputInstruction returns the directive appended to a prompt when
// the node declares an OutputSchema. The claude-cli adapter never forwards the
// schema to the model, so this is the only signal telling the model to produce
// JSON (and which shape). Demanding raw JSON with no prose/markdown/fences is
// what makes structured llm_call output reliable.
func structuredOutputInstruction(schema any) string {
	return "\n\n---\nIMPORTANT: Respond with ONLY a single JSON object conforming to the " +
		"schema below. Do not include any prose, explanation, markdown, headings, or code " +
		"fences. Your entire response must be valid JSON beginning with '{'.\n\nJSON schema:\n" +
		schemaDirective(schema)
}

// extractJSONObject returns the first balanced {...} object in s, ignoring any
// surrounding prose/markdown. Brace counting is string- and escape-aware so
// braces inside string values do not throw off the depth. ok is false when no
// balanced object is found.
func extractJSONObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// stripCodeFence removes a single surrounding markdown code fence from s, if
// present. Models — especially agent/CLI adapters that emit conversational
// text — frequently wrap structured JSON in ```json ... ``` (or a bare ```
// fence), which is not valid JSON. We strip the opening fence line (and its
// optional language tag) and the trailing fence so the inner payload can be
// parsed. If s is not fenced it is returned unchanged (trimmed of surrounding
// whitespace), so well-formed JSON is unaffected.
func stripCodeFence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Drop everything up to and including the first newline (the opening fence
	// line, e.g. "```" or "```json").
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[i+1:]
	} else {
		// Single-line fence with no body — nothing useful to extract.
		return t
	}
	// Drop a trailing closing fence if present.
	t = strings.TrimSpace(t)
	if j := strings.LastIndex(t, "```"); j >= 0 {
		t = t[:j]
	}
	return strings.TrimSpace(t)
}
