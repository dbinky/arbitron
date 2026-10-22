package renderreview

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_GroupInputCannotBeIgnored(t *testing.T) {
	for _, tc := range []struct {
		name             string
		groupNode, input bool
		reason           string
	}{
		{"missing map", true, false, "missing or mismatched group ownership"},
		{"extra map", false, true, "missing or mismatched group ownership"},
		{"v1 group", true, true, "reviewed group geometry is unavailable in v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			if tc.name == "v1 group" {
				fx.req.Geometry.Format = layout.FormatV1
			}
			if tc.groupNode {
				fx.req.Geometry.Nodes[0].TypeCaption = "parallel group"
				fx.req.Rendering.GeometryDigest = layout.GeometryDigest(fx.req.Geometry)
				fx.req.Rendering.Report.Nodes[0].Type.Content = "parallel group"
			}
			if tc.input {
				fx.req.WorkflowGroups = &workflow.GroupGraph{Order: []string{"a"}, Groups: map[string]*workflow.GroupScope{"a": {NodeID: "a"}}}
			}
			review, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			assert.False(t, review.Rendered)
			var evidence string
			for _, finding := range review.Findings {
				evidence += finding.Evidence + "\n"
			}
			assert.Contains(t, evidence, tc.reason)
			assert.Positive(t, layout.BlockingCount(review.Findings))
		})
	}
}
