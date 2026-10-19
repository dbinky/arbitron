package engine

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gsGroupWorkflow(name string, modes ...string) *workflow.Workflow {
	wf := &workflow.Workflow{Name: name, Nodes: []workflow.Node{{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true}}}
	for i, m := range modes {
		id := []string{"g1", "g2", "g3"}[i]
		var cfg *workflow.GroupConfig
		if m != "<nil>" {
			cfg = &workflow.GroupConfig{Form: workflow.GroupFormBranches, GatherMode: m}
		}
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{Group: cfg}})
	}
	return wf
}

func TestCheckGroupSupport_Matrix(t *testing.T) {
	results := gsGroupWorkflow("r", workflow.GroupGatherResults)
	files := gsGroupWorkflow("f", workflow.GroupGatherResultsAndFiles)
	unset := gsGroupWorkflow("u", "<nil>")
	plain := &workflow.Workflow{Name: "p", Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Entrypoint: true}}}

	cases := []struct {
		name  string
		wf    *workflow.Workflow
		level GroupSupport
		ok    bool
	}{
		{"plain/none", plain, GroupSupportNone, true},
		{"plain/results", plain, GroupSupportResults, true},
		{"plain/files", plain, GroupSupportResultsAndFiles, true},
		{"results/none", results, GroupSupportNone, false},
		{"results/results", results, GroupSupportResults, true},
		{"results/files", results, GroupSupportResultsAndFiles, true},
		{"files/none", files, GroupSupportNone, false},
		{"files/results", files, GroupSupportResults, false},
		{"files/files", files, GroupSupportResultsAndFiles, true},
		{"unset-config/none", unset, GroupSupportNone, false},
		{"unset-config/results", unset, GroupSupportResults, false},
		{"unset-config/files", unset, GroupSupportResultsAndFiles, true},
		{"results/unknown-level", results, GroupSupport(-1), false},
		{"nil workflow", nil, GroupSupportNone, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkGroupSupport(tc.wf, tc.level)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrGroupExecutionUnsupported))
		})
	}
}

func TestCheckGroupSupport_NoneMessageIsActionable(t *testing.T) {
	err := checkGroupSupport(gsGroupWorkflow("review", workflow.GroupGatherResults), GroupSupportNone)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "parallel_group execution is not available on this server")
	assert.Contains(t, msg, `workflow "review"`)
	assert.Contains(t, msg, `node "g1"`)
	assert.Contains(t, msg, `gatherMode "results"`)
	assert.Contains(t, msg, "this server cannot execute parallel_group yet")
	assert.Contains(t, msg, "workflow_validate", "tells the author what still works")
}

func TestCheckGroupSupport_ResultsLevelNamesTheFilesNode(t *testing.T) {
	// A results group first, then a files group: the error must name the
	// files group (g2), never silently run it as results-only.
	err := checkGroupSupport(gsGroupWorkflow("mixed", workflow.GroupGatherResults, workflow.GroupGatherResultsAndFiles), GroupSupportResults)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `node "g2"`)
	assert.NotContains(t, err.Error(), `node "g1"`)
	assert.Contains(t, err.Error(), "file gathering is not available yet")
	assert.Contains(t, err.Error(), `set gatherMode to "results"`)
}

func TestCheckGroupSupport_ResultsLevelRejectsUnsetGatherMode(t *testing.T) {
	err := checkGroupSupport(gsGroupWorkflow("broken", "<nil>"), GroupSupportResults)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gatherMode <unset>")
	assert.Contains(t, err.Error(), "workflow_validate")
}

func TestGroupSupport_String(t *testing.T) {
	assert.Equal(t, "none", GroupSupportNone.String())
	assert.Equal(t, "results", GroupSupportResults.String())
	assert.Equal(t, "results_and_files", GroupSupportResultsAndFiles.String())
	assert.Equal(t, "GroupSupport(7)", GroupSupport(7).String())
}
