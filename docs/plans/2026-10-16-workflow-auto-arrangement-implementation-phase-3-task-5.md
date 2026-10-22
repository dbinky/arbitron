# Workflow Auto-Arrangement — Phase 3 Task 5: Go browser renderer (chromedp) + embedded asset server

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

**Goal:** Implement the production `layout.Renderer`: `renderreview.BrowserRenderer` drives headless Chrome over the embedded dashboard render surface (`render.html`, P3T4). It measures text with the real CSS and fonts, and renders each candidate into a DOM `RenderReport` plus an overview and overlapping readable-scale PNG tiles.

**Architecture:** `adapter/dashboard.Assets()` exposes the embedded `dist/` (re-embedded here with `make ui` so it contains `render.html`). `assets.go` serves that FS on a loopback-only listener (`127.0.0.1:0`), plus one-shot `/__render/jobs/<token>.json` documents. `browser.go` starts Chrome on first use (auto-detected or `BrowserPath`) and gives every call its own tab. It navigates to `/render.html?job=<token>`, polls `window.__ARBITRON_RENDER_STATE__` and reads the result global. `tiles.go` plans the canvas tiles, and Render writes them under `ArtifactDir/<revision>/<attempt>/<unique-capture>/`. A missing browser is a per-call error wrapping `ErrBrowserUnavailable`, never a silent success.

**Tech Stack:** Go 1.25.5, `github.com/chromedp/chromedp v0.21.0` (generic `Action[T]` API) + `github.com/chromedp/cdproto v0.157.8`, `net/http` (`http.FileServerFS`), `crypto/rand`, `crypto/sha256`, `image/png`, `testing/fstest`, testify.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §4 "Shared renderer and production rendered-review seam" (wait for fonts/layout to settle; overview + overlapping tiles at practical zoom across the entire canvas; artifacts tied to candidate/revision/renderer version); `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` §3 and Data Provenance **PLANNED DEPENDENCY RENDER-REVIEW**. This task resolves the **renderer half** of RENDER-REVIEW. P3T4 built the page, P3T6 builds the reviewer, and P3T7 wires both into production.

**Prerequisites (merged before starting):** P1T2 (`Geometry`, `GeometryDigest`, `DefaultContract`), P1T4 (`MeasureRequest`, `Measurements`, `MeasureRequestFor`, `DefaultOptions`), P1T1 (`BuildGraph`), P1T6 (`Arrange`), P1T8 (`Renderer`, `RenderRequest`, `Rendering`, `RenderReport`, `Artifact`, `NewRenderRequest`, `RuntimeStates`), and the dashboard tasks P3T1–P3T4 (Task 5.4 runs `make ui`, which builds `render.html`). A local Chrome or Chromium is needed for the real-browser steps.

---

## Interfaces

**Consumes (exact, contracts §1 / §3; produced by other tasks):**

```go
// domain/layout (P1T1, P1T2, P1T4, P1T6, P1T8)
const ContractVersion = "arranged-renderer/1"
type Rect struct { X, Y, W, H float64 }            // json x,y,w,h; Right(), Bottom(), Contains(o Rect) bool
type RendererContract struct { Version string; FontFamily string; FontPackage string; NodeLabelFontPx float64; LineHeight float64; NodeTextMaxWidth float64; EdgeLabelMaxWidth float64; PracticalZoom float64; /* …all contract.json keys; comparable with == */ }
func DefaultContract() RendererContract
type NodeBox struct { ID string; Box Rect; Text string; TypeCaption string; TextLines int; Rank int } // TypeCaption = strings.ReplaceAll(string(Type), "_", " ")
type EdgeRoute struct { Ordinal int; Source, Target string; Points []Point; /* … */ }
type Geometry struct { Format string; Contract RendererContract; Options Options; Nodes []NodeBox; Edges []EdgeRoute; Crossings []Crossing; Canvas Rect }
func GeometryDigest(g Geometry) string
type TextStyle string // StyleNodeLabel "node_label", StyleNodeType "node_type", StyleEdgeLabel "edge_label"
type TextItem struct { Key string; Text string; Style TextStyle; MaxWidth float64 }
type MeasureRequest struct { Contract RendererContract `json:"contract"`; Items []TextItem `json:"items"` }
type TextMetrics struct { Width float64; Height float64; Lines int }
type Measurements struct { Contract RendererContract `json:"contract"`; FontsReady bool `json:"fontsReady"`; Items map[string]TextMetrics `json:"items"` }
func BuildGraph(wf *workflow.Workflow) (Graph, error)
func MeasureRequestFor(g Graph, c RendererContract) MeasureRequest
func DefaultOptions() Options
func Arrange(g Graph, m Measurements, c RendererContract, o Options) (Geometry, error)
var RuntimeStates = []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}
type RenderRequest struct { Revision string; Attempt int; Geometry Geometry; WorkflowNodeTypes map[string]string; States []string }
func NewRenderRequest(revision string, attempt int, geo Geometry, nodeTypes map[string]string) RenderRequest // States = copy of RuntimeStates
type RenderReport struct { SurfaceVersion string; Contract RendererContract; FontsReady, FontFamilyResolved bool; Zoom float64; Canvas Rect; Nodes []RenderedNode; Edges []RenderedEdge; Crossings []RenderedCrossing; States []StateProbe }
type Artifact struct { Kind string `json:"kind"`; Path string `json:"path"`; SHA256 string `json:"sha256"`; Region Rect `json:"region"`; Scale float64 `json:"scale"` } // Kind "overview" | "tile"
type Rendering struct { Revision string; Attempt int; GeometryDigest string; Report RenderReport; Artifacts []Artifact; RenderedAt time.Time }
type Renderer interface {
	Measure(ctx context.Context, req MeasureRequest) (Measurements, error)
	Render(ctx context.Context, req RenderRequest) (Rendering, error)
}
```

Page protocol (P3T4 `dashboard/render.html` → `src/render/main.tsx`, `job.ts`):
- URL `/render.html?job=<32 lowercase hex>`. The page fetches `/__render/jobs/<token>.json` once with `cache: 'no-store'`.
- Job JSON is `{"mode":"render","revision":"rev-1","attempt":1,"geometry":Geometry,"workflowNodeTypes":{id:type},"states":[...]}` or `{"mode":"measure","request":MeasureRequest}`. Null optional empty edge/crossing slices normalize to empty; missing node types/runtime-state coverage are rejected.
- Globals: `window.__ARBITRON_RENDER_STATE__` is `'loading' | 'ready' | 'error'`. It also sets `__ARBITRON_RENDER_ERROR__` (string), `__ARBITRON_RENDER_REPORT__` (RenderReport JSON) and `__ARBITRON_MEASURE_RESULT__` (Measurements JSON, with `fontsReady = fontsReady && fontFamilyResolved` and `contract` = contract.json).
- Render mode draws a container of `canvas.w*zoom × canvas.h*zoom` CSS px at the page origin (`zoom = contract.practicalZoom`). So page px = `(canvas − canvas.origin) × zoom`, and every report Rect/Point is in canvas coordinates. Before reporting ready, the page re-shows the **baseline** styling (no state probe), so the captured tiles show baseline styling. The occlusion probe uses `elementFromPoint`, so the browser viewport must cover the whole container. Render sets it to exactly that size.

```go
// adapter/dashboard/embed.go (existing; this task exports the sub-FS)
func Handler() http.Handler
```

**Produces (contracts §3 "Go render-review adapter (P3T5/P3T6)" + addendum 1; package `renderreview`, import `github.com/arbitron/arbitron/adapter/renderreview`):**

```go
// adapter/dashboard/embed.go
func Assets() fs.FS // embedded dist/ (index.html, render.html, assets/*)

// tiles.go
const (
	DefaultTileWidth  = 1600 // device px at practical zoom
	DefaultTileHeight = 1000
	DefaultOverlap    = 0.15 // fraction of a tile shared with its neighbour
	MinTileOverlapPx  = 24.0 // minimum seam overlap in canvas px (P3T6 flags less as incomplete coverage)
)
func PlanTiles(canvas layout.Rect, zoom float64, tileW, tileH int, overlap float64) []layout.Rect // row-major, covers the whole canvas; nil for invalid input

// assets.go (unexported)
type assetServer struct{ /* static fs + one-shot jobs on 127.0.0.1:0 */ }
func newAssetServer(assets fs.FS) (*assetServer, error)
func (s *assetServer) register(doc []byte) (token string, release func())
func (s *assetServer) pageURL(token string) string
func (s *assetServer) pending() int
func (s *assetServer) close()

// browser.go
var ErrBrowserUnavailable = errors.New("renderreview: no Chrome/Chromium browser available")
const DefaultRenderTimeout = 2 * time.Minute
type BrowserConfig struct {
	Assets        fs.FS         // required; must contain render.html (adapter/dashboard.Assets())
	BrowserPath   string        // "" ⇒ auto-detect (DetectBrowser)
	NoSandbox     bool          // --no-sandbox (Docker/root); from cfg.Layout.BrowserNoSandbox
	ArtifactDir   string        // required; made absolute
	Logger        *slog.Logger  // default slog.Default()
	RenderTimeout time.Duration // 0 ⇒ DefaultRenderTimeout; < 0 rejected
	TileWidth     int           // 0 ⇒ 1600; < 64 rejected
	TileHeight    int           // 0 ⇒ 1000; < 64 rejected
	Overlap       float64       // 0 ⇒ 0.15; must be in [0, 0.5)
}
type BrowserRenderer struct{ /* unexported */ }               // implements layout.Renderer
func NewBrowserRenderer(cfg BrowserConfig) (*BrowserRenderer, error) // validates; starts nothing
func (b *BrowserRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error)
func (b *BrowserRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error)
func (b *BrowserRenderer) Close()                                  // idempotent
func DetectBrowser(explicit string) (string, error)                // addition: used by requireBrowser and P3T7
```

Behaviour other tasks rely on:
- **Artifacts:** `Artifacts[0]` is the single `"overview"` (`Region == geo.Canvas`, `Scale = zoom × min(1, 2000 / max(canvas.W×zoom, canvas.H×zoom))`). `Artifacts[1:]` are `"tile"` in `PlanTiles` row-major order (`Scale == contract.PracticalZoom`). `Path` is absolute: `ArtifactDir/<revision>/<attempt>/<unique-capture>/overview.png` and `tile-000.png`, `tile-001.png`, …. `SHA256` is the lowercase hex sha256 of the file bytes. Image px (0,0) is canvas `(Region.X, Region.Y)`, and the image is `Region.W×Scale × Region.H×Scale` (±2 px). Each Render creates its own immutable unique child; existing evidence directories are never cleared or overwritten.
- **Rendering:** `GeometryDigest = layout.GeometryDigest(req.Geometry)`. `Report` is parsed verbatim from `__ARBITRON_RENDER_REPORT__`; Render does not judge it, P3T6 does. `RenderedAt` is UTC.
- **Measure** errors when `FontsReady` is false, when the returned contract `!=` the requested contract, or when any requested key is missing.
- **Errors:** a missing or unstartable browser wraps `ErrBrowserUnavailable`. Caller cancellation wraps `ctx.Err()` (`errors.Is(err, context.Canceled)`) and closes the tab at once. `RenderTimeout` expiry wraps `context.DeadlineExceeded` with the last page state. Page state `'error'` gives `"renderreview: render surface failed: <__ARBITRON_RENDER_ERROR__>"`. Invalid requests are rejected before the browser is needed: an unsafe revision, attempt < 1, no nodes, an empty canvas, zoom ≤ 0, more than 16384 device px per side, or more than 256 tiles.
- **Browser detection** (`BrowserPath == ""`), via `exec.LookPath` in this order:
  - linux: `chromium`, `chromium-browser`, `google-chrome`, `google-chrome-stable`, `headless_shell`, `headless-shell`, `chrome`, `/usr/bin/chromium`, `/usr/bin/chromium-browser`, `/usr/bin/google-chrome`, `/snap/bin/chromium`.
  - darwin: Google Chrome.app, Chromium.app, `google-chrome`, `chromium`.
  - windows: `chrome`, Program Files paths, `msedge`.

  An explicit `BrowserPath` never falls back to detection. Chrome starts lazily on the first call (launch timeout 30 s) and is relaunched if it has died. Every call runs in its own tab, so concurrent calls from the worker (Concurrency 2) are safe.
- **Chrome flags:** chromedp `DefaultExecAllocatorOptions` (headless, `disable-dev-shm-usage`, `force-color-profile=srgb`, …) plus `font-render-hinting=none`, and `--no-sandbox` when `NoSandbox`. Device scale factor is 1. Measure uses a 1280×800 viewport.

---
### Task 5.1: Tile planner (`tiles.go`)

**Files:**
- Create: `adapter/renderreview/tiles.go`
- Test:   `adapter/renderreview/tiles_test.go`

**Interfaces:** Consumes `layout.Rect` (P1T2). Produces `PlanTiles`, `DefaultTileWidth`, `DefaultTileHeight`, `DefaultOverlap`, `MinTileOverlapPx` (above).

- [x] **Step 1: Write the failing test.** Create `adapter/renderreview/tiles_test.go`. `assertCovers` is reused by the browser tests in Tasks 5.3 and 5.4.

```go
package renderreview

import (
	"math"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// covered reports whether p lies in some tile (closed intervals).
func covered(tiles []layout.Rect, x, y float64) bool {
	for _, t := range tiles {
		if x >= t.X-1e-9 && x <= t.Right()+1e-9 && y >= t.Y-1e-9 && y <= t.Bottom()+1e-9 {
			return true
		}
	}
	return false
}

// assertCovers samples the canvas every 7 canvas px plus its exact far edges.
func assertCovers(t *testing.T, canvas layout.Rect, tiles []layout.Rect) {
	t.Helper()
	xs := []float64{canvas.Right()}
	for x := canvas.X; x < canvas.Right(); x += 7 {
		xs = append(xs, x)
	}
	ys := []float64{canvas.Bottom()}
	for y := canvas.Y; y < canvas.Bottom(); y += 7 {
		ys = append(ys, y)
	}
	for _, x := range xs {
		for _, y := range ys {
			if !covered(tiles, x, y) {
				t.Fatalf("canvas point (%.1f, %.1f) is not covered by any tile", x, y)
			}
		}
	}
}

func TestPlanTiles_CoversWholeCanvasIncludingEdgeRemainder(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 4000, H: 2500}
	tiles := PlanTiles(canvas, 0.9, 1600, 1000, 0.15)

	// tile = 1777.8 × 1111.1 canvas px; step = 1511.1 × 944.4 ⇒ 3 columns × 3 rows.
	require.Len(t, tiles, 9)
	assertCovers(t, canvas, tiles)
	for _, tile := range tiles {
		assert.True(t, canvas.Contains(tile), "tile %+v leaves the canvas", tile)
		assert.LessOrEqual(t, tile.W*0.9, 1600.0+1e-9)
		assert.LessOrEqual(t, tile.H*0.9, 1000.0+1e-9)
	}
	last := tiles[len(tiles)-1]
	assert.InDelta(t, canvas.Right(), last.Right(), 1e-9, "edge remainder: last column ends on the canvas edge")
	assert.InDelta(t, canvas.Bottom(), last.Bottom(), 1e-9, "edge remainder: last row ends on the canvas edge")
	assert.InDelta(t, 0.0, tiles[0].X, 1e-9)
	assert.InDelta(t, 1511.111, tiles[1].X, 1e-3)
}

func TestPlanTiles_NeighboursOverlap(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 5000, H: 3000}
	tiles := PlanTiles(canvas, 0.9, 1600, 1000, 0.15)
	require.NotEmpty(t, tiles)
	tileW, tileH := 1600/0.9, 1000/0.9
	cols := 0
	for _, tile := range tiles {
		if tile.Y == tiles[0].Y {
			cols++
		}
	}
	require.Greater(t, cols, 1)
	for i := 0; i+1 < cols; i++ {
		seam := tiles[i].Right() - tiles[i+1].X
		assert.GreaterOrEqual(t, seam, 0.15*tileW-1e-9, "column seam %d", i)
	}
	for r := 0; (r+1)*cols < len(tiles); r++ {
		seam := tiles[r*cols].Bottom() - tiles[(r+1)*cols].Y
		assert.GreaterOrEqual(t, seam, 0.15*tileH-1e-9, "row seam %d", r)
	}
}

func TestPlanTiles_EnforcesMinimumOverlapInCanvasPx(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 1000, H: 100}
	tiles := PlanTiles(canvas, 1, 200, 200, 0) // zero fractional overlap still keeps 24 canvas px
	require.Len(t, tiles, 6)                   // step 176: 0,176,352,528,704,800
	for i := 0; i+1 < len(tiles); i++ {
		assert.GreaterOrEqual(t, tiles[i].Right()-tiles[i+1].X, MinTileOverlapPx-1e-9)
	}
	assertCovers(t, canvas, tiles)
}

func TestPlanTiles_OffsetCanvasStaysInside(t *testing.T) {
	canvas := layout.Rect{X: -120, Y: 40, W: 3000, H: 900}
	tiles := PlanTiles(canvas, 0.9, 1600, 1000, 0.15)
	require.Len(t, tiles, 2)
	assertCovers(t, canvas, tiles)
	for _, tile := range tiles {
		assert.True(t, canvas.Contains(tile), "tile %+v leaves the canvas", tile)
	}
	assert.InDelta(t, -120.0, tiles[0].X, 1e-9)
	assert.InDelta(t, 40.0, tiles[0].Y, 1e-9)
}

func TestPlanTiles_TinyCanvasIsOneTile(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 200, H: 100}
	assert.Equal(t, []layout.Rect{canvas}, PlanTiles(canvas, 0.9, 1600, 1000, 0.15))
}

func TestPlanTiles_ExactFitIsOneTile(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 1600, H: 1000}
	assert.Equal(t, []layout.Rect{canvas}, PlanTiles(canvas, 1, 1600, 1000, 0.15))
}

func TestPlanTiles_HigherZoomNeedsMoreTiles(t *testing.T) {
	canvas := layout.Rect{X: 0, Y: 0, W: 3000, H: 3000}
	assert.Greater(t, len(PlanTiles(canvas, 2, 1600, 1000, 0.15)), len(PlanTiles(canvas, 0.9, 1600, 1000, 0.15)))
}

func TestPlanTiles_InvalidInputIsGuarded(t *testing.T) {
	ok := layout.Rect{W: 100, H: 100}
	cases := map[string]func() []layout.Rect{
		"zero width":      func() []layout.Rect { return PlanTiles(layout.Rect{W: 0, H: 100}, 0.9, 1600, 1000, 0.15) },
		"negative height": func() []layout.Rect { return PlanTiles(layout.Rect{W: 100, H: -5}, 0.9, 1600, 1000, 0.15) },
		"NaN canvas": func() []layout.Rect {
			return PlanTiles(layout.Rect{X: math.NaN(), W: 100, H: 100}, 0.9, 1600, 1000, 0.15)
		},
		"infinite canvas":   func() []layout.Rect { return PlanTiles(layout.Rect{W: math.Inf(1), H: 100}, 0.9, 1600, 1000, 0.15) },
		"zero zoom":         func() []layout.Rect { return PlanTiles(ok, 0, 1600, 1000, 0.15) },
		"negative zoom":     func() []layout.Rect { return PlanTiles(ok, -1, 1600, 1000, 0.15) },
		"zero tile width":   func() []layout.Rect { return PlanTiles(ok, 0.9, 0, 1000, 0.15) },
		"negative tile h":   func() []layout.Rect { return PlanTiles(ok, 0.9, 1600, -1, 0.15) },
		"negative overlap":  func() []layout.Rect { return PlanTiles(ok, 0.9, 1600, 1000, -0.1) },
		"overlap of one":    func() []layout.Rect { return PlanTiles(ok, 0.9, 1600, 1000, 1) },
		"tile under 2×seam": func() []layout.Rect { return PlanTiles(layout.Rect{W: 500, H: 500}, 1, 40, 40, 0.15) },
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) { assert.Nil(t, plan()) })
	}
}
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing symbols, then seven behavioral failures against a compiling nil planner.

```bash
go test ./adapter/renderreview/ -run TestPlanTiles -v
```
Expected: a build failure:
```text
# github.com/arbitron/arbitron/adapter/renderreview [github.com/arbitron/arbitron/adapter/renderreview.test]
adapter/renderreview/tiles_test.go:44:11: undefined: PlanTiles
...
adapter/renderreview/tiles_test.go:88:59: undefined: MinTileOverlapPx
FAIL	github.com/arbitron/arbitron/adapter/renderreview [build failed]
```

- [x] **Step 3: Minimal implementation.** Create `adapter/renderreview/tiles.go`:

```go
package renderreview

import (
	"math"

	"github.com/arbitron/arbitron/domain/layout"
)

// Tile capture defaults (device px at the contract's practical zoom).
const (
	DefaultTileWidth  = 1600
	DefaultTileHeight = 1000
	DefaultOverlap    = 0.15
	// MinTileOverlapPx is the minimum overlap, in canvas px, between
	// neighbouring tiles; the readability reviewer (P3T6) treats a smaller
	// seam as incomplete visual coverage.
	MinTileOverlapPx = 24.0
)

// PlanTiles returns canvas-space regions, row-major, that together cover the
// whole canvas. Each region is at most tileW×tileH device px when captured at
// zoom (tileW/zoom × tileH/zoom canvas px). Neighbouring regions overlap by
// max(overlap×tile size, MinTileOverlapPx); the last region of each row and
// column is aligned to the canvas edge, so the edge remainder is covered
// without leaving the canvas. Invalid input (empty or non-finite canvas,
// non-positive zoom or tile size, overlap outside [0,1), or a tile too small
// for the minimum overlap) yields nil.
func PlanTiles(canvas layout.Rect, zoom float64, tileW, tileH int, overlap float64) []layout.Rect {
	if !finite(canvas.X, canvas.Y, canvas.W, canvas.H, zoom, overlap) ||
		canvas.W <= 0 || canvas.H <= 0 || zoom <= 0 || tileW <= 0 || tileH <= 0 ||
		overlap < 0 || overlap >= 1 {
		return nil
	}
	xs := spans(canvas.X, canvas.W, float64(tileW)/zoom, overlap)
	ys := spans(canvas.Y, canvas.H, float64(tileH)/zoom, overlap)
	if xs == nil || ys == nil {
		return nil
	}
	out := make([]layout.Rect, 0, len(xs)*len(ys))
	for _, y := range ys {
		for _, x := range xs {
			out = append(out, layout.Rect{X: x.start, Y: y.start, W: x.size, H: y.size})
		}
	}
	return out
}

type span struct{ start, size float64 }

// spans covers [origin, origin+length] with windows of size tile.
func spans(origin, length, tile, overlap float64) []span {
	if length <= tile {
		return []span{{start: origin, size: length}}
	}
	overlapPx := math.Max(overlap*tile, MinTileOverlapPx)
	if overlapPx*2 >= tile {
		return nil
	}
	step := tile - overlapPx
	// The epsilon keeps an exact fit from gaining a duplicate final window.
	n := int(math.Ceil((length-tile)/step-1e-9)) + 1
	out := make([]span, n)
	for i := 0; i < n-1; i++ {
		out[i] = span{start: origin + float64(i)*step, size: tile}
	}
	out[n-1] = span{start: origin + length - tile, size: tile}
	return out
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}
```

- [x] **Step 4: Run it and confirm it passes.**

Observed: eight tests passed (15 invalid-input subtests); gofmt silent. Counts are bounded before allocation, including overflow inputs.

```bash
gofmt -l adapter/renderreview/ && go test ./adapter/renderreview/ -run TestPlanTiles -v
```
Expected: `gofmt` prints nothing. Eight top-level tests `--- PASS` (`TestPlanTiles_CoversWholeCanvasIncludingEdgeRemainder`, `_NeighboursOverlap`, `_EnforcesMinimumOverlapInCanvasPx`, `_OffsetCanvasStaysInside`, `_TinyCanvasIsOneTile`, `_ExactFitIsOneTile`, `_HigherZoomNeedsMoreTiles`, and `_InvalidInputIsGuarded` with its 11 subtests), then `ok  	github.com/arbitron/arbitron/adapter/renderreview`.

- [x] **Step 5: Commit.**

```bash
git add adapter/renderreview/tiles.go adapter/renderreview/tiles_test.go && git commit -m "feat(renderreview): plan overlapping practical-zoom tiles covering the whole canvas" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---
### Task 5.2: Loopback asset server with one-shot job documents (`assets.go`)

**Files:**
- Create: `adapter/renderreview/assets.go`
- Test:   `adapter/renderreview/assets_test.go`

**Interfaces:** Consumes an `fs.FS` (production: `adapter/dashboard.Assets()`, Task 5.4). Produces the unexported `assetServer` API listed above. It serves the URL shape P3T4 `job.ts` expects: `/__render/jobs/<32 lowercase hex>.json` and `/render.html?job=<token>`.

Security properties pinned by the tests:
- It binds `127.0.0.1` only.
- It rejects a foreign `Host` header (DNS rebinding) with 403 and non-GET/HEAD methods with 405.
- It has no directory listing.
- Tokens are 128-bit `crypto/rand`.
- Each job is served once and is removed by `release` even if it was never fetched.

- [x] **Step 1: Write the failing test.** Create `adapter/renderreview/assets_test.go`:

```go
package renderreview

import (
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"render.html":         {Data: []byte("<!DOCTYPE html><title>surface</title>")},
		"index.html":          {Data: []byte("<!DOCTYPE html><title>spa</title>")},
		"assets/render-1.js":  {Data: []byte("console.log('render')")},
		"assets/render-1.css": {Data: []byte("body{}")},
	}
}

func startAssetServer(t *testing.T) *assetServer {
	t.Helper()
	s, err := newAssetServer(testAssets())
	require.NoError(t, err)
	t.Cleanup(s.close)
	return s
}

func get(t *testing.T, s *assetServer, path string) (int, string, http.Header) {
	t.Helper()
	res, err := http.Get("http://" + s.host + path)
	require.NoError(t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return res.StatusCode, string(body), res.Header
}

func TestAssetServer_BindsLoopbackOnly(t *testing.T) {
	s := startAssetServer(t)
	host, port, err := net.SplitHostPort(s.host)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.True(t, net.ParseIP(host).IsLoopback())
	assert.NotEqual(t, "0", port)
	assert.Equal(t, "http://"+s.host+"/render.html?job=abc", s.pageURL("abc"))
}

func TestAssetServer_ServesEmbeddedFiles(t *testing.T) {
	s := startAssetServer(t)
	code, body, hdr := get(t, s, "/render.html")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, "<title>surface</title>")
	assert.Equal(t, "no-store", hdr.Get("Cache-Control"))

	code, body, _ = get(t, s, "/assets/render-1.js")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "console.log('render')", body)

	code, _, _ = get(t, s, "/assets/missing.js")
	assert.Equal(t, http.StatusNotFound, code)
	code, _, _ = get(t, s, "/assets/")
	assert.Equal(t, http.StatusNotFound, code, "no directory listing")
}

func TestAssetServer_JobIsOneShot(t *testing.T) {
	s := startAssetServer(t)
	token, release := s.register([]byte(`{"mode":"measure"}`))
	defer release()
	assert.Equal(t, 1, s.pending())

	code, body, hdr := get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusOK, code)
	assert.JSONEq(t, `{"mode":"measure"}`, body)
	assert.Equal(t, "application/json", hdr.Get("Content-Type"))
	assert.Equal(t, "no-store", hdr.Get("Cache-Control"))
	assert.Equal(t, 0, s.pending())

	code, _, _ = get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusNotFound, code, "a job document is served once")
	release() // releasing a fetched job is a no-op
	assert.Equal(t, 0, s.pending())
}

func TestAssetServer_ReleasedJobIsGone(t *testing.T) {
	s := startAssetServer(t)
	token, release := s.register([]byte(`{}`))
	release()
	assert.Equal(t, 0, s.pending())
	code, _, _ := get(t, s, "/__render/jobs/"+token+".json")
	assert.Equal(t, http.StatusNotFound, code)
}

func TestAssetServer_UnknownOrMalformedTokenIs404(t *testing.T) {
	s := startAssetServer(t)
	_, release := s.register([]byte(`{}`))
	defer release()
	for _, path := range []string{
		"/__render/jobs/0123456789abcdef0123456789abcdef.json", // well-formed, never issued
		"/__render/jobs/../render.html",
		"/__render/jobs/ABCDEF.json",
		"/__render/jobs/0123456789abcdef0123456789abcdef",
	} {
		code, _, _ := get(t, s, path)
		assert.Equal(t, http.StatusNotFound, code, path)
	}
	assert.Equal(t, 1, s.pending(), "probing other tokens does not consume the real job")
}

func TestAssetServer_TokensAreRandomHex(t *testing.T) {
	s := startAssetServer(t)
	re := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		token, release := s.register([]byte(`{}`))
		assert.Regexp(t, re, token)
		assert.False(t, seen[token], "token reused")
		seen[token] = true
		release()
	}
}

func TestAssetServer_RejectsForeignHostAndWrites(t *testing.T) {
	s := startAssetServer(t)
	req, err := http.NewRequest(http.MethodGet, "http://"+s.host+"/render.html", nil)
	require.NoError(t, err)
	req.Host = "evil.example:80" // DNS-rebinding style request
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusForbidden, res.StatusCode)

	res, err = http.Post("http://"+s.host+"/render.html", "text/plain", strings.NewReader("x"))
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, res.StatusCode)
}

func TestAssetServer_CloseStopsListening(t *testing.T) {
	s, err := newAssetServer(testAssets())
	require.NoError(t, err)
	s.close()
	_, err = http.Get("http://" + s.host + "/render.html")
	assert.Error(t, err)
}

func TestAssetServer_NeedsAssets(t *testing.T) {
	_, err := newAssetServer(nil)
	assert.ErrorContains(t, err, "asset filesystem")
}
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing symbols, then behavioral failures with compiling empty server declarations.

```bash
go test ./adapter/renderreview/ -run TestAssetServer -v
```
Expected: `FAIL ... [build failed]` with `undefined: newAssetServer` and `undefined: assetServer`.

- [x] **Step 3: Minimal implementation.** Create `adapter/renderreview/assets.go`:

```go
package renderreview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	surfacePage   = "render.html"
	jobPathPrefix = "/__render/jobs/"
)

// jobTokenRE matches the tokens issued by register (and accepted by the
// render surface's jobTokenFrom): 32 lowercase hex characters.
var jobTokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// assetServer serves the embedded dashboard build plus one-shot job documents
// to the headless browser. It listens on 127.0.0.1 only and answers only
// requests addressed to that exact host:port.
type assetServer struct {
	static   http.Handler
	listener net.Listener
	srv      *http.Server
	host     string // "127.0.0.1:<port>"

	mu   sync.Mutex
	jobs map[string][]byte
}

func newAssetServer(assets fs.FS) (*assetServer, error) {
	if assets == nil {
		return nil, errors.New("renderreview: asset server needs the dashboard asset filesystem")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("renderreview: listen on loopback: %w", err)
	}
	s := &assetServer{
		static:   http.FileServerFS(assets),
		listener: ln,
		host:     ln.Addr().String(),
		jobs:     make(map[string][]byte),
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// register stores a job document under a fresh random token. The document is
// served at most once; release removes it if it was never fetched.
func (s *assetServer) register(doc []byte) (token string, release func()) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf) // crypto/rand.Read never returns an error (Go ≥ 1.24)
	token = hex.EncodeToString(buf)
	s.mu.Lock()
	s.jobs[token] = doc
	s.mu.Unlock()
	return token, func() {
		s.mu.Lock()
		delete(s.jobs, token)
		s.mu.Unlock()
	}
}

// pending is the number of registered, not yet fetched or released, jobs.
func (s *assetServer) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

// pageURL is the render surface URL for one job.
func (s *assetServer) pageURL(token string) string {
	return "http://" + s.host + "/" + surfacePage + "?job=" + token
}

func (s *assetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, jobPathPrefix) {
		s.serveJob(w, r)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r) // no directory listings, no SPA index
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.static.ServeHTTP(w, r)
}

func (s *assetServer) serveJob(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, jobPathPrefix), ".json")
	if !ok || !jobTokenRE.MatchString(token) {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	doc, found := s.jobs[token]
	if found && r.Method == http.MethodGet {
		delete(s.jobs, token) // one-shot
	}
	s.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(doc)
}

func (s *assetServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
	}
}
```

- [x] **Step 4: Run it and confirm it passes.**

Observed: ten tests passed, including HEAD non-consumption; gofmt silent. Static directories are rejected before FileServer, including slashless directories.

```bash
gofmt -l adapter/renderreview/ && go test ./adapter/renderreview/ -run TestAssetServer -v
```
Expected: nine `--- PASS: TestAssetServer_*` lines (`BindsLoopbackOnly`, `ServesEmbeddedFiles`, `JobIsOneShot`, `ReleasedJobIsGone`, `UnknownOrMalformedTokenIs404`, `TokensAreRandomHex`, `RejectsForeignHostAndWrites`, `CloseStopsListening`, `NeedsAssets`), then `ok`.

- [x] **Step 5: Commit.**

```bash
git add adapter/renderreview/assets.go adapter/renderreview/assets_test.go && git commit -m "feat(renderreview): loopback-only asset server with one-shot render job documents" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5.3: Headless-Chrome renderer (`browser.go`) + chromedp dependency

**Files:**
- Create: `adapter/renderreview/browser.go`
- Modify: `go.mod:5-29` (direct `require` block: add `github.com/chromedp/cdproto v0.157.8` and `github.com/chromedp/chromedp v0.21.0`), `go.mod:31-64` (indirect block: add `github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect`), `go.sum`
- Test:   `adapter/renderreview/browser_test.go`

**Interfaces:**
- Consumes: the `layout.*` types above (P1T2/P1T4/P1T8), `PlanTiles` (Task 5.1), `assetServer` (Task 5.2), and the P3T4 page protocol.
- Produces: `BrowserConfig`, `BrowserRenderer`, `NewBrowserRenderer`, `Measure`, `Render`, `Close`, `ErrBrowserUnavailable`, `DefaultRenderTimeout` and `DetectBrowser`.
- Test helpers shared with P3T6, which uses the same `package renderreview`:
  - `requireBrowser(t) string`.
  - `testBrowserConfig(t, assets fs.FS) BrowserConfig` sets BrowserPath, `NoSandbox: os.Geteuid()==0`, `ArtifactDir: t.TempDir()`, `Logger: slog.Default()` and `RenderTimeout: 90s`, and leaves the tile defaults alone.
  - `newTestRenderer(t, cfg) *BrowserRenderer`, which calls `Close` in Cleanup.
  - `distinctColors(t, pngPath) int`.

The tests come in two groups.
- **No browser needed** (they always run): config validation and defaults, browser detection with a fake `lookPath` that honours its argument, the job document shape, request validation before launch, the closed renderer, and the provenance probes `TestBrowserRenderer_MissingBrowserIsUnavailable` and `TestBrowserRenderer_BrowserThatCannotStartIsUnavailable`.
- **Real browser, fake surface** (`requireBrowser`): `echoSurface` is a stand-in `render.html` that fetches its job twice and echoes what it received. It proves the Go side of the protocol: the job is delivered once, node types and states reach the page, artifacts are hashed with correct dimensions, surface errors propagate, timeouts and cancellation abort, and tabs are isolated. It does not depend on the dashboard build.

- [x] **Step 1: Write the failing test.** Create `adapter/renderreview/browser_test.go`:

```go
package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireBrowser returns the Chrome/Chromium the real-browser tests drive
// (ARBITRON_LAYOUT_BROWSERPATH, else auto-detected). Without one the test is
// skipped, unless ARBITRON_REQUIRE_BROWSER=1 (make test-render), which fails it.
func requireBrowser(t *testing.T) string {
	t.Helper()
	path, err := DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	if err != nil {
		if os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1" {
			t.Fatalf("ARBITRON_REQUIRE_BROWSER=1 but no browser is available: %v", err)
		}
		t.Skipf("no Chrome/Chromium available (set ARBITRON_REQUIRE_BROWSER=1 to fail instead): %v", err)
	}
	return path
}

// testBrowserConfig is the config every real-browser test in this package
// (P3T5 and P3T6) uses. Chrome refuses to start sandboxed as root (CI/Docker).
func testBrowserConfig(t *testing.T, assets fs.FS) BrowserConfig {
	t.Helper()
	return BrowserConfig{
		Assets:        assets,
		BrowserPath:   requireBrowser(t),
		NoSandbox:     os.Geteuid() == 0,
		ArtifactDir:   t.TempDir(),
		Logger:        slog.Default(),
		RenderTimeout: 90 * time.Second,
	}
}

func newTestRenderer(t *testing.T, cfg BrowserConfig) *BrowserRenderer {
	t.Helper()
	b, err := NewBrowserRenderer(cfg)
	require.NoError(t, err)
	t.Cleanup(b.Close)
	return b
}

func surfaceOnly() fstest.MapFS {
	return fstest.MapFS{"render.html": {Data: []byte("<!DOCTYPE html>")}}
}

func smallNodeTypes() map[string]string { return map[string]string{"a": "agent_call", "b": "transform"} }

func smallGeometry() layout.Geometry {
	return layout.Geometry{
		Format:   "arbitron.layout/v1",
		Options: layout.DefaultOptions(),
		Contract: layout.DefaultContract(),
		Nodes: []layout.NodeBox{
			{ID: "a", Box: layout.Rect{X: 40, Y: 40, W: 200, H: 60}, Text: "Start", TypeCaption: "agent call", TextLines: 1},
			{ID: "b", Box: layout.Rect{X: 40, Y: 160, W: 200, H: 60}, Text: "Finish", TypeCaption: "transform", TextLines: 1, Rank: 1},
		},
		Edges: []layout.EdgeRoute{{Ordinal: 0, Source: "a", Target: "b", Kind: layout.EdgeForward,
            Points: []layout.Point{{X: 140, Y: 100}, {X: 140, Y: 160}},
            SourceAttach: layout.Attachment{NodeID: "a", Side: layout.SideBottom, Point: layout.Point{X: 140, Y: 100}},
            TargetAttach: layout.Attachment{NodeID: "b", Side: layout.SideTop, Point: layout.Point{X: 140, Y: 160}},
        }},
		Canvas: layout.Rect{X: 0, Y: 0, W: 900, H: 700},
	}
}

func TestNewBrowserRenderer_AppliesDefaultsWithoutStartingAnything(t *testing.T) {
	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), BrowserPath: "/definitely/not/chrome", ArtifactDir: "rel-artifacts"})
	require.NoError(t, err, "a missing browser is a per-call error, not a construction error")
	defer b.Close()
	assert.Equal(t, DefaultRenderTimeout, b.cfg.RenderTimeout)
	assert.Equal(t, 2*time.Minute, b.cfg.RenderTimeout)
	assert.Equal(t, DefaultTileWidth, b.cfg.TileWidth)
	assert.Equal(t, DefaultTileHeight, b.cfg.TileHeight)
	assert.Equal(t, DefaultOverlap, b.cfg.Overlap)
	assert.NotNil(t, b.logger)
	assert.True(t, filepath.IsAbs(b.cfg.ArtifactDir))
	assert.Nil(t, b.server, "no port is bound before the first call")
	assert.Nil(t, b.browserCtx, "no browser is launched before the first call")
}

func TestNewBrowserRenderer_RejectsInvalidConfig(t *testing.T) {
	ok := func() BrowserConfig { return BrowserConfig{Assets: surfaceOnly(), ArtifactDir: t.TempDir()} }
	cases := map[string]struct {
		mutate func(*BrowserConfig)
		want   string
	}{
		"nil assets":       {func(c *BrowserConfig) { c.Assets = nil }, "Assets is required"},
		"no render.html":   {func(c *BrowserConfig) { c.Assets = fstest.MapFS{"index.html": {Data: []byte("x")}} }, "make ui"},
		"no artifact dir":  {func(c *BrowserConfig) { c.ArtifactDir = " " }, "ArtifactDir is required"},
		"negative timeout": {func(c *BrowserConfig) { c.RenderTimeout = -time.Second }, "RenderTimeout"},
		"negative tile":    {func(c *BrowserConfig) { c.TileWidth = -1 }, "TileWidth"},
		"tiny tile":        {func(c *BrowserConfig) { c.TileHeight = 10 }, "TileHeight 10 must be at least 64"},
		"negative overlap": {func(c *BrowserConfig) { c.Overlap = -0.1 }, "Overlap"},
		"half overlap":     {func(c *BrowserConfig) { c.Overlap = 0.5 }, "Overlap"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := ok()
			tc.mutate(&cfg)
			_, err := NewBrowserRenderer(cfg)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// Provenance probe relied on by P1T8/P3T7: a configured browser that does not
// exist must fail every call, never yield a silent success.
func TestBrowserRenderer_MissingBrowserIsUnavailable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-chrome")
	artifacts := t.TempDir()
	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), BrowserPath: missing, ArtifactDir: artifacts})
	require.NoError(t, err)
	defer b.Close()

	_, err = b.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{{Key: "k", Text: "x", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
	require.ErrorIs(t, err, ErrBrowserUnavailable)
	assert.Contains(t, err.Error(), missing)

	_, err = b.Render(context.Background(), layout.NewRenderRequest("rev-1", 1, smallGeometry(), smallNodeTypes()))
	require.ErrorIs(t, err, ErrBrowserUnavailable)
	_, statErr := os.Stat(filepath.Join(artifacts, "rev-1"))
	assert.True(t, errors.Is(statErr, fs.ErrNotExist), "no artifacts without a browser")
	assert.Nil(t, b.server, "the asset server is not started without a browser")
}

func TestBrowserRenderer_BrowserThatCannotStartIsUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the fake browser")
	}
	fake := filepath.Join(t.TempDir(), "fake-chrome")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), BrowserPath: fake, ArtifactDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	start := time.Now()
	_, err = b.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract()})
	require.ErrorIs(t, err, ErrBrowserUnavailable)
	assert.Contains(t, err.Error(), "launching "+fake)
	assert.Less(t, time.Since(start), 20*time.Second)
}

func TestFindBrowser(t *testing.T) {
	known := map[string]string{"chromium": "/usr/bin/chromium", "/opt/chrome/chrome": "/opt/chrome/chrome"}
	lookPath := func(name string) (string, error) {
		if p, ok := known[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	p, err := findBrowser("", []string{"google-chrome", "chromium", "chrome"}, lookPath)
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/chromium", p, "first candidate that resolves wins")

	p, err = findBrowser("/opt/chrome/chrome", []string{"chromium"}, lookPath)
	require.NoError(t, err)
	assert.Equal(t, "/opt/chrome/chrome", p, "an explicit path is used as given")

	_, err = findBrowser("/nope/chrome", []string{"chromium"}, lookPath)
	require.ErrorIs(t, err, ErrBrowserUnavailable, "an explicit path never falls back to auto-detection")
	assert.Contains(t, err.Error(), `layout.browserpath "/nope/chrome"`)

	_, err = findBrowser("", []string{"google-chrome", "chrome"}, lookPath)
	require.ErrorIs(t, err, ErrBrowserUnavailable)
	assert.Contains(t, err.Error(), "google-chrome, chrome")
}

func TestBrowserCandidates_CoverEachPlatform(t *testing.T) {
	assert.Contains(t, browserCandidates("linux"), "chromium")
	assert.Contains(t, browserCandidates("linux"), "google-chrome")
	assert.Contains(t, browserCandidates("darwin"), "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	assert.Contains(t, browserCandidates("windows"), `C:\Program Files\Google\Chrome\Application\chrome.exe`)
}

func TestRenderJobDocument_MatchesSurfaceProtocol(t *testing.T) {
	req := layout.NewRenderRequest("rev-1", 2, smallGeometry(), smallNodeTypes())
	raw, err := renderJobDocument(req)
	require.NoError(t, err)
	var doc struct {
		Mode              string            `json:"mode"`
        Revision string `json:"revision"`
        Attempt int `json:"attempt"`
		Geometry          layout.Geometry   `json:"geometry"`
		WorkflowNodeTypes map[string]string `json:"workflowNodeTypes"`
		States            []string          `json:"states"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, "render", doc.Mode)
    assert.Equal(t, req.Revision, doc.Revision)
    assert.Equal(t, req.Attempt, doc.Attempt)
	assert.Equal(t, req.Geometry, doc.Geometry)
	assert.Equal(t, map[string]string{"a": "agent_call", "b": "transform"}, doc.WorkflowNodeTypes)
	assert.Equal(t, layout.RuntimeStates, doc.States)
}

func TestRender_RejectsInvalidRequestBeforeLaunching(t *testing.T) {
	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), BrowserPath: "/definitely/not/chrome", ArtifactDir: t.TempDir()})
	require.NoError(t, err)
	defer b.Close()
	geo := smallGeometry()
	cases := map[string]func(*layout.RenderRequest){
		"empty revision":     func(r *layout.RenderRequest) { r.Revision = "" },
		"traversal revision": func(r *layout.RenderRequest) { r.Revision = "../etc" },
		"zero attempt":       func(r *layout.RenderRequest) { r.Attempt = 0 },
		"no nodes":           func(r *layout.RenderRequest) { r.Geometry.Nodes = nil },
        "no types": func(r *layout.RenderRequest) { r.WorkflowNodeTypes = nil },
        "wrong type": func(r *layout.RenderRequest) { r.WorkflowNodeTypes["a"] = "transform" },
        "missing states": func(r *layout.RenderRequest) { r.States = nil },
		"empty canvas":       func(r *layout.RenderRequest) { r.Geometry.Canvas.W = 0 },
		"zero zoom":          func(r *layout.RenderRequest) { r.Geometry.Contract.PracticalZoom = 0 },
		"huge canvas":        func(r *layout.RenderRequest) { r.Geometry.Canvas.H = 40000 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := layout.NewRenderRequest("rev-1", 1, geo, smallNodeTypes())
			mutate(&req)
			_, err := b.Render(context.Background(), req)
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrBrowserUnavailable, "validation happens before the browser is needed")
		})
	}
}

func TestBrowserRenderer_ClosedRendererFails(t *testing.T) {
	b, err := NewBrowserRenderer(BrowserConfig{Assets: surfaceOnly(), ArtifactDir: t.TempDir()})
	require.NoError(t, err)
	b.Close()
	b.Close() // idempotent
	_, err = b.Measure(context.Background(), layout.MeasureRequest{})
	assert.ErrorContains(t, err, "renderer is closed")
}

// echoSurface stands in for render.html: it fetches its job twice (the second
// fetch must 404) and echoes what it received through the page protocol.
// Measure items whose first key is "hang", "fail", "nofonts", "othercontract"
// or "dropkey" exercise the renderer's failure handling.
const echoSurface = `<!DOCTYPE html><html><head><meta charset="utf-8">
<style>html,body{margin:0;background:#1e1e38}#box{position:absolute;left:36px;top:36px;width:180px;height:54px;background:#e0e0e0}</style></head>
<body><div id="box"></div><script>
(async () => {
  window.__ARBITRON_RENDER_STATE__ = 'loading'
  try {
    const url = '/__render/jobs/' + new URLSearchParams(location.search).get('job') + '.json'
    const job = await (await fetch(url, { cache: 'no-store' })).json()
    const again = (await fetch(url, { cache: 'no-store' })).status
    if (job.mode === 'measure') {
      const items = job.request.items || []
      const first = (items[0] || {}).key
      if (first === 'hang') return
      if (first === 'fail') throw new Error('boom: ' + items[0].text)
      const out = {}
      for (const it of items) if (first !== 'dropkey') out[it.key] = { width: Math.min(it.text.length * 7, it.maxWidth), height: 17, lines: 1 }
      const contract = first === 'othercontract' ? { ...job.request.contract, version: 'arranged-renderer/0' } : job.request.contract
      window.__ARBITRON_MEASURE_RESULT__ = { contract, fontsReady: first !== 'nofonts', items: out }
    } else {
      const g = job.geometry
      const text = (b) => ({ box: b, clipped: false, fontPx: 13, contrast: 10, occluded: false })
      window.__ARBITRON_RENDER_REPORT__ = {
        surfaceVersion: 'echo/refetch-' + again, contract: g.contract, fontsReady: true, fontFamilyResolved: true,
        zoom: g.contract.practicalZoom, canvas: g.canvas,
        nodes: g.nodes.map((n) => ({ id: n.id + ':' + job.workflowNodeTypes[n.id], box: n.box, label: text(n.box), type: text(n.box) })),
        edges: [], crossings: [],
        states: (job.states || []).map((s) => ({ state: s, nodeBoxesStable: true, labelsReadable: true })),
      }
    }
    window.__ARBITRON_RENDER_STATE__ = 'ready'
  } catch (e) {
    window.__ARBITRON_RENDER_ERROR__ = String((e && e.message) || e)
    window.__ARBITRON_RENDER_STATE__ = 'error'
  }
})()
</script></body></html>`

func echoAssets() fstest.MapFS {
	return fstest.MapFS{"render.html": {Data: []byte(echoSurface)}}
}

func measureReq(keys ...string) layout.MeasureRequest {
	req := layout.MeasureRequest{Contract: layout.DefaultContract()}
	for _, k := range keys {
		req.Items = append(req.Items, layout.TextItem{Key: k, Text: "text of " + k, Style: layout.StyleNodeLabel, MaxWidth: 240})
	}
	return req
}

func TestBrowserRenderer_MeasureProtocolRoundTrip(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	req := measureReq("node:a:label", "edge:0")
	m, err := b.Measure(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, m.FontsReady)
	assert.Equal(t, req.Contract, m.Contract)
	assert.Equal(t, layout.TextMetrics{Width: 7 * float64(len("text of node:a:label")), Height: 17, Lines: 1}, m.Items["node:a:label"])
	assert.Len(t, m.Items, 2)
	assert.Equal(t, 0, b.server.pending(), "the job was fetched once and released")
}

func TestBrowserRenderer_MeasureRejectsUntrustworthyResults(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	for key, want := range map[string]string{
		"nofonts":       "fonts not ready",
		"othercontract": `contract "arranged-renderer/0" differs`,
		"dropkey":       `no metrics for "dropkey"`,
		"fail":          "render surface failed: boom: text of fail",
	} {
		t.Run(key, func(t *testing.T) {
			_, err := b.Measure(context.Background(), measureReq(key))
			assert.ErrorContains(t, err, want)
		})
	}
}

func TestBrowserRenderer_RenderProtocolAndArtifacts(t *testing.T) {
	cfg := testBrowserConfig(t, echoAssets())
	cfg.TileWidth, cfg.TileHeight = 400, 300
	b := newTestRenderer(t, cfg)
	geo := smallGeometry()
	req := layout.NewRenderRequest("rev-echo", 3, geo, smallNodeTypes())
	before := time.Now()
	r, err := b.Render(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "rev-echo", r.Revision)
	assert.Equal(t, 3, r.Attempt)
	assert.Equal(t, layout.GeometryDigest(geo), r.GeometryDigest)
	assert.False(t, r.RenderedAt.Before(before.UTC().Add(-time.Second)))
	assert.Equal(t, "echo/refetch-404", r.Report.SurfaceVersion, "the job document is one-shot")
	require.Len(t, r.Report.Nodes, 2)
	assert.Equal(t, "a:agent_call", r.Report.Nodes[0].ID, "workflow node types reach the surface")
	assert.Equal(t, "b:transform", r.Report.Nodes[1].ID)
	require.Len(t, r.Report.States, len(layout.RuntimeStates))
	for i, s := range layout.RuntimeStates {
		assert.Equal(t, s, r.Report.States[i].State)
	}

	tiles := PlanTiles(geo.Canvas, 0.9, 400, 300, DefaultOverlap)
	require.Greater(t, len(tiles), 1)
	require.Len(t, r.Artifacts, len(tiles)+1)
	assert.Equal(t, "overview", r.Artifacts[0].Kind)
	assert.Equal(t, geo.Canvas, r.Artifacts[0].Region)
	assert.LessOrEqual(t, r.Artifacts[0].Scale, 0.9)
	dir := filepath.Join(cfg.ArtifactDir, "rev-echo", "3")
	for i, a := range r.Artifacts {
		if i > 0 {
			assert.Equal(t, "tile", a.Kind)
			assert.Equal(t, tiles[i-1], a.Region)
			assert.Equal(t, 0.9, a.Scale)
		}
		assert.Equal(t, dir, filepath.Dir(filepath.Dir(a.Path)))
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		assert.Equal(t, hex.EncodeToString(sum[:]), a.SHA256)
		img, err := png.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err, a.Path)
		assert.InDelta(t, a.Region.W*a.Scale, float64(img.Width), 2, "%s width", a.Path)
		assert.InDelta(t, a.Region.H*a.Scale, float64(img.Height), 2, "%s height", a.Path)
	}
	assert.GreaterOrEqual(t, distinctColors(t, r.Artifacts[0].Path), 2, "the overview shows the page, not a blank capture")
	assert.Equal(t, 0, b.server.pending())
}

// distinctColors counts the colours of every second pixel of a PNG.
func distinctColors(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	img, err := png.Decode(f)
	require.NoError(t, err)
	seen := map[[4]uint32]bool{}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y += 2 {
		for x := bounds.Min.X; x < bounds.Max.X; x += 2 {
			r, g, bl, a := img.At(x, y).RGBA()
			seen[[4]uint32{r, g, bl, a}] = true
		}
	}
	return len(seen)
}

func TestBrowserRenderer_SurfaceErrorIsReported(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	_, err := b.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{{Key: "fail", Text: "geometry rejected", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "geometry rejected", "the surface's __ARBITRON_RENDER_ERROR__ reaches the caller")
	assert.NotErrorIs(t, err, ErrBrowserUnavailable)
}

func TestBrowserRenderer_TimesOutWhenSurfaceNeverFinishes(t *testing.T) {
	cfg := testBrowserConfig(t, echoAssets())
	cfg.RenderTimeout = 1500 * time.Millisecond
	b := newTestRenderer(t, cfg)
	start := time.Now()
	_, err := b.Measure(context.Background(), measureReq("hang"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), `did not finish within 1.5s (state "loading")`)
	assert.Less(t, time.Since(start), 15*time.Second)
	assert.Equal(t, 0, b.server.pending(), "an unfinished job is unregistered")
}

func TestBrowserRenderer_CancellationAbortsPromptly(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	_, err := b.Measure(context.Background(), measureReq("warmup")) // browser already running
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err = b.Measure(ctx, measureReq("hang"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second, "cancellation must not wait for RenderTimeout")

	_, err = b.Measure(context.Background(), measureReq("after"))
	assert.NoError(t, err, "the browser survives a cancelled call")

	pre, cancelPre := context.WithCancel(context.Background())
	cancelPre()
	_, err = b.Render(pre, layout.NewRenderRequest("rev-1", 1, smallGeometry(), smallNodeTypes()))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestBrowserRenderer_ConcurrentCallsUseSeparateTabs(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	var wg sync.WaitGroup
	results := make([]layout.Measurements, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = b.Measure(context.Background(), measureReq(fmt.Sprintf("job-%d", i)))
		}(i)
	}
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		assert.Contains(t, results[i].Items, fmt.Sprintf("job-%d", i))
		assert.Len(t, results[i].Items, 1)
	}
}
```

- [x] **Step 2: Run it and confirm it fails.**

Observed: missing symbols, then defaults/detection/job/validation/closed-renderer assertions failed against compiling declarations. Added request-boundary RED cases before implementing validation. Failed-capture cleanup also failed behaviorally before its correction.

```bash
go test ./adapter/renderreview/ -v
```
Expected: `FAIL ... [build failed]` with `undefined: DetectBrowser`, `undefined: BrowserConfig`, `undefined: BrowserRenderer`, `undefined: NewBrowserRenderer`, `undefined: ErrBrowserUnavailable`, `undefined: findBrowser`, `undefined: renderJobDocument`, ….

- [x] **Step 3: Add the chromedp dependency.**

```bash
go get github.com/chromedp/chromedp@v0.21.0
```
Expected output:
```text
go: added github.com/chromedp/cdproto v0.157.8
go: added github.com/chromedp/chromedp v0.21.0
go: added github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875
```
(chromedp v0.21.0 requires `go 1.25`, which is compatible with the module's `go 1.25.5`. Its API is generic: `chromedp.Run[T](ctx, Action[T]) (T, error)`, `chromedp.Do(ctx, ...Action[Void])`, `chromedp.Evaluate[T](expr)`, `cdp.Call(ctx, session, page.CaptureScreenshot, params)`.)

- [x] **Step 4: Implement.** Create `adapter/renderreview/browser.go`:

```go
package renderreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/arbitron/arbitron/domain/layout"
)

// ErrBrowserUnavailable means no usable Chrome/Chromium could be found or
// started. Measure and Render wrap it; the renderer never reports success
// without a browser.
var ErrBrowserUnavailable = errors.New("renderreview: no Chrome/Chromium browser available")

const (
	// DefaultRenderTimeout bounds one Measure or Render call (navigation,
	// surface work, screenshots).
	DefaultRenderTimeout = 2 * time.Minute
	minTilePx            = 64
	maxTiles             = 256
	maxViewportPx        = 16384
	maxOverviewPx        = 2000.0
	measureViewportW     = 1280
	measureViewportH     = 800
	launchTimeout        = 30 * time.Second
	pollInterval         = 50 * time.Millisecond
)

// safeSegment restricts revision tokens used as artifact directory names.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// BrowserConfig configures the headless-Chrome renderer.
type BrowserConfig struct {
	Assets        fs.FS         // dashboard build containing render.html (adapter/dashboard.Assets())
	BrowserPath   string        // Chrome/Chromium binary; "" auto-detects
	NoSandbox     bool          // pass --no-sandbox (containers running as root)
	ArtifactDir   string        // screenshots go to ArtifactDir/<revision>/<attempt>/<unique-capture>/
	Logger        *slog.Logger  // default slog.Default()
	RenderTimeout time.Duration // per call; default DefaultRenderTimeout
	TileWidth     int           // tile size in device px; default DefaultTileWidth
	TileHeight    int           // default DefaultTileHeight
	Overlap       float64       // fraction of a tile shared with its neighbour; default DefaultOverlap
}

// BrowserRenderer implements layout.Renderer by driving the dashboard's
// render surface (render.html) in headless Chrome. The browser and the
// loopback asset server start on first use, so a missing browser surfaces as
// a per-call error wrapping ErrBrowserUnavailable.
type BrowserRenderer struct {
	cfg    BrowserConfig
	logger *slog.Logger
	now    func() time.Time

	mu            sync.Mutex
	closed        bool
	server        *assetServer
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
}

var _ layout.Renderer = (*BrowserRenderer)(nil)

// NewBrowserRenderer validates cfg and applies defaults. It neither starts a
// browser nor binds a port.
func NewBrowserRenderer(cfg BrowserConfig) (*BrowserRenderer, error) {
	if cfg.Assets == nil {
		return nil, errors.New("renderreview: BrowserConfig.Assets is required (adapter/dashboard.Assets())")
	}
	if _, err := fs.Stat(cfg.Assets, surfacePage); err != nil {
		return nil, fmt.Errorf("renderreview: dashboard assets have no %s; rebuild the embedded dashboard with `make ui`: %w", surfacePage, err)
	}
	if strings.TrimSpace(cfg.ArtifactDir) == "" {
		return nil, errors.New("renderreview: BrowserConfig.ArtifactDir is required")
	}
	abs, err := filepath.Abs(cfg.ArtifactDir)
	if err != nil {
		return nil, fmt.Errorf("renderreview: artifact dir: %w", err)
	}
	cfg.ArtifactDir = abs
	switch {
	case cfg.RenderTimeout < 0:
		return nil, fmt.Errorf("renderreview: RenderTimeout %s must not be negative", cfg.RenderTimeout)
	case cfg.RenderTimeout == 0:
		cfg.RenderTimeout = DefaultRenderTimeout
	}
	for _, d := range []struct {
		name string
		v    *int
		def  int
	}{{"TileWidth", &cfg.TileWidth, DefaultTileWidth}, {"TileHeight", &cfg.TileHeight, DefaultTileHeight}} {
		if *d.v == 0 {
			*d.v = d.def
		}
		if *d.v < minTilePx {
			return nil, fmt.Errorf("renderreview: %s %d must be at least %d px", d.name, *d.v, minTilePx)
		}
	}
	if math.IsNaN(cfg.Overlap) || cfg.Overlap < 0 || cfg.Overlap >= 0.5 {
		return nil, fmt.Errorf("renderreview: Overlap %v must be in [0, 0.5)", cfg.Overlap)
	}
	if cfg.Overlap == 0 {
		cfg.Overlap = DefaultOverlap
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &BrowserRenderer{cfg: cfg, logger: cfg.Logger, now: time.Now}, nil
}

// DetectBrowser resolves the browser binary: explicit (a path or a name on
// PATH) when non-empty, otherwise the first well-known Chrome/Chromium
// location for this OS. Failures wrap ErrBrowserUnavailable.
func DetectBrowser(explicit string) (string, error) {
	return findBrowser(explicit, browserCandidates(runtime.GOOS), exec.LookPath)
}

func findBrowser(explicit string, candidates []string, lookPath func(string) (string, error)) (string, error) {
	if explicit != "" {
		p, err := lookPath(explicit)
		if err != nil {
			return "", fmt.Errorf("%w: layout.browserpath %q: %v", ErrBrowserUnavailable, explicit, err)
		}
		return p, nil
	}
	for _, c := range candidates {
		if p, err := lookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: none of [%s] found; install Chrome or Chromium, or set layout.browserpath", ErrBrowserUnavailable, strings.Join(candidates, ", "))
}

func browserCandidates(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"google-chrome", "chromium",
		}
	case "windows":
		return []string{
			"chrome", "chrome.exe",
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
			"msedge", "msedge.exe",
		}
	default:
		return []string{
			"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
			"headless_shell", "headless-shell", "chrome",
			"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/snap/bin/chromium",
		}
	}
}

// Close stops the browser and the asset server. It is idempotent; later
// calls of Measure and Render fail.
func (b *BrowserRenderer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.stopBrowserLocked()
	if b.server != nil {
		b.server.close()
		b.server = nil
	}
}

func (b *BrowserRenderer) stopBrowserLocked() {
	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	b.browserCtx, b.browserCancel, b.allocCancel = nil, nil, nil
}

// ensureBrowser returns a live browser context and the asset server,
// (re)launching the browser when it is not running.
func (b *BrowserRenderer) ensureBrowser(ctx context.Context) (context.Context, *assetServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, errors.New("renderreview: renderer is closed")
	}
	if b.browserCtx != nil && b.browserCtx.Err() == nil {
		return b.browserCtx, b.server, nil
	}
	b.stopBrowserLocked() // a crashed browser is relaunched
	path, err := findBrowser(b.cfg.BrowserPath, browserCandidates(runtime.GOOS), exec.LookPath)
	if err != nil {
		return nil, nil, err
	}
	if b.server == nil {
		srv, err := newAssetServer(b.cfg.Assets)
		if err != nil {
			return nil, nil, err
		}
		b.server = srv
	}
	opts := append(slices.Clone(chromedp.DefaultExecAllocatorOptions[:]),
		chromedp.ExecPath(path),
		chromedp.Flag("font-render-hinting", "none"),
	)
	if b.cfg.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx,
		chromedp.WithErrorf(func(format string, args ...any) { b.logger.Debug("chromedp: " + fmt.Sprintf(format, args...)) }))
	launched := make(chan error, 1)
	go func() { launched <- chromedp.Do(browserCtx) }() // the first Do starts the browser
	select {
	case err = <-launched:
	case <-time.After(launchTimeout):
		err = fmt.Errorf("browser did not start within %s", launchTimeout)
	case <-ctx.Done():
		browserCancel()
		allocCancel()
		return nil, nil, fmt.Errorf("renderreview: starting browser: %w", ctx.Err())
	}
	if err != nil {
		browserCancel()
		allocCancel()
		return nil, nil, fmt.Errorf("%w: launching %s: %v", ErrBrowserUnavailable, path, err)
	}
	b.logger.Info("layout renderer started headless browser", "path", path, "no_sandbox", b.cfg.NoSandbox)
	b.browserCtx, b.browserCancel, b.allocCancel = browserCtx, browserCancel, allocCancel
	return browserCtx, b.server, nil
}

// Measure lays out every text item in the render surface's measure mode.
func (b *BrowserRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	doc, err := json.Marshal(measureJob{Mode: "measure", Request: req})
	if err != nil {
		return layout.Measurements{}, fmt.Errorf("renderreview: encode measure job: %w", err)
	}
	raw, err := b.runJob(ctx, doc, measureViewportW, measureViewportH, "__ARBITRON_MEASURE_RESULT__", nil)
	if err != nil {
		return layout.Measurements{}, err
	}
	var m layout.Measurements
	if err := json.Unmarshal(raw, &m); err != nil {
		return layout.Measurements{}, fmt.Errorf("renderreview: decode measurements: %w", err)
	}
	if !m.FontsReady {
		return layout.Measurements{}, fmt.Errorf("renderreview: measure surface reports fonts not ready (%s from %s)", req.Contract.FontFamily, req.Contract.FontPackage)
	}
	if m.Contract != req.Contract {
		return layout.Measurements{}, fmt.Errorf("renderreview: measure surface contract %q differs from the requested contract %q", m.Contract.Version, req.Contract.Version)
	}
	for _, it := range req.Items {
		if _, ok := m.Items[it.Key]; !ok {
			return layout.Measurements{}, fmt.Errorf("renderreview: measure surface returned no metrics for %q", it.Key)
		}
	}
	return m, nil
}

// Render draws one candidate with the shared renderer, returns the surface's
// DOM report and writes an overview plus readable-scale tiles under
// ArtifactDir/<revision>/<attempt>/<unique-capture>/.
func (b *BrowserRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	geo := req.Geometry
	if err := validateRenderRequest(req); err != nil {
		return layout.Rendering{}, err
	}
	zoom := geo.Contract.PracticalZoom
	tiles := PlanTiles(geo.Canvas, zoom, b.cfg.TileWidth, b.cfg.TileHeight, b.cfg.Overlap)
	if len(tiles) == 0 || len(tiles) > maxTiles {
		return layout.Rendering{}, fmt.Errorf("renderreview: canvas %.0f×%.0f needs %d tiles (limit %d)", geo.Canvas.W, geo.Canvas.H, len(tiles), maxTiles)
	}
	doc, err := renderJobDocument(req)
	if err != nil {
		return layout.Rendering{}, err
	}
	var artifacts []layout.Artifact
	capture := func(tabCtx context.Context) error {
		var err error
		artifacts, err = b.capture(tabCtx, req, tiles)
		return err
	}
	vw, vh := int64(math.Ceil(geo.Canvas.W*zoom)), int64(math.Ceil(geo.Canvas.H*zoom))
	raw, err := b.runJob(ctx, doc, vw, vh, "__ARBITRON_RENDER_REPORT__", capture)
	if err != nil {
		return layout.Rendering{}, err
	}
	var report layout.RenderReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return layout.Rendering{}, fmt.Errorf("renderreview: decode render report: %w", err)
	}
	return layout.Rendering{
		Revision:       req.Revision,
		Attempt:        req.Attempt,
		GeometryDigest: layout.GeometryDigest(geo),
		Report:         report,
		Artifacts:      artifacts,
		RenderedAt:     b.now().UTC(),
	}, nil
}

func validateRenderRequest(req layout.RenderRequest) error {
    for _, n := range req.Geometry.Nodes {
        typ, ok := req.WorkflowNodeTypes[n.ID]
        if !ok || typ == "" || strings.ReplaceAll(typ, "_", " ") != n.TypeCaption {
            return fmt.Errorf("renderreview: missing or mismatched node type for %s", n.ID)
        }
    }
    if len(req.WorkflowNodeTypes) != len(req.Geometry.Nodes) { return errors.New("renderreview: node type coverage differs") }
    if !slices.Equal(req.States, layout.RuntimeStates) { return errors.New("renderreview: runtime state coverage differs") }
	geo := req.Geometry
	zoom := geo.Contract.PracticalZoom
	switch {
	case !safeSegment.MatchString(req.Revision) || strings.Contains(req.Revision, ".."):
		return fmt.Errorf("renderreview: revision %q is not a safe artifact directory name", req.Revision)
	case req.Attempt < 1:
		return fmt.Errorf("renderreview: attempt %d must be at least 1", req.Attempt)
	case len(geo.Nodes) == 0:
		return errors.New("renderreview: geometry has no nodes")
	case !finite(geo.Canvas.X, geo.Canvas.Y, geo.Canvas.W, geo.Canvas.H) || geo.Canvas.W <= 0 || geo.Canvas.H <= 0:
		return fmt.Errorf("renderreview: geometry canvas %+v is empty", geo.Canvas)
	case !finite(zoom) || zoom <= 0:
		return fmt.Errorf("renderreview: contract practicalZoom %v must be positive", zoom)
	case geo.Canvas.W*zoom > maxViewportPx || geo.Canvas.H*zoom > maxViewportPx:
		return fmt.Errorf("renderreview: canvas %.0f×%.0f at zoom %v exceeds the %d px render surface limit", geo.Canvas.W, geo.Canvas.H, zoom, maxViewportPx)
	}
	return nil
}

type measureJob struct {
	Mode    string                `json:"mode"`
	Request layout.MeasureRequest `json:"request"`
}

type renderJob struct {
	Revision string `json:"revision"`
	Attempt int `json:"attempt"`
	Mode              string            `json:"mode"`
	Geometry          layout.Geometry   `json:"geometry"`
	WorkflowNodeTypes map[string]string `json:"workflowNodeTypes"`
	States            []string          `json:"states"`
}

// renderJobDocument is the job JSON of the render surface's render mode.
func renderJobDocument(req layout.RenderRequest) ([]byte, error) {
	doc, err := json.Marshal(renderJob{Mode: "render", Revision: req.Revision, Attempt: req.Attempt, Geometry: req.Geometry, WorkflowNodeTypes: req.WorkflowNodeTypes, States: req.States})
	if err != nil {
		return nil, fmt.Errorf("renderreview: encode render job: %w", err)
	}
	return doc, nil
}

// WorkflowNodeTypes is supplied by P1T9 from immutable Job.Manifest.Nodes[].Type;
// no caption-to-type inference is permitted. validateRenderRequest requires exactly
// one nonempty type per geometry node and its corresponding normalized type caption.

// runJob serves doc to a fresh tab, waits for the surface to finish and
// returns the JSON of the window global. after runs on the ready page.
func (b *BrowserRenderer) runJob(ctx context.Context, doc []byte, width, height int64, global string, after func(context.Context) error) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("renderreview: %w", err)
	}
	browserCtx, server, err := b.ensureBrowser(ctx)
	if err != nil {
		return nil, err
	}
	tabCtx, cancelTab := chromedp.NewContext(browserCtx)
	defer cancelTab()
	stop := context.AfterFunc(ctx, cancelTab) // caller cancellation closes the tab at once
	defer stop()
	callCtx, cancelCall := context.WithTimeout(tabCtx, b.cfg.RenderTimeout)
	defer cancelCall()

	token, release := server.register(doc)
	defer release()

	state := ""
	fail := func(what string, err error) error {
		switch {
		case ctx.Err() != nil:
			return fmt.Errorf("renderreview: %s: %w", what, ctx.Err())
		case errors.Is(callCtx.Err(), context.DeadlineExceeded):
			return fmt.Errorf("renderreview: render surface did not finish within %s (state %q): %w", b.cfg.RenderTimeout, state, context.DeadlineExceeded)
		case browserCtx.Err() != nil:
			return fmt.Errorf("%w: browser exited while %s: %v", ErrBrowserUnavailable, what, err)
		default:
			return fmt.Errorf("renderreview: %s: %w", what, err)
		}
	}

	if err := chromedp.Do(callCtx,
		chromedp.EmulateViewport(width, height),
		chromedp.Navigate(server.pageURL(token)),
	); err != nil {
		return nil, fail("loading the render surface", err)
	}
	for {
		s, err := chromedp.Run(callCtx, chromedp.Evaluate[string](`String(window.__ARBITRON_RENDER_STATE__ ?? "")`))
		if err != nil {
			return nil, fail("waiting for the render surface", err)
		}
		if state = s; state == "ready" || state == "error" {
			break
		}
		select {
		case <-callCtx.Done():
			return nil, fail("waiting for the render surface", callCtx.Err())
		case <-time.After(pollInterval):
		}
	}
	if state == "error" {
		msg, err := chromedp.Run(callCtx, chromedp.Evaluate[string](`String(window.__ARBITRON_RENDER_ERROR__ ?? "unknown error")`))
		if err != nil {
			return nil, fail("reading the render surface error", err)
		}
		return nil, fmt.Errorf("renderreview: render surface failed: %s", msg)
	}
	raw, err := chromedp.Run(callCtx, chromedp.Evaluate[string](`JSON.stringify(window.`+global+` ?? null)`))
	if err != nil {
		return nil, fail("reading "+global, err)
	}
	if raw == "null" {
		return nil, fmt.Errorf("renderreview: render surface is ready but published no %s", global)
	}
	if after != nil {
		if err := after(callCtx); err != nil {
			return nil, fail("capturing screenshots", err)
		}
	}
	return []byte(raw), nil
}

// capture writes the overview and every tile as PNG files.
func (b *BrowserRenderer) capture(ctx context.Context, req layout.RenderRequest, tiles []layout.Rect) ([]layout.Artifact, error) {
	geo := req.Geometry
	zoom := geo.Contract.PracticalZoom
	captureRoot := filepath.Join(b.cfg.ArtifactDir, req.Revision, strconv.Itoa(req.Attempt))
	if err := os.MkdirAll(captureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact dir: %w", err)
	}
	dir, err := os.MkdirTemp(captureRoot, "capture-*")
	if err != nil {
		return nil, fmt.Errorf("create immutable capture directory: %w", err)
	}
	overviewScale := math.Min(1, maxOverviewPx/math.Max(geo.Canvas.W*zoom, geo.Canvas.H*zoom))
	artifacts := make([]layout.Artifact, 0, len(tiles)+1)
	shoot := func(kind, name string, region layout.Rect, clipScale float64) error {
		clip := page.Viewport{
			X: (region.X - geo.Canvas.X) * zoom, Y: (region.Y - geo.Canvas.Y) * zoom,
			Width: region.W * zoom, Height: region.H * zoom, Scale: clipScale,
		}
		png, err := chromedp.Run(ctx, screenshot(clip))
		if err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, png, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		sum := sha256.Sum256(png)
		artifacts = append(artifacts, layout.Artifact{Kind: kind, Path: path, SHA256: hex.EncodeToString(sum[:]), Region: region, Scale: zoom * clipScale})
		return nil
	}
	if err := shoot("overview", "overview.png", geo.Canvas, overviewScale); err != nil {
		return nil, err
	}
	for i, tile := range tiles {
		if err := shoot("tile", fmt.Sprintf("tile-%03d.png", i), tile, 1); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

// screenshot captures one page-px region as PNG.
func screenshot(clip page.Viewport) chromedp.Action[[]byte] {
	return func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		fromSurface := true
		res, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format:      page.CaptureScreenshotFormatPng,
			Clip:        &clip,
			FromSurface: &fromSurface,
		})
		if err != nil {
			return nil, err
		}
		return res.Data, nil
	}
}
```

Tidy the module so chromedp and cdproto move to the direct block:

```bash
go mod tidy && git diff --stat go.mod go.sum && git diff go.mod
```
Expected: the `go.mod` diff adds exactly `github.com/chromedp/cdproto v0.157.8` and `github.com/chromedp/chromedp v0.21.0` to the first `require` block, and `github.com/go-json-experiment/json v0.0.0-20260213210345-44df1a37e875 // indirect` to the second. Nothing else changes.

- [x] **Step 5: Run the no-browser tests and confirm they pass** (these run on every machine, including CI without Chrome).

```bash
gofmt -l adapter/renderreview/ && go vet ./adapter/renderreview/ && go test ./adapter/renderreview/ -run 'TestNewBrowserRenderer|TestBrowserRenderer_MissingBrowserIsUnavailable|TestBrowserRenderer_BrowserThatCannotStartIsUnavailable|TestFindBrowser|TestBrowserCandidates|TestRenderJobDocument|TestRender_RejectsInvalidRequestBeforeLaunching|TestBrowserRenderer_ClosedRendererFails' -v
```
Expected: `gofmt` and `vet` are silent. The output shows `--- PASS` for `TestNewBrowserRenderer_AppliesDefaultsWithoutStartingAnything`, `TestNewBrowserRenderer_RejectsInvalidConfig` (8 subtests), `TestBrowserRenderer_MissingBrowserIsUnavailable`, `TestBrowserRenderer_BrowserThatCannotStartIsUnavailable` (under 1 s: the fake browser exits 3), `TestFindBrowser`, `TestBrowserCandidates_CoverEachPlatform`, `TestRenderJobDocument_MatchesSurfaceProtocol`, `TestRender_RejectsInvalidRequestBeforeLaunching` (7 subtests) and `TestBrowserRenderer_ClosedRendererFails`, then `ok`.

- [x] **Step 6: Run the real-browser protocol tests and confirm they pass** (requires Chrome or Chromium; `ARBITRON_REQUIRE_BROWSER=1` turns a missing browser into a failure instead of a skip).

```bash
ARBITRON_REQUIRE_BROWSER=1 go test -race ./adapter/renderreview/ -run 'TestBrowserRenderer_(MeasureProtocolRoundTrip|MeasureRejectsUntrustworthyResults|RenderProtocolAndArtifacts|SurfaceErrorIsReported|TimesOutWhenSurfaceNeverFinishes|CancellationAbortsPromptly|ConcurrentCallsUseSeparateTabs)' -v
```
Expected: one log line `INFO layout renderer started headless browser path=<chrome path> no_sandbox=<false|true>` per test. Then `--- PASS` for all seven, with `MeasureRejectsUntrustworthyResults` showing 4 subtests, and `ok  	github.com/arbitron/arbitron/adapter/renderreview` (about 10–15 s). Without `ARBITRON_REQUIRE_BROWSER=1` on a machine without Chrome, these seven print `--- SKIP` with `no Chrome/Chromium available`.

- [x] **Step 7: Commit.**

```bash
git add go.mod go.sum adapter/renderreview/browser.go adapter/renderreview/browser_test.go && git commit -m "feat(renderreview): headless-Chrome renderer driving the render surface (chromedp)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5.4: Export the embedded dist, re-embed `render.html` (`make ui`) and prove the real surface

**Files:**
- Modify: `adapter/dashboard/embed.go:13-21` (unexported `assets()` becomes exported `Assets()`) and `:26-30` (`Handler` reuses `Assets()`). The whole file is shown below.
- Modify: `adapter/dashboard/export_test.go:6` (`AssetsForTest` delegates to `Assets()`)
- Modify (generated): `adapter/dashboard/dist/**` via `make ui` (adds `render.html`, `assets/render-*.js|css` and the Inter `woff2` files, and refreshes `index.html` and its assets)
- Test:   `adapter/dashboard/assets_test.go` (new file; `embed_test.go` is untouched)
- Test:   `adapter/renderreview/surface_test.go` (real-surface provenance probes)

**Interfaces:** Produces `func Assets() fs.FS`. Consumes `layout.BuildGraph`, `MeasureRequestFor`, `Arrange`, `DefaultOptions`, `NewRenderRequest` (Phase 1), `workflow.Workflow` (domain/workflow) and `samples/pr-review/workflow.json`. This task carries out P3T4 Step 6's hand-off: "The embedded copy is refreshed and committed by P3T5 (`make ui`)".

- [x] **Step 1: Write the failing tests.** Create `adapter/dashboard/assets_test.go`:

```go
package dashboard_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/dashboard"
)

// The Go browser renderer (adapter/renderreview) serves Assets() to headless
// Chrome and navigates to /render.html; the embedded build must contain it
// and every asset it references.
func TestAssets_ContainsRenderSurface(t *testing.T) {
	assets := dashboard.Assets()
	for _, name := range []string{"index.html", "render.html"} {
		if _, err := fs.Stat(assets, name); err != nil {
			t.Fatalf("embedded dashboard is missing %s (run `make ui`): %v", name, err)
		}
	}
	raw, err := fs.ReadFile(assets, "render.html")
	if err != nil {
		t.Fatalf("reading render.html: %v", err)
	}
	if !regexp.MustCompile(`/assets/render-[A-Za-z0-9_-]+\.js`).Match(raw) {
		t.Fatalf("render.html does not load its built entry chunk:\n%s", raw)
	}
	for _, ref := range regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.(?:js|css)`).FindAll(raw, -1) {
		if _, err := fs.Stat(assets, strings.TrimPrefix(string(ref), "/")); err != nil {
			t.Errorf("render.html references %s, which is not embedded: %v", ref, err)
		}
	}
}
```

Create `adapter/renderreview/surface_test.go`. These are the provenance probes P1T4 and P3T4 assign to P3T5. The long Unicode label must wrap with `Lines > 1` and `Width <= MaxWidth`. The geometry is a real one, produced by `layout.Arrange` over `samples/pr-review/workflow.json` with **browser** measurements. The report must cover the geometry, carry the same contract, have fonts ready and match the drawn positions. The artifacts must exist on disk with matching sha256, and the tiles must cover the canvas.

```go
package renderreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embeddedSurface is the production asset FS. These tests are the provenance
// probes of the P3T4 surface: they fail when render.html is missing from the
// embedded build, cannot mount the shared renderer, or reports nothing.
func embeddedSurface(t *testing.T) fs.FS {
	t.Helper()
	assets := dashboard.Assets()
	_, err := fs.Stat(assets, "render.html")
	require.NoError(t, err, "adapter/dashboard/dist has no render.html; run `make ui` (P3T5 Task 5.4)")
	return assets
}

func loadSampleWorkflow(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", name, "workflow.json"))
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	return &wf
}

func TestBrowserRenderer_RealSurfaceMeasure(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	c := layout.DefaultContract()
	long := "Überprüfe sämtliche Änderungen im Pull-Request — 審查所有拉取請求 — und bestätige jede Abhängigkeit gründlich"
	req := layout.MeasureRequest{Contract: c, Items: []layout.TextItem{
		{Key: "long", Text: long, Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "short", Text: "Start", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "type", Text: "agent call", Style: layout.StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "branch", Text: "approved", Style: layout.StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
		{Key: "empty", Text: "", Style: layout.StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
		{Key: "wide", Text: "WWWWWWWW", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "narrow", Text: "iiiiiiii", Style: layout.StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
	}}

	m, err := b.Measure(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, m.FontsReady, "the bundled Inter faces were loaded")
	assert.Equal(t, c, m.Contract, "the surface measures with the same renderer contract")

	lm := m.Items["long"]
	assert.Greater(t, lm.Lines, 1, "a long Unicode label wraps instead of being clipped")
	assert.LessOrEqual(t, lm.Width, c.NodeTextMaxWidth, "wrapped text never exceeds the wrap width")
	assert.Greater(t, lm.Width, c.NodeTextMaxWidth/2)
	assert.GreaterOrEqual(t, lm.Height, float64(lm.Lines)*c.NodeLabelFontPx*c.LineHeight-1)

	sm := m.Items["short"]
	assert.Equal(t, 1, sm.Lines)
	assert.Greater(t, sm.Width, 0.0)
	assert.Less(t, sm.Width, c.NodeTextMaxWidth)
	assert.InDelta(t, math.Ceil(c.NodeLabelFontPx*c.LineHeight), sm.Height, 1)

	assert.Equal(t, 1, m.Items["type"].Lines)
	assert.Equal(t, 1, m.Items["branch"].Lines)
	assert.Less(t, m.Items["branch"].Width, c.EdgeLabelMaxWidth)
	assert.Equal(t, layout.TextMetrics{}, m.Items["empty"])
	assert.Greater(t, m.Items["wide"].Width, 2*m.Items["narrow"].Width, "real proportional glyph metrics, not a per-character guess")
}

func TestBrowserRenderer_RealSurfaceRender(t *testing.T) {
	cfg := testBrowserConfig(t, embeddedSurface(t))
	cfg.TileWidth, cfg.TileHeight = 640, 480 // force several tiles on a sample-sized canvas
	b := newTestRenderer(t, cfg)
	ctx := context.Background()

	wf := loadSampleWorkflow(t, "pr-review")
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	m, err := b.Measure(ctx, layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := layout.Arrange(g, m, c, layout.DefaultOptions())
	require.NoError(t, err)

	nodeTypes := make(map[string]string, len(wf.Nodes))
	for _, n := range wf.Nodes { nodeTypes[n.ID] = string(n.Type) }
	req := layout.NewRenderRequest("rev-pr-review", 1, geo, nodeTypes)
	before := time.Now().UTC()
	r, err := b.Render(ctx, req)
	require.NoError(t, err)

	// Identity: the rendering is tied to this exact candidate.
	assert.Equal(t, "rev-pr-review", r.Revision)
	assert.Equal(t, 1, r.Attempt)
	assert.Equal(t, layout.GeometryDigest(geo), r.GeometryDigest)
	assert.False(t, r.RenderedAt.Before(before.Add(-time.Second)))

	rep := r.Report
	assert.Equal(t, layout.ContractVersion, rep.SurfaceVersion)
	assert.Equal(t, geo.Contract, rep.Contract)
	assert.True(t, rep.FontsReady)
	assert.True(t, rep.FontFamilyResolved)
	assert.Equal(t, c.PracticalZoom, rep.Zoom)
	assert.Equal(t, geo.Canvas, rep.Canvas)

	// Coverage: every node and edge of the candidate was drawn where the geometry put it.
	require.Len(t, rep.Nodes, len(geo.Nodes))
	byID := map[string]layout.RenderedNode{}
	for _, n := range rep.Nodes {
		byID[n.ID] = n
	}
	for _, n := range geo.Nodes {
		got, ok := byID[n.ID]
		require.True(t, ok, "node %s missing from the report", n.ID)
		assert.InDelta(t, n.Box.X, got.Box.X, 1.5, "node %s x", n.ID)
		assert.InDelta(t, n.Box.Y, got.Box.Y, 1.5, "node %s y", n.ID)
		assert.InDelta(t, n.Box.W, got.Box.W, 1.5, "node %s w", n.ID)
		assert.InDelta(t, n.Box.H, got.Box.H, 1.5, "node %s h", n.ID)
		assert.Greater(t, got.Label.FontPx, 0.0, "node %s label was laid out", n.ID)
	}
	require.Len(t, rep.Edges, len(geo.Edges))
	for i, e := range rep.Edges {
		require.Equal(t, i, e.Ordinal, "edges are reported in ordinal order")
		route := geo.Edges[i]
		assert.True(t, e.HasArrow, "edge %d has an arrowhead", e.Ordinal)
		assert.InDelta(t, route.Points[0].X, e.Start.X, 2, "edge %d start x", e.Ordinal)
		assert.InDelta(t, route.Points[0].Y, e.Start.Y, 2, "edge %d start y", e.Ordinal)
		last := route.Points[len(route.Points)-1]
		assert.InDelta(t, last.X, e.End.X, 2, "edge %d end x", e.Ordinal)
		assert.InDelta(t, last.Y, e.End.Y, 2, "edge %d end y", e.Ordinal)
	}
	assert.Len(t, rep.Crossings, len(geo.Crossings))
	require.Len(t, rep.States, len(layout.RuntimeStates))
	for i, s := range layout.RuntimeStates {
		assert.Equal(t, s, rep.States[i].State)
	}

	// Evidence: overview + tiles on disk, hashed, covering the whole canvas.
	tiles := PlanTiles(geo.Canvas, c.PracticalZoom, 640, 480, DefaultOverlap)
	require.GreaterOrEqual(t, len(tiles), 2)
	require.Len(t, r.Artifacts, len(tiles)+1)
	assert.Equal(t, "overview", r.Artifacts[0].Kind)
	assert.Equal(t, geo.Canvas, r.Artifacts[0].Region)
	assert.GreaterOrEqual(t, distinctColors(t, r.Artifacts[0].Path), 3, "the overview shows the drawn graph")
	regions := make([]layout.Rect, 0, len(tiles))
	for i, a := range r.Artifacts {
		assert.Equal(t, filepath.Join(cfg.ArtifactDir, "rev-pr-review", "1"), filepath.Dir(filepath.Dir(a.Path)))
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		sum := sha256.Sum256(raw)
		assert.Equal(t, hex.EncodeToString(sum[:]), a.SHA256, a.Path)
		img, err := png.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err)
		assert.InDelta(t, a.Region.W*a.Scale, float64(img.Width), 2, a.Path)
		assert.InDelta(t, a.Region.H*a.Scale, float64(img.Height), 2, a.Path)
		if i == 0 {
			continue
		}
		assert.Equal(t, "tile", a.Kind)
		assert.Equal(t, c.PracticalZoom, a.Scale, "tiles are captured at practical zoom")
		assert.Equal(t, tiles[i-1], a.Region)
		regions = append(regions, a.Region)
	}
	assertCovers(t, geo.Canvas, regions)
}
```

- [x] **Step 2: Run them and confirm they fail.**

```bash
go test ./adapter/dashboard/ ./adapter/renderreview/ -run 'TestAssets_ContainsRenderSurface|TestBrowserRenderer_RealSurface' -v
```
Expected: both packages fail to build:
```text
adapter/dashboard/assets_test.go:16:22: undefined: dashboard.Assets
adapter/renderreview/surface_test.go:29:22: undefined: dashboard.Assets
FAIL	github.com/arbitron/arbitron/adapter/dashboard [build failed]
FAIL	github.com/arbitron/arbitron/adapter/renderreview [build failed]
```

- [x] **Step 3: Export the sub-FS.** Replace `adapter/dashboard/embed.go` with:

```go
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var distFS embed.FS

// Assets returns the embedded dist/ sub-filesystem: the dashboard SPA
// (index.html) and the deterministic render surface (render.html) that
// adapter/renderreview serves to headless Chrome. Callers can use fs.ReadDir
// to discover the actual (content-hashed) asset filenames at runtime.
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("failed to create sub filesystem: " + err.Error())
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded dashboard SPA.
// Static assets under /assets/ are served with immutable cache headers.
// All other paths fall back to index.html for client-side routing.
func Handler() http.Handler {
	sub := Assets()

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Try to serve the exact file if it exists.
		if path != "/" && !strings.HasSuffix(path, "/") {
			if f, err := sub.Open(strings.TrimPrefix(path, "/")); err == nil {
				f.Close()
				if strings.Contains(path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// SPA fallback: serve index.html for client-side routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
```

Replace `adapter/dashboard/export_test.go` with:

```go
package dashboard

import "io/fs"

// AssetsForTest exports the embedded dist FS for the existing handler tests.
func AssetsForTest() fs.FS { return Assets() }
```

- [x] **Step 4: Run again and confirm the probes now fail on the stale embedded build.**

Observed: dashboard asset test and both real-surface tests failed because embedded render.html was absent, after resolving the missing Assets symbol.

```bash
go test ./adapter/dashboard/ -run TestAssets_ContainsRenderSurface -v; ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ -run TestBrowserRenderer_RealSurface -v
```
Expected (the committed `adapter/dashboard/dist` predates P3T4):
```text
--- FAIL: TestAssets_ContainsRenderSurface (0.00s)
    assets_test.go:19: embedded dashboard is missing render.html (run `make ui`): open render.html: file does not exist
FAIL	github.com/arbitron/arbitron/adapter/dashboard
--- FAIL: TestBrowserRenderer_RealSurfaceMeasure
    surface_test.go:31: ... adapter/dashboard/dist has no render.html; run `make ui` (P3T5 Task 5.4)
--- FAIL: TestBrowserRenderer_RealSurfaceRender
FAIL	github.com/arbitron/arbitron/adapter/renderreview
```

- [x] **Step 5: Rebuild and re-embed the dashboard.**

Observed: make ui succeeded; both HTML entries, render entry JS/CSS, 28 Inter WOFF2 files and bundled Noto assets are embedded.

```bash
make ui && ls adapter/dashboard/dist/index.html adapter/dashboard/dist/render.html && grep -o '/assets/render-[A-Za-z0-9_-]*\.js' adapter/dashboard/dist/render.html && ls adapter/dashboard/dist/assets | grep -c 'inter-.*\.woff2'
```
Expected:
- `npm ci` and `vite build` succeed, and the build output lists `dist/index.html`, `dist/render.html` and `dist/assets/render-<hash>.js|css`.
- The copy into `adapter/dashboard/dist` succeeds.
- `ls` prints both html paths.
- `grep` prints one line `/assets/render-<hash>.js`.
- The final count is at least `4` (the Inter 400/500/600/700 `latin` faces; more subsets may be emitted).

- [x] **Step 6: Run the probes and confirm they pass.**

Observed: all dashboard tests and actual embedded-surface probes pass. Extended assertions cover DOM content/glyphs/visibility/contrast, actual segments/styles/marker outlines, runtime stability, Unicode/newlines, missing font bytes, unsupported glyphs, and nil measurement rejection at Arrange. Production DOM mutation tests cover ancestor/path/marker opacity, changed text, reversed marker, running-only label displacement and middle-route changes. P3T4's full real-browser collector fixture also passes (including crossing neighborhoods, CSS marker transforms and animation midpoints).

```bash
go test ./adapter/dashboard/ -v && ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ -run TestBrowserRenderer_RealSurface -v
```
Expected: `adapter/dashboard` shows `--- PASS` for `TestAssets_ContainsRenderSurface`, `TestHandler_ServesIndexHTML`, `TestHandler_SPAFallback` and `TestHandler_ServesStaticAsset`, then `ok`. Then `--- PASS: TestBrowserRenderer_RealSurfaceMeasure` and `--- PASS: TestBrowserRenderer_RealSurfaceRender`, then `ok  	github.com/arbitron/arbitron/adapter/renderreview`.

A failure here is a real defect in the shared renderer or surface (P3T2–P3T4), for example a node box that moved, a missing arrowhead, unloaded fonts or a contract mismatch. Fix it at its source task. Do not loosen these assertions.

- [x] **Step 7: Commit the export, the probes and the refreshed embedded build.**

```bash
git add adapter/dashboard/embed.go adapter/dashboard/export_test.go adapter/dashboard/assets_test.go adapter/renderreview/surface_test.go && git add -A adapter/dashboard/dist && git commit -m "feat(dashboard): export embedded assets and re-embed render.html for the browser renderer" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5.5: Package gate

**Files:** none new (verification only).

- [x] **Step 1: Run the whole package, with and without a browser requirement, plus the race detector.**

```bash
gofmt -l adapter/renderreview adapter/dashboard && go vet ./adapter/renderreview/ ./adapter/dashboard/ && go test ./adapter/renderreview/ ./adapter/dashboard/ && ARBITRON_REQUIRE_BROWSER=1 go test -race -count=1 ./adapter/renderreview/
```
Expected: `gofmt` and `vet` are silent. Then `ok  	github.com/arbitron/arbitron/adapter/renderreview` and `ok  	github.com/arbitron/arbitron/adapter/dashboard`. On a host without Chrome the first `go test` still prints `ok`, because the browser tests skip. With Chrome, the race run prints `ok` with no `WARNING: DATA RACE`.

- [x] **Step 2: Confirm the whole module still builds and tests.**

> Historical full-gate failure (contracts §6.18 now corrects the sample-profile assumption; fresh full verification remains required): go build ./... passes; go test ./... reaches an unrelated existing adapter/config TestSampleFeaturePipeline_ValidatesWithDefaultProfiles failure: the sample references profiles absent from its default-profile fixture (e.g. assessor-sonnet5-high, interviewer-codex, coder-pi-sol-high). Original task-package/browser/race gates passed. On the production-completion revisit, build/vet and the new root acceptance/defect race suite pass, but a fresh whole-adapter run exceeded 480s under host contention (load averages 47/95/67). A bounded focused adapter race run passed capture-failure cleanup, both-capture reviewer consumption, real measure/render surfaces and embedded assets; its independent concurrent-capture test failed at Chrome's unchanged 30s launch deadline before capture. No deadline/assertion was weakened. Leave the whole-module gate unchecked and skip these recorded baseline/environment blockers rather than repeatedly re-failing them.

```bash
go build ./... && go test ./...
```
Expected: every test-bearing package `ok` (packages without tests report `[no test files]`). P3T7 now wires this adapter through `cmd/arbitron/layout_wiring.go`; the original pre-wiring expectation is historical.

> Resolved on the full-module verification revisit: the exact `go build ./... && go test ./...` command passes. The documented-profile fixture correction is already present; no sample, default profile, review threshold or timeout was changed. The package/race and real-root results, including an initial transient failure, are recorded below.

- [x] **Step 3: Commit** (only if Step 1 required formatting fixes; otherwise nothing to commit).

```bash
git add adapter/renderreview adapter/dashboard && git commit -m "chore(renderreview): gofmt" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Wired-and-fed

| Item | Wiring task (composition root) | Data source (production producer) |
|---|---|---|
| `renderreview.BrowserRenderer` as `layout.Renderer` | **P3T7**: `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` builds `renderreview.NewBrowserRenderer(renderreview.BrowserConfig{Assets: dashboard.Assets(), BrowserPath: cfg.Layout.BrowserPath, NoSandbox: cfg.Layout.BrowserNoSandbox, ArtifactDir: <cfg.Layout.ArtifactDir or <dir of database.path>/layout-artifacts>, Logger: logger})`. It returns the renderer to `layout.NewCoordinator` (P2T9 seam) and its `Close` as the cleanup func. It replaces P2T9's `rendererUnavailable`. | `Measure` requests come from `domain/layout/coordinator.go:(*Coordinator).Run` (P1T9), which calls `MeasureRequestFor(BuildGraph(job.Manifest))`. `Render` requests carry **candidate geometry** from `Arrange` inside `Coordinator.Run` (P1T9), via `NewRenderRequest(job.Revision, attempt, geo, nodeTypes)`. `Job` is built by `domain/layout/worker.go:(*Worker).ProcessRevision` (P2T8) from the stored row's captured manifest (P2T2–P2T6). |
| `BrowserConfig.BrowserPath`, `NoSandbox`, `ArtifactDir` | P3T7 (above) | `domain/config.LayoutConfig.BrowserPath`, `BrowserNoSandbox`, `ArtifactDir` loaded by `adapter/config/loader.go` (P2T1) from YAML `layout.*` and env `ARBITRON_LAYOUT_*` |
| `BrowserConfig.Assets` | P3T7 passes `dashboard.Assets()` | `adapter/dashboard/dist` embedded by `//go:embed dist/*`, refreshed by `make ui` (this task, and again by P3T14). In Docker, the `dashboard-build` stage writes it, so `render.html` is always present in the image. |
| `Rendering` (report + artifacts) | consumed by P1T9 `Coordinator.Run` → `Reviewer.Review` | P3T6 `ReadabilityReviewer` reads `Report` and the PNG artifacts. Its Evidence carries `GeometryDigest`, `Contract`, `FontsReady` and `Artifacts` into the coordinator pass rule (contracts §1). |

Provenance probes (fail when the input is nil or empty, or the wiring is absent):
- `TestBrowserRenderer_MissingBrowserIsUnavailable` shows that a configured but absent browser makes Measure and Render fail with `ErrBrowserUnavailable`, writes no artifacts and starts no server. P1T8/P1T9/P3T7 rely on this to produce a visible `failed` (stage rendering), never a pass.
- `TestBrowserRenderer_RenderProtocolAndArtifacts` and `TestBrowserRenderer_MeasureProtocolRoundTrip` assert at the sink, which is the page. The page only answers about what it actually fetched: node ids suffixed with the types it received, `refetch-404`, and the echoed keys. A Go side that sent an empty or wrong job fails them.
- `TestBrowserRenderer_RealSurfaceMeasure` and `TestBrowserRenderer_RealSurfaceRender` fail when `render.html` is missing from `dashboard.Assets()`, when the page cannot mount `ArrangedGraph`, when the report is empty or does not cover the geometry, or when artifacts are missing or mis-hashed.
- P3T7 adds the composition-root acceptance test (`cmd/arbitron/layout_render_e2e_test.go`, with `requireBrowserE2E`). It drives `initDomain` and asserts the stored review evidence at the sink. That test can use `renderreview.DetectBrowser`.

## Runtime notes for P3T7 (Docker / CI)

- **Browser.** The runtime image needs Chrome or Chromium. On `alpine:3.20`: `apk add chromium` (pin the version as the Dockerfile does for other packages). It installs `/usr/bin/chromium-browser` (and `/usr/bin/chromium`), and auto-detection finds both, so setting `ARBITRON_LAYOUT_BROWSERPATH` is optional.
- **Fonts.** The contract font, Inter, ships inside the embedded dist via `@fontsource/inter`, so labels in Latin script need no system fonts. CJK/emoji use bundled Noto Sans SC/Noto Emoji (P3T2), with exact text-range/weight readiness and cmap/sequence checks. System noto packages are optional general runtime support and cannot establish renderer parity. Unsupported glyphs fail explicitly before measurement/rendering; a tofu rectangle cannot be counted as readable ink.
- **Sandbox.** Inside Docker, Chrome's sandbox usually cannot start: as root it refuses outright, and as the non-root `arbitron` user it needs user namespaces or seccomp, which default Docker lacks. Set `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true` in the image ENV (maps to `BrowserConfig.NoSandbox`). It is only safe because the browser loads nothing but the loopback asset server.
- **Shared memory.** `/dev/shm` is not a concern: chromedp's defaults include `--disable-dev-shm-usage`.
- **Artifact dir.** `ArtifactDir` must be writable by the server user. The default is `<dir of database.path>/layout-artifacts`, which is under `/data` in the image.
- **CI.** `make test-render` (P3T7) runs `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ ./cmd/arbitron/ -run '...'`, so the real-browser tests cannot silently skip.
- **Startup failures.** `NewBrowserRenderer` returns an error (not `ErrBrowserUnavailable`) if `Assets` has no `render.html` or `ArtifactDir` is empty. Both are build or config defects. P3T7 fails startup for a missing render.html or invalid configuration. A missing browser fails each job visibly; it never reinstalls rendererUnavailable.

## Self-review

- **Names vs contracts §3 and addendum 1.** All match exactly: `Assets() fs.FS`, `assetServer`, `BrowserConfig{Assets, BrowserPath, ArtifactDir, Logger, RenderTimeout, TileWidth, TileHeight, Overlap, NoSandbox}`, `BrowserRenderer`, `NewBrowserRenderer`, `Measure`, `Render`, `Close`, `ErrBrowserUnavailable` (same message text), `PlanTiles(canvas, zoom, tileW, tileH int, overlap float64) []layout.Rect`, and `requireBrowser(t)` in `browser_test.go` honouring `ARBITRON_REQUIRE_BROWSER=1`.
- **Protocol vs P3T4.** These all match P3T4's `job.ts` and `main.tsx`:
  - The token is 32 lowercase hex, matching `TOKEN_RE`.
  - The URL is `/render.html?job=`, and the job path is `/__render/jobs/<token>.json`.
  - Job keys are `mode`/`geometry`/`workflowNodeTypes`/`states` and `mode`/`request`.
  - Globals are `__ARBITRON_RENDER_STATE__` (`ready`/`error`), `__ARBITRON_RENDER_ERROR__`, `__ARBITRON_RENDER_REPORT__` and `__ARBITRON_MEASURE_RESULT__`.
  - The render viewport is `canvas×zoom`, so `elementFromPoint` works everywhere.
  - Tile clips use page px = `(canvas − origin) × zoom`.
  - RenderRequest.WorkflowNodeTypes comes directly from the immutable manifest; renderJobDocument copies it verbatim, including names containing underscores/spaces.
- **Additions (not in contracts).** `DetectBrowser(explicit string) (string, error)`, and the exported constants `DefaultTileWidth`, `DefaultTileHeight`, `DefaultOverlap`, `MinTileOverlapPx`, `DefaultRenderTimeout`. `MinTileOverlapPx` was agreed with P3T6.
- **Semantics chosen.** `Overlap` is a fraction in `[0, 0.5)`, with 0 meaning the default. Artifact paths and scales are as listed under Behaviour.
- **Implementation scope.** The request/resource regression steps are required alongside the examples; commands and expected outputs are predictions to verify. Real-browser steps state the exact skip/fail switch.

### Alignment TDD: immutable capture across concurrent retries

**Files:** `adapter/renderreview/browser.go`, `browser_test.go`.

- [x] **RED:** Add TestBrowserRenderer_CaptureIsImmutableAcrossRetry: render two different
  candidates using the same Revision/Attempt through independent renderers and concurrently;
  the returned paths differ, original bytes/hashes stay unchanged, and both reviews can
  read their own artifacts. The directory assertion checks the unique child below
  revision/attempt, never a deterministic filename reused by every generation.
- [x] Run `ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview -run TestBrowserRenderer_ -count=1 -race` and observe the behavioral failure before implementing the correction.

Observed: failed capture left its directory behind before cleanup was implemented. A deterministic capture-directory mutation additionally failed reused-path and unchanged-original-byte assertions; restored MkdirTemp. Independent renderer instances and concurrent same-attempt captures now retain distinct actual PNG bytes/paths. Reviewer consumption remains in the production-completion gate below.
- [x] **GREEN:** Use the unique-capture implementation in Task 5.3; never RemoveAll a published
  evidence directory. Keep source reports tied to the actual capture. On failure, clean
  only this call's unpublished directory. Browser timeout/cancellation and file errors
  remain explicit and retryable.
- [x] Rerun the command, then the owning package/typecheck gate. Temporarily remove the
  corrected input/wiring/guard and verify the regression assertion fails; restore it.
- [x] Commit the source/tests with this task. These checks are additional to the existing
  happy/success, failure, error and edge cases; fixture-only success is insufficient.

### Request and resource boundary regression gates

- [x] Assert renderJobDocument preserves revision, attempt and exact manifest node types
  at the page sink. Mutate the map to nil/wrong type and require rejection before launch.
  Verify constructor defensive copies: mutating the source map after NewRenderRequest
  cannot change the registered job or the later report.
- [x] Measure rejects nil/unknown contract, duplicate/empty keys, invalid metrics (NaN,
  negative sizes, nonpositive lines for nonempty text) and a missing requested item.
  Explicit empty text gives zero metrics, never generic 7px-per-character production data.
- [x] The generated renderer example is incomplete until these request checks are in the
  main methods, not just tests. Nil result globals and empty reports remain failures.
- [x] On capture/decode failure remove only that call's unpublished unique directory.
  Bound tile planning before allocating spans and test overflow/extreme dimensions.
  RenderTimeout includes waiting for the renderer mutex and browser launch: a cancelled
  queued caller must not wait behind an unrelated long launch. Close waits for in-flight
  calls to finish/cancel before clearing their server/browser collaborators; test -race.

**Library source verification:** The generic API in this plan was checked against the
actual downloaded github.com/chromedp/chromedp@v0.21.0 source (action.go Run/Do, eval.go
Evaluate, emulate.go EmulateViewport) and cdproto@v0.157.8 (cdp.Call and CaptureScreenshot).
The dependency is absent from this repository until Task 5.3; do not claim it is already
wired. Recheck APIs when resolving the pinned dependency during implementation.

## Production completion and execution evidence

- [x] **Production wired-and-fed acceptance:** exercise saved-manifest inputs through the real composition root and consume both immutable retry captures with the production reviewer.

Resolved on revisit: P3T6/P3T7 now construct the actual BrowserRenderer/ReadabilityReviewer in initDomain. `cmd/arbitron/layout_render_capture_e2e_test.go:TestLayoutRender_RealBrowserRetryPreservesBothCaptures` authors Unicode labels and a branch through the real Service, captures a genuinely blank embedded surface, observes blocking raster review in SQLite and authorized QueryService, closes the root, restores the surface, boots a fresh root and retries the same revision through authorized RetryWorkflow. The second generation passes with different real PNGs. Both generation results, original PNG bytes/hashes and exact manifest bytes survive; both captures are re-opened by the production reviewer after Retry, preserving the blank-capture rejection and repaired-capture pass. The observer delegates every load-bearing argument unchanged; no report, geometry, measurement, artifact or verdict is invented. Existing `TestLayoutRender_RealSurfaceReportDefectsReachSavedResults` additionally proves actual text/marker/path/runtime/blank-ink defects reach persisted findings.

Self-review (in order):
1. Spec compliance: retained the exact renderer/config/page/asset/tile interfaces and pinned generic chromedp/cdproto APIs (checked downloaded source). Added only required validation/resource/immutable-evidence behavior; no feature flag or replacement reviewer. Production completion remains explicitly blocked above.
2. Anti-green-theater: assertions read actual one-shot page inputs, DOM content/paths/markers/states and hashed PNG dimensions. Changing the capture directory to a reused path made concurrent retry path/byte assertions fail; restored unique captures. Missing font bytes, unsupported glyphs, missing report globals, malformed/empty reports, absent metrics, bad type/state maps and nil sizing input all fail at their consumers. DOM mutations reach the real embedded report, not candidate echoes. Existing P3T4 collector mutation checks verify crossing gaps/transforms/animation extrema; no test-only report is described as a passing readability review.
3. Code quality: launch admission uses a cancellable gate (no I/O under the admission mutex), the per-call timeout includes launch/queueing, shutdown cancels and joins admitted calls before clearing collaborators, browser death relaunches, and only unpublished unique evidence is removed. Renamed the old lock-specific helper, retained unrelated module declarations, and fixed timeout reporting after an observed launch-expiry RED. The page-hang test warms Chrome before its short page deadline; a separate stalled-executable test asserts queue cancellation and launch-deadline behavior.

Verification observed: tile/server focused gates; browser protocol/no-browser gates; real embedded measure→Arrange→render probes; package vet/gofmt and normal/race gates pass (49 top-level renderreview tests; final uncached race run 157.506s, no skips/races). make ui embeds both entries and bundled fonts. TypeScript and all 887 Vitest tests (66 files, --maxWorkers=2) pass, as does the required real-Chrome render-surface fixture. An initial oversubscribed parallel gate hit timing limits; sequential reruns and the dedicated launch/page timeout probes passed without loosening assertions. go build ./... passes; the unrelated default-profile module-test failure is recorded under Task 5.5 Step 2. Pre-existing untracked docs/visuals is untouched and excluded from commits.

### Production-completion revisit evidence

Selected this task after skipping the earlier recorded protocol/policy/baseline/environment blockers. Its production dependency was independently unblocked by the now-installed P3T6/P3T7 root; this pass did not start or modify those tasks.

- RED: wrote `TestLayoutRender_RealBrowserRetryPreservesBothCaptures`, then temporarily replaced `MkdirTemp(captureRoot, "capture-*")` with one deterministic reused directory. The real save/restart/Retry ran, and the test failed specifically at the reused published artifact-path sink (`/tmp/layout-p3t5-captures-red.log`). Restored the existing unique-directory implementation; GREEN (10.150s). No production addition was needed for this acceptance-only gate.
- Self-review, in order: **(1) Spec compliance:** restricted changes to the required saved-input/both-capture acceptance and this plan; no feature, option, review-policy or unrelated fixture changes. **(2) Anti-green-theater:** both reviews inspect actual immutable PNGs after Retry; both SQLite generations, authorized final view and unchanged authoring bytes are asserted. Temporarily dropping `workflowNodeTypes` from the production page document made the new test fail at the persisted state sink with `render job: invalid node types`; restored the real manifest map (`/tmp/layout-p3t5-input-red.log`). Reused-path mutation independently proves evidence aliasing is detected. The observer forwards every argument to root-created production collaborators. **(3) Code quality:** reused neighboring root/environment/surface/probe helpers, joined the first root before repairing assets, restored globals with cleanup, replaced verbose map/byte failure diagnostics with explicit path checks, derived expected type captions from saved node types and removed a nested hash-lookup loop. No mutation remains.
- GREEN after review: `go build ./...`; `go vet ./adapter/renderreview ./adapter/dashboard ./cmd/arbitron`; gofmt/diff checks; the new capture test plus real `pr-review` root and all nine persisted surface-defect cases under `ARBITRON_REQUIRE_BROWSER=1 go test -race` (85.109s, no skips/races). After the final assertion cleanup, the new test passed uncached normally (25.400s) and under `-race` (18.093s).
- Verification limits: a fresh whole-adapter run exceeded its 480s harness timeout under host contention and never reached its chained full-race command. The bounded focused adapter race run passed immutable-capture production-review consumption, failed-capture cleanup, real measure/render surfaces and embedded assets; the independent concurrent-renderer capture probe hit Chrome's existing 30s launch timeout before any screenshot. The previously documented whole-module default-profile failure remains outside this slice; it was not re-failed or hidden. Task 5.5 Step 2 remains unchecked. No timeout/assertion was weakened and no full-package green is claimed.
- The new test name already matches `Makefile:test-render`'s `LayoutRender` selection; no extra CI gate or dashboard rebuild is needed for this Go-test-only change. Pre-existing untracked `docs/visuals/` remains untouched.

## Full-module verification revisit

- Selection: P3T2's remaining independently controlled two-browser/font-inventory gate is still recorded as blocked. Worked only P3T5's remaining verification step, now unblocked by the existing documented-profile correction and lower host load. No sibling task was implemented or checked.
- Exact required command `go build ./... && go test ./...` passes across the module, including adapter/config, adapter/renderreview (4.419s) and cmd/arbitron (249.578s). Some packages reused Go's test cache, as the prescribed command permits. Log: `/tmp/p3t5-full-module.log`.
- The prescribed gofmt/vet/normal-package command passes. Its first uncached browser-required renderer race run failed only `TestBrowserRenderer_QueuedCancellationAndLaunchTimeout` while awaiting the fake executable's launch marker (1s observation budget); no data race was reported. The unchanged isolated test passes under -race (2.990s), then the complete unchanged `ARBITRON_REQUIRE_BROWSER=1 go test -race -count=1 ./adapter/renderreview/` passes (225.533s). Logs: `/tmp/p3t5-package-gate.log`, `/tmp/p3t5-launch-rerun.log`, `/tmp/p3t5-package-race-final.log`. The transient failure is not erased or called a first-run green; no assertion, timeout or source was changed.
- Ordered self-review: **(1) Spec compliance:** this remaining step explicitly specifies verification only, with no new files. Existing RED/mutation evidence remains above; no new behavior or artificial failing test was introduced to close a command-only gate. Only this plan's completed box/evidence changes. **(2) Anti-green-theater:** inspected the actual initDomain → newLayoutStack → BrowserRenderer/Coordinator wiring and immutable-manifest Measure/Render inputs. Re-ran existing `TestLayoutRender_RealBrowserRetryPreservesBothCaptures` with browser required and -race: PASS (16.149s), actual blank-capture rejection, repaired retry, both SQLite generations, immutable hashed PNGs reopened by the real reviewer, authorized final view and unchanged manifest bytes. The observer forwards all arguments; nil/empty node types or absent wiring cannot satisfy these sinks. Log: `/tmp/p3t5-root-final.log`. **(3) Code quality:** no production/test edits or timing workarounds were needed; preserved all neighboring task boundaries and historical evidence. Gofmt/vet and final diff checks pass.
- P3T5 now has no unchecked steps. This closes its module-verification gate only, not P3T7/P3T14 complex-sample, Docker, portable-font or planned group-format release acceptance.
