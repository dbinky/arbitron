# Workflow Auto-Arrangement — Phase 1 Task 8: Rendered-review ports and findings-driven refinement

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Alignment requirements:** Read contracts §6 (6.5) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Define the contract for reviewing every candidate's **actual rendered result** — the `Renderer` port (measure text; render a candidate with the dashboard's real components, CSS and fonts and report DOM measurements plus screenshot artifacts) and the `Reviewer` port (examine that rendering and return findings with evidence) — and the `Refine` policy that turns findings into the next attempt's options.

**Architecture:** `render.go` holds only value types and two interfaces; production implementations live in `adapter/renderreview` (P3T5 browser renderer, P3T6 readability reviewer) and are injected at the composition root, so `domain/layout` imports no adapters. `RenderReport` is the JSON produced by the dashboard render surface (P3T4) — the JSON tags here are the shared contract. `refine.go` maps each criterion present to targeted spacing/lane/label/gutter/search-budget increments with caps; when nothing applicable can change, it reports "no refinement possible", which the coordinator turns into early `needs_attention`.

**Tech Stack:** Go 1.25.5 (`context`, `time`, `encoding/json`), testify.

**Spec / Design:** spec "Review the rendered result… Candidates that fail the readability criteria are refined and reviewed again"; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §3 (rendered readability reviewer, overview + overlapping readable-scale tiles, missing evidence never implies a pass), §4 ("Findings drive targeted spacing, lane, label, loop-gutter and routing-search changes"; "proven inability to make a different candidate"), Data Provenance **PLANNED DEPENDENCY RENDER-REVIEW** (this task defines the port; P3T4–P3T7 build and wire the production producer).

---

### Task 8.1: Renderer/Reviewer ports and rendered-evidence types

**Files:**
- Create: `domain/layout/render.go`
- Test:   `domain/layout/render_test.go`

**Interfaces:**
- Consumes: `Geometry`, `Rect`, `Point`, `RendererContract` (P1T2); `MeasureRequest`, `Measurements` (P1T4); `Finding` (P1T7).
- Produces (exact, contracts §1): `RuntimeStates`, `RenderRequest`, `RenderedText`, `RenderedNode`, `RenderedEdge`, `RenderedCrossing`, `StateProbe`, `RenderReport`, `Artifact`, `Rendering`, `ReviewRequest`, `Evidence`, `Review`, `Renderer`, `Reviewer`, plus `func NewRenderRequest(revision string, attempt int, geo Geometry, nodeTypes map[string]string) RenderRequest`.

- [x] **Step 1: Write the failing test**

Create `domain/layout/render_test.go`:

```go
package layout

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dashboard render surface (P3T4) emits RenderReport JSON with these exact
// keys; this test pins the wire contract.
func TestRenderReport_JSONKeys(t *testing.T) {
	r := RenderReport{
		SurfaceVersion: ContractVersion, Contract: DefaultContract(), FontsReady: true, FontFamilyResolved: true,
		Zoom: 0.9, Canvas: Rect{W: 100, H: 200},
		Nodes: []RenderedNode{{ID: "a", Box: Rect{X: 1, Y: 2, W: 3, H: 4},
			Label: RenderedText{Box: Rect{W: 1, H: 1}, Clipped: true, FontPx: 13, Contrast: 9.1, Occluded: false},
			Type:  RenderedText{FontPx: 10}}},
		Edges:     []RenderedEdge{{Ordinal: 0, Start: Point{X: 1, Y: 2}, End: Point{X: 3, Y: 4}, HasArrow: true, Dashed: false, StrokePx: 2}},
		Crossings: []RenderedCrossing{{Point: Point{X: 5, Y: 6}, Over: 0, Under: 1, UnderGapVisible: true, OverContinuous: true}},
		States:    []StateProbe{{State: "running", NodeBoxesStable: true, LabelsReadable: true}},
	}
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"surfaceVersion", "contract", "fontsReady", "fontFamilyResolved", "zoom", "canvas", "nodes", "edges", "crossings", "states"} {
		assert.Contains(t, m, k)
	}
	node := m["nodes"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "box", "label", "type"} {
		assert.Contains(t, node, k)
	}
	label := node["label"].(map[string]any)
	for _, k := range []string{"content", "glyphs", "visible", "box", "clipped", "fontPx", "contrast", "occluded"} {
		assert.Contains(t, label, k)
	}
	edge := m["edges"].([]any)[0].(map[string]any)
	for _, k := range []string{"segments", "strokeColor", "dashPattern", "visible", "arrowPoints", "ordinal", "start", "end", "hasArrow", "dashed", "strokePx"} {
		assert.Contains(t, edge, k)
	}
	crossing := m["crossings"].([]any)[0].(map[string]any)
	for _, k := range []string{"point", "over", "under", "underGapVisible", "overContinuous"} {
		assert.Contains(t, crossing, k)
	}
	probe := m["states"].([]any)[0].(map[string]any)
	for _, k := range []string{"state", "nodeBoxesStable", "labelsReadable"} {
		assert.Contains(t, probe, k)
	}

	var back RenderReport
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, r, back)
}

func TestNewRenderRequest_ProbesEveryRuntimeState(t *testing.T) {
	geo := validTwoNodeGeometry()
	types := map[string]string{"a": "script", "b": "conditional"}
	req := NewRenderRequest("rev-1", 2, geo, types)
	assert.Equal(t, types, req.WorkflowNodeTypes)
	types["a"] = "mutated"
	assert.Equal(t, "script", req.WorkflowNodeTypes["a"], "raw types are defensively copied")
	assert.Equal(t, "rev-1", req.Revision)
	assert.Equal(t, 2, req.Attempt)
	assert.Equal(t, geo, req.Geometry)
	assert.Equal(t, []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}, req.States)

	req.States[0] = "mutated"
	assert.Equal(t, "pending", RuntimeStates[0], "callers get a copy of the state list")
}

// compile-time proof that small fakes satisfy the ports with exact signatures.
type portProbe struct{}

func (portProbe) Measure(context.Context, MeasureRequest) (Measurements, error) { return Measurements{}, nil }
func (portProbe) Render(context.Context, RenderRequest) (Rendering, error)       { return Rendering{}, nil }
func (portProbe) Review(context.Context, ReviewRequest) (Review, error)          { return Review{}, nil }

var (
	_ Renderer = portProbe{}
	_ Reviewer = portProbe{}
)

func TestEvidence_JSONRoundTrip(t *testing.T) {
	ev := Evidence{GeometryDigest: "abc", RendererVersion: ContractVersion, Contract: DefaultContract(), FontsReady: true, Zoom: 0.9, Tiles: 6,
		Artifacts:  []Artifact{{Kind: "tile", Path: "/tmp/t.png", SHA256: "ff", Region: Rect{W: 10, H: 10}, Scale: 0.9}},
		ReviewedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(Review{Rendered: true, Findings: []Finding{}, Evidence: ev})
	require.NoError(t, err)
	var back Review
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, ev, back.Evidence)
	assert.True(t, back.Rendered)
}
```

- [x] **Step 2: Run it, confirm it fails**

Observed missing-symbol RED, then behavioral RED with compiling declarations lacking JSON tags and a zero-value request constructor (wire-key assertions failed).

Run: `go test ./domain/layout/ -run 'TestRenderReport_JSONKeys|TestNewRenderRequest|TestEvidence_JSONRoundTrip' -v`
Expected: build failure `undefined: RenderReport`, `undefined: Renderer`, `undefined: NewRenderRequest`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/render.go`:

```go
package layout

import (
	"context"
	"time"
)

// RuntimeStates are the execution/selection styles the rendered review must
// exercise: runtime styling may restyle a node but never resize it or obscure
// its labels and edge endpoints.
var RuntimeStates = []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}

// RenderRequest asks the renderer to draw one candidate exactly as both views
// will, then probe each runtime state.
type RenderRequest struct {
	Revision string   `json:"revision"`
	Attempt  int      `json:"attempt"`
	Geometry Geometry `json:"geometry"`
	WorkflowNodeTypes map[string]string `json:"workflowNodeTypes"`
	States   []string `json:"states"`
}

// NewRenderRequest builds a request that probes every runtime state.
func NewRenderRequest(revision string, attempt int, geo Geometry, nodeTypes map[string]string) RenderRequest {
	copied := make(map[string]string, len(nodeTypes))
	for id, typ := range nodeTypes { copied[id] = typ }
	return RenderRequest{Revision: revision, Attempt: attempt, Geometry: geo, WorkflowNodeTypes: copied, States: append([]string(nil), RuntimeStates...)}
}

// RenderedText is what the browser actually laid out for one text element, in
// canvas coordinates (CSS px at zoom 1).
type RenderedSegment struct { Start Point `json:"start"`; End Point `json:"end"` }
type RenderedText struct {
	Content string `json:"content"`
	Glyphs []Rect `json:"glyphs"`
	Visible bool `json:"visible"`
	Box      Rect    `json:"box"`
	Clipped  bool    `json:"clipped"`
	FontPx   float64 `json:"fontPx"`
	Contrast float64 `json:"contrast"`
	Occluded bool    `json:"occluded"`
}

// RenderedNode is the rendered node box and its two text elements.
type RenderedNode struct {
	ID    string       `json:"id"`
	Box   Rect         `json:"box"`
	Label RenderedText `json:"label"`
	Type  RenderedText `json:"type"`
}

// RenderedEdge is the rendered path of one edge occurrence.
type RenderedEdge struct {
	Segments []RenderedSegment `json:"segments"`
	StrokeColor string `json:"strokeColor"`
	DashPattern string `json:"dashPattern"`
	Visible bool `json:"visible"`
	ArrowPoints []Point `json:"arrowPoints"`
	Ordinal  int           `json:"ordinal"`
	Start    Point         `json:"start"`
	End      Point         `json:"end"`
	HasArrow bool          `json:"hasArrow"`
	Dashed   bool          `json:"dashed"`
	StrokePx float64       `json:"strokePx"`
	Label    *RenderedText `json:"label,omitempty"`
}

// RenderedCrossing reports whether a crossing is visibly treated: the under
// edge's stroke is interrupted at the point and the over edge's is not.
type RenderedCrossing struct {
	Point           Point `json:"point"`
	Over            int   `json:"over"`
	Under           int   `json:"under"`
	UnderGapVisible bool  `json:"underGapVisible"`
	OverContinuous  bool  `json:"overContinuous"`
}

// StateProbe is the result of re-rendering every node in one runtime state.
type StateProbe struct {
	State           string   `json:"state"`
	NodeBoxesStable bool     `json:"nodeBoxesStable"`
	LabelsReadable  bool     `json:"labelsReadable"`
	Issues          []string `json:"issues,omitempty"`
}

// RenderReport is the DOM measurement report of the render surface.
type RenderReport struct {
	SurfaceVersion     string             `json:"surfaceVersion"`
	Contract           RendererContract   `json:"contract"`
	FontsReady         bool               `json:"fontsReady"`
	FontFamilyResolved bool               `json:"fontFamilyResolved"`
	Zoom               float64            `json:"zoom"`
	Canvas             Rect               `json:"canvas"`
	Nodes              []RenderedNode     `json:"nodes"`
	Edges              []RenderedEdge     `json:"edges"`
	Crossings          []RenderedCrossing `json:"crossings"`
	States             []StateProbe       `json:"states"`
}

// Artifact is one captured screenshot: the overview, or a readable-scale tile.
type Artifact struct {
	Kind   string  `json:"kind"`
	Path   string  `json:"path"`
	SHA256 string  `json:"sha256"`
	Region Rect    `json:"region"`
	Scale  float64 `json:"scale"`
}

// Rendering is the renderer's output for one candidate.
type Rendering struct {
	Revision       string       `json:"revision"`
	Attempt        int          `json:"attempt"`
	GeometryDigest string       `json:"geometryDigest"`
	Report         RenderReport `json:"report"`
	Artifacts      []Artifact   `json:"artifacts"`
	RenderedAt     time.Time    `json:"renderedAt"`
}

// ReviewRequest hands the reviewer one candidate and its own rendering.
type ReviewRequest struct {
	Revision  string    `json:"revision"`
	Attempt   int       `json:"attempt"`
	Geometry  Geometry  `json:"geometry"`
	Rendering Rendering `json:"rendering"`
}

// Evidence ties a review to the exact candidate and renderer it examined.
type Evidence struct {
	GeometryDigest  string           `json:"geometryDigest"`
	RendererVersion string           `json:"rendererVersion"`
	Contract        RendererContract `json:"contract"`
	FontsReady      bool             `json:"fontsReady"`
	Zoom            float64          `json:"zoom"`
	Tiles           int              `json:"tiles"`
	Artifacts       []Artifact       `json:"artifacts"`
	ReviewedAt      time.Time        `json:"reviewedAt"`
}

// Review is the readability verdict for one rendered candidate. Rendered is
// false when the reviewer could not examine a complete rendering.
type Review struct {
	Rendered bool      `json:"rendered"`
	Findings []Finding `json:"findings"`
	Evidence Evidence  `json:"evidence"`
}

// Renderer draws candidates with the dashboard's real node/edge components,
// CSS and fonts. Production: adapter/renderreview.BrowserRenderer.
type Renderer interface {
	Measure(ctx context.Context, req MeasureRequest) (Measurements, error)
	Render(ctx context.Context, req RenderRequest) (Rendering, error)
}

// Reviewer examines a rendering and returns readability findings with
// evidence. Production: adapter/renderreview.ReadabilityReviewer.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (Review, error)
}
```

- [x] **Step 4: Run it, confirm it passes**

> Resolved on revisit: the assigned producers/root now exist. `TestRenderPorts_RealBrowserSavedReportReachesSink` authors Unicode node/branch text through actual initDomain/Service → SQLite/Worker/Coordinator → production BrowserRenderer/render.html/ReadabilityReviewer → persisted result/Reader. It checks actual DOM content/glyphs/visibility, SVG segments/stroke/marker outlines, all runtime states, immutable PNG hashes and unchanged saved bytes. Removing the report while retaining genuine PNGs yields persisted needs_attention/unreviewed and no display evidence. Characterization GREEN; removing NewRenderRequest's raw type input caused behavioral RED (no review reached; terminal pipeline failed); restored and reran GREEN (13.364s). Focused domain wire/value tests also pass. No production fake was installed.

Run: `go test ./domain/layout/ -run 'TestRenderReport_JSONKeys|TestNewRenderRequest|TestEvidence_JSONRoundTrip' -v`
Expected: three tests `--- PASS`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/render.go domain/layout/render_test.go
git commit -m "feat(layout): rendered-review ports and evidence contract" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8.2: `Refine` — findings to the next attempt's options

**Files:**
- Create: `domain/layout/refine.go`
- Test:   `domain/layout/refine_test.go`

**Interfaces:**
- Consumes: `Options`, `DefaultOptions` (P1T4); `Finding`, criteria, severities (P1T7).
- Produces: `func Refine(o Options, findings []Finding) (Options, bool)` — `bool` is false when no applicable change is possible (only unfixable criteria, or every relevant value at its cap).

Policy (each applied once per attempt when the criterion appears in any **blocking** finding; values capped):

| Criterion (blocking) | Change |
|---|---|
| `node_overlap` | NodeSpacing +24 |
| `edge_through_node` | NodeSpacing +16, RankSpacing +16 |
| `text_collision` | LabelGap +6, PortSpacing +12, NodeSpacing +16 |
| `text_clipped` | NodeTextSlack +8 |
| `merged_lines` | LaneSeparation +6, PortSpacing +8 |
| `ambiguous_attachment` | PortSpacing +12 |
| `ambiguous_crossing` | LaneSeparation +6, PortSpacing +8, CrossingSweeps ×2 |
| `indistinct_loop_return` | GutterWidth +24, LaneSeparation +4 |
| `indistinct_branch` | PortSpacing +12, LabelGap +6 |
| `text_unreadable`, `upward_progression`, `coverage`, `arrow_direction`, `render_mismatch` | none (contract/defect — cannot be fixed by spacing) |
| advisory `crossings` | CrossingSweeps ×2, **only** together with another applicable change |

Caps: NodeSpacing 400, RankSpacing 400, LaneSeparation 60, PortSpacing 120, GutterWidth 400, LabelGap 60, NodeTextSlack 64, CrossingSweeps 64.

- [x] **Step 1: Write the failing test**

Create `domain/layout/refine_test.go`:

```go
package layout

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func blocking(c Criterion) Finding { return Finding{Criterion: c, Severity: SeverityBlocking, Source: SourceReview} }

func TestRefine_TargetedIncrements(t *testing.T) {
	base := DefaultOptions()
	cases := []struct {
		criterion Criterion
		check     func(t *testing.T, got Options)
	}{
		{CriterionNodeOverlap, func(t *testing.T, got Options) { assert.Equal(t, base.NodeSpacing+24, got.NodeSpacing) }},
		{CriterionEdgeThroughNode, func(t *testing.T, got Options) {
			assert.Equal(t, base.NodeSpacing+16, got.NodeSpacing)
			assert.Equal(t, base.RankSpacing+16, got.RankSpacing)
		}},
		{CriterionTextCollision, func(t *testing.T, got Options) {
			assert.Equal(t, base.LabelGap+6, got.LabelGap)
			assert.Equal(t, base.PortSpacing+12, got.PortSpacing)
		}},
		{CriterionTextClipped, func(t *testing.T, got Options) { assert.Equal(t, base.NodeTextSlack+8, got.NodeTextSlack) }},
		{CriterionMergedLines, func(t *testing.T, got Options) { assert.Equal(t, base.LaneSeparation+6, got.LaneSeparation) }},
		{CriterionAmbiguousAttachment, func(t *testing.T, got Options) { assert.Equal(t, base.PortSpacing+12, got.PortSpacing) }},
		{CriterionAmbiguousCrossing, func(t *testing.T, got Options) { assert.Equal(t, base.CrossingSweeps*2, got.CrossingSweeps) }},
		{CriterionIndistinctLoop, func(t *testing.T, got Options) { assert.Equal(t, base.GutterWidth+24, got.GutterWidth) }},
		{CriterionIndistinctBranch, func(t *testing.T, got Options) { assert.Equal(t, base.PortSpacing+12, got.PortSpacing) }},
	}
	for _, tc := range cases {
		t.Run(string(tc.criterion), func(t *testing.T) {
			got, changed := Refine(base, []Finding{blocking(tc.criterion)})
			assert.True(t, changed)
			tc.check(t, got)
		})
	}
}

func TestRefine_UnfixableCriteriaProveInability(t *testing.T) {
	for _, c := range []Criterion{CriterionTextUnreadable, CriterionUpwardProgression, CriterionCoverage, CriterionArrowDirection, CriterionRenderMismatch} {
		got, changed := Refine(DefaultOptions(), []Finding{blocking(c)})
		assert.False(t, changed, "%s cannot be fixed by spacing", c)
		assert.Equal(t, DefaultOptions(), got)
	}
}

func TestRefine_AdvisoryCrossingsAloneDoNotRefine(t *testing.T) {
	got, changed := Refine(DefaultOptions(), []Finding{{Criterion: CriterionCrossings, Severity: SeverityAdvisory}})
	assert.False(t, changed)
	assert.Equal(t, DefaultOptions(), got)

	got, changed = Refine(DefaultOptions(), []Finding{blocking(CriterionNodeOverlap), {Criterion: CriterionCrossings, Severity: SeverityAdvisory}})
	assert.True(t, changed)
	assert.Equal(t, DefaultOptions().CrossingSweeps*2, got.CrossingSweeps, "with another change, also widen the search")
}

func TestRefine_AppliesEachCriterionOnce(t *testing.T) {
	got, _ := Refine(DefaultOptions(), []Finding{blocking(CriterionNodeOverlap), blocking(CriterionNodeOverlap), blocking(CriterionNodeOverlap)})
	assert.Equal(t, DefaultOptions().NodeSpacing+24, got.NodeSpacing)
}

func TestRefine_AdvisoryVersionsOfBlockingCriteriaAreIgnored(t *testing.T) {
	_, changed := Refine(DefaultOptions(), []Finding{{Criterion: CriterionNodeOverlap, Severity: SeverityAdvisory}})
	assert.False(t, changed)
}

func TestRefine_CapsEventuallyStopRefinement(t *testing.T) {
	o := DefaultOptions()
	fs := []Finding{blocking(CriterionNodeOverlap)}
	steps := 0
	for {
		next, changed := Refine(o, fs)
		if !changed {
			break
		}
		o = next
		steps++
		if steps > 100 {
			t.Fatal("refinement never terminates")
		}
	}
	assert.Equal(t, 400.0, o.NodeSpacing)
}

func TestRefine_DoesNotMutateInput(t *testing.T) {
	o := DefaultOptions()
	_, _ = Refine(o, []Finding{blocking(CriterionMergedLines)})
	assert.Equal(t, DefaultOptions(), o)
}
```

- [x] **Step 2: Run it, confirm it fails**

Observed missing-symbol RED, then behavioral RED against `return o, false`, including geometry-sink assertions. The explicit-above-cap tests also failed against a global-clamp mutation before restoration.

Run: `go test ./domain/layout/ -run TestRefine -v`
Expected: build failure `undefined: Refine`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/refine.go`:

Implement Refine with these exact targeted increments and the table above:

- Collect blocking criteria into a set; detect advisory crossings independently. Ignore
  duplicate criteria and advisory copies of a blocking-only criterion.
- Each float increment uses `if old < cap { next=min(cap,old+delta) }`; when old>=cap,
  retain old. Never globally clamp unrelated options down to a cap. Thus custom explicit
  spacing above a default refinement cap cannot shrink because another field changed.
- Crossing search growth is `if old<64 { next=max(1,min(64,2*old)) }`; handle old<=0 as 1
  without overflow. Preserve values already >=64. Apply the blocking ambiguous-crossing
  doubling once; advisory crossings may double once only if another targeted field
  actually changed and ambiguous-crossing did not already double it.
- Apply each criterion once in a fixed table order; when distinct criteria affect one
  field, sum their bounded increments in that order. Return `(next,next!=o)`.
- Do not mutate fields absent from the table (DummyWidth/Margin), input findings or the
  original Options. With only unfixable criteria return the original value and false.
  render_mismatch is unfixable by spacing; changing boxes cannot repair stale/missing
  evidence or a wrong renderer. The coordinator reports no_refinement without a pass.

Add RED tests for all fields at cap, explicit inputs above caps with another field changed,
zero crossing-search budget, mixed criteria affecting the same field, advisory-only
findings, unknown criterion and empty findings. These are deterministic pure-value cases;
there is no invented I/O error. Require each mapped field to change actual corresponding
P1T6 geometry in a focused fixture, particularly branch label clipping and gutter spacing.
Run RED against a no-op and against a global-clamp implementation, then GREEN.

- [x] **Step 4: Run it, confirm it passes**

> Resolved on revisit: `TestRefine_RealBrowserSavedBranchClippingReachesSink` drives actual initDomain authoring/SQLite/Worker/Coordinator and production browser/reviewer. Its explicit measurement-fault decorator understates only a real two-line Unicode branch; geometric preflight misses it, actual DOM glyphs expose clipping, actual review findings feed Refine, subsequent real candidates grow shared text slack and branch boxes, and the unclipped reviewed result reaches SQLite, cache and Reader without changing saved bytes. Every attempt has distinct immutable PNG evidence bound to its geometry. Initial characterization passed (18.125s); replacing Refine with a no-op failed at the terminal SQLite sink (needs_attention/no_refinement instead of passed); restoration passed (18.188s). All focused Refine tests pass too. This closes this task's provenance gate, not P3T7/P3T14's separate complex-sample/UI release gate.

Run: `go test ./domain/layout/ -run TestRefine -v`
Expected: `--- PASS` for `TestRefine_TargetedIncrements` (nine mapped criteria), `TestRefine_UnfixableCriteriaProveInability`, `TestRefine_AdvisoryCrossingsAloneDoNotRefine`, `TestRefine_AppliesEachCriterionOnce`, `TestRefine_AdvisoryVersionsOfBlockingCriteriaAreIgnored`, `TestRefine_CapsEventuallyStopRefinement`, `TestRefine_DoesNotMutateInput`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/refine.go domain/layout/refine_test.go
git commit -m "feat(layout): findings-driven, capped refinement policy" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Production producers of the ports (RENDER-REVIEW resolved by later tasks, not stubs):** `Renderer` ← `adapter/renderreview.BrowserRenderer` (P3T5), driving headless Chrome over the embedded dashboard render surface `render.html` (P3T4) which draws candidates with the shared `ArrangedNode`/`RoutedEdge` components (P3T2) and bundled Inter/Noto font assets. `Reviewer` ← `adapter/renderreview.ReadabilityReviewer` (P3T6), which examines the actual DOM report and PNG tiles. Both are constructed in `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` — P2T9 creates the seam (which fails every job visibly until P3T7), P3T7 installs the browser adapter.
- **Consumers:** `Coordinator.Run` (P1T9) calls `Measure` once, `Render` + `Review` for every candidate, and `Refine` between attempts.
- **Provenance probe:** P3T7's composition-root test with `layout.browserpath` pointing at a nonexistent binary must end the job `failed` at stage `rendering` — proving the live path uses the browser renderer, and that missing render evidence never yields `passed`.

### Alignment TDD: DOM evidence fields have an actual producer

**Files:** `domain/layout/render.go`, `render_test.go`; TS mirror/report owner P3T4.

- [x] **RED:** Extend TestRenderReport_JSONKeys/round-trip for Content/Glyphs/Visible,
  actual SVG Segments/StrokeColor/DashPattern/ArrowPoints and false visibility; a missing
  report is not treated as a successful render. Assert no field is omitted merely because
  its value is false. Use `RenderedSegment` exactly as contracts §6.5.
- [x] Run `go test ./domain/layout -run 'TestRenderReport|TestNewRenderRequest|TestEvidence' -count=1` and observe the behavioral failure before implementing the correction.
  Characterization was initially green because §6.5 fields landed in 8.1. Mutating both visibility tags to `omitempty` caused missing-key/false-value assertions to fail. Mutating NewRenderRequest to alias nodeTypes failed both copy-direction assertions. Restored both corrections and reran green.
- [x] **GREEN:** Add the §6.5 fields/types and exact JSON tags. Do not add a second report schema. P3T4 populates every new input; P3T6 consumes each.
  Go schema is complete; production producer/consumer proof remains gated below.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.

> Resolved on revisit: actual root/DOM/PNG/reviewer sinks are exercised by the tests above. Repeated negative controls: omitting both visibility wire fields when false fails the existing raw-key assertions; dropping NewRenderRequest's raw node types fails the production terminal SQLite assertion (failed/rendering, missing node type, rather than passed); disabling Refine fails the persisted passed assertion. All mutations restored, with zero production source diff. Missing report + genuine PNGs is explicitly rejected in persisted state and Reader. The existing real-browser DOM mutation→reviewer and raster feature/integrity rejection tests also pass (34.456s), not merely schema envelopes.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.


### Raw node-type provenance and report rejection cases

`RenderRequest.WorkflowNodeTypes` is produced only from immutable Job.Manifest.Nodes by
P1T9 (ID → string(Type)). P3T5 forwards it unchanged to P3T4's render job; the renderer uses
it for actual node type styles. Never reverse TypeCaption by replacing spaces with `_`: a
caption is display text, not a lossless type identifier. NewRenderRequest copies the map.
Add wire-key and defensive-copy tests; P1T9's recording renderer asserts the complete map,
and P3T5 Render rejects nil, missing, extra or caption-inconsistent entries with a visible
render-stage error. Changing an agent_call to script must fail the provenance test.

Review errors/missing PNGs are P3T6 errors, malformed complete report coverage is a blocking
review result, and a nil/empty report cannot establish evidence. P3T4 owns actual DOM
Content/Glyphs/Visible/Segments/marker geometry; tests here pin keys including false values
and Unicode content, rather than inventing a second schema or a production fake producer.

## Revisit execution evidence

The production implementation already satisfies this task now that its assigned downstream producers have landed. This pass adds regression/provenance tests only; it does not change rendering policy, coordinator behavior, schema, budgets or runtime options.

Self-review, in order:
1. **Spec compliance:** closes only P1T8's previously blocked ports/refinement/evidence gates. Uses actual saved Service input and production composition factory/browser/reviewer; no new production feature or alternate schema. Complex-sample and interactive-route release acceptance remains with P3T7/P3T14.
2. **Anti-green-theater:** asserts terminal SQLite/result, cache and Reader sinks, not call counts. Missing-report input keeps genuine PNGs but loses reviewed status. Removed raw-type input and no-op Refine each caused behavioral sink failures; restored both. Every real attempt's evidence is bound to its geometry and immutable artifact paths/hashes.
3. **Code quality:** follows neighboring root-test helpers, joins through normal root cleanup, restores factory/environment overrides, synchronizes observer snapshots and forwards every load-bearing renderer/reviewer argument. No production stub, source diff or unrelated work included.

Post-review verification:
- Both prescribed `go test ./domain/layout` commands above: PASS.
- `go test -race ./domain/layout -count=1`: PASS (15.414s).
- `go vet ./domain/layout ./cmd/arbitron`, `go build ./...`, gofmt and `git diff --check`: PASS.
- `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^(TestRenderPorts_|TestRefine_RealBrowser|TestLayoutRender_RealBrowserPRReview|TestLayoutRender_MissingBrowser)' -count=1 -v`: PASS (42.826s), no skips/races; includes an unwrapped production-factory sample and actual missing-browser/retry sink.
- `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run 'TestReadabilityReviewer_RealBrowser_DOMDefectsReachFindings|TestReviewCapture_|TestSizing_RealBrowser' -count=1`: PASS (34.456s).
- `cd dashboard && npx vitest run src/render && npx tsc --noEmit`: PASS (6 files / 84 tests; typecheck clean).
