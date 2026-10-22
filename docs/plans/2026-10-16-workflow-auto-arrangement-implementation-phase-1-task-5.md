# Workflow Auto-Arrangement — Phase 1 Task 5: Layering with long-edge slots and crossing-reducing row order

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Build the row structure the router draws on — one row per rank, a real slot per node and a narrow "dummy" slot for every forward edge occurrence passing through an intermediate row — and order each row to minimise crossings with a bounded, deterministic barycentre + adjacent-swap search.

**Architecture:** `domain/layout/layers.go` (unexported). A long forward edge (rank span > 1) becomes a chain `source → dummy → … → target`, so its vertical run occupies its own reserved column in every row it crosses and can never pass through a node. Row order uses barycentre sweeps (down then up) for `Options.CrossingSweeps` iterations, keeps the best order seen, then runs bounded adjacent transposition. All tie-breaks use current position, so the result is deterministic.

**Tech Stack:** Go 1.25.5 (`sort`), testify.

**Spec / Design:** `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §2 ("Sequential flow never wraps into columns. Forward destinations are below sources; branches sit beside the flow, and shortcuts/rejoins get traceable routes"; options include a crossing-search budget), §3 ("Unnecessary crossings that the bounded routing search can remove — refine before passing").

---

### Task 5.1: `buildLayering` — rows, dummy slots and edge chains

**Files:**
- Create: `domain/layout/layers.go`
- Test:   `domain/layout/layers_test.go`

**Interfaces:**
- Consumes: `Graph` (P1T1), `classifyEdges`, `assignRanks` (P1T3), `EdgeForward` (P1T2).
- Produces (unexported; consumed by P1T6 `place.go`/`route.go`):
  ```go
  type layerItem struct { node int /* node index or -1 */; edge int /* long-edge ordinal for a dummy or -1 */; rank int }
  type layering struct {
      rows       [][]int       // rows[r] = item ids left→right
      items      []layerItem
      itemOfNode []int         // node index → item id
      chains     map[int][]int // forward edge ordinal → item ids source…target (inclusive)
      up, down   [][]int       // item → connected items in the adjacent row (one entry per edge occurrence)
  }
  func buildLayering(g Graph, kinds []EdgeKind, ranks []int) *layering
  func (l *layering) positions() []int          // item id → index within its row
  func (l *layering) crossings() int            // crossings between adjacent rows of forward segments
  func orderLayers(l *layering, sweeps int)      // Task 5.2
  ```

- [x] **Step 1: Write the failing test**

Create `domain/layout/layers_test.go`:

```go
package layout

import (
	"testing"

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
	// a→b→c→d plus a shortcut a→d spanning ranks 0..3.
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"c", "d"}, [2]string{"a", "d"})
	l := layeringFor(t, g)
	chain := l.chains[3]
	require.Len(t, chain, 4, "source, two dummies, target")
	assert.Equal(t, l.itemOfNode[0], chain[0])
	assert.Equal(t, l.itemOfNode[3], chain[3])
	for _, id := range chain[1:3] {
		assert.Equal(t, -1, l.items[id].node)
		assert.Equal(t, 3, l.items[id].edge, "dummy remembers its edge ordinal")
	}
	assert.Equal(t, []string{"b", "~"}, rowNodeIDs(g, l, 1))
	assert.Equal(t, []string{"c", "~"}, rowNodeIDs(g, l, 2))
}

func TestBuildLayering_ParallelLongEdgesGetSeparateDummies(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "c"}, [2]string{"a", "c"}, [2]string{"a", "c"})
	l := layeringFor(t, g)
	assert.NotEqual(t, l.chains[2][1], l.chains[3][1], "identical parallel shortcuts never share a column")
	assert.Len(t, l.rows[1], 3)
}

func TestBuildLayering_LoopReturnsAndSelfLoopsHaveNoChains(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"b", "b"})
	l := layeringFor(t, g)
	_, hasBack := l.chains[1]
	_, hasSelf := l.chains[2]
	assert.False(t, hasBack)
	assert.False(t, hasSelf)
}

func TestBuildLayering_UpDownAreSymmetric(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "b"}, [2]string{"a", "b"}, [2]string{"a", "c"})
	l := layeringFor(t, g)
	a := l.itemOfNode[0]
	assert.Len(t, l.down[a], 3, "one entry per edge occurrence, parallels included")
	for _, d := range l.down[a] {
		assert.Contains(t, l.up[d], a)
	}
}

func TestLayering_CrossingsCountsTwistedSegments(t *testing.T) {
	// Row 0: a, b. Row 1: c, d. Edges a→d and b→c cross once in manifest order.
	g := edgeGraph(t, [2]string{"a", "d"}, [2]string{"b", "c"})
	l := layeringFor(t, g)
	assert.Equal(t, []string{"a", "b"}, rowNodeIDs(g, l, 0))
	assert.Equal(t, []string{"d", "c"}, rowNodeIDs(g, l, 1), "nodes are first placed in manifest order (d declared before c)")
	assert.Equal(t, 0, l.crossings())

	g2 := edgeGraph(t, [2]string{"a", "c"}, [2]string{"b", "d"}, [2]string{"a", "d"}, [2]string{"b", "c"})
	l2 := layeringFor(t, g2)
	assert.Equal(t, 1, l2.crossings(), "K2,2 always has exactly one crossing")
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestBuildLayering|TestLayering_' -v`
Expected: build failure `undefined: buildLayering`, `undefined: layering`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/layers.go`:

```go
package layout

import "sort"

// layerItem is one slot in a row: a real node, or a dummy reserving a column
// for a long forward edge occurrence passing through the row.
type layerItem struct {
	node int // node index, or -1 for a dummy
	edge int // long-edge ordinal for a dummy, or -1
	rank int
}

// layering is the row structure of a graph: rows of items, each forward edge
// as a chain of items (source, dummies…, target), and the adjacency between
// consecutive rows induced by those chains.
type layering struct {
	rows       [][]int
	items      []layerItem
	itemOfNode []int
	chains     map[int][]int
	up         [][]int
	down       [][]int
}

// buildLayering places nodes in their rank's row (manifest order), appends one
// dummy per intermediate row for every forward edge spanning more than one
// rank, and records each forward edge's chain.
func buildLayering(g Graph, kinds []EdgeKind, ranks []int) *layering {
	maxRank := 0
	for _, r := range ranks {
		if r > maxRank {
			maxRank = r
		}
	}
	l := &layering{
		rows:       make([][]int, maxRank+1),
		itemOfNode: make([]int, len(g.Nodes)),
		chains:     make(map[int][]int),
	}
	addItem := func(it layerItem) int {
		id := len(l.items)
		l.items = append(l.items, it)
		l.up = append(l.up, nil)
		l.down = append(l.down, nil)
		l.rows[it.rank] = append(l.rows[it.rank], id)
		return id
	}
	for i := range g.Nodes {
		l.itemOfNode[i] = addItem(layerItem{node: i, edge: -1, rank: ranks[i]})
	}
	for _, e := range g.Edges {
		if kinds[e.Ordinal] != EdgeForward {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		chain := []int{l.itemOfNode[s]}
		for r := ranks[s] + 1; r < ranks[t]; r++ {
			chain = append(chain, addItem(layerItem{node: -1, edge: e.Ordinal, rank: r}))
		}
		chain = append(chain, l.itemOfNode[t])
		for k := 0; k+1 < len(chain); k++ {
			l.down[chain[k]] = append(l.down[chain[k]], chain[k+1])
			l.up[chain[k+1]] = append(l.up[chain[k+1]], chain[k])
		}
		l.chains[e.Ordinal] = chain
	}
	return l
}

// positions maps every item id to its index within its row.
func (l *layering) positions() []int {
	pos := make([]int, len(l.items))
	for _, row := range l.rows {
		for i, id := range row {
			pos[id] = i
		}
	}
	return pos
}

// crossings counts pairwise crossings of forward segments between every pair
// of adjacent rows (segments sharing an endpoint never count).
func (l *layering) crossings() int {
	pos := l.positions()
	total := 0
	for r := 0; r+1 < len(l.rows); r++ {
		type seg struct{ a, b int }
		var segs []seg
		for _, id := range l.rows[r] {
			for _, d := range l.down[id] {
				segs = append(segs, seg{pos[id], pos[d]})
			}
		}
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				if (segs[i].a < segs[j].a && segs[i].b > segs[j].b) || (segs[i].a > segs[j].a && segs[i].b < segs[j].b) {
					total++
				}
			}
		}
	}
	return total
}

// orderLayers is defined in Task 5.2.
var _ = sort.SliceStable
```

(The trailing `var _ = sort.SliceStable` keeps the `sort` import used until Task 5.2 adds `orderLayers`; Task 5.2 removes that line.)

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestBuildLayering|TestLayering_' -v`
Expected: `--- PASS` for the five `TestBuildLayering_*` tests and `TestLayering_CrossingsCountsTwistedSegments`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/layers.go domain/layout/layers_test.go
git commit -m "feat(layout): rank rows with reserved columns for long edges" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5.2: `orderLayers` — bounded, deterministic crossing reduction

**Files:**
- Modify: `domain/layout/layers.go` (append `orderLayers`, `sortRowBy`, `transpose`; delete the `var _ = sort.SliceStable` line)
- Test:   `domain/layout/layers_test.go` (append)

**Interfaces:**
- Produces: `func orderLayers(l *layering, sweeps int)` — mutates `l.rows`; `sweeps <= 0` leaves the initial (manifest) order.

- [x] **Step 1: Write the failing test**

Append to `domain/layout/layers_test.go`:

```go
func TestOrderLayers_RemovesAvoidableCrossing(t *testing.T) {
	// Row 0: a, b. Row 1 in manifest order: x, y, y2, x2 — so a→y2 crosses
	// b→y. Reordering row 1 to x, y2, y, x2 removes the only crossing.
	g := edgeGraph(t, [2]string{"a", "x"}, [2]string{"b", "y"}, [2]string{"a", "y2"}, [2]string{"b", "x2"},
		[2]string{"y", "z"}, [2]string{"x", "z"})
	l := layeringFor(t, g)
	before := l.crossings()
	orderLayers(l, DefaultOptions().CrossingSweeps)
	after := l.crossings()
	assert.LessOrEqual(t, after, before)
	assert.Equal(t, 0, after, "this graph is planar layered; the search must find a crossing-free order")
}

func TestOrderLayers_ZeroBudgetKeepsManifestOrder(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "d"}, [2]string{"b", "c"}, [2]string{"a", "c"})
	l := layeringFor(t, g)
	want := append([][]int(nil), l.rows...)
	orderLayers(l, 0)
	assert.Equal(t, want, l.rows)
}

func TestOrderLayers_IsDeterministic(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "feature-pipeline"))
	require.NoError(t, err)
	a := layeringFor(t, g)
	b := layeringFor(t, g)
	orderLayers(a, 8)
	orderLayers(b, 8)
	assert.Equal(t, a.rows, b.rows)
}

func TestOrderLayers_NeverWorseThanInitialOnSamples(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			g, err := BuildGraph(loadSample(t, name))
			require.NoError(t, err)
			l := layeringFor(t, g)
			before := l.crossings()
			orderLayers(l, 8)
			assert.LessOrEqual(t, l.crossings(), before)
			// Every item is still present exactly once.
			seen := map[int]bool{}
			for _, row := range l.rows {
				for _, id := range row {
					assert.False(t, seen[id])
					seen[id] = true
				}
			}
			assert.Len(t, seen, len(l.items))
		})
	}
}

func TestOrderLayers_LargerBudgetIsNeverWorse(t *testing.T) {
	g, err := BuildGraph(loadSample(t, "feature-pipeline"))
	require.NoError(t, err)
	small := layeringFor(t, g)
	large := layeringFor(t, g)
	orderLayers(small, 2)
	orderLayers(large, 16)
	assert.LessOrEqual(t, large.crossings(), small.crossings(), "refinement raises the budget expecting no regression")
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run TestOrderLayers -v`
Expected: build failure `undefined: orderLayers`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

In `domain/layout/layers.go`, delete the line `var _ = sort.SliceStable` (and its comment `// orderLayers is defined in Task 5.2.`), then append:

Implement the following bounded search in layers.go (the Task 5.1 representation stays
unchanged). Do not use a single final transposition after selecting the best barycentre
state: that does not make the larger-budget result a superset of the smaller-budget one.

1. If sweeps<=0 return unchanged. Set best=cloneRows(l.rows), bestCross=l.crossings().
2. Maintain a separate barycentre trajectory from the initial rows. For round 1..sweeps,
   do one stable downward and upward sweep; neighbour keys are their mean adjacent-row
   positions, isolated items retain current position. Save that trajectory for next round.
3. Evaluate the trajectory and a *copy* with deterministic adjacent transposition. One
   transposition pass visits rows and adjacent pairs left-to-right, accepts only strict
   reductions in total l.crossings(), and repeats up to `round` passes or no improvement.
   Retain the best of initial and every prefix's candidates, ties keep earliest candidate.
   Restore the saved barycentre trajectory before advancing to the next round. Thus the
   candidates at budget k are an exact prefix of those at budget k+1.
4. Assign a defensive clone of the winner to l.rows. Never mutate items/chains/up/down;
   keep duplicate neighbour entries for parallel occurrences. Stop safely at zero crossings.
   Define cloneRows as a deep copy of each row. sortRowBy uses sort.SliceStable, current
   position on no-neighbour rows, and no map iteration to establish ordering.

Add a small exhaustive fixture suite (2–4 items per row, parallel/dummy/isolate cases)
asserting budget k+1 never worsens k, every item stays exactly once in its original rank,
zero budget is a deep-equal no-op, and no-neighbour rows do not reorder. The existing
sample-only larger-budget assertion is insufficient to establish this prefix property.
Run RED against a no-op and against the old final-only transposition policy, then GREEN.

- [x] **Step 4: Run it, confirm it passes**

> RESOLVED (2026-10-17): The previously missing consumer/root/browser tasks have landed. `TestLayoutLayers_RealBrowserSavedRowsReachView` now drives real Service.Create → captured manifest → Requester/SQLite → Worker/Coordinator → browser measurements → Arrange/buildLayering/orderLayers → rendered review → stored result, Reader and filesystem cache. A twisted-row fixture requires an actual reorder and zero stored crossings; a parallel-shortcut fixture requires distinct channels through both intermediate rows. Both pass in one attempt without refinement. The real bundled pr-review root probe and the prescribed focused/package gates also pass. This closes P1T5's production gate, not the separate three-sample release acceptance.

Run: `go test ./domain/layout/ -run 'TestOrderLayers|TestBuildLayering|TestLayering_' -v`
Expected: all five `TestOrderLayers_*` (with three sample subtests) and the Task 5.1 tests `--- PASS`; `ok`. Run `go test ./domain/layout/` → `ok`.

If the avoidable-crossing fixture still crosses, record the failing row order and fix the bounded search; do not weaken the zero-crossing assertion.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/layers.go domain/layout/layers_test.go
git commit -m "feat(layout): bounded deterministic crossing reduction for row order" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Data source:** ranks and edge kinds from P1T3 over the captured manifest graph (P1T1 ← P2 pipeline); the sweep budget comes from `Options.CrossingSweeps` (`DefaultOptions` P1T4, raised by `Refine` P1T8 when review reports avoidable crossings).
- **Consumers / wiring:** `buildLayering` + `orderLayers` are called only by `Arrange` (P1T6), reached in production via `Coordinator.Run` (P1T9) constructed in `cmd/arbitron/layout_wiring.go:newLayoutStack` (P2T9).
- **Provenance probe:** `TestOrderLayers_NeverWorseThanInitialOnSamples` and P1T7's `samples_geometry_test.go` (crossing count recorded in the geometry and advisory findings) run on the real acceptance samples.

## Execution evidence

- Selection: P1T4's remaining gate was previously blocked on later tasks and remains unavailable. Skipped that gate as instructed; implemented only independent P1T5 steps this pass.
- Task 5.1 RED: missing-symbol failure, followed by behavioral failures against compiling empty-layering/zero-crossing stubs. GREEN: manifest-ordered rows, one dummy per intermediate rank per occurrence, source-to-target chains and symmetric occurrence-preserving adjacency. Added exact position, twisted-segment and parallel crossing-count sinks. First planned commit: `7a3de54`.
- Task 5.2 RED: missing-symbol failure, then a compiling no-op left the avoidable crossing and the transposition-required fixture unchanged. The old final-only policy failed the budget-prefix regression: 6 crossings at budget 1 became 9 at budget 2. GREEN: each barycentre prefix and its bounded transposed copy are evaluated, ties retain the earlier candidate, and the untouched barycentre trajectory seeds the next round. No temporary stub or import placeholder remains.
- Exhaustive coverage: all 1,168 adjacency matrices of 2–4 nodes per side with at most 9 possible edges, each repeated with parallel occurrences and backbone/parallel-shortcut dummy variants; budgets 0–4 never regress, items stay exactly once in original ranks, zero budget is a deep-equal no-op, isolated rows retain order, and all non-row metadata stays unchanged. Real sample manifests cover feature-pipeline, pr-review and ralph-loop. A separate three-row fixture requires transposition beyond barycentres and shows extra budget actually changes the crossing-count sink.
- Self-review, in order: (1) verified exact P1T5 types/signatures, rank/ordinal identity, stable tie policy and prefix algorithm; no new policy option, adapter or P1T6 routing behavior. (2) Confirmed empty/no-op implementations fail exact row/chain/crossing sinks; added probes rejecting deduplicated neighbour means, item-ID ordering of isolates, and a constant crossing count. Each mutation failed its intended assertions and was restored. Corrected an over-specific regression expectation: this plan transposes each trajectory, not the initial winner, so it must assert prefix non-regression rather than the old policy's exact budget-1 score. The planar fixture still requires zero crossings. Production absence is explicitly blocked above, not claimed as integration coverage. (3) Read neighboring rank/helper code, kept pure internal helpers and occurrence slices, omitted the unnecessary temporary sort-import placeholder, ran gofmt/vet/race, and checked the diff for unrelated changes.
- Initial-pass verification: prescribed focused commands, added sort/weight tests, `go test ./domain/layout/`, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` (empty), and `git diff --check` passed. Production verification was deferred until the assigned consumers/root existed; it is now closed by the revisit below. Unrelated untracked `docs/visuals/` files were preserved and excluded from commits.

## Production-gate revisit (2026-10-17)

- Selection: numeric phase/task enumeration identifies P1T5 as the first task with an unchecked step. Its former dependency blocker is resolved in the current checkout; only this task is changed.
- TDD for existing behavior (contracts §6.1): wrote the root sink test, then temporarily made `orderLayers` a compiling no-op. The real browser test failed on the stored row-order relationship and the remaining crossing (`/tmp/layout-layers-order-red.log`). Restored the implementation and both subtests passed. Independently removed intermediate dummy-slot creation; the shortcut test failed at the terminal SQLite sink with invalid attachment geometry (`/tmp/layout-layers-slots-red.log`). Restored all production code; no production diff or temporary mutation remains.
- Ordered self-review: (1) Scope is exactly P1T5's deferred production verification; existing algorithm/contract/budget and unrelated blockers remain unchanged. (2) Tests use real initDomain collaborators, actual saved inputs and browser measurements/review, require nonempty compatible persisted geometry/evidence, compare Reader/cache digests, and prove unchanged source bytes. Strengthened shortcut checks from merely different whole paths to distinct vertical channels at each intermediate node row; the reorder fixture also uses real browser metrics with zero search budget as a negative control requiring a crossing. (3) Followed neighboring root-test helpers and naming, kept the two fixtures table-driven, required valid review evidence before dereferencing it, formatted and vetted the files.
- Final post-review verification: `go test ./domain/layout/ -run 'TestOrderLayers|TestBuildLayering|TestLayering_' -count=1 -v` passed (0.981s); `go test ./domain/layout/ -count=1` passed (7.174s). `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron/ -run 'TestLayoutLayers_RealBrowserSavedRowsReachView|TestLayoutRender_RealBrowserPRReview' -count=1 -v` passed (24.313s), including both new fixtures and the existing real sample, with no skips. New root test under `-race` passed (14.767s); focused domain tests under `-race` passed (4.205s). `go vet ./domain/layout/ ./cmd/arbitron/`, gofmt and `git diff --check` passed. P3T7's recorded complex-sample/Docker gates are not claimed by this task.
