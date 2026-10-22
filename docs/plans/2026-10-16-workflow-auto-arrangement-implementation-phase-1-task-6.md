# Workflow Auto-Arrangement — Phase 1 Task 6: Placement and orthogonal routing (`Arrange`)

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Produce one complete candidate `Geometry` from a graph, real text measurements, the renderer contract and an `Options` value: continuous top-to-bottom rows, branches beside the flow, long edges (shortcuts/rejoins) in reserved columns, loop returns in distinct side-gutter lanes, self loops on the node's right side, every edge occurrence with its own attachments and tracks, branch labels beside their own edge, and every unavoidable crossing recorded for gap treatment.

**Architecture:** Three files in `domain/layout`:
- `place.go` — horizontal placement (barycentre alignment solved per row by an isotonic "pool adjacent violators" pack that keeps order and spacing), loop-return gutter side (port of `loopEdgeRouting.ts:chooseLoopSide`'s open-side idea) and nested lane assignment, attachment ports, and vertical row/channel layout.
- `route.go` — channel "hops", cycle-safe transit-column routing that prevents collinear overlaps and keeps nested loops distinct, polyline construction, crossing detection.
- `arrange.go` — `Arrange` orchestration, global normalisation, rounding to 1/100 px, error sentinels.
Routes are orthogonal; the space between two rows ("channel") holds the branch-label, source-fanout, main-track and target-fanin bands. Nodes never sit inside channels, gutters are outside every row and long edges pass through their own dummy columns, so a route cannot pass through an unrelated node by construction — P1T7's preflight then verifies it.

**Tech Stack:** Go 1.25.5 (`math`, `sort`, built-in `min`/`max`), testify.

**Spec / Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` ("Present continuous downward flow… Place branches beside the flow and keep loop returns visually distinct", acceptance criteria); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §2 Arranger ("Orthogonal routing avoids unrelated node boxes, separates parallel lanes, and reserves side channels for loop returns"; "never mutates the manifest or preserves manual coordinates").

---

### Task 6.1: Row packing primitive (`packRow`)

**Files:**
- Create: `domain/layout/place.go` (first part: `packRow`)
- Test:   `domain/layout/arrange_test.go` (first part)

**Interfaces:**
- Produces (internal): `func packRow(widths, anchors, desired []float64, gap float64) []float64` — returns each item's left x keeping input order, at least `gap` apart, minimising squared distance between each item's anchor (`left + anchors[i]`) and `desired[i]`.

- [x] **Step 1: Write the failing test**

Create `domain/layout/arrange_test.go`:

```go
package layout

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
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
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run TestPackRow -v`
Expected: build failure `undefined: packRow`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/place.go`:

```go
package layout

import (
	"math"
	"sort"
)

// packRow places a row's items left to right in their given order with at
// least gap between neighbours, minimising Σ(left_i + anchors[i] − desired[i])².
// It is the classic pool-adjacent-violators solution: items start as singleton
// blocks at their desired position; a block that would overlap its left
// neighbour merges with it and the merged block moves to the mean of its
// members' desired offsets.
func packRow(widths, anchors, desired []float64, gap float64) []float64 {
	n := len(widths)
	type block struct {
		start, end int
		sum        float64 // Σ(desiredLeft_j − off_j)
		count      int
		x          float64 // left of the block's first item
	}
	off := make([]float64, n)
	blocks := make([]block, 0, n)
	for i := 0; i < n; i++ {
		dl := desired[i] - anchors[i]
		blocks = append(blocks, block{start: i, end: i, sum: dl, count: 1, x: dl})
		for len(blocks) > 1 {
			last := blocks[len(blocks)-1]
			prev := &blocks[len(blocks)-2]
			prevRight := prev.x + off[prev.end] + widths[prev.end]
			if prevRight+gap <= last.x {
				break
			}
			shift := off[prev.end] + widths[prev.end] + gap
			for j := last.start; j <= last.end; j++ {
				off[j] += shift
			}
			prev.sum += last.sum - float64(last.count)*shift
			prev.count += last.count
			prev.end = last.end
			prev.x = prev.sum / float64(prev.count)
			blocks = blocks[:len(blocks)-1]
		}
	}
	xs := make([]float64, n)
	for _, b := range blocks {
		for j := b.start; j <= b.end; j++ {
			xs[j] = b.x + off[j]
		}
	}
	return xs
}

var _ = math.Abs
var _ = sort.SliceStable
```

(The two `var _` lines keep imports used until Task 6.2 appends the rest of `place.go`; Task 6.2 deletes them.)

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run TestPackRow -v`
Expected: `--- PASS: TestPackRow_KeepsOrderAndGap`, `--- PASS: TestPackRow_NonConflictingItemsSitExactlyOnTheirDesire`, `--- PASS: TestPackRow_SingleItem`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/place.go domain/layout/arrange_test.go
git commit -m "feat(layout): order-preserving row packing for aligned placement" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6.2: `Arrange` — placement, ports, tracks, routes, crossings

**Files:**
- Modify: `domain/layout/place.go` (delete the two `var _` lines; append everything below)
- Create: `domain/layout/route.go`
- Create: `domain/layout/arrange.go`
- Test:   `domain/layout/arrange_test.go` (append)

**Interfaces:**
- Consumes: `Graph`, `BuildGraph` (P1T1); `Geometry` and friends, `Rect`, `FormatV1`, `Validate`, `GeometryDigest`, `samePoint`, `nearly` (P1T2); `classifyEdges`, `loopBody`, `assignRanks` (P1T3); `Measurements`, `labelSize`, `nodeSize`, `edgeLabelSizes`, `nodeSizes`, `Options`, `DefaultOptions` (P1T4); `layering`, `buildLayering`, `orderLayers` (P1T5); test helpers `newFakeMeasurer`, `loadSample`, `edgeGraph` (P1T3/P1T4).
- Produces:
  ```go
  func Arrange(g Graph, m Measurements, c RendererContract, o Options) (Geometry, error)
  var ErrContractMismatch = errors.New("layout: measurements were taken with a different renderer contract")
  var ErrFontsNotReady = errors.New("layout: renderer fonts were not ready when measuring")
  ```

Routing rules implemented (each is asserted by a test below):
1. Rows are top-aligned at `rowTop[rank]`; forward edges leave a node's **bottom** and enter the target's **top**, so their last segment always points down.
2. Bottom ports of a node are ordered: left-gutter loop exits (inner lane leftmost), forward exits by next-hop x, right-gutter loop exits (outer lane first, inner rightmost); top ports likewise. Each bottom slot is `PortSpacing (+ LabelGap + label width when labelled)`, each top slot `PortSpacing`, centred on the node.
3. Channel `ch` (between row `ch` and `ch+1`; `-1` is above row 0) contains the label band for edges leaving row `ch`, then tracks `trackTop + t·LaneSeparation`. Each non-straight hop gets a track; two hops share a track only if their horizontal intervals are `LaneSeparation` apart. Conflicting endpoint columns use separate fanout/main/fanin bands with transit columns; no precedence cycle is broken by dropping a constraint. Nested loop exits/entries retain their lane order.
4. Loop returns: exit hop to the lane x in channel `rank(source)`, vertical in the gutter, entry hop in channel `rank(target)−1`. Lanes per side: shortest span first, first lane whose channel span does not overlap.
5. Self loops: the k-th (0 = innermost) of n self loops on a node leaves its right side at `y + H·(n−k)/(2n+1)`, runs out to its cumulative reserved lane (each labelled strip contributes its width and 2·LabelGap), and re-enters at `y + H·(n+1+k)/(2n+1)`.
6. Branch label boxes sit beside their own source stub in the band below the row, with LabelGap; each self-loop label has its own reserved strip beside that loop exit. No label covers a path.
7. A crossing is every proper intersection between a horizontal segment of one edge and a vertical segment of another; `Over` is the horizontal edge, `Under` the vertical one (drawn with a gap).
8. Coordinates are shifted so the leftmost element is at `Margin`, rounded to 0.01 px; canvas = bounding box + `Margin`.

- [x] **Step 1: Write the failing tests**

Append to `domain/layout/arrange_test.go` (add imports `"context"`, `"errors"`, `"reflect"`, `"strings"`, `"github.com/arbitron/arbitron/domain/workflow"`, `"github.com/stretchr/testify/require"`):

```go
func arrangeFor(t *testing.T, g Graph, o Options) Geometry {
	t.Helper()
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(g, m, c, o)
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

	_, err = Arrange(Graph{}, m, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrEmptyGraph))

	missing := Measurements{Contract: c, FontsReady: true, Items: map[string]TextMetrics{}}
	_, err = Arrange(g, missing, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrMissingMeasurement))

	other := m
	other.Contract.Version = "arranged-renderer/0"
	_, err = Arrange(g, other, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrContractMismatch))

	notReady := m
	notReady.FontsReady = false
	_, err = Arrange(g, notReady, c, DefaultOptions())
	assert.True(t, errors.Is(err, ErrFontsNotReady))
}
```


- [x] **Step 2: Run them, confirm they fail**

Run: `go test ./domain/layout/ -run TestArrange -v`
Expected: build failure `undefined: Arrange`, `undefined: ErrContractMismatch`, `undefined: ErrFontsNotReady`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Implement complete placement/routing, then candidate selection**

The old track-precedence implementation is intentionally removed: breaking a precedence
cycle does not remove collinear overlaps; dropping a hop whose x delta is <0.5px creates
diagonal routes under the 0.01px validator; simplifying every collinear triple erases
reversals. Implement the following deterministic construction in place.go/route.go and
arrange.go. The existing public Arrange signature and the internal layer/size helpers stay
unchanged. All additional routing working types remain private to this task.

**Entry validation and measurements**

1. Reject an empty Graph with ErrEmptyGraph. Graph must be a BuildGraph value; validate
   node IDs/index/edge ordinal/endpoints before indexing, so a caller-created malformed
   Graph returns an error rather than panics. Reject invalid contract/options before any
   arithmetic, using the same field policy as P1T2 Geometry.Validate. Compare `m.Contract
   != c` in full (same-version padding/weight changes are errors), then FontsReady. Reject
   missing/nonfinite/inconsistent metrics for every nonempty requested text; empty captions
   use zero metrics. Do not mutate Graph, Measurements, Contract or Options.
2. Classify/rank; compute measured label and node sizes. For each node's self loops, grow
   height to at least `(2*n+1)*max(PortSpacing,maxLabelHeight+2*LabelGap+MinClearancePx)`. Assign cumulative
   right lanes in ordinal order: each increment is max(LaneSeparation, label.W+2*LabelGap)
   for a labelled occurrence, otherwise LaneSeparation. Reserve their complete extent in
   the node's row slot before packing. Self-loop labels occupy their own lane strip, above
   their own exit arm: X=laneX-LabelGap-label.W, Y=exitY-LabelGap-label.H. The exit/entry y
   fractions in rule 5 keep outer loops above/below inner loops; the height bound keeps
   labels between exit bands, strictly nearer their own exit arm than a foreign loop arm. This produces separate labels and >=PortSpacing side ports.
3. Build the layering. For a given row order, place each real node/dummy left-to-right and
   use packRow to align item anchors to mean adjacent anchors (four down/up passes then
   one final down pass). A real slot includes its self-loop extent; a dummy reserves
   DummyWidth. Rows keep their order and NodeSpacing. Long-edge hops connect consecutive
   dummy columns; they need not be globally straight across all ranks.

**Ports, labels and cycle-safe channels**

4. Assign loop sides from body reach (ties right), and lanes per side by increasing
   channel span then ordinal. Reuse a lane only for disjoint inclusive channel intervals;
   overlapping/identical returns get distinct lanes. Loop gutter columns must lie outside
   the complete node/dummy/self-loop/fanout envelope, including the routing reserve below.
5. Sort bottom ports: left returns by lane ascending, forward edges by next-hop x then
   ordinal, right returns by lane descending. Top ports use previous-hop x/ordinal with
   the same loop groups. Source and target ports are unique. A labelled bottom slot has
   width PortSpacing+LabelGap+label.W; an unlabelled slot has PortSpacing. Place its port
   at slotLeft+PortSpacing/2, with the whole slot group centred on the node. Grow source
   width for these slots plus end clearance before placement. Labels sit beside the own
   source stub at X=portX+LabelGap in the shared band below the entire row. They never
   cover their own stroke. Top slots retain PortSpacing. Self loops use right-side ports.
6. Build one hop per forward chain gap, plus loop exit/entry hops. A channel contains,
   in order: source-label band; source fanout band; main horizontal-track band; target
   fanin band; RankSpacing; next row. Channel -1 contains only loop entries. A channel
   after the final row can contain loop exits. Space must be reserved before setting
   rowTop, not patched after routes have been drawn.
7. For channel hops whose source/target x columns coincide or violate clearance, **do not
   break a precedence cycle**. Give each source and target stub a separate transit column,
   deterministically sorted by endpoint x, role, ordinal. Transit columns are >=
   max(LaneSeparation,StrokePx+MinClearancePx,CrossingGapPx+2*StrokePx) apart, avoid the
   original endpoint x columns by the same clearance, and are included in the body routing
   envelope before gutter x is fixed. They may extend the canvas; compactness is secondary.
   Fanout uses a distinct horizontal level per source endpoint below the label band;
   fanin uses a distinct level per target endpoint above the next row. Initial source
   stubs end at their fanout level; final target stubs begin at their fanin level.
   Treat loop gutter endpoints as fixed reserved columns; entry/exit horizontal arms join
   the gutter directly, without a fictitious source/target node in that channel.
8. Give each non-straight main hop a unique horizontal level, in stable interval/ordinal
   order. Join fanout-column → main track → fanin-column. Every transit x is unique, every
   level is separated by the clearance above, and no transit x coincides with a foreign
   endpoint x. Thus intersections are perpendicular and strictly interior, separated from
   bends; verticals cannot merge. Same-source/source-x occurrences are already separated
   by ports. If a route would enter another label or node, extend the free channel bands
   and recompute, rather than return an invalid route. Keep a two-point vertical path for
   an unobstructed aligned sequential hop, reserving its column against other transit
   columns. Never treat a 0.1px displacement as straight: canonicalize coordinates first
   and compare using geomEps consistently.
9. Construct full polylines, including the explicit fanout/fanin points even when a main
   hop is straight. Use boundary attachments as first/last points. `simplify` removes
   duplicate points and a collinear middle point only when it lies between its neighbours
   (the direction does not reverse). Preserve reversals for preflight to reject; do not
   hide geometry defects. Quantize to 0.01px before final collinearity/crossing checks and
   reject any collapsed segment that cannot be removed without changing the path.

**Bounded routing search, crossings and normalization**

10. Keep the initial layering and evaluate every deterministic search prefix budget
    0..Options.CrossingSweeps (P1T5's prefix-monotone orderLayers), deduplicating equal row
    orders. For each prefix perform complete placement/ports/routing above. Compare actual
    complete routes, not only abstract inversion counts: rank by blocking geometric
    contacts (node/route/label overlap, close parallel lines, attachment or crossing/bend
    ambiguity), then actual proper crossing count, then prefix order. Implement these
    local contact predicates here; do not call future P1T7 Preflight. Stop at zero contacts
    and zero crossings. Retain the first best value. This is the bounded search promised
    by the design: an advisory crossing means it survived the selected bounded search,
    never a crossing removable by a candidate that was already generated and discarded.
11. `segmentsOf(routes []EdgeRoute) []segment` uses `type segment struct { ord int; a,b
    Point; horiz bool }`. `findCrossings(routes []EdgeRoute) []Crossing` enumerates proper
    horizontal/vertical intersections, Over=horizontal/Under=vertical, and sorts by Under,
    point Y/X then Over; deduplicate tuples. A bend/T touch is not a proper crossing and
    must instead be counted as a blocking geometric contact (not silently ignored).
    Also detect two crossing gaps that would overlap on one under-segment and crossings
    whose gap would consume a bend/attachment. Reserve/adjust tracks before accepting them.
12. Normalize all node/label boxes, route points and attachments together; shift x and
    y so the content plus stroke/marker extents has Margin clearance, Canvas origin 0.
    Canvas includes every label and complete arrowhead. Recompute crossings after final
    coordinate rounding. Set Options to the actual input options, slices in manifest
    order, and require final Geometry.Validate(g)==nil. Structural failure returns a typed
    error; never return malformed geometry as a best candidate. Geometric readability
    findings can still exist and must reach render/review (P1T9).

**Additional RED cases before this implementation:** malformed Graph index/ordinal,
same-version measurement mismatch, NaN/Inf/negative options, zero nonempty measurement,
0.1px displaced hop, three-hop precedence cycle, collinear reversal preservation, bend/T
touch and overlapping crossing-gap intervals, many parallel short/long/return edges,
intersecting loops, two long self-loop labels beside another node, 12 self loops, and two
nodes of unequal height. Assert no route/label collision and >=required port separation.
For every search prefix assert best actual route cost at budget k+1 <= k, and add a graph
where abstract layer inversions favor a worse orthogonal route. Test margin 0 with stroke
and markers still contained. Mutation probes must fail when fanout is removed, metrics
are replaced by constants, or full contract equality is reduced to version equality.

- [x] **Step 4: Run them, confirm they pass**

> Resolved (production provenance pass): the assigned coordinator, worker/store, composition root and browser producers now exist. `TestLayoutArrange_RealBrowserSavedSamplesReachView` drives all four acceptance samples through actual sample seeding and `initDomain`, browser measurements, Arrange, capture/review, SQLite, Reader and cache. It checks complete/valid geometry, downward flow, disjoint nodes, unrelated-node avoidance, gutter returns, measured text and source-relative labels at the persisted sink, matching display/cache digests and unchanged saved bytes. Empty Arrange output and missing measurement mutation probes both fail through this real seam; both were restored. This closes P1T6's missing-wiring gate, not P3T7's all-samples-passed readability gate: feature-pipeline still has incomplete captured branch ink/ambiguous crossings, and ralph-loop still has ambiguous crossings. Those actual findings and unreviewed status are preserved and asserted in the display; no pass, evidence rule or reviewer threshold was fabricated.

Run: `go test ./domain/layout/ -run 'TestArrange|TestPackRow' -v`
Expected: `--- PASS` for all `TestArrange_*` tests (including the four `TestArrange_SamplesAreValidDownwardAndOverlapFree` subtests and `TestArrange_Errors`) and the three `TestPackRow_*`; `ok  	github.com/arbitron/arbitron/domain/layout`.

Then the whole package and vet: `go test ./domain/layout/ && go vet ./domain/layout/`
Expected: `ok` and no vet output.

Debugging guidance if a sample subtest fails `assertNoRouteThroughUnrelatedNodes`: log the failing edge's `Points` and the node box; the only construction that can enter a row is a vertical at a port/dummy x — confirm `buildLayering` created a dummy for every intermediate row of that edge (P1T5) and that `packRow` kept `NodeSpacing` between that dummy and its neighbours.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/place.go domain/layout/route.go domain/layout/arrange.go domain/layout/arrange_test.go
git commit -m "feat(layout): top-to-bottom placement with orthogonal lanes, loop gutters and crossing records" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Data sources:** `Graph` — the captured saved manifest (P1T1 ← P2T2/P2T3/P2T4 `RevisionStamper` → P2T5 `Requester` → P2T6 store → P2T8 worker). `Measurements` — the production browser measurer `renderreview.BrowserRenderer.Measure` (P3T5) via the measure surface (P3T4); `Arrange` rejects measurements taken with another contract or before fonts were ready, so guessed sizes can never silently drive production geometry. `Options` — `DefaultOptions()` (P1T4) then `Refine` (P1T8).
- **Wiring:** `Arrange` is invoked by `Coordinator.Run` (P1T9) on every attempt; the coordinator is constructed at the composition root in `cmd/arbitron/layout_wiring.go:newLayoutStack` (P2T9) and executed by `layout.Worker` (P2T8) started in `initDomain` for both `serve` and `mcp`.
- **Sink:** geometry is persisted per revision (P2T6 `layout_results`), cached in `layout.json` (P2T7), returned by `QueryService` (P3T8) through HTTP/MCP (P3T9/P3T10) and drawn verbatim by the shared renderer (P3T2/P3T3) in both views and the review surface (P3T4).
- **Provenance probe:** `TestArrange_SamplesAreValidDownwardAndOverlapFree` runs on the real acceptance samples. `cmd/arbitron/layout_arrange_e2e_test.go:TestLayoutArrange_RealBrowserSavedSamplesReachView` asserts placement/routing invariants on actual root-produced, captured/reviewed and persisted geometry for all four samples. P3T7 separately owns requiring passed rendered review for the complex samples.

## Execution evidence

- Selection: P1T4/P1T5's previously blocked production gates still lack their assigned measurement/root pipeline. Skipped those gates; worked only on independent P1T6 this pass.
- Task 6.1 RED: observed missing-symbol failure, then spacing/mean/exact-position assertion failures against a compiling zero-position stub. GREEN: cumulative-offset isotonic pooling solves the ordered least-squares problem, including empty rows, without modifying inputs. No placeholder imports remain. Planned first commit: `6f31d0b`.
- Task 6.2 RED: observed missing Arrange/error symbols, then invalid/empty geometry and missing-error assertion failures against a compiling empty-geometry stub. Added malformed graph/index/ordinal, same-version mismatch, invalid contract/options, nonempty zero metrics, occurrence/port/label/Unicode, zero-margin, reversal, bend/T-touch and overlapping-gap cases. The helper stubs also failed exact segment/crossing/contact/simplification assertions.
- GREEN: complete measured sizing → rank/dummy layering → anchored packing → occurrence-preserving ports → cycle-safe fanout/main/fanin channels → gutter/self routes → stroke/marker-aware normalization and structural validation. Private routing working types stay in this task. Complete routed candidates are compared for every distinct ordering prefix; options and full text travel with the winning geometry. No future preflight dependency was introduced.
- Additional probes cover the three-hop precedence cycle and 0.1px displacements through complete polylines, two long labelled self loops beside a taller Unicode sibling, 12 self loops, many parallel short/long/return edges, intersecting/nested loops, unequal heights, missing self-loop metrics, unchanged input slices/maps, and policy changes at the geometry sink. A finite sub-ULP coordinate fixture first failed on a nonterminating transit search; sorted reservation jumps plus representable progress fixed it without a coordinate cap or new policy option.
- Self-review stage 1 (spec): checked public/shared helper signatures, port and self-strip formulas, downward progression, full contract equality, actual routed prefix selection, and assigned producer/consumer ownership. Corrected the initial loop-side implementation to use body reach relative to the endpoint spine (not unrelated components); the new left/right/tie regression failed before the fix. No extra flag, adapter or renderer behavior was added. Production completion remains blocked above, not waived.
- Self-review stage 2 (anti-green-theater): independent sink assertions check labels against every node/path, close/merged parallels, bend/T/gap ambiguity, port separation and stroke/marker containment. Replacing requested metrics with constant one-line values failed Unicode/height sinks. Removing fanout transits failed real-sample and parallel-edge path sinks. Reducing full contract comparison to version-only failed same-version padding/weight cases. Selecting only the final layer prefix failed a fixed fixture where layer inversions improve but complete orthogonal routes worsen. Removing measured self-loop strips failed the tall-Unicode-sibling label-collision and spacing sinks. All mutations were restored. These are unit seam tests, not a claim of production wiring.
- Self-review stage 3 (quality): read neighboring geometry/rank code and the dashboard's chooseLoopSide reference; preserved input ownership and manifest order. Fixed reversal contact scoring after behavioral RED, and extracted a fan comparator whose strict/transitive ordering across both directions is regression-tested (RED before the fix). Used named clearance/alignment policy, bounded reservation jumps, consistent coordinate quantization, grouped imports, and a reusable test case type. No stub, temporary probe or mutation remains.
- Verification: prescribed `TestPackRow` and `TestArrange|TestPackRow` commands, helper/contact/cycle probes, every search prefix through the default budget on all four real sample manifests, `go test ./domain/layout/`, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` (empty) and `git diff --check` pass. Unrelated untracked `docs/visuals/` files were preserved and excluded from commits. At the original implementation pass, Step 6.2.4 remained unchecked for the assigned production composition/measurement gate; the follow-up evidence below closes that dependency.

## Production provenance follow-up

- Selected this first unchecked task because its former missing-root dependencies have landed; no other task was implemented this pass.
- Existing-behavior characterization follows contracts §6.1: the real-root test is protected by behavioral mutation REDs. Returning empty geometry from Arrange made the actual worker fail with `layout: invalid geometry: format ""`; dropping its measurement map failed with `layout: missing text measurement: edge:8`. Both made the persisted-outcome assertion fail, and both changes were restored before GREEN.
- Self-review 1 — spec: restricted the new test to P1T6 placement/routing and real producer/sink provenance. An initial test incorrectly required edges in the single-node harness-update sample and complete rendered evidence for every sample; corrected those extra assumptions to match this task's explicit allowance for readability findings. The test still requires exact captured geometry identity, real hashed PNG artifacts, fonts-ready browser metrics, review timestamp, durable result generation, and honest findings/unreviewed status. A passed state must have complete rendered evidence. No production behavior, configuration or policy was changed.
- Self-review 2 — anti-green-theater: all four bundled manifests run through real root seeding, with no renderer/reviewer factory override. Independent fresh browser measurements are checked against persisted node/label line counts and heights. Geometry/node/edge coverage is checked against the saved graph; paths, labels, gutters and source bytes are checked at the sink. Empty output and missing-input mutation probes fail rather than accepting a no-op. Display and cache must match the exact authoritative result generation/digest.
- Self-review 3 — quality: followed neighboring root-test environment/browser/artifact helpers, kept assertions in a task-specific helper, avoided production exports or new policy knobs, and preserved unrelated untracked `docs/visuals/` files.
- Final verification after restoration/review: `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run '^TestLayoutArrange_RealBrowserSavedSamplesReachView$' -count=1 -v` passes all four samples (128.235s); `go test ./domain/layout/ -run 'TestArrange|TestPackRow' -v`, `go test ./domain/layout/`, `go vet ./domain/layout/ ./cmd/arbitron`, and focused Arrange/packRow `-race` pass. `gofmt -l` is empty and `git diff --check` passes.
- Concern retained, not waived: actual feature-pipeline review reports absent branch ink on edge 49, indistinct labels 31/34 and ambiguous crossings; ralph-loop reports ambiguous crossings. These remain P3T7's recorded rendered-acceptance failures. This pass proves wired-and-fed arrangement, not release acceptance or HTTP/dashboard route completion owned by later tasks.
