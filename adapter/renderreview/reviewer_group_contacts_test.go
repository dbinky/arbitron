package renderreview

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_GroupActualContactsBlock(t *testing.T) {
	for _, kind := range []string{"body outside actual lane", "supplement over branch glyphs", "connector through sibling card"} {
		t.Run(kind, func(t *testing.T) {
			scene := "branches"
			if kind == "connector through sibling card" {
				scene = "workflows"
			}
			fx := newGroupReviewFixture(t, scene)
			r := &fx.req.Rendering.Report
			criterion := layout.CriterionNodeOverlap
			switch kind {
			case "body outside actual lane":
				for i := range r.Nodes {
					if r.Nodes[i].ID == "a" {
						r.Nodes[i].Box.X = -1000
					}
				}
			case "supplement over branch glyphs":
				r.GroupElements[0].Texts[0].Text.Glyphs = r.Edges[1].Label.Glyphs
				criterion = layout.CriterionTextCollision
			case "connector through sibling card":
				var card layout.Rect
				for i, e := range fx.geo.GroupElements {
					if e.Role == "task" {
						card = r.GroupElements[i].Box
					}
				}
				r.GroupConnectors[0].Segments = []layout.RenderedSegment{{Start: layout.Point{X: card.X - 5, Y: card.Y + card.H/2}, End: layout.Point{X: card.Right() + 5, Y: card.Y + card.H/2}}}
				criterion = layout.CriterionEdgeThroughNode
			}
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			fs := withCriterion(rv.Findings, criterion)
			require.NotEmpty(t, fs, "%+v", rv.Findings)
			require.Contains(t, fs[0].NodeIDs, "fan")
			requireFindingsAcceptedByCoordinator(t, fx.geo, rv.Findings)
		})
	}
}
