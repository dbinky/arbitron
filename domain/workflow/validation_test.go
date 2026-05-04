package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockFileChecker is a test double for the FileChecker interface.
type mockFileChecker struct {
	existing map[string]bool
}

func (m *mockFileChecker) FileExists(_, path string) bool {
	return m.existing[path]
}

// minimalValidWorkflow returns a one-node, no-edge workflow that passes all checks.
func minimalValidWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Name:        "test",
		Description: "A minimal valid workflow",
		Inputs: []workflow.InputField{
			{Name: "text", Type: "string", Required: true},
		},
		Nodes: []workflow.Node{
			{
				ID:         "entry",
				Type:       workflow.NodeTypeTransform,
				Label:      "Entry",
				Entrypoint: true,
			},
		},
		Edges: []workflow.Edge{},
	}
}

func TestValidate_ValidWorkflowPasses(t *testing.T) {
	wf := minimalValidWorkflow()

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidate_NoEntryNode(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Entrypoint = false

	errs := workflow.Validate(wf, nil)
	require.Len(t, errs, 1)
	assert.Equal(t, "NO_ENTRY_NODE", errs[0].Code)
}

func TestValidate_MultipleEntryNodes(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes = append(wf.Nodes, workflow.Node{
		ID:         "entry2",
		Type:       workflow.NodeTypeTransform,
		Label:      "Entry 2",
		Entrypoint: true,
	})
	// Connect entry to entry2 so it's reachable (isolate the entry-count check)
	wf.Edges = []workflow.Edge{
		{Source: "entry", Target: "entry2"},
	}

	errs := workflow.Validate(wf, nil)
	require.Len(t, errs, 1)
	assert.Equal(t, "MULTIPLE_ENTRY_NODES", errs[0].Code)
}

func TestValidate_DuplicateNodeIDs(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes = append(wf.Nodes, workflow.Node{
		ID:    "entry",
		Type:  workflow.NodeTypeTransform,
		Label: "Duplicate",
	})

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "DUPLICATE_NODE_ID")
}

func TestValidate_InvalidEdgeSource(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes = append(wf.Nodes, workflow.Node{
		ID:    "target",
		Type:  workflow.NodeTypeTransform,
		Label: "Target",
	})
	wf.Edges = []workflow.Edge{
		{Source: "nonexistent", Target: "target"},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_EDGE_SOURCE")
}

func TestValidate_InvalidEdgeTarget(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Edges = []workflow.Edge{
		{Source: "entry", Target: "nonexistent"},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_EDGE_TARGET")
}

func TestValidate_ConditionalMissingEdgeForOutput(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "cond-test",
		Nodes: []workflow.Node{
			{
				ID:         "cond",
				Type:       workflow.NodeTypeConditional,
				Label:      "Conditional",
				Entrypoint: true,
				Config: workflow.NodeConfig{
					Conditions: []workflow.Condition{
						{Field: "x", Operator: "eq", Value: 1, Output: "branch-a"},
						{Field: "x", Operator: "eq", Value: 2, Output: "branch-b"},
					},
					DefaultOutput: "branch-default",
				},
			},
			{ID: "a", Type: workflow.NodeTypeTransform, Label: "A"},
			{ID: "b", Type: workflow.NodeTypeTransform, Label: "B"},
			{ID: "d", Type: workflow.NodeTypeTransform, Label: "D"},
		},
		Edges: []workflow.Edge{
			// Only provide edge for branch-a, missing branch-b and branch-default
			{Source: "cond", Target: "a", Output: "branch-a"},
			// edges to make b and d reachable for the connectivity check
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_CONDITIONAL_EDGE")
}

func TestValidate_UnreachableNode(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "unreachable-test",
		Nodes: []workflow.Node{
			{
				ID:         "entry",
				Type:       workflow.NodeTypeTransform,
				Label:      "Entry",
				Entrypoint: true,
			},
			{
				ID:    "island",
				Type:  workflow.NodeTypeTransform,
				Label: "Island",
			},
		},
		Edges: []workflow.Edge{},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "UNREACHABLE_NODE")
}

func TestValidate_InvalidInputType(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Inputs = []workflow.InputField{
		{Name: "bad", Type: "map", Required: true},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_INPUT_TYPE")
}

func TestValidate_MissingTemplateFile(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"

	checker := &mockFileChecker{existing: map[string]bool{}}

	errs := workflow.Validate(wf, checker)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_FILE")
}

func TestValidate_TemplateFileExists(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"

	checker := &mockFileChecker{existing: map[string]bool{"prompt.hbs": true}}

	errs := workflow.Validate(wf, checker)
	assert.Empty(t, errs)
}

func TestValidate_MissingScriptFile(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeScript
	wf.Nodes[0].Config.ScriptFile = "run.sh"

	checker := &mockFileChecker{existing: map[string]bool{}}

	errs := workflow.Validate(wf, checker)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_FILE")
}

func TestValidate_ScriptFileExists(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeScript
	wf.Nodes[0].Config.ScriptFile = "run.sh"

	checker := &mockFileChecker{existing: map[string]bool{"run.sh": true}}

	errs := workflow.Validate(wf, checker)
	assert.Empty(t, errs)
}

func TestValidate_ComplexValidWorkflow(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "complex-valid",
		Inputs: []workflow.InputField{
			{Name: "text", Type: "string", Required: true},
			{Name: "count", Type: "number", Required: false},
		},
		Nodes: []workflow.Node{
			{
				ID:         "start",
				Type:       workflow.NodeTypeTransform,
				Label:      "Start",
				Entrypoint: true,
			},
			{
				ID:    "cond",
				Type:  workflow.NodeTypeConditional,
				Label: "Branch",
				Config: workflow.NodeConfig{
					Conditions: []workflow.Condition{
						{Field: "x", Operator: "eq", Value: 1, Output: "left"},
					},
					DefaultOutput: "right",
				},
			},
			{
				ID:    "left-node",
				Type:  workflow.NodeTypeLLMCall,
				Label: "Left",
				Config: workflow.NodeConfig{
					TemplateFile: "left.hbs",
					Model:        "claude-3-haiku",
				},
			},
			{
				ID:    "right-node",
				Type:  workflow.NodeTypeScript,
				Label: "Right",
				Config: workflow.NodeConfig{
					ScriptFile: "run.sh",
				},
			},
			{
				ID:    "merge",
				Type:  workflow.NodeTypeTransform,
				Label: "Merge",
			},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "cond"},
			{Source: "cond", Target: "left-node", Output: "left"},
			{Source: "cond", Target: "right-node", Output: "right"},
			{Source: "left-node", Target: "merge"},
			{Source: "right-node", Target: "merge"},
		},
	}

	checker := &mockFileChecker{existing: map[string]bool{
		"left.hbs": true,
		"run.sh":   true,
	}}

	errs := workflow.Validate(wf, checker)
	assert.Empty(t, errs)
}

func TestValidate_FileCheckerReceivesWorkflowName(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Name = "my-workflow"
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"

	var capturedName, capturedPath string
	checker := &captureFileChecker{captureFunc: func(name, path string) bool {
		capturedName = name
		capturedPath = path
		return true
	}}

	errs := workflow.Validate(wf, checker)
	assert.Empty(t, errs)
	assert.Equal(t, "my-workflow", capturedName, "workflowName must be forwarded to FileChecker")
	assert.Equal(t, "prompt.hbs", capturedPath)
}

// captureFileChecker records the arguments passed to FileExists.
type captureFileChecker struct {
	captureFunc func(workflowName, path string) bool
}

func (c *captureFileChecker) FileExists(workflowName, path string) bool {
	return c.captureFunc(workflowName, path)
}

// extractCodes returns just the Code field from a slice of ValidationErrors.
func extractCodes(errs []workflow.ValidationError) []string {
	codes := make([]string, len(errs))
	for i, e := range errs {
		codes[i] = e.Code
	}
	return codes
}
