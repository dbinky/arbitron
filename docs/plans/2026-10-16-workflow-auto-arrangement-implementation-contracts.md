# Workflow Auto-Arrangement — Cross-Task Contracts

Shared reference for every `2026-10-16-workflow-auto-arrangement-implementation-phase-N-task-M.md` plan. Plans cite it as "contracts §N" or "contracts addendum N". Section 6 records binding decisions; sections 0–3 and each task Interfaces block must agree with them. Sections 6.18–6.20 record the current-source reconciliation, split-task ownership and the ordered v2 group migration; dated execution evidence remains historical, not an instruction to skip newly available work. Section 5 records supporting details, not permission to preserve contradictory signatures or examples. No task-local “deviation” changes a shared contract.

Every plan task file MUST use exactly these names, signatures, JSON tags, file paths and
values. If a task needs something not listed here, it must define it itself (inside its own
files) and must not be referenced by another task unless listed here.

Every task inherits the spec's global constraints: every authoring save; immediate save
before render/review; default five total attempts (file-configurable); presentation-only
changes; saved-revision/generation fencing; shared workflow/instance geometry; no manual
positions/opt-outs and no automatic processing of untouched legacy workflows. Tests must
cover actual rendered acceptance, fallback, stale/parallel edges, Unicode and outages.

Repo root (Go module `github.com/arbitron/arbitron`, Go 1.25.5; dashboard React 19 /
Vite 8 / Vitest 4 / @xyflow/react 12; testify assert/require in Go tests).
Spec: `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`.
Designs: `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-{1,2,3}.md`.

## Review focus

- A save acknowledged before a process crash must recover exactly its committed revision,
  including re-publication over an existing publication; P2T3/P2T4/P2T7/P2T9 own proof.
- Two saves/retries/processes and relocation may interleave; old jobs cannot publish or
  settle cache metadata over newer work; P2T5/P2T6/P2T7/P2T8/P3T8 own fences.
- Missing source data, browser assets, glyphs, arrows, labels or raster ink must fail actual
  review despite clean geometry/DOM; P1T8/P1T9/P3T2–P3T7 own evidence and mutation tests.
- A completed or historical instance still needs the exact snapshot revision's eventual
  layout, even after workflow deletion or visibility changes; P2T5/P3T8/P3T11–P3T14 own it.
- Long branching graphs, parallel occurrences, self-loops and Unicode must remain
  traceable at 0.9 zoom; P1T3–P1T9/P3T2–P3T7/P3T14 own real sample acceptance.

---------------------------------------------------------------------------------------
## 0. Task map (file name = docs/plans/2026-10-16-workflow-auto-arrangement-implementation-phase-N-task-M.md)

| ID | Title | Owns (create/modify) |
|---|---|---|
| P1T1 | Graph input normalisation | domain/layout/graph.go, graph_test.go |
| P1T2 | Renderer contract + geometry model + validation | domain/layout/contract.go, geometry.go, options.go (type), contract_test.go, geometry_test.go, dashboard/src/graph/arranged/contract.json |
| P1T3 | Edge classification (back-edges) + ranks | domain/layout/loops.go, rank.go, loops_test.go, rank_test.go, testhelpers_test.go (loadSample/edgeGraph) |
| P1T4 | Measurement port, options, node sizing | domain/layout/measure.go, options.go (defaults/validation), size.go, measure_test.go, size_test.go, testhelpers_test.go |
| P1T5 | Layering with dummies + crossing-minimising order | domain/layout/layers.go, layers_test.go |
| P1T6 | Placement + orthogonal routing → `Arrange` | domain/layout/place.go, route.go, arrange.go, arrange_test.go |
| P1T7 | Findings, score, geometric preflight (+ sample fixtures) | domain/layout/finding.go, preflight.go, preflight_test.go, samples_geometry_test.go |
| P1T8 | Render/review ports + findings-driven refinement | domain/layout/render.go, refine.go, refine_test.go |
| P1T9 | Bounded attempt coordinator | domain/layout/coordinator.go, coordinator_test.go |
| P2T1 | `LayoutConfig` (maxattempts, browserpath, artifactdir) | domain/config/config.go, adapter/config/loader.go, adapter/config/loader_layout_test.go |
| P2T2 | Presentation metadata, save-side ports, `RevisionStamper`, snapshot propagation | domain/workflow/workflow.go, presentation.go, arrangement.go, revision_stamper.go, revision_stamper_test.go, domain/engine/engine_presentation_test.go |
| P2T3 | Stamp every authoring save path in `workflow.Service` | domain/workflow/service.go, admin_ops.go, rename.go, service_revision_test.go |
| P2T4 | Seeder arranges newly seeded samples | domain/samples/seeder.go, seeder_revision_test.go, cmd/arbitron/sample_seed.go |
| P2T5 | `layout.Store` contract, `Requester`, `Reader`, `RepositoryManifests` | domain/layout/store.go, requester.go, reader.go, manifests.go, memstore_test.go, requester_test.go, reader_test.go |
| P2T6 | SQLite migration 019 + `LayoutRepo` + revision-specific publication proof | adapter/sqlite/migrations/019_layout_revisions.{up,down}.sql, adapter/sqlite/layout_repo.go, layout_repo_test.go, published_workflow_repo.go, published_workflow_repo_test.go |
| P2T7 | Filesystem: cross-process manifest lock, revision journal, layout.json reservation + fenced cache publisher | adapter/filesystem/manifest_lock.go, revision_journal.go, layout_cache.go, workflow_repo.go, tests, go.mod (gofrs/flock) |
| P2T8 | `layout.Worker` (claim/run/persist/publish/recover/cancel) | domain/layout/worker.go, worker_test.go, worker_lifecycle_test.go, worker_recovery_test.go |
| P2T9 | Composition root wiring (serve + mcp) | cmd/arbitron/layout_wiring.go, cmd/arbitron/main.go, cmd/arbitron/layout_e2e_test.go |
| P3T1 | Dashboard layout types, payload validation, API client | dashboard/src/graph/arranged/types.ts, validate.ts(+test), dashboard/src/api/types.ts, dashboard/src/api/client.ts(+test), dashboard/src/graph/types.ts |
| P3T2 | ArrangedNode + RoutedEdge (+ Inter font) | dashboard/src/graph/arranged/ArrangedNode.tsx(+.module.css,+test), RoutedEdge.tsx(+.module.css,+test), routedPath.ts(+test), dashboard/package.json |
| P3T3 | Shared renderer `ArrangedGraph` + fallback conversion | dashboard/src/graph/arranged/buildArrangedFlow.ts(+test), fallbackLayout.ts(+test), ArrangedGraph.tsx(+test), dashboard/src/graph/useGraphLayout.ts(+test) |
| P3T4 | Deterministic render/measure surface (render.html) | dashboard/render.html, dashboard/vite.config.ts, dashboard/src/render/{main.tsx,RenderSurface.tsx,MeasureSurface.tsx,report.ts,contrast.ts,job.ts}(+tests) |
| P3T5 | Go browser renderer (chromedp) + embedded asset server | adapter/dashboard/embed.go, adapter/renderreview/{assets.go,browser.go,tiles.go}(+tests), go.mod (chromedp) |
| P3T6 | Go readability reviewer | adapter/renderreview/reviewer.go, reviewer_test.go |
| P3T7 | Wire production renderer/reviewer + runtime (Docker) + real-browser acceptance | cmd/arbitron/layout_wiring.go, cmd/arbitron/layout_render_e2e_test.go, Dockerfile, Makefile (test-render) |
| P3T8 | Authorised query/retry seams | domain/workflow/layout_access.go(+test), domain/engine/layout_access.go(+test), domain/layout/query.go(+test), cmd/arbitron/layout_wiring.go (query construction) |
| P3T9 | HTTP surfaces | adapter/http/layout_handler.go(+test), workflow_handler.go, instance_handler.go, server.go, cmd/arbitron/main.go (RouterDeps.Layouts) |
| P3T10 | MCP surfaces | adapter/mcp/tools_layout.go(+test), tools_workflow.go, deps.go, register.go, register_test.go, cmd/arbitron/main.go (mcpDeps Layouts) |
| P3T11 | `useLayoutStatus` polling hook + `LayoutStatusPanel` | dashboard/src/hooks/useLayoutStatus.ts(+test), dashboard/src/graph/arranged/LayoutStatusPanel.tsx(+.module.css,+test) |
| P3T12 | WorkflowReview uses shared renderer; manual positioning removed client-side | dashboard/src/routes/WorkflowReview.tsx, WorkflowReview.test.tsx, dashboard/src/api/client.ts (remove saveLayout) |
| P3T13 | ExecutionView/GraphView uses shared renderer + independent layout polling | dashboard/src/routes/ExecutionView.tsx, GraphView.test.tsx, GraphView.loops.test.tsx, ExecutionView.layout.test.tsx |
| P3T14 | Remove server-side layout writes (SaveNodePositions/PUT layout) + rebuild embedded UI | domain/workflow/ports.go, service.go, adapter/filesystem/workflow_repo.go, adapter/http/workflow_handler.go, server.go, all test fakes, adapter/dashboard/dist (make ui) |
| P3T15 | Captured group input, vocabulary and protocol | domain/layout/group_geometry.go; Graph.Groups, View.Groups, MeasureRequestFor, captured RenderRequest/ReviewRequest.WorkflowGroups; Go/TS wire declarations |
| P3T16 | V2 group geometry, routing and preflight | domain/layout/group_layout.go, group_preflight.go; Arrange, Geometry.Validate, contract.go/contract.json, TS validation and structural fixtures |
| P3T17 | Shared group DOM and capture | group components, buildArrangedFlow runtime API, groupProbes.ts, group_render.go, actual RenderReport group fields, RenderSurface/report.ts and BrowserRenderer |
| P3T18 | Group readability and persisted truth | adapter/renderreview reviewer + genuine PNG fixtures; Reader/Requester compatibility, layouttest doubles, SQLite/cache JSON round trips |
| P3T19 | Live group and descendant tile mounts | useInstanceLayoutTiles.ts, composeArrangedTiles.ts, abortable instance client reads, pure computeGraphLayout; real WorkflowReview/ExecutionView mounts |
| P3T20 | Group root and release proof | cmd/arbitron/layout_groups_e2e_test.go, layout_group_surfaces_test.go; existing unfiltered test-render/test-all/CI and rebuilt embedded assets |

Wiring/data-source chain (who feeds whom in production):
- Manifest (nodes/edges) → captured by `RevisionStamper` (P2T2) at every save path (P2T3, P2T4) → `ArrangementRequest.Manifest` → `layout.Requester` (P2T5) → `layout.Store` row (P2T6) → `layout.Worker` (P2T8) unmarshals → `Coordinator.Run` (P1T9).
- Measurements + rendered evidence: `renderreview.BrowserRenderer` (P3T5) using the render surface (P3T4) built from the shared renderer (P3T2/P3T3); `renderreview.ReadabilityReviewer` (P3T6); wired by P3T7 into `newLayoutRenderReviewer` (P2T9 creates the seam).
- Attempt budget: YAML `layout.maxattempts` (P2T1) → `layout.NewRequester(..., cfg.Layout.MaxAttempts, ...)` (P2T9) → row `attempt_limit` → `Job.MaxAttempts`.
- Views: `layout.QueryService` (P3T8) constructed in `cmd/arbitron/layout_wiring.go` → HTTP (P3T9) / MCP (P3T10) → dashboard client (P3T1) → `useLayoutStatus` (P3T11) → `ArrangedGraph` (P3T3) in WorkflowReview (P3T12) / ExecutionView (P3T13).

---------------------------------------------------------------------------------------
## 1. Phase 1 — `domain/layout` (package `layout`, import path `github.com/arbitron/arbitron/domain/layout`)

Phase 1 tests use `package layout` (internal); Phase 2/3 tests of domain/layout use `package layout_test` when importing `layouttest`, to avoid an import cycle.

### graph.go (P1T1)

```go
var (
	ErrEmptyGraph    = errors.New("layout: workflow has no nodes")
	ErrEmptyNodeID   = errors.New("layout: node has an empty id")
	ErrDuplicateNode = errors.New("layout: duplicate node id")
	ErrDanglingEdge  = errors.New("layout: edge references an unknown node")
)
type GraphNode struct { ID, Text, TypeCaption string; Entrypoint bool }   // Text = Label if non-empty else ID; TypeCaption = strings.ReplaceAll(string(Type), "_", " ")
type GraphEdge struct { Ordinal int; Source, Target, Output, Label, Text string } // Text = Label if non-empty else Output ("" = no branch text)
type Graph struct { Nodes []GraphNode; Edges []GraphEdge; index map[string]int }
func BuildGraph(wf *workflow.Workflow) (Graph, error)
func (g Graph) NodeIndex(id string) (int, bool)
```

### contract.go (P1T2)

```go
const ContractVersion = "arranged-renderer/1"
type RendererContract struct {
	Version           string  `json:"version"`           // "arranged-renderer/1"
	FontFamily        string  `json:"fontFamily"`        // "Inter, Noto Sans SC, Noto Emoji"
	FontPackage       string  `json:"fontPackage"`       // "@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2"
	NodeLabelFontPx   float64 `json:"nodeLabelFontPx"`   // 13
	NodeLabelWeight   int     `json:"nodeLabelWeight"`   // 600
	NodeTypeFontPx    float64 `json:"nodeTypeFontPx"`    // 10
	NodeTypeWeight    int     `json:"nodeTypeWeight"`    // 700
	EdgeLabelFontPx   float64 `json:"edgeLabelFontPx"`   // 11
	EdgeLabelWeight   int     `json:"edgeLabelWeight"`   // 500
	LineHeight        float64 `json:"lineHeight"`        // 1.35
	NodePaddingX      float64 `json:"nodePaddingX"`      // 12
	NodePaddingY      float64 `json:"nodePaddingY"`      // 8
	NodeBorderPx      float64 `json:"nodeBorderPx"`      // 2
	NodeTypeBarPx     float64 `json:"nodeTypeBarPx"`     // 4
	NodeTextGapPx     float64 `json:"nodeTextGapPx"`     // 2
	NodeMinWidth      float64 `json:"nodeMinWidth"`      // 180
	NodeTextMaxWidth  float64 `json:"nodeTextMaxWidth"`  // 240
	EdgeLabelPaddingX float64 `json:"edgeLabelPaddingX"` // 6
	EdgeLabelPaddingY float64 `json:"edgeLabelPaddingY"` // 3
	EdgeLabelMaxWidth float64 `json:"edgeLabelMaxWidth"` // 160
	StrokePx          float64 `json:"strokePx"`          // 2
	ArrowPx           float64 `json:"arrowPx"`           // 10
	CrossingTreatment string  `json:"crossingTreatment"` // "gap"
	CrossingGapPx     float64 `json:"crossingGapPx"`     // 10
	ForwardColor      string  `json:"forwardColor"`      // "#8a8ab0"
	LoopReturnColor   string  `json:"loopReturnColor"`   // "#7c8cf5"
	LoopReturnDash    string  `json:"loopReturnDash"`    // "6 4"
	PracticalZoom     float64 `json:"practicalZoom"`     // 0.9
	MinReadableTextPx float64 `json:"minReadableTextPx"` // 9
	MinContrastRatio  float64 `json:"minContrastRatio"`  // 4.5
	MinClearancePx    float64 `json:"minClearancePx"`    // 4
}
func DefaultContract() RendererContract
```
`dashboard/src/graph/arranged/contract.json` holds exactly these JSON keys/values; a Go test
(`contract_test.go`) reads `../../dashboard/src/graph/arranged/contract.json` and asserts
equality with `DefaultContract()`.

### geometry.go (P1T2)

```go
const FormatV1 = "arbitron.layout/v1"
var ErrInvalidGeometry = errors.New("layout: invalid geometry")
type Point struct { X float64 `json:"x"`; Y float64 `json:"y"` }
type Rect struct { X float64 `json:"x"`; Y float64 `json:"y"`; W float64 `json:"w"`; H float64 `json:"h"` }
func (r Rect) Right() float64; func (r Rect) Bottom() float64; func (r Rect) CenterX() float64; func (r Rect) CenterY() float64
func (r Rect) Inflate(d float64) Rect
func (r Rect) Overlaps(o Rect) bool          // strict interior overlap on both axes
func (r Rect) Contains(o Rect) bool
type EdgeKind string
const ( EdgeForward EdgeKind = "forward"; EdgeLoopReturn EdgeKind = "loop_return"; EdgeSelfLoop EdgeKind = "self_loop" )
type Side string
const ( SideTop Side = "top"; SideBottom Side = "bottom"; SideLeft Side = "left"; SideRight Side = "right" )
type Attachment struct { NodeID string `json:"nodeId"`; Side Side `json:"side"`; Point Point `json:"point"` }
type LabelBox struct { Text string `json:"text"`; Box Rect `json:"box"`; Lines int `json:"lines"` }
type NodeBox struct {
	ID          string `json:"id"`
	Box         Rect   `json:"box"`
	Text        string `json:"text"`        // displayed primary text
	TypeCaption string `json:"typeCaption"`
	TextLines   int    `json:"textLines"`
	Rank        int    `json:"rank"`
}
type EdgeRoute struct {
	Ordinal      int        `json:"ordinal"`
	Source       string     `json:"source"`
	Target       string     `json:"target"`
	Output       string     `json:"output,omitempty"`
	Label        string     `json:"label,omitempty"`
	Kind         EdgeKind   `json:"kind"`
	Side         Side       `json:"side,omitempty"`   // gutter side for loop_return; "right" for self_loop
	Lane         int        `json:"lane,omitempty"`   // 1-based gutter lane for loop_return
	Points       []Point    `json:"points"`           // orthogonal polyline, first = SourceAttach.Point, last = TargetAttach.Point
	SourceAttach Attachment `json:"sourceAttach"`
	TargetAttach Attachment `json:"targetAttach"`
	Text         *LabelBox  `json:"text,omitempty"`   // branch label (GraphEdge.Text) when non-empty
}
type Crossing struct { Point Point `json:"point"`; Over int `json:"over"`; Under int `json:"under"` } // Under edge is drawn with a gap
type Geometry struct {
	Format    string           `json:"format"`
	Contract  RendererContract `json:"contract"`
	Options   Options          `json:"options"`
	Nodes     []NodeBox        `json:"nodes"`     // manifest node order
	Edges     []EdgeRoute      `json:"edges"`     // manifest edge order, Edges[i].Ordinal == i
	Crossings []Crossing       `json:"crossings"`
	Canvas    Rect             `json:"canvas"`
}
func (g Geometry) Validate(gr Graph) error        // structural coverage/text/attachment/path/canvas checks; wraps ErrInvalidGeometry
func GeometryDigest(g Geometry) string            // hex sha256 of json.Marshal(g)
func Compatible(g Geometry, gr Graph) bool        // Validate(gr)==nil && g.Contract == DefaultContract()
```

### measure.go / options.go / size.go (P1T4)

```go
type TextStyle string
const ( StyleNodeLabel TextStyle = "node_label"; StyleNodeType TextStyle = "node_type"; StyleEdgeLabel TextStyle = "edge_label" )
type TextItem struct { Key string `json:"key"`; Text string `json:"text"`; Style TextStyle `json:"style"`; MaxWidth float64 `json:"maxWidth"` }
type MeasureRequest struct { Contract RendererContract `json:"contract"`; Items []TextItem `json:"items"` }
type TextMetrics struct { Width float64 `json:"width"`; Height float64 `json:"height"`; Lines int `json:"lines"` }
type Measurements struct { Contract RendererContract `json:"contract"`; FontsReady bool `json:"fontsReady"`; Items map[string]TextMetrics `json:"items"` }
var ErrMissingMeasurement = errors.New("layout: missing text measurement")
func NodeLabelKey(id string) string   // "node:" + id + ":label"
func NodeTypeKey(id string) string    // "node:" + id + ":type"
func EdgeLabelKey(ordinal int) string // fmt.Sprintf("edge:%d", ordinal)
func MeasureRequestFor(g Graph, c RendererContract) MeasureRequest
type Options struct {
	RankSpacing    float64 `json:"rankSpacing"`    // 48
	NodeSpacing    float64 `json:"nodeSpacing"`    // 56
	LaneSeparation float64 `json:"laneSeparation"` // 14
	GutterWidth    float64 `json:"gutterWidth"`    // 48
	LabelGap       float64 `json:"labelGap"`       // 10
	PortSpacing    float64 `json:"portSpacing"`    // 24
	NodeTextSlack  float64 `json:"nodeTextSlack"`  // 0; shared node/branch text rounding slack
	DummyWidth     float64 `json:"dummyWidth"`     // 24
	CrossingSweeps int     `json:"crossingSweeps"` // 8
	Margin         float64 `json:"margin"`         // 40
}
func DefaultOptions() Options
func (m Measurements) Metric(key string) (TextMetrics, error)
```
Test helper (testhelpers_test.go, package layout): `type fakeMeasurer struct{ charW float64; fontsReady bool; err error; calls int }`
with `Measure(ctx, req) (Measurements, error)` honouring each item's Text, Style (font px) and MaxWidth;
`func newFakeMeasurer() *fakeMeasurer` (charW 7, fontsReady true); `func loadSample(t, name string) *workflow.Workflow` reading `../../samples/<name>/workflow.json`.

### Shared internal helpers (exact owners; package layout)

```go
// P1T2 geometry.go: structural primitives available before preflight exists.
const geomEps = 0.01
func nearly(a, b float64) bool
func samePoint(a, b Point) bool
func finite(v float64) bool
func finitePoint(p Point) bool
func finiteRect(r Rect) bool
func onSide(p Point, side Side, r Rect) bool
// P1T3 loops.go/rank.go
func classifyEdges(g Graph) []EdgeKind
func loopBody(g Graph, kinds []EdgeKind, ordinal int) []int
func topoOrder(g Graph, kinds []EdgeKind) []int
func assignRanks(g Graph, kinds []EdgeKind) []int
// P1T4 size.go
type labelSize struct { W, H float64; Lines int }
type nodeSize struct { W, H float64; TextLines int }
func edgeLabelSizes(g Graph, m Measurements, c RendererContract, o Options) (map[int]labelSize, error)
func nodeSizes(g Graph, kinds []EdgeKind, m Measurements, c RendererContract, o Options, labels map[int]labelSize) ([]nodeSize, error)
func ceilPx(v float64) float64
func metricForText(m Measurements, key, text string, fontPx, lineHeight, maxWidth float64) (TextMetrics, error)
// P1T5 layers.go
type layerItem struct { node, edge, rank int }
type layering struct { rows [][]int; items []layerItem; itemOfNode []int; chains map[int][]int; up, down [][]int }
func buildLayering(g Graph, kinds []EdgeKind, ranks []int) *layering
func (l *layering) positions() []int
func (l *layering) crossings() int
func orderLayers(l *layering, sweeps int)
// P1T6 route.go, consumed by P1T7 preflight.go
type segment struct { ord int; a, b Point; horiz bool }
func segmentsOf(routes []EdgeRoute) []segment
func findCrossings(routes []EdgeRoute) []Crossing
```

`edgeGraph(t *testing.T, pairs ...[2]string) Graph` is the P1T3 test helper;
a no-edge test explicitly supplies at least one node and does not call an empty helper
that BuildGraph correctly rejects. P1T2 owns Options' type; P1T4 owns defaults and sizing.

### Arrange (P1T6)

```go
var ErrContractMismatch = errors.New("layout: measurements were taken with a different renderer contract")
var ErrFontsNotReady = errors.New("layout: renderer fonts were not ready when measuring")
func Arrange(g Graph, m Measurements, c RendererContract, o Options) (Geometry, error)
```

### finding.go / preflight.go (P1T7)

```go
type Severity string
const ( SeverityBlocking Severity = "blocking"; SeverityAdvisory Severity = "advisory" )
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
	CriterionCrossings           Criterion = "crossings" // advisory only
)
type FindingSource string
const ( SourcePreflight FindingSource = "preflight"; SourceReview FindingSource = "review" )
type Finding struct {
	Criterion    Criterion     `json:"criterion"`
	Severity     Severity      `json:"severity"`
	Source       FindingSource `json:"source"`
	NodeIDs      []string      `json:"nodeIds,omitempty"`
	EdgeOrdinals []int         `json:"edgeOrdinals,omitempty"`
	Evidence     string        `json:"evidence"`
}
func BlockingCount(fs []Finding) int
type Score struct { Blocking int `json:"blocking"`; Advisory int `json:"advisory"`; Crossings int `json:"crossings"`; Attempt int `json:"attempt"` }
func ScoreOf(attempt int, findings []Finding, crossings int) Score
func (s Score) Less(o Score) bool   // lexicographic: Blocking, Advisory+Crossings, Attempt
func Preflight(g Graph, geo Geometry, m Measurements) []Finding
```

### render.go / refine.go (P1T8)

```go
var RuntimeStates = []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}
type RenderRequest struct { Revision string `json:"revision"`; Attempt int `json:"attempt"`; Geometry Geometry `json:"geometry"`; WorkflowNodeTypes map[string]string `json:"workflowNodeTypes"`; States []string `json:"states"` }
func NewRenderRequest(revision string, attempt int, geo Geometry, nodeTypes map[string]string) RenderRequest // copies nodeTypes and RuntimeStates
type RenderedSegment struct { Start Point `json:"start"`; End Point `json:"end"` }
type RenderedText struct { Content string `json:"content"`; Glyphs []Rect `json:"glyphs"`; Visible bool `json:"visible"`; Box Rect `json:"box"`; Clipped bool `json:"clipped"`; FontPx float64 `json:"fontPx"`; Contrast float64 `json:"contrast"`; Occluded bool `json:"occluded"` }
type RenderedNode struct { ID string `json:"id"`; Box Rect `json:"box"`; Label RenderedText `json:"label"`; Type RenderedText `json:"type"` }
type RenderedEdge struct { Segments []RenderedSegment `json:"segments"`; StrokeColor string `json:"strokeColor"`; DashPattern string `json:"dashPattern"`; Visible bool `json:"visible"`; ArrowPoints []Point `json:"arrowPoints"`; Ordinal int `json:"ordinal"`; Start Point `json:"start"`; End Point `json:"end"`; HasArrow bool `json:"hasArrow"`; Dashed bool `json:"dashed"`; StrokePx float64 `json:"strokePx"`; Label *RenderedText `json:"label,omitempty"` }
type RenderedCrossing struct { Point Point `json:"point"`; Over int `json:"over"`; Under int `json:"under"`; UnderGapVisible bool `json:"underGapVisible"`; OverContinuous bool `json:"overContinuous"` }
type StateProbe struct { State string `json:"state"`; NodeBoxesStable bool `json:"nodeBoxesStable"`; LabelsReadable bool `json:"labelsReadable"`; Issues []string `json:"issues,omitempty"` }
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
type Artifact struct { Kind string `json:"kind"`; Path string `json:"path"`; SHA256 string `json:"sha256"`; Region Rect `json:"region"`; Scale float64 `json:"scale"` } // Kind "overview" | "tile"
type Rendering struct { Revision string `json:"revision"`; Attempt int `json:"attempt"`; GeometryDigest string `json:"geometryDigest"`; Report RenderReport `json:"report"`; Artifacts []Artifact `json:"artifacts"`; RenderedAt time.Time `json:"renderedAt"` }
type ReviewRequest struct { Revision string `json:"revision"`; Attempt int `json:"attempt"`; Geometry Geometry `json:"geometry"`; Rendering Rendering `json:"rendering"` }
type Evidence struct { GeometryDigest string `json:"geometryDigest"`; RendererVersion string `json:"rendererVersion"`; Contract RendererContract `json:"contract"`; FontsReady bool `json:"fontsReady"`; Zoom float64 `json:"zoom"`; Tiles int `json:"tiles"`; Artifacts []Artifact `json:"artifacts"`; ReviewedAt time.Time `json:"reviewedAt"` }
type Review struct { Rendered bool `json:"rendered"`; Findings []Finding `json:"findings"`; Evidence Evidence `json:"evidence"` }
type Renderer interface {
	Measure(ctx context.Context, req MeasureRequest) (Measurements, error)
	Render(ctx context.Context, req RenderRequest) (Rendering, error)
}
type Reviewer interface { Review(ctx context.Context, req ReviewRequest) (Review, error) }
func Refine(o Options, findings []Finding) (Options, bool)
```

### coordinator.go (P1T9)

```go
type Stage string
const ( StageQueued Stage = "queued"; StageArranging Stage = "arranging"; StageRendering Stage = "rendering"; StageReviewing Stage = "reviewing"; StagePublishing Stage = "publishing"; StageDone Stage = "done" )
type Verdict string
const ( VerdictPassed Verdict = "passed"; VerdictNeedsAttention Verdict = "needs_attention"; VerdictFailed Verdict = "failed" )
type Job struct { Revision string; Manifest *workflow.Workflow; MaxAttempts int }
type Progress struct { Stage Stage; Attempt int }
type Candidate struct { Attempt int `json:"attempt"`; Geometry Geometry `json:"geometry"`; Preflight []Finding `json:"preflight"`; Review *Review `json:"review,omitempty"`; Score Score `json:"score"`; Passed bool `json:"passed"` }
type Failure struct { Stage Stage `json:"stage"`; Reason string `json:"reason"` }
type Result struct {
	Verdict         Verdict
	Best            *Candidate   // nil when no geometry was produced
	Findings        []Finding    // Best's preflight + review findings (unresolved)
	AttemptsUsed    int
	Failure         *Failure     // set for VerdictFailed (incl. cancellation)
	Cancelled       bool
	Unreviewed      bool         // Best has no successful rendered review
	ExhaustedReason string       // "budget" | "no_refinement" for needs_attention
}
type Coordinator struct { /* unexported */ }
type CoordinatorOption func(*Coordinator)
func WithContract(c RendererContract) CoordinatorOption
func WithOptions(o Options) CoordinatorOption
func WithStageTimeout(d time.Duration) CoordinatorOption
func HasRenderedEvidence(geo Geometry, review *Review) bool // exact candidate + full contract/version/fonts/zoom + overview/readable tiles (§6.5)
var ErrNilRenderer = errors.New("layout: renderer is required"); var ErrNilReviewer = errors.New("layout: reviewer is required")
func NewCoordinator(renderer Renderer, reviewer Reviewer, opts ...CoordinatorOption) (*Coordinator, error)
func (c *Coordinator) Run(ctx context.Context, job Job, progress func(Progress)) Result
```
Pass rule: `BlockingCount(preflight)==0 && HasRenderedEvidence(geo, review) && BlockingCount(review.Findings)==0`. Advisory crossings pass only after bounded crossing search cannot improve them. `Run` builds `WorkflowNodeTypes` directly from immutable `Job.Manifest.Nodes` and passes it to `NewRenderRequest`; no renderer reconstructs types from captions.
Budget: `max(job.MaxAttempts,1)` total attempts; attempt counted when arranging starts; measuring happens once at the start of attempt 1 (a measure error ⇒ VerdictFailed, Failure{StageRendering,...}, AttemptsUsed 1).

---------------------------------------------------------------------------------------
## 2. Phase 2

### Config (P2T1) — domain/config/config.go

```go
// in Config:
Layout LayoutConfig
// LoadWarnings collects non-fatal normalisation warnings (e.g. clamped values) for the composition root to log.
LoadWarnings []string
type LayoutConfig struct {
	MaxAttempts int    // layout.maxattempts, default 5, values < 1 clamp to 1 with a warning
	BrowserPath string // layout.browserpath, default "" (auto-detect Chrome/Chromium)
	ArtifactDir string // layout.artifactdir, default "" (composition root uses <dir of database.path>/layout-artifacts)
}
```
Constant `adapterconfig.DefaultLayoutMaxAttempts = 5`. Env: `ARBITRON_LAYOUT_MAXATTEMPTS`, `ARBITRON_LAYOUT_BROWSERPATH`, `ARBITRON_LAYOUT_ARTIFACTDIR`.
Warning text exactly: `fmt.Sprintf("layout.maxattempts %d is below 1; using 1", v)`.

### domain/workflow (P2T2)

workflow.go: add to `Workflow` (after Edges):
```go
Presentation *Presentation `json:"presentation,omitempty"`
```
presentation.go:
```go
type SaveReason string
const ( SaveReasonCreate SaveReason = "create"; SaveReasonUpdate SaveReason = "update"; SaveReasonPublish SaveReason = "publish"; SaveReasonCopy SaveReason = "copy"; SaveReasonDuplicate SaveReason = "duplicate"; SaveReasonRename SaveReason = "rename"; SaveReasonSeed SaveReason = "seed" )
type RetainedSource string
const ( RetainedFromLayout RetainedSource = "layout"; RetainedFromManifest RetainedSource = "manifest"; RetainedNone RetainedSource = "none" )
type RetainedLayout struct {
	Source         RetainedSource          `json:"source"`
	FromRevision   string                  `json:"fromRevision,omitempty"`
	FromGeneration int64                   `json:"fromGeneration,omitempty"`
	Positions      map[string]NodePosition `json:"positions,omitempty"` // top-left, keyed by node id; only nodes present in this manifest
}
type Presentation struct {
	Revision string         `json:"revision"` // server-issued uuid v7 string
	SavedAt  time.Time      `json:"savedAt"`
	Reason   SaveReason     `json:"reason"`
	Scope    string         `json:"scope"`             // ScopeLabelPrivate | ScopeLabelShared
	OwnerID  uuid.UUID      `json:"ownerId,omitempty"` // private owner; zero for shared
	Name     string         `json:"name"`              // destination folder name at save time
	Retained RetainedLayout `json:"retained"`
}
func ScopeFromPresentation(p *Presentation) Scope   // Shared() for "shared", Private(p.OwnerID) otherwise
func ScopeLabel(s Scope) string                     // ScopeLabelShared | ScopeLabelPrivate
func (w *Workflow) SavedRevision() string           // "" when Presentation is nil
```
arrangement.go:
```go
type ArrangementRequest struct {
	Revision    string
	Scope       Scope
	Name        string
	WorkflowID  uuid.UUID
	Reason      SaveReason
	Manifest    []byte // json.Marshal of the stamped *Workflow exactly as committed
	Retained    RetainedLayout
	RequestedAt time.Time
}
type ArrangementRequester interface {
	RequestArrangement(ctx context.Context, req ArrangementRequest) error
	RelocateArrangements(ctx context.Context, from Scope, fromName string, to Scope, toName string) error
}
type AcknowledgedArrangementReader interface {
	AcknowledgedArrangement(ctx context.Context, revision string) (ArrangementRequest, error)
}
type RevisionJournal interface { /* full method set in §6.2 (binding): Record, MarkCommitted, Remove,
	Quarantine, Entries, List, BeginRelocation, RelocationIntents, Relocate, EndRelocation */ }
type DestinationSettler interface { Settle(ctx context.Context, scope Scope, name string) error } // §6.2
type DisplayedLayoutReader interface {
	DisplayedLayout(ctx context.Context, revision string) (RetainedLayout, bool, error) // positions of the geometry currently displayed for revision
}
var ErrStamperMisconfigured = errors.New("workflow: revision stamper requires a journal, a requester and a settler")
```
revision_stamper.go:
```go
type StamperDeps struct {
	Journal   RevisionJournal       // required
	Requester ArrangementRequester  // required
	Displayed DisplayedLayoutReader // optional
	Settler   DestinationSettler    // required (§6.2); production: *CommitSource
	NewToken  func() string         // default: uuid.Must(uuid.NewV7()).String()
	Now       func() time.Time      // default: time.Now
	Logger    *slog.Logger          // default: slog.Default()
}
type RevisionStamper struct { /* unexported */ }
func NewRevisionStamper(repo Repository, deps StamperDeps) (*RevisionStamper, error)
func (s *RevisionStamper) Stamp(ctx context.Context, scope Scope, name string, wf *Workflow, prior *Workflow, reason SaveReason) (ArrangementRequest, error)
func (s *RevisionStamper) Committed(ctx context.Context, req ArrangementRequest)
func (s *RevisionStamper) Abandon(ctx context.Context, req ArrangementRequest)
func (s *RevisionStamper) EnsureStamped(ctx context.Context, scope Scope, name string, reason SaveReason) (ArrangementRequest, error) // exact committed journal OR exact acknowledged seed input (§6.18); never adopt a folder
```
Retained derivation order in Stamp: (1) prior has Presentation and Displayed returns ok ⇒ Source layout (FromRevision/FromGeneration from the reader); (2) prior.Presentation.Retained has positions ⇒ reuse (Source preserved); (3) prior nodes have Position ⇒ Source manifest; (4) wf nodes have Position ⇒ Source manifest; (5) Source none. Always filtered to node ids present in wf. Displayed errors are logged and fall through.

Service (P2T3): `func (s *Service) SetRevisionStamper(st *RevisionStamper)`; `func (s *Service) SetMutationCoordinator(m MutationCoordinator)`; nil stamper ⇒ legacy behaviour (isolated tests only).
Seeder (P2T4): `func (s *Seeder) SetRevisionStamper(st *workflow.RevisionStamper)`; `func (s *Seeder) SetRegistry(reg workflow.Registry)`; `func (s *Seeder) SetMutationCoordinator(m workflow.MutationCoordinator)`; `seedSampleWorkflows(ctx context.Context, sourceDir, workspacePath string, repo workflow.Repository, published workflow.PublishedStore, stamper *workflow.RevisionStamper, mutations workflow.MutationCoordinator, registry workflow.Registry, owner uuid.UUID, logger *slog.Logger) error`.

### domain/layout Phase 2 (P2T5, P2T8)

store.go:
```go
type State string
const ( StatePending State = "pending"; StateInProgress State = "in_progress"; StatePassed State = "passed"; StateNeedsAttention State = "needs_attention"; StateFailed State = "failed"; StateSuperseded State = "superseded" )
type Destination struct { Scope workflow.Scope; Name string; WorkflowID uuid.UUID }
type Record struct {
	Revision         string
	Destination      Destination
	Reason           workflow.SaveReason
	Manifest         []byte
	Retained         workflow.RetainedLayout
	State            State
	Stage            Stage
	JobGeneration    int64
	AttemptsUsed     int
	AttemptLimit     int
	Verdict          Verdict   // best stored result's verdict; "" when none
	Findings         []Finding // best stored result's unresolved findings
	Unreviewed       bool
	FailureStage     Stage
	FailureReason    string
	ResultGeneration int64 // 0 = no stored geometry
	CacheGeneration  int64
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	RequestedAt      time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
}
type StoredResult struct {
	Revision   string
	Generation int64
	Geometry   Geometry
	Review     *Review
	Preflight  []Finding
	Verdict    Verdict
	Score      Score
	CreatedAt  time.Time
}
type NewRequest struct { Revision string; Destination Destination; Reason workflow.SaveReason; Manifest []byte; Retained workflow.RetainedLayout; AttemptLimit int; RequestedAt time.Time }
type Fence struct { Revision string; Generation int64 }
type Completion struct {
	State         State     // passed | needs_attention | failed | superseded (store keeps superseded if row already superseded)
	AttemptsUsed  int
	FailureStage  Stage
	FailureReason string
	Result        *StoredResult // inserted as (revision, fence.Generation) when non-nil
	ReplaceBest   bool          // when true and Result != nil: result_generation, verdict, findings, unreviewed are replaced
}
var (
	ErrRecordNotFound   = errors.New("layout: revision record not found")
	ErrStaleGeneration  = errors.New("layout: job generation is no longer current")
	ErrNotClaimable     = errors.New("layout: revision is not claimable")
	ErrRetryConflict    = errors.New("layout: revision is not eligible for retry")
	ErrStoreUnavailable = errors.New("layout: status store unavailable")
	ErrNotCurrent       = errors.New("layout: revision is not the destination's current revision")
	ErrDestinationGone  = errors.New("layout: destination workflow no longer exists")
)
type Store interface {
	Request(ctx context.Context, req NewRequest) (Record, bool, error) // idempotent by revision; bool=created
	Get(ctx context.Context, revision string) (Record, error)
	ListActive(ctx context.Context, dest Destination) ([]Record, error) // pending|in_progress with same scope kind/owner/name
	Claimable(ctx context.Context, now time.Time, limit int) ([]string, error)
	Claim(ctx context.Context, revision, owner string, now, leaseUntil time.Time) (Record, error)
	Progress(ctx context.Context, f Fence, stage Stage, attemptsUsed int, now, leaseUntil time.Time) error
	Complete(ctx context.Context, f Fence, c Completion, now time.Time) error
	Checkpoint(ctx context.Context, f Fence, result StoredResult, replaceBest bool, now time.Time) error
	Supersede(ctx context.Context, revision string, now time.Time) error
	Retry(ctx context.Context, revision string, attemptLimit int, now time.Time) (Record, error)
	Relocate(ctx context.Context, from, to Destination, now time.Time) (int64, error)
	Result(ctx context.Context, revision string, generation int64) (StoredResult, error)
	PendingCache(ctx context.Context, limit int) ([]Record, error)
	MarkCached(ctx context.Context, f Fence, resultGeneration int64, dest Destination) error
	InvalidateResult(ctx context.Context, f Fence, resultGeneration int64, reason string, now time.Time) error
}
```
Semantics: Claim succeeds for `pending`, or `in_progress` with `lease_expires_at < now`; it sets
in_progress, stage arranging, lease, started_at. Reclaim of an expired in_progress lease
increments job_generation atomically; an initial pending claim keeps it. Return the exact
row/generation modified by that claim before a competing retry/reclaim can change it. Progress/Complete fenced by
`job_generation = f.Generation` and `state IN ('in_progress')` (Progress) /
`state IN ('in_progress','superseded')` (Complete) else ErrStaleGeneration. Retry allowed from
needs_attention, failed, or superseded with verdict != passed; increments job_generation, sets
pending/stage queued, attempts_used 0, attempt_limit, clears failure+lease; else ErrRetryConflict.
Supersede affects only pending|in_progress rows (no-op otherwise).

manifests.go:
```go
type ManifestSource interface {
	CurrentRevision(ctx context.Context, scope workflow.Scope, name string) (string, bool, error) // ("", false, nil) when gone or pre-feature
}
type RepositoryManifests struct { /* repo workflow.Repository */ }
func NewRepositoryManifests(repo workflow.Repository) *RepositoryManifests
```
requester.go:
```go
type Requester struct { /* unexported */ }
func NewRequester(store Store, attemptLimit int, logger *slog.Logger) *Requester // attemptLimit < 1 ⇒ 1
func (r *Requester) SetNotifier(fn func(revision string))
func (r *Requester) AttemptLimit() int
func (r *Requester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error // implements workflow.ArrangementRequester
func (r *Requester) RelocateArrangements(ctx context.Context, from workflow.Scope, fromName string, to workflow.Scope, toName string) error
func (r *Requester) Retry(ctx context.Context, revision string) (Record, error)
func (r *Requester) AcknowledgedArrangement(ctx context.Context, revision string) (workflow.ArrangementRequest, error) // Store.Get immutable input, no enqueue
// compile-time: var _ workflow.ArrangementRequester = (*Requester)(nil)
```
reader.go:
```go
type DisplayStatus string
const ( DisplayPending DisplayStatus = "pending"; DisplayArranging DisplayStatus = "arranging"; DisplayRendering DisplayStatus = "rendering"; DisplayReviewing DisplayStatus = "reviewing"; DisplayComplete DisplayStatus = "complete"; DisplayNeedsAttention DisplayStatus = "needs_attention"; DisplayFailed DisplayStatus = "failed"; DisplayLegacy DisplayStatus = "legacy"; DisplayUnavailable DisplayStatus = "unavailable" )
type Provenance string
const ( ProvenanceArranged Provenance = "arranged"; ProvenanceRetainedGeometry Provenance = "retained_geometry"; ProvenanceRetainedPositions Provenance = "retained_positions"; ProvenanceLegacy Provenance = "legacy" )
type View struct {
	SavedRevision     string                   `json:"savedRevision"`
	JobGeneration     int64                    `json:"jobGeneration"`
	ResultGeneration  int64                    `json:"resultGeneration"`
	State             State                    `json:"state,omitempty"`
	DisplayStatus     DisplayStatus            `json:"displayStatus"`
	Stage             Stage                    `json:"stage,omitempty"`
	AttemptsUsed      int                      `json:"attemptsUsed"`
	AttemptLimit      int                      `json:"attemptLimit"`
	Findings          []Finding                `json:"findings"`
	FailureStage      Stage                    `json:"failureStage,omitempty"`
	FailureReason     string                   `json:"failureReason,omitempty"`
	Evidence          *Evidence                `json:"evidence,omitempty"`
	Contract          *RendererContract        `json:"contract,omitempty"`
	Geometry          *Geometry                `json:"geometry,omitempty"`
	Retained          *workflow.RetainedLayout `json:"retained,omitempty"`
	Provenance        Provenance               `json:"provenance"`
	Unreviewed        bool                     `json:"unreviewed"`
	RetryEligible     bool                     `json:"retryEligible"`
	Available         bool                     `json:"available"`
	AvailabilityError string                   `json:"availabilityError,omitempty"`
}
type Reader struct { /* store Store; journal workflow.RevisionJournal */ }
func NewReader(store Store, journal workflow.RevisionJournal, cache CacheReader) *Reader
func (r *Reader) View(ctx context.Context, wf *workflow.Workflow) View   // never errors; problems are in-band
func IsActive(s DisplayStatus) bool // pending|arranging|rendering|reviewing
```
DisplayStatus mapping: pending→pending; in_progress→arranging|rendering|reviewing by stage (queued/arranging→arranging, publishing→reviewing); passed→complete; needs_attention→needs_attention; failed→failed; superseded→complete if Verdict==passed else needs_attention. Missing row ⇒ pending (stage "queued" if journal has revision, otherwise stage "queued" with AvailabilityError "awaiting recovery"), RetryEligible true. Store error ⇒ DisplayUnavailable, Available=false, Retained from manifest, RetryEligible true. Stored geometry that fails `Validate(BuildGraph(wf))` or has a different contract version ⇒ geometry dropped, Provenance retained_positions, DisplayStatus needs_attention, FailureReason "stored geometry is incompatible with this revision: <err>", RetryEligible true.

store.go (P2T5 cache values/ports), worker.go (P2T8 worker):
```go
const CacheFormatV1 = "arbitron.layout-cache/v1"
type CacheEntry struct { Format string `json:"format"`; Revision string `json:"revision"`; Generation int64 `json:"generation"`; JobGeneration int64 `json:"jobGeneration"`; Contract RendererContract `json:"contract"`; Verdict Verdict `json:"verdict"`; Geometry Geometry `json:"geometry"` }
type CachePublisher interface {
	PublishCache(ctx context.Context, dest Destination, entry CacheEntry, fence func(context.Context) error) error // ErrNotCurrent / ErrDestinationGone when CAS fails
}
type WorkerConfig struct {
	Store Store; Coordinator *Coordinator; Journal workflow.RevisionJournal; Committed workflow.CommittedRevisionSource; Manifests ManifestSource; Cache CachePublisher; Requester *Requester
	Owner string; Concurrency int; PollInterval time.Duration; LeaseDuration time.Duration; JobTimeout time.Duration; StageTimeout time.Duration
	Clock func() time.Time; Logger *slog.Logger
}
// defaults: Concurrency 2, PollInterval 15s, LeaseDuration 5m, JobTimeout 15m, StageTimeout 2m, Clock time.Now, Logger slog.Default(), Owner "<hostname>:<pid>:<random>"
var ErrWorkerMisconfigured = errors.New("layout: worker requires store, coordinator, journal, committed source, manifests, cache and requester")
func NewWorker(cfg WorkerConfig) (*Worker, error)
func (w *Worker) Notify(revision string)            // non-blocking
func (w *Worker) Start(ctx context.Context)
func (w *Worker) Stop()                             // idempotent; waits for in-flight jobs to observe cancellation
func (w *Worker) Recover(ctx context.Context) error // CommitSource.CommittedRequests → idempotent rows; see §6.2
func (w *Worker) Drain(ctx context.Context) error   // claim+process everything claimable now
func (w *Worker) ProcessRevision(ctx context.Context, revision string) error
func (w *Worker) ReconcileCache(ctx context.Context) error
```

### Filesystem (P2T7) — package filesystem

```go
const LayoutCacheFile = "layout.json"
func NewRevisionJournal(basePath string) *RevisionJournal     // dir basePath/.arbitron-layout-journal; implements workflow.RevisionJournal
type LayoutCache struct{ /* repo *WorkflowRepo */ }
func NewLayoutCache(repo *WorkflowRepo) *LayoutCache            // implements layout.CachePublisher
func (r *WorkflowRepo) SetReadSettler(s workflow.DestinationSettler) // P2T7 producer, P2T9 injection before external reads
func (c *LayoutCache) ReadCache(ctx context.Context, scope workflow.Scope, name string) (layout.CacheEntry, bool, error)
```
WorkflowRepo gains an internal cross-process lock (`basePath/.arbitron-locks/<sha256>.lock`, gofrs/flock + in-process mutex) taken by Create, Update, Delete, SaveNodePositions, CopyTree (destination) and the cache publisher. `layout.json` is reserved in WriteFile/DeleteFile (ErrInvalidFilePath) and excluded from ListFiles.

### SQLite (P2T6)

`adapter/sqlite/layout_repo.go`: `type LayoutRepo struct{ db *sql.DB }`, `func NewLayoutRepo(db *sql.DB) *LayoutRepo`, `var _ layout.Store = (*LayoutRepo)(nil)`. Raw database/sql (no sqlc). Driver errors are wrapped with `layout.ErrStoreUnavailable` via `fmt.Errorf("%w: ...: %v", layout.ErrStoreUnavailable, err)` except not-found/fence sentinels. Also implements `workflow.DisplayedLayoutReader` via `func (r *LayoutRepo) DisplayedLayout(ctx, revision string) (workflow.RetainedLayout, bool, error)` (best result node top-left positions; FromRevision = revision, FromGeneration = result_generation).
Tables: `layout_revisions`, `layout_results` (see P2T6).

### Composition root (P2T9) — cmd/arbitron/layout_wiring.go

```go
type layoutStack struct {
	store     *adaptsqlite.LayoutRepo
	journal   *filesystem.RevisionJournal
	commits   *workflow.CommitSource // §6.2 producer; WorkerConfig.Committed + StamperDeps.Settler
	cache     *filesystem.LayoutCache
	requester *layout.Requester
	stamper   *workflow.RevisionStamper
	reader    *layout.Reader
	mutations workflow.MutationCoordinator
	worker    *layout.Worker
	renderer  layout.Renderer
	reviewer  layout.Reviewer
	closeFns  []func()
	cancel    context.CancelFunc
}
func newLayoutStack(cfg *config.Config, db *sql.DB, repo *filesystem.WorkflowRepo, publications workflow.RevisionPublicationStore, registry workflow.Registry, logger *slog.Logger) (*layoutStack, error)
func newLayoutRenderReviewer(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) // P2T9: returns rendererUnavailable; P3T7 replaces with renderreview browser adapter
func (ls *layoutStack) start(logger *slog.Logger)  // worker.Start (Start owns initial and repeated recovery)
func (ls *layoutStack) stop()                       // worker.Stop, closeFns (before DB close)
```
domainStack gains field `layout *layoutStack`; initDomain builds it after `publishedRepo` and `identityRepo` (before seeding), passes `ls.stamper` to `seedSampleWorkflows`, calls `workflowSvc.SetRevisionStamper(ls.stamper)` and `workflowSvc.SetMutationCoordinator(ls.mutations)`, and calls `ls.start(logger)` next to `scheduleEval.Start`. `Close` calls `ds.layout.stop()` before `ds.db.Close()`. initDomain logs each `cfg.LoadWarnings` entry with `logger.Warn`.
`workflowRepo` local in initDomain becomes `*filesystem.WorkflowRepo` (concrete) for newLayoutStack; domainStack.workflowRepo stays `workflow.Repository`.

---------------------------------------------------------------------------------------
## 3. Phase 3

### Go auth seams (P3T8)

domain/workflow/layout_access.go:
```go
func (s *Service) LoadForLayout(ctx context.Context, name string) (*Workflow, Scope, error)       // Load visibility rules (private then shared via auditingFilter); no id allocation side effects
func (s *Service) AuthorizeLayoutRetry(ctx context.Context, name string) (*Workflow, Scope, error) // Update's policy: own private; admin edit-in-place of shared-only; non-admin shared ⇒ ErrSharedReadOnly; missing ⇒ ErrNotFound
func (s *Service) LayoutRetryTargets(ctx context.Context, name string) ([]MutationTarget, error) // private(caller) AND shared; no graph reads or writes
```
domain/engine/layout_access.go:
```go
func (e *Engine) InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)      // GetInstance (canView) then decode WorkflowSnapshot
func (e *Engine) AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) // authorizeIntervention then decode
func DecodeWorkflowSnapshot(snapshot map[string]any) (*workflow.Workflow, error)
```
domain/layout/query.go:
```go
type WorkflowAccess interface {
	LoadForLayout(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error)
	AuthorizeLayoutRetry(ctx context.Context, name string) (*workflow.Workflow, workflow.Scope, error)
	LayoutRetryTargets(ctx context.Context, name string) ([]workflow.MutationTarget, error)
}
type InstanceAccess interface {
	InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
	AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error)
}
type RetryAck struct { SavedRevision string `json:"savedRevision"`; JobGeneration int64 `json:"jobGeneration"`; State State `json:"state"`; DisplayStatus DisplayStatus `json:"displayStatus"` }
var ( ErrRevisionConflict = errors.New("layout: expected revision is not current"); ErrLegacyRevision = errors.New("layout: revision has no arrangement request") )
type QueryService struct { /* reader, requester, workflows, instances */ }
func NewQueryService(reader *Reader, requester *Requester, workflows WorkflowAccess, instances InstanceAccess, mutations workflow.MutationCoordinator) *QueryService
func (q *QueryService) WorkflowLayout(ctx context.Context, name string) (View, error)
func (q *QueryService) InstanceLayout(ctx context.Context, id uuid.UUID) (View, error)
func (q *QueryService) RetryWorkflow(ctx context.Context, name, expectedRevision string) (RetryAck, error)
func (q *QueryService) RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (RetryAck, error)
```
`func (ls *layoutStack) attachQuery(workflows layout.WorkflowAccess, instances layout.InstanceAccess)` (P3T8) sets `ls.query`; called in initDomain after `eng` exists.

### HTTP (P3T9) — package http (adapter/http)

```go
type LayoutQueryService interface {
	WorkflowLayout(ctx context.Context, name string) (layout.View, error)
	InstanceLayout(ctx context.Context, id uuid.UUID) (layout.View, error)
	RetryWorkflow(ctx context.Context, name, expectedRevision string) (layout.RetryAck, error)
	RetryInstance(ctx context.Context, id uuid.UUID, expectedRevision string) (layout.RetryAck, error)
}
type LayoutHandler struct{ layouts LayoutQueryService }
func NewLayoutHandler(layouts LayoutQueryService) *LayoutHandler
func (h *LayoutHandler) GetWorkflowLayout(w, r)   // GET  /api/v1/workflows/{name}/layout
func (h *LayoutHandler) RetryWorkflowLayout(w, r) // POST /api/v1/workflows/{name}/layout/retry   body {"expectedRevision": "..."}  → 202 RetryAck
func (h *LayoutHandler) GetInstanceLayout(w, r)   // GET  /api/v1/instances/{id}/layout
func (h *LayoutHandler) RetryInstanceLayout(w, r) // POST /api/v1/instances/{id}/layout/retry → 202
// RouterDeps gains: Layouts LayoutQueryService
// instanceResponse gains: Layout *layout.View `json:"layout,omitempty"` (InstanceHandler.SetLayouts)
```
Status mapping: workflow.ErrNotFound / instance.ErrNotFound → 404 NOT_FOUND; workflow.ErrSharedReadOnly, layout.ErrRevisionConflict, layout.ErrRetryConflict → 409 CONFLICT; identity.ErrForbidden/instance.ErrForbidden/workflow.ErrUnauthorized → 403 FORBIDDEN; layout.ErrLegacyRevision → 409 CONFLICT; layout.ErrStoreUnavailable / workflow.ErrMutationPending → 503 UNAVAILABLE; bad body → 400.

### MCP (P3T10) — package mcp

`Deps.Layouts LayoutQueryService` (same 4-method interface, declared in adapter/mcp/deps.go). `workflow_get` returns the manifest object plus key `"arrangement"`:
`{"savedRevision","displayStatus","state","stage","attemptsUsed","attemptLimit","findings","failureStage","failureReason","retryEligible","available","availabilityError","provenance"}` (geometry omitted).
New tools: `workflow_layout_retry` (args `name`, `expectedRevision` required) and `instance_layout_retry` (args `id`, `expectedRevision` required); both return the RetryAck JSON.

### Dashboard TS (P3T1+)

`dashboard/src/graph/arranged/types.ts` mirrors Go JSON exactly:
```ts
export interface Point { x: number; y: number }
export interface Rect { x: number; y: number; w: number; h: number }
export type EdgeKind = 'forward' | 'loop_return' | 'self_loop'
export type Side = 'top' | 'bottom' | 'left' | 'right'
export interface Attachment { nodeId: string; side: Side; point: Point }
export interface LabelBox { text: string; box: Rect; lines: number }
export interface NodeBox { id: string; box: Rect; text: string; typeCaption: string; textLines: number; rank: number }
export interface EdgeRoute { ordinal: number; source: string; target: string; output?: string; label?: string; kind: EdgeKind; side?: Side; lane?: number; points: Point[]; sourceAttach: Attachment; targetAttach: Attachment; text?: LabelBox }
export interface Crossing { point: Point; over: number; under: number }
export interface RendererContract { version: string; fontFamily: string; fontPackage: string; nodeLabelFontPx: number; nodeLabelWeight: number; nodeTypeFontPx: number; nodeTypeWeight: number; edgeLabelFontPx: number; edgeLabelWeight: number; lineHeight: number; nodePaddingX: number; nodePaddingY: number; nodeBorderPx: number; nodeTypeBarPx: number; nodeTextGapPx: number; nodeMinWidth: number; nodeTextMaxWidth: number; edgeLabelPaddingX: number; edgeLabelPaddingY: number; edgeLabelMaxWidth: number; strokePx: number; arrowPx: number; crossingTreatment: string; crossingGapPx: number; forwardColor: string; loopReturnColor: string; loopReturnDash: string; practicalZoom: number; minReadableTextPx: number; minContrastRatio: number; minClearancePx: number }
export interface Geometry { format: string; contract: RendererContract; options: Record<string, number>; nodes: NodeBox[]; edges: EdgeRoute[]; crossings: Crossing[]; canvas: Rect }
export type DisplayStatus = 'pending' | 'arranging' | 'rendering' | 'reviewing' | 'complete' | 'needs_attention' | 'failed' | 'legacy' | 'unavailable'
export type Provenance = 'arranged' | 'retained_geometry' | 'retained_positions' | 'legacy'
export interface LayoutFinding { criterion: string; severity: 'blocking' | 'advisory'; source: 'preflight' | 'review'; nodeIds?: string[]; edgeOrdinals?: number[]; evidence: string }
export interface RetainedLayout { source: 'layout' | 'manifest' | 'none'; fromRevision?: string; fromGeneration?: number; positions?: Record<string, { x: number; y: number }> }
export interface LayoutEvidence { geometryDigest: string; rendererVersion: string; contract: RendererContract; fontsReady: boolean; zoom: number; tiles: number; artifacts: Array<{ kind: string; path: string; sha256: string; region: Rect; scale: number }>; reviewedAt: string }
export interface LayoutView { savedRevision: string; jobGeneration: number; resultGeneration: number; state?: string; displayStatus: DisplayStatus; stage?: string; attemptsUsed: number; attemptLimit: number; findings: LayoutFinding[] | null; failureStage?: string; failureReason?: string; evidence?: LayoutEvidence; contract?: RendererContract; geometry?: Geometry; retained?: RetainedLayout; provenance: Provenance; unreviewed: boolean; retryEligible: boolean; available: boolean; availabilityError?: string }
export interface RetryAck { savedRevision: string; jobGeneration: number; state: string; displayStatus: DisplayStatus }
export interface Presentation { revision: string; savedAt: string; reason: string; scope: string; ownerId?: string; name: string; retained: RetainedLayout }
export const ACTIVE_STATUSES: ReadonlySet<DisplayStatus>  // pending, arranging, rendering, reviewing
```
`dashboard/src/graph/types.ts` `WorkflowDefinition` gains `presentation?: Presentation` (import type from './arranged/types').
`dashboard/src/api/types.ts` `Instance` gains `layout?: LayoutView`.
API client (P3T1): `api.workflows.getLayout(name): Promise<LayoutView>`, `api.workflows.retryLayout(name, expectedRevision): Promise<RetryAck>`, `api.instances.getLayout(id): Promise<LayoutView>`, `api.instances.retryLayout(id, expectedRevision): Promise<RetryAck>`. `saveLayout` removed in P3T12.
`validate.ts`: `export function validateGeometry(def: WorkflowDefinition, geo: Geometry, contract: RendererContract): { ok: boolean; issues: string[] }` (full exact renderer contract, origin-zero canvas, node/edge text and occurrence coverage/order, attachments/orthogonality/crossings and finite positive geometry; §6.6).

DOM contract used by the render report (P3T2 produces, P3T4 consumes):
- node root: `data-arranged-node="<id>"`; primary text element `data-node-text="label"`; type caption element `data-node-text="type"`; the node root has inline `width/height` = box.w/box.h and CSS `box-sizing: border-box`.
- edge: `<path data-edge-path="<ordinal>" data-edge-kind="<kind>" d="...">` with `marker-end` referencing `<marker id="<graphInstanceId>-arranged-arrow-<ordinal>">`; gaps are rendered as separate `M` subpaths in the SAME path.
- edge label: `<div data-edge-label="<ordinal>">` absolutely positioned at label box (flow coordinates).
- graph root: `<div data-arranged-canvas="true">` wrapping the ReactFlow instance.
Components (P3T2/P3T3):
```ts
// ArrangedNode.tsx
export interface ArrangedNodeData { nodeBox: NodeBox; nodeType: string; executionState: NodeExecutionState; contract: RendererContract; [key: string]: unknown }
export default memo(ArrangedNode)   // xyflow node type key 'arrangedNode'
// RoutedEdge.tsx
export interface RoutedEdgeData { route: EdgeRoute; gaps: Point[]; contract: RendererContract; graphInstanceId: string; active: boolean; [key: string]: unknown }
export default memo(RoutedEdge)     // xyflow edge type key 'routedEdge'
// routedPath.ts
export function buildRoutedPath(points: Point[], gaps: Point[], gapPx: number): string
// buildArrangedFlow.ts
export function buildArrangedFlow(geo: Geometry, nodeTypesById: Record<string, string>, executionStates: Record<string, NodeExecutionState>, graphInstanceId: string, activeSources?: Set<string>): { nodes: Node<ArrangedNodeData>[]; edges: Edge<RoutedEdgeData>[] }
export const arrangedNodeTypes: NodeTypes  // { arrangedNode: ArrangedNode }
export const arrangedEdgeTypes: EdgeTypes  // { routedEdge: RoutedEdge }
// fallbackLayout.ts
export function fallbackPositions(nodes: WorkflowNode[], retained: Record<string, {x:number;y:number}> | undefined): Record<string, {x:number;y:number}> | null // null ⇒ caller uses dagre
// ArrangedGraph.tsx
export interface ArrangedGraphProps {
  workflowDef: WorkflowDefinition
  view: LayoutView | null                       // geometry used when view.geometry validates; else fallback (retained positions → dagre)
  executionStates?: Record<string, NodeExecutionState>
  selectedNodeId: string | null
  selectedNodeIds?: ReadonlySet<string>         // render-surface state probe: marks many nodes selected
  onNodeClick: (nodeId: string) => void
  anchorNodeId: string | null                   // initial centre at DEFAULT_ZOOM (0.9); ignored when fixedViewport is set
  onMoveStart?: (event: unknown) => void
  onDisplayIssues?: (issues: readonly string[]) => void // P3T12/P3T13 merge real font/display state into status panel
  onAnchorPosition?: (center: { x: number; y: number } | null) => void
  fixedViewport?: { x: number; y: number; zoom: number } // render surface: no pan/zoom, no Controls/Background, no centring
  children?: React.ReactNode                    // rendered inside <ReactFlow> (e.g. recenter button)
}
export default function ArrangedGraph(props: ArrangedGraphProps): JSX.Element
export function nodeCenter(view: LayoutView | null, rfNodes: Node[], nodeId: string): { x: number; y: number } | null
```
useGraphLayout (P3T3): input gains `positions?: Record<string,{x:number;y:number}>`; output drops `needsAutoSave`.
Hook (P3T11):
```ts
export interface UseLayoutStatus { view: LayoutView | null; error: string | null; loading: boolean; refresh: () => void; retry: () => Promise<void>; retrying: boolean; retryError: string | null }
export function useLayoutStatus(fetcher: () => Promise<LayoutView>, retrier: (expectedRevision: string) => Promise<RetryAck>, key: string): UseLayoutStatus
export const POLL_INTERVAL_MS = 2000; export const MAX_BACKOFF_MS = 30000
// LayoutStatusPanel.tsx
export interface LayoutStatusPanelProps { view: LayoutView | null; error: string | null; onRetry: () => void; retrying: boolean; retryError: string | null; canRetry: boolean; displayIssues?: readonly string[] }
```
Render surface (P3T4): `dashboard/render.html` → `src/render/main.tsx`. Page reads `?job=<token>`, fetches `/__render/jobs/<token>.json`:
`{ "mode": "render", "revision": string, "attempt": number, "geometry": Geometry, "workflowNodeTypes": Record<string,string>, "states": string[] }` or
`{ "mode": "measure", "request": MeasureRequest }`. It sets `window.__ARBITRON_RENDER_STATE__` = 'loading' | 'ready' | 'error',
`window.__ARBITRON_RENDER_ERROR__` (string), and on ready either `window.__ARBITRON_RENDER_REPORT__` (RenderReport JSON, contract §1) or
`window.__ARBITRON_MEASURE_RESULT__` (Measurements JSON). Render mode lays out the flow at zoom = contract.practicalZoom with
viewport translate (0,0) and a container sized canvas.w*zoom × canvas.h*zoom, so page CSS px = canvas px × zoom.

### Go render-review adapter (P3T5/P3T6) — package renderreview (adapter/renderreview)

```go
// adapter/dashboard/embed.go
func Assets() fs.FS
// assets.go
type assetServer struct{ /* unexported */ }   // loopback http server: static fs + /__render/jobs/<token>.json
// browser.go
type BrowserConfig struct { Assets fs.FS; BrowserPath string; ArtifactDir string; Logger *slog.Logger; RenderTimeout time.Duration; TileWidth, TileHeight int; Overlap float64 }
type BrowserRenderer struct{ /* unexported */ }
func NewBrowserRenderer(cfg BrowserConfig) (*BrowserRenderer, error)
func (b *BrowserRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error)
func (b *BrowserRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error)
func (b *BrowserRenderer) Close()
var ErrBrowserUnavailable = errors.New("renderreview: no Chrome/Chromium browser available")
// tiles.go
func PlanTiles(canvas layout.Rect, zoom float64, tileW, tileH int, overlap float64) []layout.Rect // canvas-space regions covering the whole canvas
// reviewer.go
type ReadabilityReviewer struct{ /* unexported */ }
func NewReadabilityReviewer(logger *slog.Logger) *ReadabilityReviewer
func (r *ReadabilityReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error)
```
Tests needing a real browser call `requireBrowser(t)` (defined in adapter/renderreview/browser_test.go and duplicated as `requireBrowserE2E(t)` in cmd/arbitron/layout_render_e2e_test.go): skip when no browser is found unless env `ARBITRON_REQUIRE_BROWSER=1`, in which case `t.Fatal`. Makefile target `test-render` runs those packages with `ARBITRON_REQUIRE_BROWSER=1`.

---------------------------------------------------------------------------------------
## 5. Addenda (binding; override anything above that conflicts)

1. `LayoutConfig` also has `BrowserNoSandbox bool` (`layout.browsernosandbox`, default false, env `ARBITRON_LAYOUT_BROWSERNOSANDBOX`) — added by P2T1, consumed by P3T7 (`BrowserConfig.NoSandbox`). `BrowserConfig` therefore has field `NoSandbox bool`.
2. Test helper package `domain/layout/layouttest` (non-test Go files, package `layouttest`):
   - P2T5 owns `memstore.go` (`type MemStore struct`, `func NewMemStore() *MemStore`, implements `layout.Store` with the exact semantics of §2; `func (m *MemStore) SetFailure(err error)` makes every subsequent call return `fmt.Errorf("%w: %v", layout.ErrStoreUnavailable, err)` until `SetFailure(nil)`), `store_contract.go` (`func RunStoreContract(t *testing.T, newStore func(t *testing.T) layout.Store)` — the behavioural suite run against MemStore in P2T5 and against `adaptsqlite.LayoutRepo` in P2T6), and `measure.go` (`func Measurements(g layout.Graph, c layout.RendererContract) layout.Measurements` — deterministic 7px/char metrics honouring each item's text, style and max width).
   - P2T8 owns `fakes.go`: `type FakeRenderer struct` (Measure via `Measurements`; Render returns a Rendering whose GeometryDigest is computed from the request geometry; fields `MeasureErr, RenderErr error`, `RenderDelay time.Duration`, `Calls int` guarded by mutex), `type FakeReviewer struct` (`Script []ReviewScript` per attempt — `ReviewScript{Findings []layout.Finding; Err error}`; Review copies the rendering's digest/contract into Evidence so the coordinator's pass rule is exercised). P2T5 owns `FakeCache` with the exact accessor API below; P2T8 must not redefine it.
   - Phase 2/3 tests of domain/layout use external package `layout_test` (so they can import layouttest without an import cycle). Phase 1 tests stay internal (`package layout`).
3. `CacheEntry`, `CachePublisher`, `CacheReader` live in `domain/layout/store.go` (P2T5), not worker.go:
   ```go
   type CacheReader interface { ReadCache(ctx context.Context, scope workflow.Scope, name string) (CacheEntry, bool, error) }
   func NewReader(store Store, journal workflow.RevisionJournal, cache CacheReader) *Reader // cache may be nil
   ```
   Reader uses the cache only when the store errors: if `entry.Revision == wf.SavedRevision()` and the geometry validates, it returns that geometry with Provenance arranged, `Available=false`, `AvailabilityError="status store unavailable; showing the last published layout for this revision"`. The destination for the cache lookup comes from `workflow.ScopeFromPresentation(wf.Presentation)` + `wf.Presentation.Name`.
4. `requester.go` defines `var ErrInvalidArrangementRequest = errors.New("layout: arrangement request needs a revision and a manifest")`.
5. RoutedEdge arrow marker id is `<graphInstanceId>-arranged-arrow-<ordinal>` (unique per mounted graph and edge), defined in the edge's own `<defs>`; `path[marker-end]` = `url(#<graphInstanceId>-arranged-arrow-<ordinal>)`.
6. CSS module class names (P3T2): `ArrangedNode.module.css` → `node`, `typeBar`, `content`, `typeCaption`, `label`, `stateDot`, plus state classes `statePending`, `stateRunning`, `stateCompleted`, `stateFailed`, `stateWaiting`, `stateSkipped`, `selected`; `RoutedEdge.module.css` → `edgeLabel`, `activeOverlay`. Exports for the measure surface: `export const nodeTextClassNames = { label: styles.label, typeCaption: styles.typeCaption }` (ArrangedNode.tsx) and `export const edgeLabelClassName = styles.edgeLabel` (RoutedEdge.tsx). `dashboard/src/graph/arranged/fonts.ts` (P3T2) imports `@fontsource/inter/400.css`, `500.css`, `600.css`, `700.css`, and matching Noto fallback weights from §6.13; imported by ArrangedGraph.tsx and src/render/main.tsx.
7. Node visuals (P3T2): border width constant (`contract.nodeBorderPx`) in every state; state shown via border colour / box-shadow only — never opacity (opacity breaks contrast); the state dot is absolutely positioned (does not affect text wrapping); type caption text colour `#b4b4cc` (accent colour only on the 4px type bar); label `#e0e0e0`; node background `var(--bg-surface)` (#1e1e38); edge label background `#1a1a2e`, text `#e0e0e0` (forward) / `#9aa6ff` (loop return).
8. P2T9's composition-root e2e test must hold whether or not a browser is installed: assert the sink (row exists with captured manifest/attempt limit; terminal state reached; retained fallback; if state is failed then FailureStage == rendering). P3T7 adds the browser-required acceptance tests and removes `rendererUnavailable`.
9. HTTP router construction is extracted in P3T9 into `func (ds *domainStack) routerDeps(mcpHandler http.Handler, dashHandler http.Handler, notifHandler *adapthttp.NotificationHandler, webhookHandler *adapthttp.WebhookHandler) adapthttp.RouterDeps` in cmd/arbitron/main.go (runServe calls it) so an e2e test can build the real router from `initDomain`. (P3T9 may adjust the exact parameter list to what runServe actually builds, and must report it.)
10. P3T6 adds `var ErrEvidenceUnavailable = errors.New("renderreview: rendered evidence is unavailable")` in `adapter/renderreview/reviewer.go`. `ReadabilityReviewer.Review` returns an error wrapping it when screenshot files are missing, unreadable, hash-mismatched or not PNGs, or when there is no overview or no tile. A rendering that is not provably of the requested candidate, or is incomplete, returns `Review{Rendered: false}` with blocking `coverage`/`render_mismatch` findings and no error. Evidence fields describe the examined rendering: `GeometryDigest = Rendering.GeometryDigest`, `FontsReady = Report.FontsReady && Report.FontFamilyResolved`. P3T6 also adds the files `reviewer_checks.go` and `reviewer_capture.go` (plus tests) next to `reviewer.go`.
11. P3T7 (cmd/arbitron/layout_wiring.go) adds `var layoutRenderAssets = dashboard.Assets` and `func layoutArtifactDir(cfg *config.Config) string` (`cfg.Layout.ArtifactDir`, else `filepath.Join(filepath.Dir(cfg.Database.Path), "layout-artifacts")`). It deletes `rendererUnavailable` and `errRendererNotInstalled`. A missing browser does not fail boot: jobs fail at stage `rendering` with `renderreview.ErrBrowserUnavailable`, and boot logs a WARN. A build without `render.html` fails boot. The test option `layoutE2EOptions.realSample` copies `samples/<name>` and seeds it. The Docker image sets `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true` and `ARBITRON_LAYOUT_ARTIFACTDIR=/data/layout-artifacts`.

## 6. Alignment decisions and execution gates (binding)

These corrections apply to every task and its code/test examples. They close implementation
and evidence gaps found during hierarchy review. The commit-proof dependency is resolved in
§6.2 (two-phase journal + receipts, P2T7 Task 7.5). Never assume a prepared journal entry
proves a committed save.

### 6.1 Ownership, order and strict TDD

Execute P1T1–P1T9, P2T1–P2T9, P3T1–P3T20 in order, with these intra-task rules:

- P1T2 owns the complete `Options` **type** in `options.go` (fields/tags in §1).
  P1T4 owns defaults, validation and sizing. There is no temporary empty `Options` type.
- P1T3 creates `testhelpers_test.go:loadSample`/`edgeGraph`; P1T4 appends the measurer.
- P2T5 owns `CacheEntry`, `CachePublisher`, `CacheReader` in `store.go`, and all Store
  semantics/helpers. P2T8 consumes those exact definitions and implements lifecycle in
  Tasks 8.5/8.6 as well as per-job processing. Expired-lease claim increments generation;
  an initial pending claim does not. `StoredResult.IsUnreviewed` uses §6.5 evidence validation.
- P2T9 does not declare `layoutStack.query` until P3T8 produces `QueryService`.
  `layoutStack.start` calls `worker.Start` once; Start owns recovery.
- `rendererUnavailable` is a temporary, explicitly failing build seam, removed by P3T7.
  No release or end-to-end acceptance is complete before P3T7 and P3T14. A missing browser
  at runtime produces a visible retryable failure; it never substitutes a passing fake.
- Every task runs its tests RED before changing behavior, then GREEN, then refactors.
  A compile failure establishes a missing symbol only; behavioral assertions must also
  be observed failing against the minimal compiling implementation. Existing-behavior
  characterization tests may start green but require the documented mutation probe.
  Each task's happy/success, failure, error and edge cases below are required, not optional.
  Pure value/model/deletion tasks test rejected values and unchanged legacy behavior rather
  than inventing I/O errors. Only documentation is changed by this alignment review.

### 6.2 Durable commit provenance — SAVE-COMMIT-PROOF (resolved 2026-10-17)

The original `Stamp → journal.Record → repository write → Committed → journal.Remove`
protocol records only **prepared** input. A crash before the write leaves an entry for a
save that never committed, and blind `Recover` would turn it into a real job. Copy/publish/
seed also exposed the copied source presentation before restamping. Per-method file locks
do not make these multi-step operations atomic. The recorded resolution (2026-10-17)
is a **two-phase journal with commit receipts, executed inside the destination mutation
lock**, with the **registry publication record as the commit point for registry-backed
operations** (roll back otherwise).

P2T2 adds the consumer-owned port in `domain/workflow/arrangement.go`:

```go
type CommittedRevisionSource interface {
    CommittedRequests(ctx context.Context) ([]ArrangementRequest, error)
}
```

Its contract: immutable, destination-specific inputs for **proven committed authoring
operations only**, including historical revisions overwritten/deleted before enqueue;
never prepared/rolled-back trees. The returned destination reflects committed relocation,
while Manifest remains the exact saved snapshot. Results are idempotent by revision.
Errors preserve evidence for recovery. An outage cannot turn a prepared entry into a job.

**Producer:** `domain/workflow/commit_source.go:CommitSource` (built by **P2T7 Task 7.5**)
over the extended `RevisionJournal` port implemented by
`adapter/filesystem/revision_journal.go:RevisionJournal` (receipts and relocation intents
also added by P2T7 Task 7.5). P2T9 constructs it and injects it as
`WorkerConfig.Committed` and `StamperDeps.Settler`. It is never nil or a no-op in production.

**Port additions (declared by P2T2 in arrangement.go; implemented by P2T7 Task 7.5):**

```go
type JournalState string
const ( JournalPrepared JournalState = "prepared"; JournalCommitted JournalState = "committed" )
type JournalEntry struct { Request ArrangementRequest; State JournalState }
type RelocationIntent struct { ID string; WorkflowID uuid.UUID; From Scope; FromName string; To Scope; ToName string; CreatedAt time.Time }
type RevisionJournal interface {
    Record(ctx context.Context, req ArrangementRequest) error          // prepared; durable (file + dir fsync); overwrites same revision
    MarkCommitted(ctx context.Context, revision string) error           // durable receipt <rev>.committed; idempotent
    Remove(ctx context.Context, revision string) error                  // entry + receipt; missing is not an error
    Quarantine(ctx context.Context, revision, reason string) error      // move entry (+receipt) to quarantine/ with reason; never deletes
    Entries(ctx context.Context) ([]JournalEntry, error)                // oldest first; excludes quarantine
    List(ctx context.Context) ([]ArrangementRequest, error)             // oldest first; all live entries (display only, never recovery proof)
    BeginRelocation(ctx context.Context, from Scope, fromName string, to Scope, toName string, workflowID uuid.UUID) (RelocationIntent, error)
    RelocationIntents(ctx context.Context) ([]RelocationIntent, error)
    Relocate(ctx context.Context, from Scope, fromName string, to Scope, toName string) error // rewrite destination of every live entry at from
    EndRelocation(ctx context.Context, id string) error
}
type DestinationSettler interface {
    Settle(ctx context.Context, scope Scope, name string) error // caller already holds that destination's mutation lock
}
// domain/workflow/commit_source.go (P2T7 Task 7.5)
type CommitSourceDeps struct {
    Journal      RevisionJournal       // required
    Repo         Repository            // required
    Mutations    MutationCoordinator   // required
    Publications RevisionPublicationStore // exact-revision proof; nil only without a publication registry
    Trees        TreeCommitter         // required; durable rollback snapshots (§6.10)
    Registry     Registry              // required when relocation is enabled; actual ResolveName + Resolve (§6.10)
    Relocator    ArrangementRequester  // required; RelocateArrangements for rolled-forward relocations
    Logger       *slog.Logger
}
func NewCommitSource(deps CommitSourceDeps) (*CommitSource, error) // ErrCommitSourceMisconfigured
func (c *CommitSource) Settle(ctx context.Context, scope Scope, name string) error
func (c *CommitSource) CommittedRequests(ctx context.Context) ([]ArrangementRequest, error)
var ErrCommitSourceMisconfigured = errors.New("workflow: commit source requires journal, repository, mutation coordinator, tree committer and relocator")
```

**Authoring protocol (every path in P2T3/P2T4; all steps inside one `WithMutations` callback
covering source and destination):**

1. **Settle first.** `Stamp` calls `Settler.Settle(scope, name)` for the destination before
   issuing a token. A destination is never overwritten while a prior prepared entry for it
   is undecided. For the operation's own live rename intent only, Stamp receives the
   private withOwnRelocation context described in §6.18; external reads never receive it. If Settle errors, the mutation fails with nothing changed.
2. **Prepare.** `Stamp` issues the new token and `Journal.Record`s the prepared request
   before any visible write.
3. **Visibility commit.** The single atomic rename (manifest for Create/Update; staged
   tree for Copy/Publish/Duplicate/Seed/overwrite) is the filesystem commit point.
   **New-token-before-visibility:** tree operations use
   `Repository.CopyTreeStamped(ctx, src, srcName, dst, dstName, overwrite, stamp func(*Workflow) error)`
   (added to the port and `WorkflowRepo` by P2T3), which runs `stamp` on the staged manifest
   and writes the stamped manifest into staging **before** the rename. The source token
   is never visible at the destination. Seeding stamps the manifest before the first write
   that makes the destination visible. Supporting files are written inside the same lock
   before the receipt.
4. **Registry commit (registry-backed operations only).** Publish/re-publish, seeded publications and shared authoring updates commit only
   when `RevisionPublicationStore.CommitPublication` atomically writes the **new revision**
   and workflow identity with the publication (§6.10). An old publication row is not proof.
   Before replacement, retain a durable `TreeCommitter` rollback snapshot. On record
   failure restore that snapshot (or delete a fresh destination), then `Abandon`. A failed
   rollback preserves its intent/journal and blocks further mutations until settled.
5. **Receipt.** `RevisionStamper.Committed` calls `Journal.MarkCommitted` while the lock is
   still held, then `Requester.RequestArrangement`. Only an acknowledged request is followed
   by `Journal.Remove`. A receipt-write failure is logged at ERROR and leaves the entry
   prepared; step 1 or recovery rolls it forward. The save still reports success because
   its content committed.
6. **Abandon.** An observed failure before or at step 3/4 (nothing became visible, or it
   was rolled back) calls `Abandon`, which `Remove`s the entry.
7. **Relocation (rename/transfer/owner move).** `BeginRelocation` → tree rename/registry
   update → `Journal.Relocate` → `Requester.RelocateArrangements` → `EndRelocation`. Only
   settled entries exist for the source at this point (step 1 ran for both endpoints). The intent carries the stable workflow ID; registry identity, not folder presence, selects recovery (§6.10).
8. **Delete.** Settle runs first. Committed receipts survive deletion, so historical
   revisions still recover. Prepared entries are already decided.

**Settle rules (per prepared entry at the destination):**

| Observed state | Decision |
|---|---|
| Receipt present | committed (no change) |
| Destination manifest carries the entry's revision, and (for any shared destination when `Publications` is non-nil) its publication has `Revision == entry.Request.Revision` and matching non-nil workflow identity | **roll forward:** `MarkCommitted` |
| Destination manifest carries the revision but the exact-revision publication proof is absent (registry-backed) | **rolled back:** `Trees.RollbackTreeCommit` restores the complete prior tree or removes a fresh tree; then `Quarantine(reason "publish-not-recorded")`, WARN; never arrange the uncommitted revision |
| Destination missing or carries a different revision | **abandoned:** `Quarantine(reason "not-committed")`, WARN |
| Manifest read error (not `ErrNotFound`), registry error/unavailable, journal I/O error | **undecided:** leave the entry prepared and return the error; retried on the next poll |

**`CommittedRequests`:** first resolve every `RelocationIntent` under both endpoint locks.
Use `Registry.ResolveName(intent.WorkflowID)` then `Registry.Resolve(name)` and verify ID,
scope and owner. If the registry points at `To`, complete journal/store relocation and source
cleanup, then end the intent; this includes the normal copy-then-swap crash where **both**
folders exist. If it points at `From`, roll back the uncommitted destination and end the
intent. Missing/ambiguous identity or registry errors preserve the intent and return an
error; tree existence alone is not commit proof. Then, for each destination with prepared entries, take that destination's lock via
`Mutations.WithMutations` and `Settle` it. Return every committed entry, oldest
`RequestedAt` then revision, as defensive copies. Committed historical revisions are returned
even when no longer current. Per-destination errors are aggregated
(`errors.Join`), never cause removal, and do not hide other destinations' committed entries.
Never enumerate the workflow catalog; only journal entries are considered.

**Crash-window outcomes (required tests):** crash after Record before rename → quarantined,
no job. Crash after rename before receipt → rolled forward, one job. Crash after publish
tree rename before record → quarantined, no job. Crash after receipt before enqueue →
recovered, one job. Crash after enqueue before Remove → idempotent, one row. Crash mid-
relocation → rolled forward or discarded as above. Overwrite after a missed receipt →
step 1 rolls the earlier revision forward first, so the historical revision still recovers.

P2T8 `WorkerConfig` gains `Committed workflow.CommittedRevisionSource` (required). P2T9
constructs `workflow.NewCommitSource` in `newLayoutStack` before seeding, passes it as
`WorkerConfig.Committed` and `StamperDeps.Settler`, and adds subprocess kill/restart tests
around the manifest/registry/receipt/enqueue commits. Unit tests use
`layouttest.FakeCommittedRevisions` (P2T5). `List` and `Journal` presence are never recovery proof.

### 6.3 Cross-process mutations, retry and publication

P2T7 produces this workflow-owned coordination port (declare it in P2T2 arrangement.go):

```go
type MutationTarget struct { Scope Scope; Name string }
type MutationCoordinator interface {
    WithMutations(ctx context.Context, targets []MutationTarget, fn func(context.Context) error) error
}
// WorkflowRepo implements the port using its manifestLocks.
```

Acquire the unique target keys in sorted `LocatorKey` order and release in reverse order.
The callback receives a context marking the locks held by this coordinator; repo methods
recognise that token and do not reacquire their own locks. Never reuse the token across
independent coordinators. This resolves nested-lock deadlocks. P2T3 wraps whole save,
copy/publish, rename, transfer and delete operations (source AND destination for tree
operations), not just individual file writes. Keep raw identity writes non-triggering, but
coordinate their read/modify/write too, so backfill cannot overwrite a newer presentation.
P2T4 uses the same coordination for seeding. These locks protect concurrency; they do not
resolve the durable crash protocol in §6.2.

P3T8 adds `Mutations workflow.MutationCoordinator` to `QueryService` construction:

```go
func NewQueryService(reader *Reader, requester *Requester, workflows WorkflowAccess,
    instances InstanceAccess, mutations workflow.MutationCoordinator) *QueryService
```

`layoutStack` retains `mutations workflow.MutationCoordinator` from the same filesystem
repo at P2T9. `attachQuery` passes it to `NewQueryService`. For workflow retry, authorize to
resolve destination, obtain Service.LayoutRetryTargets (private caller scope AND shared),
acquire the full sorted lock set, authorize/load again inside it and compare BOTH resolved
scope and expected revision immediately before Store.Retry or missing-row RequestArrangement. Retry linearizes
inside that lock; a save that wins first yields ErrRevisionConflict and no generation bump.
An instance retry uses its authorised immutable snapshot and does not require current
workflow existence. It must not supersede unrelated active historical instance retries.

P2T8 cache fencing compares **both** `JobGeneration` and `ResultGeneration` while P2T7
holds the destination lock. A late generation cannot publish after a newer retry has been
accepted, even when the best retained result is from the old generation. Add
`JobGeneration int64 json:"jobGeneration"` to `CacheEntry` (P2T5); `Generation` remains the
best result generation. Cache reads check positive generations, format, full contract,
coverage and revision. Once a retry finishes, its current worker may republish its retained
best using the current JobGeneration; a stale worker cannot. Publication uses the latest
stored destination, not the destination captured at claim, so an in-flight ownership move
can still publish at the new location. Reconciliation must also detect relocation and
reset cache-settled state for the moved current revision. No stale job recreates a folder.

### 6.4 Worker lifecycle, bounded I/O and recovery

P2T8 owns Task 8.5 (`Notify`, `Start`, `Stop`, `Drain`) and Task 8.6 (`Recover` plus repeated
recovery sweeps). P2T9 cannot compile or pass its wiring tests until these land.

- `Recover` reads `Committed.CommittedRequests`, then idempotently requests each revision;
  only acknowledged committed requests can be removed from the pending journal. It does
  not replay `Journal.List` as proof. Recover immediately at startup **and every poll** so
  a live queue/SQLite outage can recover without a process restart. Do not scan/arrange the
  catalog. Prepared/corrupt entries remain diagnosed, not silently removed or arranged.
- One dispatcher bounds concurrent ProcessRevision calls; coalesce notifications and let
  durable sweeps recover dropped notifications. Stop cancels, stops dispatch and waits
  for all jobs/heartbeat goroutines before closing collaborators/SQLite. Start/Stop are
  idempotent, including Stop-before-Start and cancellation while the queue is full.
- Renew leases every `LeaseDuration/3`, including during one long renderer/reviewer call;
  atomically record the latest stage/attempt. Lease loss cancels the job. Persist terminal
  best-so-far for deadline/supersede; on shutdown retain the completed candidate if one
  exists via Store.Checkpoint without asserting a terminal pass, and leave an interrupted job
  in_progress with its lease so automatic reclaim remains possible.
- Add `StageTimeout time.Duration` to `WorkerConfig`, default 2m, and
  `WithStageTimeout(d time.Duration) CoordinatorOption` (P1T9). P2T8 passes that runtime
  value to the coordinator per job using a copy (do not mutate a shared coordinator).
  Measure, Render and Review each receive a separate stage timeout nested under the 15m
  JobTimeout. Persistence/fence/cache/recovery calls use bounded contexts too (30s),
  including detached cancellation cleanup. Timed-out stages report the exact failing
  stage and retain best-so-far. Stop must not hang on `context.WithoutCancel` I/O.

### 6.5 Render evidence is produced from the sink

P1T8 adds these fields/types to render.go, mirrored by P3T4 job.ts:

```go
type RenderedSegment struct { Start Point `json:"start"`; End Point `json:"end"` }
// additions to RenderedText:
Content string `json:"content"`    // actual DOM textContent, not candidate text
Glyphs []Rect `json:"glyphs"`      // actual Range line bounds in canvas coordinates
Visible bool `json:"visible"`     // computed display/visibility and cumulative opacity
// additions to RenderedEdge:
Segments []RenderedSegment `json:"segments"` // actual SVG M/L subpaths; gaps preserved
StrokeColor string `json:"strokeColor"`     // computed opaque RGB
DashPattern string `json:"dashPattern"`     // computed pattern
Visible bool `json:"visible"`
ArrowPoints []Point `json:"arrowPoints"`    // transformed marker outline at destination
```

P3T4 `collectText` populates Content/Glyphs/Visible from DOM/styles/Range, including ancestor
opacity/visibility; P3T4 `collectEdges` parses the actual path `d`, transform and marker
shape/styles. Missing/unsupported paths or hidden markers are blocking, not guessed from
candidate geometry. Verify all actual segments against the expected gap-split route,
complete text against node/branch text, and arrow orientation/visibility against its final
segment. Runtime state probes compare paths, label positions, crossings and visible
arrowheads as well as node boxes. P3T2 supplies those real DOM elements; P3T5 captures them;
P3T6 consumes them. No field may be filled by echoing expected geometry.

P3T6 pixel checks must prove labels, edge paths, gap treatment and arrowheads actually
appear across the entire tile set. Two colours in a node rectangle proves neither readable
text nor an edge. Keep integrity/seam checks, then sample the actual route corridors at
2 canvas-pixel spacing (including both sides of bends, endpoints and parallel lanes),
respecting dash/gap intervals; require the expected stroke within max(1,strokePx*zoom/2)
image pixels with per-channel tolerance 32 for antialiasing. Check marker interiors and
line/glyph regions for visible ink, contrasting background and clipping. In overlapping
tiles, a feature passes only if at least one tile contains its complete local region.
Mutated screenshots with valid hashes and clean DOM reports must still fail when paths,
arrowheads, text ink or crossing separation are removed. A rendered fixture consisting
only of coloured node rectangles is a **failure fixture**, never a passing review fixture.
This deterministic DOM+raster reviewer is the selected implementation; no external model
or unprovided vision service is implied.

P3T5 artifacts are immutable: create a unique capture directory with `os.MkdirTemp` below
`ArtifactDir/<revision>/<attempt>/`. Retry/reclaim/processes may reuse an attempt number;
none may overwrite PNGs referenced by an earlier result. Publish files only when complete,
keep paths/hashes stable, and clean only unreferenced failed captures.

P1T9 produces `func HasRenderedEvidence(geo Geometry, review *Review) bool`, checking
Rendered, geometry digest, exact contract, ready fonts, renderer version, practical zoom,
a complete union of readable-scale tile regions covering the entire origin-zero canvas and
an overview. Require nonempty paths, valid SHA256 digests, finite positive artifact
regions/scales and nonzero ReviewedAt; unit artifacts remain synthetic and production
file validation belongs to the adapter. Use it for pass eligibility, candidate ranking,
Result.Unreviewed and P2T5 StoredResult.IsUnreviewed. The production reviewer additionally
validates file content; unit fakes must construct matching evidence for the actual request,
reject empty/mismatched rendering, and never ignore revision/attempt/geometry arguments.

### 6.6 Read truth, compatibility and dashboard polling

P1T2 `Compatible` and P3T1 `validateGeometry` check the **full** renderer contract, displayed
node/type/edge text, output/label verification data, route attachments/sides/orthogonality,
canvas containment with origin exactly (0,0), finite positive dimensions/spacing, nonnegative Margin/NodeTextSlack, nonnegative integral CrossingSweeps, and crossing ordinals/locations.
A matching version string alone is insufficient. Keep the pure validator independent of
DOM measurement. P3T3 renders only validated geometry; P3T11 never labels an unreviewed
fallback “Layout reviewed”.

P2T5 Reader sets Unreviewed for every retained fallback, including retained positions. A
missing/corrupt referenced result returns `DisplayUnavailable` or `DisplayNeedsAttention`
with Available=false/explicit reason, captured fallback and Retry; never DisplayComplete
with no compatible reviewed geometry. If stored geometry is rejected, clear its Evidence
and Contract. Retained complete geometry is resolved through the immutable prior result
reference and validated; it must survive cache deletion and workflow deletion.

P3T11 handles in-band `available=false` just like a failed fetch: retain the last compatible
geometry for that revision, show the availability error, and back off until recovery. A
successful RetryAck installs a minimum generation before refetch, invalidates older pending
requests, displays pending and resumes polling even if the first GET returns the previous
terminal generation. Reject lower job/result generations for the same revision; on key
change reset `retrying` and all request/generation guards. P3T12/P3T13 join snapshot/manifest
revision before passing one coherent status+geometry value to both graph and panel.

### 6.7 Acceptance and dependency provenance

P3T7 and P3T14 jointly own the release gate: real `initDomain` → authoring save → SQLite
best result → real authorised HTTP router → shared WorkflowReview and ExecutionView DOM.
Drive both routes with that actual wire JSON in a real browser. Assert equality of every
node box/text, every edge ordinal/path/label/attachment and geometry digest, including a
completed instance whose arrangement finishes later. Repeat after retry, external save,
cache deletion and restart. Do not replace the graph, API response or reviewer with a fake
in this gate. Keep real tests for pr-review, feature-pipeline and ralph-loop; a browserless
run may test visible failure but cannot count as acceptance. `make test-render` must be a
required dependency of `make test-all` and the repository CI workflow (P3T7 owns that edit),
with ARBITRON_REQUIRE_BROWSER=1 and embedded assets rebuilt first.

| Design dependency | Owning producer and wiring tasks | Status |
|---|---|---|
| LAYOUT-CORE | P1T1–P1T9; P2T9 coordinator/worker | planned |
| RENDER-REVIEW | P3T2–P3T6 DOM/font/browser/raster producers; P3T7 construction and browser-required gate | planned with strengthened sink tests |
| SAVE-REVISION | P2T2 metadata, P2T3/P2T4 all writers, P2T7 filesystem; §6.2 commit protocol | planned |
| LAYOUT-CONFIG | P2T1 loader → P2T9 Requester → P2T8 persisted job budget | planned |
| LAYOUT-STORE | P2T5 Store/helpers → P2T6 SQLite → P2T9 injection | planned |
| ASYNC-PIPELINE | P2T8 Tasks 8.1–8.6 → P2T9 both CLI lifecycles; committed source below | planned |
| PUBLICATION-CAS | P2T7 MutationCoordinator/locks → P2T3 writers, P2T8 publisher, P3T8 retry | planned |
| QUERY-AUTH | P3T8 workflow/engine seams → P3T9 HTTP/P3T10 MCP → P3T11–P3T13 UI | planned |
| SAVE-COMMIT-PROOF | P2T2 port → P2T7 Task 7.5 `domain/workflow/commit_source.go:CommitSource` + `filesystem.RevisionJournal` receipts → P2T3/P2T4 protocol → P2T8 Recover → P2T9 injection + kill/restart tests | planned (resolved 2026-10-17, §6.2) |

### 6.8 Additional test-support owners

P2T5 produces `layouttest.Artifacts(geo layout.Geometry) []layout.Artifact` and
`layouttest.FakeCommittedRevisions` in fixtures.go/committed.go with tests. Its exact
Prepare/Commit/Abandon/SetError/CommittedRequests methods are in P2T5's final prerequisite;
P2T8 consumes them from Task 8.2 onward. A fake is never passed by newLayoutStack.
P3T1 produces `fixtureGeometryWithBoxes(boxes: Record<string, Rect>): Geometry` in
arranged/testFixtures.ts. P3T3/P3T12/P3T13 import it for valid moved/resized fixtures.
P3T6 owns the real-browser fixture generator/immutable testdata directory described in
its alignment prerequisite. P3T7 creates .github/workflows/layout.yml; P3T14 extends its
browser suite. These support producers do not introduce external services. The production
SAVE-COMMIT-PROOF producer is P2T7 Task 7.5 `CommitSource`, never this fake.

### 6.9 Retry of a stored result that can no longer be displayed

Reader downgrades a StatePassed record when its stored geometry is missing/incompatible
or its exact-candidate rendered evidence/verdict/findings do not support a pass. RetryEligible=true must then be actionable; ordinary valid passed results
still reject retry. P2T5 extends Store with:

```go
InvalidateResult(ctx context.Context, f Fence, resultGeneration int64, reason string, now time.Time) error
```

Only a StatePassed row, or a StateSuperseded row with VerdictPassed, matching **both** f.Generation and resultGeneration transitions
to StateNeedsAttention; preserve immutable results/input and set the incompatibility
reason. A mismatch returns ErrStaleGeneration; missing row returns ErrRecordNotFound.
P2T5 Requester.Retry, before Store.Retry, loads the row/result and captured graph for
StatePassed and superseded-with-passed-verdict alike. For
missing result (ErrRecordNotFound), failed Compatible, invalid HasRenderedEvidence, blocking
review/preflight findings or an inconsistent passed verdict, it calls InvalidateResult under
the same expected job/result fence, then Retry. Store outages return errors and do not
invalidate. A valid passed result with complete evidence and zero blocking findings still gets ErrRetryConflict. Reader stays read-only.
P2T5 produces MemStore and its shared contract suite; P2T6 produces the SQLite conditional
update; P2T9 injects that Store; P3T8/P3T9/P3T10 consume the unchanged retry seam.


### 6.10 Exact publication proof, reversible tree replacement and data-source closure

These are concrete implementation refinements of §6.2's recorded publication-commit
policy, not a new commit policy. An existing publication row belongs to an older revision;
its mere presence cannot prove a re-publication committed. Existing `WriteAndRecord` does
not restore an overwritten tree after Upsert failure. The following tasks replace those
behaviors for stamped authoring operations.

P2T2 declares these additions in workflow's ports/types (no domain/layout import):

```go
// additions to domain/workflow/visibility.go:Publication
Revision   string    // publication proof for exactly this authoring save
WorkflowID uuid.UUID // stable identity linked in the same atomic publication write
// domain/workflow/arrangement.go
type RevisionPublicationStore interface {
    PublishedStore
    CommitPublication(ctx context.Context, name string, publication Publication) error
    RenamePublication(ctx context.Context, id uuid.UUID, oldName, newName string, publication Publication) error
}
type TreeCommit struct {
    ID string
    Destination MutationTarget
    Revision string
    PriorRevision string
    HadPrior bool
}
type TreeCommitter interface {
    PrepareTreeCommit(ctx context.Context, dest MutationTarget, revision string) (TreeCommit, error)
    FinishTreeCommit(ctx context.Context, id string) error
    RollbackTreeCommit(ctx context.Context, id string) error
    PendingTreeCommits(ctx context.Context) ([]TreeCommit, error)
}
```

- **Producer P2T6:** migration 019 adds nullable `published_workflows.saved_revision`;
  `adapter/sqlite/published_workflow_repo.go:CommitPublication` atomically upserts the
  publication fields, `workflow_id` and nonempty `saved_revision`. `RenamePublication`
  atomically renames the stable registry identity and publication name/proof in one SQLite
  transaction, rolling back both if either update fails; shared Rename uses this seam
  rather than two separate registry/publication commits. `PublicationOf` reads
  both new proof fields via explicit SQL; do not silently omit them in the older generated
  query model. Existing metadata-only `Upsert` preserves `saved_revision`; it never creates
  new save proof. Legacy NULL revision is readable but cannot prove a prepared new save.
- **Producer P2T7:** `WorkflowRepo` implements `TreeCommitter` using durable private tree
  backups and intents below `.arbitron-tree-commits/`, excluded from migration/catalog/file
  APIs. Prepare completes a full prior-tree copy and fsyncs it plus its intent before any
  replacement; errors leave visible content unchanged. Rollback restores all manifest,
  supporting-file and layout-cache bytes via staged rename, or removes a fresh destination.
  Finish removes backup/intent only after a durable commit receipt; each method is
  idempotent and holds the same destination mutation lock. Recovery first completes or
  rolls back pending tree commits, preserving undecided intents on I/O/registry errors.
- **Consumers P2T3/P2T4:** `PublishRequest` gains `PreparedPublication func() (Publication, error)`;
  validate the revision-capable store before calling WriteTree. After WriteTree stamps its
  final manifest, this callback supplies its exact revision/ID to the atomic publication
  write. Stamped operations always provide it; nil is only the isolated legacy path.
  Fresh/re-publish, shared authoring Update/Rename and seed advance
  the publication revision with `CommitPublication`. Resolve/register stable identity
  before that call, and stamp the same ID/token into the staged manifest. Complete the
  tree before the atomic publication call. On failure restore the old tree even for an
  overwrite; only then abandon the request. A rollback error preserves evidence and
  blocks overwrite/delete through Settle. Ordinary private Create/Update use the atomic
  manifest commit and need no publication proof. Rename/transfer relocation carries its
  stable ID in `RelocationIntent`; post-registry source cleanup may fail without undoing
  the committed destination.
- **Recovery P2T7:** `CommitSourceDeps.Publications` is `RevisionPublicationStore`;
  `Trees TreeCommitter` is required; `Registry Registry` resolves relocation. Publication
  proof must equal both the prepared revision and its stable ID when nonnil. In a crash
  before the new publication write, an old row means rollback, not roll-forward. If an
  operation committed publication but crashed before receipt, make the receipt and finish
  its tree intent. If a receipt exists, finish its backup cleanup without removing the
  committed request before enqueue succeeds.
- **Root P2T9:** move `identityRepo` construction before seeding; pass real `publishedRepo`
  and `identityRepo` explicitly to `newLayoutStack`, inject `Trees: repo` and `Registry:
  registry` into CommitSource, and `publications` into its publication port. Seeder gets
  `SetRegistry` and the same mutation coordinator. Neither a nil proof source nor a
  type assertion failure is silently ignored when stamped publishing is enabled.
- **Required strict TDD:** exact-revision round trip and atomic rollback on publication
  SQL failure; legacy/previous publication does not prove new commit; re-publish crash
  after replacement before publication restores every old tree byte and produces no new
  row; crash after atomic publication before receipt produces exactly one new revision
  job; failed rollback retains intent and rejects a next save; relocation with both folders
  present chooses registry authority. Root subprocess kill/restart tests drive these
  windows through actual filesystem + SQLite + initDomain and assert saved bytes and
  terminal revision records. Removing the real publication/registry/tree injection must
  make those tests fail.

`Store.MarkCached(ctx, fence, resultGeneration, dest)` is a conditional settlement, not an
unfenced notification: match current job generation, best result generation and full
scope/owner/name destination or return ErrStaleGeneration. `Retry` and `Relocate` reset
CacheGeneration to zero so PendingCache can republish a retained best result for the new
generation/destination. P2T5 defines/tests the full port and MemStore, P2T6 implements SQL,
P2T8 passes the exact published fence/destination, P2T9 supplies the same store/cache.

### 6.11 Render job input provenance and multiple graph instances

P1T8 owns `RenderRequest.WorkflowNodeTypes` and the four-argument `NewRenderRequest` in
§1. P1T9 obtains the map directly from each immutable manifest node's ID and Type. P3T5
copies it, revision and attempt into the page render job; P3T4 requires one nonempty type
for every geometry node and verifies its type caption. The synthetic WorkflowDefinition's
presentation revision and LayoutView.savedRevision both equal job.revision. Reject missing,
empty or mismatched maps/revision/attempt before capture. A human-readable TypeCaption is
never a source for reconstructing the raw type. P2T8 fakes preserve and validate these
arguments. Root/browser tests vary actual manifest node types and fail if the map is empty.

SVG marker references must remain correct when two graphs are mounted. P3T3 ArrangedGraph
creates a stable sanitized React useId namespace and feeds it to P3T2 RoutedEdge via data;
marker ID is `<graphInstanceId>-arranged-arrow-<ordinal>`. Tests mount two graphs with edge
ordinal zero and assert each path resolves its own visible marker. Namespace differences
do not alter any persisted geometry or measured output.


### 6.12 Historical work, readable commits and bounded crossing selection

- P2T5 Requester and P2T8 Worker do **not** automatically supersede other revisions on a
  save or recovery replay. Every committed immutable revision stays eligible for review;
  current-revision/generation CAS prevents it publishing over another save. The spec
  permits cancellation but does not require it. This policy avoids cancelling an
  authorised historical-instance retry without inventing a missing retry-origin field.
  Explicit Store.Supersede remains supported and tested as a lifecycle operation.
- P2T7 WorkflowRepo.Load takes the same destination lock used by whole authoring
  operations, recognising the callback's owned context. A concurrent graph read/trigger
  sees the final committed or restored prior manifest, never a staging source token or
  the gap in overwrite's backup/rename sequence. Hold locks only through the authoring
  commit/enqueue acknowledgement, never through arrangement/render/review. Startup
  recovery settles pending tree/relocation intents before seeding/backfill/read traffic.
- P1T6 compares complete routed candidates across bounded ordering-search prefixes
  0..Options.CrossingSweeps, using geometric blocking contacts, actual routed crossing
  count, then deterministic prefix order. P1T5 includes its bounded transposition search
  in each prefix so increasing the budget cannot regress its best result. Tests include
  a graph whose removable crossing survives barycentre-only ordering and a case where
  layer crossing count differs from final routed crossings. Advisory crossings may pass
  only after that search selects its best remaining distinguishable crossing arrangement.
- P3T1 owns correcting the existing client request helper to read the HTTP `{error,code}`
  error body (legacy `message` may remain a fallback). HTTP/MCP failures retain their
  canonical status/concealment behavior. P3T13 owns adapting `useFollowActiveNode` for
  variable boxes and explicit Recenter on terminal instances; tests drive actual graph
  viewport/callout behavior rather than accepting a no-op mocked callback.


### 6.13 Portable Unicode typography has a production producer

P3T2 owns bundling `@fontsource/noto-sans-sc@5.3.1` and
`@fontsource/noto-emoji@5.3.2` alongside Inter in fonts.ts at weights 400/500/600/700,
with package-lock integrity and font licenses. DefaultContract/contract.json (P1T2) use
FontFamily `Inter, Noto Sans SC, Noto Emoji` and FontPackage
`@fontsource/inter@5;@fontsource/noto-sans-sc@5.3.1;@fontsource/noto-emoji@5.3.2`.
The version remains arranged-renderer/1 because no renderer format has shipped yet.

P3T2 also owns a build-only `fontkit@2.0.4` generator over the **shipped** WOFF2 cmap and
character sets, producing a font coverage manifest with resolved package versions and
SHA256 for every bundled font file. fonts.ts consumes that manifest for each measured or
displayed text, loads its used ranges/weights before layout/capture, and rejects unsupported
scalars/grapheme sequences with an explicit rendering issue. Shaping controls are handled
explicitly, never mistaken for missing visible glyphs. P3T4 invokes the same checks before
measurement and rendering; measured content Width/Height retain actual fractional CSS
values (e.g. two 13px × 1.35 lines are 35.1px high), and only domain sizing rounds box
dimensions. Browser Math.ceil quantization must not falsely fail metricForText tolerance; P3T5 propagates failures to the worker. P3T3 invokes the same
font stack/coverage checks in both user views before drawing reviewed geometry and emits
loading/failure/ready issues through ArrangedGraph.onDisplayIssues. P3T12/P3T13 pass that
callback to the actual panel, suppressing a reviewed badge during font loading/failure.
Revision/digest/font-asset epochs reject stale promise callbacks. P3T7/P3T14 bundle/verify these assets in the
real distribution and exercise Latin + CJK + combining text + supported emoji in all
three surfaces with system fallback fonts unavailable. Removing the coverage manifest or
a required font must fail capture, never produce a passed result with tofu glyphs.

Docker's system Noto packages may remain for browser operation but are not the source of
reviewed label glyphs. The previous assumption that installing fonts only on the server
proves client parity is removed. Unsupported text remains saved and visibly failed with
captured fallback and Retry; no unprovided external typography service is consumed.


### 6.14 Test-support API and storage precision

P2T5 owns the following in domain/layout/layouttest before P2T8/P3T8 consumption (all
mutable values are copied and concurrent access is mutex-protected):

```go
func MeasureItems(c layout.RendererContract, items []layout.TextItem) layout.Measurements
func SampleWorkflow() *workflow.Workflow
func Arranged(t testing.TB, wf *workflow.Workflow) layout.Geometry
func ManifestJSON(t testing.TB, wf *workflow.Workflow) []byte
func NewFakeManifests() *FakeManifests
func (f *FakeManifests) Set(scope workflow.Scope, name, revision string)
func (f *FakeManifests) Remove(scope workflow.Scope, name string)
func (f *FakeManifests) SetErr(err error)
func (f *FakeManifests) Calls() int
func NewFakeJournal(entries ...workflow.ArrangementRequest) *FakeJournal
func (f *FakeJournal) SetErr(err error)
func (f *FakeJournal) Has(revision string) bool
func (f *FakeJournal) Len() int
func NewFakeCache() *FakeCache
func (f *FakeCache) SetPublishErr(err error)
func (f *FakeCache) SetReadErr(err error)
func (f *FakeCache) Seed(scope workflow.Scope, name string, entry layout.CacheEntry)
func (f *FakeCache) Published(dest layout.Destination) []layout.CacheEntry
func (f *FakeCache) PublishCalls() int
func NewFakeCommittedRevisions() *FakeCommittedRevisions
func (f *FakeCommittedRevisions) Prepare(req workflow.ArrangementRequest)
func (f *FakeCommittedRevisions) Commit(revision string)
func (f *FakeCommittedRevisions) Abandon(revision string)
func (f *FakeCommittedRevisions) SetError(err error)
func (f *FakeCommittedRevisions) CommittedRequests(ctx context.Context) ([]workflow.ArrangementRequest, error)
// P2T5 store.go, consumed by P2T6–P2T8
func LocatorKey(scope workflow.Scope, name string) string
func (d Destination) Key() string
func (s State) Terminal() bool
func (r StoredResult) UnresolvedFindings() []Finding
func (r StoredResult) IsUnreviewed() bool
// P3T2 fonts.ts, consumed by P3T3/P3T4
// export const RENDERER_FONT_FAMILIES: readonly string[]
// export function ensureBundledTextFonts(doc: Document, texts: readonly string[], contract: RendererContract): Promise<{fontsReady: boolean; fontFamilyResolved: boolean}>
```

FakeJournal implements every receipt/quarantine/relocation method in §6.2; constructor
entries are prepared until MarkCommitted is called. FakeCommittedRevisions never returns
a merely prepared entry. FakeCache calls the fence and records no failed publication.
Artifacts is unit-only metadata with full canvas coverage, not a passing production PNG.

P2T6 uses layout-specific SQLite INTEGER UnixNano timestamps for layout records/leases,
including all comparisons, and converts to UTC time.Time at the port boundary. Do not
reuse the existing whole-second timeToString helper or change it globally. Tests use
subsecond lease expiry/renewal and verify exact round trips and stale claim fences.

### 6.15 Shutdown checkpoints and text refinement inputs

P2T5 declares/tests `Store.Checkpoint(ctx context.Context, f Fence, result StoredResult,
replaceBest bool, now time.Time) error`; P2T6 implements it transactionally and P2T8 uses
it only when a shutdown interrupts a job with a completed candidate. Match in_progress and
exact current job generation, insert immutable result `(revision,generation)` and optionally
update the best pointer/evidence, leaving state/stage/lease claimable after expiry. Repeating
the identical checkpoint is idempotent; conflicting content at the same result key fails.
Stale fence or unavailable storage cannot overwrite newer work. A later expired-lease
claim increments generation, so its final result cannot collide with the checkpoint.
Ordinary job/stage deadline and review failures still Complete as failed. P2T9's real-root
shutdown/restart test asserts both retained geometry and automatic eventual terminal
recovery; an intermediate fake Complete call is insufficient.

P1T4 `edgeLabelSizes` consumes Options as well as graph/measurement/contract, so P1T8's
NodeTextSlack refinement grows branch-label boxes too. Every caller (P1T6/P1T7 test
helpers included) passes its actual options. NodeTextSlack keeps its wire name but denotes
shared text rounding slack. `render_mismatch` does not trigger arbitrary spacing growth:
wrong/missing render evidence is a failed review or an unfixable finding, never evidence
that another unchanged typography input will improve the rendering.

### 6.16 Read settlement and the live peer-crash window

Destination locks prevent concurrent visibility only while their holder is alive. A peer
process may crash with an uncommitted shared or rename tree visible, then release its OS
lock while another already-running process reads before the next worker sweep. Likewise,
a private Rename's prepared target token alone does not prove Registry.Rename committed.

- P2T2 declares `ErrMutationPending = errors.New("workflow: destination has an undecided durable mutation")`.
- P2T7 produces `WorkflowRepo.SetReadSettler(s workflow.DestinationSettler)`. External Load
  acquires the destination lock and invokes this settler with its coordinator's owned
  context before decoding/returning the manifest. Internal Load calls already carrying
  that owned context read directly, avoiding Settle→Load recursion. Nil is permitted only
  before composition or in isolated legacy adapter tests, never in the production root.
- P2T7 cache publication also invokes the configured settler while destination-locked
  before its internal owned-context Load; ErrMutationPending remains transient/pending,
  never a permanent not-current cache settlement for an uncommitted replacement.
- P2T7 `CommitSource.Settle` checks for any live relocation intent covering its destination
  **before** rolling any prepare forward. It returns ErrMutationPending unchanged for external/foreign-operation contexts until
  the global recovery pass resolves that intent under BOTH endpoint locks by registry
  authority. The operation-private active-rename context in §6.18 is the only exception. It must not acquire another lower-order endpoint lock from the one-lock
  callback. Ordinary tree prepares settle through the exact publication proof and restore
  old bytes on failure as §6.10 requires. A valid private atomic manifest commit remains
  readable even if its receipt/enqueue was interrupted.
- P2T9 injects `repo.SetReadSettler(commits)` immediately after constructing CommitSource.
  A bounded pre-seed settlement pass runs before seeds/backfill/traffic; undecided durable
  operation errors fail boot explicitly while preserving evidence. Worker.Start still owns
  initial and repeated enqueue recovery after seed capture. A live process's external
  reads invoke settlement too, so a peer crash does not require restarting that reader.
- P3T9 maps ErrMutationPending to HTTP 503 UNAVAILABLE for layout and existing manifest
  surfaces; P3T10 reports an explicit tool error. P3T11–P3T13 retain prior compatible graph
  and back off rather than treating it as absent/legacy. No read stamps or arranges an
  untouched legacy workflow.
- Strict RED→GREEN tests use actual two-process filesystem locks: hold/pause the writer
  after target visibility before registry proof, kill it, read through the already-running
  peer before its next sweep, and assert restored prior committed content or explicit
  unavailable error, never the uncommitted revision. Include private Rename, shared
  re-publish over an old row and postproof/pre-receipt roll-forward. Removing SetReadSettler
  injection must fail those sink assertions. Test internal context reentry without recursion
  and a pending relocation returning promptly without second-lock deadlock.

### 6.17 Remove inert requester input and preserve relocation envelopes

`NewRequester(store Store, attemptLimit int, logger *slog.Logger) *Requester` has three
arguments. After selecting no automatic supersede, Requester has no current-manifest
consumer and must not retain an unused ManifestSource collaborator. RepositoryManifests
remains a real Worker publication input. P2T5 defines this constructor; P2T9 root and
P3T8 tests use the same three-argument contract. Historical retries and captured request
replay never reload a later workflow's graph.

RequestArrangement validates matching revision and a canonical current request destination,
but does not require that destination equal Manifest.Presentation's original saved locator:
a committed ownership move/rename relocates request metadata while immutable saved bytes
stay unchanged. CommitSource/RevisionStamper are the trusted internal producers. Tests
relocate before enqueue, recover the original Manifest at the new destination and replay
an older request without moving a durable row back. For a journal-less missing-row
historical-instance retry, the authorised snapshot original locator remains historical
metadata, not proof of a current folder. Exact-token CAS may skip optional old-locator
cache refresh; Store-by-revision still supplies identical geometry to the moved workflow
and historical instance. P3T8/P3T14 test this sink parity and no folder recreation without
requiring current workflow visibility/existence for the historical retry.

ErrMutationPending has a fixed safe public message: `workflow destination is awaiting durable
mutation recovery; retry shortly`, HTTP503 `{error: <message>, code: "UNAVAILABLE"}` via
WriteJSON for this sentinel, since the existing WriteError sanitizes ordinary 5xx details.
P3T9 handles layout and existing manifest save/read routes; P3T10 handles authoring/get/query
MCP tools with the same safe reason. Raw filesystem/registry errors stay sanitized.

### 6.18 Current-source reconciliation and remaining completion work (2026-10-18)

These contracts replace the stale protocol blockers recorded in earlier execution notes.
They do not retroactively check off acceptance tests or assert full feature completion.
Each producer identified as existing below has a named source or owning task. P2T9.8 has since landed (stderr console logging and unfiltered subprocess protocol tests); the group completion chain is P3T15–P3T20. Every owning task retains the
required sink/mutation tests, including crash windows not yet covered. No task may keep
skipping an available producer because an older note called it absent.

| Contract / owning tasks | Production producer, consumer and required proof |
|---|---|
| Active rename — P2T2/P2T3/P2T7/P2T9 | `domain/workflow/commit_source.go:withOwnRelocation` / `ownsRelocation` is a package-private context marker for exactly the newly created intent. `service_revision_move.go:renameRevision` creates it inside the callback already owning BOTH sorted endpoint locks and passes it only to Stamp. Settle skips only that exact intent; other covering intents and all external reads still return ErrMutationPending. Recovery still uses registry authority, never this context or folder existence. Tests must cover wrong intent, unrelated destination, external/peer reads, pre/post-registry crash and shared atomic publication; do not add an exported settlement bypass. |
| Seed acknowledgement — P2T2/P2T4/P2T5/P2T9 | workflow owns `AcknowledgedArrangementReader.AcknowledgedArrangement(ctx,revision) (ArrangementRequest,error)`. `domain/layout/requester.go:Requester.AcknowledgedArrangement` reconstructs the exact immutable request from Store.Get (not a moving manifest; ErrRecordNotFound maps to workflow.ErrNotFound). `RevisionStamper.EnsureStamped` can restore missing journal input ONLY for SaveReasonSeed with nonnil workflow ID, exact revision/scope/owner/name/reason/time/ID/retained/input equality, and no conflicting live entry. Seeder additionally checks exact publication revision/ID/publisher/source before recordSeed. Root supplies the real Requester, not an optional empty reader. Store outage preserves saved content and returns an explicit error; no new token or ledger reorder. |
| Abandoned fresh seed claim — P2T4/P2T7 | `domain/samples/seeder_revision.go:seedRevision` records the prepared request BEFORE Registry.Register and before visible tree creation. `CommitSource.releaseSeedClaim` removes only that exact prepared seed's ID after tree rollback, no remaining destination and no publication, under the same destination lock; absent or foreign/different claims are untouched. I/O error retains prepared evidence. Add the crash immediately after Register, prior to tree intent, as well as the existing before-proof reseed case. Already-ledgered or unproven existing trees are never overwritten; prior-tree overwrite rollback acceptance belongs to Publish (P2T3), not fresh seeding. |
| Retry resolution lock set — P3T8/P2T9 | workflow Service produces LayoutRetryTargets, layout.WorkflowAccess consumes it; QueryService validates inclusion of the first destination, acquires both private and shared candidates, then reauthorizes and compares scope AND token. Existing root injects the actual Service and repo through attachQuery. Same-token private shadow must conflict, shared-only retry must succeed, and missing/empty target sets must fail without a generation bump. Historical retry locks snapshot and committed relocated envelope destinations, revalidating after acquisition; it never reauthorizes against current workflow visibility. |
| Settled raw manifest/cache reads — P2T7/P2T9/P3T9/P3T10 | `adapter/filesystem/file_reader.go:FileReaderAdapter.SetSettledRepository(*WorkflowRepo)` is called by initDomain on the FileReader shared by HTTP, MCP and engine. `withExisting` settles BEFORE scope/existence selection, and the reserved-artifact read itself occurs under the same destination lock. Existing context-free engine.FileReader uses a bounded 30s adapter context; do not hold an unlocked path returned by ResolvePath out as safe raw-byte reading. Actual HTTP/MCP ReadFile must return committed bytes or the fixed unavailable error after a peer crash, even if a private swap temporarily removed the tree. Omitting this setter must fail the first raw read before any other settling read/poll. |
| Stdio principal/logging — P2T9/P3T10 | `cmd/arbitron/stdio_identity.go:domainStack.stdioContext` resolves UserRepo.GetDefaultUser and LocalAdminIdentity only in local mode; both actual MCP launch sites pass its context to ServeStdio. Org/edge remain unauthenticated and deny authoring, matching the current transport policy; no synthetic organization admin. **Implemented producer P2T9.8:** `adapter/observability/logging.go:SetupLogging` sends console text to os.Stderr; stdout is MCP JSON. `cmd/arbitron/layout_stdio_protocol_test.go` and the unfiltered runStdioSession helper verify the actual stream. Preserve those guards rather than reintroducing filtering. Actual subprocess create/update/retry and EOF tests assert immutable SQLite sinks and no non-protocol stdout. |
| Zero policy values — P1T2/P1T4/P3T1 | Domain validateGeometryOptions and client validateGeometry accept finite Margin >= 0, NodeTextSlack >= 0 and integral CrossingSweeps >= 0; all other spacing fields are finite > 0. Zero search budget preserves manifest layer order; zero margin still requires canvas containment of all visible ink/markers. Accept zero policy fixtures emitted by Go and reject negative/fractional/nonfinite values on both sides. |
| Client font failure — P3T2/P3T3/P3T11–P3T14 | Existing ArrangedGraph offers `Reload reviewed fonts` through window.location.reload while retaining an explicitly unreviewed fallback and suppressing the reviewed badge. This reload retries client assets; it never POSTs arrangement retry or changes a valid passed generation. Server measurement/render/font failure remains eligible for the existing arrangement Retry. Real routes must demonstrate font failure→reload→restored reviewed geometry with unchanged revision/generation/digest and no POST. |
| Sample profile validation — P2T1/P3T7/P3T14 | Existing `adapter/config/sample_validation_test.go:TestSampleWorkflows_ValidateWithTheirDocumentedProfiles` obtains required aliases/kinds from the bundled samples' PROFILES.md. Preserve the negative unknown-profile assertion. The former assumption that all sample-specific aliases ship as default profiles is obsolete; do not alter sample execution, default profiles or layout thresholds to satisfy that old assertion. Layout acceptance saves/holds snapshots without running LLM/agent work. |

Historical feature-pipeline/ralph-loop failures are reproduction leads, not current blockers
after P3T7's recorded closure; a newly observed readability failure is a producer defect,
not permission to mark the feature complete with needs_attention. P3T7 owns reproducing
and closing them with P1T6/P1T8/P3T2/P3T4/P3T6 as appropriate; P3T14 owns both real routes.
Docker/browser and full-suite/race evidence is recorded in the owning tasks;
P3T7's later closure supersedes its older blocked notes. Re-run those existing gates
for the group migration; do not reinstate resolved failures as permanent blockers. They consume existing/named task producers,
not an unprovided external review service. Keep tests that expect a pass within five
attempts; never loosen review, shrink samples, skip the browser or raise the budget.

### 6.19 Parallel-group completion and ordered format migration

The 2026-10-19 split is binding: **P3T15 / 15.1 → P3T16 / 15.2 → P3T17 / 15.3 →
P3T18 / 15.4 → P3T19 / 15.5 → P3T20 / 15.6**. These are separate files and green
checkpoints, not six steps still owned by P3T15. Older references to P3T15's full
migration mean this chain; they never require an earlier task to wait on P3T20.

P3T15's Exact interfaces define Graph.Groups, revision-bound View.Groups, GroupText,
GroupElement/Lane/Connector/Crossing and IDs, optional Geometry group arrays,
Attachment.ElementID and captured RenderRequest/ReviewRequest.WorkflowGroups. These
input declarations exist in source. P3T16 produces usable v2 geometry and complete
Go/TS validation, and atomically migrates FormatV2, arranged-renderer/2, contract.json
and structural fixtures. Ordinary v2 edges also carry ElementID. P3T17 declares the
new actual DOM report fields (§6.20) and produces shared drawing/capture. P3T18 consumes
that report in review, storage truth and genuine screenshot fixtures. P3T19 wires live
inputs and child revision tiles. P3T20 proves the combined real-root/browser sink.

Earlier §§1–3 v1 examples apply only before P3T16. Positive group capture belongs to
P3T17 and positive rendered review to P3T18; neither is a P3T16 prerequisite. Replace
ordinary-box arranger guards at P3T16 and standard-layout renderer guards at P3T17 with
stronger owner-level assertions so those packages can stay green; preserve the v1/nil/
wrong-ownership fallback controls. P3T20 strengthens the release gate, not an earlier
unit test's permission to implement its own producer.

GroupGraph is produced by existing workflow.AnalyzeGroups of the immutable manifest.
Reader derives View.Groups under that same SavedRevision even during outages. Child
snapshots/ownership/live data come from engine.GraphStateReader; their arrangements
come from the existing authorized instance layout endpoint. No second group service,
SQL store or ownership analysis is introduced. Root and visible grouped child snapshots
retain local revision/digest/geometry; scene translation adds no execution edges and
is never persisted as one reviewed revision. Live state cannot resize boxes.

### 6.20 Group inputs, signatures and failure-sensitive completion gates

These contracts close the split's remaining producer and caller gaps. P3T16–P3T20
inherit the global constraints and strict behavioral RED→GREEN→refactor rule (§6.1).
Existing correct behavior requires a failing mutation probe, not a fabricated failing
implementation. No task checks a successor's acceptance box.

**P3T16 — measured live reservations and route ownership.** groupTextItems in
`domain/layout/group_geometry.go` remains the text producer. Extend its existing
vocabulary with footer `not_started`, mirrored in groupText.contract.json. Size the
OverlayBox from actual measurements of the complete simultaneously visible block,
not the maximum of one badge. Use fixed stacked rows with NodeTextGapPx: footer state,
done/active/not-started/failed progress fragments and winner; task/lane status, frozen
status and winner/non-blocking/cancel-requested badges. Reserve each row's largest
allowed measured value, including every count 0..task count and every configured task
key. Empty rows retain their reserved slot. This uses existing styles/wrap widths,
keeps every finite indicator readable, and avoids exponential combinations or guessed
character widths. Static redacted card title uses the measured `hidden dependency`
alternative; unbounded activity/details stay in GroupPanel/callouts. P3T17 draws exactly
these rows; P3T19 feeds actual live values. Zero/absent measurements must fail sizing.

Every real edge keeps its original ordinal and verification data, even when its
rendered source is the group footer. Attachment.NodeID verifies the real endpoint;
Attachment.ElementID selects the rendered box. Synthetic attachments use ElementID
and empty NodeID. Resolve ordinary node IDs with GroupElementID(nodeID,"header","").
P3T16 produces private `groupConnectorID(groupID, kind, taskKey, sourceID, targetID string)
string` using P3T15's canonical JSON tuple. For branches, use immediate ownership and
continuation edges to find sinks; nested groups are atomic at their footer. Container/
lane boxes contain their own children and are not unrelated-node obstacles. Paths may
cross their own containment outlines; unrelated lane interiors, text and node/card boxes
remain obstacles. No fictitious attachment/port field is consumed. Complete route search
scores real+synthetic blocking contacts and crossings; Refine changes their actual spacing.
P3T16 also produces `CrossingCount(geo Geometry) int` in group_geometry.go (real plus
mixed crossings) and replaces every Coordinator reviewed/unreviewed ScoreOf crossing
argument with it. P3T18 uses it for rendered crossing advisories. A route-only improvement
must affect persisted candidate ranking; counting only Geometry.Crossings is insufficient.

**P3T17 — renderer caller contract.** Extend the existing five-argument function with
one optional runtime argument; change all callers together. The return widens to the
existing xyflow Node/Edge types because it now includes group node/connector variants:

```ts
// dashboard/src/graph/arranged/buildArrangedFlow.ts
export interface ArrangedRuntime {
  groups: GroupGraph | null
  groupLive?: Record<string, GroupLiveSummary>
  nodeContexts?: Record<string, NodeContextInfo>
  layoutInstanceId?: string // scene tile identity, distinct from executing branch instance
}
export function buildArrangedFlow(
  geo: Geometry, nodeTypesById: Record<string,string>,
  executionStates: Record<string,NodeExecutionState>, graphInstanceId: string,
  activeSources?: Set<string>, runtime?: ArrangedRuntime,
): { nodes: Node[]; edges: Edge[] }
```

P3T17 also adds `groupLive?: Record<string,GroupLiveSummary>` and
`nodeContexts?: Record<string,NodeContextInfo>` to ArrangedGraphProps (groups already
exists). Compatible geometry always uses validated view.groups; the separate groups
prop is only a frozen fallback input. A group candidate without matching ownership
throws/falls back visibly; ordinary graphs retain the existing call behavior.

P3T17 owns exported `ArrangedGroupElementData` in buildArrangedFlow.ts with element:
GroupElement, contract:RendererContract, group:GroupScope, optional nodeBox:NodeBox
(header only), live:GroupLiveSummary|null, taskLive:GroupLiveTask|null,
context:NodeContextInfo|null, definitionNodeId:string, optional layoutInstanceId:string
and an unknown-valued string index.
P3T17 extends ArrangedNodeData with optional definitionNodeId:string,
instanceId:string|null, independent:boolean, hiddenDependency:boolean and
layoutInstanceId:string. It copies NodeContextInfo into these actual data fields, keeping
explicit null ownership distinct from absent static data. Ordinary nodes show fixed-size
outline/indicator/accessible descriptions for independence/hidden ownership, never a new
unmeasured text row; full details remain in GroupPanel/callouts. The selected original
(instanceId,nodeId) must survive this conversion, not fall back to root accidentally.
Existing GroupContainerData/GroupEndpointData/GroupTaskCardData gain optional
`reviewed?: ArrangedGroupElementData`; group node components consume it without changing
legacy fallback data. Baseline group paint is explicit: border-box sizing, NodeBorderPx
solid ForwardColor outline; container/lane fill #1a1a2e, header/footer/task fill #1e1e38.
Text uses §5.7's contrasting colours and declared style sizes. Runtime colour/selection
indicators may change without changing border width, adding text rows or obscuring ink.
P3T18 compares baseline computed paint against this policy before sampling pixels;
zero border/transparent card fill cannot authorize an empty raster check.
The header renders once, with original NodeBox label/type plus
its measured supplements. RoutedEdge exports `RoutedGroupConnectorData` with connector:
GroupConnector, gaps:Point[], overs:Point[], contract:RendererContract,
graphInstanceId:string, active:boolean, optional layoutInstanceId:string and the same
index signature. Existing RoutedEdgeData gains the same optional layoutInstanceId. Its data union
selects the connector via `connector in data`; no synthetic ordinal is invented.
All painted node/group/path/label components forward layoutInstanceId to their
optional data-layout-instance attribute, so P3T19's tile scope reaches actual DOM.
Group endpoint/card data preserves explicit null owner just as ordinary node data does.
P3T17 updates arrangedNodeTypes/arrangedEdgeTypes, actual handles and all real path/label
callers. Geometry is absolute; when using ReactFlow parentId, subtract the parent's
absolute origin once, never again from persisted paths/texts. GroupCrossings split gaps
on both real and synthetic paths using route keys, without SVG path recomputation.

**Actual report (P3T17 declarations/producer, P3T18 consumer).** In render.go and job.ts:
RenderedGroupText has Key:string (`key`) and Text:RenderedText (`text`).
RenderedGroupElement has ID:string (`id`), Box:Rect (`box`), Texts:[]RenderedGroupText
(`texts`), Visible:bool (`visible`), BorderColor:string (`borderColor`),
BorderPx:float64 (`borderPx`), BackgroundColor:string (`backgroundColor`). Colours,
box and visibility come from computed DOM, not geometry. RenderedGroupConnector has
ID:string, Segments:[]RenderedSegment, StrokeColor:string, DashPattern:string,
Visible:bool, ArrowPoints:[]Point, Start/End:Point, HasArrow:bool, StrokePx:float64
with existing lower-camel tags and no Ordinal. RenderedGroupCrossing has Point:Point,
Over/Under:string, UnderGapVisible/OverContinuous:bool, also lower-camel tags.
RenderReport gains GroupElements/GroupConnectors/GroupCrossings slices with respective
`groupElements,omitempty`, `groupConnectors,omitempty`, `groupCrossings,omitempty` tags.
Boolean paint fields on each element/connector are never omitempty. Ordinary reports
omit group arrays. P3T17 exports `collectGroupElements(geo:Geometry,ctx:CollectContext): RenderedGroupElement[]`,
`collectGroupConnectors(geo:Geometry,ctx:CollectContext): RenderedGroupConnector[]` and
`collectGroupCrossings(geo:Geometry,ctx:CollectContext): RenderedGroupCrossing[]` in report.ts.
They collect exact unique IDs/text/Range/SVG/style/marker data;
BrowserRenderer transports it unchanged. P3T18 checks actual outline/background ink as
well as text/paths, including container/lane decorations. Clean DOM plus valid hashes
cannot excuse erased painted elements.

**Review runtime source (P3T17).** New `src/render/groupProbes.ts` exports
`buildGroupProbeCases(groups: GroupGraph): GroupProbeCase[]`, where GroupProbeCase is
`{name:string; groupLive:Record<string,GroupLiveSummary>;
nodeContexts:Record<string,NodeContextInfo>; selectedNodeIds:ReadonlySet<string>}`.
It builds deterministic synthetic overlays from the captured configs/task keys; it
never fetches a running instance. Extend RenderSurface.SurfaceProbe with those two
maps and forward them to the same ArrangedGraph used by real routes. All static group
texts and every measured overlay alternative feed ensureBundledTextFonts on both paths.

Required names are `group:run:<state>` for not_started/launching/running/decided/gathering/
resolving_conflicts/done/unknown; `group:task:<state>` for not_started/queued/running/
waiting/paused/completed/failed/cancelled/unfinished/unknown; `group:badges`,
`group:redacted`, `group:selected-running`; and one canonical JSON name
`["group-probe","winner",groupID,taskKey]` per configured task. P3T17 produces
`GroupProbeNames(groups *workflow.GroupGraph) []string` in domain/layout/group_render.go
and a Go↔TS golden name fixture `arranged/groupProbe.contract.json`; P3T18 consumes it
rather than confusing GroupRunState with GroupTaskStatus. Each case covers all elements,
including nested groups; winner cases vary the addressed group/task and retain the rest.
The page measures actual allowed text, row bounds, visibility and paint for each case;
StateProbe booleans/issues derive from those observations. Compare static texts/routes/
markers against baseline; compare changing overlay text against the case's allowed
vocabulary/reserved rows, not against the empty static baseline. Then restore static
baseline and settle before PNG capture. Missing case/input/ink is a failure, not an
empty successful probe. P3T18 requires the exact complete probe set and tests missing,
false and runtime-only-clipping controls; actual browser tests drive these controls.

**P3T19 — child layout and scene data.** It owns these exact additions:

```ts
// api/client.ts; request<T> already accepts RequestInit
// api.instances.getLayout: (id: string, signal?: AbortSignal) => Promise<LayoutView>
// graph/useGraphLayout.ts: export the existing interfaces and pure memo body
export function computeGraphLayout(input: UseGraphLayoutInput): UseGraphLayoutOutput
// graph/arranged/composeArrangedTiles.ts
export interface LayoutTile {
  instanceId: string; definition: WorkflowDefinition; view: LayoutView|null
  groups: GroupGraph|null
  executionStates?: Record<string,NodeExecutionState>
  groupLive?: Record<string,GroupLiveSummary>
  nodeContexts?: Record<string,NodeContextInfo>
}
export function composeArrangedTiles(tiles: readonly LayoutTile[], graphInstanceId: string):
  {nodes:Node[]; edges:Edge[]; bounds:Rect}
```

The existing root hook owns root polling; useInstanceLayoutTiles receives only visible
workflow/spawn child contexts with their own grouped snapshots, not root/branch/redacted
contexts. The hook's exact return/retry contract is in P3T19. It passes AbortSignal to
the real client GET, bounds child GETs to four, aborts removed contexts and ignores late
reads/retries with per-instance revision/epoch/generation guards. Complete/idle execution
never stops active layout polling. No hook is called dynamically inside a context loop.

ArrangedGraph adds `tiles?: readonly LayoutTile[]` at P3T19 and passes its stable useId
namespace to composeArrangedTiles. The composer calls P3T17 buildArrangedFlow with each
validated view.groups and actual live/context maps. Root is translated by zero, children
follow in captured context order below preceding full extents with GroupRowGapPx;
qualify all scene IDs by canonical JSON ["tile",instanceId,logicalElementOrRouteID]
encoded as unpadded UTF-8 base64url and prefixed by the graph namespace. The DOM retains
logical data-arranged-node/group/edge IDs plus data-layout-instance for scoped assertions.
Keep layoutInstanceId distinct from executing branch `data.instanceId`/definitionNodeId
used by selectionFromNode. Text/path/gap/arrow translations are uniform and inputs remain
unchanged. Tile titles and real LayoutStatusPanel instances live in a separate navigation/
status rail outside the painted graph, so variable findings/titles do not collide with
reviewed content or require an unprovided title measurement. Missing child layout renders
only that child's explicit fallback; it cannot replace or relayout the reviewed root.

**Root and release closure (P3T20).** Production sources already wired by initDomain
are Stamp→Requester→SQLite→Worker→Coordinator→BrowserRenderer/render.html→Reviewer→Store,
Reader/QueryService→authorized HTTP and local MCP, and GraphStateReader→aggregateState.
P3T20 must drive nonempty group inputs through these actual sites and both embedded routes,
then assert immutable result/PNG evidence, live statuses, original selection and per-child
endpoint/revision geometry at the sink. Remove each group/overlay/child-layout feed in
isolated mutation probes; each corresponding sink assertion must fail. Missing required
fonts or runtime browser is an explicit retryable failure. Preserve P3T7/P3T14 sample gates.
`make test-render` currently runs the WHOLE adapter/renderreview and cmd/arbitron packages;
keep that unfiltered selection, add new files without build tags, and prove their names
appear in the required run. Do not replace it with a group-only name filter. CI runs
make test-all, including rebuilt assets and required browser/font portability gates.

## 7. Spec outcome and production evidence coverage

| Spec purpose/requirement | Owning tasks and observable completion evidence |
|---|---|
| Trace paths without manually untangling; readability over compactness | P1T3–P1T8 ordering/routing/refinement; P3T6 raster+DOM reviewer; P3T7/P3T14 real pr-review/feature-pipeline full-canvas acceptance at 0.9 |
| Every create/update authoring path, replacing manual coordinates | P2T2–P2T4 stamp/capture every service/seed path; P2T9 real serve/MCP sinks; P3T12/P3T14 remove position writes/dragging |
| Save before arrangement finishes, visible progress | P2T3 save returns while review blocked; P2T8 stages; P3T9/P3T10 responses; P3T11–P3T13 actual pending→terminal views |
| Downward sequential flow, branches beside it, distinct returns | P1T3 rank and edge classification; P1T5/P1T6 complete routes; P1T7/P3T6 blockers; P3T2/P3T3 draw persisted paths |
| Review every candidate's rendered result, refine failures | P1T9 arrange→preflight→render→review for each produced candidate, exact candidate evidence; P3T4–P3T7 actual shared renderer + screenshots |
| Five total attempts, config file limit | P2T1 YAML/default/clamp; P2T9 configured value reaches SQLite; P2T8 captured job limit; P1T9 counting/timeout/best-result tests |
| Exhaustion/failure retains best/existing presentation, issues and Retry | P1T9 best-so-far; P2T2 retained source; P2T5 truthful Reader/invalidated retry; P3T8–P3T13 authorised same-revision retry, errors/backoff |
| Same saved-revision layout in workflow and instance | P2T2 snapshot; P2T5 authoritative result/fallback; P3T8 access seams; P3T12/P3T13 revision joins; P3T14 real surface parity |
| Old review cannot replace newer saved revision/retry | P2T6 generation conditional writes; P2T7 destination lock/CAS; P2T8 fenced publication/cache settlement; P3T8 locked expected-revision retry |
| Behavior/connections unchanged | P1T1/P1T6 immutable graph and every ordinal round trip; P2T2 immutable saved snapshot; P2T8 cache-only publication; P3T3 presentation-only conversion |
| No overlap, clipping, concealed paths/endpoints or ambiguous crossings | P1T2 structural validation; P1T7 preflight; P3T4 sink report; P3T6 full raster checks and clean-report/valid-hash mutation failures |
| Crossings minimized within bounded search; unavoidable crossings distinguishable | P1T5/P1T6 complete routed search prefixes, P1T7/P3T6 ambiguous-crossing blockers, P3T2 actual gap treatment |
| Reviewed group workflows and live descendants preserve saved-revision geometry | P3T15–P3T20 captured ownership → measured v2 group elements/routes → actual DOM/raster review → SQLite → both views and independent revision tiles, including missing-input/ink negative controls |
| Untouched legacy workflows never auto-arrange | P2T3 identity writes non-triggering; P2T4 ledger; P2T8 journal-only proven recovery; P2T9 restart/backfill tests; P3T3 legacy read fallback has no writes |

A phase or task's unit/fake success is not a release gate. Completion requires the full
real-root, actual-browser, saved-result and user-surface sink checks above. Planned symbols
are closed dependencies only because their named producer tasks, callers, root injection
and failure-sensitive tests are all required; existing source is not misrepresented as
already implementing them.

## 8. Alignment review verification (2026-10-18)

Docs-only review: no implementation completion box was checked. Focused tests for
active rename/recovery, exact abandoned seed claims, acknowledged seed resume, complete
retry lock sets/same-token shadow conflicts, zero-policy geometry, settled reserved-file
reads and documented sample profiles passed in domain/workflow, domain/layout,
adapter/filesystem and adapter/config. Actual-root private/shared rename, local stdio
identity/authoring and ledger-only seed-failure resume tests passed in cmd/arbitron.

Those stdio tests currently discard non-JSON stdout, so they establish the identity/save
sink only; P2T9.8 explicitly closes the untested protocol stream. No new claim is made
for complex-sample browser readability, Docker, full suites or the planned v2 group path.
The dependency graph names existing or task-owned producers for each of those inputs;
implementation/verification still must complete the unchecked steps.
