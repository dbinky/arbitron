package layout

import (
	"context"
	"errors"
	"maps"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackRow_KeepsOrderAndGap(t *testing.T) {
	widths := []float64{100, 50, 80}
	anchors := []float64{50, 25, 40}
	desired := []float64{0, 0, 0} // everyone wants the same centre
	xs := packRow(widths, anchors, desired, 20)
	for i := 1; i < len(xs); i++ {
		assert.GreaterOrEqual(t, xs[i], xs[i-1]+widths[i-1]+20-1e-9, "item %d keeps order and spacing", i)
	}
	// The block is centred on the common desire: mean anchor offset is zero.
	mean := 0.0
	for i := range xs {
		mean += xs[i] + anchors[i] - desired[i]
	}
	assert.InDelta(t, 0, mean/3, 1e-9)
}

func TestPackRow_NonConflictingItemsSitExactlyOnTheirDesire(t *testing.T) {
	xs := packRow([]float64{10, 10}, []float64{5, 5}, []float64{0, 100}, 20)
	assert.InDelta(t, -5, xs[0], 1e-9)
	assert.InDelta(t, 95, xs[1], 1e-9)
}

func TestPackRow_SingleItem(t *testing.T) {
	xs := packRow([]float64{180}, []float64{90}, []float64{300}, 56)
	assert.InDelta(t, 210, xs[0], 1e-9)
	assert.False(t, math.IsNaN(xs[0]))
}

func arrangeFor(t *testing.T, g Graph, o Options) Geometry {
	t.Helper()
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	require.NoError(t, geo.Validate(g), "Arrange must always produce a valid, complete geometry")
	return geo
}

func boxOf(geo Geometry, id string) Rect {
	for _, n := range geo.Nodes {
		if n.ID == id {
			return n.Box
		}
	}
	return Rect{}
}

// segmentCrossesBox reports whether an axis-aligned segment passes through the
// interior of r.
func segmentCrossesBox(a, b Point, r Rect) bool {
	if nearly(a.X, b.X) {
		y1, y2 := math.Min(a.Y, b.Y), math.Max(a.Y, b.Y)
		return a.X > r.X && a.X < r.Right() && y1 < r.Bottom() && y2 > r.Y
	}
	x1, x2 := math.Min(a.X, b.X), math.Max(a.X, b.X)
	return a.Y > r.Y && a.Y < r.Bottom() && x1 < r.Right() && x2 > r.X
}

func assertNoRouteThroughUnrelatedNodes(t *testing.T, geo Geometry) {
	t.Helper()
	for _, e := range geo.Edges {
		for i := 1; i < len(e.Points); i++ {
			for _, n := range geo.Nodes {
				if n.ID == e.Source || n.ID == e.Target {
					continue
				}
				assert.False(t, segmentCrossesBox(e.Points[i-1], e.Points[i], n.Box),
					"edge %d (%s→%s) segment %d passes through %s", e.Ordinal, e.Source, e.Target, i, n.ID)
			}
		}
	}
}

func assertNoNodeOverlap(t *testing.T, geo Geometry) {
	t.Helper()
	for i := range geo.Nodes {
		for j := i + 1; j < len(geo.Nodes); j++ {
			assert.False(t, geo.Nodes[i].Box.Overlaps(geo.Nodes[j].Box), "%s overlaps %s", geo.Nodes[i].ID, geo.Nodes[j].ID)
		}
	}
}

func TestArrange_LinearChainIsOneDownwardColumn(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"})
	geo := arrangeFor(t, g, DefaultOptions())
	for i := 1; i < len(geo.Nodes); i++ {
		assert.InDelta(t, geo.Nodes[0].Box.CenterX(), geo.Nodes[i].Box.CenterX(), 0.01, "no wrapping into columns")
		assert.Greater(t, geo.Nodes[i].Box.Y, geo.Nodes[i-1].Box.Bottom(), "each step is below the previous one")
	}
	for _, e := range geo.Edges {
		assert.Len(t, e.Points, 2, "aligned sequential edges are straight")
		assert.Equal(t, SideBottom, e.SourceAttach.Side)
		assert.Equal(t, SideTop, e.TargetAttach.Side)
		assert.Equal(t, EdgeForward, e.Kind)
	}
	assert.Empty(t, geo.Crossings)
}

func TestArrange_BranchesSitBesideTheFlowWithReadableLabels(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "gate", Type: workflow.NodeTypeConditional}, {ID: "yes"}, {ID: "no"}, {ID: "join"}},
		Edges: []workflow.Edge{
			{Source: "gate", Target: "yes", Output: "pass"},
			{Source: "gate", Target: "no", Output: "fail"},
			{Source: "yes", Target: "join"},
			{Source: "no", Target: "join"},
		},
	})
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())

	yes, no := boxOf(geo, "yes"), boxOf(geo, "no")
	assert.InDelta(t, yes.Y, no.Y, 0.01, "branches share a row beside each other")
	assert.False(t, yes.Overlaps(no))
	assert.Greater(t, boxOf(geo, "join").Y, yes.Bottom(), "the rejoin is below both branches")

	l0, l1 := geo.Edges[0].Text, geo.Edges[1].Text
	require.NotNil(t, l0)
	require.NotNil(t, l1)
	assert.Equal(t, "pass", l0.Text)
	assert.False(t, l0.Box.Overlaps(l1.Box), "branch labels never overlap")
	assert.GreaterOrEqual(t, l0.Box.Y, boxOf(geo, "gate").Bottom(), "labels sit below the source")
	assert.InDelta(t, geo.Edges[0].Points[0].X+DefaultOptions().LabelGap, l0.Box.X, 0.01, "label sits beside its own source stub")
	assert.NotEqual(t, geo.Edges[0].SourceAttach.Point.X, geo.Edges[1].SourceAttach.Point.X, "distinct attachments")
	assertNoNodeOverlap(t, geo)
	assertNoRouteThroughUnrelatedNodes(t, geo)
}

func TestArrange_ShortcutUsesItsOwnColumn(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"a", "d"})
	geo := arrangeFor(t, g, DefaultOptions())
	assertNoRouteThroughUnrelatedNodes(t, geo)
	shortcut := geo.Edges[3]
	assert.Greater(t, len(shortcut.Points), 2, "the shortcut detours around b and c")
	assert.Greater(t, shortcut.TargetAttach.Point.Y, shortcut.SourceAttach.Point.Y, "still points down")
}

func TestArrange_LoopReturnUsesASideGutter(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"})
	geo := arrangeFor(t, g, DefaultOptions())
	back := geo.Edges[2]
	require.Equal(t, EdgeLoopReturn, back.Kind)
	assert.Contains(t, []Side{SideLeft, SideRight}, back.Side)
	assert.Equal(t, 1, back.Lane)

	bodyLeft, bodyRight := math.Inf(1), math.Inf(-1)
	for _, n := range geo.Nodes {
		bodyLeft = math.Min(bodyLeft, n.Box.X)
		bodyRight = math.Max(bodyRight, n.Box.Right())
	}
	outside := false
	for i := 1; i < len(back.Points); i++ {
		a, b := back.Points[i-1], back.Points[i]
		if nearly(a.X, b.X) && (a.X > bodyRight || a.X < bodyLeft) && b.Y < a.Y {
			outside = true
		}
	}
	assert.True(t, outside, "the loop return climbs in a gutter outside every row")
	last := back.Points[len(back.Points)-1]
	prev := back.Points[len(back.Points)-2]
	assert.Greater(t, last.Y, prev.Y, "arrow enters the loop head downward")
	assertNoRouteThroughUnrelatedNodes(t, geo)
}

func TestArrange_NestedLoopsGetDistinctLanes(t *testing.T) {
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"},
		[2]string{"e", "b"}, [2]string{"d", "c"},
	)
	geo := arrangeFor(t, g, DefaultOptions())
	outer, inner := geo.Edges[4], geo.Edges[5]
	if outer.Side == inner.Side {
		assert.Less(t, inner.Lane, outer.Lane, "inner loop is nearer the flow")
	}
	assert.Empty(t, crossingsBetween(geo, 4, 5), "nested loop returns do not cross each other")
	assertNoRouteThroughUnrelatedNodes(t, geo)
}

func crossingsBetween(geo Geometry, a, b int) []Crossing {
	var out []Crossing
	for _, c := range geo.Crossings {
		if (c.Over == a && c.Under == b) || (c.Over == b && c.Under == a) {
			out = append(out, c)
		}
	}
	return out
}

func TestArrange_SelfLoopsStayOnTheRightSide(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "b", Target: "b", Output: "again"}, {Source: "b", Target: "b"}, {Source: "a", Target: "c"}},
	})
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	b := boxOf(geo, "b")
	for _, ord := range []int{1, 2} {
		e := geo.Edges[ord]
		assert.Equal(t, EdgeSelfLoop, e.Kind)
		assert.Equal(t, SideRight, e.SourceAttach.Side)
		assert.Equal(t, SideRight, e.TargetAttach.Side)
		assert.InDelta(t, b.Right(), e.SourceAttach.Point.X, 0.01)
		for _, p := range e.Points {
			assert.GreaterOrEqual(t, p.X, b.Right()-0.01, "self loop never crosses its own box")
		}
	}
	assert.NotEqual(t, geo.Edges[1].SourceAttach.Point.Y, geo.Edges[2].SourceAttach.Point.Y, "parallel self loops are nested, not merged")
	require.NotNil(t, geo.Edges[1].Text)
	assert.GreaterOrEqual(t, geo.Edges[1].Text.Box.X, b.Right())
	assertNoNodeOverlap(t, geo)
}

func TestArrange_IdenticalParallelEdgesNeverMerge(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "a"})
	geo := arrangeFor(t, g, DefaultOptions())
	assert.NotEqual(t, geo.Edges[0].SourceAttach.Point.X, geo.Edges[1].SourceAttach.Point.X)
	assert.NotEqual(t, geo.Edges[0].TargetAttach.Point.X, geo.Edges[1].TargetAttach.Point.X)
	assert.NotEqual(t, geo.Edges[2].Lane, geo.Edges[3].Lane, "parallel loop returns use separate lanes")
}

func TestArrange_MultipleEntrypointsAndDisconnectedComponents(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "s1", Entrypoint: true}, {ID: "s2", Entrypoint: true}, {ID: "m"}, {ID: "iso"}, {ID: "x"}, {ID: "y"}},
		Edges: []workflow.Edge{{Source: "s1", Target: "m"}, {Source: "s2", Target: "m"}, {Source: "x", Target: "y"}},
	})
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	assertNoNodeOverlap(t, geo)
	assertNoRouteThroughUnrelatedNodes(t, geo)
	assert.Greater(t, boxOf(geo, "m").Y, boxOf(geo, "s1").Bottom())
}

func TestArrange_SingleNode(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "only", Type: workflow.NodeTypeScript}}})
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	o := DefaultOptions()
	box := geo.Nodes[0].Box
	assert.InDelta(t, o.Margin, box.X, 0.01)
	assert.InDelta(t, o.Margin, box.Y, 0.01)
	assert.InDelta(t, box.Right()+o.Margin, geo.Canvas.W, 0.01)
	assert.InDelta(t, box.Bottom()+o.Margin, geo.Canvas.H, 0.01)
}

func TestArrange_LongUnicodeLabelsGrowBoxes(t *testing.T) {
	long := strings.Repeat("検証 Überprüfung ", 12)
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Label: long}, {ID: "b"}}, Edges: []workflow.Edge{{Source: "a", Target: "b", Label: long}}})
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	assert.Greater(t, geo.Nodes[0].TextLines, 1)
	assert.Greater(t, geo.Nodes[0].Box.H, geo.Nodes[1].Box.H)
	require.NotNil(t, geo.Edges[0].Text)
	assert.Greater(t, geo.Edges[0].Text.Lines, 1)
	assert.Equal(t, long, geo.Nodes[0].Text, "full text is carried, never truncated")
}

func TestArrange_IsDeterministic(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "feature-pipeline"))
	require.NoError(t, err)
	a := arrangeFor(t, g, DefaultOptions())
	b := arrangeFor(t, g, DefaultOptions())
	assert.True(t, reflect.DeepEqual(a, b))
	assert.Equal(t, GeometryDigest(a), GeometryDigest(b))
}

func TestArrange_OptionsChangeTheCandidate(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"})
	base := arrangeFor(t, g, DefaultOptions())
	o := DefaultOptions()
	o.RankSpacing += 40
	tall := arrangeFor(t, g, o)
	assert.Greater(t, tall.Canvas.H, base.Canvas.H)
	assert.NotEqual(t, GeometryDigest(base), GeometryDigest(tall))
	assert.Equal(t, o, tall.Options, "the producing options travel with the geometry")
}

func TestArrange_DoesNotMutateTheGraph(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "ralph-loop"))
	require.NoError(t, err)
	nodes := append([]GraphNode(nil), g.Nodes...)
	edges := append([]GraphEdge(nil), g.Edges...)
	_ = arrangeFor(t, g, DefaultOptions())
	assert.Equal(t, nodes, g.Nodes)
	assert.Equal(t, edges, g.Edges)
}

func TestArrange_SamplesAreValidDownwardAndOverlapFree(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			geo := arrangeFor(t, g, DefaultOptions())
			assertNoNodeOverlap(t, geo)
			assertNoRouteThroughUnrelatedNodes(t, geo)
			for _, e := range geo.Edges {
				if e.Kind != EdgeForward {
					continue
				}
				assert.Greater(t, boxOf(geo, e.Target).Y, boxOf(geo, e.Source).Bottom(), "forward edge %d points down", e.Ordinal)
				for i := 1; i < len(e.Points); i++ {
					assert.GreaterOrEqual(t, e.Points[i].Y, e.Points[i-1].Y-0.01, "forward edge %d never climbs", e.Ordinal)
				}
			}
		})
	}
}

func TestArrange_Errors(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "go"}},
	})
	require.NoError(t, err)
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)

	_, err = Arrange(context.Background(), Graph{}, m, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrEmptyGraph))

	missing := Measurements{Contract: c, FontsReady: true, Items: map[string]TextMetrics{}}
	_, err = Arrange(context.Background(), g, missing, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrMissingMeasurement))

	other := m
	other.Contract.Version = "arranged-renderer/0"
	_, err = Arrange(context.Background(), g, other, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrContractMismatch))

	notReady := m
	notReady.FontsReady = false
	_, err = Arrange(context.Background(), g, notReady, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrFontsNotReady))
}

func TestArrange_RejectsMalformedInputWithoutMutation(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"})
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	type inputCase struct {
		name     string
		graph    Graph
		metrics  Measurements
		contract RendererContract
		options  Options
	}
	cases := []inputCase{
		{"nil index", Graph{Nodes: g.Nodes, Edges: g.Edges}, m, c, o},
		{"bad index", Graph{Nodes: g.Nodes, Edges: g.Edges, index: map[string]int{"a": 8, "b": 1}}, m, c, o},
	}
	for _, change := range []func(*Graph){
		func(v *Graph) { v.Edges[0].Ordinal = 9 }, func(v *Graph) { v.Edges[0].Target = "missing" },
		func(v *Graph) { v.Nodes[1].ID = "a" }, func(v *Graph) { v.Nodes[0].ID = "" },
	} {
		v := g
		v.Nodes = append([]GraphNode(nil), g.Nodes...)
		v.Edges = append([]GraphEdge(nil), g.Edges...)
		change(&v)
		cases = append(cases, inputCase{"malformed graph", v, m, c, o})
	}
	for _, field := range []string{"padding", "weight"} {
		other := m
		if field == "padding" {
			other.Contract.NodePaddingX++
		} else {
			other.Contract.NodeLabelWeight++
		}
		cases = append(cases, inputCase{field, g, other, c, o})
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1, 0} {
		opts := o
		opts.LaneSeparation = bad
		cases = append(cases, inputCase{"invalid option", g, m, c, opts})
		con := c
		con.StrokePx = bad
		cases = append(cases, inputCase{"invalid contract", g, m, con, o})
	}
	zero := measured(t, g)
	zero.Items[NodeLabelKey("a")] = TextMetrics{}
	cases = append(cases, inputCase{"zero text", g, zero, c, o})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeNodes := append([]GraphNode(nil), tc.graph.Nodes...)
			beforeEdges := append([]GraphEdge(nil), tc.graph.Edges...)
			beforeIndex, beforeMetrics := maps.Clone(tc.graph.index), maps.Clone(tc.metrics.Items)
			assert.NotPanics(t, func() {
				_, err := Arrange(context.Background(), tc.graph, tc.metrics, tc.contract, tc.options)
				assert.Error(t, err)
			})
			assert.Equal(t, beforeNodes, tc.graph.Nodes)
			assert.Equal(t, beforeEdges, tc.graph.Edges)
			assert.Equal(t, beforeIndex, tc.graph.index)
			assert.Equal(t, beforeMetrics, tc.metrics.Items)
		})
	}
}

func assertLabelsClear(t *testing.T, geo Geometry) {
	t.Helper()
	for _, e := range geo.Edges {
		if e.Text != nil {
			for _, n := range geo.Nodes {
				assert.False(t, e.Text.Box.Overlaps(n.Box), "label %d covers node %s", e.Ordinal, n.ID)
			}
			for _, other := range geo.Edges {
				if other.Ordinal > e.Ordinal && other.Text != nil {
					assert.False(t, e.Text.Box.Overlaps(other.Text.Box), "labels %d/%d", e.Ordinal, other.Ordinal)
				}
				for i := 1; i < len(other.Points); i++ {
					assert.False(t, segmentCrossesBox(other.Points[i-1], other.Points[i], e.Text.Box.Inflate(geo.Contract.StrokePx/2)), "path %d enters label %d", other.Ordinal, e.Ordinal)
				}
			}
		}
	}
}

// Independent sink check: perpendicular contacts need room for the crossing
// gap on both strokes; close overlapping parallels must never masquerade as one.
func assertDistinguishablePaths(t *testing.T, geo Geometry) {
	t.Helper()
	for i, a := range geo.Edges {
		for _, b := range geo.Edges[i+1:] {
			for ia := 1; ia < len(a.Points); ia++ {
				for ib := 1; ib < len(b.Points); ib++ {
					a0, a1, b0, b1 := a.Points[ia-1], a.Points[ia], b.Points[ib-1], b.Points[ib]
					ah, bh := a0.Y == a1.Y, b0.Y == b1.Y
					if ah == bh {
						dist := math.Abs(a0.X - b0.X)
						lo, hi := math.Max(math.Min(a0.Y, a1.Y), math.Min(b0.Y, b1.Y)), math.Min(math.Max(a0.Y, a1.Y), math.Max(b0.Y, b1.Y))
						if ah {
							dist = math.Abs(a0.Y - b0.Y)
							lo = math.Max(math.Min(a0.X, a1.X), math.Min(b0.X, b1.X))
							hi = math.Min(math.Max(a0.X, a1.X), math.Max(b0.X, b1.X))
						}
						if hi > lo+geomEps {
							assert.GreaterOrEqual(t, dist+geomEps, geo.Contract.StrokePx+geo.Contract.MinClearancePx, "merged/close edges %d/%d", a.Ordinal, b.Ordinal)
						}
						continue
					}
					h0, h1, v0, v1 := a0, a1, b0, b1
					if !ah {
						h0, h1, v0, v1 = b0, b1, a0, a1
					}
					x, y := v0.X, h0.Y
					if x < math.Min(h0.X, h1.X) || x > math.Max(h0.X, h1.X) || y < math.Min(v0.Y, v1.Y) || y > math.Max(v0.Y, v1.Y) {
						continue
					}
					for _, distance := range []float64{math.Abs(x - h0.X), math.Abs(x - h1.X), math.Abs(y - v0.Y), math.Abs(y - v1.Y)} {
						assert.GreaterOrEqual(t, distance+geomEps, geo.Contract.CrossingGapPx/2+geo.Contract.StrokePx, "bend/T or gap ambiguity edges %d/%d", a.Ordinal, b.Ordinal)
					}
				}
			}
		}
	}
	for i, c := range geo.Crossings {
		for _, d := range geo.Crossings[i+1:] {
			if c.Under == d.Under && nearly(c.Point.X, d.Point.X) {
				assert.GreaterOrEqual(t, math.Abs(c.Point.Y-d.Point.Y)+geomEps, geo.Contract.CrossingGapPx+2*geo.Contract.StrokePx)
			}
		}
	}
}

func TestArrange_StressOccurrencePortsAndLabels(t *testing.T) {
	for _, mode := range []string{"self", "parallel", "intersecting loops", "unequal heights"} {
		t.Run(mode, func(t *testing.T) {
			wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b", Label: strings.Repeat("検証Überprüfung", 25)}, {ID: "c"}, {ID: "d"}}, Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}, {Source: "c", Target: "d"}}}
			switch mode {
			case "self":
				for i := 0; i < 12; i++ {
					e := workflow.Edge{Source: "a", Target: "a"}
					if i < 2 {
						e.Label = strings.Repeat("再試行", 25)
					}
					wf.Edges = append(wf.Edges, e)
				}
			case "parallel":
				for i := 0; i < 8; i++ {
					wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "b", Output: "short"}, workflow.Edge{Source: "a", Target: "d", Output: "long"}, workflow.Edge{Source: "d", Target: "a", Output: "return"})
				}
			case "intersecting loops":
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "c", Target: "a", Output: "retry"}, workflow.Edge{Source: "d", Target: "b", Output: "again"})
			case "unequal heights":
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "d", Label: "shortcut"})
			}
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			geo := arrangeFor(t, g, DefaultOptions())
			assertNoNodeOverlap(t, geo)
			assertNoRouteThroughUnrelatedNodes(t, geo)
			assertLabelsClear(t, geo)
			assertDistinguishablePaths(t, geo)
			for _, a := range geo.Edges {
				for _, b := range geo.Edges {
					if a.Ordinal >= b.Ordinal {
						continue
					}
					for _, pair := range [][2]Attachment{{a.SourceAttach, b.SourceAttach}, {a.TargetAttach, b.TargetAttach}} {
						if pair[0].NodeID == pair[1].NodeID && pair[0].Side == pair[1].Side {
							distance := math.Hypot(pair[0].Point.X-pair[1].Point.X, pair[0].Point.Y-pair[1].Point.Y)
							assert.GreaterOrEqual(t, distance+geomEps, geo.Options.PortSpacing)
						}
					}
				}
			}
		})
	}
}

func TestArrange_TwoLongSelfLabelsBesideTallUnicodeNode(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "short", Label: "Short"}, {ID: "tall", Label: strings.Repeat("検証", 1000)}, {ID: "join"}}, Edges: []workflow.Edge{
		{Source: "short", Target: "short", Label: strings.Repeat("再試行 Überprüfung ", 4)},
		{Source: "short", Target: "short", Label: strings.Repeat("次の試行へ ", 12)},
		{Source: "short", Target: "join", Output: "pass"}, {Source: "tall", Target: "join"},
	}})
	require.NoError(t, err)
	o := DefaultOptions()
	geo := arrangeFor(t, g, o)
	short, tall := boxOf(geo, "short"), boxOf(geo, "tall")
	assert.Equal(t, short.Y, tall.Y)
	assert.Less(t, short.H, tall.H)
	assertLabelsClear(t, geo)
	assertDistinguishablePaths(t, geo)
	assertNoNodeOverlap(t, geo)
	assertNoRouteThroughUnrelatedNodes(t, geo)
	for _, ord := range []int{0, 1} {
		label := geo.Edges[ord].Text
		require.NotNil(t, label)
		assert.GreaterOrEqual(t, label.Box.X, short.Right()+o.LabelGap-geomEps)
	}
	o.LabelGap += 4
	spaced := arrangeFor(t, g, o)
	assertLabelsClear(t, spaced)
	assertDistinguishablePaths(t, spaced)
	oldExtent := geo.Edges[1].Points[1].X - short.Right()
	newExtent := spaced.Edges[1].Points[1].X - boxOf(spaced, "short").Right()
	assert.InDelta(t, 16, newExtent-oldExtent, geomEps, "two measured label strips each grow by twice the extra gap")
	c := DefaultContract()
	m := measured(t, g)
	delete(m.Items, EdgeLabelKey(1))
	before := maps.Clone(m.Items)
	_, err = Arrange(context.Background(), g, m, c, DefaultOptions())
	assert.ErrorIs(t, err, ErrMissingMeasurement)
	assert.Equal(t, before, m.Items)
}

func TestArrange_MeasurementsDriveGeometryAndAreUnchanged(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Label: strings.Repeat("検証", 100)}, {ID: "b"}}, Edges: []workflow.Edge{{Source: "a", Target: "b", Label: strings.Repeat("検証", 40)}}})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	original := map[string]TextMetrics{}
	for k, v := range m.Items {
		original[k] = v
	}
	base, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	require.Len(t, base.Nodes, len(g.Nodes))
	require.Len(t, base.Edges, len(g.Edges))
	assert.GreaterOrEqual(t, base.Nodes[0].Box.H, m.Items[NodeLabelKey("a")].Height)
	require.NotNil(t, base.Edges[0].Text)
	assert.GreaterOrEqual(t, base.Edges[0].Text.Box.H, m.Items[EdgeLabelKey(0)].Height)
	o.NodeTextSlack = 8
	grown, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	assert.Equal(t, base.Nodes[0].Box.H+8, grown.Nodes[0].Box.H)
	assert.Equal(t, base.Edges[0].Text.Box.H+8, grown.Edges[0].Text.Box.H)
	assert.Equal(t, original, m.Items)
}

func TestArrange_LayerInversionsMustNotDiscardBetterOrthogonalRoutes(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "x"}, {ID: "y"}, {ID: "z"}, {ID: "end"}}, Edges: []workflow.Edge{{Source: "a", Target: "x"}, {Source: "a", Target: "y"}, {Source: "a", Target: "z"}, {Source: "b", Target: "x"}}})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, m, c, o, labels)
	require.NoError(t, err)
	initial := buildLayering(g, kinds, ranks)
	ordered := *initial
	ordered.rows = cloneRows(initial.rows)
	orderLayers(context.Background(), &ordered, 1)
	assert.Less(t, ordered.crossings(), initial.crossings())
	original := newPlacement(g, kinds, ranks, initial, sizes, labels, c, o).route()
	reordered := newPlacement(g, kinds, ranks, &ordered, sizes, labels, c, o).route()
	assert.True(t, costOf(original).less(costOf(reordered)), "fixture must favor initial complete routes despite abstract inversions")
	o.CrossingSweeps = 1
	actual, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	assert.Equal(t, costOf(original), costOf(actual), "must retain better complete routes, not fewer abstract inversions")
	for i := range original.Edges {
		assert.Equal(t, original.Edges[i].Points, actual.Edges[i].Points)
	}
}

func TestArrange_SearchPrefixesCompareCompleteRoutes(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			c, o := DefaultContract(), DefaultOptions()
			m := measured(t, g)
			kinds := classifyEdges(g)
			ranks := assignRanks(g, kinds)
			labels, err := edgeLabelSizes(g, m, c, o)
			require.NoError(t, err)
			sizes, err := nodeSizes(g, kinds, m, c, o, labels)
			require.NoError(t, err)
			initial := buildLayering(g, kinds, ranks)
			var previous routeCost
			maxBudget := o.CrossingSweeps
			for budget := 0; budget <= maxBudget; budget++ {
				o.CrossingSweeps = budget
				geo, err := Arrange(context.Background(), g, m, c, o)
				require.NoError(t, err)
				got := costOf(geo)
				if budget > 0 {
					assert.False(t, previous.less(got), "budget %d regressed: %+v -> %+v", budget, previous, got)
				}
				previous = got
				for prefix := 0; prefix <= budget; prefix++ {
					l := *initial
					l.rows = cloneRows(initial.rows)
					orderLayers(context.Background(), &l, prefix)
					candidate := newPlacement(g, kinds, ranks, &l, sizes, labels, c, o).route()
					want := costOf(candidate)
					assert.False(t, want.less(got), "discarded better routed candidate at prefix %d", prefix)
				}
				assertNoRouteThroughUnrelatedNodes(t, geo)
				assertLabelsClear(t, geo)
				assertDistinguishablePaths(t, geo)
			}
			t.Logf("contacts/crossings at budget %d: %+v", maxBudget, previous)
		})
	}
}

func TestArrange_EqualRouteCostsKeepEarliestPrefix(t *testing.T) {
	// Prefix 0 has target order 2,3,4; prefix 1 reorders it to 4,2,3.
	// Both complete routes have zero contacts and six crossings, so the
	// search must retain prefix 0 even though the later layering differs.
	g := numberedLayerGraph(t, 5, [][2]int{{0, 2}, {0, 4}, {1, 2}, {1, 3}})
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	o.CrossingSweeps = 0
	first, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	require.Equal(t, routeCost{contacts: 0, crossings: 6}, costOf(first))

	o.CrossingSweeps = 1
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	later := buildLayering(g, kinds, ranks)
	orderLayers(context.Background(), later, 1)
	require.Equal(t, []string{"4", "2", "3"}, rowNodeIDs(g, later, 1))
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, m, c, o, labels)
	require.NoError(t, err)
	second := newPlacement(g, kinds, ranks, later, sizes, labels, c, o).route()
	require.NoError(t, second.Validate(g))
	require.Equal(t, routeCost{contacts: 0, crossings: 6}, costOf(second))
	// Compare presentation with the same serialized search budget; an options
	// field difference alone would not establish a distinct candidate.
	first.Options = o
	require.NotEqual(t, GeometryDigest(first), GeometryDigest(second))

	selected, err := Arrange(context.Background(), g, m, c, o)
	require.NoError(t, err)
	require.NoError(t, selected.Validate(g))
	assert.Equal(t, GeometryDigest(first), GeometryDigest(selected), "equal-cost search must keep the earliest prefix's geometry")
}

func TestArrange_MarginZeroContainsInkAndMarkers(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "b"})
	o := DefaultOptions()
	o.Margin = 0
	geo := arrangeFor(t, g, o)
	for _, e := range geo.Edges {
		for _, p := range e.Points {
			assert.True(t, geo.Canvas.Contains(Rect{X: p.X - geo.Contract.StrokePx/2, Y: p.Y - geo.Contract.StrokePx/2, W: geo.Contract.StrokePx, H: geo.Contract.StrokePx}))
		}
		p := e.Points[len(e.Points)-1]
		before := e.Points[len(e.Points)-2]
		size := geo.Contract.ArrowPx
		if nearly(p.X, before.X) {
			assert.True(t, geo.Canvas.Contains(Rect{X: p.X - size/2, Y: p.Y - size, W: size, H: size}))
		} else {
			assert.True(t, geo.Canvas.Contains(Rect{X: p.X, Y: p.Y - size/2, W: size, H: size}))
		}
	}
}
