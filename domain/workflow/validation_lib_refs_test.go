package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func libRefWorkflow(cfg workflow.NodeConfig) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "test",
		Nodes: []workflow.Node{
			{ID: "entry", Type: workflow.NodeTypeScript, Entrypoint: true, Config: cfg},
		},
	}
}

func TestValidateFileRefs_LibPathPresent_NoFinding(t *testing.T) {
	wf := libRefWorkflow(workflow.NodeConfig{ScriptFile: "@lib/x.md"})
	errs := workflow.Validate(wf, &mockFileChecker{existing: map[string]bool{"@lib/x.md": true}}, nil, nil)
	assert.NotContains(t, codesOf(errs), "MISSING_FILE")
	assert.NotContains(t, codesOf(errs), "INVALID_LIB_PATH")
}

func TestValidateFileRefs_LibPathMissing_MissingFile(t *testing.T) {
	wf := libRefWorkflow(workflow.NodeConfig{ScriptFile: "@lib/x.md"})
	errs := workflow.Validate(wf, &mockFileChecker{existing: map[string]bool{}}, nil, nil)
	assert.Contains(t, codesOf(errs), "MISSING_FILE")
	assert.NotContains(t, codesOf(errs), "INVALID_LIB_PATH")
}

func TestValidateFileRefs_MalformedLibPath_InvalidLibPath(t *testing.T) {
	// The checker answers true for everything: a malformed lib path must still
	// be rejected before the checker is consulted.
	all := &mockFileChecker{existing: map[string]bool{"@lib/../x": true, "@lib/": true, "@lib/workflow.json": true}}
	cases := map[string]workflow.NodeConfig{
		"templateFile":      {TemplateFile: "@lib/../x"},
		"scriptFile":        {ScriptFile: "@lib/"},
		"orchestrationFile": {OrchestrationFile: "@lib/workflow.json"},
	}
	for field, cfg := range cases {
		t.Run(field, func(t *testing.T) {
			errs := workflow.Validate(libRefWorkflow(cfg), all, nil, nil)
			var got *workflow.ValidationError
			for i := range errs {
				if errs[i].Code == "INVALID_LIB_PATH" {
					got = &errs[i]
				}
			}
			require.NotNil(t, got, "codes: %v", codesOf(errs))
			assert.Equal(t, "entry", got.NodeID)
			assert.Contains(t, got.Message, "node entry references invalid library path "+field+": ")
			assert.NotContains(t, codesOf(errs), "MISSING_FILE")
		})
	}
}

func TestValidateFileRefs_CaseVariantPrefixIsWorkflowFile(t *testing.T) {
	wf := libRefWorkflow(workflow.NodeConfig{ScriptFile: "@LIB/x"})
	errs := workflow.Validate(wf, &mockFileChecker{existing: map[string]bool{}}, nil, nil)
	assert.Contains(t, codesOf(errs), "MISSING_FILE")
	assert.NotContains(t, codesOf(errs), "INVALID_LIB_PATH")

	errs = workflow.Validate(wf, &mockFileChecker{existing: map[string]bool{"@LIB/x": true}}, nil, nil)
	assert.NotContains(t, codesOf(errs), "MISSING_FILE")
}

func TestValidateGroup_ResolverPromptMalformedLibPath(t *testing.T) {
	wf := grpValidBranches()
	g := wf.Nodes[groupNodeIndex(t, wf)].Config.Group
	g.GatherMode = workflow.GroupGatherResultsAndFiles
	g.UnfinishedBehavior = workflow.GroupUnfinishedCancel
	g.ResolverProfile = "agenty"
	g.ResolverPromptFile = "@lib/../escape.md"
	errs := workflow.Validate(wf, grpFiles{}, grpProfiles{"agenty": "agent"}, nil)
	assert.Contains(t, codesOf(errs), "INVALID_LIB_PATH")
	assert.NotContains(t, codesOf(errs), "GROUP_MISSING_PROMPT_FILE")

	// A well-formed but missing lib prompt keeps the group missing-file behavior.
	g.ResolverPromptFile = "@lib/prompt.md"
	errs = workflow.Validate(wf, grpFiles{}, grpProfiles{"agenty": "agent"}, nil)
	assert.Contains(t, codesOf(errs), "GROUP_MISSING_PROMPT_FILE")
	assert.NotContains(t, codesOf(errs), "INVALID_LIB_PATH")
}

func TestIsGroupValidationCode_FalseForInvalidLibPath(t *testing.T) {
	assert.False(t, workflow.IsGroupValidationCode("INVALID_LIB_PATH"))
}

func groupNodeIndex(t *testing.T, wf *workflow.Workflow) int {
	t.Helper()
	for i, n := range wf.Nodes {
		if n.Type == workflow.NodeTypeParallelGroup {
			return i
		}
	}
	t.Fatal("no parallel_group node")
	return -1
}
