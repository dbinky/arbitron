# Workflow Auto-Arrangement — Phase 1 Task 4: Measurement port, arrangement options and node sizing

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Alignment requirements:** Read contracts §6 (6.1) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Define how the arranger obtains real rendered text measurements (the `MeasureRequest`/`Measurements` value exchanged with the production renderer), the explicit `Options` policy value with its defaults, and the sizing rules that grow node and branch-label boxes to fit full wrapped text and their ports — never ellipsizing.

**Architecture:** `measure.go` defines the measurement value types and `MeasureRequestFor` (one item per node label, node type caption and branch label, each with the contract's wrap width). Measurements come from the browser render surface in production (P3T4/P3T5) — the arranger never guesses glyph widths. `options.go` holds `Options`/`DefaultOptions` (new Phase 1 policy). `size.go` converts measurements into node/label box sizes using the renderer contract's padding, border, type bar and gap values.

**Tech Stack:** Go 1.25.5 (`math`, `fmt`, `unicode/utf8` in tests), testify.

**Spec / Design:** `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §1 ("Long labels must wrap or grow boxes rather than silently ellipsize"), §2 (explicit options value: rank/node spacing, gutter width, lane separation, crossing-search budget), Data Provenance row "Actual glyph/text measurements … RENDER-REVIEW" (measurements are a port fed by the renderer).

---

### Task 4.1: Measurement value types and request builder

**Files:**
- Create: `domain/layout/measure.go`
- Modify: `domain/layout/testhelpers_test.go` (append `fakeMeasurer`; file created by P1T3)
- Test:   `domain/layout/measure_test.go`

**Interfaces:**
- Consumes: `Graph` (P1T1), `RendererContract`, `DefaultContract` (P1T2).
- Produces:
  ```go
  type TextStyle string
  const (StyleNodeLabel TextStyle = "node_label"; StyleNodeType TextStyle = "node_type"; StyleEdgeLabel TextStyle = "edge_label")
  type TextItem struct { Key string `json:"key"`; Text string `json:"text"`; Style TextStyle `json:"style"`; MaxWidth float64 `json:"maxWidth"` }
  type MeasureRequest struct { Contract RendererContract `json:"contract"`; Items []TextItem `json:"items"` }
  type TextMetrics struct { Width float64 `json:"width"`; Height float64 `json:"height"`; Lines int `json:"lines"` }
  type Measurements struct { Contract RendererContract `json:"contract"`; FontsReady bool `json:"fontsReady"`; Items map[string]TextMetrics `json:"items"` }
  var ErrMissingMeasurement = errors.New("layout: missing text measurement")
  func NodeLabelKey(id string) string; func NodeTypeKey(id string) string; func EdgeLabelKey(ordinal int) string
  func MeasureRequestFor(g Graph, c RendererContract) MeasureRequest
  func (m Measurements) Metric(key string) (TextMetrics, error)
  ```
  Internal test helper: `type fakeMeasurer struct{...}`, `func newFakeMeasurer() *fakeMeasurer`, `func (f *fakeMeasurer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error)`.

- [x] **Step 1: Write the failing test**

Append to `domain/layout/testhelpers_test.go` (keep the existing `loadSample`/`edgeGraph`; add `"context"`, `"fmt"`, `"math"`, `"strings"` and `"unicode/utf8"` to its imports):

```go
// fakeMeasurer is a deterministic stand-in for the browser text measurer. It
// honours every load-bearing argument: the item's text (rune count), its style
// (font size from the request contract) and its wrap width (MaxWidth).
type fakeMeasurer struct {
	charW      float64 // px per rune at 13px; scaled by the style's font size
	fontsReady bool
	err        error
	calls      int
}

func newFakeMeasurer() *fakeMeasurer { return &fakeMeasurer{charW: 7, fontsReady: true} }

func (f *fakeMeasurer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error) {
	if err := ctx.Err(); err != nil { return Measurements{}, err }
	f.calls++
	if f.err != nil {
		return Measurements{}, f.err
	}
	out := Measurements{Contract: req.Contract, FontsReady: f.fontsReady, Items: map[string]TextMetrics{}}
	for _, it := range req.Items {
		if !finite(it.MaxWidth) || it.MaxWidth <= 0 { return Measurements{}, fmt.Errorf("invalid max width for %s", it.Key) }
		out.Items[it.Key] = fakeMetric(it, req.Contract, f.charW)
	}
	return out, nil
}

func fakeMetric(it TextItem, c RendererContract, charW float64) TextMetrics {
	font := c.NodeLabelFontPx
	switch it.Style {
	case StyleNodeType:
		font = c.NodeTypeFontPx
	case StyleEdgeLabel:
		font = c.EdgeLabelFontPx
	}
	if it.Text == "" { return TextMetrics{} }
	// fakeMetric is called only after fakeMeasurer validates finite MaxWidth>0.
	lines, width := 0, 0.0
	for _, paragraph := range strings.Split(it.Text, "\n") {
		natural := float64(utf8.RuneCountInString(paragraph))*charW*font/13
		lines += max(1, int(math.Ceil(natural/it.MaxWidth)))
		width = math.Max(width, math.Min(natural,it.MaxWidth))
	}
	return TextMetrics{Width: width, Height: float64(lines)*font*c.LineHeight, Lines: lines}
}
```

Create `domain/layout/measure_test.go`:

```go
package layout

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureKeys(t *testing.T) {
	assert.Equal(t, "node:init:label", NodeLabelKey("init"))
	assert.Equal(t, "node:init:type", NodeTypeKey("init"))
	assert.Equal(t, "edge:7", EdgeLabelKey(7))
}

func TestMeasureRequestFor_OneItemPerDisplayedText(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "Init"}, {ID: "b", Type: workflow.NodeTypeConditional}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "b", Target: "a", Output: "retry"}},
	})
	require.NoError(t, err)
	c := DefaultContract()

	req := MeasureRequestFor(g, c)

	assert.Equal(t, c, req.Contract)
	assert.Equal(t, []TextItem{
		{Key: "node:a:label", Text: "Init", Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:a:type", Text: "script", Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:b:label", Text: "b", Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:b:type", Text: "conditional", Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "edge:1", Text: "retry", Style: StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
	}, req.Items, "unlabelled edges are not measured")
}

func TestMeasurements_Metric(t *testing.T) {
	m := Measurements{Items: map[string]TextMetrics{
		"ok":   {Width: 10, Height: 17.55, Lines: 1},
		"nan":  {Width: math.NaN(), Height: 1, Lines: 1},
		"neg":  {Width: -1, Height: 1, Lines: 1},
		"zero": {},
	}}
	got, err := m.Metric("ok")
	require.NoError(t, err)
	assert.Equal(t, 10.0, got.Width)

	_, err = m.Metric("absent")
	assert.True(t, errors.Is(err, ErrMissingMeasurement))

	_, err = m.Metric("nan")
	assert.Error(t, err, "non-finite measurements are rejected")
	_, err = m.Metric("neg")
	assert.Error(t, err, "negative measurements are rejected")

	z, err := m.Metric("zero")
	require.NoError(t, err, "empty text legitimately measures zero")
	assert.Equal(t, TextMetrics{}, z)
}

func TestFakeMeasurer_HonoursWrapWidthAndStyle(t *testing.T) {
	c := DefaultContract()
	long := strings.Repeat("word ", 30)
	got, err := newFakeMeasurer().Measure(context.Background(), MeasureRequest{Contract: c, Items: []TextItem{
		{Key: "wide", Text: long, Style: StyleNodeLabel, MaxWidth: 240},
		{Key: "narrow", Text: long, Style: StyleNodeLabel, MaxWidth: 120},
		{Key: "small", Text: long, Style: StyleEdgeLabel, MaxWidth: 240},
	}})
	require.NoError(t, err)
	assert.LessOrEqual(t, got.Items["wide"].Width, 240.0)
	assert.Greater(t, got.Items["narrow"].Lines, got.Items["wide"].Lines, "narrower wrap width means more lines")
	assert.Less(t, got.Items["small"].Lines, got.Items["wide"].Lines, "smaller font fits more per line")
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestMeasure|TestFakeMeasurer' -v`
Expected: build failure `undefined: NodeLabelKey`, `undefined: MeasureRequest`, `undefined: TextItem`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/measure.go`:

```go
package layout

import (
	"errors"
	"fmt"
)

// TextStyle names a renderer text style whose CSS the measurer must apply.
type TextStyle string

const (
	StyleNodeLabel TextStyle = "node_label"
	StyleNodeType  TextStyle = "node_type"
	StyleEdgeLabel TextStyle = "edge_label"
)

// TextItem is one piece of displayed text to measure with the renderer's real
// fonts and CSS, wrapped at MaxWidth CSS px.
type TextItem struct {
	Key      string    `json:"key"`
	Text     string    `json:"text"`
	Style    TextStyle `json:"style"`
	MaxWidth float64   `json:"maxWidth"`
}

// MeasureRequest asks the renderer to measure every displayed text of a graph.
type MeasureRequest struct {
	Contract RendererContract `json:"contract"`
	Items    []TextItem       `json:"items"`
}

// TextMetrics is the rendered size of one text item: Width is the widest line,
// Height the full block height, both in CSS px at zoom 1.
type TextMetrics struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Lines  int     `json:"lines"`
}

// Measurements is the renderer's answer to a MeasureRequest. Contract is the
// contract the rendering surface actually used; FontsReady reports that the
// contract fonts were loaded when measuring.
type Measurements struct {
	Contract   RendererContract       `json:"contract"`
	FontsReady bool                   `json:"fontsReady"`
	Items      map[string]TextMetrics `json:"items"`
}

// ErrMissingMeasurement means a required text item was not measured.
var ErrMissingMeasurement = errors.New("layout: missing text measurement")

// NodeLabelKey is the measurement key of a node's primary text.
func NodeLabelKey(id string) string { return "node:" + id + ":label" }

// NodeTypeKey is the measurement key of a node's type caption.
func NodeTypeKey(id string) string { return "node:" + id + ":type" }

// EdgeLabelKey is the measurement key of an edge occurrence's branch text.
func EdgeLabelKey(ordinal int) string { return fmt.Sprintf("edge:%d", ordinal) }

// MeasureRequestFor lists every displayed text of g with the contract's wrap
// widths: each node's label and type caption, and each edge's branch text.
func MeasureRequestFor(g Graph, c RendererContract) MeasureRequest {
	items := make([]TextItem, 0, 2*len(g.Nodes)+len(g.Edges))
	for _, n := range g.Nodes {
		items = append(items,
			TextItem{Key: NodeLabelKey(n.ID), Text: n.Text, Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
			TextItem{Key: NodeTypeKey(n.ID), Text: n.TypeCaption, Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		)
	}
	for _, e := range g.Edges {
		if e.Text == "" {
			continue
		}
		items = append(items, TextItem{Key: EdgeLabelKey(e.Ordinal), Text: e.Text, Style: StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth})
	}
	return MeasureRequest{Contract: c, Items: items}
}

// Metric returns the measurement for key, rejecting absent, non-finite or
// negative values.
func (m Measurements) Metric(key string) (TextMetrics, error) {
	tm, ok := m.Items[key]
	if !ok {
		return TextMetrics{}, fmt.Errorf("%w: %s", ErrMissingMeasurement, key)
	}
	if !finite(tm.Width) || !finite(tm.Height) || tm.Width < 0 || tm.Height < 0 || tm.Lines < 0 {
		return TextMetrics{}, fmt.Errorf("layout: invalid measurement for %s: %+v", key, tm)
	}
	if (tm.Width == 0 || tm.Height == 0 || tm.Lines == 0) && tm != (TextMetrics{}) {
		return TextMetrics{}, fmt.Errorf("layout: inconsistent measurement for %s: %+v", key, tm)
	}
	return tm, nil
}
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run 'TestMeasure|TestFakeMeasurer' -v`
Expected: `--- PASS: TestMeasureKeys`, `--- PASS: TestMeasureRequestFor_OneItemPerDisplayedText`, `--- PASS: TestMeasurements_Metric`, `--- PASS: TestFakeMeasurer_HonoursWrapWidthAndStyle`; `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/measure.go domain/layout/measure_test.go domain/layout/testhelpers_test.go
git commit -m "feat(layout): rendered text measurement port and request builder" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4.2: `Options` and node/label sizing

**Files:**
- Modify: `domain/layout/options.go` (retain the complete P1T2 type)
- Create: `domain/layout/size.go`
- Test:   `domain/layout/size_test.go`

**Interfaces:**
- Consumes: `Measurements.Metric`, keys (Task 4.1); `classifyEdges` (P1T3); `RendererContract` (P1T2).
- Produces:
  ```go
  // Options is already defined by P1T2, with all JSON tags in contracts §1.
  func DefaultOptions() Options
  // internal, used by P1T6:
  type labelSize struct{ W, H float64; Lines int }
  type nodeSize struct{ W, H float64; TextLines int }
  func edgeLabelSizes(g Graph, m Measurements, c RendererContract, o Options) (map[int]labelSize, error)
  func nodeSizes(g Graph, kinds []EdgeKind, m Measurements, c RendererContract, o Options, labels map[int]labelSize) ([]nodeSize, error)
  func ceilPx(v float64) float64
  ```

Sizing rules (contract values; the renderer in P3T2 uses the identical box model — `box-sizing: border-box`, border on all sides, 4px type bar, padded content with caption, gap, label):
- Branch label box: `W = ceilPx(textW) + 2·EdgeLabelPaddingX + NodeTextSlack`, `H = ceilPx(textH) + 2·EdgeLabelPaddingY + NodeTextSlack`. `ceilPx(v) = ceil(v) + 1` (one px of rounding slack so the browser never wraps differently).
- Node height: `2·NodeBorderPx + NodeTypeBarPx + 2·NodePaddingY + ceilPx(typeH) + NodeTextGapPx + ceilPx(labelH) + NodeTextSlack`.
- Node width = max of: `NodeMinWidth`; `ceilPx(max(labelW, typeW)) + 2·NodePaddingX + 2·NodeBorderPx + NodeTextSlack`; bottom-port need `Σ slot + PortSpacing` where each exit (forward or loop-return edge leaving this node) has `slot = PortSpacing + LabelGap + labelBox.W` when labelled, else `PortSpacing`; top-port need `(entries + 1)·PortSpacing` for incoming forward and loop-return edges. Self loops use the right side and are not ports here.

- [x] **Step 1: Write the failing test**

Create `domain/layout/size_test.go`:

```go
package layout

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func measured(t *testing.T, g Graph) Measurements {
	t.Helper()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, DefaultContract()))
	require.NoError(t, err)
	return m
}

func sizesFor(t *testing.T, g Graph, o Options) ([]nodeSize, map[int]labelSize) {
	t.Helper()
	c := DefaultContract()
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, labels)
	require.NoError(t, err)
	return sizes, labels
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	assert.Equal(t, Options{
		RankSpacing: 48, NodeSpacing: 56, LaneSeparation: 14, GutterWidth: 48, LabelGap: 10,
		PortSpacing: 24, NodeTextSlack: 0, DummyWidth: 24, CrossingSweeps: 8, Margin: 40,
	}, o)
}

func TestNodeSizes_ShortLabelUsesMinimumWidth(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "Init"}}})
	require.NoError(t, err)
	sizes, _ := sizesFor(t, g, DefaultOptions())
	c := DefaultContract()
	assert.Equal(t, c.NodeMinWidth, sizes[0].W)
	assert.Equal(t, 1, sizes[0].TextLines)
	// height = borders + bar + padding + caption + gap + one label line, each text rounded up with 1px slack
	typeH := ceilPx(1 * c.NodeTypeFontPx * c.LineHeight)
	labelH := ceilPx(1 * c.NodeLabelFontPx * c.LineHeight)
	want := 2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY + typeH + c.NodeTextGapPx + labelH
	assert.Equal(t, want, sizes[0].H)
}

func TestNodeSizes_LongLabelWrapsAndGrowsTallerNotWider(t *testing.T) {
	long := strings.Repeat("Resolve blockers then re-align ", 4)
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{
		{ID: "short", Type: workflow.NodeTypeAgentCall, Label: "Plan"},
		{ID: "long", Type: workflow.NodeTypeAgentCall, Label: long},
	}})
	require.NoError(t, err)
	sizes, _ := sizesFor(t, g, DefaultOptions())
	c := DefaultContract()
	assert.Greater(t, sizes[1].TextLines, 1, "long labels wrap")
	assert.Greater(t, sizes[1].H, sizes[0].H, "wrapped labels grow the box vertically")
	maxW := ceilPx(c.NodeTextMaxWidth) + 2*c.NodePaddingX + 2*c.NodeBorderPx
	assert.LessOrEqual(t, sizes[1].W, maxW, "text never widens a node beyond the wrap width")
}

func TestNodeSizes_LabelledBranchesWidenTheSource(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "gate", Type: workflow.NodeTypeConditional}, {ID: "x"}, {ID: "y"}, {ID: "z"}},
		Edges: []workflow.Edge{
			{Source: "gate", Target: "x", Label: "plans exist — re-align + revalidate"},
			{Source: "gate", Target: "y", Label: "spec+designs exist"},
			{Source: "gate", Target: "z", Label: "fresh run"},
		},
	})
	require.NoError(t, err)
	o := DefaultOptions()
	sizes, labels := sizesFor(t, g, o)
	need := o.PortSpacing
	for ord := 0; ord < 3; ord++ {
		need += o.PortSpacing + o.LabelGap + labels[ord].W
	}
	assert.GreaterOrEqual(t, sizes[0].W, need, "every branch label fits beside its own port")
	assert.Greater(t, sizes[0].W, DefaultContract().NodeMinWidth)
}

func TestNodeSizes_ManyIncomingEdgesWidenTheTarget(t *testing.T) {
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "join"}}}
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id})
		wf.Edges = append(wf.Edges, workflow.Edge{Source: id, Target: "join"})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	o := DefaultOptions()
	sizes, _ := sizesFor(t, g, o)
	assert.GreaterOrEqual(t, sizes[0].W, 13*o.PortSpacing, "distinct top attachments for every incoming edge")
}

func TestNodeSizes_TextSlackGrowsBoxes(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Label: strings.Repeat("x", 60)}}})
	require.NoError(t, err)
	base, _ := sizesFor(t, g, DefaultOptions())
	o := DefaultOptions()
	o.NodeTextSlack = 8
	slack, _ := sizesFor(t, g, o)
	assert.Equal(t, base[0].H+8, slack[0].H)
	assert.GreaterOrEqual(t, slack[0].W, base[0].W)
}

func TestEdgeLabelSizes_IncludePadding(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}, {Source: "a", Target: "b"}},
	})
	require.NoError(t, err)
	c := DefaultContract()
	o := DefaultOptions()
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	require.Contains(t, labels, 0)
	assert.NotContains(t, labels, 1, "unlabelled edges have no label box")
	tm := m.Items[EdgeLabelKey(0)]
	assert.Equal(t, ceilPx(tm.Width)+2*c.EdgeLabelPaddingX, labels[0].W)
	assert.Equal(t, ceilPx(tm.Height)+2*c.EdgeLabelPaddingY, labels[0].H)
	assert.Equal(t, tm.Lines, labels[0].Lines)
}

func TestSizing_MissingMeasurementIsAnError(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}},
	})
	require.NoError(t, err)
	c := DefaultContract()
	o := DefaultOptions()
	m := measured(t, g)
	delete(m.Items, EdgeLabelKey(0))
	_, err = edgeLabelSizes(g, m, c, o)
	assert.True(t, errors.Is(err, ErrMissingMeasurement))

	m = measured(t, g)
	delete(m.Items, NodeLabelKey("b"))
	_, err = nodeSizes(g, classifyEdges(g), m, c, DefaultOptions(), map[int]labelSize{0: {W: 30, H: 20, Lines: 1}})
	assert.True(t, errors.Is(err, ErrMissingMeasurement))
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run 'TestDefaultOptions|TestNodeSizes|TestEdgeLabelSizes|TestSizing' -v`
Expected: build failure `undefined: nodeSizes`, `undefined: edgeLabelSizes`, `undefined: ceilPx`.

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Append `DefaultOptions` below the existing declaration in `domain/layout/options.go` (P1T2 owns the type; do not repeat the package clause or redeclare the type):

```go

// DefaultOptions returns the initial arrangement policy for attempt 1.
func DefaultOptions() Options {
	return Options{
		RankSpacing:    48,
		NodeSpacing:    56,
		LaneSeparation: 14,
		GutterWidth:    48,
		LabelGap:       10,
		PortSpacing:    24,
		NodeTextSlack:  0,
		DummyWidth:     24,
		CrossingSweeps: 8,
		Margin:         40,
	}
}
```

Create `domain/layout/size.go`:

```go
package layout

import ("math"; "fmt")

// labelSize is a branch-label box size including contract padding.
type labelSize struct {
	W, H  float64
	Lines int
}

// nodeSize is a node box size and its primary-text line count.
type nodeSize struct {
	W, H      float64
	TextLines int
}

// ceilPx rounds a measured length up to whole px plus one px of slack so the
// browser lays out the same line breaks in the reviewed box.
func ceilPx(v float64) float64 { return math.Ceil(v) + 1 }

// metricForText checks the measurement against the exact requested text/style.
func metricForText(m Measurements, key, text string, fontPx, lineHeight, maxWidth float64) (TextMetrics, error) {
	tm, err := m.Metric(key)
	if err != nil { return TextMetrics{}, err }
	if text == "" {
		if tm != (TextMetrics{}) { return TextMetrics{}, fmt.Errorf("layout: nonzero metric for empty text %s", key) }
		return tm, nil
	}
	if tm.Width <= 0 || tm.Height <= 0 || tm.Lines <= 0 || tm.Width > maxWidth+0.5 || math.Abs(tm.Height-float64(tm.Lines)*fontPx*lineHeight)>0.5 {
		return TextMetrics{}, fmt.Errorf("layout: metric disagrees with requested text/style for %s: %+v", key, tm)
	}
	return tm, nil
}

// edgeLabelSizes sizes every branch label (edges with non-empty Text).
func edgeLabelSizes(g Graph, m Measurements, c RendererContract, o Options) (map[int]labelSize, error) {
	out := make(map[int]labelSize)
	for _, e := range g.Edges {
		if e.Text == "" {
			continue
		}
		tm, err := metricForText(m, EdgeLabelKey(e.Ordinal), e.Text, c.EdgeLabelFontPx, c.LineHeight, c.EdgeLabelMaxWidth)
		if err != nil {
			return nil, err
		}
		out[e.Ordinal] = labelSize{
			W:     ceilPx(tm.Width) + 2*c.EdgeLabelPaddingX + o.NodeTextSlack,
			H:     ceilPx(tm.Height) + 2*c.EdgeLabelPaddingY + o.NodeTextSlack,
			Lines: tm.Lines,
		}
	}
	return out, nil
}

// nodeSizes sizes every node so its full wrapped text and every attachment
// (with its branch label) fit. Nodes grow; text is never truncated.
func nodeSizes(g Graph, kinds []EdgeKind, m Measurements, c RendererContract, o Options, labels map[int]labelSize) ([]nodeSize, error) {
	bottomNeed := make([]float64, len(g.Nodes))
	topCount := make([]int, len(g.Nodes))
	for i := range bottomNeed {
		bottomNeed[i] = o.PortSpacing
	}
	for _, e := range g.Edges {
		if kinds[e.Ordinal] == EdgeSelfLoop {
			continue
		}
		s, _ := g.NodeIndex(e.Source)
		t, _ := g.NodeIndex(e.Target)
		slot := o.PortSpacing
		if l, ok := labels[e.Ordinal]; ok {
			slot += o.LabelGap + l.W
		}
		bottomNeed[s] += slot
		topCount[t]++
	}

	out := make([]nodeSize, len(g.Nodes))
	for i, n := range g.Nodes {
		label, err := metricForText(m, NodeLabelKey(n.ID), n.Text, c.NodeLabelFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err != nil {
			return nil, err
		}
		caption, err := metricForText(m, NodeTypeKey(n.ID), n.TypeCaption, c.NodeTypeFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err != nil {
			return nil, err
		}
		textW := ceilPx(math.Max(label.Width, caption.Width)) + 2*c.NodePaddingX + 2*c.NodeBorderPx + o.NodeTextSlack
		topNeed := float64(topCount[i]+1) * o.PortSpacing
		w := math.Max(math.Max(c.NodeMinWidth, textW), math.Max(bottomNeed[i], topNeed))
		h := 2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY +
			ceilPx(caption.Height) + c.NodeTextGapPx + ceilPx(label.Height) + o.NodeTextSlack
		// Empty captions have no rendered block or inter-text gap.
		if n.TypeCaption == "" {
			h -= ceilPx(caption.Height) + c.NodeTextGapPx
		}
		selfCount, maxSelfLabelH := 0, 0.0
		for _, e := range g.Edges {
			if e.Source == n.ID && kinds[e.Ordinal] == EdgeSelfLoop {
				selfCount++
				if l, ok := labels[e.Ordinal]; ok { maxSelfLabelH = math.Max(maxSelfLabelH, l.H) }
			}
		}
		if selfCount > 0 {
			h = math.Max(h, math.Max(float64(2*selfCount+1)*math.Max(o.PortSpacing, maxSelfLabelH+2*o.LabelGap+c.MinClearancePx), maxSelfLabelH))
		}
		lines := label.Lines
		if lines < 1 {
			lines = 1
		}
		out[i] = nodeSize{W: math.Ceil(w), H: math.Ceil(h), TextLines: lines}
	}
	return out, nil
}
```

Note the test `TestNodeSizes_ShortLabelUsesMinimumWidth` expects the exact sum; because `ceilPx` already returns whole numbers and all contract values are whole numbers, `math.Ceil(h)` does not change it.

- [x] **Step 4: Run it, confirm it passes**

> RESOLVED (2026-10-17): The previously missing Arrange/Refine/MeasureSurface/BrowserRenderer and production root now exist. Focused sizing/routing/refinement tests, the whole domain package and race/vet gates pass. New real-Chrome probes verify saved Unicode node/branch measurements at the SQLite → Reader/cache sink and visual-only branch clipping → Refine → larger node/branch boxes. See completion evidence below. This closes this task's sizing gate, not P3T7/P3T14's separate complex-sample/user-route release acceptance.

Run: `go test ./domain/layout/ -run 'TestDefaultOptions|TestNodeSizes|TestEdgeLabelSizes|TestSizing' -v`
Expected: `--- PASS` for `TestDefaultOptions`, the five `TestNodeSizes_*`, `TestEdgeLabelSizes_IncludePadding`, `TestSizing_MissingMeasurementIsAnError`; `ok`.

Then the whole package: `go test ./domain/layout/` → `ok`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/
git add domain/layout/options.go domain/layout/size.go domain/layout/size_test.go
git commit -m "feat(layout): arrangement options and wrap-aware node/label sizing" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

- **Data source (production measurements):** `renderreview.BrowserRenderer.Measure` (P3T5) renders every `TextItem` in the dashboard's measure surface (P3T4 `MeasureSurface.tsx`) with the same CSS classes as the reviewed nodes and labels (P3T2 `nodeTextClassNames`, `edgeLabelClassName`) and the bundled Inter/Noto font assets. It is injected through `layout.Renderer` into `Coordinator` (P1T9), wired in `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` (P2T9 seam, P3T7 production body). No guessed Go font constants are used in production — `fakeMeasurer` exists only in `_test.go`.
- **Options source:** `DefaultOptions()` seeds attempt 1 inside `Coordinator.Run` (P1T9); later attempts use `Refine` (P1T8) driven by preflight (P1T7) and rendered-review findings (P3T6).
- **Consumers:** `nodeSizes`/`edgeLabelSizes` are stages of `Arrange` (P1T6).
- **Provenance probe:** P3T5's real-browser test asserts that a long label measured by the browser wraps (`Lines > 1`, `Width ≤ MaxWidth`); P3T6's reviewer flags `text_clipped` when a rendered box is smaller than its rendered text, and P1T8's `Refine` responds by raising `NodeTextSlack`.


### Required measurement and sizing regressions

Before implementation, add behavioral RED cases for Inf width/height, negative lines,
partially zero metrics, and a zero metric for nonempty primary/branch/type text. `Metric`
allows the all-zero metric only as a value; `edgeLabelSizes`/`nodeSizes` must additionally
check it against Graph text, reject nonempty text with zero dimensions/lines, and require
`Lines` and height to match the requested style's line height within browser rounding
tolerance. Empty type captions are legitimate, reserve no caption block/gap, and must
not silently turn into a measured nonempty caption. Include explicit newlines and long
unbroken Unicode text in the fake and real-browser cases: the fake must model explicit
line breaks as well as max-width wrapping and reject nonpositive/nonfinite MaxWidth.

For self loops, reserve right-side attachment height before placement. With n self loops,
`H >= (2*n+1)*max(PortSpacing,maxLabelHeight+2*LabelGap+MinClearancePx)`, so the existing fractional port formula gives at least
PortSpacing between all 2*n ports and from each corner. Reserve a separate nonoverlapping
right-hand label column per labelled self-loop occurrence (manifest ordinal order), each
with LabelGap around it; choose H at least the tallest label. This feeds P1T6's selfExtent
and label placement. Test 12 identical self loops, two long labelled self loops, a short
node beside a tall Unicode node, and absent self-loop measurement. Labels must not collide
with one another, node boxes or another loop path; spacing changes must actually alter
this geometry. A sizing error never mutates Graph or Measurements.

Run the focused tests RED (after compiling stubs), GREEN and the package gate. Temporarily
replace a requested measurement with a constant to show the Unicode/wrap/port tests fail,
then restore it. Production measurements remain P3T4 MeasureSurface → P3T5 BrowserRenderer;
no fake metrics enter newLayoutStack.

Branch-clipping RED probe: a labelled edge's visual-only text_clipped finding raises
NodeTextSlack through Refine; re-arranging must increase that edge label's W/H as well
as node text boxes. NodeTextSlack is the existing wire name for shared text rounding
slack. Do not add a second unconsumed option. edgeLabelSizes takes Options explicitly.

## Execution evidence

- Task 4.1: observed missing-symbol failure, then assertion failures with compiling empty-key/request and zero-metric stubs. Implemented the exact measurement value types, keys, manifest-ordered requests and metric validation. Focused tests and package gate pass. Commit: `69e69f5`.
- Task 4.2: observed missing-symbol failure, then behavioral failures with compiling zero-options/zero-size stubs. Implemented defaults, style-aware metric checking, wrap-aware node/label boxes, shared text slack, top/bottom port widths and self-loop right-side height reservation. Corrected the example helper's invalid options redeclaration so the caller's policy is actually consumed. Existing `validateGeometryOptions` already validates the complete options type; defaults pass it.
- Required local regressions cover NaN/Inf, negative and partially zero metrics, nonempty text with zero metrics, style/line-height inconsistency and rounding tolerance, empty captions, explicit newlines, long unbroken Unicode, invalid fake wrap widths, 12 identical self loops, two long labelled self loops beside a tall Unicode node, absent self-loop metrics, spacing changes, nil measurement input, and unchanged graph/measurement/label inputs on error.
- Self-review in order: (1) checked the exact shared interfaces, defaults, sizing formulas and assigned ownership; added no flag, second slack option or adapter. Full routing/refinement/browser checks remain blocked under Task 4.2 Step 4, not waived. (2) temporarily replaced requested nonempty metrics with a constant single-line metric. Strengthened the port/self-loop tests to derive expected reservations from the original measurements, not from the label-size output being tested; Unicode, wrapping, branch-port and self-loop tests then all failed. A separate fake-font mutation failed style assertions. Restored all mutations. Tests assert sizing sinks through `BuildGraph` → `MeasureRequestFor` → load-bearing test measurer → sizing; they are unit tests, not evidence of production wiring. (3) checked neighboring Go conventions, kept the existing Options type/validator, used a named browser rounding tolerance, and accumulated self-loop reservations in one edge pass instead of rescanning every edge for every node. No compiling stub or constant-metric mutation remains.
- Final GREEN: both prescribed focused commands, `go test ./domain/layout/`, `go test -race ./domain/layout/`, `go vet ./domain/layout/`, `gofmt -l domain/layout/` (empty) and `git diff --check` pass.
- Original pass: production completion was blocked pending P1T6/P1T8/P2T9/P3T4/P3T5/P3T7. The follow-up below closes that recorded sizing dependency gate.

### Completion evidence — 2026-10-17 follow-up

- Re-selected this first unfinished task after confirming its named production dependencies now exist. No later plan task was changed. Existing sizing behavior needed no production rewrite; under contracts §6.1 the added characterization probes began green, then required mutation REDs before restoration/GREEN.
- `cmd/arbitron/layout_sizing_e2e_test.go:TestLayoutSizing_RealBrowserSavedUnicodeMeasurementsReachView` boots actual `initDomain` with Chrome, creates a short-labelled workflow through Service, then updates it with explicit newlines and unbroken CJK node/branch text. Both jobs pass real DOM/raster review. Assertions cover exact saved text/revision, browser line counts and minimum heights in persisted geometry, growth from the short revision, Reader geometry, exact-token cache publication, on-disk artifact hashes, unchanged saved manifest bytes and unchanged historical result. Missing/empty measurement or root wiring cannot satisfy the passed result/view/cache assertions.
- `adapter/renderreview/sizing_browser_test.go:TestSizing_RealBrowserBranchClippingRefinesSharedTextSlack` deliberately understates only a real browser-measured branch height. Geometric preflight does not invent clipping; actual DOM/glyph review reports `text_clipped` for ordinal 0. Those real findings drive Refine, and Arrange must enlarge the branch's W/H and node H by the shared slack increment. The original real measurements remain unchanged.
- Observed RED: replacing long-text metrics with a single-line constant in `metricForText` leaves the short revision passing but makes the Unicode saved revision `needs_attention` with actual clipped node/branch findings (`/tmp/layout-sizing-unicode-red.log`). Removing branch `NodeTextSlack` makes both exact branch growth assertions fail (`/tmp/layout-sizing-slack-red.log`, expected 181/30, actual 173/22). Both mutations were restored; `domain/layout/size.go` has no diff.
- Ordered self-review: (1) spec compliance — only this task's missing sizing/provenance regressions were added; no options, renderer behavior, route wiring or release-gate claims; (2) anti-green-theater — nonempty real saved manifests reach SQLite, reviewed PNGs, Reader and cache, with constant-metric/shared-slack mutation failures and the existing nil-measurement browser negative; (3) code quality — neighboring root/browser helpers reused, bounded waits, nil review/label guards, gofmt/vet clean.
- Final GREEN: prescribed focused measurement/default/sizing tests plus occurrence-port/self-loop/long-Unicode routing and Refine regressions; `go test ./domain/layout/ -count=1` (4.513s); `go test -race ./domain/layout/` (25.041s); `go vet ./domain/layout/ ./adapter/renderreview ./cmd/arbitron`; gofmt/diff checks. Browser-required combined probes (new tests, existing real Unicode/newline measurement, nil measurement rejection, visual-only node clipping and root pr-review) pass without skips (adapter 19.335s, root 23.182s). New browser/root tests also pass under `-race` (18.722s/24.760s). Final logs: `/tmp/layout-sizing-domain.log`, `/tmp/layout-sizing-browser-green.log`, `/tmp/layout-sizing-race-green.log`.
- Broader P3T7 complex-sample/Docker and P3T14 actual user-route acceptance remain separately tracked; no missing producer is accepted as a no-op here.

## Measurement double cancellation control

- [x] Add/retain a canceled-context regression for fakeMeasurer.Measure: returns
  context.Canceled with no successful measurements or call-count success. Keep actual
  stage cancellation proof in P1T9/P2T8 with a blocked producer. Removing the ctx.Err
  check must fail this assertion; the fake may not ignore the stage context.

Completion evidence: `TestFakeMeasurer_CanceledContext` drives a nonempty
BuildGraph → MeasureRequestFor request through the helper. Observed behavioral RED:
canceled context returned nil error, successful metrics and an incremented call count.
The helper now checks ctx.Err before counting/measurement. The same helper still
produces nonempty metrics with a live context. Removing the guard reproduced RED;
restoring it passed. Ordered self-review confirmed exact test-support scope, assertions
at the measurement/error/count sinks (including the live-input control), and neighboring
Go conventions. No production fake or new wiring was added; production measurements
remain BrowserRenderer → Coordinator at the existing composition root. After review
and mutation restoration, both prescribed focused commands, the whole domain/layout
package, its race gate, go vet, gofmt and git diff --check pass.
