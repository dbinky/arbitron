package layout

import (
	"math"
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
