# Workflow Auto-Arrangement — Phase 1 Task 3: Edge classification (loop returns) and top-to-bottom ranks

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Classify every edge occurrence as forward, loop return or self loop with the dashboard's DFS/back-edge semantics, compute loop bodies for gutter-side choice, and assign longest-path ranks so every forward edge points strictly downward and sequential flow never wraps into columns.

**Architecture:** `domain/layout/loops.go` ports the interpretation of `dashboard/src/graph/loops.ts:findBackEdges` (iterative DFS, roots in edge-source order then the remaining nodes, a child on the DFS stack closes a cycle) but classifies by edge **ordinal**, so identical parallel edges are each classified and routed. `domain/layout/rank.go` runs Kahn's algorithm over forward edges only. Both are unexported pure functions consumed by `Arrange` (P1T6).

**Tech Stack:** Go 1.25.5, testify; sample manifests under `samples/`.

**Spec / Design:** `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §2 Arranger ("Layered top-to-bottom ranking excludes back-edges using the DFS/back-edge semantics in `loops.ts:detectLoops`. Port its interpretation, not its pair-keyed edge identity").

---

### Task 3.1: Shared internal test helper `loadSample`

**Files:**
- Create: `domain/layout/testhelpers_test.go`

**Interfaces:**
- Produces (internal test helper, `package layout`): `func loadSample(t *testing.T, name string) *workflow.Workflow` — reads `../../samples/<name>/workflow.json`. P1T4 appends `fakeMeasurer` to this same file.

- [x] **Step 1: Create the helper**

Create `domain/layout/testhelpers_test.go`:

```go
package layout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// loadSample reads a bundled sample manifest (samples/<name>/workflow.json),
// the spec's acceptance examples (feature-pipeline, pr-review, ralph-loop).
func loadSample(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", name, "workflow.json"))
	require.NoError(t, err, "sample %s must exist", name)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	return &wf
}

// edgeGraph builds a Graph from "src>dst" pairs; nodes are declared in first
// appearance order (sources and targets), like loops.test.ts fixtures.
func edgeGraph(t *testing.T, pairs ...[2]string) Graph {
	t.Helper()
	wf := &workflow.Workflow{}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeScript})
		}
	}
	for _, p := range pairs {
		add(p[0])
		add(p[1])
		wf.Edges = append(wf.Edges, workflow.Edge{Source: p[0], Target: p[1]})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	return g
}
```

(No run/commit yet — it is committed with Task 3.2's commit.)

---

### Task 3.2: `classifyEdges` and `loopBody`

**Files:**
- Create: `domain/layout/loops.go`
- Test:   `domain/layout/loops_test.go`

**Interfaces:**
- Consumes: `Graph`, `GraphEdge`, `(Graph).NodeIndex` (P1T1); `EdgeKind`, `EdgeForward`, `EdgeLoopReturn`, `EdgeSelfLoop` (P1T2 `geometry.go`).
- Produces (unexported, used by `Arrange` in P1T6 and by `rank.go`):
  ```go
  func classifyEdges(g Graph) []EdgeKind                 // indexed by edge ordinal
  func loopBody(g Graph, kinds []EdgeKind, ordinal int) []int // node indexes of the loop closed by a loop-return ordinal, manifest order
  ```

- [x] **Step 1: Write the failing test**

Create `domain/layout/loops_test.go`:

```go
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
	for _, k := range classifyEdges(g) {
		assert.Equal(t, EdgeForward, k)
	}
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
	assert.Equal(t, EdgeLoopReturn, k[5])
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
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"x", "y"}, [2]string{"y", "x"})
	k := classifyEdges(g)
	assert.Equal(t, []EdgeKind{EdgeForward, EdgeForward, EdgeLoopReturn}, k)
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
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run TestClassifyEdges -v`
Expected: build failure `undefined: classifyEdges`, `undefined: loopBody`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/loops.go`:

```go
package layout

// classifyEdges labels every edge occurrence (indexed by ordinal) using the
// dashboard's DFS back-edge semantics (dashboard/src/graph/loops.ts
// findBackEdges): DFS roots are edge sources in edge order, then every
// remaining node in manifest order; children are visited in edge order; an
// edge to the node itself is a self loop and an edge to a node still on the
// DFS stack closes a cycle (loop return). Unlike the dashboard, identity is the
// edge ordinal, so identical parallel edges are each classified.
func classifyEdges(g Graph) []EdgeKind {
	n := len(g.Nodes)
	kinds := make([]EdgeKind, len(g.Edges))
	adj := make([][]int, n)
	for _, e := range g.Edges {
		kinds[e.Ordinal] = EdgeForward
		if e.Source == e.Target {
			kinds[e.Ordinal] = EdgeSelfLoop
		}
		s, _ := g.NodeIndex(e.Source)
		adj[s] = append(adj[s], e.Ordinal)
	}

	roots := make([]int, 0, n)
	isRoot := make([]bool, n)
	for _, e := range g.Edges {
		s, _ := g.NodeIndex(e.Source)
		if !isRoot[s] {
			isRoot[s] = true
			roots = append(roots, s)
		}
	}
	for i := 0; i < n; i++ {
		if !isRoot[i] {
			isRoot[i] = true
			roots = append(roots, i)
		}
	}

	type frame struct{ node, next int }
	visited := make([]bool, n)
	onStack := make([]bool, n)
	for _, root := range roots {
		if visited[root] {
			continue
		}
		visited[root], onStack[root] = true, true
		stack := []frame{{node: root}}
		for len(stack) > 0 {
			top := len(stack) - 1
			node := stack[top].node
			if stack[top].next >= len(adj[node]) {
				onStack[node] = false
				stack = stack[:top]
				continue
			}
			ord := adj[node][stack[top].next]
			stack[top].next++
			child, _ := g.NodeIndex(g.Edges[ord].Target)
			switch {
			case child == node:
				// self loop, already classified
			case onStack[child]:
				kinds[ord] = EdgeLoopReturn
			case !visited[child]:
				visited[child], onStack[child] = true, true
				stack = append(stack, frame{node: child})
			}
		}
	}
	return kinds
}

// loopBody returns, in manifest order, the node indexes of the loop closed by
// the loop-return (or self-loop) edge ordinal: its head (target), its tail
// (source) and every node that is both forward-reachable from the head and
// forward-reaches the tail. Only forward edges are followed, so one loop's body
// cannot leak into another through a shared head (as in loops.ts).
func loopBody(g Graph, kinds []EdgeKind, ordinal int) []int {
	e := g.Edges[ordinal]
	head, _ := g.NodeIndex(e.Target)
	tail, _ := g.NodeIndex(e.Source)
	if head == tail {
		return []int{head}
	}
	n := len(g.Nodes)
	fwd := make([][]int, n)
	rev := make([][]int, n)
	for _, fe := range g.Edges {
		if kinds[fe.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(fe.Source)
		t, _ := g.NodeIndex(fe.Target)
		fwd[s] = append(fwd[s], t)
		rev[t] = append(rev[t], s)
	}
	down := reach(head, fwd, n)
	up := reach(tail, rev, n)
	body := make([]int, 0)
	for i := 0; i < n; i++ {
		if i == head || i == tail || (down[i] && up[i]) {
			body = append(body, i)
		}
	}
	return body
}

// reach is a BFS over adjacency lists; it marks start and everything reachable.
func reach(start int, adj [][]int, n int) []bool {
	seen := make([]bool, n)
	seen[start] = true
	queue := []int{start}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range adj[v] {
			if !seen[w] {
				seen[w] = true
				queue = append(queue, w)
			}
		}
	}
	return seen
}
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run TestClassifyEdges -v`
Expected: `--- PASS` for all 11 `TestClassifyEdges_*` tests; `ok  	github.com/arbitron/arbitron/domain/layout`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/   # expect no output
git add domain/layout/testhelpers_test.go domain/layout/loops.go domain/layout/loops_test.go
git commit -m "feat(layout): classify loop returns per edge occurrence (DFS parity with loops.ts)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3.3: Longest-path ranks (`assignRanks`) and forward topological order

**Files:**
- Create: `domain/layout/rank.go`
- Test:   `domain/layout/rank_test.go`

**Interfaces:**
- Consumes: `classifyEdges` (Task 3.2).
- Produces (unexported, used by P1T5/P1T6):
  ```go
  func topoOrder(g Graph, kinds []EdgeKind) []int // forward-edge topological order (Kahn, manifest-order queue)
  func assignRanks(g Graph, kinds []EdgeKind) []int // rank per node index; forward edges strictly increase rank
  ```

- [x] **Step 1: Write the failing test**

Create `domain/layout/rank_test.go`:

```go
package layout

import (
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
			kinds := classifyEdges(g)
			ranks := assignRanks(g, kinds)
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
				}
			}
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

func TestClassifyEdges_SamplesForwardSubgraphIsADAG(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			// Kahn's algorithm visits every node only when forward edges are acyclic.
			assert.Len(t, topoOrder(g, classifyEdges(g)), len(g.Nodes), "forward edges must form a DAG")
		})
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestAssignRanks|TestTopoOrder|TestClassifyEdges' -v`
Expected: build failure `undefined: assignRanks`, `undefined: topoOrder`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/rank.go`:

```go
package layout

// topoOrder returns node indexes in a topological order of the forward-edge
// subgraph (Kahn's algorithm, ready queue seeded in manifest order and appended in
// manifest edge order, so the order is deterministic). classifyEdges guarantees the forward
// subgraph is acyclic, so every node appears exactly once.
func topoOrder(g Graph, kinds []EdgeKind) []int {
	n := len(g.Nodes)
	indeg := make([]int, n)
	out := make([][]int, n)
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		out[s] = append(out[s], t)
		indeg[t]++
	}
	order := make([]int, 0, n)
	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		order = append(order, v)
		for _, t := range out[v] {
			indeg[t]--
			if indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	return order
}

// assignRanks gives every node its longest-path distance from a source over
// forward edges. Loop returns and self loops are excluded, so every forward
// edge points to a strictly greater rank (continuous downward progression) and
// every loop return points to a strictly smaller one.
func assignRanks(g Graph, kinds []EdgeKind) []int {
	ranks := make([]int, len(g.Nodes))
	out := make([][]int, len(g.Nodes))
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		out[s] = append(out[s], t)
	}
	for _, v := range topoOrder(g, kinds) {
		for _, t := range out[v] {
			if ranks[v]+1 > ranks[t] {
				ranks[t] = ranks[v] + 1
			}
		}
	}
	return ranks
}
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestAssignRanks|TestTopoOrder|TestClassifyEdges' -v`
Expected: all `TestClassifyEdges_*` (12 tests incl. the 4-sample DAG test), `TestAssignRanks_*` (5 tests incl. 3 sample subtests) and `TestTopoOrder_IsDeterministicManifestOrderTieBreak` `--- PASS`; `ok  	github.com/arbitron/arbitron/domain/layout`.

Run the whole package to be sure nothing else broke: `go test ./domain/layout/`
Expected: `ok  	github.com/arbitron/arbitron/domain/layout`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/   # expect no output
git add domain/layout/rank.go domain/layout/rank_test.go
git commit -m "feat(layout): longest-path top-to-bottom ranks over forward edges" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Execution evidence

- Task 3.1: added the shared sample-manifest loader and validated Graph fixture builder.
- Task 3.2 RED: observed missing-symbol failures, then behavioral failures against compiling all-forward classification / empty-body stubs (including the real ralph-loop sample). GREEN: all classification tests pass. First planned commit: `be08bbd`.
- Task 3.3 RED: observed missing-symbol failures, then behavioral failures against compiling zero-rank / empty-order stubs, including sample edge progression. GREEN: all prescribed focused tests and the whole package pass.
- Self-review, in order: (1) verified the exact internal interfaces, ordinal identity, DFS semantics and Kahn/longest-path slice; no additional API or arrangement policy. (2) Strengthened non-vacuous classification, actual isolated-node coverage, edge-source root order, child order, shared-head bodies, parallel occurrences, deterministic sample ranks, graph immutability and complete/unique/topological sample ordering. Temporary manifest-root / visited-instead-of-on-stack mutations failed the root/diamond assertions; rank-relaxation and LIFO mutations failed the rank/order assertions. Restored all mutations. Tests use the real `BuildGraph` and bundled manifests and assert algorithm outputs; they are not production integration tests. (3) Reviewed neighboring Go conventions and formatting; no extra dependency, option or dead stub remains.
- Final verification: `go test ./domain/layout/ -run 'TestAssignRanks|TestTopoOrder|TestClassifyEdges' -v`, `go test ./domain/layout/`, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` (empty) and `git diff --check` pass.
- Production completion remains pending the assigned Arrange consumer (P1T6), Coordinator (P1T9), real saved-manifest producer/worker/composition-root chain (P2T2–P2T9), and rendered acceptance (P3T7). This algorithm task does not claim those future wiring or sink verifications have run.

## Wired-and-fed

- **Data source:** `Graph` from `BuildGraph` (P1T1) over the captured saved manifest (P2T2 `RevisionStamper` → P2T5 `Requester` → P2T6 row → P2T8 worker).
- **Consumers / wiring:** `classifyEdges`, `loopBody`, `assignRanks` and `topoOrder` are internal stages of `Arrange` (P1T6), reached in production only through `Coordinator.Run` (P1T9) constructed in `cmd/arbitron/layout_wiring.go:newLayoutStack` (P2T9).
- **Provenance probe:** `TestAssignRanks_LoopReturnTargetsAreAboveTheirSources` runs against the real acceptance samples; P1T7's `samples_geometry_test.go` and P3T7's real-browser acceptance test then assert the same downward progression on the produced geometry and rendered result.
