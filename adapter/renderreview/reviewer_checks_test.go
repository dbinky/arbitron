package renderreview

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_VisualOnlyDefectsBlock(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(r *layout.RenderReport)
		criterion layout.Criterion
		nodes     []string
		edges     []int
	}{
		{"fonts not loaded", func(r *layout.RenderReport) { r.FontsReady = false }, layout.CriterionTextUnreadable, nil, nil},
		{"fallback font family", func(r *layout.RenderReport) { r.FontFamilyResolved = false }, layout.CriterionTextUnreadable, nil, nil},
		{"node label clipped", func(r *layout.RenderReport) { r.Nodes[1].Label.Clipped = true }, layout.CriterionTextClipped, []string{"b"}, nil},
		{"type caption clipped", func(r *layout.RenderReport) { r.Nodes[2].Type.Clipped = true }, layout.CriterionTextClipped, []string{"c"}, nil},
		{"node label occluded", func(r *layout.RenderReport) { r.Nodes[0].Label.Occluded = true }, layout.CriterionTextCollision, []string{"a"}, nil},
		{"node label too small at practical zoom", func(r *layout.RenderReport) { r.Nodes[0].Label.FontPx = 9 }, layout.CriterionTextUnreadable, []string{"a"}, nil},
		{"node label not laid out", func(r *layout.RenderReport) { r.Nodes[0].Label.FontPx = 0 }, layout.CriterionTextUnreadable, []string{"a"}, nil},
		{"low contrast caption", func(r *layout.RenderReport) { r.Nodes[1].Type.Contrast = 3.2 }, layout.CriterionTextUnreadable, []string{"b"}, nil},
		{"branch label clipped", func(r *layout.RenderReport) { r.Edges[0].Label.Clipped = true }, layout.CriterionTextClipped, []string{"a", "b"}, []int{0}},
		{"branch label low contrast", func(r *layout.RenderReport) { r.Edges[0].Label.Contrast = 2 }, layout.CriterionTextUnreadable, []string{"a", "b"}, []int{0}},
		{"branch label missing", func(r *layout.RenderReport) { r.Edges[0].Label = nil }, layout.CriterionIndistinctBranch, []string{"a", "b"}, []int{0}},
		{"branch label drawn away from its edge", func(r *layout.RenderReport) { r.Edges[0].Label.Box.X += 120 }, layout.CriterionIndistinctBranch, []string{"a", "b"}, []int{0}},
		{"label on an unlabelled edge", func(r *layout.RenderReport) {
			l := layout.RenderedText{Box: layout.Rect{X: 300, Y: 140, W: 30, H: 16}, FontPx: 11, Contrast: 9}
			r.Edges[2].Label = &l
		}, layout.CriterionRenderMismatch, []string{"a", "c"}, []int{2}},
		{"node box resized by the renderer", func(r *layout.RenderReport) { r.Nodes[2].Box.W = 160 }, layout.CriterionRenderMismatch, []string{"c"}, nil},
		{"rendered nodes overlap", func(r *layout.RenderReport) { r.Nodes[2].Box.X = 200 }, layout.CriterionNodeOverlap, []string{"b", "c"}, nil},
		{"missing arrowhead", func(r *layout.RenderReport) { r.Edges[2].HasArrow = false }, layout.CriterionArrowDirection, []string{"a", "c"}, []int{2}},
		{"edge starts away from its route", func(r *layout.RenderReport) { r.Edges[2].Start.X += 30 }, layout.CriterionAmbiguousAttachment, []string{"a", "c"}, []int{2}},
		{"edge ends inside its target", func(r *layout.RenderReport) {
			r.Edges[0].End.Y += 20
		}, layout.CriterionArrowDirection, []string{"a", "b"}, []int{0}},
		{"loop return drawn solid", func(r *layout.RenderReport) { r.Edges[1].Dashed = false }, layout.CriterionIndistinctLoop, []string{"b", "a"}, []int{1}},
		{"forward edge drawn dashed", func(r *layout.RenderReport) { r.Edges[2].Dashed = true }, layout.CriterionIndistinctLoop, []string{"a", "c"}, []int{2}},
		{"hairline stroke", func(r *layout.RenderReport) { r.Edges[2].StrokePx = 1 }, layout.CriterionRenderMismatch, []string{"a", "c"}, []int{2}},
		{"crossing reads as a junction", func(r *layout.RenderReport) { r.Crossings[0].UnderGapVisible = false }, layout.CriterionAmbiguousCrossing, nil, []int{2, 1}},
		{"over edge interrupted", func(r *layout.RenderReport) { r.Crossings[0].OverContinuous = false }, layout.CriterionAmbiguousCrossing, nil, []int{2, 1}},
		{"running glow moves endpoints", func(r *layout.RenderReport) {
			r.States[1] = layout.StateProbe{State: "running", NodeBoxesStable: false, LabelsReadable: true, Issues: []string{"node b grew 4px"}}
		}, layout.CriterionRenderMismatch, []string{"b"}, nil},
		{"selection hides labels", func(r *layout.RenderReport) {
			r.States[6] = layout.StateProbe{State: "selected", NodeBoxesStable: true, LabelsReadable: false, Issues: []string{"node a label contrast 2.10 < 4.5"}}
		}, layout.CriterionTextUnreadable, []string{"a"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			tc.mutate(&fx.req.Rendering.Report)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			// Readability findings may set Rendered=false when required captured features are absent.
			assert.NotZero(t, layout.BlockingCount(rv.Findings))
			got := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, got, "want a %s finding, got %+v", tc.criterion, rv.Findings)
			assert.Equal(t, layout.SeverityBlocking, got[0].Severity)
			assert.Equal(t, layout.SourceReview, got[0].Source)
			assert.NotEmpty(t, got[0].Evidence)
			assert.Equal(t, tc.nodes, got[0].NodeIDs)
			assert.Equal(t, tc.edges, got[0].EdgeOrdinals)
		})
	}
}

func TestReadabilityReviewer_ReadableBoundaryIsInclusive(t *testing.T) {
	fx := newReviewFixture(t)
	r := &fx.req.Rendering.Report
	r.Nodes[0].Type.FontPx = 10 // 10 × 0.9 = 9px: exactly the minimum
	r.Nodes[0].Type.Contrast = 4.5
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
}

func TestReadabilityReviewer_MergedArrivalsConcealDestinations(t *testing.T) {
	geo := reviewGeometry()
	// Edge 2 now also enters b, at exactly the point edge 0 uses.
	geo.Edges[2].Target = "b"
	geo.Edges[2].Points = []layout.Point{{X: 200, Y: 100}, {X: 200, Y: 150}, {X: 140, Y: 150}, {X: 140, Y: 200}}
	geo.Edges[2].TargetAttach = layout.Attachment{NodeID: "b", ElementID: layout.GroupElementID("b", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 140, Y: 200}}
	geo.Crossings = nil
	fx := newReviewFixtureFor(t, geo)

	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	got := withCriterion(rv.Findings, layout.CriterionAmbiguousAttachment)
	require.Len(t, got, 1, "%+v", rv.Findings)
	assert.Equal(t, []string{"b"}, got[0].NodeIDs)
	assert.Equal(t, []int{0, 2}, got[0].EdgeOrdinals)
	assert.Empty(t, withCriterion(rv.Findings, layout.CriterionCrossings), "no crossings, no advisory finding")
}

func TestReadabilityReviewer_ParallelEdgesWithOwnPortsAreFine(t *testing.T) {
	geo := reviewGeometry()
	// Two identical parallel edges a→b, each with its own port: traceable.
	dup := geo.Edges[0]
	dup.Ordinal = 3
	dup.Output = ""
	dup.Text = nil
	dup.Points = []layout.Point{{X: 110, Y: 100}, {X: 110, Y: 200}}
	dup.SourceAttach.Point = layout.Point{X: 110, Y: 100}
	dup.TargetAttach.Point = layout.Point{X: 110, Y: 200}
	geo.Edges = append(geo.Edges, dup)
	fx := newReviewFixtureFor(t, geo)

	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
}

func TestReadabilityReviewer_FindingsAreDeterministic(t *testing.T) {
	fx := newReviewFixture(t)
	r := &fx.req.Rendering.Report
	r.Nodes[1].Label.Clipped = true
	r.Edges[1].Dashed = false
	r.Crossings[0].UnderGapVisible = false
	first, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	second, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	for _, f := range first.Findings {
		assert.Equal(t, layout.SourceReview, f.Source)
	}
}

// expectedSegments mirrors dashboard/src/graph/arranged/routedPath.ts: a dashed
// route restarts its dash (a same-point subpath) one dash before each gap and in
// dash steps across each crossing it passes over. Solid routes never restart.
func TestExpectedSegments_DashRestartsMirrorTheRenderer(t *testing.T) {
	type piece struct {
		from, to float64
		restart  bool
	}
	horizontal := func(kind layout.EdgeKind, crossings ...layout.Crossing) []piece {
		geo := layout.Geometry{Contract: layout.DefaultContract(), Crossings: crossings}
		e := layout.EdgeRoute{Ordinal: 1, Kind: kind, Points: []layout.Point{{X: 0, Y: 0}, {X: 100, Y: 0}}}
		var out []piece
		for _, s := range expectedSegments(geo, e) {
			require.Equal(t, s.Start.Y, s.End.Y)
			out = append(out, piece{s.Start.X, s.End.X, s.restart})
		}
		return out
	}
	over := func(x float64) layout.Crossing { return layout.Crossing{Point: layout.Point{X: x}, Over: 1, Under: 0} }
	under := func(x float64) layout.Crossing { return layout.Crossing{Point: layout.Point{X: x}, Over: 0, Under: 1} }

	assert.Equal(t, []piece{{0, 34, true}, {34, 40, true}, {40, 100, true}}, horizontal(layout.EdgeLoopReturn, over(40)), "over crossing")
	assert.Equal(t, []piece{{0, 29, true}, {29, 35, true}, {45, 100, true}}, horizontal(layout.EdgeLoopReturn, under(40)), "under gap banks")
	assert.Equal(t, []piece{{0, 29, true}, {29, 35, true}, {45, 48, true}, {58, 100, true}}, horizontal(layout.EdgeLoopReturn, under(40), under(53)), "restart inside a gap is skipped")
	assert.Equal(t, []piece{{0, 3, true}, {13, 100, true}}, horizontal(layout.EdgeLoopReturn, under(8)), "no restart at the path start")
	assert.Equal(t, []piece{{0, 100, true}}, horizontal(layout.EdgeForward, over(40)), "solid over")
	assert.Equal(t, []piece{{0, 35, true}, {45, 100, true}}, horizontal(layout.EdgeForward, under(40)), "solid under")

	// A bank restart before the segment start lands on the bend that starts it.
	geo := layout.Geometry{Contract: layout.DefaultContract(), Crossings: []layout.Crossing{under(8)}}
	geo.Crossings[0].Point.Y = 100
	e := layout.EdgeRoute{Ordinal: 1, Kind: layout.EdgeLoopReturn, Points: []layout.Point{{X: 0, Y: 0}, {X: 0, Y: 100}, {X: 100, Y: 100}}}
	var flags, corners []bool
	for _, s := range expectedSegments(geo, e) {
		flags = append(flags, s.restart)
		corners = append(corners, s.corner)
	}
	// The gap 3..13 past the bend leaves a 2px corner and a 1px bank, which
	// restarts its dash right after the corner.
	assert.Equal(t, []bool{true, false, true, true}, flags, "the corner continues segment 0; the bank and the far side restart")
	assert.Equal(t, []bool{false, true, false, false}, corners)
}

// Rounded bends mirror routedPath.ts:cornerRadii: each interior bend becomes a
// corner piece (the arc's chord) clamped to half of either segment and kept
// off every gap span and over-crossing ink span.
func TestExpectedSegments_RoundedCornersMirrorTheRenderer(t *testing.T) {
	type piece struct {
		a, b   layout.Point
		corner bool
	}
	pieces := func(points []layout.Point, kind layout.EdgeKind, crossings ...layout.Crossing) []piece {
		geo := layout.Geometry{Contract: layout.DefaultContract(), Crossings: crossings}
		var out []piece
		for _, s := range expectedSegments(geo, layout.EdgeRoute{Ordinal: 1, Kind: kind, Points: points}) {
			out = append(out, piece{s.Start, s.End, s.corner})
		}
		return out
	}
	p := func(x, y float64) layout.Point { return layout.Point{X: x, Y: y} }
	assert.Equal(t, []piece{{p(0, 0), p(0, 44), false}, {p(0, 44), p(6, 50), true}, {p(6, 50), p(34, 50), false}, {p(34, 50), p(40, 56), true}, {p(40, 56), p(40, 100), false}},
		pieces([]layout.Point{p(0, 0), p(0, 50), p(40, 50), p(40, 100)}, layout.EdgeForward), "full radius")
	assert.Equal(t, []piece{{p(0, 0), p(0, 47), false}, {p(0, 47), p(3, 50), true}, {p(3, 50), p(6, 53), true}, {p(6, 53), p(6, 100), false}},
		pieces([]layout.Point{p(0, 0), p(0, 50), p(6, 50), p(6, 100)}, layout.EdgeForward), "a short jog halves both corners")
	assert.Equal(t, []piece{{p(0, 0), p(39, 0), false}, {p(39, 0), p(40, 1), true}, {p(40, 1), p(40, 2), false}, {p(40, 12), p(40, 100), false}},
		pieces([]layout.Point{p(0, 0), p(40, 0), p(40, 100)}, layout.EdgeForward, layout.Crossing{Point: p(40, 7), Over: 0, Under: 1}), "gap 2..12 past the bend keeps a straight bank")
	assert.Equal(t, []piece{{p(0, 0), p(37, 0), false}, {p(37, 0), p(40, 3), true}, {p(40, 3), p(40, 100), false}},
		pieces([]layout.Point{p(0, 0), p(40, 0), p(40, 100)}, layout.EdgeForward, layout.Crossing{Point: p(31, 0), Over: 1, Under: 0}), "solid over ink stays straight")
	assert.Equal(t, []piece{{p(0, 0), p(0, 20), false}, {p(0, 20), p(0, 10), false}},
		pieces([]layout.Point{p(0, 0), p(0, 20), p(0, 10)}, layout.EdgeForward), "a reversal stays sharp")
	for _, s := range pieces([]layout.Point{p(0, 0), p(0, 100), p(100, 100)}, layout.EdgeForward, layout.Crossing{Point: p(0, 100), Over: 0, Under: 1}) {
		assert.False(t, s.corner, "a gap on the bend keeps it sharp")
	}
}

// The final corner leaves arrowPx of the last segment straight under the
// arrowhead, and a crossing within half a pixel of a segment counts as on it,
// exactly as routedPath.ts decides.
func TestExpectedSegments_CornerClampsMirrorTheRenderer(t *testing.T) {
	p := func(x, y float64) layout.Point { return layout.Point{X: x, Y: y} }
	geo := layout.Geometry{Contract: layout.DefaultContract()}
	var got []layout.RenderedSegment
	for _, s := range expectedSegments(geo, layout.EdgeRoute{Ordinal: 1, Kind: layout.EdgeForward, Points: []layout.Point{p(0, 0), p(30, 0), p(30, 30), p(16, 30)}}) {
		got = append(got, s.RenderedSegment)
	}
	assert.Equal(t, []layout.RenderedSegment{{Start: p(0, 0), End: p(24, 0)}, {Start: p(24, 0), End: p(30, 6)}, {Start: p(30, 6), End: p(30, 26)}, {Start: p(30, 26), End: p(26, 30)}, {Start: p(26, 30), End: p(16, 30)}}, got, "a 14px return keeps 10px straight for the arrow")

	geo.Crossings = []layout.Crossing{{Point: p(31, 0.4), Over: 1, Under: 0}}
	segs := expectedSegments(geo, layout.EdgeRoute{Ordinal: 1, Kind: layout.EdgeForward, Points: []layout.Point{p(0, 0), p(40, 0), p(40, 100)}})
	require.Len(t, segs, 3)
	assert.InDelta(t, 36.99, segs[0].End.X, .01, "an over crossing 0.4px off the segment still clamps the corner")
}

// A rendered corner must be flagged as one: a diagonal L chord with the same
// end points is not a rounded bend.
func TestSegmentsMatch_RequiresCornerFlags(t *testing.T) {
	p := func(x, y float64) layout.Point { return layout.Point{X: x, Y: y} }
	geo := layout.Geometry{Contract: layout.DefaultContract()}
	expected := expectedSegments(geo, layout.EdgeRoute{Ordinal: 1, Kind: layout.EdgeForward, Points: []layout.Point{p(0, 0), p(0, 50), p(40, 50)}})
	var rendered []layout.RenderedSegment
	for _, s := range expected {
		r := s.RenderedSegment
		r.Corner = s.corner
		rendered = append(rendered, r)
	}
	assert.True(t, segmentsMatch(rendered, expected))
	rendered[1].Corner = false
	assert.False(t, segmentsMatch(rendered, expected), "an unflagged chord")
	rendered[1].Corner = true
	rendered[0].Corner = true
	assert.False(t, segmentsMatch(rendered, expected), "a straight piece flagged as a corner")
	assert.False(t, segmentsMatch(rendered[:2], expected), "a missing piece")
}
