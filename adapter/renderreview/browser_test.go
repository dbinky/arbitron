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
// (ARBITRON_LAYOUT_BROWSERPATH, else auto-detected). Real-browser tests are
// opt-in: outside ARBITRON_REQUIRE_BROWSER=1 (make test-render) TestMain pins the
// path to a sentinel so the test skips; inside it a missing browser fails.
func requireBrowser(t *testing.T) string {
	t.Helper()
	path, err := DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	if err != nil {
		if os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1" {
			t.Fatalf("ARBITRON_REQUIRE_BROWSER=1 but no browser is available: %v", err)
		}
		t.Skipf("real-browser test: opt-in via `make test-render` (ARBITRON_REQUIRE_BROWSER=1): %v", err)
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

func smallNodeTypes() map[string]string {
	return map[string]string{"a": "agent_call", "b": "transform"}
}

func smallGeometry() layout.Geometry {
	return layout.Geometry{
		Format:   "arbitron.layout/v1",
		Options:  layout.DefaultOptions(),
		Contract: layout.DefaultContract(),
		Nodes: []layout.NodeBox{
			{ID: "a", Box: layout.Rect{X: 40, Y: 40, W: 200, H: 60}, Text: "Start", TypeCaption: "agent call", TextLines: 1},
			{ID: "b", Box: layout.Rect{X: 40, Y: 160, W: 200, H: 60}, Text: "Finish", TypeCaption: "transform", TextLines: 1, Rank: 1},
		},
		Edges: []layout.EdgeRoute{{Ordinal: 0, Source: "a", Target: "b", Kind: layout.EdgeForward,
			Points:       []layout.Point{{X: 140, Y: 100}, {X: 140, Y: 160}},
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
	_, err = b.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(), Items: []layout.TextItem{}})
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
		Revision          string            `json:"revision"`
		Attempt           int               `json:"attempt"`
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
		"no types":           func(r *layout.RenderRequest) { r.WorkflowNodeTypes = nil },
		"wrong type":         func(r *layout.RenderRequest) { r.WorkflowNodeTypes["a"] = "transform" },
		"missing states":     func(r *layout.RenderRequest) { r.States = nil },
		"empty canvas":       func(r *layout.RenderRequest) { r.Geometry.Canvas.W = 0 },
		"zero zoom":          func(r *layout.RenderRequest) { r.Geometry.Contract.PracticalZoom = 0 },
		"huge canvas":        func(r *layout.RenderRequest) { r.Geometry.Canvas.H = 40000 },
		"unknown node id":    func(r *layout.RenderRequest) { r.WorkflowNodeTypes["unknown"] = "script" },
		"empty node type":    func(r *layout.RenderRequest) { r.WorkflowNodeTypes["a"] = "" },
		"duplicate state":    func(r *layout.RenderRequest) { r.States[1] = r.States[0] },
		"unknown state":      func(r *layout.RenderRequest) { r.States[0] = "exploding" },
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
      document.documentElement.style.backgroundColor = g.nodes[0].text === 'alternate' ? '#ff0000' : '#1e1e38'
      const text = (b) => ({ content: job.revision + ':' + job.attempt, box: b, clipped: false, fontPx: 13, contrast: 10, occluded: false })
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
	types := smallNodeTypes()
	req := layout.NewRenderRequest("rev-echo", 3, geo, types)
	types["a"] = "wrong_mutation"
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
	assert.Equal(t, "rev-echo:3", r.Report.Nodes[0].Label.Content, "revision and attempt reach the page")
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
	// Screenshots show private workflow text: owner-only dirs and files.
	for _, p := range []string{filepath.Join(cfg.ArtifactDir, "rev-echo"), dir, filepath.Dir(r.Artifacts[0].Path)} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm(), p)
	}
	for _, a := range r.Artifacts {
		fi, err := os.Stat(a.Path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), a.Path)
	}
	// Release removes exactly this capture.
	require.NoError(t, b.Release(context.Background(), r.Artifacts))
	assert.NoDirExists(t, filepath.Dir(r.Artifacts[0].Path))
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
	b := newTestRenderer(t, cfg)
	_, err := b.Measure(context.Background(), measureReq("warmup"))
	require.NoError(t, err)
	// This probe exercises the page timeout; launch/queue expiry is covered separately.
	b.cfg.RenderTimeout = 1500 * time.Millisecond
	start := time.Now()
	_, err = b.Measure(context.Background(), measureReq("hang"))
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
