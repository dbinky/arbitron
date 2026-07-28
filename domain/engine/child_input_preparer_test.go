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

// Both the contract listing and the context listing must appear in the prompt
// in SORTED key order — documented in childInputPrompt as required so the
// prompt stays stable across runs for provider-side caching and quiet trace
// diffs. Go's map iteration order is randomized per run, so a regression that
// dropped the sort would produce a flaky (not a reliably-failing) prompt;
// asserting the exact order here is what pins the invariant instead of
// leaving it to chance.
func TestChildInputPreparer_PromptFieldsAndContextAreSortedForStability(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	unsortedFields := []workflow.InputField{
		{Name: "zebra", Type: "string", Required: false},
		{Name: "apple", Type: "string", Required: false},
		{Name: "mango", Type: "string", Required: false},
	}

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: unsortedFields,
		ParentContext: map[string]any{"zulu": 1, "alpha": 2, "mike": 3},
		ProfileAlias:  "chat-default",
	})
	require.NoError(t, err)

	fieldIdx := map[string]int{
		"apple": strings.Index(got.Prompt, "apple"),
		"mango": strings.Index(got.Prompt, "mango"),
		"zebra": strings.Index(got.Prompt, "zebra"),
	}
	require.NotEqual(t, -1, fieldIdx["apple"])
	require.NotEqual(t, -1, fieldIdx["mango"])
	require.NotEqual(t, -1, fieldIdx["zebra"])
	assert.True(t, fieldIdx["apple"] < fieldIdx["mango"] && fieldIdx["mango"] < fieldIdx["zebra"],
		"declared inputs must be listed alphabetically regardless of Inputs slice order: %s", got.Prompt)

	ctxIdx := map[string]int{
		"alpha: 2": strings.Index(got.Prompt, "alpha: 2"),
		"mike: 3":  strings.Index(got.Prompt, "mike: 3"),
		"zulu: 1":  strings.Index(got.Prompt, "zulu: 1"),
	}
	require.NotEqual(t, -1, ctxIdx["alpha: 2"])
	require.NotEqual(t, -1, ctxIdx["mike: 3"])
	require.NotEqual(t, -1, ctxIdx["zulu: 1"])
	assert.True(t, ctxIdx["alpha: 2"] < ctxIdx["mike: 3"] && ctxIdx["mike: 3"] < ctxIdx["zulu: 1"],
		"parent context must be listed alphabetically by key regardless of map iteration order: %s", got.Prompt)
}

// The preparer's ChatRequest must carry the request's WorkDir. Subprocess-backed
// chat providers (claude-cli) reject an empty WorkDir outright rather than
// silently inheriting the engine process's own cwd, so dropping this field
// here would fail every claude-cli-backed spawn node at the preparation call.
func TestChildInputPreparer_WorkDirReachesChatRequest(t *testing.T) {
	var got engine.ChatRequest
	calls := 0
	prov := respondWith(`{"repo":"r","branch":"b"}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	_, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
		WorkDir: "/var/arbitron/workspaces/parent-run-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "/var/arbitron/workspaces/parent-run-1", got.WorkDir,
		"the preparation call must carry the parent's WorkDir, not an empty string")
}

// compile-time proof the concrete type satisfies the port.
var _ engine.ChildInputPreparer = (*engine.LLMChildInputPreparer)(nil)

// the chat kind is what the guard demands for a spawn node
var _ provider.ProfileKind = provider.ProfileKindChat
