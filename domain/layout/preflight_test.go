package layout

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture: branch + rejoin + shortcut + loop return, the shapes the spec names.
func preflightFixture(t *testing.T) (Graph, Geometry, Measurements) {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeScript}, {ID: "gate", Type: workflow.NodeTypeConditional},
			{ID: "yes"}, {ID: "no"}, {ID: "join"}, {ID: "end"},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "gate"},
			{Source: "gate", Target: "yes", Output: "pass"},
			{Source: "gate", Target: "no", Output: "fail"},
			{Source: "yes", Target: "join"},
			{Source: "no", Target: "join"},
			{Source: "join", Target: "end"},
			{Source: "start", Target: "end", Label: "shortcut"},
			{Source: "join", Target: "gate", Label: "back-edge: retry"},
		},
	})
	require.NoError(t, err)
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
	require.NoError(t, err)
	return g, geo, m
}

func criteria(fs []Finding) map[Criterion]int {
	out := map[Criterion]int{}
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			out[f.Criterion]++
		}
	}
	return out
}

func cloneGeometry(t *testing.T, geo Geometry) Geometry {
	t.Helper()
	out := geo
	out.Nodes = append([]NodeBox(nil), geo.Nodes...)
	out.Edges = make([]EdgeRoute, len(geo.Edges))
	for i, e := range geo.Edges {
		e.Points = append([]Point(nil), e.Points...)
		if e.Text != nil {
			lb := *e.Text
			e.Text = &lb
		}
		out.Edges[i] = e
	}
	out.Crossings = append([]Crossing(nil), geo.Crossings...)
	return out
}

func TestPreflight_ArrangedFixtureHasNoBlockingFindings(t *testing.T) {
	g, geo, m := preflightFixture(t)
	fs := Preflight(g, geo, m)
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			t.Errorf("unexpected blocking finding: %+v", f)
		}
		assert.Equal(t, SourcePreflight, f.Source)
	}
}

func TestPreflight_InvalidGeometryIsACoverageFinding(t *testing.T) {
	g, geo, m := preflightFixture(t)
	broken := cloneGeometry(t, geo)
	broken.Nodes = broken.Nodes[:3]
	fs := Preflight(g, broken, m)
	require.Len(t, fs, 1)
	assert.Equal(t, CriterionCoverage, fs[0].Criterion)
	assert.Equal(t, SeverityBlocking, fs[0].Severity)
}

func TestPreflight_DetectsInjectedDefects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, g Graph, geo *Geometry)
		want   Criterion
	}{
		{"node overlap", func(t *testing.T, _ Graph, geo *Geometry) {
			yes, no := idx(geo, "yes"), idx(geo, "no")
			geo.Nodes[no].Box.X = geo.Nodes[yes].Box.X + 10
			geo.Nodes[no].Box.Y = geo.Nodes[yes].Box.Y
		}, CriterionCoverage},
		{"route through unrelated node", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[0] // start → gate
			yes := geo.Nodes[idx(geo, "yes")].Box
			src, dst := e.Points[0], e.Points[len(e.Points)-1]
			// detour: down, across through "yes", back, down
			mid := yes.CenterY()
			e.Points = []Point{src, {X: src.X, Y: mid}, {X: yes.CenterX(), Y: mid}, {X: yes.CenterX(), Y: mid + 1}, {X: dst.X, Y: mid + 1}, dst}
		}, CriterionCoverage},
		{"label over a node", func(t *testing.T, _ Graph, geo *Geometry) {
			yes := geo.Nodes[idx(geo, "yes")].Box
			geo.Edges[1].Text.Box.X, geo.Edges[1].Text.Box.Y = yes.X+5, yes.Y+5
		}, CriterionTextCollision},
		{"label too small for its text", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Edges[1].Text.Box.W = 8
		}, CriterionTextClipped},
		{"node too small for its text", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Nodes[idx(geo, "gate")].Box.H = 20
		}, CriterionCoverage},
		{"merged lines", func(t *testing.T, _ Graph, geo *Geometry) {
			// draw edge 4 (no→join) exactly over edge 3 (yes→join)
			geo.Edges[4].Points = append([]Point(nil), geo.Edges[3].Points...)
			geo.Edges[4].SourceAttach.Point = geo.Edges[4].Points[0]
			geo.Edges[4].TargetAttach.Point = geo.Edges[4].Points[len(geo.Edges[4].Points)-1]
			geo.Edges[4].SourceAttach.NodeID = "no"
		}, CriterionCoverage},
		{"attachment off the node", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[5]
			e.SourceAttach.Point.Y -= 7
			e.Points[0].Y -= 7
		}, CriterionCoverage},
		{"arrow points away from target", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[5] // join → end
			last := e.Points[len(e.Points)-1]
			e.Points = append(e.Points[:len(e.Points)-1], Point{X: last.X, Y: last.Y + 30}, last)
		}, CriterionArrowDirection},
		{"upward forward edge", func(t *testing.T, _ Graph, geo *Geometry) {
			end := idx(geo, "end")
			start := geo.Nodes[idx(geo, "start")].Box
			geo.Nodes[end].Box.Y = start.Y - 200
			geo.Nodes[end].Box.Y = max(geo.Nodes[end].Box.Y, 0)
		}, CriterionCoverage},
		{"loop return without gutter", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[7]
			src, dst := e.Points[0], e.Points[len(e.Points)-1]
			e.Points = []Point{src, {X: src.X, Y: src.Y + 5}, {X: dst.X, Y: src.Y + 5}, {X: dst.X, Y: dst.Y - 5}, {X: dst.X - 1, Y: dst.Y - 5}, {X: dst.X - 1, Y: dst.Y}}
			e.TargetAttach.Point = Point{X: dst.X - 1, Y: dst.Y}
		}, CriterionCoverage},
		{"label detached from its edge", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Edges[1].Text.Box.X += 400
			geo.Canvas.W += 500
		}, CriterionIndistinctBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, base, m := preflightFixture(t)
			geo := cloneGeometry(t, base)
			tc.mutate(t, g, &geo)
			fs := Preflight(g, geo, m)
			t.Logf("mutated findings: %+v", fs)
			got := criteria(fs)
			assert.Greater(t, got[tc.want], 0, "want a blocking %s finding, got %v", tc.want, got)
		})
	}
}

func idx(geo *Geometry, id string) int {
	for i, n := range geo.Nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func TestPreflight_UntreatedCrossingK22(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "c"}, [2]string{"a", "d"}, [2]string{"b", "c"}, [2]string{"b", "d"})
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
	require.NoError(t, err)
	require.NotEmpty(t, geo.Crossings, "this fixed two-rank port order requires a crossing")

	treated := Preflight(g, geo, m)
	assert.Equal(t, 0, criteria(treated)[CriterionAmbiguousCrossing], "treated crossings are fine")
	advisory := 0
	for _, f := range treated {
		if f.Criterion == CriterionCrossings {
			advisory++
			assert.Equal(t, SeverityAdvisory, f.Severity)
		}
	}
	assert.Equal(t, 1, advisory, "remaining crossings are reported as advisory")

	untreated := cloneGeometry(t, geo)
	untreated.Crossings = nil
	assert.Greater(t, criteria(Preflight(g, untreated, m))[CriterionAmbiguousCrossing], 0)

	noTreatment := cloneGeometry(t, geo)
	noTreatment.Contract.CrossingTreatment = "none"
	m.Contract = noTreatment.Contract
	assert.Greater(t, criteria(Preflight(g, noTreatment, m))[CriterionAmbiguousCrossing], 0)
}

func TestPreflight_UnreadableContract(t *testing.T) {
	g, geo, m := preflightFixture(t)
	small := cloneGeometry(t, geo)
	small.Contract.NodeTypeFontPx = 8 // 8 × 0.9 = 7.2px < 9px
	m.Contract = small.Contract       // this test isolates readability, not measurement mismatch
	fs := Preflight(g, small, m)
	assert.Greater(t, criteria(fs)[CriterionTextUnreadable], 0)
	var ev string
	for _, f := range fs {
		if f.Criterion == CriterionTextUnreadable {
			ev = f.Evidence
		}
	}
	assert.True(t, strings.Contains(ev, "type caption"))
}

func TestPreflight_MissingMeasurementIsCoverage(t *testing.T) {
	g, geo, m := preflightFixture(t)
	delete(m.Items, NodeLabelKey("gate"))
	assert.Greater(t, criteria(Preflight(g, geo, m))[CriterionCoverage], 0)
}

func TestCrossingNearBend(t *testing.T) {
	routes := []EdgeRoute{
		{Ordinal: 0, Points: []Point{{X: 0, Y: 100}, {X: 200, Y: 100}, {X: 200, Y: 300}}},
		{Ordinal: 1, Points: []Point{{X: 196, Y: 0}, {X: 196, Y: 300}}},
		{Ordinal: 2, Points: []Point{{X: 100, Y: 0}, {X: 100, Y: 300}}},
	}
	radius := DefaultContract().CrossingGapPx/2 + DefaultContract().StrokePx
	assert.True(t, crossingNearBend(Crossing{Point: Point{X: 196, Y: 100}, Over: 0, Under: 1}, routes, radius), "4px from edge 0's bend")
	assert.False(t, crossingNearBend(Crossing{Point: Point{X: 100, Y: 100}, Over: 0, Under: 2}, routes, radius))
}

func TestSegmentHitsRect(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 100, H: 50}
	assert.True(t, segmentHitsRect(Point{X: 50, Y: 0}, Point{X: 50, Y: 100}, r))
	assert.False(t, segmentHitsRect(Point{X: 10, Y: 0}, Point{X: 10, Y: 100}, r), "running along a boundary is not passing through")
	assert.True(t, segmentHitsRect(Point{X: 0, Y: 30}, Point{X: 200, Y: 30}, r))
	assert.False(t, segmentHitsRect(Point{X: 0, Y: 70}, Point{X: 200, Y: 70}, r))
}
