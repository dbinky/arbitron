package layout

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ranksByID(g Graph, ranks []int) map[string]int {
	out := map[string]int{}
	for i, n := range g.Nodes {
		out[n.ID] = ranks[i]
	}
	return out
}

func TestAssignRanks_LinearChainIsOneDownwardColumnOfRanks(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"})
	r := ranksByID(g, assignRanks(g, classifyEdges(g)))
	assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2, "d": 3}, r, "sequential flow never wraps")
}

func TestAssignRanks_DiamondRejoinUsesLongestPath(t *testing.T) {
	// a→b→d, a→c→e→d: d must sit below e (longest path), b and c share rank 1.
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "c"}, [2]string{"b", "d"}, [2]string{"c", "e"}, [2]string{"e", "d"})
	r := ranksByID(g, assignRanks(g, classifyEdges(g)))
	assert.Equal(t, 0, r["a"])
	assert.Equal(t, 1, r["b"])
	assert.Equal(t, 1, r["c"])
	assert.Equal(t, 2, r["e"])
	assert.Equal(t, 3, r["d"])
}

func TestAssignRanks_LoopReturnsAndSelfLoopsDoNotAffectRanks(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"}, [2]string{"b", "b"})
	r := ranksByID(g, assignRanks(g, classifyEdges(g)))
	assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2}, r)
}

func TestAssignRanks_MultipleEntrypointsAndDisconnectedComponents(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "s1", Entrypoint: true}, {ID: "s2", Entrypoint: true}, {ID: "m"}, {ID: "iso"}, {ID: "x"}, {ID: "y"}},
		Edges: []workflow.Edge{{Source: "s1", Target: "m"}, {Source: "s2", Target: "m"}, {Source: "x", Target: "y"}},
	})
	require.NoError(t, err)
	r := ranksByID(g, assignRanks(g, classifyEdges(g)))
	assert.Equal(t, 0, r["s1"])
	assert.Equal(t, 0, r["s2"])
	assert.Equal(t, 1, r["m"])
	assert.Equal(t, 0, r["iso"], "isolated nodes start at the top")
	assert.Equal(t, 0, r["x"])
	assert.Equal(t, 1, r["y"])
}

func TestAssignRanks_LoopReturnTargetsAreAboveTheirSources(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			require.NotEmpty(t, g.Edges, "acceptance samples must exercise edge progression")
			before, err := json.Marshal(g)
			require.NoError(t, err)
			kinds := classifyEdges(g)
			ranks := assignRanks(g, kinds)
			require.Len(t, ranks, len(g.Nodes))
			assert.Equal(t, ranks, assignRanks(g, kinds), "sample ranks must be deterministic")
			for _, e := range g.Edges {
				s, _ := g.NodeIndex(e.Source)
				d, _ := g.NodeIndex(e.Target)
				switch kinds[e.Ordinal] {
				case EdgeForward:
					assert.Greater(t, ranks[d], ranks[s], "forward edge %d (%s→%s) must point down", e.Ordinal, e.Source, e.Target)
				case EdgeLoopReturn:
					assert.Less(t, ranks[d], ranks[s], "loop return %d (%s→%s) must point up", e.Ordinal, e.Source, e.Target)
				case EdgeSelfLoop:
					assert.Equal(t, s, d)
				default:
					t.Errorf("edge %d has unknown kind %q", e.Ordinal, kinds[e.Ordinal])
				}
			}
			after, err := json.Marshal(g)
			require.NoError(t, err)
			assert.JSONEq(t, string(before), string(after), "classification and ranking must not mutate the graph")
		})
	}
}

func TestTopoOrder_IsDeterministicManifestOrderTieBreak(t *testing.T) {
	// Node declaration order is a, c, b (first appearance). Sources a and b are
	// both ready; the queue is seeded in manifest order, so a, b, then c.
	g := edgeGraph(t, [2]string{"a", "c"}, [2]string{"b", "c"})
	order := topoOrder(g, classifyEdges(g))
	assert.Equal(t, []string{"a", "b", "c"}, nodeIDs(g, order))
	assert.Equal(t, order, topoOrder(g, classifyEdges(g)), "same input, same order")
}

func TestTopoOrder_ReadyNodesAppendInEdgeOrder(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "isolated"}},
		Edges: []workflow.Edge{{Source: "a", Target: "c"}, {Source: "a", Target: "b"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "isolated", "c", "b"}, nodeIDs(g, topoOrder(g, classifyEdges(g))))
}

func TestAssignRanks_ParallelOccurrencesDoNotDuplicateNodes(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "a"})
	kinds := classifyEdges(g)
	assert.Equal(t, []string{"a", "b", "c"}, nodeIDs(g, topoOrder(g, kinds)))
	assert.Equal(t, map[string]int{"a": 0, "b": 1, "c": 2}, ranksByID(g, assignRanks(g, kinds)))
}

func TestAssignRanks_IsolatedNode(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "isolated"}}})
	require.NoError(t, err)
	kinds := classifyEdges(g)
	assert.Equal(t, []int{0}, assignRanks(g, kinds))
	assert.Equal(t, []int{0}, topoOrder(g, kinds))
}

func TestClassifyEdges_SamplesForwardSubgraphIsADAG(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			// Kahn's algorithm must visit every node exactly once, before its forward targets.
			kinds := classifyEdges(g)
			order := topoOrder(g, kinds)
			require.Len(t, order, len(g.Nodes), "forward edges must form a DAG")
			positions := make(map[int]int, len(order))
			for pos, node := range order {
				require.GreaterOrEqual(t, node, 0)
				require.Less(t, node, len(g.Nodes))
				_, duplicate := positions[node]
				require.False(t, duplicate, "node %d appears twice", node)
				positions[node] = pos
			}
			for _, edge := range g.Edges {
				if kinds[edge.Ordinal] != EdgeForward {
					continue
				}
				source, _ := g.NodeIndex(edge.Source)
				target, _ := g.NodeIndex(edge.Target)
				assert.Less(t, positions[source], positions[target], "forward edge %d", edge.Ordinal)
			}
		})
	}
}
