package renderreview

import (
	"context"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestReadabilityReviewer_InvalidCandidateFailsClosed(t *testing.T) {
	for _, mutate := range []func(*layout.Geometry){
		func(g *layout.Geometry) { g.Nodes = nil },
		func(g *layout.Geometry) { g.Nodes[1].ID = g.Nodes[0].ID },
		func(g *layout.Geometry) { g.Edges[0].Points = nil },
		func(g *layout.Geometry) { g.Crossings[0].Under = 99 },
		func(g *layout.Geometry) { g.Contract = layout.RendererContract{} },
	} {
		fx := newReviewFixture(t)
		mutate(&fx.req.Geometry)
		fx.req.Rendering.GeometryDigest = layout.GeometryDigest(fx.req.Geometry)
		rv, err := testReviewer().Review(context.Background(), fx.req)
		require.NoError(t, err)
		require.False(t, rv.Rendered)
		require.NotZero(t, layout.BlockingCount(rv.Findings))
	}
}

func TestReadabilityReviewer_FullRenderedPath(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*layout.RenderReport)
		criterion layout.Criterion
	}{
		{"wrong middle same endpoints", func(r *layout.RenderReport) { r.Edges[2].Segments[1].Start.X += 20 }, layout.CriterionRenderMismatch},
		{"empty path", func(r *layout.RenderReport) { r.Edges[2].Segments = nil }, layout.CriterionRenderMismatch},
		{"hidden path", func(r *layout.RenderReport) { r.Edges[2].Visible = false }, layout.CriterionRenderMismatch},
		{"wrong color", func(r *layout.RenderReport) { r.Edges[2].StrokeColor = "rgb(0, 0, 0)" }, layout.CriterionRenderMismatch},
		{"missing dash evidence", func(r *layout.RenderReport) { r.Edges[0].DashPattern = "" }, layout.CriterionIndistinctLoop},
		{"wrong dash", func(r *layout.RenderReport) { r.Edges[1].DashPattern = "1px, 20px" }, layout.CriterionIndistinctLoop},
		{"reversed arrow", func(r *layout.RenderReport) { r.Edges[2].ArrowPoints[0].Y += 20; r.Edges[2].ArrowPoints[2].Y += 20 }, layout.CriterionArrowDirection},
		{"empty arrow", func(r *layout.RenderReport) { r.Edges[2].ArrowPoints = nil }, layout.CriterionArrowDirection},
		{"wrong text", func(r *layout.RenderReport) { r.Nodes[0].Label.Content = "Wrong" }, layout.CriterionRenderMismatch},
		{"empty text", func(r *layout.RenderReport) { r.Nodes[0].Label.Content = "" }, layout.CriterionTextUnreadable},
		{"empty glyphs", func(r *layout.RenderReport) { r.Nodes[0].Label.Glyphs = nil }, layout.CriterionTextUnreadable},
		{"hidden text", func(r *layout.RenderReport) { r.Nodes[0].Label.Visible = false }, layout.CriterionTextUnreadable},
		{"glyph outside", func(r *layout.RenderReport) { r.Nodes[0].Label.Glyphs[0].X += 200 }, layout.CriterionTextClipped},
		{"text from different nodes collides", func(r *layout.RenderReport) { r.Nodes[0].Label.Glyphs = r.Nodes[1].Label.Glyphs }, layout.CriterionTextCollision},
		{"routes merge in middle", func(r *layout.RenderReport) { r.Edges[2].Segments[1] = r.Edges[0].Segments[0] }, layout.CriterionMergedLines},
		{"text collision", func(r *layout.RenderReport) { r.Nodes[0].Label.Glyphs = r.Nodes[0].Type.Glyphs }, layout.CriterionTextCollision},
		{"route through unrelated node", func(r *layout.RenderReport) {
			r.Edges[2].Segments[0] = layout.RenderedSegment{Start: layout.Point{X: 100, Y: 180}, End: layout.Point{X: 100, Y: 280}}
		}, layout.CriterionEdgeThroughNode},
		{"branch over its route", func(r *layout.RenderReport) { r.Edges[0].Label.Box.X = 130 }, layout.CriterionTextCollision},
		{"branch collides with node text", func(r *layout.RenderReport) { r.Edges[0].Label.Glyphs = r.Nodes[0].Label.Glyphs }, layout.CriterionTextCollision},
		{"wrong branch text", func(r *layout.RenderReport) { r.Edges[0].Label.Content = "no" }, layout.CriterionRenderMismatch},
		{"wrong crossing", func(r *layout.RenderReport) { r.Crossings[0].Point.X += 20 }, layout.CriterionRenderMismatch},
		{"duplicate crossing", func(r *layout.RenderReport) { r.Crossings = append(r.Crossings, r.Crossings[0]) }, layout.CriterionCoverage},
		{"duplicate state", func(r *layout.RenderReport) { r.States = append(r.States, r.States[0]) }, layout.CriterionCoverage},
		{"unknown state", func(r *layout.RenderReport) { r.States[0].State = "invented" }, layout.CriterionCoverage},
		{"state issues", func(r *layout.RenderReport) {
			r.States[0].Issues = []string{"running+selected midpoint obscures arrow 2"}
		}, layout.CriterionRenderMismatch},
		{"nonfinite text", func(r *layout.RenderReport) { r.Nodes[0].Label.FontPx = math.NaN() }, layout.CriterionTextUnreadable},
		{"nonfinite box", func(r *layout.RenderReport) { r.Nodes[0].Box.X = math.Inf(1) }, layout.CriterionRenderMismatch},
		{"nonfinite segment", func(r *layout.RenderReport) { r.Edges[0].Segments[0].End.Y = math.NaN() }, layout.CriterionRenderMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			tc.mutate(&fx.req.Rendering.Report)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			require.NotEmpty(t, withCriterion(rv.Findings, tc.criterion), "%+v", rv.Findings)
		})
	}
}
