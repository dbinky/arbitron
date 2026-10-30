package layout

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimplify_PreservesReversalsAndSmallDisplacement(t *testing.T) {
	assert.Equal(t, []Point{{0, 0}, {0, 10}, {0, 5}, {10, 5}}, simplify([]Point{{0, 0}, {0, 10}, {0, 5}, {10, 5}}))
	assert.Equal(t, []Point{{0, 0}, {0, 10}}, simplify([]Point{{0, 0}, {0, 0}, {0, 5}, {0, 10}}))
	assert.Equal(t, []Point{{0, 0}, {0, 5}, {0.1, 5}, {0.1, 10}}, simplify([]Point{{0, 0}, {0, 5}, {0.1, 5}, {0.1, 10}}))
}

func TestFindCrossings_ProperOnlySortedAndDeduplicated(t *testing.T) {
	routes := []EdgeRoute{{Ordinal: 0, Points: []Point{{0, 10}, {40, 10}}}, {Ordinal: 1, Points: []Point{{20, 0}, {20, 30}}}, {Ordinal: 2, Points: []Point{{20, 20}, {40, 20}}}}
	assert.Equal(t, []Crossing{{Point: Point{20, 10}, Over: 0, Under: 1}}, findCrossings(routes))
	segs := segmentsOf(routes)
	require.Len(t, segs, 3)
	assert.True(t, segs[0].horiz)
	assert.Equal(t, 1, segs[1].ord)
}

func TestRouteCost_LessPrioritizesContactsThenCrossings(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b routeCost
		want bool
	}{
		{"fewer contacts despite more crossings", routeCost{0, 9}, routeCost{1, 0}, true},
		{"more contacts despite fewer crossings", routeCost{1, 0}, routeCost{0, 9}, false},
		{"equal contacts fewer crossings", routeCost{2, 3}, routeCost{2, 4}, true},
		{"equal contacts more crossings", routeCost{2, 4}, routeCost{2, 3}, false},
		{"equal nonzero costs retain earlier candidate", routeCost{2, 3}, routeCost{2, 3}, false},
		{"equal zero costs retain earlier candidate", routeCost{}, routeCost{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.a.less(tc.b))
		})
	}
}

// These arbitrary endpoint columns form the precedence cycle 0->1000->2000->0.
// Small displacements must remain orthogonal, not be dropped as near-straight.
func TestRouteChannel_TransitColumnsBreakCyclesWithoutDroppingHops(t *testing.T) {
	for _, targets := range [][]float64{{1000, 2000, 0}, {0.1, 1000.1, 2000.1}} {
		t.Run("cycle or small delta", func(t *testing.T) {
			g := edgeGraph(t, [2]string{"a", "x"}, [2]string{"b", "y"}, [2]string{"c", "z"})
			c, o := DefaultContract(), DefaultOptions()
			kinds := classifyEdges(g)
			ranks := assignRanks(g, kinds)
			l := buildLayering(g, kinds, ranks)
			labels, err := edgeLabelSizes(g, measured(t, g), c, o)
			require.NoError(t, err)
			sizes, err := nodeSizes(g, kinds, measured(t, g), c, o, labels)
			require.NoError(t, err)
			p := newPlacement(g, kinds, ranks, l, sizes, labels, c, o)
			p.sourceX = []float64{0, 1000, 2000}
			p.targetX = targets
			for _, e := range g.Edges {
				s, _ := g.NodeIndex(e.Source)
				target, _ := g.NodeIndex(e.Target)
				p.x[l.itemOfNode[s]] = p.sourceX[e.Ordinal] - sizes[s].W/2
				p.x[l.itemOfNode[target]] = p.targetX[e.Ordinal] - sizes[target].W/2
			}
			p.left, p.right = -100, 2100
			channels := p.channels()
			require.Len(t, channels[1].hops, 3)
			occupied := []float64{0, 1000, 2000, targets[0], targets[1], targets[2]}
			transits := []float64{}
			for _, h := range channels[1].hops {
				assert.False(t, h.straight)
				assert.Less(t, h.fanout, h.track)
				assert.Less(t, h.track, h.fanin)
				for _, x := range []float64{h.sourceTransit, h.targetTransit} {
					for _, ep := range append(append([]float64(nil), occupied...), transits...) {
						assert.GreaterOrEqual(t, math.Abs(x-ep)+geomEps, routingClearance(c, o))
					}
					transits = append(transits, x)
				}
			}
			geo := p.route()
			require.NoError(t, geo.Validate(g))
			assertDistinguishablePaths(t, geo)
			assertNoRouteThroughUnrelatedNodes(t, geo)
		})
	}
}

func TestRouteChannel_HugeFiniteColumnsTerminate(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"})
	c, o := DefaultContract(), DefaultOptions()
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	labels, err := edgeLabelSizes(g, measured(t, g), c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, measured(t, g), c, o, labels)
	require.NoError(t, err)
	p := newPlacement(g, kinds, ranks, buildLayering(g, kinds, ranks), sizes, labels, c, o)
	p.sourceX[0] = 1e308
	p.targetX[0] = math.Nextafter(1e308, 0)
	done := make(chan []routeChannel, 1)
	go func() { done <- p.channels() }()
	select {
	case channels := <-done:
		h := channels[1].hops[0]
		assert.True(t, finite(h.sourceTransit))
		assert.True(t, finite(h.targetTransit))
		assert.NotEqual(t, h.sx, h.sourceTransit)
		assert.NotEqual(t, h.tx, h.targetTransit)
	case <-time.After(time.Second):
		t.Fatal("finite but sub-ULP clearance must not cause an unbounded search")
	}
}

func TestFanOrder_IsStrictAndTransitiveAcrossDirections(t *testing.T) {
	var hops []*channelHop
	for _, x := range []float64{0, 10, 20} {
		for _, y := range []float64{-10, 5, 30} {
			hops = append(hops, &channelHop{ord: len(hops), sx: x, tx: y})
		}
	}
	for _, source := range []bool{true, false} {
		for _, a := range hops {
			assert.False(t, fanLess(a, a, source))
			for _, b := range hops {
				if fanLess(a, b, source) {
					assert.False(t, fanLess(b, a, source))
					for _, c := range hops {
						if fanLess(b, c, source) {
							assert.True(t, fanLess(a, c, source), "nontransitive fan order: %+v %+v %+v source=%v", a, b, c, source)
						}
					}
				}
			}
		}
	}
}

func TestLoopSide_UsesBodyReachNotUnrelatedComponents(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"}, [2]string{"a", "outside"})
	c, o := DefaultContract(), DefaultOptions()
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	labels, err := edgeLabelSizes(g, measured(t, g), c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, measured(t, g), c, o, labels)
	require.NoError(t, err)
	for _, tc := range []struct {
		body, outside float64
		want          Side
	}{{400, -1000, SideLeft}, {-400, 1000, SideRight}, {100, 1000, SideRight}} {
		l := buildLayering(g, kinds, ranks)
		p := newPlacement(g, kinds, ranks, l, sizes, labels, c, o)
		for _, id := range []string{"a", "c"} {
			n, _ := g.NodeIndex(id)
			p.x[l.itemOfNode[n]] = 100
		}
		n, _ := g.NodeIndex("b")
		p.x[l.itemOfNode[n]] = tc.body
		n, _ = g.NodeIndex("outside")
		p.x[l.itemOfNode[n]] = tc.outside
		p.left = math.Min(tc.body, tc.outside)
		p.right = math.Max(tc.body, tc.outside) + 180
		p.assignLoops()
		assert.Equal(t, tc.want, p.loops[2].side)
	}
}

func TestRouteContacts_BendsTouchesMergedLinesAndOverlappingGaps(t *testing.T) {
	cases := [][]EdgeRoute{
		{{Ordinal: 0, Points: []Point{{0, 0}, {0, 20}, {0, 10}}}},
		{{Ordinal: 0, Points: []Point{{0, 10}, {20, 10}, {20, 20}}}, {Ordinal: 1, Points: []Point{{20, 0}, {20, 30}}}},
		{{Ordinal: 0, Points: []Point{{0, 10}, {40, 10}}}, {Ordinal: 1, Points: []Point{{20, 0}, {20, 10}}}},
		{{Ordinal: 0, Points: []Point{{0, 10}, {40, 10}}}, {Ordinal: 1, Points: []Point{{20, 10}, {60, 10}}}},
		{{Ordinal: 0, Points: []Point{{0, 10}, {40, 10}}}, {Ordinal: 1, Points: []Point{{0, 15}, {40, 15}}}, {Ordinal: 2, Points: []Point{{20, 0}, {20, 30}}}},
	}
	for _, routes := range cases {
		geo := Geometry{Contract: DefaultContract(), Edges: routes}
		assert.Positive(t, costOf(geo).contacts)
	}
	clean := Geometry{Contract: DefaultContract(), Edges: []EdgeRoute{{Ordinal: 0, Points: []Point{{0, 20}, {80, 20}}}, {Ordinal: 1, Points: []Point{{40, 0}, {40, 40}}}}}
	assert.Zero(t, costOf(clean).contacts)
	assert.Equal(t, 1, costOf(clean).crossings)
}

func TestRouteContacts_LabelsNodesAndBoundaryDirection(t *testing.T) {
	c := DefaultContract()
	node := NodeBox{ID: "a", Box: Rect{X: 0, Y: 0, W: 100, H: 100}}
	path := EdgeRoute{Ordinal: 0, Source: "a", Target: "b", Points: []Point{{50, 100}, {50, 150}}, SourceAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{50, 100}}}
	assert.Zero(t, costOf(Geometry{Contract: c, Nodes: []NodeBox{node}, Edges: []EdgeRoute{path}}).contacts)
	reverse := path
	reverse.Points = []Point{{50, 100}, {50, 50}}
	assert.Positive(t, costOf(Geometry{Contract: c, Nodes: []NodeBox{node}, Edges: []EdgeRoute{reverse}}).contacts)
	label := path
	label.Text = &LabelBox{Box: Rect{X: 45, Y: 120, W: 20, H: 20}}
	assert.Positive(t, costOf(Geometry{Contract: c, Nodes: []NodeBox{node}, Edges: []EdgeRoute{label}}).contacts)
	foreign := NodeBox{ID: "other", Box: Rect{X: 40, Y: 120, W: 20, H: 20}}
	assert.Positive(t, costOf(Geometry{Contract: c, Nodes: []NodeBox{node, foreign}, Edges: []EdgeRoute{path}}).contacts)
}

// verticalSpan reports whether one vertical segment of pts runs from above y1
// to below y2 without a jog: a long edge passing dummy items stays straight.
func verticalSpan(pts []Point, y1, y2 float64) bool {
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		if a.X == b.X && math.Min(a.Y, b.Y) < y1 && math.Max(a.Y, b.Y) > y2 {
			return true
		}
	}
	return false
}

func TestRoute_LongEdgeRunsStraightThroughAlignedDummies(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"}, [2]string{"a", "e"})
	geo := arrangeFor(t, g, DefaultOptions())
	long := geo.Edges[4]
	require.Equal(t, EdgeForward, long.Kind)
	assert.True(t, verticalSpan(long.Points, boxOf(geo, "b").Y, boxOf(geo, "d").Bottom()), "one vertical run past b, c and d: %v", long.Points)
	// Source fanout and target fanin keep their lane-separation jog; nothing else bends.
	assert.LessOrEqual(t, len(long.Points)-2, 8, "%v", long.Points)
	assertDistinguishablePaths(t, geo)
	assertNoRouteThroughUnrelatedNodes(t, geo)
}

func TestRoute_TwoRankEdgeKeepsItsFanJogs(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "c"}, [2]string{"b", "d"}, [2]string{"c", "d"})
	geo := arrangeFor(t, g, DefaultOptions())
	for _, e := range geo.Edges {
		// port, fanout, source transit, track, target transit, fanin, port
		assert.Len(t, e.Points, 8, "real fanout and fanin columns still separate lanes: edge %d %v", e.Ordinal, e.Points)
	}
	assertDistinguishablePaths(t, geo)
}

// dummyPlacement arranges a→e beside a four-node spine, so the long edge owns
// one dummy in each of ranks 1..3.
func dummyPlacement(t *testing.T) (*placement, Graph, []int) {
	t.Helper()
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"}, [2]string{"a", "e"})
	c, o := DefaultContract(), DefaultOptions()
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	l := buildLayering(g, kinds, ranks)
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, m, c, o, labels)
	require.NoError(t, err)
	p := newPlacement(g, kinds, ranks, l, sizes, labels, c, o)
	chain := l.chains[4]
	require.Len(t, chain, 5)
	return p, g, chain[1:4]
}

func TestRoute_ShiftedDummyChainMakesOneCleanJog(t *testing.T) {
	p, g, dummies := dummyPlacement(t)
	// Rank 3's dummy is pushed sideways: the chain must jog once, between ranks 2 and 3.
	p.x[dummies[2]] += 40
	geo := p.route()
	require.NoError(t, geo.Validate(g))
	long := geo.Edges[4]
	b, c, d := boxOf(geo, "b"), boxOf(geo, "c"), boxOf(geo, "d")
	assert.True(t, verticalSpan(long.Points, b.Y, c.Bottom()), "aligned ranks 1..2 stay one run: %v", long.Points)
	jogs := 0
	for i := 1; i < len(long.Points); i++ {
		a, z := long.Points[i-1], long.Points[i]
		if a.Y == z.Y && a.Y > c.Bottom() && a.Y < d.Y {
			jogs++
			assert.InDelta(t, 40, math.Abs(z.X-a.X), geomEps, "the jog spans the shift exactly")
		}
	}
	assert.Equal(t, 1, jogs, "one clean jog in the shifted channel: %v", long.Points)
	assertDistinguishablePaths(t, geo)
}

func chainShifts(p *placement, dummies []int) int {
	shifts := 0
	for i := 1; i < len(dummies); i++ {
		if p.itemX(dummies[i]) != p.itemX(dummies[i-1]) {
			shifts++
		}
	}
	return shifts
}

func TestStraightenChains_RealignsADisplacedDummy(t *testing.T) {
	p, _, dummies := dummyPlacement(t)
	want := p.itemX(dummies[0])
	p.x[dummies[2]] += 40
	p.straightenChains()
	for _, d := range dummies {
		assert.Equal(t, want, p.itemX(d), "every dummy returns to the shared column")
	}
}

func TestStraightenChains_MovesTheFewestDummies(t *testing.T) {
	p, _, dummies := dummyPlacement(t)
	want := p.itemX(dummies[1])
	p.x[dummies[0]] -= 200
	p.straightenChains()
	assert.Zero(t, chainShifts(p, dummies))
	assert.Equal(t, want, p.itemX(dummies[0]), "the displaced dummy joins the others, not the reverse")
	for r, row := range p.layers.rows {
		for i := 1; i < len(row); i++ {
			assert.GreaterOrEqual(t, p.x[row[i]]+geomEps, p.x[row[i-1]]+p.widths[row[i-1]]+p.options.NodeSpacing, "row %d keeps order and gap", r)
		}
	}
}

func TestStraightenChains_NeverOverlapsAWedgedNeighbour(t *testing.T) {
	p, _, dummies := dummyPlacement(t)
	r := p.layers.items[dummies[1]].rank
	row := p.layers.rows[r]
	require.Len(t, row, 2)
	// Put the row's node exactly where the shared column would need its slot.
	node := row[0]
	if node == dummies[1] {
		node = row[1]
	}
	target := p.itemX(dummies[0])
	p.x[dummies[1]] += 300
	if row[0] == node {
		p.x[node] = target - p.anchors[dummies[1]] - p.options.NodeSpacing - p.widths[node] + 50
	} else {
		p.x[node] = target - p.anchors[dummies[1]] + p.widths[dummies[1]] + p.options.NodeSpacing - 50
	}
	wedged := p.itemX(dummies[1])
	p.straightenChains()
	for i := 1; i < len(row); i++ {
		assert.GreaterOrEqual(t, p.x[row[i]]+geomEps, p.x[row[i-1]]+p.widths[row[i-1]]+p.options.NodeSpacing)
	}
	// The wedged dummy cannot reach the old column, so the free ones join it.
	assert.Equal(t, wedged, p.itemX(dummies[1]), "the wedged dummy stays put")
	for _, d := range dummies {
		assert.Equal(t, wedged, p.itemX(d), "every dummy shares the wedged column")
	}
}

// Two long edges packed beside a spine whose middle node is wider: the block
// sits against each row's node, so it steps out and back. The inner chain can
// only straighten on the outward column if the outer chain is pushed outward
// too; no real node ever moves.
func TestStraightenChains_PushesPackedDummiesOutward(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"}, [2]string{"a", "e"}, [2]string{"a", "e"})
	c, o := DefaultContract(), DefaultOptions()
	kinds := classifyEdges(g)
	ranks := assignRanks(g, kinds)
	l := buildLayering(g, kinds, ranks)
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, kinds, m, c, o, labels)
	require.NoError(t, err)
	p := newPlacement(g, kinds, ranks, l, sizes, labels, c, o)
	inner, outer := l.chains[4][1:4], l.chains[5][1:4]
	// Lay each row out as [spine node, inner dummy, outer dummy], packed.
	for r := 1; r <= 3; r++ {
		row := l.rows[r]
		node := row[slices.IndexFunc(row, func(id int) bool { return l.items[id].node >= 0 })]
		l.rows[r] = []int{node, inner[r-1], outer[r-1]}
		if r == 2 {
			p.widths[node] += 40 // the wider middle spine node
		}
		p.x[node] = 0
		p.x[inner[r-1]] = p.widths[node] + o.NodeSpacing
		p.x[outer[r-1]] = p.x[inner[r-1]] + p.widths[inner[r-1]] + o.NodeSpacing
	}
	require.Equal(t, 2, chainShifts(p, inner))
	require.Equal(t, 2, chainShifts(p, outer))
	nodes := map[int]float64{}
	for id, it := range l.items {
		if it.node >= 0 {
			nodes[id] = p.x[id]
		}
	}
	p.straightenChains()
	assert.Zero(t, chainShifts(p, inner), "inner chain straight")
	assert.Zero(t, chainShifts(p, outer), "outer chain pushed out and straight")
	for id, x := range nodes {
		assert.Equal(t, x, p.x[id], "real nodes are walls, never pushed")
	}
	for r := 1; r <= 3; r++ {
		row := l.rows[r]
		for i := 1; i < len(row); i++ {
			assert.GreaterOrEqual(t, p.x[row[i]]+geomEps, p.x[row[i-1]]+p.widths[row[i-1]]+o.NodeSpacing, "row %d keeps order and gap", r)
		}
	}
}
