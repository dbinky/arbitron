package renderreview

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestRenderJobDocument_ForwardsCapturedWorkflowGroups(t *testing.T) {
	groups := &workflow.GroupGraph{Order: []string{"a"}, Owner: map[string]workflow.BodyOwner{}, Groups: map[string]*workflow.GroupScope{
		"a": {NodeID: "a", Config: workflow.GroupConfig{Form: workflow.GroupFormWorkflows, Tasks: []workflow.GroupTask{{Key: "second ✅", Workflow: "同じ子"}, {Key: "first", Workflow: "同じ子"}}}},
	}}
	req := layout.NewRenderRequest("captured-group", 1, smallGeometry(), smallNodeTypes())
	req.WorkflowGroups = groups
	raw, err := renderJobDocument(req)
	require.NoError(t, err)
	var doc struct {
		WorkflowGroups *workflow.GroupGraph `json:"workflowGroups"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, groups, doc.WorkflowGroups, "the browser document must retain the complete captured map and author task order")

	req.WorkflowGroups = nil
	raw, err = renderJobDocument(req)
	require.NoError(t, err)
	var ordinary map[string]any
	require.NoError(t, json.Unmarshal(raw, &ordinary))
	require.NotContains(t, ordinary, "workflowGroups")
}
