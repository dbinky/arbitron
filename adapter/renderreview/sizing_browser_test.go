package renderreview

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSizing_RealBrowserBranchClippingRefinesSharedTextSlack(t *testing.T) {
	ctx := context.Background()
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	wf := &workflow.Workflow{
		Name: "branch-sizing",
		Nodes: []workflow.Node{
			{ID: "a", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: "Start"},
			{ID: "b", Type: workflow.NodeTypeTransform, Label: "Finish"},
		},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Label: "Überprüfung\n" + strings.Repeat("審查", 20)}},
	}
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c, options := layout.DefaultContract(), layout.DefaultOptions()
	metrics, err := b.Measure(ctx, layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	key := layout.EdgeLabelKey(0)
	actual := metrics.Items[key]
	require.Greater(t, actual.Lines, 2, "the branch must actually wrap in the browser")
	// Deliberately understate only branch height. Preflight sees internally
	// consistent metrics; only the real DOM/glyph capture can expose clipping.
	lying := metrics
	lying.Items = make(map[string]layout.TextMetrics, len(metrics.Items))
	for k, v := range metrics.Items {
		lying.Items[k] = v
	}
	understated := actual
	understated.Lines = 1
	understated.Height = c.EdgeLabelFontPx * c.LineHeight
	lying.Items[key] = understated
	geo, err := layout.Arrange(context.Background(), g, lying, c, options)
	require.NoError(t, err)
	assert.Empty(t, withCriterion(layout.Preflight(g, geo, lying), layout.CriterionTextClipped))
	rend, err := b.Render(ctx, layout.NewRenderRequest("branch-sizing", 1, geo, map[string]string{"a": "transform", "b": "transform"}))
	require.NoError(t, err)
	review, err := NewReadabilityReviewer(nil).Review(ctx, layout.ReviewRequest{Revision: "branch-sizing", Attempt: 1, Geometry: geo, Rendering: rend})
	require.NoError(t, err)
	clipped := withCriterion(review.Findings, layout.CriterionTextClipped)
	require.NotEmpty(t, clipped, "actual clipped branch must reach reviewer: %+v", review.Findings)
	assert.Contains(t, clipped[0].EdgeOrdinals, 0)
	refined, changed := layout.Refine(options, clipped)
	require.True(t, changed)
	require.Greater(t, refined.NodeTextSlack, options.NodeTextSlack)
	grown, err := layout.Arrange(context.Background(), g, lying, c, refined)
	require.NoError(t, err)
	slack := refined.NodeTextSlack - options.NodeTextSlack
	assert.Equal(t, geo.Nodes[0].Box.H+slack, grown.Nodes[0].Box.H)
	require.NotNil(t, grown.Edges[0].Text)
	assert.Equal(t, geo.Edges[0].Text.Box.W+slack, grown.Edges[0].Text.Box.W)
	assert.Equal(t, geo.Edges[0].Text.Box.H+slack, grown.Edges[0].Text.Box.H)
	assert.Equal(t, actual, metrics.Items[key], "injected failure must not change real measurements")
}
