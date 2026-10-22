package layout

import (
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func blocking(c Criterion) Finding {
	return Finding{Criterion: c, Severity: SeverityBlocking, Source: SourceReview}
}

func TestRefine_TargetedIncrements(t *testing.T) {
	cases := []struct {
		criterion Criterion
		update    func(*Options)
	}{
		{CriterionNodeOverlap, func(o *Options) { o.NodeSpacing += 24 }},
		{CriterionEdgeThroughNode, func(o *Options) { o.NodeSpacing += 16; o.RankSpacing += 16 }},
		{CriterionTextCollision, func(o *Options) { o.LabelGap += 6; o.PortSpacing += 12; o.NodeSpacing += 16 }},
		{CriterionTextClipped, func(o *Options) { o.NodeTextSlack += 8 }},
		{CriterionMergedLines, func(o *Options) { o.LaneSeparation += 6; o.PortSpacing += 8 }},
		{CriterionAmbiguousAttachment, func(o *Options) { o.PortSpacing += 12 }},
		{CriterionAmbiguousCrossing, func(o *Options) { o.LaneSeparation += 6; o.PortSpacing += 8; o.CrossingSweeps *= 2 }},
		{CriterionIndistinctLoop, func(o *Options) { o.GutterWidth += 24; o.LaneSeparation += 4 }},
		{CriterionIndistinctBranch, func(o *Options) { o.PortSpacing += 12; o.LabelGap += 6 }},
	}
	for _, tc := range cases {
		t.Run(string(tc.criterion), func(t *testing.T) {
			base, want := DefaultOptions(), DefaultOptions()
			tc.update(&want)
			got, changed := Refine(base, []Finding{blocking(tc.criterion)})
			assert.True(t, changed)
			assert.Equal(t, want, got, "every mapped field and no unrelated fields change")
		})
	}
}

func TestRefine_UnfixableCriteriaProveInability(t *testing.T) {
	for _, c := range []Criterion{CriterionTextUnreadable, CriterionUpwardProgression, CriterionCoverage, CriterionArrowDirection, CriterionRenderMismatch, "unknown"} {
		got, changed := Refine(DefaultOptions(), []Finding{blocking(c)})
		assert.False(t, changed, "%s cannot be fixed by spacing", c)
		assert.Equal(t, DefaultOptions(), got)
	}
}

func TestRefine_AdvisoryCrossingsAloneDoNotRefine(t *testing.T) {
	advisory := Finding{Criterion: CriterionCrossings, Severity: SeverityAdvisory}
	for _, findings := range [][]Finding{nil, {}, {advisory}, {blocking(CriterionCrossings)}, {blocking(CriterionRenderMismatch), advisory}} {
		got, changed := Refine(DefaultOptions(), findings)
		assert.False(t, changed)
		assert.Equal(t, DefaultOptions(), got)
	}
	got, changed := Refine(DefaultOptions(), []Finding{blocking(CriterionNodeOverlap), advisory, advisory})
	assert.True(t, changed)
	assert.Equal(t, DefaultOptions().CrossingSweeps*2, got.CrossingSweeps)

	got, _ = Refine(DefaultOptions(), []Finding{blocking(CriterionAmbiguousCrossing), advisory})
	assert.Equal(t, DefaultOptions().CrossingSweeps*2, got.CrossingSweeps, "do not double twice")

	o := DefaultOptions()
	o.NodeSpacing = 400
	got, changed = Refine(o, []Finding{blocking(CriterionNodeOverlap), advisory})
	assert.False(t, changed, "a capped targeted field cannot authorize advisory search growth")
	assert.Equal(t, o, got)
}

func TestRefine_AppliesEachCriterionOnce(t *testing.T) {
	got, _ := Refine(DefaultOptions(), []Finding{blocking(CriterionNodeOverlap), blocking(CriterionNodeOverlap), blocking(CriterionNodeOverlap)})
	assert.Equal(t, DefaultOptions().NodeSpacing+24, got.NodeSpacing)
}

func TestRefine_AdvisoryVersionsOfBlockingCriteriaAreIgnored(t *testing.T) {
	for _, c := range refinementCriteria() {
		got, changed := Refine(DefaultOptions(), []Finding{{Criterion: c, Severity: SeverityAdvisory}, {Criterion: c, Severity: "unknown"}})
		assert.False(t, changed)
		assert.Equal(t, DefaultOptions(), got)
	}
}

func refinementCriteria() []Criterion {
	return []Criterion{CriterionNodeOverlap, CriterionEdgeThroughNode, CriterionTextCollision, CriterionTextClipped, CriterionMergedLines, CriterionAmbiguousAttachment, CriterionAmbiguousCrossing, CriterionIndistinctLoop, CriterionIndistinctBranch}
}

func allRefinementFindings() []Finding {
	var fs []Finding
	for _, c := range refinementCriteria() {
		fs = append(fs, blocking(c))
	}
	return fs
}

func TestRefine_CapsEventuallyStopRefinement(t *testing.T) {
	o := DefaultOptions()
	fs := allRefinementFindings()
	for steps := 0; ; steps++ {
		require.Less(t, steps, 100, "refinement must terminate")
		next, changed := Refine(o, fs)
		if !changed {
			break
		}
		o = next
	}
	want := Options{NodeSpacing: 400, RankSpacing: 400, LaneSeparation: 60, PortSpacing: 120, GutterWidth: 400, LabelGap: 60, NodeTextSlack: 64, CrossingSweeps: 64, DummyWidth: 24, Margin: 40}
	assert.Equal(t, want, o)
	got, changed := Refine(want, fs)
	assert.False(t, changed)
	assert.Equal(t, want, got)
}

func TestRefine_CapsNeverShrinkExplicitOptions(t *testing.T) {
	o := Options{NodeSpacing: 500, RankSpacing: 500, LaneSeparation: 70, PortSpacing: 130, GutterWidth: 500, LabelGap: 70, NodeTextSlack: 80, CrossingSweeps: math.MaxInt, DummyWidth: 900, Margin: 900}
	got, changed := Refine(o, allRefinementFindings())
	assert.False(t, changed)
	assert.Equal(t, o, got)

	o.NodeTextSlack = 63
	want := o
	want.NodeTextSlack = 64
	got, changed = Refine(o, allRefinementFindings())
	assert.True(t, changed)
	assert.Equal(t, want, got, "do not clamp unrelated custom values")
}

func TestRefine_SearchBudgetBoundaries(t *testing.T) {
	for _, old := range []int{math.MinInt, -1, 0, 1, 31, 32, 63, 64, 65, math.MaxInt} {
		o := DefaultOptions()
		o.CrossingSweeps = old
		want := old
		if old <= 0 {
			want = 1
		} else if old < 64 {
			want = min(64, old*2)
		}
		for _, findings := range [][]Finding{
			{blocking(CriterionAmbiguousCrossing)},
			{blocking(CriterionNodeOverlap), {Criterion: CriterionCrossings, Severity: SeverityAdvisory}},
		} {
			got, changed := Refine(o, findings)
			assert.True(t, changed)
			assert.Equal(t, want, got.CrossingSweeps, "old %d", old)
		}
	}
}

func TestRefine_MixedCriteriaSumIncrementsRegardlessOfFindingOrder(t *testing.T) {
	fs := allRefinementFindings()
	want := DefaultOptions()
	want.NodeSpacing += 24 + 16 + 16
	want.RankSpacing += 16
	want.LabelGap += 6 + 6
	want.PortSpacing += 12 + 8 + 12 + 8 + 12
	want.NodeTextSlack += 8
	want.LaneSeparation += 6 + 6 + 4
	want.GutterWidth += 24
	want.CrossingSweeps *= 2
	got, changed := Refine(DefaultOptions(), fs)
	assert.True(t, changed)
	assert.Equal(t, want, got)
	for i, j := 0, len(fs)-1; i < j; i, j = i+1, j-1 {
		fs[i], fs[j] = fs[j], fs[i]
	}
	reversed, _ := Refine(DefaultOptions(), fs)
	assert.Equal(t, got, reversed)
}

func TestRefine_DoesNotMutateInput(t *testing.T) {
	o := DefaultOptions()
	fs := []Finding{blocking(CriterionMergedLines)}
	before := append([]Finding(nil), fs...)
	_, _ = Refine(o, fs)
	assert.Equal(t, DefaultOptions(), o)
	assert.Equal(t, before, fs)
}

// These fixtures assert geometry sinks, not the echoed Geometry.Options field.
// Actual DOM clipping and the production feedback loop require P3T4–P3T7.
func TestRefine_FeedsArrangementGeometry(t *testing.T) {
	t.Run("node spacing", func(t *testing.T) {
		g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}}})
		require.NoError(t, err)
		o := DefaultOptions()
		next, _ := Refine(o, []Finding{blocking(CriterionNodeOverlap)})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		assert.InDelta(t, 24, (boxOf(after, "b").X-boxOf(after, "a").Right())-(boxOf(before, "b").X-boxOf(before, "a").Right()), geomEps)
	})
	t.Run("rank spacing", func(t *testing.T) {
		g := edgeGraph(t, [2]string{"a", "b"})
		o := DefaultOptions()
		next, _ := Refine(o, []Finding{blocking(CriterionEdgeThroughNode)})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		assert.InDelta(t, 16, (boxOf(after, "b").Y-boxOf(after, "a").Bottom())-(boxOf(before, "b").Y-boxOf(before, "a").Bottom()), geomEps)
	})
	t.Run("branch labels and ports", func(t *testing.T) {
		g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}}, Edges: []workflow.Edge{{Source: "a", Target: "b", Label: "批准 e\u0301 😀"}, {Source: "a", Target: "c", Output: "retry"}}})
		require.NoError(t, err)
		o := DefaultOptions()
		next, _ := Refine(o, []Finding{blocking(CriterionIndistinctBranch)})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		b, a := before.Edges[0], after.Edges[0]
		require.NotNil(t, a.Text)
		require.NotNil(t, b.Text)
		assert.InDelta(t, 6, (a.Text.Box.X-a.SourceAttach.Point.X)-(b.Text.Box.X-b.SourceAttach.Point.X), geomEps)
		assert.InDelta(t, 18, (after.Edges[1].SourceAttach.Point.X-a.SourceAttach.Point.X)-(before.Edges[1].SourceAttach.Point.X-b.SourceAttach.Point.X), geomEps, "12px port growth plus 6px label gap")
		assertLabelsClear(t, after)

		next, _ = Refine(o, []Finding{blocking(CriterionTextClipped)})
		after = arrangeFor(t, g, next)
		assert.InDelta(t, 8, after.Edges[0].Text.Box.W-b.Text.Box.W, geomEps)
		assert.InDelta(t, 8, after.Edges[0].Text.Box.H-b.Text.Box.H, geomEps)
		assert.InDelta(t, 8, boxOf(after, "a").H-boxOf(before, "a").H, geomEps)
		assertLabelsClear(t, after)
	})
	t.Run("lane separation", func(t *testing.T) {
		g := edgeGraph(t, [2]string{"a", "a"}, [2]string{"a", "a"})
		o := DefaultOptions()
		next, _ := Refine(o, []Finding{blocking(CriterionMergedLines)})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		assert.InDelta(t, 6, (after.Edges[1].Points[1].X-after.Edges[0].Points[1].X)-(before.Edges[1].Points[1].X-before.Edges[0].Points[1].X), geomEps)
	})
	t.Run("gutter clearance", func(t *testing.T) {
		g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"})
		o := DefaultOptions()
		next, _ := Refine(o, []Finding{blocking(CriterionIndistinctLoop)})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		// The return's vertical segment is outside the body on the right.
		gutter := func(geo Geometry) float64 {
			right := max(boxOf(geo, "a").Right(), boxOf(geo, "b").Right())
			points := geo.Edges[1].Points
			for i := 1; i < len(points); i++ {
				if nearly(points[i].X, points[i-1].X) && points[i].Y < points[i-1].Y {
					return points[i].X - right
				}
			}
			t.Fatal("missing upward return segment")
			return 0
		}
		assert.GreaterOrEqual(t, gutter(after)-gutter(before), 24.0)
		assertNoRouteThroughUnrelatedNodes(t, after)
	})
	t.Run("crossing search", func(t *testing.T) {
		g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "x"}, {ID: "y"}}, Edges: []workflow.Edge{{Source: "a", Target: "y"}, {Source: "b", Target: "x"}}})
		require.NoError(t, err)
		o := DefaultOptions()
		o.CrossingSweeps = 0
		// Advisory search grows only alongside an applicable spacing change;
		// disconnected source boxes make the extra text slack irrelevant to ordering.
		next, _ := Refine(o, []Finding{blocking(CriterionTextClipped), {Criterion: CriterionCrossings, Severity: SeverityAdvisory}})
		before, after := arrangeFor(t, g, o), arrangeFor(t, g, next)
		assert.Less(t, len(after.Crossings), len(before.Crossings), "search must improve actual routes")
	})
}
