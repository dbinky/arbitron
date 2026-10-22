# Workflow Auto-Arrangement — Phase 3 Task 6: Go readability reviewer (`renderreview.ReadabilityReviewer`)

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Execution discipline:** Expected outputs below are predictions, never recorded test results.
Where an initial run cannot compile/import a new symbol, add only its declaration and a
minimal compiling non-passing body, then rerun and observe the named behavioral assertion
fail before implementing behavior. Keep each subtask green before committing; do not commit
empty success stubs or knowingly failing tests. Test count examples are illustrative: record
the actual command/output at execution. Each data path needs an assertion at its consumer
and an absent/nil/empty/wrong-input negative control; constructor/call-count checks alone
cannot establish that the graph, status, evidence or retry was fed correctly.

**Alignment requirements:** Read contracts §6 (6.5) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Implement the production `layout.Reviewer`. It examines what the browser actually drew for one candidate (the P3T4 DOM report plus the P3T5 overview and overlapping readable-scale PNG tiles) and returns findings against every spec readability criterion. A pass is impossible unless the rendering is provably of this exact candidate, complete, and captured at practical zoom.

**Architecture:** `adapter/renderreview/reviewer.go` holds the entry point and the evidence-integrity checks: identity, contract, coverage and the screenshot files. `reviewer_checks.go` holds the readability checks over the DOM report (text, nodes, edges, crossings, runtime states). `reviewer_capture.go` holds the screenshot checks: tile seam coverage, image sizes, and whether complete text, routes, arrowheads and crossing separation are visible in the pixels. If identity or coverage fails, the result is `Rendered=false` with blocking findings. If the screenshots cannot be read, `Review` returns an error wrapping `ErrEvidenceUnavailable`, which the coordinator records as `failed` at stage `reviewing` (retryable). Readability defects are blocking findings with `Rendered=true`, so P1T8 `Refine` can respond to them.

**Tech Stack:** Go 1.25.5 (`image`, `image/png`, `image/draw`, `crypto/sha256`, `log/slog`), testify; headless Chrome for the real-browser tests (P3T5 `requireBrowser`).

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` ("Review the rendered result", Outcomes and Acceptance Criteria); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §3 ("A pass requires zero blocking findings and successful rendered review of that exact candidate with the matching renderer contract… Review the whole graph in overlapping readable-scale tiles plus an overview"); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §4. This task resolves the **reviewer half** of **PLANNED DEPENDENCY RENDER-REVIEW**. P3T5 built the renderer half. **P3T7** wires both into `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer`, which is the composition-root wiring task for this component.

**Prerequisites (merged before starting):** P1T2 (`Geometry`, `Rect`, `Point`, `GeometryDigest`, `DefaultContract`, `ContractVersion`, `FormatV1`), P1T4 (`DefaultOptions`, `MeasureRequestFor`), P1T6 (`Arrange`), P1T7 (`Finding`, criteria, severities, `BlockingCount`, `Preflight`), P1T8 (`Reviewer`, `ReviewRequest`, `Review`, `Evidence`, `Rendering`, `RenderReport`, `Artifact`, `RuntimeStates`), P1T9 (`NewCoordinator`, `Job`, `VerdictPassed`), P3T5 (package `renderreview`: `PlanTiles`, `DefaultOverlap`, `MinTileOverlapPx`, `BrowserRenderer`; test helpers `requireBrowser`, `testBrowserConfig`, `newTestRenderer`, `embeddedSurface`, `loadSampleWorkflow`).

---

## Wired-and-fed

| Input of `ReadabilityReviewer.Review` | Production producer |
|---|---|
| `ReviewRequest.Geometry` | `domain/layout/coordinator.go:(*Coordinator).Run` (P1T9) passes the candidate it just arranged with `layout.Arrange` (P1T6) from the worker's captured manifest (P2T8 `layout.Worker`) |
| `ReviewRequest.Rendering` (report + artifacts) | `adapter/renderreview/browser.go:(*BrowserRenderer).Render` (P3T5), which drives the embedded `render.html` surface (P3T4). The coordinator passes that call's own result |
| Readability thresholds (`MinReadableTextPx` 9, `MinContrastRatio` 4.5, `MinClearancePx` 4, `PracticalZoom` 0.9, `StrokePx` 2) | `domain/layout/contract.go:DefaultContract` (P1T2), carried in `Geometry.Contract` |
| Construction at the composition root | **P3T7**: `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` returns `renderreview.NewReadabilityReviewer(logger)` |

Provenance probes in this task:
- `TestReadabilityReviewer_EvidenceIsRequired` fails if the reviewer accepts an empty/nil rendering.
- `TestReadabilityReviewer_RealBrowser_PRReviewPassesThroughTheCoordinator` fails if the real surface or renderer is absent.
- P3T7's composition-root tests prove this reviewer is the one on the live path.

## Interfaces

**Consumes** (exact, from earlier tasks):

```go
// domain/layout
func GeometryDigest(g Geometry) string
func BlockingCount(fs []Finding) int
func Preflight(g Graph, geo Geometry, m Measurements) []Finding
type Reviewer interface { Review(ctx context.Context, req ReviewRequest) (Review, error) }
type ReviewRequest struct { Revision string; Attempt int; Geometry Geometry; Rendering Rendering }
type Review struct { Rendered bool; Findings []Finding; Evidence Evidence }
type Evidence struct { GeometryDigest, RendererVersion string; Contract RendererContract; FontsReady bool; Zoom float64; Tiles int; Artifacts []Artifact; ReviewedAt time.Time }
// Criterion constants: CriterionCoverage, CriterionNodeOverlap, CriterionTextCollision, CriterionTextClipped,
// CriterionTextUnreadable, CriterionMergedLines, CriterionAmbiguousAttachment, CriterionArrowDirection,
// CriterionAmbiguousCrossing, CriterionIndistinctLoop, CriterionIndistinctBranch, CriterionRenderMismatch, CriterionCrossings
// Rect methods: Right(), Bottom(), CenterX(), CenterY(), Inflate(d), Overlaps(o), Contains(o)

// adapter/renderreview (P3T5)
func PlanTiles(canvas layout.Rect, zoom float64, tileW, tileH int, overlap float64) []layout.Rect
const DefaultOverlap = 0.15
const MinTileOverlapPx = 24.0
```

**Produces** (contracts §3, package `renderreview`):

```go
var ErrEvidenceUnavailable = errors.New("renderreview: rendered evidence is unavailable")
type ReadabilityReviewer struct{ /* logger *slog.Logger; now func() time.Time */ }
func NewReadabilityReviewer(logger *slog.Logger) *ReadabilityReviewer // nil logger ⇒ slog.Default()
func (r *ReadabilityReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error)
```

Review semantics that other tasks rely on:
- **Evidence** always describes what was examined. `GeometryDigest = Rendering.GeometryDigest` (not the request's digest), `RendererVersion = Report.SurfaceVersion`, `Contract = Report.Contract`, `FontsReady = Report.FontsReady && Report.FontFamilyResolved`, `Zoom = Report.Zoom`, `Tiles` = the number of `"tile"` artifacts, `Artifacts` = a copy of `Rendering.Artifacts`, `ReviewedAt` = now in UTC. This makes the P1T9 pass rule fail on any mismatch.
- **`Rendered=false`** (never an error) when any of these hold: the rendering is for another revision/attempt/digest; the surface version, contract, zoom or canvas differ; a node, edge or crossing is missing or duplicated; a runtime state was not exercised; the overview does not show the canvas; image sizes do not match their regions; tiles leave a canvas point without `MinTileOverlapPx/2` clearance from an interior seam; a tile scale differs from the practical zoom; or a required feature is absent or unreadable in its captured tiles. All such findings use `CriterionRenderMismatch` or `CriterionCoverage`, severity blocking.
- **Errors**: a cancelled context returns an error wrapping `ctx.Err()`. Missing, non-absolute, hash-mismatched or undecodable artifact files, no overview, more than one overview, no tiles, or an unknown kind return an error wrapping `ErrEvidenceUnavailable`.
- **Findings** are `SourceReview`, in deterministic check order, and never nil (`[]layout.Finding{}` when clean). The only advisory finding is one `CriterionCrossings` entry: `"<n> crossing(s) remain in the rendered layout"`.

---

### Task 6.1: Evidence integrity — identity, coverage, artifact files

**Files:**
- Create: `adapter/renderreview/reviewer.go`
- Test:   `adapter/renderreview/reviewer_test.go`

- [x] **Step 1: Write the failing test**

Create `adapter/renderreview/reviewer_test.go`:

```go
package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var reviewClock = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// reviewGeometry is a small candidate covering every edge kind the reviewer
// inspects: a labelled forward branch (0), a right-gutter loop return (1) and a
// forward edge (2) that crosses the loop return with a recorded crossing.
func reviewGeometry() layout.Geometry {
	return layout.Geometry{
		Format:   layout.FormatV1,
		Contract: layout.DefaultContract(),
		Options:  layout.DefaultOptions(),
		Nodes: []layout.NodeBox{
			{ID: "a", Box: layout.Rect{X: 40, Y: 40, W: 200, H: 60}, Text: "Start", TypeCaption: "agent call", TextLines: 1, Rank: 0},
			{ID: "b", Box: layout.Rect{X: 40, Y: 200, W: 200, H: 60}, Text: "Review", TypeCaption: "agent call", TextLines: 1, Rank: 1},
			{ID: "c", Box: layout.Rect{X: 300, Y: 200, W: 200, H: 60}, Text: "Ship", TypeCaption: "transform", TextLines: 1, Rank: 1},
		},
		Edges: []layout.EdgeRoute{
			{
				Ordinal: 0, Source: "a", Target: "b", Output: "yes", Kind: layout.EdgeForward,
				Points:       []layout.Point{{X: 140, Y: 100}, {X: 140, Y: 200}},
				SourceAttach: layout.Attachment{NodeID: "a", Side: layout.SideBottom, Point: layout.Point{X: 140, Y: 100}},
				TargetAttach: layout.Attachment{NodeID: "b", Side: layout.SideTop, Point: layout.Point{X: 140, Y: 200}},
				Text:         &layout.LabelBox{Text: "yes", Box: layout.Rect{X: 150, Y: 136, W: 34, H: 20}, Lines: 1},
			},
			{
				Ordinal: 1, Source: "b", Target: "a", Kind: layout.EdgeLoopReturn, Side: layout.SideRight, Lane: 1,
				Points:       []layout.Point{{X: 240, Y: 230}, {X: 270, Y: 230}, {X: 270, Y: 70}, {X: 240, Y: 70}},
				SourceAttach: layout.Attachment{NodeID: "b", Side: layout.SideRight, Point: layout.Point{X: 240, Y: 230}},
				TargetAttach: layout.Attachment{NodeID: "a", Side: layout.SideRight, Point: layout.Point{X: 240, Y: 70}},
			},
			{
				Ordinal: 2, Source: "a", Target: "c", Kind: layout.EdgeForward,
				Points:       []layout.Point{{X: 200, Y: 100}, {X: 200, Y: 150}, {X: 400, Y: 150}, {X: 400, Y: 200}},
				SourceAttach: layout.Attachment{NodeID: "a", Side: layout.SideBottom, Point: layout.Point{X: 200, Y: 100}},
				TargetAttach: layout.Attachment{NodeID: "c", Side: layout.SideTop, Point: layout.Point{X: 400, Y: 200}},
			},
		},
		Crossings: []layout.Crossing{{Point: layout.Point{X: 270, Y: 150}, Over: 2, Under: 1}},
		Canvas:    layout.Rect{X: 0, Y: 0, W: 560, H: 320},
	}
}

// Passing fixtures come from recorded real shared-renderer captures, never a
// report echoed from candidate geometry or node rectangles painted by Go.
// Implement this task-owned fixture loader and TestGenerateReadabilityFixture:
// 1. testdata/readability/<GeometryDigest>/ contains geometry.json, rendering.json,
//    actual overview + all practical-zoom tiles, bundled-font fingerprint and hashes.
// 2. The generator is gated by ARBITRON_UPDATE_RENDER_FIXTURES=1, requires a real
//    browser, and draws the reviewGeometry, merged-arrivals and parallel-edge scenes
//    through BrowserRenderer using exact original workflow type maps. Measure any
//    text before setting boxes; keep each scene's expected semantic defect explicit.
// 3. newReviewFixtureFor loads by exact digest; missing/wrong/empty recordings FAIL.
//    It verifies candidate/report/hash identity, copies all PNGs to t.TempDir and
//    rewrites paths. It NEVER regenerates or invents a clean report during unit runs.
// 4. newReviewFixture calls newReviewFixtureFor(t, reviewGeometry()). The existing
//    reviewFixture fields geo/dir/tiles/req remain available to the tests below.
// 5. Keep a separate rectangles-only negative fixture and require blocking findings.
//    Stage synthetic report mutations over a captured baseline for checker-unit tests;
//    raster-only mutations preserve the original actual report and refresh PNG hashes.
// These fixture helper bodies are required implementation in reviewer_test.go;
// they are not existing package exports or pre-provided files.

func testReviewer() *ReadabilityReviewer {
	r := NewReadabilityReviewer(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return reviewClock }
	return r
}

func withCriterion(fs []layout.Finding, c layout.Criterion) []layout.Finding {
	var out []layout.Finding
	for _, f := range fs {
		if f.Criterion == c {
			out = append(out, f)
		}
	}
	return out
}

func TestReadabilityReviewer_ImplementsThePort(t *testing.T) {
	var _ layout.Reviewer = (*ReadabilityReviewer)(nil)
	r := NewReadabilityReviewer(nil)
	require.NotNil(t, r.logger, "a nil logger defaults to slog.Default()")
	require.NotNil(t, r.now)
}

func TestReadabilityReviewer_CleanRenderingPasses(t *testing.T) {
	fx := newReviewFixture(t)
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)

	assert.True(t, rv.Rendered)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "clean rendering must have no blocking findings: %+v", rv.Findings)
	require.Len(t, rv.Findings, 1, "only the advisory crossing count remains")
	assert.Equal(t, layout.CriterionCrossings, rv.Findings[0].Criterion)
	assert.Equal(t, layout.SeverityAdvisory, rv.Findings[0].Severity)
	assert.Equal(t, layout.SourceReview, rv.Findings[0].Source)
	assert.Equal(t, "1 crossing(s) remain in the rendered layout", rv.Findings[0].Evidence)

	ev := rv.Evidence
	assert.Equal(t, layout.GeometryDigest(fx.geo), ev.GeometryDigest)
	assert.Equal(t, layout.ContractVersion, ev.RendererVersion)
	assert.Equal(t, fx.geo.Contract, ev.Contract)
	assert.True(t, ev.FontsReady)
	assert.Equal(t, 0.9, ev.Zoom)
	assert.Equal(t, len(fx.tiles), ev.Tiles)
	assert.Equal(t, fx.req.Rendering.Artifacts, ev.Artifacts)
	assert.Equal(t, reviewClock, ev.ReviewedAt)
}

func TestReadabilityReviewer_EvidenceIsRequired(t *testing.T) {
	fx := newReviewFixture(t)
	req := layout.ReviewRequest{Revision: "rev-1", Attempt: 1, Geometry: fx.geo}
	rv, err := testReviewer().Review(context.Background(), req)
	require.NoError(t, err, "an empty rendering is reported in-band, never as a pass")
	assert.False(t, rv.Rendered)
	assert.NotZero(t, layout.BlockingCount(rv.Findings))
	assert.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionRenderMismatch))
	assert.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionCoverage))
	assert.Empty(t, rv.Evidence.GeometryDigest, "evidence describes what was examined: nothing")
}

func TestReadabilityReviewer_IntegrityMismatchesAreNotRendered(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(fx *reviewFixture)
		criterion layout.Criterion
		nodes     []string
		edges     []int
	}{
		{"other revision", func(fx *reviewFixture) { fx.req.Rendering.Revision = "rev-0" }, layout.CriterionRenderMismatch, nil, nil},
		{"other attempt", func(fx *reviewFixture) { fx.req.Rendering.Attempt = 2 }, layout.CriterionRenderMismatch, nil, nil},
		{"other geometry", func(fx *reviewFixture) { fx.req.Rendering.GeometryDigest = "deadbeef" }, layout.CriterionRenderMismatch, nil, nil},
		{"other surface version", func(fx *reviewFixture) { fx.req.Rendering.Report.SurfaceVersion = "arranged-renderer/0" }, layout.CriterionRenderMismatch, nil, nil},
		{"other contract", func(fx *reviewFixture) { fx.req.Rendering.Report.Contract.NodeLabelFontPx = 12 }, layout.CriterionRenderMismatch, nil, nil},
		{"other zoom", func(fx *reviewFixture) { fx.req.Rendering.Report.Zoom = 0.5 }, layout.CriterionRenderMismatch, nil, nil},
		{"other canvas", func(fx *reviewFixture) { fx.req.Rendering.Report.Canvas.H = 100 }, layout.CriterionRenderMismatch, nil, nil},
		{"missing node", func(fx *reviewFixture) {
			fx.req.Rendering.Report.Nodes = fx.req.Rendering.Report.Nodes[:2]
		}, layout.CriterionCoverage, []string{"c"}, nil},
		{"duplicated node", func(fx *reviewFixture) {
			r := &fx.req.Rendering.Report
			r.Nodes = append(r.Nodes, r.Nodes[0])
		}, layout.CriterionCoverage, []string{"a"}, nil},
		{"unknown node", func(fx *reviewFixture) { fx.req.Rendering.Report.Nodes[2].ID = "zz" }, layout.CriterionCoverage, []string{"zz"}, nil},
		{"missing edge", func(fx *reviewFixture) {
			fx.req.Rendering.Report.Edges = fx.req.Rendering.Report.Edges[:2]
		}, layout.CriterionCoverage, nil, nil},
		{"edge out of order", func(fx *reviewFixture) {
			e := fx.req.Rendering.Report.Edges
			e[0], e[1] = e[1], e[0]
		}, layout.CriterionCoverage, nil, []int{1}},
		{"missing crossing", func(fx *reviewFixture) { fx.req.Rendering.Report.Crossings = nil }, layout.CriterionCoverage, nil, nil},
		{"runtime state not exercised", func(fx *reviewFixture) {
			fx.req.Rendering.Report.States = fx.req.Rendering.Report.States[:3]
		}, layout.CriterionCoverage, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			tc.mutate(fx)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			assert.False(t, rv.Rendered, "a mismatched or incomplete rendering is not rendered evidence")
			got := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, got, "want a %s finding, got %+v", tc.criterion, rv.Findings)
			assert.Equal(t, layout.SeverityBlocking, got[0].Severity)
			assert.Equal(t, layout.SourceReview, got[0].Source)
			assert.NotEmpty(t, got[0].Evidence)
			if tc.nodes != nil {
				assert.Equal(t, tc.nodes, got[0].NodeIDs)
			}
			if tc.edges != nil {
				assert.Equal(t, tc.edges, got[0].EdgeOrdinals)
			}
		})
	}
}

func TestReadabilityReviewer_EvidenceDescribesTheExaminedRendering(t *testing.T) {
	fx := newReviewFixture(t)
	fx.req.Rendering.GeometryDigest = "deadbeef"
	fx.req.Rendering.Report.FontFamilyResolved = false
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", rv.Evidence.GeometryDigest, "evidence never claims the candidate's digest for another rendering")
	assert.False(t, rv.Evidence.FontsReady, "fonts count as ready only when the contract family resolved")
}

func TestReadabilityReviewer_UnreadableArtifactsAreAnError(t *testing.T) {
	cases := map[string]func(fx *reviewFixture){
		"missing file": func(fx *reviewFixture) {
			require.NoError(t, os.Remove(fx.req.Rendering.Artifacts[2].Path))
		},
		"hash mismatch": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].SHA256 = "00" },
		"not a png": func(fx *reviewFixture) {
			a := &fx.req.Rendering.Artifacts[1]
			require.NoError(t, os.WriteFile(a.Path, []byte("not a png"), 0o600))
			sum := sha256.Sum256([]byte("not a png"))
			a.SHA256 = hex.EncodeToString(sum[:])
		},
		"relative path": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].Path = "tile-000.png" },
		"no artifacts":  func(fx *reviewFixture) { fx.req.Rendering.Artifacts = nil },
		"no overview":   func(fx *reviewFixture) { fx.req.Rendering.Artifacts = fx.req.Rendering.Artifacts[1:] },
		"no tiles":      func(fx *reviewFixture) { fx.req.Rendering.Artifacts = fx.req.Rendering.Artifacts[:1] },
		"two overviews": func(fx *reviewFixture) {
			a := fx.req.Rendering.Artifacts
			fx.req.Rendering.Artifacts = append(a, a[0])
		},
		"unknown kind": func(fx *reviewFixture) { fx.req.Rendering.Artifacts[1].Kind = "thumbnail" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newReviewFixture(t)
			mutate(fx)
			_, err := testReviewer().Review(context.Background(), fx.req)
			require.ErrorIs(t, err, ErrEvidenceUnavailable, "unreadable screenshots are a retryable review failure, never a pass")
		})
	}
}

func TestReadabilityReviewer_Cancelled(t *testing.T) {
	fx := newReviewFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testReviewer().Review(ctx, fx.req)
	require.ErrorIs(t, err, context.Canceled)
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./adapter/renderreview/ -run 'TestReadabilityReviewer_' -v`
Expected: build failure. The output includes `undefined: NewReadabilityReviewer`, `undefined: ReadabilityReviewer` and `undefined: ErrEvidenceUnavailable`.

- [x] **Step 3: Minimal implementation**

Create `adapter/renderreview/reviewer.go`. `checkCapture`, `checkFonts`, `checkNodes`, `checkEdges`, `checkCrossings` and `checkStates` are added in Tasks 6.2 and 6.3. Implement these checks with Tasks 6.2/6.3 before any success claim. During incremental TDD a missing check returns an explicit blocking finding; it must never return empty success. Keep the complete production reviewer uncommitted until the combined integrity, DOM and raster suite is green.

```go
package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
)

// ErrEvidenceUnavailable means the rendering's screenshots cannot be examined.
// The coordinator records it as a failed (retryable) review, never a pass.
var ErrEvidenceUnavailable = errors.New("renderreview: rendered evidence is unavailable")

// ReadabilityReviewer is the production layout.Reviewer. It judges what the
// browser actually drew (the render surface's DOM report and the captured
// overview and readable-scale tiles), not the geometry's promises.
type ReadabilityReviewer struct {
	logger *slog.Logger
	now    func() time.Time
}

var _ layout.Reviewer = (*ReadabilityReviewer)(nil)

// NewReadabilityReviewer builds the reviewer. A nil logger uses slog.Default().
func NewReadabilityReviewer(logger *slog.Logger) *ReadabilityReviewer {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReadabilityReviewer{logger: logger, now: time.Now}
}

// Review examines one candidate's own rendering. A rendering that is not
// provably of this candidate, or is incomplete, is reported with
// Rendered=false. Unreadable screenshot files are an error.
func (r *ReadabilityReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	if err := ctx.Err(); err != nil {
		return layout.Review{}, fmt.Errorf("renderreview: review interrupted: %w", err)
	}
	geo, rend := req.Geometry, req.Rendering
	rep := rend.Report
	ev := layout.Evidence{
		GeometryDigest:  rend.GeometryDigest,
		RendererVersion: rep.SurfaceVersion,
		Contract:        rep.Contract,
		FontsReady:      rep.FontsReady && rep.FontFamilyResolved,
		Zoom:            rep.Zoom,
		Tiles:           countKind(rend.Artifacts, "tile"),
		Artifacts:       append([]layout.Artifact(nil), rend.Artifacts...),
		ReviewedAt:      r.now().UTC(),
	}
	f := newFindingSet()

	checkIdentity(req, f)
	checkCoverage(geo, rep, f)
	if f.blocking() > 0 {
		r.logger.Warn("layout review: rendering is not evidence for this candidate",
			"revision", req.Revision, "attempt", req.Attempt, "findings", len(f.items))
		return layout.Review{Rendered: false, Findings: f.items, Evidence: ev}, nil
	}

	overview, tiles, err := loadArtifacts(ctx, rend.Artifacts)
	if err != nil {
		return layout.Review{}, err
	}
	checkCapture(ctx, geo, rep, overview, tiles, f)
    if err := ctx.Err(); err != nil { return layout.Review{}, fmt.Errorf("renderreview: review interrupted: %w", err) }
	if f.blocking() > 0 {
		r.logger.Warn("layout review: captured screenshots do not cover the candidate",
			"revision", req.Revision, "attempt", req.Attempt, "findings", len(f.items))
		return layout.Review{Rendered: false, Findings: f.items, Evidence: ev}, nil
	}

	checkFonts(rep, f)
	checkNodes(geo, rep, f)
	checkEdges(geo, rep, f)
	checkCrossings(geo, rep, f)
	checkStates(rep, f)
	return layout.Review{Rendered: true, Findings: f.items, Evidence: ev}, nil
}

// findingSet accumulates review findings in check order.
type findingSet struct{ items []layout.Finding }

func newFindingSet() *findingSet { return &findingSet{items: []layout.Finding{}} }

func (s *findingSet) add(c layout.Criterion, sev layout.Severity, nodes []string, edges []int, format string, args ...any) {
	s.items = append(s.items, layout.Finding{
		Criterion: c, Severity: sev, Source: layout.SourceReview,
		NodeIDs: nodes, EdgeOrdinals: edges, Evidence: fmt.Sprintf(format, args...),
	})
}

func (s *findingSet) block(c layout.Criterion, nodes []string, edges []int, format string, args ...any) {
	s.add(c, layout.SeverityBlocking, nodes, edges, format, args...)
}

func (s *findingSet) blocking() int { return layout.BlockingCount(s.items) }

func countKind(arts []layout.Artifact, kind string) int {
	n := 0
	for _, a := range arts {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

// checkIdentity proves the rendering is of this exact candidate, drawn by the
// same renderer contract at the practical zoom.
func checkIdentity(req layout.ReviewRequest, f *findingSet) {
	geo, rend, rep := req.Geometry, req.Rendering, req.Rendering.Report
	if rend.Revision != req.Revision || rend.Attempt != req.Attempt {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendering belongs to revision %q attempt %d; review requested revision %q attempt %d",
			rend.Revision, rend.Attempt, req.Revision, req.Attempt)
	}
	if want := layout.GeometryDigest(geo); rend.GeometryDigest != want {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendering is of geometry %q; the candidate under review is %q", rend.GeometryDigest, want)
	}
	if rep.SurfaceVersion != layout.ContractVersion {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"render surface version %q does not match renderer contract %q", rep.SurfaceVersion, layout.ContractVersion)
	}
	if rep.Contract != geo.Contract {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"render surface used a different renderer contract than the candidate (%q)", geo.Contract.Version)
	}
	if rep.Zoom != geo.Contract.PracticalZoom {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendered at zoom %.3f; readable review requires the practical zoom %.2f", rep.Zoom, geo.Contract.PracticalZoom)
	}
	if rep.Canvas != geo.Canvas {
		f.block(layout.CriterionRenderMismatch, nil, nil,
			"rendered canvas %s differs from the candidate canvas %s", fmtRect(rep.Canvas), fmtRect(geo.Canvas))
	}
}

// checkCoverage proves every node, edge occurrence, crossing and runtime state
// of the candidate was drawn exactly once.
func checkCoverage(geo layout.Geometry, rep layout.RenderReport, f *findingSet) {
	want := make(map[string]bool, len(geo.Nodes))
	for _, n := range geo.Nodes {
		want[n.ID] = true
	}
	seen := make(map[string]int, len(rep.Nodes))
	for _, n := range rep.Nodes {
		seen[n.ID]++
		if !want[n.ID] {
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "render report contains node %q, which is not in the candidate", n.ID)
		}
	}
	for _, n := range geo.Nodes {
		switch c := seen[n.ID]; c {
		case 1:
		case 0:
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "node %q was not drawn", n.ID)
		default:
			f.block(layout.CriterionCoverage, []string{n.ID}, nil, "node %q was drawn %d times", n.ID, c)
		}
	}
	if len(rep.Edges) != len(geo.Edges) {
		f.block(layout.CriterionCoverage, nil, nil, "render report has %d edges; the candidate has %d edge occurrences", len(rep.Edges), len(geo.Edges))
	}
	for i, e := range rep.Edges {
		if e.Ordinal != i {
			f.block(layout.CriterionCoverage, nil, []int{e.Ordinal}, "edge report %d carries ordinal %d; edges must be reported in manifest order", i, e.Ordinal)
		}
	}
	if len(rep.Crossings) != len(geo.Crossings) {
		f.block(layout.CriterionCoverage, nil, nil, "render report has %d crossings; the candidate records %d", len(rep.Crossings), len(geo.Crossings))
	}
	probed := make(map[string]bool, len(rep.States))
	for _, s := range rep.States {
		probed[s.State] = true
	}
	for _, s := range layout.RuntimeStates {
		if !probed[s] {
			f.block(layout.CriterionCoverage, nil, nil, "runtime state %q was not exercised by the rendering", s)
		}
	}
}

// shot is one decoded screenshot artifact.
type shot struct {
	art layout.Artifact
	img image.Image
}

// loadArtifacts reads, verifies and decodes the overview and the tiles.
func loadArtifacts(ctx context.Context, arts []layout.Artifact) (shot, []shot, error) {
	var overview *shot
	var tiles []shot
	for _, a := range arts {
		if err := ctx.Err(); err != nil {
			return shot{}, nil, fmt.Errorf("renderreview: review interrupted: %w", err)
		}
		s, err := loadArtifact(a)
		if err != nil {
			return shot{}, nil, err
		}
		switch a.Kind {
		case "overview":
			if overview != nil {
				return shot{}, nil, fmt.Errorf("%w: rendering has more than one overview", ErrEvidenceUnavailable)
			}
			overview = &s
		case "tile":
			tiles = append(tiles, s)
		default:
			return shot{}, nil, fmt.Errorf("%w: unknown artifact kind %q (%s)", ErrEvidenceUnavailable, a.Kind, a.Path)
		}
	}
	if overview == nil {
		return shot{}, nil, fmt.Errorf("%w: rendering has no overview screenshot", ErrEvidenceUnavailable)
	}
	if len(tiles) == 0 {
		return shot{}, nil, fmt.Errorf("%w: rendering has no readable-scale tiles", ErrEvidenceUnavailable)
	}
	return *overview, tiles, nil
}

func loadArtifact(a layout.Artifact) (shot, error) {
	if !filepath.IsAbs(a.Path) {
		return shot{}, fmt.Errorf("%w: artifact path %q is not absolute", ErrEvidenceUnavailable, a.Path)
	}
	raw, err := os.ReadFile(a.Path)
	if err != nil {
		return shot{}, fmt.Errorf("%w: %v", ErrEvidenceUnavailable, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != a.SHA256 {
		return shot{}, fmt.Errorf("%w: %s has sha256 %s; the rendering recorded %s", ErrEvidenceUnavailable, a.Path, got, a.SHA256)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return shot{}, fmt.Errorf("%w: %s is not a PNG: %v", ErrEvidenceUnavailable, a.Path, err)
	}
	return shot{art: a, img: img}, nil
}

func fmtRect(r layout.Rect) string {
	return fmt.Sprintf("(%.1f,%.1f %.1f×%.1f)", r.X, r.Y, r.W, r.H)
}

// No empty success stubs are permitted. Supply the real check bodies from
// Tasks 6.2/6.3 before committing the production reviewer.

```

- [x] **Step 4:** Run `go test ./adapter/renderreview -run TestReadabilityReviewer_ -count=1 -v`.
  Capture the intended failing integrity assertions against the minimal compiling reviewer,
  then implement. Any still-pending DOM/raster check keeps the clean-render test failing.
- [x] **Step 5:** Defer the production commit until Tasks 6.2 and 6.3 are green together.
  An isolated subset of integrity tests is not a complete reviewer or a passing commit.

---

### Task 6.2: DOM readability checks — text, nodes, edges, crossings, runtime states

**Files:**
- Create: `adapter/renderreview/reviewer_checks.go`
- Modify: `adapter/renderreview/reviewer.go` (implement the real font/node/edge/crossing/state checks; checkCapture is implemented with Task 6.3 before the combined commit)
- Test:   `adapter/renderreview/reviewer_checks_test.go`

**Interfaces:** Consumes `findingSet`, `fmtRect`, `reviewFixture`, `newReviewFixture`, `newReviewFixtureFor`, `reviewGeometry`, `testReviewer` and `withCriterion` from Task 6.1. Produces the real bodies of `checkFonts`, `checkNodes`, `checkEdges`, `checkCrossings` and `checkStates`, plus `checkText`, `near`, `rectNear` and `onBorder`.

Rules (all blocking unless noted; tolerances `boxTolerancePx = 1.5`, `endpointTolerancePx = 2`, `borderTolerancePx = 3`):

| Rendered defect | Criterion | Implicated |
|---|---|---|
| Contract font not loaded / family not resolved | `text_unreadable` | — |
| Label/type/branch text clipped | `text_clipped` | node, or edge ends + ordinal |
| Text occluded by another element | `text_collision` | same |
| `fontPx × zoom < MinReadableTextPx` | `text_unreadable` | same |
| `contrast < MinContrastRatio` | `text_unreadable` | same |
| Node box drawn > 1.5px from its reviewed box | `render_mismatch` | node |
| Rendered node boxes overlap (inflated by `MinClearancePx/2`) | `node_overlap` | both nodes |
| No arrowhead | `arrow_direction` | edge |
| Start/end > 2px from the reviewed route endpoints | `ambiguous_attachment` | edge |
| Start not on its source side | `ambiguous_attachment` | edge |
| End not on its target side | `arrow_direction` | edge |
| Loop return or self loop drawn solid, or forward edge drawn dashed | `indistinct_loop_return` | edge |
| Stroke narrower than `StrokePx − 0.25` | `render_mismatch` | edge |
| Branch label not drawn | `indistinct_branch` | edge |
| Branch label centre outside its reviewed label box (±1.5px) | `indistinct_branch` | edge |
| Label drawn on an edge that has none | `render_mismatch` | edge |
| Two edges into the same node end within 2px of each other (or two out of the same node start within 2px) | `ambiguous_attachment` | the node + both ordinals |
| Crossing without a visible gap under, or with an interrupted over edge | `ambiguous_crossing` | `[over, under]` |
| Any remaining crossings | advisory `crossings` (one finding) | — |
| Runtime state moves boxes/endpoints or loses an arrowhead | `render_mismatch` | — |
| Runtime state makes labels unreadable | `text_unreadable` | — |

- [x] **Step 1: Write the failing test**

Create `adapter/renderreview/reviewer_checks_test.go`:

```go
package renderreview

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_VisualOnlyDefectsBlock(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(r *layout.RenderReport)
		criterion layout.Criterion
		nodes     []string
		edges     []int
	}{
		{"fonts not loaded", func(r *layout.RenderReport) { r.FontsReady = false }, layout.CriterionTextUnreadable, nil, nil},
		{"fallback font family", func(r *layout.RenderReport) { r.FontFamilyResolved = false }, layout.CriterionTextUnreadable, nil, nil},
		{"node label clipped", func(r *layout.RenderReport) { r.Nodes[1].Label.Clipped = true }, layout.CriterionTextClipped, []string{"b"}, nil},
		{"type caption clipped", func(r *layout.RenderReport) { r.Nodes[2].Type.Clipped = true }, layout.CriterionTextClipped, []string{"c"}, nil},
		{"node label occluded", func(r *layout.RenderReport) { r.Nodes[0].Label.Occluded = true }, layout.CriterionTextCollision, []string{"a"}, nil},
		{"node label too small at practical zoom", func(r *layout.RenderReport) { r.Nodes[0].Label.FontPx = 9 }, layout.CriterionTextUnreadable, []string{"a"}, nil},
		{"node label not laid out", func(r *layout.RenderReport) { r.Nodes[0].Label.FontPx = 0 }, layout.CriterionTextUnreadable, []string{"a"}, nil},
		{"low contrast caption", func(r *layout.RenderReport) { r.Nodes[1].Type.Contrast = 3.2 }, layout.CriterionTextUnreadable, []string{"b"}, nil},
		{"branch label clipped", func(r *layout.RenderReport) { r.Edges[0].Label.Clipped = true }, layout.CriterionTextClipped, []string{"a", "b"}, []int{0}},
		{"branch label low contrast", func(r *layout.RenderReport) { r.Edges[0].Label.Contrast = 2 }, layout.CriterionTextUnreadable, []string{"a", "b"}, []int{0}},
		{"branch label missing", func(r *layout.RenderReport) { r.Edges[0].Label = nil }, layout.CriterionIndistinctBranch, []string{"a", "b"}, []int{0}},
		{"branch label drawn away from its edge", func(r *layout.RenderReport) { r.Edges[0].Label.Box.X += 120 }, layout.CriterionIndistinctBranch, []string{"a", "b"}, []int{0}},
		{"label on an unlabelled edge", func(r *layout.RenderReport) {
			l := layout.RenderedText{Box: layout.Rect{X: 300, Y: 140, W: 30, H: 16}, FontPx: 11, Contrast: 9}
			r.Edges[2].Label = &l
		}, layout.CriterionRenderMismatch, []string{"a", "c"}, []int{2}},
		{"node box resized by the renderer", func(r *layout.RenderReport) { r.Nodes[2].Box.W = 160 }, layout.CriterionRenderMismatch, []string{"c"}, nil},
		{"rendered nodes overlap", func(r *layout.RenderReport) { r.Nodes[2].Box.X = 200 }, layout.CriterionNodeOverlap, []string{"b", "c"}, nil},
		{"missing arrowhead", func(r *layout.RenderReport) { r.Edges[2].HasArrow = false }, layout.CriterionArrowDirection, []string{"a", "c"}, []int{2}},
		{"edge starts away from its route", func(r *layout.RenderReport) { r.Edges[2].Start.X += 30 }, layout.CriterionAmbiguousAttachment, []string{"a", "c"}, []int{2}},
		{"edge ends inside its target", func(r *layout.RenderReport) {
			r.Edges[0].End.Y += 20
		}, layout.CriterionArrowDirection, []string{"a", "b"}, []int{0}},
		{"loop return drawn solid", func(r *layout.RenderReport) { r.Edges[1].Dashed = false }, layout.CriterionIndistinctLoop, []string{"b", "a"}, []int{1}},
		{"forward edge drawn dashed", func(r *layout.RenderReport) { r.Edges[2].Dashed = true }, layout.CriterionIndistinctLoop, []string{"a", "c"}, []int{2}},
		{"hairline stroke", func(r *layout.RenderReport) { r.Edges[2].StrokePx = 1 }, layout.CriterionRenderMismatch, []string{"a", "c"}, []int{2}},
		{"crossing reads as a junction", func(r *layout.RenderReport) { r.Crossings[0].UnderGapVisible = false }, layout.CriterionAmbiguousCrossing, nil, []int{2, 1}},
		{"over edge interrupted", func(r *layout.RenderReport) { r.Crossings[0].OverContinuous = false }, layout.CriterionAmbiguousCrossing, nil, []int{2, 1}},
		{"running glow moves endpoints", func(r *layout.RenderReport) {
			r.States[1] = layout.StateProbe{State: "running", NodeBoxesStable: false, LabelsReadable: true, Issues: []string{"node b grew 4px"}}
		}, layout.CriterionRenderMismatch, nil, nil},
		{"selection hides labels", func(r *layout.RenderReport) {
			r.States[6] = layout.StateProbe{State: "selected", NodeBoxesStable: true, LabelsReadable: false, Issues: []string{"node a label contrast 2.10 < 4.5"}}
		}, layout.CriterionTextUnreadable, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newReviewFixture(t)
			tc.mutate(&fx.req.Rendering.Report)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			// Readability findings may set Rendered=false when required captured features are absent.
            assert.NotZero(t, layout.BlockingCount(rv.Findings))
			got := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, got, "want a %s finding, got %+v", tc.criterion, rv.Findings)
			assert.Equal(t, layout.SeverityBlocking, got[0].Severity)
			assert.Equal(t, layout.SourceReview, got[0].Source)
			assert.NotEmpty(t, got[0].Evidence)
			assert.Equal(t, tc.nodes, got[0].NodeIDs)
			assert.Equal(t, tc.edges, got[0].EdgeOrdinals)
		})
	}
}

func TestReadabilityReviewer_ReadableBoundaryIsInclusive(t *testing.T) {
	fx := newReviewFixture(t)
	r := &fx.req.Rendering.Report
	r.Nodes[0].Type.FontPx = 10 // 10 × 0.9 = 9px: exactly the minimum
	r.Nodes[0].Type.Contrast = 4.5
	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
}

func TestReadabilityReviewer_MergedArrivalsConcealDestinations(t *testing.T) {
	geo := reviewGeometry()
	// Edge 2 now also enters b, at exactly the point edge 0 uses.
	geo.Edges[2].Target = "b"
	geo.Edges[2].Points = []layout.Point{{X: 200, Y: 100}, {X: 200, Y: 150}, {X: 140, Y: 150}, {X: 140, Y: 200}}
	geo.Edges[2].TargetAttach = layout.Attachment{NodeID: "b", Side: layout.SideTop, Point: layout.Point{X: 140, Y: 200}}
	geo.Crossings = nil
	fx := newReviewFixtureFor(t, geo)

	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	got := withCriterion(rv.Findings, layout.CriterionAmbiguousAttachment)
	require.Len(t, got, 1, "%+v", rv.Findings)
	assert.Equal(t, []string{"b"}, got[0].NodeIDs)
	assert.Equal(t, []int{0, 2}, got[0].EdgeOrdinals)
	assert.Empty(t, withCriterion(rv.Findings, layout.CriterionCrossings), "no crossings, no advisory finding")
}

func TestReadabilityReviewer_ParallelEdgesWithOwnPortsAreFine(t *testing.T) {
	geo := reviewGeometry()
	// Two identical parallel edges a→b, each with its own port: traceable.
	dup := geo.Edges[0]
	dup.Ordinal = 3
	dup.Output = ""
	dup.Text = nil
	dup.Points = []layout.Point{{X: 110, Y: 100}, {X: 110, Y: 200}}
	dup.SourceAttach.Point = layout.Point{X: 110, Y: 100}
	dup.TargetAttach.Point = layout.Point{X: 110, Y: 200}
	geo.Edges = append(geo.Edges, dup)
	fx := newReviewFixtureFor(t, geo)

	rv, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
}

func TestReadabilityReviewer_FindingsAreDeterministic(t *testing.T) {
	fx := newReviewFixture(t)
	r := &fx.req.Rendering.Report
	r.Nodes[1].Label.Clipped = true
	r.Edges[1].Dashed = false
	r.Crossings[0].UnderGapVisible = false
	first, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	second, err := testReviewer().Review(context.Background(), fx.req)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	for _, f := range first.Findings {
		assert.Equal(t, layout.SourceReview, f.Source)
	}
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./adapter/renderreview/ -run 'TestReadabilityReviewer_(VisualOnlyDefectsBlock|ReadableBoundaryIsInclusive|MergedArrivalsConcealDestinations|ParallelEdgesWithOwnPortsAreFine|FindingsAreDeterministic|CleanRenderingPasses)' -v`
Expected: the named defect assertions fail against minimal compiling behavior. Observe actual RED failures, implement each check, and keep clean real-capture controls failing until every DOM and raster requirement is implemented. Empty-success stubs are forbidden.

- [x] **Step 3: Minimal implementation**

Implement the font/node/edge/crossing/state checks in reviewer_checks.go.

Create `adapter/renderreview/reviewer_checks.go`:

Implement reviewer_checks.go in criterion order with finite/shape guards before
indexing. No endpoint-only success path is sufficient:

1. checkFonts validates exact bundled contract/readiness, actual used ranges/weights and
   supported glyph sequences. Missing coverage is text_unreadable/render_mismatch.
2. checkText checks Visible, nonempty actual Content/Glyphs, clipping/occlusion, finite
   fontPx/contrast, minimum readable zoom and contrast thresholds (inclusive boundaries).
   checkNodes compares full Content with NodeBox.Text/TypeCaption and actual box (1.5px),
   then checks actual node overlap and glyph containment/collisions.
3. checkEdges compares ALL actual Segments to the expected crossing-gap-split polyline,
   endpoint/attachment sides, StrokeColor/DashPattern/Visible/stroke width, and transformed
   ArrowPoints against the final segment and target. Nil/empty paths and wrong middle
   paths with unchanged endpoints fail. HasArrow alone cannot establish direction.
4. Check full branch text, exact label box (1.5px), glyph containment, proximity to its own
   route and no other closer edge; inspect route/text and route/unrelated-node collisions
   from actual segments. Branch labels must stay beside their own paths (LabelGap), never
   hide their own route. Parallel routes/arrivals remain individually traceable.
5. checkCrossings verifies exact ordinal pair/location against Geometry, actual required
   gap neighborhood and continuous over path. No count-only or natural-dash-hole proof.
   Advisory crossing count is emitted only after distinguishable treatment succeeds.
6. checkStates rejects duplicate/unknown/missing required states and consumes the complete
   phase-aware stability/readability probe. Running+selected combinations cannot obscure
   text/arrows. Any issue yields deterministic SourceReview blocking findings with IDs
   and ordinals; retain advisory crossings only for unavoidable distinguishable crossings.

Use the defect-to-criterion table above, boxTolerancePx=1.5, endpointTolerancePx=2,
borderTolerancePx=3, strokeTolerancePx=.25 and finite checks throughout. Helpers near,
rectNear/onBorder must reject non-finite values. Add the full internal-segment/marker/
content/visibility/duplicate/collision rejection matrix to Step 1 before implementing.

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./adapter/renderreview/ -run 'TestReadabilityReviewer_' -v`
Run with Task 6.3 complete: every integrity, DOM and raster test passes. Until then report unfinished checks; a filtered green subset cannot count as reviewer completion.

- [x] **Step 5:** Commit these source/tests with Task 6.3 only after the combined suite is green; no incomplete production reviewer commit.

---

### Task 6.3: Screenshot evidence of text, routes, arrows and crossing separation

**Files:** Create `adapter/renderreview/reviewer_capture.go` and
`reviewer_capture_test.go`; modify `reviewer.go` to call the real `checkCapture`.
Consumes the DOM fields produced by P3T4 and immutable PNGs from P3T5, with the exact
thresholds and evidence rules in contracts §6.5. This replaces the node-colour-count
shortcut: a scene made only of filled node rectangles cannot pass visual review.

- [x] **Step 1 (RED):** Add table-driven `TestReviewCapture_RequiresVisibleFeatures`.
  Start from a real-browser small branch/parallel-edge/loop/crossing capture. For each
  case alter the PNGs, recompute their SHA256, keep the report clean, then call the
  production `ReadabilityReviewer.Review`. Remove (a) one route's middle, (b) an arrow,
  (c) label ink while preserving its node border/background, (d) a crossing gap,
  (e) one parallel lane. Assert blocking merged_lines/arrow_direction/text_unreadable/
  ambiguous_crossing/coverage findings with implicated IDs/ordinals and no pass.
  Include the previous rectangles-only fixture as an explicit rejection. Add success
  controls for solid paths, dashed returns, antialiasing and a feature crossing a tile
  seam; every original real capture must pass. Keep missing-file/hash/error, wrong-scale,
  abutting/missing-tile and cancellation tests from Tasks 6.1/6.2. Use the real `BrowserRenderer`
  to produce passing fixtures; no fake report/rectangle scene is sufficient for success.
- [x] **Step 2:** Run `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run
  'TestReviewCapture|TestReadabilityReviewer' -count=1 -v`; observe the missing-feature
  cases fail against the old two-colour check, while the real-capture controls pass.
- [x] **Step 3 (GREEN):** Implement `checkCapture(ctx context.Context, geo layout.Geometry, rep layout.RenderReport, overview shot, tiles []shot, f *findingSet)` in `reviewer_capture.go`: retain full
  overview, finite image-size, overlap/coverage and practical-zoom checks, then perform
  the 2px corridor/glyph/arrow/gap checks in contracts §6.5 over decoded tiles. Check
  context in tile and feature loops. Missing evidence is an ErrEvidenceUnavailable error;
  visible defects produce SourceReview blocking findings tied to this exact candidate.
  A measurement report alone cannot override absent pixel evidence.
- [x] **Step 4:** Rerun the command to GREEN; temporarily bypass the feature checks and
  confirm the negative cases fail, then restore the verified implementation.
- [x] **Step 5:** Commit the implementation and focused tests with the Task 6.1/6.2
  fixture changes. Preserve real-browser Task 6.4 below as an independent production check.

### Task 6.4: Real-browser review — pr-review passes, visual-only clipping is caught

**Files:**
- Create: `adapter/renderreview/reviewer_browser_test.go`

**Interfaces:** Consumes the P3T5 test helpers `requireBrowser`, `testBrowserConfig(t, assets) BrowserConfig`, `newTestRenderer(t, cfg) *BrowserRenderer`, `embeddedSurface(t) fs.FS` and `loadSampleWorkflow(t, name) *workflow.Workflow` (same package). It also uses `withCriterion` (Task 6.1); `layout.NewCoordinator`, `layout.Job` and `layout.VerdictPassed` (P1T9); and `layout.Preflight` (P1T7).

These are the provenance probes for the real producer. They fail when the embedded `render.html` is missing or reports nothing, when the browser renderer is not what feeds the reviewer, or when the reviewer cannot see a defect that geometric preflight misses.

- [x] **Step 1: Write the tests**

Create `adapter/renderreview/reviewer_browser_test.go`:

```go
package renderreview

import (
	"context"
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bounded coordinator, driven by the real browser renderer and this
// reviewer, passes the simple acceptance sample at practical zoom. Its
// evidence belongs to the returned candidate.
func TestReadabilityReviewer_RealBrowser_PRReviewPassesThroughTheCoordinator(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	coord, err := layout.NewCoordinator(b, NewReadabilityReviewer(nil))
	require.NoError(t, err)

	res := coord.Run(context.Background(), layout.Job{Revision: "rev-pr-review", Manifest: loadSampleWorkflow(t, "pr-review"), MaxAttempts: 5}, nil)
	require.Nil(t, res.Failure, "failure: %+v", res.Failure)
	require.Equal(t, layout.VerdictPassed, res.Verdict, "unresolved findings: %+v", res.Findings)
	require.NotNil(t, res.Best)
	require.NotNil(t, res.Best.Review)
	assert.LessOrEqual(t, res.AttemptsUsed, 5)
	assert.False(t, res.Unreviewed)

	rv := res.Best.Review
	assert.True(t, rv.Rendered)
	assert.Zero(t, layout.BlockingCount(rv.Findings))
	assert.Equal(t, layout.GeometryDigest(res.Best.Geometry), rv.Evidence.GeometryDigest)
	assert.Equal(t, res.Best.Geometry.Contract, rv.Evidence.Contract)
	assert.True(t, rv.Evidence.FontsReady)
	assert.Equal(t, 0.9, rv.Evidence.Zoom)
	assert.GreaterOrEqual(t, rv.Evidence.Tiles, 1)
	require.NotEmpty(t, rv.Evidence.Artifacts)
	assert.Equal(t, "overview", rv.Evidence.Artifacts[0].Kind)
}

// understate keeps real widths but claims every wrapped text fits on one line.
// Geometry built from it is internally consistent, so preflight trusts it.
func understate(m layout.Measurements, c layout.RendererContract) layout.Measurements {
	out := layout.Measurements{Contract: m.Contract, FontsReady: m.FontsReady, Items: make(map[string]layout.TextMetrics, len(m.Items))}
	for k, v := range m.Items {
		if v.Lines > 1 {
			v.Lines = 1
			v.Height = math.Ceil(c.NodeLabelFontPx * c.LineHeight)
		}
		out.Items[k] = v
	}
	return out
}

// Design Phase 1 "Failure": injected visual-only clipping prevents a pass
// even when geometric checks pass.
func TestReadabilityReviewer_RealBrowser_CatchesVisualOnlyClipping(t *testing.T) {
	ctx := context.Background()
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	long := "Überprüfe sämtliche Änderungen im Pull-Request und bestätige jede einzelne Abhängigkeit gründlich, bevor du fortfährst"
	wf := &workflow.Workflow{
		Name: "visual-clip",
		Nodes: []workflow.Node{
			{ID: "n1", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: long},
			{ID: "n2", Type: workflow.NodeTypeTransform, Label: "Done"},
		},
		Edges: []workflow.Edge{{Source: "n1", Target: "n2"}},
	}
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	real, err := b.Measure(ctx, layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	require.Greater(t, real.Items[layout.NodeLabelKey("n1")].Lines, 1, "the long label really wraps in the browser")

	lying := understate(real, c)
	geo, err := layout.Arrange(g, lying, c, layout.DefaultOptions())
	require.NoError(t, err)
	assert.Empty(t, withCriterion(layout.Preflight(g, geo, lying), layout.CriterionTextClipped),
		"geometric preflight trusts the measurements it was given")

	rend, err := b.Render(ctx, layout.NewRenderRequest("rev-clip", 1, geo, map[string]string{"n1": "transform", "n2": "transform"}))
	require.NoError(t, err)
	rv, err := NewReadabilityReviewer(nil).Review(ctx, layout.ReviewRequest{Revision: "rev-clip", Attempt: 1, Geometry: geo, Rendering: rend})
	require.NoError(t, err)
	assert.True(t, rv.Rendered)
	clipped := withCriterion(rv.Findings, layout.CriterionTextClipped)
	require.NotEmpty(t, clipped, "the real rendering clips the long label: %+v", rv.Findings)
	assert.Equal(t, []string{"n1"}, clipped[0].NodeIDs)
	assert.NotZero(t, layout.BlockingCount(rv.Findings), "a clipped label can never pass")
}
```

- [x] **Step 2: Run them**

Run: `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ -run 'TestReadabilityReviewer_RealBrowser' -count=1 -v -timeout 20m`
Expected, on a machine with Chrome/Chromium and a `make ui` build that contains `render.html`: both tests `--- PASS`; `ok`.

Without a browser, the same command fails with `ARBITRON_REQUIRE_BROWSER=1 but no browser is available`. Without `ARBITRON_REQUIRE_BROWSER=1`, the tests are skipped with `no Chrome/Chromium available`.

If `PRReviewPassesThroughTheCoordinator` fails with unresolved findings, the failure message lists them. Fix the renderer or arranger defect it names (P1T6/P3T2/P3T4) rather than loosening a reviewer rule. The spec requires pr-review to pass.

- [x] **Step 3: Run the whole package under the race detector**

Run: `go test -race ./adapter/renderreview/ -count=1`
Expected: `ok  	github.com/arbitron/arbitron/adapter/renderreview`; no `WARNING: DATA RACE`.

- [x] **Step 4: Commit**

```bash
git add adapter/renderreview/reviewer_browser_test.go
git commit -m "test(renderreview): real-browser readability review of pr-review and visual-only clipping" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

**Next:** P3T7 installs `NewReadabilityReviewer(logger)` together with the browser renderer at `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer`. It then asserts `passed` for pr-review, feature-pipeline and ralph-loop through the real composition root.

### Alignment prerequisite: replace the old synthetic success fixture

Task 6.1 owns the real-capture fixture loader/generator. A node-rectangle scene is a negative test; no success fixture may use synthetic cleanReport or writeShot.
Create `adapter/renderreview/testdata/readability/` with Geometry, actual RenderReport and
PNG artifacts produced by the real BrowserRenderer for the small branch/loop/crossing
scene. A fixture-generation test named `TestGenerateReadabilityFixture` runs only when
`ARBITRON_UPDATE_RENDER_FIXTURES=1`, requires a real browser, records the candidate digest,
contract, report and artifact hashes, and writes the complete scene. `newReviewFixture`
loads/copies those immutable artifacts into t.TempDir and rewrites absolute paths; it
does not echo expected geometry into the report. Keep normal integrity/unit tests
browser-independent by reading these real captures. P3T6 Task 6.4 and P3T7 still generate
fresh real captures to catch renderer drift; checked-in fixtures alone are insufficient.
The generator and fixture loading tests are owned by this task, not an unprovided tool.

### Full rendered-path checks (part of Task 6.2, before GREEN)

Implement these in the checkEdges/checkNodes bodies above before declaring them complete:
compare every RenderedSegment with the expected gap-split route, including internal bends
and disjoint M subpaths; verify StrokeColor/DashPattern/Visible and actual ArrowPoints
against the last segment and target boundary. A positive HasArrow alone is insufficient.
Check label box/glyph containment, actual text equality, text-text and route-text collisions,
and each actual segment through unrelated actual node boxes. Compare crossing reports by
exact ordinal pair/location, not only count. Reject duplicate/unknown runtime states and
non-finite report rectangles/points/metrics before indexing or geometric comparisons.
The capture checker verifies actual glyph ink/complete arrows/route corridors and gap
regions across all tiles; do not skip route pixels inside an opaque branch-label box.
P1T6 places branch labels beside routes, and covering one's own route remains a defect.
Write RED cases for same endpoints/different middle, reversed marker, wrong-but-nonempty
text, empty glyphs, duplicate crossing/state and valid-hash PNG missing each visible feature.

## Pass execution evidence and self-review (2026-10-17)

- Implemented the combined reviewer, never committing an incomplete integrity/DOM/raster subset. Recorded real Chrome fixtures (geometry/report/measurements/font fingerprint and WOFF2 hashes/overview/all tiles) with explicit gated generation. Normal tests load exact digests and verify original PNG hashes; they never synthesize a passing report.
- Observed behavioral RED against the compiling blocking reviewer: clean evidence, identity/evidence fields, visual criteria, missing/unreadable artifact errors and cancellation assertions failed. Additional RED cycles exposed absent pixel features, bright rectangle substitutes, arrow wings, low-contrast ink, invalid candidates, cross-node collisions, missing dash evidence and runtime finding IDs before their corrections.
- Corrected the example fixture's invalid loop side ports to the existing bottom/top contract; measured all labels/captions and checked their boxes before recording. Kept the merged-arrival scene deliberately invalid semantically and asserted its blocking findings.
- The fresh loop/crossing capture exposed a real producer defect: Chrome computes SVG dashes as `6px, 4px`; `strokeAt` previously used Number and rejected them. The collector now parses finite CSS-pixel lengths, and `make ui` re-embeds the correction. Reverting that parser and rebuilding makes `TestReadabilityReviewer_RealBrowser_LoopCrossingAndSeam` fail with unsupported dash pattern; restored and rebuilt.
- Binding §6.5 overrides the older clipping example's Rendered=true assertion: the actual long Unicode label has incomplete captured glyph ink, so this test requires Rendered=false as well as implicated text_clipped findings. No missing pixel evidence is accepted as a completed render.
- **Spec compliance first:** full exact identity/coverage/contract, actual content/glyphs/segments/styles/marker outline, text/route/node collisions, branch clearance, crossings, runtime states and evidence error semantics; no new configuration or production factory override. Production installation stays with its assigned P3T7 task.
- **Anti-green-theater second:** actual sample → browser → coordinator → reviewer passes; actual ancestor opacity, hidden/reversed marker, changed text and changed middle path reach blocking reviewer findings. Both immutable same-attempt retry captures are opened and reviewed. Valid-hash raster mutations retain the original clean DOM report. Bypassing all capture checks fails all 9 feature cases; bypassing only feature checks while preserving image/seam/overview validation fails 8; omitting DOM checks fails 43 cases; omitting identity checks fails all 7 mismatch cases. All mutations restored.
- **Code quality third:** shared existing finite helper, finite/shape/index guards, explicit tolerances and named raster thresholds, deterministic non-nil findings, implicated runtime node/edge IDs, per-tile/per-feature cancellation, no empty-success stubs or accepted missing inputs. gofmt, go vet and diff whitespace checks pass.
- Verification: `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run 'TestReviewCapture|TestReadabilityReviewer' -count=1 -v` passes (including fresh pr-review, loop/crossing/seam, actual DOM defects, visual clipping and immutable retries). `ARBITRON_REQUIRE_BROWSER=1 go test -race ./adapter/renderreview -count=1` passes (351.322s), no race reports. `go build ./...`, `go vet ./adapter/renderreview`, and `go test ./domain/layout ./cmd/arbitron ./adapter/dashboard` pass. `make ui` and dashboard typecheck pass. Isolated full dashboard Vitest passes: 66 files / 887 tests. Concurrent browser/race runs caused the existing fontFamilyResolved 5-second test timeout (and one ArrangedGraph async assertion); the same focused files and isolated full suite pass without changing tests/timeouts to hide contention.

## Production completion gate

- [x] Exercise this reviewer on saved-manifest inputs through actual initDomain/serve/MCP construction, rather than only the real browser/coordinator adapter integration.

Resolved on production-completion revisit: P3T7 now constructs BrowserRenderer and NewReadabilityReviewer in `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer`. `TestLayoutRender_ReadabilityReviewerSavedEvidenceReachesSinks` drives actual initDomain, authorized Service.Create, filesystem/journal/SQLite, Worker/Coordinator, embedded Chrome DOM/PNG production and the unwrapped production reviewer through persisted results and the authorized query view. The render-boundary wrapper delegates real measurement/rendering and only injects evidence faults; it never supplies a report or verdict. Both the actual saved manifest JSON and its Unicode node/branch labels are asserted at the consumer. No production change was necessary.

### Production-completion execution evidence (2026-10-17)

- Selection: listed tasks in phase/task order. Earlier unchecked gates retain their documented baseline regression, active-rename, acknowledged-seed-input, validation-policy and unavailable-environment blockers; did not re-fail or guess fixes for those. Took this now-unblocked P3T6 root gate only.
- Added five saved-input root cases: complete evidence passes; another revision's rendering is unreviewed/needs_attention; a deleted tile or hash-mismatched valid PNG fails specifically at reviewing with ErrEvidenceUnavailable; blank tiles with refreshed valid hashes and unchanged clean DOM yield blocking raster findings. Assert terminal state, exact row input, one revision row, retained candidate/evidence policy, review→row→query findings and unchanged saved manifest bytes. Baseline also asserts reviewed query geometry/evidence parity and artifact hashes on disk.
- Characterization/TDD: initial execution exposed incorrect test assumptions (review I/O failure retains an unreviewed best candidate, not zero result generation; blank tiles implicate text_unreadable, not coverage). Read the existing Coordinator/Worker contract and corrected only those expectations; root cases passed (52.758s). Then removed identity and capture checks and disabled hash validation temporarily: stale identity lost the reviewer-specific finding, and both raster cases falsely reached passed. With identity/capture restored, independently disabling hash validation changed failed to needs_attention. These behavioral REDs are recorded in `/tmp/p3t6-reviewer-mutation-red.log` and `/tmp/p3t6-hash-mutation-red.log`. Restored every production mutation before final GREEN.
- Self-review, in order: (1) exact P3T6 evidence/reviewer slice; no new option, policy, wiring override in production or claim of P3T7/P3T14 complex-sample acceptance. (2) Strengthened stale-identity findings to require the reviewer's own evidence text at persisted/query sinks, so Coordinator's separate metadata guard cannot mask a missing reviewer check. Valid-PNG hash faults and valid-hash blank pixels distinguish file-integrity from raster checks; real saved labels/report/artifacts and the existing missing-report root control prevent nil/empty-input theater. (3) Followed neighboring root-test setup/cleanup and constructor delegation; buffered observation, no parallel global mutation, no fake reviewer, OS-independent missing-file assertion, gofmt/vet/diff checks clean.
- Final GREEN: `ARBITRON_REQUIRE_BROWSER=1 go test -race ./cmd/arbitron -run '^TestLayoutRender_ReadabilityReviewerSavedEvidenceReachesSinks$|^TestLayoutRender_RealBrowserPRReview$|^TestRenderPorts_RealBrowserSavedReportReachesSink$' -count=1 -timeout 8m` (69.701s); `ARBITRON_REQUIRE_BROWSER=1 go test -race ./adapter/renderreview -run 'TestReviewCapture|TestReadabilityReviewer' -count=1 -v -timeout 10m` (94.513s, all fresh-browser reviewer tests pass, no skips or race reports); `go build ./...`; `go vet ./adapter/renderreview ./cmd/arbitron`; `git diff --check`.
- Verification concern outside this task's focused reviewer tests: the required whole `adapter/renderreview` race run was attempted (624.517s) and failed only in the existing CloseCancelsInflightCalls and RealSurfaceDOMMutationsReachReport/path_opacity browser-launch checks at the unchanged 30-second Chrome startup deadline. All reviewer tests in that run passed; the complete focused reviewer and new root race suites subsequently passed above. Did not alter deadlines, weaken assertions or repeat the whole environment-sensitive run. Log: `/tmp/p3t6-adapter-race.log`. This is not a claim of a green whole-adapter rerun.
