package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// LLMChildInputPreparer implements ChildInputPreparer with a tool-suppressed
// chat call.
//
// It mirrors LLMCallExecutor's dependency set and reuses its proven call
// sequence (resolve profile -> assert binding kind -> select provider -> chat
// -> parse) rather than inventing a second way to make a structured LLM call.
// It deliberately holds NO instance repository, queue manager, or engine: its
// tests need a fake chat provider and nothing else, and its internals (prompt
// shape, schema synthesis) can change without any consumer changing.
//
// There is NO retry loop here. Node-level retry already exists in
// Engine.executeWithRetry, driven by the node's ErrorBehavior; a private loop
// would multiply against it invisibly.
type LLMChildInputPreparer struct {
	selector ChatProviderSelector
	resolver provider.ProfileResolver
}

// NewChildInputPreparer constructs the default preparer. The selector chooses
// the concrete ChatProvider per resolved binding, keeping every concrete
// adapter out of the domain.
func NewChildInputPreparer(selector ChatProviderSelector, resolver provider.ProfileResolver) *LLMChildInputPreparer {
	return &LLMChildInputPreparer{selector: selector, resolver: resolver}
}

// maxResponseExcerpt bounds how much of an unparseable response is echoed into
// the error message. An unparseable response is the single most likely runtime
// failure of this component, so the excerpt lets an operator diagnose it from
// the trace without re-running — but a whole model response in an error string
// would bloat every node-execution record.
const maxResponseExcerpt = 500

// Prepare implements ChildInputPreparer.
func (p *LLMChildInputPreparer) Prepare(ctx context.Context, req ChildInputRequest) (map[string]any, error) {
	// A child with no declared inputs has no contract to satisfy; an LLM call
	// would be pure cost.
	if len(req.Inputs) == 0 {
		return map[string]any{}, nil
	}

	// The child DOES declare inputs, so a preparation call is about to happen and
	// this node needs a profile. A spawn node's model_profile is optional at
	// AUTHOR time — validation cannot load the child to know whether it declares
	// inputs — so the requirement is enforced here, at the first moment it is
	// actually knowable, and named by node and child so the trace says which
	// node to fix. Resolving "" instead would surface as `unknown profile: ""`,
	// which reads like a registry problem rather than a missing config field.
	if req.ProfileAlias == "" {
		return nil, fmt.Errorf(
			"spawn_workflow %q: child workflow %q declares %d input(s), so this node must set a model_profile to prepare them",
			req.NodeID, req.ChildWorkflow, len(req.Inputs))
	}

	schema := childInputSchema(req.Inputs)
	prompt := childInputPrompt(req) + structuredOutputInstruction(schema)

	binding, err := p.resolver.Resolve(ctx, req.ProfileAlias)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: resolve profile: %w", req.NodeID, err)
	}

	// FAIL CLOSED on a binding.Kind <-> node.Type mismatch BEFORE any provider is
	// selected, so a tool-enabled provider is never constructed for what must be
	// a tool-suppressed reasoning call.
	if err := assertBindingKind(req.NodeID, workflow.NodeTypeSpawnWorkflow, binding.Kind); err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: %w", req.NodeID, err)
	}

	chatProvider, err := p.selector.ChatProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: select provider: %w", req.NodeID, err)
	}

	resp, err := chatProvider.Chat(ctx, ChatRequest{
		Model:        binding.Model,
		Prompt:       prompt,
		OutputSchema: schema,
		WorkDir:      req.WorkDir,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: %w", req.NodeID, err)
	}

	produced, err := parseChildInputResponse(req.NodeID, resp)
	if err != nil {
		return nil, err
	}

	if err := validateChildInputs(req.ChildWorkflow, req.Inputs, produced); err != nil {
		// No PARTIAL result is returned alongside the error: the caller must not
		// be able to spawn with half a payload.
		return nil, fmt.Errorf("spawn_workflow %q: %w", req.NodeID, err)
	}
	return produced, nil
}

// parseChildInputResponse turns the provider's raw text into a JSON object.
//
// Every ChatProvider returns the model's raw assistant text in resp.Output (a
// string) — adapters never pre-parse — so a non-string here is a contract
// violation and is reported rather than silently passed through.
func parseChildInputResponse(nodeID string, resp *ChatResponse) (map[string]any, error) {
	if resp == nil {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: provider returned no response", nodeID)
	}
	raw, ok := resp.Output.(string)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: expected raw string from provider, got %T",
			nodeID, resp.Output)
	}

	cleaned := stripCodeFence(raw)
	var parsed any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		// Tolerate prose/markdown around the JSON: extract the first balanced
		// object and parse that. Models intermittently wrap the payload in a
		// heading or sentence even when asked for raw JSON.
		obj, found := extractJSONObject(cleaned)
		if !found {
			return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: no JSON object found in model output: %q",
				nodeID, boundedExcerpt(raw))
		}
		if err2 := json.Unmarshal([]byte(obj), &parsed); err2 != nil {
			return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: parse: %v: %q",
				nodeID, err2, boundedExcerpt(raw))
		}
	}

	obj, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spawn_workflow %q: prepare child inputs: model produced a %T, not a JSON object: %q",
			nodeID, parsed, boundedExcerpt(raw))
	}
	return obj, nil
}

// boundedExcerpt returns at most maxResponseExcerpt bytes of s, marking a
// truncation so an operator reading the trace knows the value was cut.
func boundedExcerpt(s string) string {
	if len(s) <= maxResponseExcerpt {
		return s
	}
	return s[:maxResponseExcerpt] + "…(truncated)"
}

// childInputPrompt assembles the preparation prompt from the child's contract,
// the parent's available context, and the optional author guidance.
//
// Both the contract listing and the context listing are emitted in SORTED key
// order so the prompt is stable across runs (an unstable prompt defeats
// provider-side caching and makes trace diffs noisy).
func childInputPrompt(req ChildInputRequest) string {
	var b strings.Builder
	b.WriteString("You are preparing the startup values for a child workflow run.\n\n")
	b.WriteString("CHILD WORKFLOW: ")
	b.WriteString(req.ChildWorkflow)
	b.WriteString("\n\nThe child declares the following inputs. Its declaration is the contract; " +
		"produce a value for every REQUIRED input.\n")

	fields := append([]workflow.InputField(nil), req.Inputs...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	for _, f := range fields {
		requiredness := "optional"
		if f.Required {
			requiredness = "REQUIRED"
		}
		b.WriteString("  - ")
		b.WriteString(f.Name)
		if f.Type != "" {
			b.WriteString(" (")
			b.WriteString(f.Type)
			b.WriteString(")")
		}
		b.WriteString(" [")
		b.WriteString(requiredness)
		b.WriteString("]")
		if f.Description != "" {
			b.WriteString(": ")
			b.WriteString(f.Description)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nAVAILABLE CONTEXT FROM THE PARENT RUN (its inputs and the outputs of every " +
		"node executed so far):\n")
	if len(req.ParentContext) == 0 {
		b.WriteString("  (none)\n")
	} else {
		keys := make([]string, 0, len(req.ParentContext))
		for k := range req.ParentContext {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("  - ")
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(renderContextValue(req.ParentContext[k]))
			b.WriteString("\n")
		}
	}

	if req.Guidance != "" {
		b.WriteString("\nAUTHOR GUIDANCE:\n")
		b.WriteString(req.Guidance)
		b.WriteString("\n")
	}
	return b.String()
}

// renderContextValue renders one parent-context value for the prompt. Strings
// pass through; everything else is JSON-encoded so nested node outputs stay
// legible. A value that cannot be encoded degrades to its Go representation
// rather than failing the whole preparation.
func renderContextValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
