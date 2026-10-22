package layout

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func kindsOf(t *testing.T, g Graph) map[string]EdgeKind {
	t.Helper()
	kinds := classifyEdges(g)
	require.Len(t, kinds, len(g.Edges))
	out := map[string]EdgeKind{}
	for _, e := range g.Edges {
		out[e.Source+">"+e.Target] = kinds[e.Ordinal]
	}
	return out
}

func nodeIDs(g Graph, idx []int) []string {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, g.Nodes[i].ID)
	}
	return out
}

func TestClassifyEdges_LinearHasNoLoops(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"})
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward}, classifyEdges(g))
}

func TestClassifyEdges_SingleLoop(t *testing.T) {
	// Parity with loops.test.ts "single loop": a→b→c, c→b closes the loop.
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "b"})
	k := kindsOf(t, g)
	assert.Equal(t, EdgeForward, k["a>b"])
	assert.Equal(t, EdgeForward, k["b>c"])
	assert.Equal(t, EdgeLoopReturn, k["c>b"])
	assert.Equal(t, []string{"b", "c"}, nodeIDs(g, loopBody(g, classifyEdges(g), 2)))
}

func TestClassifyEdges_SelfLoop(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "b"})
	k := classifyEdges(g)
	assert.Equal(t, EdgeForward, k[0])
	assert.Equal(t, EdgeSelfLoop, k[1])
	assert.Equal(t, []string{"b"}, nodeIDs(g, loopBody(g, k, 1)))
}

func TestClassifyEdges_NestedLoops(t *testing.T) {
	// Parity with loops.test.ts "nested loops".
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"},
		[2]string{"e", "b"}, [2]string{"d", "c"},
	)
	k := classifyEdges(g)
	assert.Equal(t, EdgeLoopReturn, k[4], "e→b is the outer return")
	assert.Equal(t, EdgeLoopReturn, k[5], "d→c is the inner return")
	assert.Equal(t, []string{"b", "c", "d", "e"}, nodeIDs(g, loopBody(g, k, 4)))
	assert.Equal(t, []string{"c", "d"}, nodeIDs(g, loopBody(g, k, 5)))
}

func TestClassifyEdges_ThreeLevelNestingWithSelfLoop(t *testing.T) {
	// Parity with loops.test.ts "three-level nesting".
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"d", "e"}, [2]string{"e", "f"},
		[2]string{"f", "a"}, [2]string{"d", "b"}, [2]string{"c", "c"},
	)
	k := classifyEdges(g)
	assert.Equal(t, EdgeLoopReturn, k[5])
	assert.Equal(t, EdgeLoopReturn, k[6])
	assert.Equal(t, EdgeSelfLoop, k[7])
	assert.Equal(t, []string{"a", "b", "c", "d", "e", "f"}, nodeIDs(g, loopBody(g, k, 5)))
	assert.Equal(t, []string{"b", "c", "d"}, nodeIDs(g, loopBody(g, k, 6)))
}

func TestClassifyEdges_DiamondInsideLoop(t *testing.T) {
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"b", "d"}, [2]string{"c", "e"}, [2]string{"d", "e"},
		[2]string{"e", "b"},
	)
	k := classifyEdges(g)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward, EdgeForward, EdgeForward, EdgeForward, EdgeLoopReturn}, k)
	assert.Equal(t, []string{"b", "c", "d", "e"}, nodeIDs(g, loopBody(g, k, 5)))
}

func TestClassifyEdges_IdenticalParallelBackEdgesAreEachClassified(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "a"})
	k := classifyEdges(g)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeLoopReturn, EdgeLoopReturn}, k,
		"pair-keyed identity would collapse these; ordinal identity keeps both")
}

func TestClassifyEdges_IdenticalParallelForwardEdges(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "b"})
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward}, classifyEdges(g))
}

func TestClassifyEdges_DisconnectedAndIsolatedNodes(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "isolated"}, {ID: "a"}, {ID: "b"}, {ID: "x"}, {ID: "y"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "x", Target: "y"}, {Source: "y", Target: "x"}},
	})
	require.NoError(t, err)
	k := classifyEdges(g)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward, EdgeLoopReturn}, k)
}

func TestClassifyEdges_RootsFollowEdgeSourcesNotNodeOrder(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "z"}, {ID: "a"}},
		Edges: []workflow.Edge{{Source: "a", Target: "z"}, {Source: "z", Target: "a"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeLoopReturn}, classifyEdges(g))
}

func TestClassifyEdges_ChildrenFollowEdgeOrder(t *testing.T) {
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"a", "c"},
		[2]string{"b", "c"}, [2]string{"c", "b"},
	)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward, EdgeForward, EdgeLoopReturn}, classifyEdges(g))
}

func TestClassifyEdges_SharedHeadBodiesDoNotLeak(t *testing.T) {
	g := edgeGraph(t,
		[2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"},
		[2]string{"c", "b"}, [2]string{"d", "b"},
	)
	kinds := classifyEdges(g)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward, EdgeForward, EdgeLoopReturn, EdgeLoopReturn}, kinds)
	assert.Equal(t, []string{"b", "c"}, nodeIDs(g, loopBody(g, kinds, 3)))
	assert.Equal(t, []string{"b", "c", "d"}, nodeIDs(g, loopBody(g, kinds, 4)))
}

func TestClassifyEdges_NoEdges(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "isolated"}}})
	require.NoError(t, err)
	assert.Empty(t, classifyEdges(g))
}

func TestClassifyEdges_RalphLoopHasLoopReturns(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "ralph-loop"))
	require.NoError(t, err)
	returns := 0
	for _, k := range classifyEdges(g) {
		if k == EdgeLoopReturn || k == EdgeSelfLoop {
			returns++
		}
	}
	assert.Greater(t, returns, 0, "ralph-loop is the loop acceptance sample")
}
