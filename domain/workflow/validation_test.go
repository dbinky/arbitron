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

func TestValidate_EmptyNodeID(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].ID = ""

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "EMPTY_NODE_ID")
}

func TestValidate_EmptyInputName(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Inputs = []workflow.InputField{{Name: "", Type: "string"}}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "EMPTY_INPUT_NAME")
}

func TestValidate_LLMNodeMissingTemplateFile(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeLLMCall

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_REQUIRED_CONFIG")
}

func TestValidate_LLMNodeWithTemplateFileIsValid(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeLLMCall
	wf.Nodes[0].Config.TemplateFile = "prompt.hbs"

	errs := workflow.Validate(wf, nil)
	assert.NotContains(t, extractCodes(errs), "MISSING_REQUIRED_CONFIG")
}

func TestValidate_ScriptNodeMissingScriptFile(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeScript

	errs := workflow.Validate(wf, nil)
	assert.Contains(t, extractCodes(errs), "MISSING_REQUIRED_CONFIG")
}

func TestValidate_ToolCallNodeMissingCommand(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeToolCall

	errs := workflow.Validate(wf, nil)
	assert.Contains(t, extractCodes(errs), "MISSING_REQUIRED_CONFIG")
}

func TestValidate_ConditionalNodeNoConditionsNoDefault(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeConditional
	wf.Nodes[0].Config.Conditions = nil
	wf.Nodes[0].Config.DefaultOutput = ""

	errs := workflow.Validate(wf, nil)
	assert.Contains(t, extractCodes(errs), "MISSING_REQUIRED_CONFIG")
}

func TestValidate_ConditionalNodeWithDefaultOnlyIsValid(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].Type = workflow.NodeTypeConditional
	wf.Nodes[0].Config.Conditions = nil
	wf.Nodes[0].Config.DefaultOutput = "next"
	// Add matching edge so MISSING_CONDITIONAL_EDGE doesn't fire.
	wf.Edges = []workflow.Edge{{Source: wf.Nodes[0].ID, Target: wf.Nodes[0].ID, Output: "next"}}

	errs := workflow.Validate(wf, nil)
	assert.NotContains(t, extractCodes(errs), "MISSING_REQUIRED_CONFIG")
}

func TestValidate_InvalidErrorBehaviorType(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.DefaultErrorBehavior = workflow.ErrorBehavior{Type: "explode"}

	errs := workflow.Validate(wf, nil)
	assert.Contains(t, extractCodes(errs), "INVALID_ERROR_BEHAVIOR")
}

func TestValidate_NodeInvalidErrorBehaviorType(t *testing.T) {
	wf := minimalValidWorkflow()
	wf.Nodes[0].ErrorBehavior = workflow.ErrorBehavior{Type: "Halt"}

	errs := workflow.Validate(wf, nil)
	assert.Contains(t, extractCodes(errs), "INVALID_ERROR_BEHAVIOR")
}

func TestValidate_ValidErrorBehaviorTypes(t *testing.T) {
	for _, typ := range []string{"", "halt", "retry", "skip", "route"} {
		wf := minimalValidWorkflow()
		wf.DefaultErrorBehavior = workflow.ErrorBehavior{Type: typ}
		errs := workflow.Validate(wf, nil)
		assert.NotContains(t, extractCodes(errs), "INVALID_ERROR_BEHAVIOR", "type=%q should be valid", typ)
	}
}

// extractCodes returns just the Code field from a slice of ValidationErrors.
func extractCodes(errs []workflow.ValidationError) []string {
	codes := make([]string, len(errs))
	for i, e := range errs {
		codes[i] = e.Code
	}
	return codes
}

// ---------------------------------------------------------------------------
// Tests: Timer Delay node validation
// ---------------------------------------------------------------------------

func TestValidateTimerDelay_Valid(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-valid",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "30"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "timer"},
			{Source: "timer", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateTimerDelay_MissingDurationSeconds(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-no-duration",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "timer"},
			{Source: "timer", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_REQUIRED_CONFIG")
}

func TestValidateTimerDelay_NoOutgoingEdge(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-no-edge",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "10"}},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "timer"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_TIMER_DELAY")
}

func TestValidateTimerDelay_MultipleOutgoingEdges(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-multi-edge",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "10"}},
			{ID: "a", Type: workflow.NodeTypeTransform},
			{ID: "b", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "timer"},
			{Source: "timer", Target: "a"},
			{Source: "timer", Target: "b"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_TIMER_DELAY")
}

func TestValidateTimerDelay_AsEntrypoint(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-entry",
		Nodes: []workflow.Node{
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Entrypoint: true, Config: workflow.NodeConfig{DurationSeconds: "10"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "timer", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_TIMER_DELAY")
}

func TestValidateTimerDelay_WithSubstitution(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "timer-subst",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "timer", Type: workflow.NodeTypeTimerDelay, Config: workflow.NodeConfig{DurationSeconds: "{{delay}}"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "timer"},
			{Source: "timer", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

// ---------------------------------------------------------------------------
// Tests: Webhook Wait node validation
// ---------------------------------------------------------------------------

func TestValidateWebhookWait_Valid_NoConfig(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-valid-noconfig",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateWebhookWait_Valid_WithSecret(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-valid-secret",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait, Config: workflow.NodeConfig{Secret: "my-secret"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateWebhookWait_Valid_WithTimeout(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-valid-timeout",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait, Config: workflow.NodeConfig{WaitTimeoutSeconds: "60"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateWebhookWait_Valid_WithBoth(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-valid-both",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait, Config: workflow.NodeConfig{Secret: "s", WaitTimeoutSeconds: "30"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateWebhookWait_NoOutgoingEdge(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-no-edge",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_WEBHOOK_WAIT")
}

func TestValidateWebhookWait_MultipleOutgoingEdges(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-multi-edge",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait},
			{ID: "a", Type: workflow.NodeTypeTransform},
			{ID: "b", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "a"},
			{Source: "webhook", Target: "b"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_WEBHOOK_WAIT")
}

func TestValidateWebhookWait_AsEntrypoint(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-entry",
		Nodes: []workflow.Node{
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait, Entrypoint: true},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "INVALID_WEBHOOK_WAIT")
}

func TestValidateWebhookWait_TimeoutWithSubstitution(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "webhook-subst",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "webhook", Type: workflow.NodeTypeWebhookWait, Config: workflow.NodeConfig{WaitTimeoutSeconds: "{{timeout}}"}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "webhook"},
			{Source: "webhook", Target: "next"},
		},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

// ---------------------------------------------------------------------------
// Tests: Interactive node validation
// ---------------------------------------------------------------------------

func TestValidateInteractive_Valid(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "interactive-valid",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "interact", Type: workflow.NodeTypeInteractive, Config: workflow.NodeConfig{
				SystemPrompt:      "You are a helpful assistant",
				OrchestrationFile: "orchestrate.yaml",
				TemplateFile:      "prompt.hbs",
				Model:             "claude-3-sonnet",
			}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "interact"},
			{Source: "interact", Target: "next"},
		},
	}

	checker := &mockFileChecker{existing: map[string]bool{
		"prompt.hbs":       true,
		"orchestrate.yaml": true,
	}}

	errs := workflow.Validate(wf, checker)
	assert.Empty(t, errs)
}

func TestValidateInteractive_AcceptsNodeType(t *testing.T) {
	// The interactive node type should be accepted by the workflow parser
	// (i.e., used in a single-node workflow without validation errors).
	wf := &workflow.Workflow{
		Name: "interactive-basic",
		Nodes: []workflow.Node{
			{
				ID:         "entry",
				Type:       workflow.NodeTypeInteractive,
				Label:      "Interactive",
				Entrypoint: true,
			},
		},
		Edges: []workflow.Edge{},
	}

	errs := workflow.Validate(wf, nil)
	assert.Empty(t, errs)
}

func TestValidateInteractive_MissingOrchestrationFile(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "interactive-missing-orch",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "interact", Type: workflow.NodeTypeInteractive, Config: workflow.NodeConfig{
				OrchestrationFile: "nonexistent.md",
			}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "interact"},
			{Source: "interact", Target: "next"},
		},
	}

	checker := &mockFileChecker{existing: map[string]bool{}}

	errs := workflow.Validate(wf, checker)
	codes := extractCodes(errs)
	assert.Contains(t, codes, "MISSING_FILE")
}

func TestValidateInteractive_OrchestrationFileSkippedWithNilChecker(t *testing.T) {
	wf := &workflow.Workflow{
		Name: "interactive-nil-checker",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true},
			{ID: "interact", Type: workflow.NodeTypeInteractive, Config: workflow.NodeConfig{
				OrchestrationFile: "nonexistent.md",
			}},
			{ID: "next", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "entry", Target: "interact"},
			{Source: "interact", Target: "next"},
		},
	}

	// With nil checker, file references should not be validated.
	errs := workflow.Validate(wf, nil)
	assert.NotContains(t, extractCodes(errs), "MISSING_FILE")
}
