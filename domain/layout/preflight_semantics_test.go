package layout

import (
	"fmt"
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func collected(run func(addFinding)) []Finding {
	var fs []Finding
	run(func(c Criterion, s Severity, ns []string, es []int, f string, args ...any) {
		fs = append(fs, Finding{Criterion: c, Severity: s, Source: SourcePreflight, NodeIDs: ns, EdgeOrdinals: es, Evidence: fmt.Sprintf(f, args...)})
	})
	return fs
}

func TestPreflight_DirectSemanticChecks(t *testing.T) {
	c := DefaultContract()
	cases := []struct {
		name      string
		criterion Criterion
		run       func(addFinding)
	}{
		{"clearance", CriterionNodeOverlap, func(add addFinding) {
			checkNodeOverlap(Geometry{Contract: c, Nodes: []NodeBox{{ID: "a", Box: Rect{0, 0, 100, 100}}, {ID: "b", Box: Rect{102, 0, 100, 100}}}}, add)
		}},
		{"parallel lanes", CriterionMergedLines, func(add addFinding) {
			checkMergedLines(Geometry{Contract: c, Edges: []EdgeRoute{{Ordinal: 0, Points: []Point{{0, 0}, {100, 0}}}, {Ordinal: 1, Points: []Point{{0, 3}, {100, 3}}}}}, add)
		}},
		{"off side", CriterionAmbiguousAttachment, func(add addFinding) {
			checkAttachments(Geometry{Contract: c, Nodes: []NodeBox{{ID: "a", Box: Rect{0, 0, 100, 100}}}, Edges: []EdgeRoute{{Ordinal: 0, SourceAttach: Attachment{NodeID: "a", Side: SideTop, Point: Point{50, 5}}, TargetAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{50, 100}}}}}, add)
		}},
		{"merged ports", CriterionAmbiguousAttachment, func(add addFinding) {
			checkAttachments(Geometry{Contract: c, Nodes: []NodeBox{{ID: "a", Box: Rect{0, 0, 100, 100}}}, Edges: []EdgeRoute{{Ordinal: 0, SourceAttach: Attachment{NodeID: "a", Side: SideTop, Point: Point{50, 0}}, TargetAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{50, 100}}}, {Ordinal: 1, SourceAttach: Attachment{NodeID: "a", Side: SideTop, Point: Point{53, 0}}, TargetAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{80, 100}}}}}, add)
		}},
		{"upward interior", CriterionUpwardProgression, func(add addFinding) {
			checkProgression(Geometry{Nodes: []NodeBox{{ID: "a", Box: Rect{0, 0, 100, 100}}, {ID: "b", Box: Rect{0, 300, 100, 100}}}, Edges: []EdgeRoute{{Ordinal: 0, Source: "a", Target: "b", Kind: EdgeForward, Points: []Point{{50, 100}, {50, 200}, {70, 200}, {70, 180}, {80, 180}, {80, 300}}}}}, add)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := collected(tc.run)
			require.Greater(t, criteria(fs)[tc.criterion], 0)
			for _, f := range fs {
				assert.NotEmpty(t, f.Evidence)
			}
		})
	}
}

func TestPreflight_ValidGeometryStillBlocksSemanticDefects(t *testing.T) {
	t.Run("close nodes", func(t *testing.T) {
		g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}}})
		require.NoError(t, err)
		m := measured(t, g)
		geo := arrangeFor(t, g, DefaultOptions())
		geo.Nodes[1].Box.X = geo.Nodes[0].Box.Right() + 2
		geo.Nodes[1].Box.Y = geo.Nodes[0].Box.Y
		geo.Canvas.W = max(geo.Canvas.W, geo.Nodes[1].Box.Right()+40)
		require.NoError(t, geo.Validate(g))
		assert.Positive(t, criteria(Preflight(g, geo, m))[CriterionNodeOverlap])
	})
	t.Run("close lanes", func(t *testing.T) {
		g := edgeGraph(t, [2]string{"a", "c"}, [2]string{"b", "d"})
		m := measured(t, g)
		geo := arrangeFor(t, g, DefaultOptions())
		// Separate node boundaries remain valid; the middle horizontal lanes are only 3px apart.
		for i := range geo.Nodes {
			geo.Nodes[i].Box = Rect{X: float64(i)*250 + 40, Y: 40, W: 180, H: 80}
			if geo.Nodes[i].ID == "c" || geo.Nodes[i].ID == "d" {
				geo.Nodes[i].Box.Y = 500
			}
		}
		for i := range geo.Edges {
			e := &geo.Edges[i]
			s, d := boxOf(geo, e.Source), boxOf(geo, e.Target)
			a, b := Point{s.CenterX(), s.Bottom()}, Point{d.CenterX(), d.Y}
			y := 250 + float64(i)*3
			e.Points = []Point{a, {a.X, y}, {1000, y}, {1000, 400 + float64(i)*20}, {b.X, 400 + float64(i)*20}, b}
			e.SourceAttach.Point = a
			e.TargetAttach.Point = b
		}
		geo.Canvas = Rect{0, 0, 1200, 650}
		geo.Crossings = nil
		require.NoError(t, geo.Validate(g))
		assert.Positive(t, criteria(Preflight(g, geo, m))[CriterionMergedLines])
	})
	t.Run("upward segment", func(t *testing.T) {
		g, geo, m := preflightFixture(t)
		e := &geo.Edges[0]
		a := e.Points[0]
		e.Points = append([]Point{a, {a.X, a.Y + 30}, {a.X + 5, a.Y + 30}, {a.X + 5, a.Y + 20}, {a.X, a.Y + 20}}, e.Points[1:]...)
		geo.Crossings = nil
		require.NoError(t, geo.Validate(g))
		assert.Positive(t, criteria(Preflight(g, geo, m))[CriterionUpwardProgression])
	})
}

func TestPreflight_ValidRouteThroughNodeAndWrongGutter(t *testing.T) {
	g, geo, m := preflightFixture(t)
	t.Run("through unrelated node", func(t *testing.T) {
		bad := cloneGeometry(t, geo)
		e := &bad.Edges[0]
		a, b := e.Points[0], e.Points[len(e.Points)-1]
		yes := boxOf(bad, "yes")
		x, y := yes.CenterX(), yes.CenterY()
		e.Points = []Point{a, {a.X, y}, {x, y}, {x, y + 1}, {b.X, y + 1}, b}
		e.Points = simplify(e.Points)
		bad.Crossings = nil
		require.NoError(t, bad.Validate(g))
		assert.Positive(t, criteria(Preflight(g, bad, m))[CriterionEdgeThroughNode])
	})
	t.Run("wrong declared gutter side", func(t *testing.T) {
		bad := cloneGeometry(t, geo)
		e := &bad.Edges[7]
		if e.Side == SideLeft {
			e.Side = SideRight
		} else {
			e.Side = SideLeft
		}
		require.NoError(t, bad.Validate(g))
		assert.Positive(t, criteria(Preflight(g, bad, m))[CriterionIndistinctLoop])
	})
}

func TestPreflight_SharedLoopLaneOnClassifiedReturns(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "a"})
	geo := arrangeFor(t, g, DefaultOptions())
	require.Equal(t, EdgeLoopReturn, geo.Edges[2].Kind)
	geo.Edges[2] = geo.Edges[1]
	geo.Edges[2].Ordinal = 2
	require.NoError(t, geo.Validate(g))
	fs := collected(func(add addFinding) { checkLoops(g, geo, add) })
	require.Len(t, fs, 1)
	assert.Equal(t, CriterionIndistinctLoop, fs[0].Criterion)
	assert.Contains(t, fs[0].Evidence, "share a gutter lane")
	assert.Equal(t, []int{1, 2}, fs[0].EdgeOrdinals)
}

func TestLabelAssociated_AllKinds(t *testing.T) {
	for _, kind := range []EdgeKind{EdgeForward, EdgeLoopReturn, EdgeSelfLoop} {
		t.Run(string(kind), func(t *testing.T) {
			e := EdgeRoute{Ordinal: 0, Kind: kind, Points: []Point{{0, 0}, {0, 200}}, Text: &LabelBox{Box: Rect{10, 50, 30, 20}}}
			base := Geometry{Options: DefaultOptions(), Edges: []EdgeRoute{e}}
			assert.True(t, labelAssociated(base, e), "distance to full segment, not vertices")
			for _, tc := range []struct {
				name     string
				box      Rect
				foreignX float64
				want     bool
			}{
				{"detached", Rect{50, 50, 30, 20}, 100, false},
				{"equidistant", e.Text.Box, 50, false},
				{"foreign nearer", e.Text.Box, 45, false},
				{"overlap own", Rect{-5, 50, 30, 20}, 100, false},
				{"nonfinite box", Rect{math.NaN(), 50, 30, 20}, 100, false},
				{"clear foreign", e.Text.Box, 100, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					geo := base
					own := e
					own.Text = &LabelBox{Box: tc.box}
					geo.Edges = []EdgeRoute{own, {Ordinal: 1, Points: []Point{{tc.foreignX, 0}, {tc.foreignX, 200}}}}
					assert.Equal(t, tc.want, labelAssociated(geo, own))
				})
			}
		})
	}
}

func TestPreflight_MeasurementCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		metric TextMetrics
	}{
		{"nonfinite", TextMetrics{math.Inf(1), 20, 1}}, {"zero nonempty", TextMetrics{}}, {"wrong lines", TextMetrics{20, 20, 99}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, geo, m := preflightFixture(t)
			m.Items[NodeLabelKey("gate")] = tc.metric
			assert.Positive(t, criteria(Preflight(g, geo, m))[CriterionCoverage])
		})
	}
}

func crossingGeometry() Geometry {
	return Geometry{Contract: DefaultContract(), Edges: []EdgeRoute{
		{Ordinal: 0, Points: []Point{{0, 100}, {300, 100}}},
		{Ordinal: 1, Points: []Point{{100, 0}, {100, 300}}},
	}, Crossings: []Crossing{{Point: Point{100, 100}, Over: 0, Under: 1}}}
}
func TestPreflight_CrossingSemantics(t *testing.T) {
	base := crossingGeometry()
	assert.Empty(t, criteria(collected(func(add addFinding) { checkCrossings(base, add) })))
	for _, tc := range []struct {
		name   string
		mutate func(*Geometry)
	}{
		{"no annotation", func(g *Geometry) { g.Crossings = nil }},
		{"no treatment", func(g *Geometry) { g.Contract.CrossingTreatment = "none" }},
		{"bend contact", func(g *Geometry) { g.Edges[0].Points = []Point{{0, 100}, {100, 100}, {100, 200}}; g.Crossings = nil }},
		{"T touch", func(g *Geometry) { g.Edges[0].Points[1].X = 100; g.Crossings = nil }},
		{"endpoint touch", func(g *Geometry) { g.Edges[0].Points[1].X = 100; g.Edges[1].Points[0].Y = 100; g.Crossings = nil }},
		{"near bend", func(g *Geometry) { g.Edges[0].Points = []Point{{0, 100}, {104, 100}, {104, 250}} }},
		{"overlapping gaps", func(g *Geometry) {
			g.Edges = append(g.Edges, EdgeRoute{Ordinal: 2, Points: []Point{{0, 110}, {300, 110}}})
			g.Crossings = append(g.Crossings, Crossing{Point: Point{100, 110}, Over: 2, Under: 1})
		}},
		{"arrow interference", func(g *Geometry) { g.Edges[1].Points[1].Y = 112 }},
		{"false annotation", func(g *Geometry) { g.Crossings[0].Point.Y = 120 }},
		{"duplicate annotation", func(g *Geometry) { g.Crossings = append(g.Crossings, g.Crossings[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := cloneGeometry(t, base)
			tc.mutate(&geo)
			fs := collected(func(add addFinding) { checkCrossings(geo, add) })
			assert.Positive(t, criteria(fs)[CriterionAmbiguousCrossing], "%+v", fs)
		})
	}
	t.Run("true shared boundary", func(t *testing.T) {
		geo := crossingGeometry()
		p := Point{100, 100}
		geo.Nodes = []NodeBox{{ID: "n", Box: Rect{0, 100, 200, 100}}}
		geo.Edges[0].Points = []Point{{0, 100}, p}
		geo.Edges[1].Points = []Point{p, {100, 0}}
		geo.Edges[0].TargetAttach = Attachment{NodeID: "n", Side: SideTop, Point: p}
		geo.Edges[1].SourceAttach = Attachment{NodeID: "n", Side: SideTop, Point: p}
		geo.Crossings = nil
		assert.Empty(t, collected(func(add addFinding) { checkCrossings(geo, add) }))
	})
}

func TestPreflight_InvalidAnnotationsAreCoverage(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "c"}, [2]string{"a", "d"}, [2]string{"b", "c"}, [2]string{"b", "d"})
	geo := arrangeFor(t, g, DefaultOptions())
	m := measured(t, g)
	require.NotEmpty(t, geo.Crossings)
	for _, duplicate := range []bool{false, true} {
		bad := cloneGeometry(t, geo)
		if duplicate {
			bad.Crossings = append(bad.Crossings, bad.Crossings[0])
		} else {
			bad.Crossings[0].Point = Point{1, 1}
		}
		fs := Preflight(g, bad, m)
		require.Len(t, fs, 1)
		assert.Equal(t, CriterionCoverage, fs[0].Criterion)
	}
}

func TestPreflight_LoopSemantics(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"})
	base := arrangeFor(t, g, DefaultOptions())
	require.Equal(t, EdgeLoopReturn, base.Edges[1].Kind)
	for _, tc := range []struct {
		name   string
		mutate func(*Geometry)
	}{
		{"wrong classification", func(geo *Geometry) { geo.Edges[1].Kind = EdgeForward }},
		{"wrong declared side", func(geo *Geometry) {
			e := &geo.Edges[1]
			if e.Side == SideLeft {
				e.Side = SideRight
			} else {
				e.Side = SideLeft
			}
		}},
		{"no positive lane", func(geo *Geometry) { geo.Edges[1].Lane = 0 }},
		{"gutter only downward", func(geo *Geometry) {
			e := &geo.Edges[1]
			for i, j := 0, len(e.Points)-1; i < j; i, j = i+1, j-1 {
				e.Points[i], e.Points[j] = e.Points[j], e.Points[i]
			}
		}},
		{"gutter inside full node extent", func(geo *Geometry) {
			e := geo.Edges[1]
			lo, hi := geo.Nodes[0].Box.X, geo.Nodes[0].Box.Right()
			for _, p := range e.Points {
				lo, hi = min(lo, p.X-20), max(hi, p.X+20)
			}
			geo.Nodes = append(geo.Nodes, NodeBox{ID: "unrelated", Box: Rect{lo, 1000, hi - lo, 80}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := cloneGeometry(t, base)
			tc.mutate(&geo)
			fs := collected(func(add addFinding) { checkLoops(g, geo, add) })
			assert.Positive(t, criteria(fs)[CriterionIndistinctLoop], "%+v", fs)
		})
	}
	sg := edgeGraph(t, [2]string{"a", "a"}, [2]string{"a", "a"})
	self := arrangeFor(t, sg, DefaultOptions())
	for _, tc := range []struct {
		name   string
		mutate func(*Geometry)
	}{
		{"close ports", func(geo *Geometry) {
			e := &geo.Edges[0]
			e.Points[len(e.Points)-1].Y = e.Points[0].Y + 2
			e.TargetAttach.Point = e.Points[len(e.Points)-1]
		}},
		{"intersecting bodies", func(geo *Geometry) {
			e := &geo.Edges[1]
			e.Points = append([]Point(nil), geo.Edges[0].Points...)
			e.SourceAttach.Point = e.Points[0]
			e.TargetAttach.Point = e.Points[len(e.Points)-1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := cloneGeometry(t, self)
			tc.mutate(&geo)
			assert.Positive(t, criteria(collected(func(add addFinding) { checkLoops(sg, geo, add) }))[CriterionIndistinctLoop])
		})
	}
}
