# Workflow Auto-Arrangement — Phase 1 Task 2: Renderer contract, geometry model and validation

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Alignment requirements:** Read contracts §6 (6.1/6.6) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Define the versioned, serialisable geometry value (node boxes, routed edge occurrences, attachments, branch-label boxes, crossings, canvas) and the renderer contract (fonts, sizes, padding, stroke, markers, crossing treatment, practical zoom) shared by Go and the dashboard, plus the validation every reader runs before trusting a stored geometry.

**Architecture:** `domain/layout/contract.go` holds `RendererContract` + `DefaultContract()`; the dashboard gets the same values as `dashboard/src/graph/arranged/contract.json`, and a Go test fails if the two drift. `domain/layout/geometry.go` holds the geometry types, `Validate` (coverage, identity, finiteness, orthogonality, attachment consistency), `GeometryDigest` (ties review evidence to the exact candidate) and `Compatible` (validation + exact full renderer contract).

**Tech Stack:** Go 1.25.5 (`crypto/sha256`, `encoding/json`, `math`), testify; JSON file consumed later by TypeScript (P3T1).

**Spec / Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §1 (geometry and renderer contract), §3 (pass requires matching renderer contract), Data Provenance (LAYOUT-CORE, RENDER-REVIEW contract half).

---

### Task 2.1: Renderer contract shared with the dashboard

**Files:**
- Create: `domain/layout/contract.go`
- Create: `dashboard/src/graph/arranged/contract.json`
- Test:   `domain/layout/contract_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  ```go
  const ContractVersion = "arranged-renderer/1"
  type RendererContract struct { /* fields below, JSON tags exact */ }
  func DefaultContract() RendererContract
  ```
  `dashboard/src/graph/arranged/contract.json` — read by P3T1 (`validate.ts`), P3T2 (component styles), P3T4 (render surface `surfaceVersion`).

Values are new Phase 1 policy (design: "Defaults ... are new Phase 1 policy values"). Type caption is raised from today's 9px to 10px so that `10 × 0.9 (DEFAULT_ZOOM) = 9.0px ≥ minReadableTextPx 9`; the forward edge colour moves from `#444466` (≈1.6:1 on `#1a1a2e`) to `#8a8ab0`.

- [x] **Step 1: Write the failing test**

Create `domain/layout/contract_test.go`:

```go
package layout

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractJSONPath is the dashboard copy of the renderer contract. The Go
// arranger and the TypeScript renderer must agree on every value, so this test
// is the drift alarm between them.
var contractJSONPath = filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "contract.json")

func TestDefaultContract_MatchesDashboardContractJSON(t *testing.T) {
	raw, err := os.ReadFile(contractJSONPath)
	require.NoError(t, err, "dashboard contract.json must exist")

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fromJSON RendererContract
	require.NoError(t, dec.Decode(&fromJSON), "contract.json must only contain RendererContract keys")

	assert.Equal(t, DefaultContract(), fromJSON)
}

func TestDefaultContract_EveryFieldIsSet(t *testing.T) {
	c := DefaultContract()
	v := reflect.ValueOf(c)
	for i := 0; i < v.NumField(); i++ {
		assert.False(t, v.Field(i).IsZero(), "contract field %s must have a non-zero default", v.Type().Field(i).Name)
	}
}

func TestDefaultContract_PinnedValues(t *testing.T) {
	c := DefaultContract()
	assert.Equal(t, ContractVersion, c.Version)
	assert.Equal(t, "Inter, Noto Sans SC, Noto Emoji", c.FontFamily)
	assert.Equal(t, 0.9, c.PracticalZoom, "practical zoom starts from dashboard DEFAULT_ZOOM")
	assert.Equal(t, "gap", c.CrossingTreatment)
	assert.GreaterOrEqual(t, c.NodeTypeFontPx*c.PracticalZoom, c.MinReadableTextPx, "smallest text stays readable at practical zoom")
	assert.GreaterOrEqual(t, c.EdgeLabelFontPx*c.PracticalZoom, c.MinReadableTextPx)
	assert.GreaterOrEqual(t, c.NodeLabelFontPx*c.PracticalZoom, c.MinReadableTextPx)
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run TestDefaultContract -v`
Expected: build failure `undefined: RendererContract` / `undefined: DefaultContract`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/contract.go`:

```go
package layout

// ContractVersion identifies the renderer behaviour a geometry was arranged
// and reviewed against. A stored geometry whose contract version differs from
// the running renderer's is not displayed as reviewed (see Compatible).
const ContractVersion = "arranged-renderer/1"

// RendererContract pins every typographic and stroke value that both the Go
// arranger (sizing, routing, preflight) and the dashboard renderer (CSS,
// SVG) must agree on, so the reviewed rendering is reproducible. The dashboard
// copy lives in dashboard/src/graph/arranged/contract.json.
type RendererContract struct {
	Version           string  `json:"version"`
	FontFamily        string  `json:"fontFamily"`
	FontPackage       string  `json:"fontPackage"`
	NodeLabelFontPx   float64 `json:"nodeLabelFontPx"`
	NodeLabelWeight   int     `json:"nodeLabelWeight"`
	NodeTypeFontPx    float64 `json:"nodeTypeFontPx"`
	NodeTypeWeight    int     `json:"nodeTypeWeight"`
	EdgeLabelFontPx   float64 `json:"edgeLabelFontPx"`
	EdgeLabelWeight   int     `json:"edgeLabelWeight"`
	LineHeight        float64 `json:"lineHeight"`
	NodePaddingX      float64 `json:"nodePaddingX"`
	NodePaddingY      float64 `json:"nodePaddingY"`
	NodeBorderPx      float64 `json:"nodeBorderPx"`
	NodeTypeBarPx     float64 `json:"nodeTypeBarPx"`
	NodeTextGapPx     float64 `json:"nodeTextGapPx"`
	NodeMinWidth      float64 `json:"nodeMinWidth"`
	NodeTextMaxWidth  float64 `json:"nodeTextMaxWidth"`
	EdgeLabelPaddingX float64 `json:"edgeLabelPaddingX"`
	EdgeLabelPaddingY float64 `json:"edgeLabelPaddingY"`
	EdgeLabelMaxWidth float64 `json:"edgeLabelMaxWidth"`
	StrokePx          float64 `json:"strokePx"`
	ArrowPx           float64 `json:"arrowPx"`
	CrossingTreatment string  `json:"crossingTreatment"`
	CrossingGapPx     float64 `json:"crossingGapPx"`
	ForwardColor      string  `json:"forwardColor"`
	LoopReturnColor   string  `json:"loopReturnColor"`
	LoopReturnDash    string  `json:"loopReturnDash"`
	PracticalZoom     float64 `json:"practicalZoom"`
	MinReadableTextPx float64 `json:"minReadableTextPx"`
	MinContrastRatio  float64 `json:"minContrastRatio"`
	MinClearancePx    float64 `json:"minClearancePx"`
}

// DefaultContract returns the renderer contract of this build.
func DefaultContract() RendererContract {
	return RendererContract{
		Version:           ContractVersion,
		FontFamily:        "Inter, Noto Sans SC, Noto Emoji",
		FontPackage:       "@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2",
		NodeLabelFontPx:   13,
		NodeLabelWeight:   600,
		NodeTypeFontPx:    10,
		NodeTypeWeight:    700,
		EdgeLabelFontPx:   11,
		EdgeLabelWeight:   500,
		LineHeight:        1.35,
		NodePaddingX:      12,
		NodePaddingY:      8,
		NodeBorderPx:      2,
		NodeTypeBarPx:     4,
		NodeTextGapPx:     2,
		NodeMinWidth:      180,
		NodeTextMaxWidth:  240,
		EdgeLabelPaddingX: 6,
		EdgeLabelPaddingY: 3,
		EdgeLabelMaxWidth: 160,
		StrokePx:          2,
		ArrowPx:           10,
		CrossingTreatment: "gap",
		CrossingGapPx:     10,
		ForwardColor:      "#8a8ab0",
		LoopReturnColor:   "#7c8cf5",
		LoopReturnDash:    "6 4",
		PracticalZoom:     0.9,
		MinReadableTextPx: 9,
		MinContrastRatio:  4.5,
		MinClearancePx:    4,
	}
}
```

Create `dashboard/src/graph/arranged/contract.json`:

```json
{
  "version": "arranged-renderer/1",
  "fontFamily": "Inter, Noto Sans SC, Noto Emoji",
  "fontPackage": "@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2",
  "nodeLabelFontPx": 13,
  "nodeLabelWeight": 600,
  "nodeTypeFontPx": 10,
  "nodeTypeWeight": 700,
  "edgeLabelFontPx": 11,
  "edgeLabelWeight": 500,
  "lineHeight": 1.35,
  "nodePaddingX": 12,
  "nodePaddingY": 8,
  "nodeBorderPx": 2,
  "nodeTypeBarPx": 4,
  "nodeTextGapPx": 2,
  "nodeMinWidth": 180,
  "nodeTextMaxWidth": 240,
  "edgeLabelPaddingX": 6,
  "edgeLabelPaddingY": 3,
  "edgeLabelMaxWidth": 160,
  "strokePx": 2,
  "arrowPx": 10,
  "crossingTreatment": "gap",
  "crossingGapPx": 10,
  "forwardColor": "#8a8ab0",
  "loopReturnColor": "#7c8cf5",
  "loopReturnDash": "6 4",
  "practicalZoom": 0.9,
  "minReadableTextPx": 9,
  "minContrastRatio": 4.5,
  "minClearancePx": 4
}
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run TestDefaultContract -v`
Expected: `--- PASS: TestDefaultContract_MatchesDashboardContractJSON`, `--- PASS: TestDefaultContract_EveryFieldIsSet`, `--- PASS: TestDefaultContract_PinnedValues`, then `ok`.

Drift check (prove the test bites): temporarily change `"arrowPx": 10` to `11` in contract.json, rerun — expected `FAIL ... TestDefaultContract_MatchesDashboardContractJSON` with a diff on `ArrowPx`; revert the edit and rerun to PASS.

- [x] **Step 5: Commit**

```bash
git add domain/layout/contract.go domain/layout/contract_test.go dashboard/src/graph/arranged/contract.json
git commit -m "feat(layout): renderer contract shared by arranger and dashboard" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2.2: Geometry model, validation, digest and compatibility

**Files:**
- Create: `domain/layout/geometry.go`
- Test:   `domain/layout/geometry_test.go`

**Interfaces:**
- Consumes: `Graph`, `GraphNode`, `GraphEdge` (P1T1); `RendererContract`, `ContractVersion`, `DefaultContract` (Task 2.1); `Options` (value type defined by this task in Step 3b; defaults and sizing follow in P1T4).
- Produces (exact):
  ```go
  const FormatV1 = "arbitron.layout/v1"
  var ErrInvalidGeometry = errors.New("layout: invalid geometry")
  type Point, Rect, EdgeKind, Side, Attachment, LabelBox, NodeBox, EdgeRoute, Crossing, Geometry
  func (r Rect) Right() float64; Bottom(); CenterX(); CenterY(); Inflate(d float64) Rect; Overlaps(o Rect) bool; Contains(o Rect) bool
  func (g Geometry) Validate(gr Graph) error
  func GeometryDigest(g Geometry) string
  func Compatible(g Geometry, gr Graph) bool
  ```

- [x] **Step 1: Write the failing test**

Create `domain/layout/geometry_test.go`:

```go
package layout

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoNodeGraph is a → b with a branch label.
func twoNodeGraph(t *testing.T) Graph {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeConditional, Label: "Gate"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}},
	})
	require.NoError(t, err)
	return g
}

// validTwoNodeGeometry is a hand-built geometry matching twoNodeGraph.
func validTwoNodeGeometry() Geometry {
	return Geometry{
		Format:   FormatV1,
		Contract: DefaultContract(),
		Options: Options{RankSpacing: 48, NodeSpacing: 56, LaneSeparation: 14, GutterWidth: 48, LabelGap: 10, PortSpacing: 24, NodeTextSlack: 0, DummyWidth: 24, CrossingSweeps: 8, Margin: 40},
		Nodes: []NodeBox{
			{ID: "a", Box: Rect{X: 40, Y: 40, W: 180, H: 60}, Text: "a", TypeCaption: "script", TextLines: 1, Rank: 0},
			{ID: "b", Box: Rect{X: 40, Y: 200, W: 180, H: 60}, Text: "Gate", TypeCaption: "conditional", TextLines: 1, Rank: 1},
		},
		Edges: []EdgeRoute{{
			Ordinal: 0, Source: "a", Target: "b", Output: "pass", Kind: EdgeForward,
			Points:       []Point{{X: 130, Y: 100}, {X: 130, Y: 200}},
			SourceAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{X: 130, Y: 100}},
			TargetAttach: Attachment{NodeID: "b", Side: SideTop, Point: Point{X: 130, Y: 200}},
			Text:         &LabelBox{Text: "pass", Box: Rect{X: 112, Y: 110, W: 36, H: 21}, Lines: 1},
		}},
		Crossings: []Crossing{},
		Canvas:    Rect{X: 0, Y: 0, W: 260, H: 300},
	}
}

func TestRect_Helpers(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 50}
	assert.Equal(t, 110.0, r.Right())
	assert.Equal(t, 70.0, r.Bottom())
	assert.Equal(t, 60.0, r.CenterX())
	assert.Equal(t, 45.0, r.CenterY())
	assert.Equal(t, Rect{X: 5, Y: 15, W: 110, H: 60}, r.Inflate(5))

	assert.True(t, r.Overlaps(Rect{X: 100, Y: 60, W: 20, H: 20}), "interiors overlap")
	assert.False(t, r.Overlaps(Rect{X: 110, Y: 20, W: 10, H: 10}), "touching edges do not overlap")
	assert.True(t, r.Contains(Rect{X: 20, Y: 30, W: 10, H: 10}))
	assert.False(t, r.Contains(Rect{X: 105, Y: 30, W: 10, H: 10}))
}

func TestGeometry_ValidateAcceptsMatchingGeometry(t *testing.T) {
	assert.NoError(t, validTwoNodeGeometry().Validate(twoNodeGraph(t)))
}

func TestGeometry_ValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(g *Geometry)
	}{
		{"wrong format", func(g *Geometry) { g.Format = "other/v9" }},
		{"missing contract version", func(g *Geometry) { g.Contract.Version = "" }},
		{"missing node", func(g *Geometry) { g.Nodes = g.Nodes[:1] }},
		{"node order differs", func(g *Geometry) { g.Nodes[0], g.Nodes[1] = g.Nodes[1], g.Nodes[0] }},
		{"node text differs", func(g *Geometry) { g.Nodes[1].Text = "Other" }},
		{"node type caption differs", func(g *Geometry) { g.Nodes[0].TypeCaption = "tool call" }},
		{"zero width node", func(g *Geometry) { g.Nodes[0].Box.W = 0 }},
		{"NaN coordinate", func(g *Geometry) { g.Nodes[0].Box.X = math.NaN() }},
		{"Inf coordinate", func(g *Geometry) { g.Edges[0].Points[1].Y = math.Inf(1) }},
		{"missing edge", func(g *Geometry) { g.Edges = nil }},
		{"wrong ordinal", func(g *Geometry) { g.Edges[0].Ordinal = 3 }},
		{"edge endpoints differ", func(g *Geometry) { g.Edges[0].Target = "a" }},
		{"edge output differs", func(g *Geometry) { g.Edges[0].Output = "fail" }},
		{"unknown kind", func(g *Geometry) { g.Edges[0].Kind = "diagonal" }},
		{"single point route", func(g *Geometry) { g.Edges[0].Points = g.Edges[0].Points[:1] }},
		{"diagonal segment", func(g *Geometry) { g.Edges[0].Points[1].X = 150 }},
		{"route does not start at source attachment", func(g *Geometry) { g.Edges[0].SourceAttach.Point.X = 120 }},
		{"attachment on wrong node", func(g *Geometry) { g.Edges[0].TargetAttach.NodeID = "a" }},
		{"branch label missing", func(g *Geometry) { g.Edges[0].Text = nil }},
		{"branch label text differs", func(g *Geometry) { g.Edges[0].Text.Text = "fail" }},
		{"crossing references unknown edge", func(g *Geometry) { g.Crossings = []Crossing{{Point: Point{X: 1, Y: 1}, Over: 0, Under: 7}} }},
		{"canvas does not contain node", func(g *Geometry) { g.Canvas.H = 150 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			geo := validTwoNodeGeometry()
			tc.mutate(&geo)
			err := geo.Validate(twoNodeGraph(t))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidGeometry), "got %v", err)
		})
	}
}

func TestGeometry_ValidateRejectsUnexpectedLabel(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeConditional, Label: "Gate"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}}, // no branch text
	})
	require.NoError(t, err)
	geo := validTwoNodeGeometry()
	geo.Edges[0].Output = ""
	err = geo.Validate(g)
	require.Error(t, err, "a label box for an unlabelled edge is a mismatch")
	assert.ErrorIs(t, err, ErrInvalidGeometry)
}

func TestGeometryDigest_StableAndSensitive(t *testing.T) {
	a := validTwoNodeGeometry()
	b := validTwoNodeGeometry()
	assert.Equal(t, GeometryDigest(a), GeometryDigest(b), "same value, same digest")
	assert.Len(t, GeometryDigest(a), 64, "hex sha256")

	b.Nodes[1].Box.Y += 1
	assert.NotEqual(t, GeometryDigest(a), GeometryDigest(b), "any coordinate change changes the digest")
}

func TestGeometry_JSONRoundTrip(t *testing.T) {
	geo := validTwoNodeGeometry()
	raw, err := json.Marshal(geo)
	require.NoError(t, err)
	var back Geometry
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, geo, back)
	assert.Contains(t, string(raw), `"format":"arbitron.layout/v1"`)
	assert.Contains(t, string(raw), `"sourceAttach"`)
}

func TestCompatible(t *testing.T) {
	g := twoNodeGraph(t)
	geo := validTwoNodeGeometry()
	assert.True(t, Compatible(geo, g))

	old := validTwoNodeGeometry()
	old.Contract.Version = "arranged-renderer/0"
	assert.False(t, Compatible(old, g), "a different renderer version is not compatible")

	broken := validTwoNodeGeometry()
	broken.Nodes = broken.Nodes[:1]
	assert.False(t, Compatible(broken, g), "invalid coverage is not compatible")
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestRect_|TestGeometry|TestCompatible' -v`
Expected: build failure `undefined: Geometry`, `undefined: Rect`, `undefined: FormatV1`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3a: Minimal implementation — geometry**

Create `domain/layout/geometry.go`:

```go
package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// FormatV1 identifies the serialised geometry payload shared by the arranger,
// SQLite history, layout.json and the dashboard renderer.
const FormatV1 = "arbitron.layout/v1"

// ErrInvalidGeometry wraps every Validate failure.
var ErrInvalidGeometry = errors.New("layout: invalid geometry")

// geomEps is the tolerance for coordinate equality (geometry is rounded to
// 1/100 px by Arrange).
const geomEps = 0.01

// Point is a canvas coordinate in CSS px at zoom 1.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is an axis-aligned box; X/Y is the top-left corner.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Right is the x of the right edge.
func (r Rect) Right() float64 { return r.X + r.W }

// Bottom is the y of the bottom edge.
func (r Rect) Bottom() float64 { return r.Y + r.H }

// CenterX is the horizontal centre.
func (r Rect) CenterX() float64 { return r.X + r.W/2 }

// CenterY is the vertical centre.
func (r Rect) CenterY() float64 { return r.Y + r.H/2 }

// Inflate grows the rectangle by d on every side (negative d shrinks it).
func (r Rect) Inflate(d float64) Rect {
	return Rect{X: r.X - d, Y: r.Y - d, W: r.W + 2*d, H: r.H + 2*d}
}

// Overlaps reports whether the interiors of r and o intersect. Rectangles that
// only touch along an edge do not overlap.
func (r Rect) Overlaps(o Rect) bool {
	return r.X < o.Right() && o.X < r.Right() && r.Y < o.Bottom() && o.Y < r.Bottom()
}

// Contains reports whether o lies inside r within the quantization tolerance.
func (r Rect) Contains(o Rect) bool {
	const eps = geomEps
	return o.X >= r.X-eps && o.Y >= r.Y-eps && o.Right() <= r.Right()+eps && o.Bottom() <= r.Bottom()+eps
}

// EdgeKind classifies how an edge occurrence is routed and drawn.
type EdgeKind string

const (
	// EdgeForward points downward to a later rank.
	EdgeForward EdgeKind = "forward"
	// EdgeLoopReturn closes a cycle and is routed through a side gutter.
	EdgeLoopReturn EdgeKind = "loop_return"
	// EdgeSelfLoop leaves and re-enters the same node on its right side.
	EdgeSelfLoop EdgeKind = "self_loop"
)

// Side names a node boundary side or a gutter side.
type Side string

const (
	SideTop    Side = "top"
	SideBottom Side = "bottom"
	SideLeft   Side = "left"
	SideRight  Side = "right"
)

// Attachment is where an edge meets a node boundary.
type Attachment struct {
	NodeID string `json:"nodeId"`
	Side   Side   `json:"side"`
	Point  Point  `json:"point"`
}

// LabelBox is a rendered text box (branch label) in canvas coordinates. Box
// includes the contract padding.
type LabelBox struct {
	Text  string `json:"text"`
	Box   Rect   `json:"box"`
	Lines int    `json:"lines"`
}

// NodeBox is one node's reviewed box and complete displayed text.
type NodeBox struct {
	ID          string `json:"id"`
	Box         Rect   `json:"box"`
	Text        string `json:"text"`
	TypeCaption string `json:"typeCaption"`
	TextLines   int    `json:"textLines"`
	Rank        int    `json:"rank"`
}

// EdgeRoute is one routed edge occurrence. Ordinal is the manifest edge index.
type EdgeRoute struct {
	Ordinal      int        `json:"ordinal"`
	Source       string     `json:"source"`
	Target       string     `json:"target"`
	Output       string     `json:"output,omitempty"`
	Label        string     `json:"label,omitempty"`
	Kind         EdgeKind   `json:"kind"`
	Side         Side       `json:"side,omitempty"`
	Lane         int        `json:"lane,omitempty"`
	Points       []Point    `json:"points"`
	SourceAttach Attachment `json:"sourceAttach"`
	TargetAttach Attachment `json:"targetAttach"`
	Text         *LabelBox  `json:"text,omitempty"`
}

// Crossing is a proper intersection between two edge routes. The renderer
// draws Under with a gap (contract CrossingTreatment "gap") so a crossing is
// never mistaken for a junction.
type Crossing struct {
	Point Point `json:"point"`
	Over  int   `json:"over"`
	Under int   `json:"under"`
}

// Geometry is one candidate layout: everything needed to reproduce the
// reviewed rendering. Runtime state may restyle it but never moves it.
type Geometry struct {
	Format    string           `json:"format"`
	Contract  RendererContract `json:"contract"`
	Options   Options          `json:"options"`
	Nodes     []NodeBox        `json:"nodes"`
	Edges     []EdgeRoute      `json:"edges"`
	Crossings []Crossing       `json:"crossings"`
	Canvas    Rect             `json:"canvas"`
}

// Validate checks that g is a complete, well-formed geometry for gr: format,
// one box per node in manifest order with matching text, one orthogonal route
// per edge occurrence in manifest order whose endpoints are its attachments,
// branch labels exactly where the graph has branch text, finite numbers, valid
// crossing references and a canvas that contains everything.
func (g Geometry) Validate(gr Graph) error {
	// Implement the numbered validation algorithm immediately below this code
	// block. Return ErrInvalidGeometry wrapped with the failing node/ordinal/field.
	// This body is a deliberate compiling RED stub, not the finished validator.
	return fmt.Errorf("%w: validator not implemented", ErrInvalidGeometry)
}

// GeometryDigest is the hex SHA-256 of the canonical JSON encoding of g. Review
// evidence carries it so findings always belong to the exact candidate.
func GeometryDigest(g Geometry) string {
	raw, err := json.Marshal(g)
	if err != nil {
		// Invalid non-finite values have no candidate digest.
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Compatible reports whether a stored geometry can be displayed for gr by this
// build's renderer.
func Compatible(g Geometry, gr Graph) bool {
	return g.Validate(gr) == nil && g.Contract == DefaultContract()
}

func nearly(a, b float64) bool { return math.Abs(a-b) <= geomEps }

func samePoint(a, b Point) bool { return nearly(a.X, b.X) && nearly(a.Y, b.Y) }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func finitePoint(p Point) bool { return finite(p.X) && finite(p.Y) }

func finiteRect(r Rect) bool { return finite(r.X) && finite(r.Y) && finite(r.W) && finite(r.H) }
```

**Implement Validate from this complete algorithm; do not stop at the RED stub:**

1. Reject wrong Format; empty graph; Canvas not finite, positive or at origin (X=Y=0).
   Validate every RendererContract numeric field, including overflowed derived right/bottom
   and products: font sizes, line height, spacing/padding/borders, marker/gap dimensions,
   zoom, min-clearance/readability/contrast and weights must be finite/positive; strings
   must be nonempty. Weights are 100..900, zoom <=1, contrast <=21. Contract version may
   differ here (structural validation); Compatible additionally requires the entire exact
   DefaultContract. A custom positive font size remains structurally valid for preflight.
2. Validate every Options float as finite; RankSpacing, NodeSpacing, LaneSeparation,
   GutterWidth, LabelGap, PortSpacing and DummyWidth are positive; NodeTextSlack and Margin
   are nonnegative, CrossingSweeps is nonnegative. Do this directly in geometry.go because
   DefaultOptions/Arrange do not exist until later tasks. Do not call a later task symbol.
3. Require exact node/edge slice lengths and manifest order, IDs, complete node/type text,
   edge Source/Target/Output/Label and displayed branch text; every node has positive finite
   dimensions, TextLines>=1, Rank>=0, and is inside Canvas. Rect.Right/Bottom and attachment
   comparisons must reject arithmetic overflow even if individual inputs were finite.
4. For every edge require valid Kind and side metadata: self_loop iff Source==Target,
   self-loop SideRight and right/right attachments; loop_return has SideLeft/SideRight and
   Lane>=1, bottom/top attachments; forward has no gutter Side/Lane, bottom/top attachments.
   The detailed DFS classification check remains P1T7's semantic preflight responsibility.
   Require >=2 points; every point finite/contained; each segment nonzero and orthogonal
   after 0.01px quantization. Source/target NodeIDs and first/last points match exactly
   within geomEps, and each attachment lies strictly between corners on its declared node
   boundary. Define `onSide(p Point, side Side, r Rect) bool` here (P1T7 consumes it).
   The first and final segments' direction into/out of the boundary is preflight work.
5. Branch label presence/text matches GraphEdge.Text; its Rect is positive/finite/contained
   and Lines>=1. No label exists for an unlabelled edge. Geometry validation does not infer
   actual glyph bounds or text readability; that is measurement/preflight/render review.
6. Each Crossing has distinct in-range edge ordinals, a finite contained point and a unique
   (Over,Under,Point) tuple. It must be a proper interior horizontal/vertical intersection
   on those two routes, with Over horizontal and Under vertical. Check segments inline;
   do not depend on P1T6 findCrossings. Missing crossing annotations are preflight findings;
   fabricated annotations or locations at bends/endpoints are structurally invalid.
7. Every invalid value wraps ErrInvalidGeometry; no panic, no mutation. GeometryDigest
   returns an empty string when json.Marshal fails (never hash an error message). Consumers
   require a nonempty 64-hex digest; malformed geometry must not share a synthetic digest.

**RED/GREEN validation matrix:** extend TestGeometry_ValidateRejects before replacing the
stub with same-version padding/font/crossing-contract mutation in TestCompatible; zero or
Inf/NaN contract/options, overflowing Rect sums, negative dimensions/lines/ranks, invalid
side/lane, off-boundary attachments, zero-length and 0.1px diagonal segments, out-of-canvas
label/crossing, duplicate/fabricated crossing and wrong edge Label. Test nonzero canvas
origin rejects. Add valid zero-edge, parallel-edge and Unicode round trips and a valid
proper crossing fixture; zero Slack/Margin/CrossingSweeps are allowed. Run behavioral RED
against the compiling stub, implement the algorithm, then GREEN and mutation probe each
identity/boundary/contract guard. The valid fixture itself must satisfy every check.

- [x] **Step 3b: Define the complete `Options` value type**

Create `domain/layout/options.go` now. P1T4 adds `DefaultOptions` to this file;
it does not replace this type. This avoids consuming a later task's undefined type.

```go
package layout

// Options is the explicit arrangement policy. Defaults are Phase 1 policy
// values; Refine (P1T8) adjusts them from review findings between attempts.
// Every value is in CSS px at zoom 1 except CrossingSweeps.
type Options struct {
	// RankSpacing is the clear vertical space between the last track of a
	// channel and the next row of nodes.
	RankSpacing float64 `json:"rankSpacing"`
	// NodeSpacing is the minimum horizontal gap between adjacent items in a row.
	NodeSpacing float64 `json:"nodeSpacing"`
	// LaneSeparation is the distance between parallel horizontal tracks and
	// between parallel loop-return gutter lanes.
	LaneSeparation float64 `json:"laneSeparation"`
	// GutterWidth is the distance from the widest row to the first loop lane.
	GutterWidth float64 `json:"gutterWidth"`
	// LabelGap separates the row bottom, the branch-label band and the tracks.
	LabelGap float64 `json:"labelGap"`
	// PortSpacing is the minimum distance between attachments on one side.
	PortSpacing float64 `json:"portSpacing"`
	// NodeTextSlack is extra px added to node and branch text boxes (grows on clipping).
	NodeTextSlack float64 `json:"nodeTextSlack"`
	// DummyWidth is the row slot reserved for a long edge passing through a row.
	DummyWidth float64 `json:"dummyWidth"`
	// CrossingSweeps is the barycentre sweep budget for crossing reduction.
	CrossingSweeps int `json:"crossingSweeps"`
	// Margin is the empty border around the canvas.
	Margin float64 `json:"margin"`
}

```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestRect_|TestGeometry|TestCompatible|TestDefaultContract' -v`
Expected: every listed test `--- PASS`, including the full structural rejection matrix; `ok  	github.com/arbitron/arbitron/domain/layout`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/   # expect no output
git add domain/layout/geometry.go domain/layout/geometry_test.go domain/layout/options.go
git commit -m "feat(layout): versioned geometry model with coverage validation and digest" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
P1T2 owns `options.go`'s type declaration; include it in this commit.

---

## Execution evidence

- Task 2.1 RED: missing symbols first, then compiling zero-value defaults failed the nonzero/pinned-value assertions and missing dashboard-file assertion. GREEN: all contract tests pass. Temporarily changing JSON `arrowPx` from 10 to 11 failed the drift test on `ArrowPx`; restored and rerun green.
- Task 2.2 RED: missing symbols first, then compiling declarations with no-op validation, rectangle helpers and digest failed behavioral assertions across the rejection matrix, digest and compatibility tests. GREEN: the prescribed `TestRect_|TestGeometry|TestCompatible|TestDefaultContract` run passes, including zero-edge/parallel/Unicode round trips, proper crossings, custom structurally valid contracts and zero nonnegative policy values.
- Self-review, in order: (1) checked the exact task interfaces, fields/tags and seven-part structural algorithm against contracts §6.1/§6.6; left defaults/sizing/semantic preflight to their owning tasks. (2) Added guard-isolating saved-manifest identity and coherent attachment/self-loop cases; all 44 temporary identity/boundary/contract guard-removal probes fail tests, including two initially surviving ID/kind probes after strengthening those cases. Restored every mutation. These tests check the real `BuildGraph` producer and serialized value validation, not production integration. (3) Checked neighboring Go conventions, improved subtest diagnostics and reused the self-loop fixture; formatting and vet are clean.
- Final verification: focused verbose tests, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` (empty), and `git diff --check` pass. Package statement coverage is 99.3%. Dashboard `npx tsc --noEmit` and `npx vitest run` pass (50 files / 522 tests). No embedded rebuild is needed yet: this task's JSON gains its runtime consumer in P3T1/P3T2.
- Production acceptance remains pending the explicitly assigned Arrange/Coordinator producers (P1T6/P1T9), Reader/storage/worker/composition-root consumers (P2T5–P2T9), and browser renderer/reviewer chain (P3T1–P3T7). This value-model task does not claim those future production tests have run.

## Wired-and-fed

- **Producers:** `Geometry` values are produced only by `Arrange` (P1T6) inside `Coordinator.Run` (P1T9); the worker (P2T8) persists them in `layout_results.geometry` (P2T6) and `layout.json` (P2T7). `RendererContract` is produced by `DefaultContract()` here and by the rendered page's own `contract.json` (P3T4 report `contract`), which the reviewer (P3T6) compares — a mismatch is a blocking `render_mismatch`.
- **Consumers / wiring:** `Validate`/`Compatible` are called by `layout.Reader.View` (P2T5) before any stored geometry reaches a view, so both HTTP/MCP views (P3T9/P3T10) and the dashboard renderer (P3T1 `validateGeometry` mirrors it) never display an incomplete/stale payload; `GeometryDigest` is set on every `Rendering` by the browser renderer (P3T5) and checked by the coordinator's pass rule (P1T9).
- **Provenance probe:** `TestDefaultContract_MatchesDashboardContractJSON` fails if the dashboard renderer and arranger disagree; P3T7's real-browser acceptance test fails if the rendered surface reports a different contract than the geometry it reviewed.
