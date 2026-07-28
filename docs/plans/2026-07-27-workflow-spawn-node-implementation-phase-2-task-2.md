# Phase 2 — Task 2: `LLMChildInputPreparer` — the tool-suppressed preparation call

**Goal:** Implement `ChildInputPreparer` with a tool-suppressed chat call: assemble the prompt from the child's contract + the parent's context + optional guidance, resolve the profile, assert the binding kind, select the backend, call, parse, and validate.

**Architecture:** New file `domain/engine/child_input_preparer.go`. It reuses `LLMCallExecutor`'s proven call sequence (resolve → assert kind → select → chat → parse) and its `structuredOutputInstruction` helper verbatim rather than inventing a second way to make a structured LLM call — the claude-cli adapter does not forward a schema to the model, so that instruction is the only signal telling it to emit raw JSON. There is no retry loop inside the preparer: node-level retry already exists in `executeWithRetry` and a private loop would multiply against it invisibly.

**Tech Stack:** Go 1.25.5, standard library `testing`/`encoding/json`, `testify`, module `github.com/arbitron/arbitron`.

---

### Task 2: preparer happy path and success cases

**Files:**
- Create: `domain/engine/child_input_preparer.go`
- Test:   `domain/engine/child_input_preparer_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/child_input_preparer_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childFields is the standard two-required-one-optional child contract used
// across these tests.
func childFields() []workflow.InputField {
	return []workflow.InputField{
		{Name: "repo", Type: "string", Description: "target repository", Required: true},
		{Name: "branch", Type: "string", Description: "target branch", Required: true},
		{Name: "dryRun", Type: "boolean", Description: "skip writes", Required: false},
	}
}

// respondWith builds a chat provider that returns the given raw text and
// records the request it was called with.
func respondWith(raw string, got *engine.ChatRequest, calls *int) *mockChatProvider {
	return &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			*calls++
			*got = req
			return &engine.ChatResponse{Output: raw, Model: req.Model}, nil
		},
	}
}

// ---------- happy path ----------

func TestChildInputPreparer_HappyPath(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"arbitron/arbitron","branch":"main","dryRun":false}`, &got, &calls)

	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("gpt-4o"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID:        "spawn-1",
		ChildWorkflow: "child-pipeline",
		Inputs:        childFields(),
		ParentContext: map[string]any{"repository": "arbitron/arbitron", "targetBranch": "main"},
		ProfileAlias:  "chat-default",
	})
	require.NoError(t, err)
	assert.Equal(t, "arbitron/arbitron", values["repo"])
	assert.Equal(t, "main", values["branch"])
	assert.Equal(t, false, values["dryRun"])

	assert.Equal(t, 1, calls)
	assert.Equal(t, "gpt-4o", got.Model,
		"the call must be made against the RESOLVED binding's model, not the alias")
}

// ---------- success ----------

func TestChildInputPreparer_PromptCarriesChildContract(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID:        "spawn-1",
		ChildWorkflow: "child-pipeline",
		Inputs:        childFields(),
		ParentContext: map[string]any{"repository": "arbitron/arbitron"},
		ProfileAlias:  "chat-default",
	})
	require.NoError(t, err)

	assert.Contains(t, got.Prompt, "child-pipeline", "the prompt must name the child workflow")
	assert.Contains(t, got.Prompt, "target repository", "each declared input's description must reach the prompt")
	assert.Contains(t, got.Prompt, "repo")
	assert.Contains(t, got.Prompt, "branch")
	assert.Contains(t, got.Prompt, "dryRun")
}

func TestChildInputPreparer_StructuredOutputInstructionAppended(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)

	// The shared helper's exact wording — reusing it means the preparer inherits
	// any future improvement to the structured-output instruction.
	assert.Contains(t, got.Prompt, "Respond with ONLY a single JSON object")
	assert.Contains(t, got.Prompt, "JSON schema:")
	require.NotNil(t, got.OutputSchema, "the synthesized schema must ride the request too")
}

func TestChildInputPreparer_ParentNodeOutputsVisibleInPrompt(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{
			"userInput": "please ship it",
			// a prior node's output, the shape vars.Snapshot() produces
			"analyzer": map[string]any{"chosenBranch": "release/2.0"},
		},
		ProfileAlias: "chat-default",
	})
	require.NoError(t, err)

	assert.Contains(t, got.Prompt, "please ship it", "the parent's inputs must be visible")
	assert.Contains(t, got.Prompt, "release/2.0",
		"the parent's prior NODE OUTPUTS must be visible, not just its inputs")
}

func TestChildInputPreparer_GuidanceReachesPrompt(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{},
		Guidance:      "Always prefer the release branch over main.",
		ProfileAlias:  "chat-default",
	})
	require.NoError(t, err)
	assert.Contains(t, got.Prompt, "Always prefer the release branch over main.")
}

func TestChildInputPreparer_NoGuidanceIsNotAnError(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err, "an UNCONFIGURED guidance file is not an error")
}

// A child with zero declared inputs succeeds with an empty map WITHOUT calling
// the provider at all — there is no contract to satisfy, so an LLM call would
// be pure cost.
func TestChildInputPreparer_ZeroDeclaredInputs_NoProviderCall(t *testing.T) {
	calls := 0
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			calls++
			return &engine.ChatResponse{Output: "{}"}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: nil,
		ParentContext: map[string]any{"a": 1}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	assert.NotNil(t, values)
	assert.Empty(t, values)
	assert.Equal(t, 0, calls, "a child with no declared inputs must not cost a provider call")
}

func TestChildInputPreparer_ExtraKeysPassedThrough(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b","note":"context for the child"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	assert.Equal(t, "context for the child", values["note"],
		"extra keys are seeded by the trigger path and must not be stripped here")
}

func TestChildInputPreparer_AbsentOptionalStaysAbsent(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	_, present := values["dryRun"]
	assert.False(t, present,
		"an absent OPTIONAL input must be left absent so the child's own defaulting applies")
}

// The preparation call is a REASONING call: it must carry no tool scope.
func TestChildInputPreparer_RequestCarriesNoToolScope(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	assert.Empty(t, got.ToolScope, "input preparation is a tool-suppressed reasoning call")
}

// The synthesized schema must be the CHILD's contract, verbatim.
func TestChildInputPreparer_SchemaOnRequestMatchesChildContract(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)

	b, mErr := json.Marshal(got.OutputSchema)
	require.NoError(t, mErr)
	s := string(b)
	assert.True(t, strings.Contains(s, `"required"`), "schema must declare required inputs: %s", s)
	assert.True(t, strings.Contains(s, `"repo"`) && strings.Contains(s, `"branch"`), "schema: %s", s)
}

// compile-time proof the concrete type satisfies the port.
var _ engine.ChildInputPreparer = (*engine.LLMChildInputPreparer)(nil)

// the chat kind is what the guard demands for a spawn node
var _ provider.ProfileKind = provider.ProfileKindChat
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run TestChildInputPreparer
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/child_input_preparer_test.go:...: undefined: engine.NewChildInputPreparer
domain/engine/child_input_preparer_test.go:...: undefined: engine.LLMChildInputPreparer
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Minimal implementation**

Create `domain/engine/child_input_preparer.go`:

```go
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
		req := "optional"
		if f.Required {
			req = "REQUIRED"
		}
		b.WriteString("  - ")
		b.WriteString(f.Name)
		if f.Type != "" {
			b.WriteString(" (")
			b.WriteString(f.Type)
			b.WriteString(")")
		}
		b.WriteString(" [")
		b.WriteString(req)
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
```

- [ ] **Step 4: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run TestChildInputPreparer -v
```

Expected output:

```
--- PASS: TestChildInputPreparer_HappyPath (0.00s)
--- PASS: TestChildInputPreparer_PromptCarriesChildContract (0.00s)
--- PASS: TestChildInputPreparer_StructuredOutputInstructionAppended (0.00s)
--- PASS: TestChildInputPreparer_ParentNodeOutputsVisibleInPrompt (0.00s)
--- PASS: TestChildInputPreparer_GuidanceReachesPrompt (0.00s)
--- PASS: TestChildInputPreparer_NoGuidanceIsNotAnError (0.00s)
--- PASS: TestChildInputPreparer_ZeroDeclaredInputs_NoProviderCall (0.00s)
--- PASS: TestChildInputPreparer_ExtraKeysPassedThrough (0.00s)
--- PASS: TestChildInputPreparer_AbsentOptionalStaysAbsent (0.00s)
--- PASS: TestChildInputPreparer_RequestCarriesNoToolScope (0.00s)
--- PASS: TestChildInputPreparer_SchemaOnRequestMatchesChildContract (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.4s
```

- [ ] **Step 5: Confirm the package still passes**

```bash
go test ./domain/engine/
```

Expected: `ok  	github.com/arbitron/arbitron/domain/engine`

- [ ] **Step 6: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/child_input_preparer.go domain/engine/child_input_preparer_test.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): implement the LLM child input preparer"
```
