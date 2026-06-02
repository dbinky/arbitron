// Package workflows holds runnable example workflow folders (data, not engine
// code). This test proves the committed native-ralph-loop worked example parses
// and validates against the real workflow loader and validator under the
// capability profile checker that knows editor=agent and assessor=chat.
package workflows_test

import (
	"context"
	"os"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProfileChecker resolves the worked example's aliases to their capability
// kinds. Mirrors the real ProfileChecker: an unknown alias reports ok=false.
type mockProfileChecker struct {
	kinds map[string]string
}

func (m *mockProfileChecker) ProfileKind(alias string) (string, bool) {
	k, ok := m.kinds[alias]
	return k, ok
}

const nativeRalphLoop = "native-ralph-loop"

// loadExample loads the committed native-ralph-loop workflow via the real
// filesystem repo rooted at this examples directory.
func loadExample(t *testing.T) (*workflow.Workflow, *filesystem.FileReaderAdapter) {
	t.Helper()
	// Test runs with cwd = this package dir; use an absolute base path so the
	// repo's path-traversal guard accepts the workflow name.
	base, err := os.Getwd()
	require.NoError(t, err)
	repo := filesystem.NewWorkflowRepo(base)
	reader := filesystem.NewFileReader(base)

	wf, err := repo.Load(context.Background(), nativeRalphLoop)
	require.NoError(t, err, "loading committed example workflow")
	return wf, reader
}

// TestNativeRalphLoop_ParsesAndValidates is the worked-example-validation proof:
// the committed folder parses and Validate returns zero errors when the editor
// alias is an agent profile and the assessor alias is a chat profile, with file
// references resolved against the real filesystem reader.
func TestNativeRalphLoop_ParsesAndValidates(t *testing.T) {
	wf, reader := loadExample(t)

	checker := &mockProfileChecker{kinds: map[string]string{
		"editor":   "agent",
		"assessor": "chat",
	}}

	errs := workflow.Validate(wf, reader, checker, nil)
	assert.Empty(t, errs, "committed example must validate cleanly under the correct binding")
}

// TestNativeRalphLoop_Shape asserts the worked example expresses the 4-node cycle
// on existing node kinds plus a back-edge: agent_call editor -> llm_call assessor
// -> transform increment -> conditional (two exit conditions + a loop default and
// a back-edge to the entrypoint).
func TestNativeRalphLoop_Shape(t *testing.T) {
	wf, _ := loadExample(t)

	byID := make(map[string]workflow.Node, len(wf.Nodes))
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}

	// Node kinds and aliases.
	require.Equal(t, workflow.NodeTypeAgentCall, byID["editor"].Type)
	assert.True(t, byID["editor"].Entrypoint, "editor must be the entrypoint")
	assert.Equal(t, "editor", byID["editor"].Config.ModelProfile)

	require.Equal(t, workflow.NodeTypeLLMCall, byID["assessor"].Type)
	assert.Equal(t, "assessor", byID["assessor"].Config.ModelProfile)
	assert.NotNil(t, byID["assessor"].Config.OutputSchema, "assessor must declare a structured outputSchema")

	require.Equal(t, workflow.NodeTypeTransform, byID["increment-counter"].Type)
	require.Len(t, byID["increment-counter"].Config.Operations, 1)
	assert.Equal(t, "increment", byID["increment-counter"].Config.Operations[0].Operation)
	assert.Equal(t, "iteration", byID["increment-counter"].Config.Operations[0].Variable)

	// The conditional encodes (done OR counter>=max) as two conditions routing to
	// the same exit port, with the default routing back into the loop.
	cond := byID["loop-or-exit"]
	require.Equal(t, workflow.NodeTypeConditional, cond.Type)
	require.Len(t, cond.Config.Conditions, 2)
	assert.Equal(t, "exit", cond.Config.Conditions[0].Output)
	assert.Equal(t, "exit", cond.Config.Conditions[1].Output)
	assert.Equal(t, "assessor.done", cond.Config.Conditions[0].Field)
	assert.Equal(t, "iteration", cond.Config.Conditions[1].Field)
	assert.Equal(t, "gte", cond.Config.Conditions[1].Operator)
	assert.Equal(t, "loop", cond.Config.DefaultOutput)

	// The back-edge: loop-or-exit --loop--> editor (the entrypoint).
	var hasBackEdge, hasExitEdge bool
	for _, e := range wf.Edges {
		if e.Source == "loop-or-exit" && e.Output == "loop" && e.Target == "editor" {
			hasBackEdge = true
		}
		if e.Source == "loop-or-exit" && e.Output == "exit" {
			hasExitEdge = true
		}
	}
	assert.True(t, hasBackEdge, "back-edge loop-or-exit --loop--> editor must exist")
	assert.True(t, hasExitEdge, "exit edge from loop-or-exit must exist")
}

// TestNativeRalphLoop_IterationInputHasDefault0 verifies the iteration input is
// declared with default=0 so the counter is numeric before the first increment.
// A missing or non-numeric default would cause the transform to error on the
// first loop iteration.
func TestNativeRalphLoop_IterationInputHasDefault0(t *testing.T) {
	wf, _ := loadExample(t)

	var iterInput *workflow.InputField
	for i := range wf.Inputs {
		if wf.Inputs[i].Name == "iteration" {
			iterInput = &wf.Inputs[i]
			break
		}
	}
	require.NotNil(t, iterInput, "workflow must declare an 'iteration' input")
	assert.False(t, iterInput.Required, "iteration must not be required (has a default)")
	require.NotNil(t, iterInput.Default, "iteration input must have a default value")
	// The JSON number 0 unmarshals as float64. The transform increments this on
	// the first pass, so it must be numeric at the start.
	_, isNumeric := iterInput.Default.(float64)
	assert.True(t, isNumeric,
		"iteration default must be a numeric type (float64 from JSON 0), got %T", iterInput.Default)
}

// TestNativeRalphLoop_UnknownAliasFailsValidation verifies that a profile
// checker that doesn't know the editor or assessor aliases produces
// UNKNOWN_PROFILE errors (not a silent pass). This is the guard that prevents
// a miswired binding from silently skipping alias validation.
func TestNativeRalphLoop_UnknownAliasFailsValidation(t *testing.T) {
	wf, reader := loadExample(t)

	// Empty checker — no aliases known.
	checker := &mockProfileChecker{kinds: map[string]string{}}

	errs := workflow.Validate(wf, reader, checker, nil)

	var codes []string
	for _, e := range errs {
		codes = append(codes, e.Code)
	}
	assert.Contains(t, codes, "UNKNOWN_PROFILE",
		"unknown editor/assessor aliases must produce UNKNOWN_PROFILE errors")
}

// TestNativeRalphLoop_AssessmentTemplateInjectsEditorOutput verifies that the
// assessor template actually includes the editor's output via {{editor}}. The
// assessor (llm_call) is tool-suppressed: it cannot read workspace files, so
// the editor's summary is its only window into what changed this pass. Without
// {{editor}} in the template, the assessor reasons in a vacuum — it would
// always see the same prompt regardless of what the editor did.
func TestNativeRalphLoop_AssessmentTemplateInjectsEditorOutput(t *testing.T) {
	_, reader := loadExample(t)

	content, err := reader.ReadFile(nativeRalphLoop, "assessment.md")
	require.NoError(t, err, "reading assessment.md")

	// Resolve the template with a sentinel editor output. If {{editor}} is
	// absent from the template, the sentinel will not appear in the resolved
	// prompt and the test fails — the data never flows to the assessor.
	store := variable.NewStore()
	store.SetNodeOutput("editor", map[string]any{"response": "editor-summary-sentinel"})
	resolved := variable.Resolve(string(content), store)

	assert.Contains(t, resolved, "editor-summary-sentinel",
		"assessment.md must contain {{editor}} so the engine threads the editor's "+
			"output into the assessor's prompt; without it the assessor is blind to what was changed")
}

// TestNativeRalphLoop_ChatProfileOnEditorFailsValidation is the negative variant
// (acceptance #6 territory): binding the editor alias to a chat-only profile must
// fail validation with a PROFILE_KIND_MISMATCH, proving the same graph rejects a
// miswired binding.
func TestNativeRalphLoop_ChatProfileOnEditorFailsValidation(t *testing.T) {
	wf, reader := loadExample(t)

	checker := &mockProfileChecker{kinds: map[string]string{
		"editor":   "chat", // wrong: agent_call needs an agent profile
		"assessor": "chat",
	}}

	errs := workflow.Validate(wf, reader, checker, nil)

	var codes []string
	for _, e := range errs {
		codes = append(codes, e.Code)
	}
	assert.Contains(t, codes, "PROFILE_KIND_MISMATCH",
		"editor bound to a chat-only profile must fail capability validation")
}
