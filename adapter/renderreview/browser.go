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

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/layout"
)

// ErrBrowserUnavailable means no usable Chrome/Chromium could be found or
// started. Measure and Render wrap it; the renderer never reports success
// without a browser.
var ErrBrowserUnavailable = fmt.Errorf("renderreview: %w", layout.ErrRendererUnavailable)

const (
	// DefaultRenderTimeout bounds one Measure or Render call (navigation,
	// surface work, screenshots).
	DefaultRenderTimeout = 2 * time.Minute
	minTilePx            = 64
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
	ArtifactGrace time.Duration // min age before the reconcile removes an unreferenced capture; default DefaultArtifactGrace
}

// BrowserRenderer implements layout.Renderer by driving the dashboard's
// render surface (render.html) in headless Chrome. The browser and the
// loopback asset server start on first use, so a missing browser surfaces as
// a per-call error wrapping ErrBrowserUnavailable.
type BrowserRenderer struct {
	cfg    BrowserConfig
	logger *slog.Logger
	now    func() time.Time

	mu            sync.Mutex // protects admission; never held during browser I/O
	active        sync.WaitGroup
	lifeCtx       context.Context
	cancelLife    context.CancelFunc
	closeDone     chan struct{}
	launchGate    chan struct{}
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
	lifeCtx, cancelLife := context.WithCancel(context.Background())
	return &BrowserRenderer{cfg: cfg, logger: cfg.Logger, now: time.Now, lifeCtx: lifeCtx, cancelLife: cancelLife, closeDone: make(chan struct{}), launchGate: make(chan struct{}, 1)}, nil
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
	return "", fmt.Errorf("%w: none of [%s] found; install Chrome, Chromium or chrome-headless-shell, or set layout.browserpath to its absolute path", ErrBrowserUnavailable, strings.Join(candidates, ", "))
}

func browserCandidates(goos string) []string {
	switch goos {
	case "darwin":
		// chrome-headless-shell first: launching the full Chrome.app binary
		// registers it with the Dock on every launch, even with --headless, so a
		// server arranging workflows (or a test suite) visibly flickers the Dock.
		return []string{
			"chrome-headless-shell",
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
			"chrome-headless-shell", "headless_shell", "headless-shell", "chrome",
			"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/snap/bin/chromium",
		}
	}
}

// Close stops the browser and the asset server. It is idempotent; later
// calls of Measure and Render fail.
func (b *BrowserRenderer) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		<-b.closeDone
		return
	}
	b.closed = true
	b.cancelLife()
	b.mu.Unlock()
	b.active.Wait()
	b.stopBrowser()
	if b.server != nil {
		b.server.close()
		b.server = nil
	}
	close(b.closeDone)
}

// Admit calls before shutdown waits, and bound queueing, launch and capture by
// the same timeout. Shutdown cancels calls before disposing their collaborators.
func (b *BrowserRenderer) beginCall(ctx context.Context) (context.Context, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, errors.New("renderreview: renderer is closed")
	}
	b.active.Add(1)
	callCtx, cancel := context.WithTimeout(ctx, b.cfg.RenderTimeout)
	stop := context.AfterFunc(b.lifeCtx, cancel)
	return callCtx, func() { stop(); cancel(); b.active.Done() }, nil
}

// Called under launchGate or after all admitted calls have stopped.
func (b *BrowserRenderer) stopBrowser() {
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
	select {
	case b.launchGate <- struct{}{}:
		defer func() { <-b.launchGate }()
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if b.browserCtx != nil && b.browserCtx.Err() == nil {
		select {
		case <-chromedp.FromContext(b.browserCtx).Browser.LostConnection:
		default:
			return b.browserCtx, b.server, nil
		}
	}
	b.stopBrowser() // a crashed browser is relaunched
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
		// SECURITY: the browser gets ONLY browserEnv(). chromedp.Env alone
		// appends to os.Environ() (chromedp v0.21 setCmdEnv); NoInheritEnv makes
		// the allowlist replace it, so ARBITRON_MASTER_KEY and provider API keys
		// never reach the renderer process.
		chromedp.NoInheritEnv,
		chromedp.Env(browserEnv()...),
	)
	if b.cfg.NoSandbox {
		opts = append(opts, chromedp.NoSandbox)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(b.lifeCtx, opts...)
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

// browserBaseNames are the procenv.BaseEnv entries the browser keeps. The
// agent-specific CLAUDE_CONFIG_DIR / ANTHROPIC_BASE_URL and SHELL are dropped.
var browserBaseNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TERM": true, "TMPDIR": true,
}

// browserExtraNames are non-secret variables Chromium itself consults (time
// zone, XDG runtime/config/cache dirs, fontconfig) that the base allowlist
// does not carry.
var browserExtraNames = []string{
	"TZ", "XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
	"FONTCONFIG_FILE", "FONTCONFIG_PATH",
}

// browserEnv is the complete environment of the headless browser: a
// browser-specific subset of procenv.BaseEnv (never wholesale inheritance)
// plus browserExtraNames. It never contains secrets such as
// ARBITRON_MASTER_KEY or provider API keys.
func browserEnv() []string {
	base := procenv.BaseEnv()
	env := make([]string, 0, len(base)+len(browserExtraNames))
	for _, e := range base {
		name, _, _ := strings.Cut(e, "=")
		if browserBaseNames[name] || strings.HasPrefix(name, "LC_") {
			env = append(env, e)
		}
	}
	for _, name := range browserExtraNames {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// Measure lays out every text item in the render surface's measure mode.
func (b *BrowserRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	callCtx, finish, err := b.beginCall(ctx)
	if err != nil {
		return layout.Measurements{}, err
	}
	defer finish()
	if err := validateMeasureRequest(req); err != nil {
		return layout.Measurements{}, err
	}
	doc, err := json.Marshal(measureJob{Mode: "measure", Request: req})
	if err != nil {
		return layout.Measurements{}, fmt.Errorf("renderreview: encode measure job: %w", err)
	}
	raw, err := b.runJob(callCtx, doc, measureViewportW, measureViewportH, "__ARBITRON_MEASURE_RESULT__", nil)
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
		metric, err := m.Metric(it.Key)
		if err != nil {
			return layout.Measurements{}, err
		}
		if (it.Text == "" && metric != (layout.TextMetrics{})) || (it.Text != "" && (metric.Width <= 0 || metric.Height <= 0 || metric.Lines <= 0)) || metric.Width > it.MaxWidth+0.5 {
			return layout.Measurements{}, fmt.Errorf("renderreview: invalid metrics for %q", it.Key)
		}
	}
	return m, nil
}

// Render draws one candidate with the shared renderer, returns the surface's
// DOM report and writes an overview plus readable-scale tiles under
// ArtifactDir/<revision>/<attempt>/<unique-capture>/.
func (b *BrowserRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	callCtx, finish, err := b.beginCall(ctx)
	if err != nil {
		return layout.Rendering{}, err
	}
	defer finish()
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
	published := false
	defer func() {
		if !published && len(artifacts) > 0 {
			_ = os.RemoveAll(filepath.Dir(artifacts[0].Path))
		}
	}()
	capture := func(tabCtx context.Context) error {
		var err error
		artifacts, err = b.capture(tabCtx, req, tiles)
		return err
	}
	vw, vh := int64(math.Ceil(geo.Canvas.W*zoom)), int64(math.Ceil(geo.Canvas.H*zoom))
	raw, err := b.runJob(callCtx, doc, vw, vh, "__ARBITRON_RENDER_REPORT__", capture)
	if err != nil {
		return layout.Rendering{}, err
	}
	var report layout.RenderReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return layout.Rendering{}, fmt.Errorf("renderreview: decode render report: %w", err)
	}
	if len(report.Nodes) == 0 {
		return layout.Rendering{}, errors.New("renderreview: empty render report")
	}
	published = true
	return layout.Rendering{
		Revision:       req.Revision,
		Attempt:        req.Attempt,
		GeometryDigest: layout.GeometryDigest(geo),
		Report:         report,
		Artifacts:      artifacts,
		RenderedAt:     b.now().UTC(),
	}, nil
}

func validateMeasureRequest(req layout.MeasureRequest) error {
	if req.Contract != layout.DefaultContract() {
		return errors.New("renderreview: unknown measurement contract")
	}
	if req.Items == nil {
		return errors.New("renderreview: measure items must be explicit")
	}
	seen := make(map[string]bool, len(req.Items))
	for _, it := range req.Items {
		if it.Key == "" || seen[it.Key] || !finite(it.MaxWidth) || it.MaxWidth <= 0 {
			return errors.New("renderreview: invalid or duplicate measurement item")
		}
		switch it.Style {
		case layout.StyleNodeLabel, layout.StyleNodeType, layout.StyleEdgeLabel:
		default:
			return errors.New("renderreview: unknown text style")
		}
		seen[it.Key] = true
	}
	return nil
}

func validateRenderRequest(req layout.RenderRequest) error {
	for _, n := range req.Geometry.Nodes {
		typ, ok := req.WorkflowNodeTypes[n.ID]
		if !ok || typ == "" || strings.ReplaceAll(typ, "_", " ") != n.TypeCaption {
			return fmt.Errorf("renderreview: missing or mismatched node type for %s", n.ID)
		}
	}
	if len(req.WorkflowNodeTypes) != len(req.Geometry.Nodes) {
		return errors.New("renderreview: node type coverage differs")
	}
	if !slices.Equal(req.States, layout.RuntimeStates) {
		return errors.New("renderreview: runtime state coverage differs")
	}
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
	Mode string `json:"mode"`
	layout.RenderRequest
}

// renderJobDocument is the job JSON of the render surface's render mode.
func renderJobDocument(req layout.RenderRequest) ([]byte, error) {
	doc, err := json.Marshal(renderJob{Mode: "render", RenderRequest: req})
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
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, b.surfaceTimeout("")
		}
		return nil, fmt.Errorf("renderreview: %w", err)
	}
	browserCtx, server, err := b.ensureBrowser(ctx)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, b.surfaceTimeout("")
		}
		return nil, err
	}
	tabCtx, cancelTab := chromedp.NewContext(browserCtx)
	defer cancelTab()
	stop := context.AfterFunc(ctx, cancelTab) // caller cancellation closes the tab at once
	defer stop()
	callCtx, cancelCall := context.WithCancel(tabCtx)
	defer cancelCall()

	token, release := server.register(doc)
	defer release()

	state := ""
	fail := func(what string, err error) error {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return b.surfaceTimeout(state)
		case ctx.Err() != nil:
			return fmt.Errorf("renderreview: %s: %w", what, ctx.Err())
		case browserCtx.Err() != nil:
			return fmt.Errorf("renderreview: %w while %s: %v", layout.ErrRendererCrashed, what, err)
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

func (b *BrowserRenderer) surfaceTimeout(state string) error {
	return fmt.Errorf("renderreview: render surface did not finish within %s (state %q): %w", b.cfg.RenderTimeout, state, context.DeadlineExceeded)
}

// capture writes the overview and every tile as PNG files.
func (b *BrowserRenderer) capture(ctx context.Context, req layout.RenderRequest, tiles []layout.Rect) ([]layout.Artifact, error) {
	geo := req.Geometry
	zoom := geo.Contract.PracticalZoom
	captureRoot := filepath.Join(b.cfg.ArtifactDir, req.Revision, strconv.Itoa(req.Attempt))
	// Screenshots show private workflow text: owner-only throughout. Release
	// and the reconcile prune empty parents, so re-create them if one vanished
	// between MkdirAll and MkdirTemp.
	var dir string
	for try := 0; ; try++ {
		if err := os.MkdirAll(captureRoot, 0o700); err != nil {
			return nil, fmt.Errorf("create artifact dir: %w", err)
		}
		var err error
		if dir, err = os.MkdirTemp(captureRoot, capturePrefix+"*"); err == nil {
			break
		} else if try == 2 || !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("create immutable capture directory: %w", err)
		}
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(dir)
		}
	}()
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
		if err := os.WriteFile(path, png, 0o600); err != nil {
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
	complete = true
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
