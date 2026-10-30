package layout

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func layeringFor(t *testing.T, g Graph) *layering {
	t.Helper()
	kinds := classifyEdges(g)
	return buildLayering(g, kinds, assignRanks(g, kinds))
}

func rowNodeIDs(g Graph, l *layering, r int) []string {
	var out []string
	for _, id := range l.rows[r] {
		it := l.items[id]
		if it.node >= 0 {
			out = append(out, g.Nodes[it.node].ID)
		} else {
			out = append(out, "~")
		}
	}
	return out
}

func TestBuildLayering_ChainIsOneItemPerRow(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"})
	l := layeringFor(t, g)
	require.Len(t, l.rows, 3)
	for r := 0; r < 3; r++ {
		assert.Len(t, l.rows[r], 1, "a linear flow is a single column")
	}
	assert.Equal(t, []int{l.itemOfNode[0], l.itemOfNode[1]}, l.chains[0])
}

func TestBuildLayering_LongEdgeGetsADummyInEveryIntermediateRow(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"a", "d"})
	l := layeringFor(t, g)
	chain := l.chains[3]
	require.Len(t, chain, 4, "source, two dummies, target")
	assert.Equal(t, l.itemOfNode[0], chain[0])
	assert.Equal(t, l.itemOfNode[3], chain[3])
	for r, id := range chain[1:3] {
		assert.Equal(t, -1, l.items[id].node)
		assert.Equal(t, 3, l.items[id].edge, "dummy remembers its edge ordinal")
		assert.Equal(t, r+1, l.items[id].rank)
	}
	assert.Equal(t, []string{"b", "~"}, rowNodeIDs(g, l, 1))
	assert.Equal(t, []string{"c", "~"}, rowNodeIDs(g, l, 2))
}

func TestBuildLayering_ParallelLongEdgesGetSeparateDummies(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"a", "c"}, [2]string{"a", "c"})
	l := layeringFor(t, g)
	require.Len(t, l.chains[2], 3)
	require.Len(t, l.chains[3], 3)
	assert.NotEqual(t, l.chains[2][1], l.chains[3][1], "identical parallel shortcuts never share a column")
	assert.Len(t, l.rows[1], 3)
}

func TestBuildLayering_LoopReturnsAndSelfLoopsHaveNoChains(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "b"})
	l := layeringFor(t, g)
	assert.NotContains(t, l.chains, 1)
	assert.NotContains(t, l.chains, 2)
}

func TestBuildLayering_UpDownAreSymmetric(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"a", "c"})
	l := layeringFor(t, g)
	require.Len(t, l.itemOfNode, len(g.Nodes))
	a := l.itemOfNode[0]
	assert.Len(t, l.down[a], 3, "one entry per edge occurrence, parallels included")
	for _, d := range l.down[a] {
		assert.Contains(t, l.up[d], a)
	}
	assert.Equal(t, []int{a, a}, l.up[l.itemOfNode[1]], "parallel occurrences are also retained upstream")
}

func TestLayering_CrossingsCountsTwistedSegments(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "d"}, [2]string{"b", "c"})
	l := layeringFor(t, g)
	require.Len(t, l.rows, 2)
	assert.Equal(t, []string{"a", "b"}, rowNodeIDs(g, l, 0))
	assert.Equal(t, []string{"d", "c"}, rowNodeIDs(g, l, 1), "nodes start in manifest order")
	assert.Equal(t, 0, l.crossings())
	l.rows[1][0], l.rows[1][1] = l.rows[1][1], l.rows[1][0]
	assert.Equal(t, 1, l.crossings(), "reversing the target row twists the two segments")
	for r, row := range l.rows {
		for p, id := range row {
			assert.Equal(t, p, l.positions()[id], "position in row %d", r)
		}
	}

	g2 := edgeGraph(t, [2]string{"a", "c"}, [2]string{"b", "d"}, [2]string{"a", "d"}, [2]string{"b", "c"})
	assert.Equal(t, 1, layeringFor(t, g2).crossings(), "K2,2 always has exactly one crossing")
}

func TestLayering_CrossingsPreserveParallelMultiplicity(t *testing.T) {
	g := numberedLayerGraph(t, 4, [][2]int{{0, 3}, {0, 3}, {1, 2}, {1, 2}, {1, 2}})
	l := layeringFor(t, g)
	assert.Equal(t, 6, l.crossings(), "each of two segments crosses each of three parallel segments")
}

func TestOrderLayers_RemovesAvoidableCrossing(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "x"}, [2]string{"b", "y"}, [2]string{"a", "y2"}, [2]string{"b", "x2"},
		[2]string{"y", "z"}, [2]string{"x", "z"})
	l := layeringFor(t, g)
	before := l.crossings()
	require.Positive(t, before, "fixture must require a real reorder")
	orderLayers(context.Background(), l, DefaultOptions().CrossingSweeps)
	assert.LessOrEqual(t, l.crossings(), before)
	assert.Zero(t, l.crossings(), "the bounded search must find this planar layered order")
}

func copyLayerRows(rows [][]int) [][]int {
	out := make([][]int, len(rows))
	for r := range rows {
		out[r] = append([]int(nil), rows[r]...)
	}
	return out
}

func TestOrderLayers_ZeroBudgetKeepsManifestOrder(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "d"}, [2]string{"b", "c"}, [2]string{"a", "c"})
	for _, budget := range []int{0, -1} {
		l := layeringFor(t, g)
		want := copyLayerRows(l.rows)
		orderLayers(context.Background(), l, budget)
		assert.Equal(t, want, l.rows)
	}
}

func TestOrderLayers_IsDeterministic(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "legacy-feature-pipeline"))
	require.NoError(t, err)
	a, b := layeringFor(t, g), layeringFor(t, g)
	orderLayers(context.Background(), a, 8)
	orderLayers(context.Background(), b, 8)
	assert.Equal(t, a.rows, b.rows)
}

func TestOrderLayers_NeverWorseThanInitialOnSamples(t *testing.T) {
	for _, name := range []string{"legacy-feature-pipeline", "pr-review", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			l := layeringFor(t, g)
			before := l.crossings()
			orderLayers(context.Background(), l, 8)
			assert.LessOrEqual(t, l.crossings(), before)
			assertLayerRanks(t, l)
		})
	}
}

func assertLayerRanks(t *testing.T, l *layering) {
	t.Helper()
	seen := make(map[int]bool)
	for r, row := range l.rows {
		for _, id := range row {
			require.False(t, seen[id], "duplicate item %d", id)
			seen[id] = true
			require.Equal(t, l.items[id].rank, r, "item %d moved rank", id)
		}
	}
	require.Len(t, seen, len(l.items))
}

func TestOrderLayers_LargerBudgetIsNeverWorse(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "legacy-feature-pipeline"))
	require.NoError(t, err)
	small, large := layeringFor(t, g), layeringFor(t, g)
	orderLayers(context.Background(), small, 2)
	orderLayers(context.Background(), large, 16)
	assert.LessOrEqual(t, large.crossings(), small.crossings())
}

// numberedLayerGraph fixes manifest node order independently of edge order.
func numberedLayerGraph(t *testing.T, count int, pairs [][2]int) Graph {
	t.Helper()
	wf := &workflow.Workflow{}
	for n := 0; n < count; n++ {
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: fmt.Sprint(n)})
	}
	for _, p := range pairs {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: fmt.Sprint(p[0]), Target: fmt.Sprint(p[1])})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	return g
}

func TestOrderLayers_EveryBudgetKeepsEarlierTranspositionCandidate(t *testing.T) {
	// A final-only transposition chooses 6 crossings at budget 1, but 9 at 2:
	// its later barycentre winner discards an earlier, better transposed order.
	g := numberedLayerGraph(t, 11, [][2]int{
		{0, 4}, {0, 6}, {1, 4}, {1, 5}, {1, 6}, {2, 3}, {2, 6},
		{3, 7}, {3, 8}, {4, 7}, {4, 8}, {4, 9}, {5, 10}, {6, 8}, {6, 9}, {6, 10},
	})
	one := layeringFor(t, g)
	before := one.crossings()
	orderLayers(context.Background(), one, 1)
	// The initial and first barycentre orders both have 11 crossings; a
	// strict improvement requires evaluating the transposed trajectory.
	require.Less(t, one.crossings(), before, "one prefix must include transposition, not just barycentres")
	previous := one.crossings()
	for budget := 2; budget <= 8; budget++ {
		l := layeringFor(t, g)
		orderLayers(context.Background(), l, budget)
		assert.LessOrEqual(t, l.crossings(), previous, "budget %d loses an earlier candidate", budget)
		previous = l.crossings()
	}
	assert.Less(t, previous, one.crossings(), "this fixture must consume additional search budget")
}

func TestSortRowBy_ParallelOccurrencesWeightTheBarycentre(t *testing.T) {
	g := numberedLayerGraph(t, 5, [][2]int{{0, 3}, {2, 3}, {2, 3}, {1, 4}})
	l := layeringFor(t, g)
	// Target 3 has mean (0+2+2)/3; target 4 has mean 1. Deduplicating
	// neighbours would tie them and incorrectly retain their initial order.
	l.sortRowBy(1, l.up)
	assert.Equal(t, []string{"4", "3"}, rowNodeIDs(g, l, 1))
}

func TestSortRowBy_TiesAndIsolatesKeepCurrentOrder(t *testing.T) {
	g := numberedLayerGraph(t, 5, [][2]int{{0, 3}, {2, 3}, {1, 4}})
	l := layeringFor(t, g)
	want := copyLayerRows(l.rows)
	l.sortRowBy(1, l.up)
	assert.Equal(t, want, l.rows, "equal means retain current position")

	g = numberedLayerGraph(t, 4, nil)
	l = layeringFor(t, g)
	l.rows[0][0], l.rows[0][3] = l.rows[0][3], l.rows[0][0]
	want = copyLayerRows(l.rows)
	l.sortRowBy(0, l.up)
	l.sortRowBy(0, l.down)
	assert.Equal(t, want, l.rows, "no-neighbour rows retain current, not item-id, order")
}

func TestOrderLayers_ExhaustiveSmallGraphs(t *testing.T) {
	// All adjacency matrices for 2..4 nodes on either side (at most 9
	// possible edges), including empty graphs and isolated nodes. Repeat
	// with parallel occurrences and with a backbone plus long-edge dummies.
	for left := 2; left <= 4; left++ {
		for right := 2; right <= 4; right++ {
			if left*right > 9 {
				continue
			}
			t.Run(fmt.Sprintf("%dx%d", left, right), func(t *testing.T) {
				for mask := 0; mask < 1<<(left*right); mask++ {
					var pairs [][2]int
					for a := 0; a < left; a++ {
						for b := 0; b < right; b++ {
							if mask&(1<<(a*right+b)) != 0 {
								pairs = append(pairs, [2]int{a, left + b})
							}
						}
					}
					for variant := 0; variant < 3; variant++ {
						edges := append([][2]int(nil), pairs...)
						count := left + right
						if variant == 1 && len(pairs) > 0 {
							edges = append(edges, pairs[0], pairs[0])
						}
						if variant == 2 {
							// 0 -> extra -> target and two 0 -> target
							// shortcuts guarantee two distinct dummy slots.
							edges = append(edges, [2]int{0, count}, [2]int{count, left}, [2]int{0, left}, [2]int{0, left})
							count++
						}
						g := numberedLayerGraph(t, count, edges)
						previous := layeringFor(t, g).crossings()
						for budget := 0; budget <= 4; budget++ {
							l := layeringFor(t, g)
							wantRows := copyLayerRows(l.rows)
							// Capture metadata independently; ordering may only mutate rows.
							want := layeringFor(t, g)
							orderLayers(context.Background(), l, budget)
							require.LessOrEqual(t, l.crossings(), previous, "mask %d variant %d budget %d", mask, variant, budget)
							previous = l.crossings()
							assertLayerRanks(t, l)
							if budget == 0 {
								require.Equal(t, wantRows, l.rows)
							}
							for r, row := range wantRows {
								isolated := true
								for _, id := range row {
									isolated = isolated && len(l.up[id]) == 0 && len(l.down[id]) == 0
								}
								if isolated {
									require.Equal(t, row, l.rows[r], "no-neighbour row must not reorder")
								}
							}
							l.rows = wantRows
							require.Equal(t, want, l, "items, chains and adjacency must remain unchanged")
						}
					}
				}
			})
		}
	}
}

// A cancelled context stops crossing reduction with ctx.Err() instead of
// finishing the O(items·E²)-per-pass search.
func TestOrderLayers_StopsOnCancelledContext(t *testing.T) {
	g := numberedLayerGraph(t, 4, [][2]int{{0, 3}, {0, 3}, {1, 2}, {1, 2}, {1, 2}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := layeringFor(t, g)
	require.Positive(t, l.crossings(), "fixture must need search")
	require.ErrorIs(t, orderLayers(ctx, l, DefaultOptions().CrossingSweeps), context.Canceled)
	require.ErrorIs(t, layeringFor(t, g).transpose(ctx, 4), context.Canceled)
	require.NoError(t, orderLayers(context.Background(), layeringFor(t, g), DefaultOptions().CrossingSweeps))
}
