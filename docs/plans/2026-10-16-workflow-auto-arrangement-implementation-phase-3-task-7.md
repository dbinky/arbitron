# Workflow Auto-Arrangement — Phase 3 Task 7: Wire the production renderer/reviewer, ship the browser runtime, real-browser acceptance

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

**Alignment requirements:** Read contracts §6 (6.7) before executing this task. Those requirements and the corrective TDD steps below are part of this task; conflicting earlier example bodies must be updated, not copied unchanged.

**Goal:** Put `renderreview.BrowserRenderer` (P3T5) and `renderreview.ReadabilityReviewer` (P3T6) on the live arrangement path for both `arbitron serve` and stdio `arbitron mcp`, replacing P2T9's explicit `rendererUnavailable`. Ship Chromium in the Docker image. Prove through the real composition root that the spec's acceptance samples reach `passed` only after actual rendered review, and that a missing browser fails visibly and stays retryable.

**Architecture:** P2T9 left a single construction seam, `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer`, which `newLayoutStack` calls through `layoutRenderReviewerFactory`. This task replaces its body. It builds the browser renderer from `cfg.Layout` (`BrowserPath`, `BrowserNoSandbox`, `ArtifactDir`, default `<dir of database.path>/layout-artifacts`) and the embedded dashboard build (`adapter/dashboard.Assets()`, which contains `render.html`), plus the readability reviewer. The renderer's `Close` becomes the stack's close function. A missing browser does not fail boot. Instead, every job ends `failed` at stage `rendering` with `renderreview.ErrBrowserUnavailable` in its reason, and a WARN is logged at boot. A broken build (no `render.html`) does fail boot, because production must never silently run without a reviewer. Acceptance tests seed the real `samples/` directories through `initDomain` and assert at the sinks: the SQLite row and stored result, the evidence files on disk, the `layout.json` cache, and the shared `layout.Reader` view.

**Tech Stack:** Go 1.25.5, testify, headless Chrome/Chromium via chromedp (P3T5); Docker (Alpine 3.20 `chromium`, `font-noto`, `font-noto-cjk`, `font-noto-emoji`); GNU make.

**Spec/Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md` (Review the rendered result; Handle incomplete results; Outcomes and Acceptance Criteria: "The current feature-pipeline is the complex acceptance example; pr-review is the simpler example. Both must show continuous downward progression"). `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` Testing strategy (Happy path / Success) and **PLANNED DEPENDENCY RENDER-REVIEW**. `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-2.md` §6 ("Production must not silently install a no-op requester or reviewer"). `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-3.md` §4 ("It must work for stdio MCP saves without a user opening the dashboard"). This task **closes RENDER-REVIEW**: the producer (P3T4/P3T5/P3T6) is constructed at the composition root and fed by the worker's real candidates.

**Prerequisites (merged before starting):** P2T1 (`LayoutConfig.BrowserPath`, `BrowserNoSandbox`, `ArtifactDir`; env `ARBITRON_LAYOUT_BROWSERPATH`, `ARBITRON_LAYOUT_BROWSERNOSANDBOX`), P2T4 (seeding stamps and requests new samples), P2T5–P2T8 (store, requester, reader, cache, worker), P2T9 (`layout_wiring.go`, `layout_e2e_test.go` and its helpers), P3T4 + P3T5 Task 5.4 (`make ui` embedded `render.html`; `adapter/dashboard.Assets()`), P3T5 (`BrowserRenderer`, `BrowserConfig`, `DetectBrowser`, `ErrBrowserUnavailable`), P3T6 (`NewReadabilityReviewer`).

**Runtime dependency:** Non-Docker installs use a local Chrome/Chromium via auto-detection or layout.browserpath. P3T7 documents this in installation/configuration docs and tests missing-browser visible failure + Retry. The spec explicitly permits arrangement/review failure; this dependency does not block the authorized Docker/host implementation or require an unattended product decision. Do not claim successful reviewed arrangement on an unprovisioned host.

---

## Wired-and-fed

| Component / input | Composition-root construction (this task) | Production producer of its inputs |
|---|---|---|
| `renderreview.BrowserRenderer` as `layout.Renderer` | `cmd/arbitron/layout_wiring.go:newLayoutRenderReviewer` → `renderreview.NewBrowserRenderer(renderreview.BrowserConfig{...})`, reached from `initDomain` → `newLayoutStack` → `layoutRenderReviewerFactory` | Candidates: `domain/layout/coordinator.go:(*Coordinator).Run` (P1T9) from the worker's captured manifest (`domain/layout/worker.go:(*Worker).ProcessRevision`, P2T8). Assets: `adapter/dashboard/embed.go:Assets` (P3T5 Task 5.4, built by `make ui` from P3T2–P3T4) |
| `BrowserConfig.BrowserPath` | `cfg.Layout.BrowserPath` | `adapter/config/loader.go:Load` (P2T1): YAML `layout.browserpath` / env `ARBITRON_LAYOUT_BROWSERPATH`; `""` ⇒ `renderreview.DetectBrowser` auto-detection. Docker image ships `chromium` (Task 7.4) |
| `BrowserConfig.NoSandbox` | `cfg.Layout.BrowserNoSandbox` | P2T1 loader: `layout.browsernosandbox` / `ARBITRON_LAYOUT_BROWSERNOSANDBOX`; the Dockerfile sets it `true` (Task 7.4) |
| `BrowserConfig.ArtifactDir` | `layoutArtifactDir(cfg)` | P2T1 loader `layout.artifactdir`; default derived from `cfg.Database.Path` |
| `renderreview.ReadabilityReviewer` as `layout.Reviewer` | `newLayoutRenderReviewer` → `renderreview.NewReadabilityReviewer(logger)` | Its `ReviewRequest.Rendering` is the same attempt's `BrowserRenderer.Render` result, passed by the coordinator |
| Renderer shutdown | `newLayoutRenderReviewer` returns `renderer.Close`; `newLayoutStack` appends it to `ls.closeFns`; `domainStack.Close` → `ls.stop()` runs it before `db.Close()` (P2T9) | — |

Provenance probes (each fails if the wiring is absent or fed nothing):
- `TestNewLayoutRenderReviewer_InstallsTheBrowserAdapter` fails while the placeholder is installed: a `Measure` on the installed renderer must wrap `renderreview.ErrBrowserUnavailable`.
- `TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable` drives `initDomain` and asserts the SQLite row's failure reason. That reason can only come from the browser adapter.
- `TestLayoutRender_RealBrowserAcceptance` drives `initDomain` with the real `samples/` and asserts `passed`, rendered evidence files under the configured `artifactdir`, the `layout.json` cache and the reader view.

## Interfaces

**Consumes** (exact):

```go
// adapter/renderreview (P3T5, P3T6)
type BrowserConfig struct {
	Assets fs.FS; BrowserPath string; NoSandbox bool; ArtifactDir string; Logger *slog.Logger
	RenderTimeout time.Duration; TileWidth, TileHeight int; Overlap float64
}
func NewBrowserRenderer(cfg BrowserConfig) (*BrowserRenderer, error) // error when Assets lacks render.html or ArtifactDir is empty
func (b *BrowserRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error)
func (b *BrowserRenderer) Close()
func DetectBrowser(explicit string) (string, error) // wraps ErrBrowserUnavailable
var ErrBrowserUnavailable = errors.New("renderreview: no Chrome/Chromium browser available")
func NewReadabilityReviewer(logger *slog.Logger) *ReadabilityReviewer

// adapter/dashboard (P3T5 Task 5.4)
func Assets() fs.FS

// cmd/arbitron (P2T9) — layout_wiring.go
func newLayoutStack(cfg *config.Config, db *sql.DB, repo *filesystem.WorkflowRepo, publications workflow.RevisionPublicationStore, registry workflow.Registry, logger *slog.Logger) (*layoutStack, error)
var layoutRenderReviewerFactory = newLayoutRenderReviewer
// layoutStack fields used here: store *adaptsqlite.LayoutRepo; cache *filesystem.LayoutCache; requester *layout.Requester;
// reader *layout.Reader; renderer layout.Renderer; reviewer layout.Reviewer

// cmd/arbitron (P2T9) — layout_e2e_test.go helpers
type layoutE2EOptions struct { maxAttempts string; seedSample string }
type layoutE2EEnv struct { root, workspaceDir, dbPath, logPath, configPath, samplesDir string }
func newLayoutE2EEnv(t *testing.T, opts layoutE2EOptions) *layoutE2EEnv
func (e *layoutE2EEnv) boot(t *testing.T) (*domainStack, func())
func (e *layoutE2EEnv) privatePath(name string) string
func (e *layoutE2EEnv) sharedPath(name string) string
func layoutE2EManifest(t *testing.T, path string) *workflow.Workflow
func layoutE2ESavedRevision(t *testing.T, path string) string
func layoutE2EWaitTerminal(t *testing.T, ds *domainStack, revision string) layout.Record
func layoutE2EWorkflow(name, label string) *workflow.Workflow
func layoutE2EDiscardLogger() *slog.Logger
func layoutE2EOpenDB(t *testing.T) *sql.DB
func layoutE2EConfig(workspace string, maxAttempts int) *config.Config
func adminCtxForLocalUser(t *testing.T, ds *domainStack) context.Context // spawn_e2e_test.go

// domain/layout (P2T5, P2T6, P2T8)
func (s Store) Get(ctx context.Context, revision string) (Record, error)
func (s Store) Result(ctx context.Context, revision string, generation int64) (StoredResult, error)
func (r *Reader) View(ctx context.Context, wf *workflow.Workflow) View
func (c *LayoutCache) ReadCache(ctx context.Context, scope workflow.Scope, name string) (layout.CacheEntry, bool, error)
func (r *Requester) Retry(ctx context.Context, revision string) (Record, error)
```

**Produces:**

```go
// cmd/arbitron/layout_wiring.go
var layoutRenderAssets = dashboard.Assets           // seam: the embedded build the render surface is served from
func layoutArtifactDir(cfg *config.Config) string   // cfg.Layout.ArtifactDir, else <dir of cfg.Database.Path>/layout-artifacts
func newLayoutRenderReviewer(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) // production body
// deleted: type rendererUnavailable, var errRendererNotInstalled

// cmd/arbitron/layout_e2e_test.go
type layoutE2EOptions struct { maxAttempts string; seedSample string; realSample string } // realSample: copy ../../samples/<name> and seed it

// cmd/arbitron/layout_render_e2e_test.go
func requireBrowserE2E(t *testing.T) string
func useLayoutE2EBrowser(t *testing.T, path string)

// Makefile: target test-render; Dockerfile: chromium + fonts + ARBITRON_LAYOUT_BROWSERNOSANDBOX=true + ARBITRON_LAYOUT_ARTIFACTDIR=/data/layout-artifacts
```

---

### Task 7.1: Production body of `newLayoutRenderReviewer`; delete the placeholder

**Files:**
- Modify: `cmd/arbitron/layout_wiring.go`: imports; delete `errRendererNotInstalled`, `rendererUnavailable` and its three methods; replace the body of `newLayoutRenderReviewer`; add `layoutRenderAssets` and `layoutArtifactDir`
- Modify: `cmd/arbitron/layout_e2e_test.go`:
  - delete `TestLayoutE2E_RendererUnavailable_EveryMethodFailsVisibly`
  - delete `TestLayoutE2E_UnavailableRendererFailsEveryJobVisibly`
  - in `TestLayoutE2E_NewLayoutStack_WiresStamperRequesterAndStore`, replace the two `assert.IsType(t, rendererUnavailable{}, …)` lines
- Create: `cmd/arbitron/layout_render_wiring_test.go`

- [x] **Step 1: Write the failing test**

Create `cmd/arbitron/layout_render_wiring_test.go`:

```go
package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderWiringConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "arbitron.db")
	cfg.Workspace.Path = t.TempDir()
	cfg.Layout.MaxAttempts = 5
	return cfg
}

func TestLayoutArtifactDir(t *testing.T) {
	cfg := renderWiringConfig(t)
	assert.Equal(t, filepath.Join(filepath.Dir(cfg.Database.Path), "layout-artifacts"), layoutArtifactDir(cfg),
		"evidence defaults to a directory beside the database")

	cfg.Layout.ArtifactDir = "/srv/arbitron/evidence"
	assert.Equal(t, "/srv/arbitron/evidence", layoutArtifactDir(cfg), "layout.artifactdir wins")
}

func TestLayoutRenderAssets_IsTheEmbeddedBuildWithTheRenderSurface(t *testing.T) {
	_, err := fs.Stat(layoutRenderAssets(), "render.html")
	require.NoError(t, err, "the production assets must contain render.html; run `make ui`")
	_, err = fs.Stat(dashboard.Assets(), "render.html")
	require.NoError(t, err)
}

// PROVENANCE PROBE: the composition root installs the browser adapter, not a
// placeholder. With an explicit browser path that does not exist, construction
// still succeeds (boot must not fail without a browser), and the installed
// renderer's own error is the adapter's ErrBrowserUnavailable.
func TestNewLayoutRenderReviewer_InstallsTheBrowserAdapter(t *testing.T) {
	cfg := renderWiringConfig(t)
	cfg.Layout.BrowserPath = filepath.Join(t.TempDir(), "no-such-chromium")

	renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, layoutE2EDiscardLogger())
	require.NoError(t, err, "a missing browser must not fail boot")
	require.NotNil(t, closeFn)
	t.Cleanup(closeFn)

	require.IsType(t, &renderreview.BrowserRenderer{}, renderer)
	require.IsType(t, &renderreview.ReadabilityReviewer{}, reviewer)

	_, err = renderer.Measure(context.Background(), layout.MeasureRequest{Contract: layout.DefaultContract(),
		Items: []layout.TextItem{{Key: "k", Text: "Start", Style: layout.StyleNodeLabel, MaxWidth: 240}}})
	require.ErrorIs(t, err, renderreview.ErrBrowserUnavailable, "the live renderer reports a missing browser explicitly")

	closeFn()
	closeFn() // idempotent: domainStack.Close may run after a failed boot cleanup
}

func TestNewLayoutRenderReviewer_BrokenBuildFailsBoot(t *testing.T) {
	prev := layoutRenderAssets
	t.Cleanup(func() { layoutRenderAssets = prev })
	layoutRenderAssets = func() fs.FS { return fstest.MapFS{"index.html": {Data: []byte("<!DOCTYPE html>")}} }

	_, _, _, err := newLayoutRenderReviewer(renderWiringConfig(t), layoutE2EDiscardLogger())
	require.Error(t, err, "a build without the render surface must not boot with an unusable reviewer")
	assert.Contains(t, err.Error(), "render.html")
}

func TestNewLayoutRenderReviewer_RequiresConfig(t *testing.T) {
	_, _, _, err := newLayoutRenderReviewer(nil, nil)
	require.Error(t, err)
	assert.True(t, !errors.Is(err, renderreview.ErrBrowserUnavailable), "a missing config is a wiring defect, not a missing browser")
}
```

In `cmd/arbitron/layout_e2e_test.go`, replace these two lines in `TestLayoutE2E_NewLayoutStack_WiresStamperRequesterAndStore`:

```go
	assert.IsType(t, rendererUnavailable{}, ls.renderer, "this build installs the explicit unavailable renderer")
	assert.IsType(t, rendererUnavailable{}, ls.reviewer)
```

with:

```go
	assert.IsType(t, &renderreview.BrowserRenderer{}, ls.renderer, "newLayoutStack installs the production browser renderer")
	assert.IsType(t, &renderreview.ReadabilityReviewer{}, ls.reviewer, "newLayoutStack installs the production readability reviewer")
```

Add `"github.com/arbitron/arbitron/adapter/renderreview"` to that file's import block. Then delete the whole of `TestLayoutE2E_RendererUnavailable_EveryMethodFailsVisibly` and `TestLayoutE2E_UnavailableRendererFailsEveryJobVisibly`. Task 7.2's `TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable` replaces their visible-failure coverage, using the real adapter.

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./cmd/arbitron/ -run 'TestLayoutArtifactDir|TestLayoutRenderAssets|TestNewLayoutRenderReviewer|TestLayoutE2E_NewLayoutStack_WiresStamperRequesterAndStore' -count=1 -v`
Expected: build failure: `undefined: layoutArtifactDir`, `undefined: layoutRenderAssets`.

- [x] **Step 3: Minimal implementation**

In `cmd/arbitron/layout_wiring.go`:

1. Replace the import block with:

```go
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/arbitron/arbitron/adapter/dashboard"
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/adapter/renderreview"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)
```

2. Delete everything from the `// errRendererNotInstalled is the visible, retryable failure reason…` comment through the end of the existing `newLayoutRenderReviewer` function. That covers `errRendererNotInstalled`, `rendererUnavailable`, its `var _` assertions, its `Measure`/`Render`/`Review` methods and the old `newLayoutRenderReviewer`. Put this in their place:

```go
// layoutRenderAssets is the embedded dashboard build the render surface
// (render.html) is served from. Production never reassigns it. Tests replace it
// to prove a build without the surface fails boot.
var layoutRenderAssets = dashboard.Assets

// layoutArtifactDir is where rendered review evidence (overview and tile
// screenshots) is written: layout.artifactdir, else a directory beside the
// SQLite database.
func layoutArtifactDir(cfg *config.Config) string {
	if cfg.Layout.ArtifactDir != "" {
		return cfg.Layout.ArtifactDir
	}
	return filepath.Join(filepath.Dir(cfg.Database.Path), "layout-artifacts")
}

// newLayoutRenderReviewer builds the production renderer and reviewer. The
// renderer is headless Chrome drawing each candidate with the dashboard's own
// components, CSS and fonts, and the reviewer judges that rendering. A missing
// browser does not fail boot: every job then ends failed at the rendering stage
// with renderreview.ErrBrowserUnavailable (retryable), and this logs a WARN. A
// build without render.html does fail boot.
func newLayoutRenderReviewer(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
	if cfg == nil {
		return nil, nil, nil, errors.New("layout renderer requires a config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	artifactDir := layoutArtifactDir(cfg)
	renderer, err := renderreview.NewBrowserRenderer(renderreview.BrowserConfig{
		Assets:      layoutRenderAssets(),
		BrowserPath: cfg.Layout.BrowserPath,
		NoSandbox:   cfg.Layout.BrowserNoSandbox,
		ArtifactDir: artifactDir,
		Logger:      logger,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building layout browser renderer: %w", err)
	}
	if path, derr := renderreview.DetectBrowser(cfg.Layout.BrowserPath); derr != nil {
		logger.Warn("no Chrome/Chromium browser found for layout review: every arrangement job will end failed at the rendering stage (retryable) until one is installed or layout.browserpath is set; saved workflows are unaffected",
			"error", derr)
	} else {
		logger.Info("layout review browser", "path", path, "artifactDir", artifactDir, "noSandbox", cfg.Layout.BrowserNoSandbox)
	}
	return renderer, renderreview.NewReadabilityReviewer(logger), renderer.Close, nil
}
```

3. Leave `var layoutRenderReviewerFactory = newLayoutRenderReviewer` and `newLayoutStack` unchanged. They already call the factory, append `closeRender` to `ls.closeFns`, and pass `renderer`/`reviewer` to `layout.NewCoordinator`.

Then check that nothing still references the deleted symbols:

Run: `grep -rn "rendererUnavailable\|errRendererNotInstalled" cmd/`
Expected: no output.

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./cmd/arbitron/ -run 'TestLayoutArtifactDir|TestLayoutRenderAssets|TestNewLayoutRenderReviewer|TestLayoutE2E_' -count=1 -v`
Expected: `TestLayoutArtifactDir`, `TestLayoutRenderAssets_IsTheEmbeddedBuildWithTheRenderSurface`, `TestNewLayoutRenderReviewer_InstallsTheBrowserAdapter`, `TestNewLayoutRenderReviewer_BrokenBuildFailsBoot`, `TestNewLayoutRenderReviewer_RequiresConfig` and every remaining `TestLayoutE2E_*` test `--- PASS`; `ok`. The P2T9 e2e tests pass whether or not a browser is installed (`layoutE2EAssertOutcome` accepts passed, needs_attention, or failed at rendering).

- [x] **Step 5: Commit**

```bash
gofmt -l cmd/arbitron/
git add cmd/arbitron/layout_wiring.go cmd/arbitron/layout_e2e_test.go cmd/arbitron/layout_render_wiring_test.go
git commit -m "feat(layout): install the browser renderer and readability reviewer at the composition root" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.2: A missing browser fails every job visibly, with Retry, through `initDomain`

**Files:**
- Create: `cmd/arbitron/layout_render_e2e_test.go`

**Interfaces:** Consumes the P2T9 helpers listed above plus `renderreview.ErrBrowserUnavailable`. Produces `requireBrowserE2E` and `useLayoutE2EBrowser`, which Task 7.3 uses.

- [x] **Step 1: Write the test**

Create `cmd/arbitron/layout_render_e2e_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireBrowserE2E returns the Chrome/Chromium the acceptance tests drive
// (ARBITRON_LAYOUT_BROWSERPATH, else auto-detected). Without one the test is
// skipped, unless ARBITRON_REQUIRE_BROWSER=1 (make test-render), which fails it.
func requireBrowserE2E(t *testing.T) string {
	t.Helper()
	path, err := renderreview.DetectBrowser(os.Getenv("ARBITRON_LAYOUT_BROWSERPATH"))
	if err != nil {
		if os.Getenv("ARBITRON_REQUIRE_BROWSER") == "1" {
			t.Fatalf("ARBITRON_REQUIRE_BROWSER=1 but no browser is available: %v", err)
		}
		t.Skipf("no Chrome/Chromium available (set ARBITRON_REQUIRE_BROWSER=1 to fail instead): %v", err)
	}
	return path
}

// useLayoutE2EBrowser points the composition root's loader at path through the
// production env keys. Chrome refuses to start sandboxed as root (CI/Docker).
func useLayoutE2EBrowser(t *testing.T, path string) {
	t.Helper()
	t.Setenv("ARBITRON_LAYOUT_BROWSERPATH", path)
	if os.Geteuid() == 0 {
		t.Setenv("ARBITRON_LAYOUT_BROWSERNOSANDBOX", "true")
	}
}

// PROVENANCE PROBE (missing runtime): with the production browser adapter on
// the live path and no browser, a save still succeeds and is never rewritten.
// Its arrangement fails visibly at the rendering stage with the adapter's own
// reason, Retry starts a new generation, and boot logs the WARN.
func TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable(t *testing.T) {
	useLayoutE2EBrowser(t, filepath.Join(t.TempDir(), "no-such-chromium"))
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "3"})
	ds, _ := env.boot(t)
	require.IsType(t, &renderreview.BrowserRenderer{}, ds.layout.renderer, "initDomain installs the browser renderer")
	require.IsType(t, &renderreview.ReadabilityReviewer{}, ds.layout.reviewer, "initDomain installs the readability reviewer")
	ctx := adminCtxForLocalUser(t, ds)

	require.NoError(t, ds.workflowSvc.Create(ctx, "needs-browser", layoutE2EWorkflow("needs-browser", "Start")),
		"a save never depends on the renderer")
	revision := layoutE2ESavedRevision(t, env.privatePath("needs-browser"))

	rec := layoutE2EWaitTerminal(t, ds, revision)
	assert.Equal(t, layout.StateFailed, rec.State, "a missing browser must never pass")
	assert.Equal(t, layout.StageRendering, rec.FailureStage)
	assert.Contains(t, rec.FailureReason, renderreview.ErrBrowserUnavailable.Error(), "the reason names the missing browser")
	assert.Equal(t, 1, rec.AttemptsUsed, "measurement fails at the start of attempt 1")
	assert.Zero(t, rec.ResultGeneration, "no geometry is stored without measurements")
	assert.Equal(t, workflow.RetainedFromManifest, rec.Retained.Source, "the saved positions remain the displayed fallback")
	assert.Equal(t, layoutE2EPositions, rec.Retained.Positions)

	saved := layoutE2EManifest(t, env.privatePath("needs-browser"))
	require.NotNil(t, saved.Presentation)
	assert.Equal(t, revision, saved.Presentation.Revision, "a failed arrangement never rewrites the saved workflow")
	require.Len(t, saved.Nodes, 3)
	assert.Equal(t, "Start", saved.Nodes[0].Label)

	retried, err := ds.layout.requester.Retry(ctx, revision)
	require.NoError(t, err, "a failed revision is retryable")
	assert.Equal(t, rec.JobGeneration+1, retried.JobGeneration)
	require.Eventually(t, func() bool {
		got, gerr := ds.layout.store.Get(context.Background(), revision)
		return gerr == nil && got.JobGeneration == retried.JobGeneration &&
			got.State == layout.StateFailed && got.FailureStage == layout.StageRendering &&
			strings.Contains(got.FailureReason, renderreview.ErrBrowserUnavailable.Error())
	}, 60*time.Second, 50*time.Millisecond, "the retry runs through the worker and fails visibly again")

	require.Eventually(t, func() bool {
		raw, rerr := os.ReadFile(env.logPath)
		return rerr == nil && strings.Contains(string(raw), "no Chrome/Chromium browser found for layout review")
	}, 10*time.Second, 50*time.Millisecond, "boot warns that arrangement cannot be reviewed")
}
```

- [x] **Step 2: Run it**

Run: `go test ./cmd/arbitron/ -run TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable -count=1 -v`
Expected: `--- PASS`; `ok`. This test needs no browser.

To prove the test detects a broken wiring, temporarily change `newLayoutRenderReviewer` to return `nil, nil, func() {}, nil`. Then run: `go test ./cmd/arbitron/ -run TestLayoutRender_MissingBrowserFailsVisiblyAndIsRetryable -count=1`
Expected: `FAIL`, with `initDomain` failing on `layout: renderer is required`. Revert the temporary change and re-run to green.

- [x] **Step 3: Commit**

```bash
gofmt -l cmd/arbitron/
git add cmd/arbitron/layout_render_e2e_test.go
git commit -m "test(layout): missing browser fails arrangement visibly and stays retryable through initDomain" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.3: Real-browser acceptance — pr-review, feature-pipeline and ralph-loop pass through the composition root

**Files:**
- Modify: `cmd/arbitron/layout_e2e_test.go`: the `layoutE2EOptions` struct and the samples block in `newLayoutE2EEnv`
- Modify: `cmd/arbitron/layout_render_e2e_test.go` (append)

**Interfaces:** Consumes `requireBrowserE2E`, `useLayoutE2EBrowser` (Task 7.2), P2T9 helpers, and `ds.layout.store.Result`, `ds.layout.reader.View`, `ds.layout.cache.ReadCache` (P2T5–P2T8). Produces `layoutE2EOptions.realSample`.

The samples reach the pipeline through the production seeding path (P2T4 `Seeder.seedOne` → `RevisionStamper` → `Requester` → worker), exactly as the Docker image seeds `/samples` on first boot.

- [x] **Step 1: Add the `realSample` option**

In `cmd/arbitron/layout_e2e_test.go`, replace:

```go
type layoutE2EOptions struct {
	maxAttempts string // YAML value for layout.maxattempts
	seedSample  string // when set, a sample of this name is seeded at boot
}
```

with:

```go
type layoutE2EOptions struct {
	maxAttempts string // YAML value for layout.maxattempts
	seedSample  string // when set, a synthetic sample of this name is seeded at boot
	realSample  string // when set, the repository's samples/<name> is copied and seeded at boot
}
```

In `newLayoutE2EEnv`, insert this block directly after the closing `}` of the `if opts.seedSample != "" { … }` block, and before `cfg := "" +`:

```go
	if opts.realSample != "" {
		env.samplesDir = filepath.Join(root, "samples")
		src := filepath.Join("..", "..", "samples", opts.realSample)
		require.NoError(t, os.CopyFS(filepath.Join(env.samplesDir, opts.realSample), os.DirFS(src)),
			"copying the bundled sample %s", opts.realSample)
		samples = "samples:\n  seed: true\n  sourcedir: \"" + env.samplesDir + "\"\n"
	}
```

- [x] **Step 2: Write the acceptance test**

> Resolved on runtime revisit: the complete three-sample acceptance is retained. Current production producers pass without changes to geometry, review thresholds or attempt budget. Missing-browser input mutation fails all three SQLite passed assertions, then restoration passes. The following blocker is historical.

> Historical blocker: The exact three-sample test was written and executed through initDomain with real Chrome. pr-review passed (11.21s); feature-pipeline and ralph-loop reached needs_attention after five attempts, not passed. feature-pipeline findings include edge 49 missing branch ink, labels 31/34 closer to other routes, and ambiguous crossings [34,49], [34,53], [14,53], [78,61], [77,69], [78,77]. ralph-loop has ambiguous crossings [37,20] and [37,38]; a second independent run reproduced those exact findings. P1 routing/refinement and P3 renderer crossing treatment do not yet satisfy the existing reviewer. No rule/threshold/assertion was relaxed and no fake pass was installed. In accordance with the plan's prohibition on committing knowingly failing tests, the combined failing acceptance test was not committed: only the independently green TestLayoutRender_RealBrowserPRReview provenance probe (all SQLite/result/PNG/cache/Reader/unchanged-manifest sinks) is retained. This is explicitly NOT the three-sample acceptance gate. Restore the full test below and fix the named producer defects before checking Steps 2/3/5. Joint authorised HTTP/WorkflowReview/ExecutionView acceptance additionally awaits P3T8–P3T14.

Append to `cmd/arbitron/layout_render_e2e_test.go` (add `"crypto/sha256"`, `"encoding/hex"`, `"math"` and `"sort"` to its imports):

```go
// layoutRenderWaitTerminal is layoutE2EWaitTerminal with a budget sized for
// real rendering of large workflows (feature-pipeline: 66 nodes, 79 edges).
func layoutRenderWaitTerminal(t *testing.T, ds *domainStack, revision string, within time.Duration) layout.Record {
	t.Helper()
	var rec layout.Record
	require.Eventually(t, func() bool {
		got, err := ds.layout.store.Get(context.Background(), revision)
		if err != nil {
			return false
		}
		rec = got
		return got.State.Terminal()
	}, within, 250*time.Millisecond, "revision %s must reach a terminal arrangement state", revision)
	return rec
}

// assertDownwardArrangement checks the spec's continuous downward progression.
// Every forward edge goes down and never climbs, and each rank is a horizontal
// band strictly below the previous one, so sequential flow never wraps into
// columns.
func assertDownwardArrangement(t *testing.T, geo layout.Geometry) {
	t.Helper()
	boxes := make(map[string]layout.NodeBox, len(geo.Nodes))
	top, bottom := map[int]float64{}, map[int]float64{}
	for _, n := range geo.Nodes {
		boxes[n.ID] = n
		if v, ok := top[n.Rank]; !ok || n.Box.Y < v {
			top[n.Rank] = n.Box.Y
		}
		if v, ok := bottom[n.Rank]; !ok || n.Box.Bottom() > v {
			bottom[n.Rank] = n.Box.Bottom()
		}
	}
	for _, e := range geo.Edges {
		if e.Kind != layout.EdgeForward {
			continue
		}
		src, dst := boxes[e.Source], boxes[e.Target]
		assert.Greater(t, dst.Rank, src.Rank, "forward edge %d (%s → %s) descends at least one rank", e.Ordinal, e.Source, e.Target)
		assert.GreaterOrEqual(t, dst.Box.Y, src.Box.Bottom(), "forward edge %d: target sits below its source", e.Ordinal)
		for i := 1; i < len(e.Points); i++ {
			assert.GreaterOrEqual(t, e.Points[i].Y, e.Points[i-1].Y-0.5, "forward edge %d never climbs upward", e.Ordinal)
		}
	}
	ranks := make([]int, 0, len(top))
	for r := range top {
		ranks = append(ranks, r)
	}
	sort.Ints(ranks)
	for i := 1; i < len(ranks); i++ {
		assert.Greater(t, top[ranks[i]], bottom[ranks[i-1]], "rank %d starts below rank %d (no column wrapping)", ranks[i], ranks[i-1])
	}
}

// loopChannel returns the x and vertical extent of a loop return's longest
// vertical segment, which is its side-gutter channel.
func loopChannel(e layout.EdgeRoute) (x, y0, y1 float64) {
	best := -1.0
	for i := 1; i < len(e.Points); i++ {
		a, b := e.Points[i-1], e.Points[i]
		if a.X != b.X {
			continue
		}
		if l := math.Abs(b.Y - a.Y); l > best {
			best, x, y0, y1 = l, a.X, min(a.Y, b.Y), max(a.Y, b.Y)
		}
	}
	return x, y0, y1
}

// assertDistinctLoopChannels checks that loop returns use side gutters and that
// two returns whose channels overlap vertically on the same side never share a
// channel.
func assertDistinctLoopChannels(t *testing.T, geo layout.Geometry) {
	t.Helper()
	var loops []layout.EdgeRoute
	for _, e := range geo.Edges {
		if e.Kind == layout.EdgeLoopReturn {
			assert.Contains(t, []layout.Side{layout.SideLeft, layout.SideRight}, e.Side, "loop return %d uses a side gutter", e.Ordinal)
			assert.GreaterOrEqual(t, e.Lane, 1, "loop return %d has a gutter lane", e.Ordinal)
			loops = append(loops, e)
		}
	}
	require.NotEmpty(t, loops, "the sample has loop returns")
	for i := range loops {
		for j := i + 1; j < len(loops); j++ {
			a, b := loops[i], loops[j]
			if a.Side != b.Side {
				continue
			}
			ax, ay0, ay1 := loopChannel(a)
			bx, by0, by1 := loopChannel(b)
			if ay0 < by1 && by0 < ay1 {
				assert.NotEqual(t, ax, bx, "loop returns %d and %d overlap vertically and must not share a channel", a.Ordinal, b.Ordinal)
			}
		}
	}
}

// assertEvidenceOnDisk checks the reviewed result's own screenshots: an overview
// and tiles exist under the configured layout.artifactdir with matching hashes.
func assertEvidenceOnDisk(t *testing.T, env *layoutE2EEnv, ev layout.Evidence) {
	t.Helper()
	require.NotEmpty(t, ev.Artifacts)
	assert.Equal(t, "overview", ev.Artifacts[0].Kind)
	assert.Equal(t, len(ev.Artifacts)-1, ev.Tiles)
	root := filepath.Join(env.root, "layout-artifacts") + string(filepath.Separator)
	for _, a := range ev.Artifacts {
		assert.True(t, strings.HasPrefix(a.Path, root), "artifact %s is written under layout.artifactdir", a.Path)
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err, "evidence %s exists", a.Path)
		sum := sha256.Sum256(raw)
		assert.Equal(t, a.SHA256, hex.EncodeToString(sum[:]), "evidence %s is the reviewed file", a.Path)
	}
}

// ACCEPTANCE (spec Outcomes; design Phase 1 Happy path + Success): the bundled
// samples, seeded through the production seeding path, are arranged by the
// live worker with the real browser renderer and readability reviewer. They
// pass within five total attempts. The stored geometry is the reviewed one
// with continuous downward progression and distinct loop channels. The cache
// and the shared reader view serve that same geometry.
func TestLayoutRender_RealBrowserAcceptance(t *testing.T) {
	browser := requireBrowserE2E(t)
	cases := []struct {
		name         string
		nodes, edges int // 0 = not pinned
		loops        bool
	}{
		{name: "pr-review"},
		{name: "feature-pipeline", nodes: 66, edges: 79, loops: true},
		{name: "ralph-loop", loops: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useLayoutE2EBrowser(t, browser)
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5", realSample: tc.name})
			ds, _ := env.boot(t)
			ctx := context.Background()

			revision := layoutE2ESavedRevision(t, env.sharedPath(tc.name))
			rec := layoutRenderWaitTerminal(t, ds, revision, 20*time.Minute)

			// SINK 1: the authoritative SQLite record.
			require.Equal(t, layout.StatePassed, rec.State,
				"failure stage %q reason %q; unresolved findings %+v", rec.FailureStage, rec.FailureReason, rec.Findings)
			assert.Equal(t, layout.VerdictPassed, rec.Verdict)
			assert.False(t, rec.Unreviewed, "passed means actual rendered review passed")
			assert.Equal(t, 5, rec.AttemptLimit, "the YAML budget reached the row")
			assert.GreaterOrEqual(t, rec.AttemptsUsed, 1)
			assert.LessOrEqual(t, rec.AttemptsUsed, 5, "five attempts means five at most")
			assert.Zero(t, layout.BlockingCount(rec.Findings))
			require.Positive(t, rec.ResultGeneration)

			// SINK 2: the stored best result is the reviewed candidate.
			res, err := ds.layout.store.Result(ctx, revision, rec.ResultGeneration)
			require.NoError(t, err)
			require.NotNil(t, res.Review)
			assert.True(t, res.Review.Rendered)
			assert.Equal(t, layout.GeometryDigest(res.Geometry), res.Review.Evidence.GeometryDigest, "evidence belongs to the stored geometry")
			assert.Equal(t, res.Geometry.Contract, res.Review.Evidence.Contract)
			assert.True(t, res.Review.Evidence.FontsReady)
			assert.Equal(t, 0.9, res.Review.Evidence.Zoom, "reviewed at the practical zoom")
			assertEvidenceOnDisk(t, env, res.Review.Evidence)

			geo := res.Geometry
			if tc.nodes > 0 {
				assert.Len(t, geo.Nodes, tc.nodes, "every manifest node is drawn once")
				assert.Len(t, geo.Edges, tc.edges, "every edge occurrence is routed once")
			}
			wf := layoutE2EManifest(t, env.sharedPath(tc.name))
			assert.Len(t, geo.Nodes, len(wf.Nodes))
			assert.Len(t, geo.Edges, len(wf.Edges))
			assertDownwardArrangement(t, geo)
			if tc.loops {
				assertDistinctLoopChannels(t, geo)
			}

			// SINK 3: both views read the same revision-scoped geometry.
			view := ds.layout.reader.View(ctx, wf)
			assert.Equal(t, revision, view.SavedRevision)
			assert.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			assert.Equal(t, layout.ProvenanceArranged, view.Provenance)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(*view.Geometry))

			// SINK 4: the fenced layout.json cache holds the same result.
			require.Eventually(t, func() bool {
				entry, ok, cerr := ds.layout.cache.ReadCache(ctx, rec.Destination.Scope, rec.Destination.Name)
				return cerr == nil && ok && entry.Revision == revision &&
					entry.Generation == rec.ResultGeneration &&
					layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(geo)
			}, 60*time.Second, 100*time.Millisecond, "the current revision's reviewed geometry is published to layout.json")

			// Arrangement is presentation only: the saved manifest is unchanged.
			assert.Equal(t, revision, wf.Presentation.Revision, "publication never stamps a new authoring revision")
		})
	}
}
```

- [x] **Step 3: Run it (browser required)**

> Resolved on runtime revisit: the exact browser-required three-sample command passes (30.11s); after mutation/restoration and self-review, acceptance passes under -race (56.11s), each on attempt 1. Logs: /tmp/p3t7-acceptance-current.log and /tmp/p3t7-focused-final.log.

> Historical blocker: Actual full command failed as detailed under Step 2; ralph-loop failed again on an independent rerun (37.158s). Local logs: /tmp/layout-7-3-browser.log and /tmp/layout-7-3-repeat.log. No browserless skip is counted as acceptance.

Run: `ARBITRON_REQUIRE_BROWSER=1 go test ./cmd/arbitron/ -run TestLayoutRender_RealBrowserAcceptance -count=1 -v -timeout 60m`
Expected, with Chrome/Chromium installed and the `make ui` build containing `render.html`: `--- PASS: TestLayoutRender_RealBrowserAcceptance`, with subtests `pr-review`, `feature-pipeline` and `ralph-loop` each `--- PASS`; `ok`.

Without `ARBITRON_REQUIRE_BROWSER=1` and with no browser, the test is reported as `--- SKIP` with `no Chrome/Chromium available`.

A `passed` failure message lists the unresolved findings and the failure stage. Fix the defect the findings name, in P1T6 arrangement, P1T8 refinement, or the P3T2–P3T4 renderer. Do not relax the assertions or the P3T6 rules: these three samples are the spec's acceptance examples.

- [x] **Step 4: Confirm the default suite still holds without a browser**

Run: `env -u ARBITRON_REQUIRE_BROWSER go test ./cmd/arbitron/ -count=1`
Expected: `ok  	github.com/arbitron/arbitron/cmd/arbitron`. The acceptance test skips when no browser exists and runs when one does.

- [x] **Step 5: Commit**

> Resolved on runtime revisit: complete green three-sample acceptance is included in the mission's consolidated per-task commit.

> Historical blocker: Only the green pr-review root-provenance slice is committed (ec5b4ab); a three-sample acceptance commit would falsely claim the failures above were resolved. Leave this unchecked until the full acceptance test is green.

```bash
gofmt -l cmd/arbitron/
git add cmd/arbitron/layout_e2e_test.go cmd/arbitron/layout_render_e2e_test.go
git commit -m "test(layout): real-browser acceptance of pr-review, feature-pipeline and ralph-loop through initDomain" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.4: Browser runtime in the Docker image and the `make test-render` gate

**Files:**
- Modify: `Dockerfile`: the runtime-stage `RUN apk add --no-cache \` list and the `ENV` block before `VOLUME /data`
- Modify: `Makefile`: line 1 `.PHONY`, plus a new `test-render` target after `test-all`

The image runs as the non-root `arbitron` user. Chromium's sandbox needs user namespaces, which Docker's default seccomp profile blocks, so the image sets `ARBITRON_LAYOUT_BROWSERNOSANDBOX=true`. The container itself is the isolation boundary, and the render surface loads only loopback-served embedded assets (P3T5 `assetServer` binds `127.0.0.1` and rejects foreign hosts). Evidence goes to the `/data` volume. `@fontsource/inter` is bundled in the dashboard build. `font-noto` / `font-noto-cjk` / `font-noto-emoji` may remain general runtime packages; reviewed label coverage comes from P3T2 bundled fallback faces and cmap/sequence checks, never OS-dependent fallback.

- [x] **Step 1: Confirm the package pins are still current for v3.20**

> Resolved on runtime revisit: Docker responds. The exact pinned-image apk-policy command prints all four planned versions from v3.20 (/tmp/p3t7-apk-policy.log); no pin was guessed or changed.

> Historical blocker: The exact pinned-image apk-policy command cannot start: Docker Desktop's unix socket returns HTTP 500 for /_ping (after a prolonged wait); docker info also timed out. No apk policy output was produced. Do not guess the currently available versions or claim a built/runtime-tested image. Re-run this step with a healthy Docker daemon.

The pins are `chromium=131.0.6778.108-r0`, `font-noto=23.7.1-r0`, `font-noto-cjk=0_git20220127-r1` and `font-noto-emoji=2.042-r0`. These are the v3.20 community/main versions on pkgs.alpinelinux.org as of 2026-10-16. Check them, following the Dockerfile's "Refresh apk pins with `apk policy <pkg>` against the pinned base image" rule:

```bash
docker run --rm alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc \
  sh -c 'apk update -q && apk policy chromium font-noto font-noto-cjk font-noto-emoji'
```

Expected: `chromium policy:` lists `131.0.6778.108-r0:`, `font-noto policy:` lists `23.7.1-r0:`, `font-noto-cjk policy:` lists `0_git20220127-r1:` and `font-noto-emoji policy:` lists `2.042-r0:`, each followed by a `.../alpine/v3.20/...` repository line. If Alpine has since published a security rebuild, use the printed version for that package in Step 2 instead. An outdated pin makes `apk add` fail with `unable to select packages`.

- [x] **Step 2: Add the runtime packages and environment**

> Resolved on runtime revisit: all four verified packages are installed in the final runtime stage. Existing sandbox/artifact ENV defaults are unchanged. The new package guard fails before these additions and passes afterward. The actual image builds and reviews saved Unicode input.

> Historical blocker: The independent sandbox/artifact ENV defaults and host configuration/install documentation are implemented and tested, but package additions are intentionally withheld until Step 1 can validate the supply-chain pins. The current Dockerfile does NOT ship Chromium yet; host Chrome proves only host acceptance. This step remains incomplete.

In `Dockerfile`, change the end of the runtime `apk add` list from:

```dockerfile
        make=4.4.1-r2 \
 && npm install -g @anthropic-ai/claude-code@2.1.170 \
```

to:

```dockerfile
        make=4.4.1-r2 \
        chromium=131.0.6778.108-r0 \
        font-noto=23.7.1-r0 \
        font-noto-cjk=0_git20220127-r1 \
        font-noto-emoji=2.042-r0 \
 && npm install -g @anthropic-ai/claude-code@2.1.170 \
```

Directly above `VOLUME /data`, add:

```dockerfile
# Workflow auto-arrangement renders every candidate layout in headless
# Chromium (auto-detected on PATH) and reviews the screenshots. The image
# runs as a non-root user under Docker's default seccomp profile, which
# blocks Chromium's user-namespace sandbox, so the sandbox is disabled. The
# container is the isolation boundary, and the render surface only loads
# loopback-served embedded assets. Review evidence lives on the data volume.
ENV ARBITRON_LAYOUT_BROWSERNOSANDBOX=true \
    ARBITRON_LAYOUT_ARTIFACTDIR=/data/layout-artifacts
```

- [x] **Step 3: Add the `test-render` make target**

In `Makefile`, change line 1 to:

```make
.PHONY: ui build test test-ui test-all test-render dist samples-tarball install-samples release clean-dist test-release test-install
```

Change test-all to `test-all: test test-ui test-render`, ensure `test` depends on ui so parallel make cannot test stale embedded assets, then add:

```make
# Real-browser layout render/review gate: the render surface, browser
# renderer, readability reviewer and composition-root acceptance of the
# bundled samples. Needs Chrome/Chromium (ARBITRON_LAYOUT_BROWSERPATH picks
# one) and a `make ui` build that contains render.html. Fails, rather than
# skips, when no browser is available.
test-render: ui
	ARBITRON_REQUIRE_BROWSER=1 go test ./adapter/renderreview/ ./cmd/arbitron/ -run 'RealBrowser|RealSurface|LayoutRender|LayoutSurfaces' -count=1 -timeout 60m -v
```

- [x] **Step 4: Verify the image and the gate**

> Final runtime verification: `make test-all` passes, including its exact unfiltered test-render dependency (adapter/renderreview 241.021s; cmd/arbitron 887.809s). The final image/runtime/saved-input checks also pass as documented below. This closes the image and browser-gate step, not the separate full-race gate.

> Earlier runtime verification failure: image build, non-root executable/headless launch, ENV defaults and actual container measurement/capture/SQLite sinks now pass. `make test-render` rebuilds assets and adapter/renderreview passes (265.642s), but the whole cmd package fails at the unrelated TestGroupFilesE2E_DeadParentBeforeGatherRetriesFrozenFailure/periodic/failure (expected failed, actual not_applicable; missing parent-failure reason), finishing at 946.029s. All task/browser/route tests pass separately under -race. Log: /tmp/p3t7-test-render.log. No group lifecycle code or assertion was changed to force a release pass; that first gate run is not counted as a pass. The already-running complete test-all subsequently passes its same unfiltered browser gate without any source/assertion change.

> Historical blocker: Image build, executable, headless launch and ENV runtime checks require the unavailable Docker daemon and validated package additions above. Independent `make test-render` passed after rebuilding embedded assets: adapter/renderreview 114.705s, cmd/arbitron 10.493s, no SKIPs. This is the available producer/provenance suite, NOT the blocked three-sample or future P3T14 release acceptance.

Run: `docker build -t arbitron:layout-render .`
Expected: the build finishes with `naming to docker.io/library/arbitron:layout-render`. No `apk` error such as `unable to select packages`.

Run: `docker run --rm --entrypoint sh arbitron:layout-render -c 'command -v chromium-browser || command -v chromium'`
Expected: one absolute path, `/usr/bin/chromium-browser` or `/usr/bin/chromium`. Both names are in P3T5's linux auto-detect list, so no `layout.browserpath` is needed.

Run: `docker run --rm --entrypoint sh arbitron:layout-render -c 'B=$(command -v chromium-browser || command -v chromium); "$B" --headless --no-sandbox --disable-gpu --dump-dom about:blank'`
Expected: stdout contains `<html><head></head><body></body></html>`.

Run: `docker run --rm --entrypoint sh arbitron:layout-render -c 'echo $ARBITRON_LAYOUT_BROWSERNOSANDBOX $ARBITRON_LAYOUT_ARTIFACTDIR'`
Expected: `true /data/layout-artifacts`.

Run: `make test-render`
Expected, on a host with Chrome/Chromium: `ok  	github.com/arbitron/arbitron/adapter/renderreview` and `ok  	github.com/arbitron/arbitron/cmd/arbitron`. The `RealSurface*` (P3T5), `RealBrowser*` (P3T6) and `TestLayoutRender_*` (this task) tests all report `--- PASS`, none `--- SKIP`.

- [x] **Step 5: Commit**

> Runtime revisit: verified final-stage Chromium/font packages and green packaging/sample tests are committed together. The first aggregate-gate failure above is recorded, not counted as successful verification; the final complete test-all browser gate passes.

> Historical blocker: Independent ENV/docs/Makefile changes are committed (fdc8059), but the prescribed Chromium-shipping commit cannot be made until package pins and runtime/image checks succeed. No image completion is claimed.

```bash
git add Dockerfile Makefile
git commit -m "build(layout): ship Chromium in the image and add the make test-render gate" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7.5: Full verification

- [x] **Step 1: Whole Go suite under the race detector**

> Resolved (dead-parent release pass): after the fixture admission correction, the exact six-package race command passes without race warnings (cmd/arbitron 495.414s). Fresh make test-all and browser-required focused race verification also pass. The BLOCKED notes below are historical, not current failures. Logs: /tmp/p3t7-recovery-full-race-final.log, /tmp/p3t7-recovery-test-all.log, /tmp/p3t7-recovery-focused-final.log.

> BLOCKED (startup/launch correction pass): the three failures from the stage-entry pass are corrected and pass both the fresh exact full-race run and the final focused browser-required race run. The exact full-race command still fails in TestGroupFilesE2E_DeadParentBeforeGatherRetriesFrozenFailure/periodic/success (group_dead_parent_recovery_e2e_test.go:101, expected failure / actual success) and /periodic/failure (:103–113, expected failed gather / actual not_applicable, missing parent-failure reason, parent still running so Retry is refused). This dead-parent gather failure was already observed in 7.4; no gather logic/assertion is changed here. All five other packages pass; no race warning or package deadline exhaustion occurs. Log: /tmp/p3t7-startup-full-race.log. Do not repeat this aggregate gate without correcting that lifecycle seam.

> BLOCKED (stage-entry correction pass): the owned TestRun_StageTimeout entry race is corrected and domain/layout passes the exact full race command (32.624s); layouttest, SQLite (367.676s) and filesystem also pass. The command still fails at TestBrowserRenderer_QueuedCancellationAndLaunchTimeout (browser_boundary_test.go:158, launch-entry condition never satisfied), TestGraphStateE2E_RunningGroupShowsBothBranchesThroughCompositionRoot (graph_state_e2e_test.go:222, expected waiting / actual running), and TestGroupE2E_StartupReconcileSettlesGroupAfterCrash (group_e2e_test.go:625, PRAGMA journal_mode=WAL SQLITE_BUSY during reboot). No WARNING: DATA RACE or package-deadline exhaustion is emitted. Log: /tmp/p3t7-entry-full-race.log. These remaining launch/group lifecycle failures are not fixed by weakening assertions, changing production deadlines or repeating the aggregate gate.

> BLOCKED (runtime revisit): the exact full -race command fails again at TestRun_StageTimeout/render2, review1 and review2 (15ms entry timing). SQLite and cmd/arbitron also exhaust their unchanged 10m package deadlines, respectively while running TestMigration017_WorkflowIDColumns and building the stdio protocol binary; the cmd suite additionally fails the unrelated group-state waiting/running assertion. No WARNING: DATA RACE is emitted. Log: /tmp/p3t7-race.log. The restored task/root/real-route focused -race suite passes (301.179s); no production/test timeout or unrelated test was changed. This release gate remains unchecked rather than retried indefinitely.

> Earlier blocker: The exact race command was run twice. adapter/renderreview, adapter/sqlite, adapter/filesystem, domain/layout/layouttest and cmd/arbitron passed; neither run emitted WARNING: DATA RACE. domain/layout's existing TestRun_StageTimeout failed its reached/stage/attempt assertions in render2 on the first run and review1/review2 on the second. Its 15ms test budget is load-sensitive: an isolated -race rerun passed (1.589s). No production deadline or unrelated test was changed to hide this failure. The task's focused CLI/root/build/render tests separately pass under -race (46.442s).

Run: `go test -race ./domain/layout/... ./adapter/renderreview/ ./adapter/sqlite/ ./adapter/filesystem/ ./cmd/arbitron/ -count=1`
Expected: every package `ok`; no `WARNING: DATA RACE`.

- [x] **Step 2: No placeholder renderer remains anywhere**

Run: `grep -rn "rendererUnavailable\|errRendererNotInstalled\|renderer is not installed" --include='*.go' .`
Expected: no output.

- [x] **Step 3: Both command modes boot with the production renderer**

Run:

```bash
D=$(mktemp -d) && cat > "$D/config.yaml" <<YAML
auth:
  mode: "local"
database:
  path: "$D/arbitron.db"
workspace:
  path: "$D/workspaces"
samples:
  seed: false
logging:
  level: "warn"
  file: "$D/arbitron.log"
layout:
  browserpath: "/nonexistent/chromium"
YAML
go build -o "$D/arbitron" ./cmd/arbitron && printf '' | "$D/arbitron" mcp --config "$D/config.yaml" >/dev/null 2>&1; grep -m1 "no Chrome/Chromium browser found for layout review" "$D/arbitron.log"
```

Expected: grep prints the WARN line from the log, which shows that stdio `mcp` built the production adapter from the YAML `layout.browserpath`. The process exits when stdin closes. The temp config keeps the run away from `~/.arbitron`.

### Task 7.6: Make rendered review a required CI gate

**Files:** Modify Makefile; create `.github/workflows/layout.yml` (this checkout has only
installers.yml/release.yml, so there is no existing application test workflow to assume).

- [x] **RED:** Add a gate check that `make -n test-all` includes building the embedded
  UI and `ARBITRON_REQUIRE_BROWSER=1` render tests. Inspect the new workflow on pull_request
  and push: Go version from go.mod, Node 22 from the existing Docker dashboard-build baseline (dashboard/package.json currently has no engines field), npm ci,
  a real Chrome/Chromium install, then `make test-all`. No continue-on-error, browser skip,
  path filter excluding dashboard/renderreview/cmd/domain or fake evidence is allowed.
- [x] **GREEN:** `test-all` depends on test-render as well as test/test-ui; test-render
  depends on ui and runs both adapter/renderreview and cmd/arbitron browser suites.
  Include P3T14's TestLayoutSurfaces_ once produced. Use an explicit configured browser
  executable in CI. Prove ARBITRON_LAYOUT_BROWSERPATH=/missing with
  ARBITRON_REQUIRE_BROWSER=1 makes the gate fail, then run the real-browser gate to GREEN.
- [x] Commit Makefile/workflow after YAML validation and successful real-browser gate.

Actual verification: TestLayoutBuild_ failed before the make/UI/browser prerequisites, Docker evidence ENV and workflow existed, then passed. actionlint validates layout.yml. ARBITRON_LAYOUT_BROWSERPATH=/missing ARBITRON_REQUIRE_BROWSER=1 make test-render failed at the real browser tests (not skipped). Restoring real Chrome made the rebuilt gate green: adapter/renderreview 103.052s, cmd/arbitron 7.013s, no SKIPs. This gate is required by test-all and the new unfiltered push/pull_request workflow; the blocked three-sample/P3T14 release gate remains unclaimed.

## Execution evidence and ordered self-review

Selection: enumerated plan files in numeric phase/task order. Earlier unchecked tasks retain recorded protocol or producer/root/render gates; P3T7 is the next independent task and the only task modified in this pass. The unrelated pre-existing untracked docs/visuals directory is preserved.

TDD: missing-symbol RED, then compiling behavioral RED against the placeholder (directory/defaults, config validation, browser construction and broken assets). Real stack authoring/request/SQLite tests pass with the browser adapter's own missing-browser reason. Missing-browser Retry test goes RED if the root returns nil renderer/reviewer; restore goes GREEN. The sample option initially fails at the actual seeded-manifest sink when no sample is fed. pr-review then passes real measurements/DOM/PNG/reviewer/store/cache/Reader sinks. Full complex acceptance failures are recorded above, not suppressed or counted as completion.

Self-review applied in order before final commit:
1. **Spec compliance:** production BrowserRenderer/ReadabilityReviewer are installed for both serve and MCP, with exact configured assets/path/sandbox/artifact inputs, lazy missing-browser failure, boot WARN, broken-build boot rejection, and joined renderer shutdown. No new option, fake evidence, auth bypass or alternate renderer was added. Docker shipping and full three-sample/joint-route release acceptance remain explicitly incomplete.
2. **Anti-green-theater:** assertions consume actual SQLite state, stored reviewed geometry, hashed overview/tile files, filesystem cache and shared Reader output, plus unchanged authoring bytes. Negative controls cover absent wiring/assets/browser/config and wrong sandbox input. The sandbox-policy executable forwards to real Chrome only when --no-sandbox is fed: true yields real nonempty measured metrics; false fails visibly with empty metrics. Removing NoSandbox from the production BrowserConfig made the true control fail with ErrBrowserUnavailable, then restoration passed. Missing-browser required make gate fails, not skips. CI/build contract tests fail if required prerequisites/workflow are removed; these static checks are not substituted for browser evidence.
3. **Code quality:** read neighboring root/adapter helpers, retained existing stack/resource lifecycle, replaced stale placeholder references in test gates with the real reviewer/renderer, isolated legacy root tests with explicit unavailable-browser configuration, and applied gofmt/diff checks. Kept only the green single-sample provenance test rather than committing a knowingly failing or skipped three-sample release test. No unrelated timing, authorization, rename, seed-retention or query/UI work was changed.

Other verification: default cmd/arbitron suite passed (35.408s); all remaining TestLayoutE2E_ root tests passed; no placeholder symbols remain in Go sources; actual built stdio mcp exits on EOF and writes the configured missing-browser boot WARN. Both Cobra modes were exercised through TestLayoutE2E_ActualCommandsJoinOnContextAndEOF with real production adapters and seeded SQLite sinks. Host runtime/configuration prerequisites are documented in docs/install/README.md.

After the ordered self-review and sandbox mutation restoration: make test-render passed (adapter/renderreview 104.714s; cmd/arbitron 12.677s; no SKIPs), default cmd/arbitron suite passed again (38.381s), actionlint/gofmt/diff checks passed. The task remains BLOCKED on the precisely recorded complex-sample, Docker and full-race gates, not release-complete.

## Task 7.7: Close the actual sample readability and runtime release gates

**Owners:** this task owns production reproduction/sink acceptance; P1T6 owns route
placement/clearance/cost, P1T8 owns targeted refinement, P3T2/P3T4 own actual drawing/report
and P3T6 owns independent readability checks. P3T14 owns user-route parity. No duplicate
coordinator, reviewer or layout service is introduced.

- [x] Reinstall the complete 7.3 three-sample test before changing producer behavior and
  run through current initDomain/browser/SQLite, with five total attempts and immutable
  input. The recorded findings are reproduction leads, not proof that current code still
  fails. If current samples pass, retain that complete test and perform a targeted
  guard/input mutation; do not keep the acceptance gate absent merely to keep commits
  green. Never commit a knowingly failing acceptance test by itself.
- [x] For each remaining failure, record exact current revision/attempt/candidate digest,
  implicated route/label/crossing and its actual DOM plus readable tile region. Add the
  smallest geometry/render regression for that observable defect first, observe RED,
  then repair its named producer. Existing routingClearance/labelDrop/costOf already
  address some previously reported cases; verify those paths before inventing a second
  algorithm. Any change to gap/dash/marker rendering updates the shared TS renderer and
  independent Go expected-path/raster tests together, preserving the existing criteria.
- [x] Check success/failure/error/edges: all actual samples pass and retain every ordinal;
  erased branch ink/ambiguous bends/overlapping gaps still fail with clean reports and
  valid hashes; missing browser/artifacts fail visibly; parallel routes/Unicode/long
  canvas still pass full tile coverage. Actual manifest/execution config is byte/value
  unchanged. Reducing graph size, raising zoom/budget, loosening review or substituting
  a fake result is prohibited.
- [x] Run browser-required 7.3 and P3T14 both-route parity after each relevant fix. Finish
  Docker 7.4 on the pinned base with verified package versions and an actual container
  launch/measurement/capture; an unavailable local daemon is verification pending, not
  proof that the shipped image contains a browser. Keep 7.4 unchecked until verified.
- [x] Run make test-all and the focused race suite with fresh logs. If test-only timeout
  flakes recur, P1T9 owns replacing 15ms stage-start races with entry barriers and a
  generous outer deadline; keep stage timeout/cancellation assertions load-bearing and
  production timeouts unchanged. Profile repeated expensive route candidates before
  considering bounded work reuse; preserve deterministic routed-prefix selection and
  sample pass sinks. Do not hide failures by removing tests or increasing production
  attempt limits. This alignment review records no new browser/Docker/full-suite pass.

  - [x] Replace the owned 15ms coordinator stage-entry races with entry barriers, bounded asynchronous execution and a generous test-only deadline; retain actual stage timeout, cancellation, attempt and retained-best assertions. Healthy port delegates still consume their real arguments.
  - [x] Run fresh `make test-all`: all Go packages, TypeScript, 91 dashboard files / 1384 tests, rebuilt assets and the browser-required adapter (215.163s) / cmd (907.784s) gate pass. Log: /tmp/p3t7-entry-test-all.log.
  - [x] After self-review, run browser-required focused task/root and both real-route parity tests under `-race`; domain/layout passes (6.672s) and cmd/arbitron passes (191.782s), with no skips. Log: /tmp/p3t7-entry-focused-final.log.
  - [x] Close the exact full race gate after the launch/group lifecycle failures recorded in 7.5 are resolved.
    - [x] Separate queued-caller expiry from process-start scheduling and independently verify launch RenderTimeout; controlled startup delay passes and removing queue cancellation or RenderTimeout fails.
    - [x] Hold both real branch producers at bounded process gates and join the parent's persisted waiting transition before asserting the graph-state HTTP snapshot; the original tool outputs and frozen gather remain asserted.
    - [x] Reproduce startup SQLite lock contention and install the existing busy_timeout before journal_mode=WAL; real lock/WAL assertions and the composition-root crash/restart sink pass.

> BLOCKED (startup/launch correction pass): fresh make test-all passes all Go packages and TypeScript but stops at dashboard src/render/report.test.ts:48 (fontFamilyResolved / requires every bundled family and contract weight, 5000ms test timeout; 90 files/1383 tests pass, one fails). Its browser dependency is not reached. The separate final browser-required focused -race run passes actual samples, both routes and all corrected checks without skips. The full-race dead-parent gather failures recorded in 7.5 remain independent blockers. Logs: /tmp/p3t7-startup-test-all.log and /tmp/p3t7-startup-focused-final.log. Neither aggregate gate is repeatedly retried or relabeled green.

> BLOCKED (stage-entry correction pass): the original stage-entry defect is resolved, but the three named launch/group lifecycle failures in 7.5 keep this combined release gate incomplete. The full race command was run once after that correction, not repeatedly until green. `make test-all` is green independently; it is not evidence of a green full-race gate.

> BLOCKED (runtime revisit): make test-all and focused actual-browser task/route -race tests pass. The exact full race gate still fails at the stage-entry timing, group-state and package deadline cases detailed in 7.5. Keep this combined release box unchecked pending its owned test-timing work; no attempt budget, timeout, route sample or reviewer criterion was weakened. The first standalone test-render run's unrelated group-recovery failure is also recorded in 7.4; the final test-all browser dependency passes without a source/assertion change.

## Production runtime and complete sample acceptance revisit

- Selection: P3T2 retains its independently controlled second-browser/font-inventory blocker; it was not retried. Worked only P3T7's newly available Docker runtime and complete current-sample acceptance. Docker now responds, closing the former daemon blocker.
- Strict TDD: restored the complete three-sample test before changing any producer. It passes on the current source (30.11s); no new routing/reviewer implementation is necessary. Replacing its real configured browser input with an absent executable fails every sample at the actual SQLite `passed` assertion; restoration is green. The final-stage package test separately fails for all four missing pins, then passes after the minimal Dockerfile addition.
- Runtime: the exact pinned-base apk-policy command confirms all four versions. `docker build -t arbitron:layout-render .` succeeds. The final image runs as arbitron, auto-detects /usr/bin/chromium-browser, prints `true /data/layout-artifacts` and produces the requested blank-page DOM. The actual container server accepts saved Café/CJK/emoji node/branch labels over HTTP, measures and captures them through its real worker/browser/reviewer, and reaches passed generation 1. Both PNGs exist below /data/layout-artifacts with matching hashes; its copied authoritative SQLite row/input matches the unchanged saved manifest and HTTP revision. Container digest: deeb4baec9f7050ea8d09cc1e4df9263e986586fdb807b47d46abc9eaa40352f. Temporary containers are removed. Logs/script: /tmp/p3t7-apk-policy.log, /tmp/p3t7-docker-build.log, /tmp/p3t7-docker-smoke.log, /tmp/p3t7-container-runtime.py and /tmp/p3t7-container-runtime.log. An initial verification-script query used the wrong SQL column name; correcting it to input_manifest and repeating the fresh-container check passes, with no production change.
- Ordered self-review BEFORE final verification: **(1) Spec:** retained exactly the planned runtime pins and three actual seeded samples, budget five, downward rank/route and distinct-loop-channel assertions, immutable manifests, SQLite/result/evidence/cache/Reader sinks. Existing config/composition/shutdown feeds the installed runtime; no new option, fake, reviewer relaxation or sibling-task implementation. Current samples have no remaining geometry defects requiring a new producer regression. **(2) Anti-green-theater:** absent browser input fails all three persisted-state sinks; empty/missing geometry cannot satisfy nonempty cardinality, complete evidence/digest, loop and publication assertions. Actual container authoring verifies shipped assets/runtime rather than treating the static package guard as integration. Cache assertions also require the exact job generation. **(3) Quality:** followed adjacent test helpers, retained actual artifact hashing and unchanged-byte checks, used one sample helper and existing runtime environment, and ran gofmt/diff checks. No temporary mutation or embedded-asset diff remains.
- After review: browser-required task/root/defect and both actual WorkflowReview/ExecutionView parity tests pass under -race (301.179s; /tmp/p3t7-focused-final.log). Each seeded sample passes on attempt 1; successful logs include actual revision/digest. pr-review and feature-pipeline both match their persisted HTTP geometry on both routes. Existing wrong text/ancestor visibility/hidden or reversed marker/wrong middle route/runtime label/runtime crossing/blank capture tests remain green blocking controls. The full race gate is blocked precisely above.
- Final broad verification: `make test-all` **PASS**, including `go test ./...`, TypeScript, all **90 dashboard files / 1380 tests**, fresh embedded assets and the unfiltered browser-required gate (**adapter/renderreview 241.021s; cmd/arbitron 887.809s**). The earlier standalone `make test-render` failed only its group-recovery assertion (7.4); it is not reported as green. Logs: /tmp/p3t7-test-all.log, /tmp/p3t7-test-render.log. Full-race results remain separate and incomplete; there is no new claim of a complete race/release gate. `git diff --check` passes and the embedded build has no diff.

## Stage-entry correction pass (partial release gate)

Selected the first unchecked task in numeric phase/task order, P3T7. Its 7.7 step
explicitly authorizes correcting the owned coordinator stage-entry test race before
verification. This pass changes only that test and this plan; P3T15 remains untouched.

RED: the isolated old test happens to pass twenty race-detector repetitions. A temporary
30ms healthy measurement delay deterministically reproduces premature measurement timeout:
all four render/review cases fail their selected-stage/attempt/retained-best sinks
(`/tmp/p3t7-entry-load-red.log`). The barrier-based test, with a one-second test-only
stage timeout and ten-second outer bound, passes that identical control
(`/tmp/p3t7-entry-load-green.log`). The artificial delay is removed. Removing the
production measurement timeout makes the revised test fail at its bounded completion
assertion (`/tmp/p3t7-entry-missing-timeout-red.log`); production code is restored.

Ordered self-review before final verification/commit:
1. **Spec:** only the explicitly requested test-timing correction is implemented.
   Production stage/job/package timeouts, attempt budgets, renderer inputs and review
   rules are unchanged. Neither remaining release checkbox is falsely closed.
2. **Anti-green-theater:** the gate observes entry to the selected real coordinator port
   call, then its actual deadline and final failure stage, attempts, cancellation flag
   and retained-best result. Targeted hooks delegate the original request to the existing
   argument-honoring test ports before blocking. Missing production timeout fails the
   test rather than being mistaken for cancellation or a successful return.
3. **Quality:** named timing bounds, buffered result transfer, context cancellation and
   joined goroutine cleanup replace the unsynchronized reached flag and sub-second
   whole-job performance assertion. Neighboring coordinator helpers remain in use;
   no sleeps, production mutations, unrelated group fixes or embedded asset diffs remain.

After review, the exact full race command passes layout/layouttest/SQLite/filesystem but
fails the three precise launch/group lifecycle checks recorded under 7.5. There is no
race warning or package deadline exhaustion. Fresh `make test-all` passes including its
unfiltered browser-required gate. Those independent results are recorded separately;
this pass does not claim that the whole race/release gate is complete. Final browser-required
focused `-race` verification also passes the revised timeout test, complete three-sample
SQLite/evidence/cache/Reader acceptance, production renderer wiring and both actual routes
(`/tmp/p3t7-entry-focused-final.log`, cmd/arbitron 191.782s, no skips). `git diff --check`
and gofmt pass; production code and embedded assets have no diff.

## Startup/launch correction pass (partial release gate)

Selected P3T7, the first unchecked task. Rather than rerunning its old blocked gate
unchanged, this pass corrects its three named startup/observation failures. P3T15 is
untouched and no v2/group-geometry completion is claimed.

Strict TDD:
- New TestOpen_WaitsForStartupLockBeforeEnablingWAL holds a real exclusive database
  lock. Before implementation it fails immediately with setting PRAGMA journal_mode=WAL:
  SQLITE_BUSY; moving the unchanged 5000ms busy policy before WAL makes it wait and
  then return a real WAL/foreign-key-enabled connection after release. Logs:
  /tmp/p3t7-startup-lock-{red,green}.log.
- A temporary two-second fake-process admission delay reproduces the old one-second
  launch marker failure. Splitting queue expiry from launch timeout passes the same
  control; the delay is removed. Disabling production RenderTimeout or queued-context
  cancellation independently fails the new bounded assertions; both mutations are
  restored. Logs: /tmp/p3t7-launch-entry-{red,green}.log,
  /tmp/p3t7-launch-{timeout,queue}-mutation.log.
- A temporary 500ms delay before SuspendRunning reproduces the graph-state test's
  waiting/running mismatch. Bounded process gates plus the actual parent waiting
  checkpoint pass the identical control. That production delay is removed. Logs:
  /tmp/p3t7-graph-checkpoint-{red,green}.log.

Ordered self-review before final focused verification/commit:
1. **Spec:** changes are limited to the recorded release-gate corrections. The only
   production change orders existing SQLite pragmas safely during boot; no timeout,
   attempt budget, review threshold, execution behavior or new option is introduced.
   The two aggregate release steps stay unchecked with precise current failures.
2. **Anti-green-theater:** the DB test observes real lock contention and actual WAL
   state, not a mocked call order. The group test uses initDomain's real engine,
   processes, repository and HTTP reader, asserts both running node identities and
   parent waiting, then releases original alpha/beta producers and checks their frozen
   results. Removing launch timeout/queue cancellation fails, not a fake success.
   Existing real-browser acceptance still asserts SQLite/evidence/cache/Reader and
   actual embedded WorkflowReview/ExecutionView sinks.
3. **Quality:** reused neighboring process-gate/stack helpers; kept task output
   commands intact; bounded waits and buffered results prevent test hangs; DB cleanup
   releases its lock and joins the opener even on assertion failure. All temporary
   production mutations are restored; gofmt/diff checks pass, embedded assets unchanged.

Verification after review: browser-required focused -race tests pass
(adapter/renderreview 3.384s, SQLite 1.904s, cmd/arbitron 445.095s), with no skips or
race warnings. This includes all three samples (31.74s), both real user routes,
missing-browser/Retry, startup recovery and the corrected timing checks. The fresh
exact full race and make test-all failures are recorded under 7.5/7.7, not hidden by
repeating either gate. The task remains release-blocked on dead-parent gather recovery
and the dashboard font-resolution timeout; only the independently green corrections
are committed.

## Dead-parent and font-inventory release closure

Selected P3T7, the first unchecked task. Investigated the recorded failure instead of
repeating the aggregate gate unchanged. No P3T15 implementation or v2/group acceptance
is claimed; that task remains open.

Strict TDD:
- The existing dead-parent test reproduces periodic/failure's not_applicable gather,
  missing parent-failure reason, running parent and refused Retry under -race
  (/tmp/p3t7-dead-parent-before.log). Engine.Shutdown only joins current walkers;
  periodic queue wakes and the executor's direct waiting-parent claim can still admit
  another walker between that drain and SetFailed. The new crash-boundary assertion
  fails behaviorally without an admission barrier (/tmp/p3t7-dead-parent-red.log).
- Added a test-local SQLite trigger blocking BOTH waiting-to-queued and
  waiting-to-running parent transitions before draining. Real tasks, decisions,
  terminal receipts and all frozen-output/filesystem/Retry assertions remain intact.
  The trigger is removed before reconstructing startup's running crash image or
  performing periodic's Retry. Removed the ineffective ten-interval sleep. Both
  concrete production repository wake methods assert the barrier, preventing a
  silent fixture regression. All four cases pass under -race.
- Disabling the real periodic sweep makes both periodic cases fail at the durable
  done/continuation sink (/tmp/p3t7-dead-parent-missing-sweep.log); restoration passes.
- The complete-font-inventory characterization observes 63,903 family reads for 357
  faces before the fix (/tmp/p3t7-font-inventory-red.log). Replaced repeated scans
  with a one-pass set of normalized full face signatures. The test now observes
  exactly one family read per loaded input face; absent subsets, wrong family,
  weight, style, range and unloaded inputs still fail. Quoted families and normalized
  range spellings remain accepted. No test or production timeout was increased.

Ordered self-review before final verification and commit:
1. Spec: changes only correct the two recorded release-gate failures. No execution
   semantics, renderer criteria, attempt budget, new option or group-format feature
   was added. Existing text-specific asset/hash/font readiness is unchanged.
2. Anti-green-theater: initDomain still constructs the real coordinator, executor,
   gather reconciler and stores. The recovery test observes frozen SQLite output,
   untouched parent files, failure routing and original roster on Retry. Missing
   periodic input fails. Font signatures consume actual inventory fields, with
   missing/wrong/empty controls. Real browser acceptance checks all three samples
   and HTTP-to-embedded-route parity, not just constructors or intermediate calls.
3. Quality: retained neighboring repository and fixture patterns, bounded waits,
   deterministic SQLite barriers, collision-free signature keys and no cross-call
   cache. All temporary mutations are restored; gofmt/diff checks pass. make ui
   rebuilds successfully with no embedded-asset diff.

Verification:
- The first aggregate attempt caught a test-only compile error calling the claim
  method through InstanceRepository rather than its concrete SQLite adapter. Fixed
  that call and reran the focused cases before the final exact aggregate command;
  the build-failed attempt is not counted as a pass.
- Exact full-race gate: layout 27.218s, layouttest 1.595s, renderreview 21.157s,
  SQLite 289.350s, filesystem 33.235s, cmd/arbitron 495.414s; every package passes.
- Fresh make test-all passes all Go packages, TypeScript, 91 dashboard files / 1385
  tests and the rebuilt browser-required whole-package gate (renderreview 204.233s,
  cmd/arbitron 910.129s). Log: /tmp/p3t7-recovery-test-all.log.
- After ordered self-review, browser-required focused -race passes all TestLayoutRender_
  cases, production wiring, three-sample acceptance, both real workflow/instance
  sample routes and all four dead-parent cases (220.469s), no skips. Log:
  /tmp/p3t7-recovery-focused-final.log. TypeScript and the complete dashboard suite
  pass again (91 files / 1385 tests; /tmp/p3t7-recovery-dashboard-final.log).

All remaining P3T7 boxes are now checked. Historical failures above are retained as
execution evidence, not active blockers. P3T15 remains required independent work.
