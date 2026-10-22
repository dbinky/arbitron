# Workflow Auto-Arrangement — Phase 1 Task 7: Findings, candidate score and geometric preflight

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Encode the spec's readability acceptance criteria as typed findings (criterion, severity, implicated nodes/edge ordinals, evidence), rank candidates lexicographically, and check every candidate geometry before it is rendered — including the bundled acceptance samples.

**Architecture:** `finding.go` defines the shared vocabulary used by preflight here and by the rendered reviewer (P3T6), plus `Score`/`ScoreOf`/`Less` used by the coordinator (P1T9) to choose the best candidate. `preflight.go` implements geometry-only checks. Preflight is explicitly **not** visual review: the coordinator renders and reviews every candidate, including ones that fail preflight, and a pass requires both (P1T9). `samples_geometry_test.go` asserts the real samples arrange with zero blocking preflight findings.

**Tech Stack:** Go 1.25.5, testify.

**Spec / Design:** spec "Outcomes and Acceptance Criteria"; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §3 (blocking vs advisory list; "Geometric preflight is not visual review"), §4 (lexicographic ranking: blocking severity/count, then advisory incl. crossings, then attempt order).

---

### Task 7.1: Finding vocabulary and candidate score

**Files:**
- Create: `domain/layout/finding.go`
- Test:   `domain/layout/finding_test.go`

**Interfaces:**
- Produces (exact, contracts §1): `Severity` (`SeverityBlocking`, `SeverityAdvisory`), `Criterion` constants (`CriterionCoverage`, `CriterionNodeOverlap`, `CriterionEdgeThroughNode`, `CriterionTextCollision`, `CriterionTextClipped`, `CriterionTextUnreadable`, `CriterionMergedLines`, `CriterionAmbiguousAttachment`, `CriterionArrowDirection`, `CriterionAmbiguousCrossing`, `CriterionUpwardProgression`, `CriterionIndistinctLoop`, `CriterionIndistinctBranch`, `CriterionRenderMismatch`, `CriterionCrossings`), `FindingSource` (`SourcePreflight`, `SourceReview`), `Finding`, `BlockingCount`, `Score`, `ScoreOf`, `(Score).Less`.

- [x] **Step 1: Write the failing test**

Create `domain/layout/finding_test.go`:

```go
package layout

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockingCount(t *testing.T) {
	fs := []Finding{
		{Criterion: CriterionNodeOverlap, Severity: SeverityBlocking},
		{Criterion: CriterionCrossings, Severity: SeverityAdvisory},
		{Criterion: CriterionTextClipped, Severity: SeverityBlocking},
	}
	assert.Equal(t, 2, BlockingCount(fs))
	assert.Equal(t, 0, BlockingCount(nil))
}

func TestScoreOf(t *testing.T) {
	fs := []Finding{
		{Criterion: CriterionNodeOverlap, Severity: SeverityBlocking},
		{Criterion: CriterionCrossings, Severity: SeverityAdvisory},
		{Criterion: CriterionIndistinctBranch, Severity: SeverityAdvisory},
	}
	assert.Equal(t, Score{Blocking: 1, Advisory: 1, Crossings: 4, Attempt: 2}, ScoreOf(2, fs, 4),
		"the crossings finding is represented by the crossing count, not double-counted as advisory")
}

func TestScore_LessIsLexicographic(t *testing.T) {
	passCompactLater := Score{Blocking: 0, Advisory: 5, Crossings: 9, Attempt: 3}
	blockedCompact := Score{Blocking: 1, Advisory: 0, Crossings: 0, Attempt: 1}
	assert.True(t, passCompactLater.Less(blockedCompact), "a layout with unresolved blocking issues never beats one without")

	fewerCrossings := Score{Blocking: 0, Advisory: 0, Crossings: 2, Attempt: 4}
	moreCrossings := Score{Blocking: 0, Advisory: 0, Crossings: 3, Attempt: 1}
	assert.True(t, fewerCrossings.Less(moreCrossings))

	earlier := Score{Attempt: 1}
	later := Score{Attempt: 2}
	assert.True(t, earlier.Less(later), "ties break by deterministic attempt order")
	assert.False(t, later.Less(earlier))
	assert.False(t, earlier.Less(earlier))
}

func TestFinding_JSON(t *testing.T) {
	f := Finding{Criterion: CriterionMergedLines, Severity: SeverityBlocking, Source: SourceReview, NodeIDs: []string{"a"}, EdgeOrdinals: []int{0, 3}, Evidence: "x"}
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	assert.JSONEq(t, `{"criterion":"merged_lines","severity":"blocking","source":"review","nodeIds":["a"],"edgeOrdinals":[0,3],"evidence":"x"}`, string(raw))
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestBlockingCount|TestScore|TestFinding_JSON' -v`
Expected: build failure `undefined: Finding`, `undefined: BlockingCount`, `undefined: Score`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/finding.go`:

```go
package layout

// Severity says whether a finding prevents a pass.
type Severity string

const (
	// SeverityBlocking findings must be resolved before a candidate passes.
	SeverityBlocking Severity = "blocking"
	// SeverityAdvisory findings are recorded but do not block a pass.
	SeverityAdvisory Severity = "advisory"
)

// Criterion names the spec acceptance criterion a finding concerns.
type Criterion string

const (
	CriterionCoverage            Criterion = "coverage"
	CriterionNodeOverlap         Criterion = "node_overlap"
	CriterionEdgeThroughNode     Criterion = "edge_through_node"
	CriterionTextCollision       Criterion = "text_collision"
	CriterionTextClipped         Criterion = "text_clipped"
	CriterionTextUnreadable      Criterion = "text_unreadable"
	CriterionMergedLines         Criterion = "merged_lines"
	CriterionAmbiguousAttachment Criterion = "ambiguous_attachment"
	CriterionArrowDirection      Criterion = "arrow_direction"
	CriterionAmbiguousCrossing   Criterion = "ambiguous_crossing"
	CriterionUpwardProgression   Criterion = "upward_progression"
	CriterionIndistinctLoop      Criterion = "indistinct_loop_return"
	CriterionIndistinctBranch    Criterion = "indistinct_branch"
	CriterionRenderMismatch      Criterion = "render_mismatch"
	// CriterionCrossings is advisory only: remaining, visibly treated crossings.
	CriterionCrossings Criterion = "crossings"
)

// FindingSource says which stage produced a finding.
type FindingSource string

const (
	SourcePreflight FindingSource = "preflight"
	SourceReview    FindingSource = "review"
)

// Finding is one readability issue of one candidate.
type Finding struct {
	Criterion    Criterion     `json:"criterion"`
	Severity     Severity      `json:"severity"`
	Source       FindingSource `json:"source"`
	NodeIDs      []string      `json:"nodeIds,omitempty"`
	EdgeOrdinals []int         `json:"edgeOrdinals,omitempty"`
	Evidence     string        `json:"evidence"`
}

// BlockingCount counts blocking findings.
func BlockingCount(fs []Finding) int {
	n := 0
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			n++
		}
	}
	return n
}

// Score ranks candidates: fewer blocking findings first, then fewer advisory
// findings plus crossings, then earlier attempt. Compactness is not a factor.
type Score struct {
	Blocking  int `json:"blocking"`
	Advisory  int `json:"advisory"`
	Crossings int `json:"crossings"`
	Attempt   int `json:"attempt"`
}

// ScoreOf scores a candidate from all of its findings (preflight + review) and
// its crossing count. The advisory crossings finding is represented by the
// crossing count and not counted again.
func ScoreOf(attempt int, findings []Finding, crossings int) Score {
	s := Score{Attempt: attempt, Crossings: crossings}
	for _, f := range findings {
		switch {
		case f.Severity == SeverityBlocking:
			s.Blocking++
		case f.Criterion != CriterionCrossings:
			s.Advisory++
		}
	}
	return s
}

// Less reports whether s ranks strictly better than o.
func (s Score) Less(o Score) bool {
	if s.Blocking != o.Blocking {
		return s.Blocking < o.Blocking
	}
	if sa, oa := s.Advisory+s.Crossings, o.Advisory+o.Crossings; sa != oa {
		return sa < oa
	}
	return s.Attempt < o.Attempt
}
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestBlockingCount|TestScore|TestFinding_JSON' -v`
Expected: four tests `--- PASS`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/finding.go domain/layout/finding_test.go
git commit -m "feat(layout): readability findings and lexicographic candidate score" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.2: Geometric preflight

**Files:**
- Create: `domain/layout/preflight.go`
- Test:   `domain/layout/preflight_test.go`

**Interfaces:**
- Consumes: `Geometry.Validate`, `Rect` helpers, `nearly` (P1T2); `classifyEdges` (P1T3); `Measurements.Metric`, keys (P1T4); `findCrossings`, `segmentsOf`, `segment` (P1T6 `route.go`); `onSide` (P1T2 `geometry.go`); test helper `arrangeFor`, `boxOf` (P1T6 `arrange_test.go`).
- Produces: `func Preflight(g Graph, geo Geometry, m Measurements) []Finding`; `func labelAssociated(geo Geometry, edge EdgeRoute) bool` (own-label/foreign-segment distances as specified in Step 3); internal `func segmentHitsRect(a, b Point, r Rect) bool`, `func crossingNearBend(c Crossing, routes []EdgeRoute, radius float64) bool`.

Checks (all `SourcePreflight`; blocking unless noted): coverage (Validate failure ⇒ only finding); unreadable contract text at practical zoom; node overlap/clearance; route through an unrelated node; branch label overlapping a node, another label or another edge; label not associated with its own edge (`indistinct_branch`); node/label box smaller than its measured text (`text_clipped`); collinear overlapping segments of different edges (`merged_lines`); attachment off its declared side or two attachments merged; arrow not pointing into the target side or route not leaving the source side (`arrow_direction`); untreated crossing, crossing too close to a bend, or contract without crossing treatment (`ambiguous_crossing`); forward edge pointing or climbing up (`upward_progression`); route kind disagreeing with classification or a loop return not using a side gutter (`indistinct_loop_return`); remaining crossings (advisory `crossings`).

- [x] **Step 1: Write the failing test**

Create `domain/layout/preflight_test.go`:

```go
package layout

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture: branch + rejoin + shortcut + loop return, the shapes the spec names.
func preflightFixture(t *testing.T) (Graph, Geometry, Measurements) {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeScript}, {ID: "gate", Type: workflow.NodeTypeConditional},
			{ID: "yes"}, {ID: "no"}, {ID: "join"}, {ID: "end"},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "gate"},
			{Source: "gate", Target: "yes", Output: "pass"},
			{Source: "gate", Target: "no", Output: "fail"},
			{Source: "yes", Target: "join"},
			{Source: "no", Target: "join"},
			{Source: "join", Target: "end"},
			{Source: "start", Target: "end", Label: "shortcut"},
			{Source: "join", Target: "gate", Label: "back-edge: retry"},
		},
	})
	require.NoError(t, err)
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(g, m, c, DefaultOptions())
	require.NoError(t, err)
	return g, geo, m
}

func criteria(fs []Finding) map[Criterion]int {
	out := map[Criterion]int{}
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			out[f.Criterion]++
		}
	}
	return out
}

func cloneGeometry(t *testing.T, geo Geometry) Geometry {
	t.Helper()
	out := geo
	out.Nodes = append([]NodeBox(nil), geo.Nodes...)
	out.Edges = make([]EdgeRoute, len(geo.Edges))
	for i, e := range geo.Edges {
		e.Points = append([]Point(nil), e.Points...)
		if e.Text != nil {
			lb := *e.Text
			e.Text = &lb
		}
		out.Edges[i] = e
	}
	out.Crossings = append([]Crossing(nil), geo.Crossings...)
	return out
}

func TestPreflight_ArrangedFixtureHasNoBlockingFindings(t *testing.T) {
	g, geo, m := preflightFixture(t)
	fs := Preflight(g, geo, m)
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			t.Errorf("unexpected blocking finding: %+v", f)
		}
		assert.Equal(t, SourcePreflight, f.Source)
	}
}

func TestPreflight_InvalidGeometryIsACoverageFinding(t *testing.T) {
	g, geo, m := preflightFixture(t)
	broken := cloneGeometry(t, geo)
	broken.Nodes = broken.Nodes[:3]
	fs := Preflight(g, broken, m)
	require.Len(t, fs, 1)
	assert.Equal(t, CriterionCoverage, fs[0].Criterion)
	assert.Equal(t, SeverityBlocking, fs[0].Severity)
}

func TestPreflight_DetectsInjectedDefects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, g Graph, geo *Geometry)
		want   Criterion
	}{
		{"node overlap", func(t *testing.T, _ Graph, geo *Geometry) {
			yes, no := idx(geo, "yes"), idx(geo, "no")
			geo.Nodes[no].Box.X = geo.Nodes[yes].Box.X + 10
			geo.Nodes[no].Box.Y = geo.Nodes[yes].Box.Y
		}, CriterionCoverage},
		{"route through unrelated node", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[0] // start → gate
			yes := geo.Nodes[idx(geo, "yes")].Box
			src, dst := e.Points[0], e.Points[len(e.Points)-1]
			// detour: down, across through "yes", back, down
			mid := yes.CenterY()
			e.Points = []Point{src, {X: src.X, Y: mid}, {X: yes.CenterX(), Y: mid}, {X: yes.CenterX(), Y: mid + 1}, {X: dst.X, Y: mid + 1}, dst}
		}, CriterionEdgeThroughNode},
		{"label over a node", func(t *testing.T, _ Graph, geo *Geometry) {
			yes := geo.Nodes[idx(geo, "yes")].Box
			geo.Edges[1].Text.Box.X, geo.Edges[1].Text.Box.Y = yes.X+5, yes.Y+5
		}, CriterionTextCollision},
		{"label too small for its text", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Edges[1].Text.Box.W = 8
		}, CriterionTextClipped},
		{"node too small for its text", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Nodes[idx(geo, "gate")].Box.H = 20
		}, CriterionCoverage},
		{"merged lines", func(t *testing.T, _ Graph, geo *Geometry) {
			// draw edge 4 (no→join) exactly over edge 3 (yes→join)
			geo.Edges[4].Points = append([]Point(nil), geo.Edges[3].Points...)
			geo.Edges[4].SourceAttach.Point = geo.Edges[4].Points[0]
			geo.Edges[4].TargetAttach.Point = geo.Edges[4].Points[len(geo.Edges[4].Points)-1]
			geo.Edges[4].SourceAttach.NodeID = "no"
		}, CriterionCoverage},
		{"attachment off the node", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[5]
			e.SourceAttach.Point.Y -= 7
			e.Points[0].Y -= 7
		}, CriterionCoverage},
		{"arrow points away from target", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[5] // join → end
			last := e.Points[len(e.Points)-1]
			e.Points = append(e.Points[:len(e.Points)-1], Point{X: last.X, Y: last.Y + 30}, last)
		}, CriterionArrowDirection},
		{"upward forward edge", func(t *testing.T, _ Graph, geo *Geometry) {
			end := idx(geo, "end")
			start := geo.Nodes[idx(geo, "start")].Box
			geo.Nodes[end].Box.Y = start.Y - 200
			geo.Nodes[end].Box.Y = max(geo.Nodes[end].Box.Y, 0)
		}, CriterionCoverage},
		{"loop return without gutter", func(t *testing.T, _ Graph, geo *Geometry) {
			e := &geo.Edges[7]
			src, dst := e.Points[0], e.Points[len(e.Points)-1]
			e.Points = []Point{src, {X: src.X, Y: src.Y + 5}, {X: dst.X, Y: src.Y + 5}, {X: dst.X, Y: dst.Y - 5}, {X: dst.X - 1, Y: dst.Y - 5}, {X: dst.X - 1, Y: dst.Y}}
			e.TargetAttach.Point = Point{X: dst.X - 1, Y: dst.Y}
		}, CriterionIndistinctLoop},
		{"label detached from its edge", func(t *testing.T, _ Graph, geo *Geometry) {
			geo.Edges[1].Text.Box.X += 400
			geo.Canvas.W += 500
		}, CriterionIndistinctBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, base, m := preflightFixture(t)
			geo := cloneGeometry(t, base)
			tc.mutate(t, g, &geo)
			got := criteria(Preflight(g, geo, m))
			assert.Greater(t, got[tc.want], 0, "want a blocking %s finding, got %v", tc.want, got)
		})
	}
}

func idx(geo *Geometry, id string) int {
	for i, n := range geo.Nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

func TestPreflight_UntreatedCrossingK22(t *testing.T) {
	g := edgeGraph(t, [2]string{"a", "c"}, [2]string{"a", "d"}, [2]string{"b", "c"}, [2]string{"b", "d"})
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(g, m, c, DefaultOptions())
	require.NoError(t, err)
	require.NotEmpty(t, geo.Crossings, "this fixed two-rank port order requires a crossing")

	treated := Preflight(g, geo, m)
	assert.Equal(t, 0, criteria(treated)[CriterionAmbiguousCrossing], "treated crossings are fine")
	advisory := 0
	for _, f := range treated {
		if f.Criterion == CriterionCrossings {
			advisory++
			assert.Equal(t, SeverityAdvisory, f.Severity)
		}
	}
	assert.Equal(t, 1, advisory, "remaining crossings are reported as advisory")

	untreated := cloneGeometry(t, geo)
	untreated.Crossings = nil
	assert.Greater(t, criteria(Preflight(g, untreated, m))[CriterionAmbiguousCrossing], 0)

	noTreatment := cloneGeometry(t, geo)
	noTreatment.Contract.CrossingTreatment = "none"
	assert.Greater(t, criteria(Preflight(g, noTreatment, m))[CriterionAmbiguousCrossing], 0)
}

func TestPreflight_UnreadableContract(t *testing.T) {
	g, geo, m := preflightFixture(t)
	small := cloneGeometry(t, geo)
	small.Contract.NodeTypeFontPx = 8 // 8 × 0.9 = 7.2px < 9px
	m.Contract = small.Contract // this test isolates readability, not measurement mismatch
	fs := Preflight(g, small, m)
	assert.Greater(t, criteria(fs)[CriterionTextUnreadable], 0)
	var ev string
	for _, f := range fs {
		if f.Criterion == CriterionTextUnreadable {
			ev = f.Evidence
		}
	}
	assert.True(t, strings.Contains(ev, "type caption"))
}

func TestPreflight_MissingMeasurementIsCoverage(t *testing.T) {
	g, geo, m := preflightFixture(t)
	delete(m.Items, NodeLabelKey("gate"))
	assert.Greater(t, criteria(Preflight(g, geo, m))[CriterionCoverage], 0)
}

func TestCrossingNearBend(t *testing.T) {
	routes := []EdgeRoute{
		{Ordinal: 0, Points: []Point{{X: 0, Y: 100}, {X: 200, Y: 100}, {X: 200, Y: 300}}},
		{Ordinal: 1, Points: []Point{{X: 196, Y: 0}, {X: 196, Y: 300}}},
		{Ordinal: 2, Points: []Point{{X: 100, Y: 0}, {X: 100, Y: 300}}},
	}
	radius := DefaultContract().CrossingGapPx/2 + DefaultContract().StrokePx
	assert.True(t, crossingNearBend(Crossing{Point: Point{X: 196, Y: 100}, Over: 0, Under: 1}, routes, radius), "4px from edge 0's bend")
	assert.False(t, crossingNearBend(Crossing{Point: Point{X: 100, Y: 100}, Over: 0, Under: 2}, routes, radius))
}

func TestSegmentHitsRect(t *testing.T) {
	r := Rect{X: 10, Y: 10, W: 100, H: 50}
	assert.True(t, segmentHitsRect(Point{X: 50, Y: 0}, Point{X: 50, Y: 100}, r))
	assert.False(t, segmentHitsRect(Point{X: 10, Y: 0}, Point{X: 10, Y: 100}, r), "running along a boundary is not passing through")
	assert.True(t, segmentHitsRect(Point{X: 0, Y: 30}, Point{X: 200, Y: 30}, r))
	assert.False(t, segmentHitsRect(Point{X: 0, Y: 70}, Point{X: 200, Y: 70}, r))
}
```

Crossing treatment is covered deterministically by `TestPreflight_UntreatedCrossingK22` (the constrained two-rank K2,2 fixture produces a crossing).

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestPreflight|TestCrossingNearBend|TestSegmentHitsRect' -v`
Expected: build failure `undefined: Preflight`, `undefined: crossingNearBend`, `undefined: segmentHitsRect`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/preflight.go`:

```go
package layout

import (
	"fmt"
	"math"
	"sort"
)

type addFinding func(c Criterion, sev Severity, nodes []string, edges []int, format string, args ...any)

// Preflight checks a candidate against the acceptance criteria using geometry
// and measurements only. It is not visual review: a candidate with no
// preflight findings must still pass rendered readability review.
func Preflight(g Graph, geo Geometry, m Measurements) []Finding {
	fs := []Finding{}
	add := func(c Criterion, sev Severity, nodes []string, edges []int, format string, args ...any) {
		fs = append(fs, Finding{Criterion: c, Severity: sev, Source: SourcePreflight, NodeIDs: nodes, EdgeOrdinals: edges, Evidence: fmt.Sprintf(format, args...)})
	}
	if err := geo.Validate(g); err != nil {
		add(CriterionCoverage, SeverityBlocking, nil, nil, "%v", err)
		return fs
	}
	if m.Contract != geo.Contract || !m.FontsReady {
		add(CriterionCoverage, SeverityBlocking, nil, nil, "measurements do not match candidate contract or fonts are not ready")
		return fs
	}
	checkReadable(geo.Contract, add)
	checkNodeOverlap(geo, add)
	checkRoutesAvoidNodes(geo, add)
	checkLabels(geo, add)
	checkTextFits(geo, m, add)
	checkMergedLines(geo, add)
	checkAttachments(geo, add)
	checkArrows(geo, add)
	checkCrossings(geo, add)
	checkProgression(geo, add)
	checkLoops(g, geo, add)
	return fs
}

func checkReadable(c RendererContract, add addFinding) {
	for _, t := range []struct {
		name string
		px   float64
	}{{"node label", c.NodeLabelFontPx}, {"node type caption", c.NodeTypeFontPx}, {"branch label", c.EdgeLabelFontPx}} {
		if t.px*c.PracticalZoom < c.MinReadableTextPx {
			add(CriterionTextUnreadable, SeverityBlocking, nil, nil,
				"%s renders at %.1fpx at practical zoom %.2f (minimum %.1fpx)", t.name, t.px*c.PracticalZoom, c.PracticalZoom, c.MinReadableTextPx)
		}
	}
}

func checkNodeOverlap(geo Geometry, add addFinding) {
	half := geo.Contract.MinClearancePx / 2
	for i := range geo.Nodes {
		for j := i + 1; j < len(geo.Nodes); j++ {
			a, b := geo.Nodes[i], geo.Nodes[j]
			if a.Box.Inflate(half).Overlaps(b.Box.Inflate(half)) {
				add(CriterionNodeOverlap, SeverityBlocking, []string{a.ID, b.ID}, nil,
					"%q and %q overlap or are closer than %.0fpx", a.ID, b.ID, geo.Contract.MinClearancePx)
			}
		}
	}
}

// segmentHitsRect reports whether an axis-aligned segment passes through the
// interior of r (running along r's boundary does not count).
func segmentHitsRect(a, b Point, r Rect) bool {
	if nearly(a.X, b.X) {
		y1, y2 := math.Min(a.Y, b.Y), math.Max(a.Y, b.Y)
		return a.X > r.X && a.X < r.Right() && y1 < r.Bottom() && y2 > r.Y
	}
	if nearly(a.Y, b.Y) {
		x1, x2 := math.Min(a.X, b.X), math.Max(a.X, b.X)
		return a.Y > r.Y && a.Y < r.Bottom() && x1 < r.Right() && x2 > r.X
	}
	return true // non-orthogonal segments are rejected by Validate
}

func checkRoutesAvoidNodes(geo Geometry, add addFinding) {
	for _, e := range geo.Edges {
		last := len(e.Points) - 1
		for s := 1; s <= last; s++ {
			for _, n := range geo.Nodes {
				if (s == 1 && n.ID == e.Source) || (s == last && n.ID == e.Target) {
					continue
				}
				if segmentHitsRect(e.Points[s-1], e.Points[s], n.Box) {
					add(CriterionEdgeThroughNode, SeverityBlocking, []string{n.ID}, []int{e.Ordinal},
						"edge %d (%s→%s) segment %d passes through %q", e.Ordinal, e.Source, e.Target, s, n.ID)
				}
			}
		}
	}
}

func checkLabels(geo Geometry, add addFinding) {
	clear := geo.Contract.MinClearancePx / 2
	for _, e := range geo.Edges {
		if e.Text == nil {
			continue
		}
		box := e.Text.Box
		for _, n := range geo.Nodes {
			if box.Inflate(clear).Overlaps(n.Box.Inflate(clear)) {
				add(CriterionTextCollision, SeverityBlocking, []string{n.ID}, []int{e.Ordinal}, "label of edge %d overlaps node %q", e.Ordinal, n.ID)
			}
		}
		for _, o := range geo.Edges {
			if o.Text == nil || o.Ordinal <= e.Ordinal {
				continue
			}
			if box.Inflate(clear).Overlaps(o.Text.Box.Inflate(clear)) {
				add(CriterionTextCollision, SeverityBlocking, nil, []int{e.Ordinal, o.Ordinal}, "labels of edges %d and %d overlap", e.Ordinal, o.Ordinal)
			}
		}
		for _, o := range geo.Edges {
			if o.Ordinal == e.Ordinal {
				continue
			}
			for s := 1; s < len(o.Points); s++ {
				if segmentHitsRect(o.Points[s-1], o.Points[s], box) {
					add(CriterionTextCollision, SeverityBlocking, nil, []int{e.Ordinal, o.Ordinal}, "edge %d passes through the label of edge %d", o.Ordinal, e.Ordinal)
					break
				}
			}
		}
		// Labels are beside their own route, never centered over its stroke.
		// Compute the minimum distance from the label Rect to each orthogonal
		// segment of its own edge. Require the nearest segment to be within
		// geo.Options.LabelGap+geomEps, and no foreign segment to be equally
		// close or closer (ties are ambiguous). Distances use the full segment,
		// not only vertices; inside/intersection distance is zero.
		associated := labelAssociated(geo, e)
		if !associated {
			add(CriterionIndistinctBranch, SeverityBlocking, nil, []int{e.Ordinal}, "label %q is not attached to its own edge %d", e.Text.Text, e.Ordinal)
		}
	}
}

func checkTextFits(geo Geometry, m Measurements, add addFinding) {
	c := geo.Contract
	for _, n := range geo.Nodes {
		label, err1 := m.Metric(NodeLabelKey(n.ID))
		caption, err2 := m.Metric(NodeTypeKey(n.ID))
		if err1 != nil || err2 != nil {
			add(CriterionCoverage, SeverityBlocking, []string{n.ID}, nil, "missing text measurement for %q", n.ID)
			continue
		}
		innerW := n.Box.W - 2*c.NodePaddingX - 2*c.NodeBorderPx
		innerH := n.Box.H - 2*c.NodeBorderPx - c.NodeTypeBarPx - 2*c.NodePaddingY
		if n.TypeCaption != "" { innerH -= c.NodeTextGapPx }
		if innerW+0.01 < math.Ceil(math.Max(label.Width, caption.Width)) || innerH+0.01 < math.Ceil(label.Height)+math.Ceil(caption.Height) {
			add(CriterionTextClipped, SeverityBlocking, []string{n.ID}, nil, "box %.0f×%.0f is smaller than the text of %q", n.Box.W, n.Box.H, n.ID)
		}
	}
	for _, e := range geo.Edges {
		if e.Text == nil {
			continue
		}
		tm, err := m.Metric(EdgeLabelKey(e.Ordinal))
		if err != nil {
			add(CriterionCoverage, SeverityBlocking, nil, []int{e.Ordinal}, "missing text measurement for edge %d", e.Ordinal)
			continue
		}
		if e.Text.Box.W-2*c.EdgeLabelPaddingX+0.01 < math.Ceil(tm.Width) || e.Text.Box.H-2*c.EdgeLabelPaddingY+0.01 < math.Ceil(tm.Height) {
			add(CriterionTextClipped, SeverityBlocking, nil, []int{e.Ordinal}, "label box of edge %d is smaller than %q", e.Ordinal, e.Text.Text)
		}
	}
}

func checkMergedLines(geo Geometry, add addFinding) {
	minSep := geo.Contract.StrokePx + geo.Contract.MinClearancePx
	segs := segmentsOf(geo.Edges)
	reported := map[[2]int]bool{}
	for i := range segs {
		for j := i + 1; j < len(segs); j++ {
			a, b := segs[i], segs[j]
			if a.ord == b.ord || a.horiz != b.horiz {
				continue
			}
			var gap, overlap float64
			if a.horiz {
				gap = math.Abs(a.a.Y - b.a.Y)
				overlap = math.Min(math.Max(a.a.X, a.b.X), math.Max(b.a.X, b.b.X)) - math.Max(math.Min(a.a.X, a.b.X), math.Min(b.a.X, b.b.X))
			} else {
				gap = math.Abs(a.a.X - b.a.X)
				overlap = math.Min(math.Max(a.a.Y, a.b.Y), math.Max(b.a.Y, b.b.Y)) - math.Max(math.Min(a.a.Y, a.b.Y), math.Min(b.a.Y, b.b.Y))
			}
			key := [2]int{min(a.ord, b.ord), max(a.ord, b.ord)}
			if gap < minSep && overlap > 0.5 && !reported[key] {
				reported[key] = true
				add(CriterionMergedLines, SeverityBlocking, nil, []int{key[0], key[1]},
					"edges %d and %d run %.1fpx apart for %.0fpx, concealing their destinations", key[0], key[1], gap, overlap)
			}
		}
	}
}

func checkAttachments(geo Geometry, add addFinding) {
	boxes := map[string]Rect{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n.Box
	}
	type att struct {
		ord   int
		along float64
	}
	groups := map[string][]att{}
	for _, e := range geo.Edges {
		for _, a := range []Attachment{e.SourceAttach, e.TargetAttach} {
			if !onSide(a.Point, a.Side, boxes[a.NodeID]) {
				add(CriterionAmbiguousAttachment, SeverityBlocking, []string{a.NodeID}, []int{e.Ordinal},
					"edge %d attaches off the %s side of %q", e.Ordinal, a.Side, a.NodeID)
				continue
			}
			along := a.Point.X
			if a.Side == SideLeft || a.Side == SideRight {
				along = a.Point.Y
			}
			k := a.NodeID + "|" + string(a.Side)
			groups[k] = append(groups[k], att{ord: e.Ordinal, along: along})
		}
	}
	minSep := geo.Contract.StrokePx + geo.Contract.MinClearancePx
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		as := groups[k]
		sort.SliceStable(as, func(i, j int) bool { return as[i].along < as[j].along })
		for i := 1; i < len(as); i++ {
			if as[i].along-as[i-1].along < minSep {
				add(CriterionAmbiguousAttachment, SeverityBlocking, nil, []int{as[i-1].ord, as[i].ord},
					"edges %d and %d share an attachment point on %s", as[i-1].ord, as[i].ord, k)
			}
		}
	}
}

func checkArrows(geo Geometry, add addFinding) {
	for _, e := range geo.Edges {
		n := len(e.Points)
		p, q := e.Points[n-2], e.Points[n-1]
		in := false
		switch e.TargetAttach.Side {
		case SideTop:
			in = nearly(p.X, q.X) && q.Y > p.Y
		case SideBottom:
			in = nearly(p.X, q.X) && q.Y < p.Y
		case SideLeft:
			in = nearly(p.Y, q.Y) && q.X > p.X
		case SideRight:
			in = nearly(p.Y, q.Y) && q.X < p.X
		}
		a, b := e.Points[0], e.Points[1]
		out := false
		switch e.SourceAttach.Side {
		case SideBottom:
			out = nearly(a.X, b.X) && b.Y > a.Y
		case SideTop:
			out = nearly(a.X, b.X) && b.Y < a.Y
		case SideRight:
			out = nearly(a.Y, b.Y) && b.X > a.X
		case SideLeft:
			out = nearly(a.Y, b.Y) && b.X < a.X
		}
		if !in || !out {
			add(CriterionArrowDirection, SeverityBlocking, []string{e.Source, e.Target}, []int{e.Ordinal},
				"edge %d does not leave %q and enter %q through their attachment sides", e.Ordinal, e.Source, e.Target)
		}
	}
}

// crossingNearBend reports whether a crossing lies within radius of any vertex
// of either crossing edge, where a gap could be mistaken for a junction.
func crossingNearBend(c Crossing, routes []EdgeRoute, radius float64) bool {
	for _, r := range routes {
		if r.Ordinal != c.Over && r.Ordinal != c.Under {
			continue
		}
		for _, p := range r.Points {
			if math.Hypot(p.X-c.Point.X, p.Y-c.Point.Y) < radius {
				return true
			}
		}
	}
	return false
}

// checkCrossings implements the required proper-crossing and bend/T-touch
// algorithm in Required semantic, adversarial and sample RED probes below.
// Build a compiling no-op only for behavioral RED, then implement every case.
func checkCrossings(geo Geometry, add addFinding) { /* behavioral RED stub */ }

func samePointWithin(a, b Point, eps float64) bool {
	return math.Abs(a.X-b.X) <= eps && math.Abs(a.Y-b.Y) <= eps
}

func checkProgression(geo Geometry, add addFinding) {
	boxes := map[string]Rect{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n.Box
	}
	for _, e := range geo.Edges {
		if e.Kind != EdgeForward {
			continue
		}
		if boxes[e.Target].Y <= boxes[e.Source].Bottom() {
			add(CriterionUpwardProgression, SeverityBlocking, []string{e.Source, e.Target}, []int{e.Ordinal},
				"forward edge %d leads from %q to %q, which is not below it", e.Ordinal, e.Source, e.Target)
			continue
		}
		for i := 1; i < len(e.Points); i++ {
			if e.Points[i].Y < e.Points[i-1].Y-0.01 {
				add(CriterionUpwardProgression, SeverityBlocking, nil, []int{e.Ordinal}, "forward edge %d climbs at segment %d", e.Ordinal, i)
				break
			}
		}
	}
}

// Implement all classification, upward gutter, declared-side, overlapping
// lane and self-loop separation checks in the required RED matrix below.
func checkLoops(g Graph, geo Geometry, add addFinding) { /* behavioral RED stub */ }

```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestPreflight|TestCrossingNearBend|TestSegmentHitsRect' -v`
Expected: `--- PASS` for `TestPreflight_ArrangedFixtureHasNoBlockingFindings`, `TestPreflight_InvalidGeometryIsACoverageFinding`, `TestPreflight_DetectsInjectedDefects` (11 subtests), `TestPreflight_UntreatedCrossingK22`, `TestPreflight_UnreadableContract`, `TestPreflight_MissingMeasurementIsCoverage`, `TestCrossingNearBend`, `TestSegmentHitsRect`; `ok`.

If an injected-defect subtest fails because the mutated geometry no longer validates (Preflight then returns only `coverage`), log `Preflight(g, geo, m)` — structurally invalid mutations intentionally expect coverage. Never relax Validate to force a more specific criterion. Semantic checks are exercised independently on valid fixtures and by the focused helper tests below.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/preflight.go domain/layout/preflight_test.go
git commit -m "feat(layout): geometric preflight for every readability criterion" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.3: Acceptance samples pass preflight

**Files:**
- Test: `domain/layout/samples_geometry_test.go`

**Interfaces:** consumes `Arrange`, `Preflight`, `BlockingCount`, `loadSample`, `newFakeMeasurer`.

This is the geometric half of the spec's acceptance examples (feature-pipeline = complex, pr-review = simpler, ralph-loop = loops). The rendered half runs through the real browser in P3T7.

- [x] **Step 1: Write the test**

Create `domain/layout/samples_geometry_test.go`:

```go
package layout

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func arrangeSample(t *testing.T, name string) (Graph, Geometry, Measurements) {
	t.Helper()
	g, err := BuildGraph(loadSample(t, name))
	require.NoError(t, err)
	c := DefaultContract()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(g, m, c, DefaultOptions())
	require.NoError(t, err)
	return g, geo, m
}

func TestSamples_PassGeometricPreflight(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "pr-review", "ralph-loop", "harness-update"} {
		t.Run(name, func(t *testing.T) {
			g, geo, m := arrangeSample(t, name)
			fs := Preflight(g, geo, m)
			for _, f := range fs {
				if f.Severity == SeverityBlocking {
					t.Errorf("blocking finding on %s: %+v", name, f)
				}
			}
			assert.Equal(t, 0, BlockingCount(fs))
			t.Logf("%s: %d nodes, %d edges, canvas %.0f×%.0f, %d crossings", name, len(geo.Nodes), len(geo.Edges), geo.Canvas.W, geo.Canvas.H, len(geo.Crossings))
		})
	}
}

func TestSamples_FeaturePipelineShape(t *testing.T) {
	_, geo, _ := arrangeSample(t, "feature-pipeline")
	assert.Len(t, geo.Nodes, 66)
	assert.Len(t, geo.Edges, 79)
	assert.Greater(t, geo.Canvas.H, geo.Canvas.W, "continuous downward progression: long workflows scroll vertically")
}

func TestSamples_PRReviewShape(t *testing.T) {
	_, geo, _ := arrangeSample(t, "pr-review")
	assert.Len(t, geo.Nodes, 19)
	assert.Greater(t, geo.Canvas.H, geo.Canvas.W)
}

func TestSamples_RalphLoopHasDistinctLoopChannels(t *testing.T) {
	_, geo, _ := arrangeSample(t, "ralph-loop")
	type span struct {
		lane   int
		side   Side
		y1, y2 float64
		ord    int
	}
	var loops []span
	for _, e := range geo.Edges {
		if e.Kind != EdgeLoopReturn {
			continue
		}
		y1, y2 := e.Points[0].Y, e.Points[0].Y
		for _, p := range e.Points {
			y1, y2 = min(y1, p.Y), max(y2, p.Y)
		}
		loops = append(loops, span{lane: e.Lane, side: e.Side, y1: y1, y2: y2, ord: e.Ordinal})
	}
	require.NotEmpty(t, loops, "ralph-loop must contain loop returns")
	for i := range loops {
		for j := i + 1; j < len(loops); j++ {
			a, b := loops[i], loops[j]
			if a.side == b.side && a.y1 < b.y2 && b.y1 < a.y2 {
				assert.NotEqual(t, a.lane, b.lane, "loop returns %d and %d overlap vertically on the same side and must use distinct lanes", a.ord, b.ord)
			}
		}
	}
}
```

- [x] **Step 2: Run it**

> Resolved (production provenance follow-up): the coordinator, saved-manifest worker/store, composition root and browser producers now exist. `TestLayoutPreflight_RealBrowserSavedFindingsAndScoreReachView` drives all four actual acceptance samples through seeding → initDomain → browser measurement → coordinator Preflight/ScoreOf → real render/review → SQLite → Reader. Every returned geometry has zero blocking preflight findings, with nonempty crossing advisories for the three branching samples. Stored findings match recomputation using actual browser measurements, scores independently count actual findings/crossings without double-counting, and both preflight and review findings reach the display sink for the exact saved revision. Removing the production Preflight call, removing ScoreOf, and ignoring its blocking input each fail the real-root regression. This closes P1T7's missing-production-input gate, not P3T7/P3T14's rendered release gate: feature-pipeline and ralph-loop still retain their genuine blocking rendered findings and needs_attention status; no reviewer threshold, evidence rule or pass requirement was relaxed.

Run: `go test ./domain/layout/ -run TestSamples_ -v`
Expected: `--- PASS` for `TestSamples_PassGeometricPreflight` (4 subtests, each logging its canvas size and crossing count), `TestSamples_FeaturePipelineShape`, `TestSamples_PRReviewShape`, `TestSamples_RalphLoopHasDistinctLoopChannels`; `ok`.

If `TestSamples_PassGeometricPreflight/feature-pipeline` reports a blocking finding, it is an arranger defect (P1T6), not a test to relax: add its exact edge/label shape as a focused TestArrange regression, then fix the reserved channel/label geometry. Do not restore cycle-breaking precedence code.

- [x] **Step 3: Commit**

```bash
git add domain/layout/samples_geometry_test.go
git commit -m "test(layout): acceptance samples arrange with zero blocking preflight findings" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Producers of inputs:** candidate geometry from `Arrange` (P1T6) over the captured manifest (P2 pipeline) and browser measurements (P3T5).
- **Consumers / wiring:** `Preflight` and `ScoreOf` are called by `Coordinator.Run` (P1T9) on every attempt; findings are persisted with the best candidate (P2T8 → P2T6) and shown in both views (P3T8 → P3T9/P3T10 → P3T11 `LayoutStatusPanel`). The same `Finding` vocabulary is produced by the rendered reviewer (P3T6), so `Refine` (P1T8) responds to both.
- **Provenance probe:** `TestSamples_PassGeometricPreflight` runs on the real samples; P3T7's composition-root acceptance test asserts the same samples reach `passed` only after rendered review in a real browser.


### Required semantic, adversarial and sample RED probes

- Before implementation, add direct checkNodeOverlap/checkMergedLines/checkAttachments/
  checkProgression tests that exercise the intended criterion independently of structural
  validation. Invalid whole geometries still return only coverage. Add structurally valid
  close-but-not-overlapping nodes/parallel lanes and valid upward interior route segments
  so the aggregate Preflight path also proves blocking semantics.
- Add labelAssociated tests for every edge kind: label beside a segment, detached label,
  exactly equidistant foreign segment, another edge nearer, overlapping label and nonfinite
  measurements. Never exempt self-loop labels from edge proximity checks.
- checkCrossings must also enumerate perpendicular contacts at bends/endpoints/T touches,
  excluding only true boundary attachments on the same node. Add a blocking
  ambiguous_crossing finding for these contacts; findCrossings deliberately lists proper
  interior crossings only. Detect adjacent gaps whose intervals overlap on the under-path,
  false/duplicate annotations and gap/arrow interference. The presence of a gap annotation
  alone cannot make these contacts advisory. Geometry-invalid annotations return coverage.
- checkLoops must require an upward vertical on the declared gutter side outside the full
  node extent, with positive lane metadata, rather than accepting any exterior vertical.
  Exercise wrong declared side, a gutter below rather than above, two returns sharing a
  lane, self-loop ports too close, and overlapping/intersecting loop bodies.
- Geometric checks cannot establish DOM text, contrast or raster readability. Inject
  visual-only defects in P3T6/P3T7 and prove this preflight may be clean while review blocks.
  An advisory crossing is allowed only after P1T6's bounded complete-route selection;
  add a route-search fixture with a removable crossing and assert the selected geometry
  has fewer crossings before any coordinator can pass it.
- Sample tests characterize P1T6/P1T7 and may start green. To observe RED, remove one dummy
  or fanout reserve, overlay a saved branch label, and remove gap annotations in isolated
  mutation probes; each relevant acceptance assertion must fail before restoring it.
  Run all focused tests then go test ./domain/layout. No browser fake counts as acceptance.

## Execution evidence

- Selection: P1T4–P1T6 still lack their previously recorded downstream production dependencies. Skipped those blocked gates and implemented only P1T7's independent steps.
- Task 7.1 RED: missing symbols, then compiling zero-count/zero-score/false-comparator stubs failed the prescribed assertions. GREEN: typed findings/JSON, blocking counts and lexicographic `(Blocking, Advisory+Crossings, Attempt)` scoring; crossing advisories are not double-counted.
- Task 7.2 RED: missing symbols, then compiling no-op helpers failed coverage, defect, proximity, crossing, lane, measurement and direction assertions. Direct semantic helper tests preceded implementation; the full test set now covers: close nodes/parallel lanes/ports, upward interiors, every edge kind's label proximity and foreign ties, nonfinite/zero/style-mismatched metrics, bends/T/endpoints, overlapping gaps, arrow interference, invalid annotations, wrong classification/side/lane, downward-only gutters, full-node extent and intersecting self bodies. Structurally valid aggregate fixtures independently assert semantic blocking. Invalid annotations still return only coverage.
- Two prescribed injected routes were structurally invalid (diagonal segment and off-side target). Logged their coverage findings and retained strict validation; separate valid route-through-node and wrong-gutter-side probes assert the specific semantic criteria. The no-treatment test updates the matching measurement contract to avoid testing only contract mismatch.
- Task 7.3 characterization: all four real sample manifests pass preflight. Feature-pipeline: 66 nodes/79 edges, 1456×9061, 52 crossings; pr-review: 19/19, 615×2330, 2 crossings; ralph-loop: 37/42, 883×4492, 15 crossings; harness-update: 1/0, 260×140. These are geometric/unit results, not browser acceptance.
- Self-review 1 (spec compliance): checked finding values/tags and shared signatures; all geometric criteria and full contract/font/measurement coverage are implemented, including full-segment label association for self loops. No opt-out, policy option, fake production adapter or visual-pass claim was added. Render-only proof and production wiring remain explicitly blocked above.
- Self-review 2 (anti-green-theater): replaced an out-of-range duplicate-return test with two genuinely classified returns, valid geometry and an exact lane-finding assertion, so classification cannot make the lane test pass accidentally. Real samples reject saved label overlays and missing gap annotations. Temporarily removing the pre-fanout label band failed all three branching sample acceptance assertions; temporarily overlaying a branch label and removing gap annotations each failed those same clean-preflight assertions. Removing only fanout track increments or source transit offsets did not make these sample shapes ambiguous, so those were not counted as successful mutation probes. A fixed removable-crossing fixture asserts fewer actual routed crossings; disabling ordering-prefix search made it fail (59 was not less than 59). All temporary mutations were restored. The missing real-root sink is a blocker, not a unit-test substitute.
- Self-review 3 (quality): read neighboring geometry/sizing/routing implementations; reused finite/metric validation and geometry tolerance, removed an unused helper, shared parallel-distance calculations and avoided repeated edge scans in self-loop comparisons. Finding order is deterministic and no geometry/measurement input is mutated.
- Verification at the initial implementation pass: prescribed finding/preflight/sample commands, all additional semantic probes, `go test ./domain/layout/`, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` and `git diff --check`. Unrelated untracked `docs/visuals/` is preserved and excluded. Step 7.3.2 was then left unchecked solely for the assigned production/browser gate, resolved below.

## Production provenance follow-up

- Selected the first remaining unchecked task, P1T7. Its recorded missing-producer blocker is now resolvable; no later task was implemented. Existing production code already supplies the required behavior, so this pass adds the missing real-root regression rather than a duplicate implementation.
- RED: removing `Preflight(g, geo, m)` from the real coordinator produced nil persisted preflight and lost the required crossing advisory on pr-review. Restoring it and disconnecting `ScoreOf` produced a zero persisted crossing count/attempt. Restoring that call and mutating ScoreOf to ignore blocking findings persisted Blocking=0 instead of 2 for ralph-loop. Each was an actual browser/SQLite/Reader sink failure, not a compile error. All mutations restored. The initial characterization also caught an incorrect test expectation for human-readable evidence wording; corrected the test to check its crossing count without inventing a new wire contract, and did not count that as behavioral RED.
- GREEN: all four real samples have zero blocking preflight findings. Current best results carry 29, 2, 15 and 0 crossings for feature-pipeline, pr-review, ralph-loop and harness-update respectively. pr-review/harness-update pass actual rendered review; feature-pipeline/ralph-loop truthfully remain needs_attention. Exact best attempts may vary with raster review and are bound to each returned evidence capture rather than pinned to a brittle number.
- Self-review 1 — spec compliance: restricted changes to P1T7's production provenance gate and tests. Asserted real measured samples, typed finding source/evidence, crossing advisory, exact candidate-local score, review even with preflight findings, saved revision/result generation and unchanged manifest bytes. No new flag, fake producer, root override, geometry policy or claim that all samples pass rendered review.
- Self-review 2 — anti-green-theater: independently require nonempty crossing data/advisories, independently count blocking/advisory/crossing score fields, bind the score attempt to actual immutable PNG capture paths, verify PNG hashes, and compare findings at both persisted record and Reader sinks. The three production mutations above fail these assertions; nil browser/wiring also cannot reach the required terminal geometry/evidence. Existing focused semantic/adversarial and sample mutation tests remain in the gate.
- Self-review 3 — code quality: followed neighboring layout_*_e2e_test.go composition-root helpers and browser lifecycle, used filepath containment helpers, avoided fixed revision IDs and attempt assumptions, and left production source byte-identical after restoring mutations. No unrelated docs/visuals content is staged.
- Final verification after the three-stage self-review: prescribed finding/preflight/sample commands and the additional proximity/semantic probes pass; whole-package `go test -race ./domain/layout/ -count=1` and `go vet ./domain/layout/` pass. Browser-required `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron -run TestLayoutPreflight_ -count=1 -v` passes all four samples (103s); the final `-race` rerun also passes all four (184s), with no race reports. `gofmt -l` is empty and `git diff --check` passes.
- Concern retained: rendered branch-ink/label/crossing acceptance for feature-pipeline and ambiguous crossings for ralph-loop remain assigned to P3T7; HTTP/dashboard release acceptance remains P3T9/P3T11–P3T14. P1T7 does not hide these findings or require a fake visual pass.
