package renderreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrowserRenderer_MeasureRequestBoundary(t *testing.T) {
	b := newTestRenderer(t, BrowserConfig{Assets: surfaceOnly(), BrowserPath: "/no/chrome", ArtifactDir: t.TempDir()})
	cases := map[string]func(*layout.MeasureRequest){
		"zero contract":      func(r *layout.MeasureRequest) { r.Contract = layout.RendererContract{} },
		"unknown contract":   func(r *layout.MeasureRequest) { r.Contract.Version = "unknown" },
		"duplicate key":      func(r *layout.MeasureRequest) { r.Items = append(r.Items, r.Items[0]) },
		"empty key":          func(r *layout.MeasureRequest) { r.Items[0].Key = "" },
		"nil items":          func(r *layout.MeasureRequest) { r.Items = nil },
		"style":              func(r *layout.MeasureRequest) { r.Items[0].Style = "unknown" },
		"zero max width":     func(r *layout.MeasureRequest) { r.Items[0].MaxWidth = 0 },
		"infinite max width": func(r *layout.MeasureRequest) { r.Items[0].MaxWidth = math.Inf(1) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := measureReq("a")
			mutate(&req)
			_, err := b.Measure(context.Background(), req)
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrBrowserUnavailable)
			assert.Nil(t, b.server)
		})
	}
}

func TestBrowserRenderer_MeasureRejectsInvalidMetrics(t *testing.T) {
	for name, metrics := range map[string]string{
		"negative": `{width:-1,height:17,lines:1}`, "zero": `{width:0,height:0,lines:0}`,
		"no lines": `{width:10,height:17,lines:0}`, "infinity": `{width:Infinity,height:17,lines:1}`,
		"NaN":           `{width:NaN,height:17,lines:1}`,
		"empty nonzero": `{width:10,height:17,lines:1}`,
	} {
		t.Run(name, func(t *testing.T) {
			surface := strings.Replace(echoSurface, "width: Math.min(it.text.length * 7, it.maxWidth), height: 17, lines: 1", strings.Trim(metrics, "{}"), 1)
			b := newTestRenderer(t, testBrowserConfig(t, fstest.MapFS{"render.html": {Data: []byte(surface)}}))
			req := measureReq("bad")
			if name == "empty nonzero" {
				req.Items[0].Text = ""
			}
			_, err := b.Measure(context.Background(), req)
			assert.Error(t, err)
		})
	}
}

func TestRenderJobDocument_DefensiveMapCopy(t *testing.T) {
	types := smallNodeTypes()
	req := layout.NewRenderRequest("revision_from_manifest", 7, smallGeometry(), types)
	types["a"] = "wrong_type"
	raw, err := renderJobDocument(req)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	typesDoc, ok := doc["workflowNodeTypes"].(map[string]any)
	require.True(t, ok, "missing node types in job")
	assert.Equal(t, "agent_call", typesDoc["a"])
	assert.Equal(t, "revision_from_manifest", doc["revision"])
	assert.Equal(t, float64(7), doc["attempt"])
}

func TestBrowserRenderer_EmptyReportAndNilGlobalsFail(t *testing.T) {
	for _, value := range []string{`null`, `{}`, `{ nodes: [], edges: [], states: [] }`, `{nodes: "invalid"}`} {
		t.Run(value, func(t *testing.T) {
			surface := `<!DOCTYPE html><script>window.__ARBITRON_RENDER_REPORT__=` + value + `;window.__ARBITRON_RENDER_STATE__='ready'</script>`
			cfg := testBrowserConfig(t, fstest.MapFS{"render.html": {Data: []byte(surface)}})
			b := newTestRenderer(t, cfg)
			_, err := b.Render(context.Background(), layout.NewRenderRequest("bad-report", 1, smallGeometry(), smallNodeTypes()))
			assert.Error(t, err)
			files, globErr := filepath.Glob(filepath.Join(cfg.ArtifactDir, "bad-report", "1", "capture-*"))
			require.NoError(t, globErr)
			assert.Empty(t, files, "failed unpublished capture must be cleaned")
		})
	}
}

func TestBrowserRenderer_CaptureIsImmutableAcrossRetry(t *testing.T) {
	cfg := testBrowserConfig(t, echoAssets())
	first := newTestRenderer(t, cfg)
	second := newTestRenderer(t, cfg)
	req := layout.NewRenderRequest("same-revision", 1, smallGeometry(), smallNodeTypes())
	original, err := first.Render(context.Background(), req)
	require.NoError(t, err)
	require.NotEmpty(t, original.Artifacts)
	bytesBefore := map[string][]byte{}
	for _, a := range original.Artifacts {
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		bytesBefore[a.Path] = raw
	}
	req.Geometry.Nodes[0].Text = "alternate"
	var results [2]layout.Rendering
	var errs [2]error
	var wg sync.WaitGroup
	for i, b := range []*BrowserRenderer{first, second} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = b.Render(context.Background(), req) }()
	}
	wg.Wait()
	paths := map[string]bool{}
	for _, a := range original.Artifacts {
		paths[a.Path] = true
	}
	for i, r := range results {
		require.NoError(t, errs[i])
		require.NotEmpty(t, r.Artifacts)
		assert.NotEqual(t, original.Artifacts[0].SHA256, r.Artifacts[0].SHA256, "different actual page pixels")
		for _, a := range r.Artifacts {
			assert.False(t, paths[a.Path], "published path reused")
			paths[a.Path] = true
			assert.Equal(t, filepath.Join(cfg.ArtifactDir, req.Revision, "1"), filepath.Dir(filepath.Dir(a.Path)))
			_, err := os.ReadFile(a.Path)
			require.NoError(t, err)
		}
	}
	for path, want := range bytesBefore {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestBrowserRenderer_QueuedCancellationAndLaunchTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake browser")
	}
	const outerDeadline = 10 * time.Second
	newFake := func(t *testing.T) (BrowserConfig, string) {
		t.Helper()
		dir := t.TempDir()
		marker := filepath.Join(dir, "started")
		fake := filepath.Join(dir, "chrome")
		require.NoError(t, os.WriteFile(fake, []byte(fmt.Sprintf("#!/bin/sh\ntouch %q\nexec sleep 60\n", marker)), 0755))
		return BrowserConfig{Assets: surfaceOnly(), BrowserPath: fake, ArtifactDir: dir}, marker
	}
	t.Run("queued caller cancels during launch", func(t *testing.T) {
		cfg, marker := newFake(t)
		b := newTestRenderer(t, cfg)
		firstCtx, cancelFirst := context.WithCancel(context.Background())
		defer cancelFirst()
		first := make(chan error, 1)
		go func() { _, err := b.Measure(firstCtx, measureReq("first")); first <- err }()
		// Observe actual process entry before testing the second caller. Process
		// scheduling is not required to beat a sub-second request deadline.
		require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, outerDeadline, 10*time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		queued := make(chan error, 1)
		go func() { _, err := b.Measure(ctx, measureReq("queued")); queued <- err }()
		select {
		case err := <-queued:
			require.ErrorIs(t, err, context.DeadlineExceeded)
			select {
			case err := <-first:
				t.Fatalf("the launch must still be blocked when the queued caller expires: %v", err)
			default:
			}
		case <-time.After(outerDeadline):
			t.Fatal("queued caller waited on the launch gate instead of its deadline")
		}
		cancelFirst()
		select {
		case err := <-first:
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(outerDeadline):
			t.Fatal("cancellation did not abort browser launch")
		}
	})
	t.Run("render timeout bounds launch", func(t *testing.T) {
		cfg, _ := newFake(t)
		cfg.RenderTimeout = 100 * time.Millisecond
		b := newTestRenderer(t, cfg)
		done := make(chan error, 1)
		go func() { _, err := b.Measure(context.Background(), measureReq("timeout")); done <- err }()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Contains(t, err.Error(), `(state "")`)
		case <-time.After(outerDeadline):
			t.Fatal("RenderTimeout did not bound browser launch")
		}
	})
}

func TestBrowserRenderer_CaptureFailureCleansOnlyUnpublishedDirectory(t *testing.T) {
	cfg := testBrowserConfig(t, echoAssets())
	cfg.TileWidth, cfg.TileHeight = 128, 128
	b := newTestRenderer(t, cfg)
	geo := smallGeometry()
	geo.Canvas = layout.Rect{W: 400, H: 300}
	req := layout.NewRenderRequest("cancel-capture", 1, geo, smallNodeTypes())
	published, err := b.Render(context.Background(), req)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.Render(ctx, req); done <- err }()
	require.Eventually(t, func() bool {
		files, _ := filepath.Glob(filepath.Join(cfg.ArtifactDir, req.Revision, "1", "capture-*", "overview.png"))
		return len(files) > 1
	}, 10*time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	dirs, err := filepath.Glob(filepath.Join(cfg.ArtifactDir, req.Revision, "1", "capture-*"))
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Dir(published.Artifacts[0].Path)}, dirs)
	for _, a := range published.Artifacts {
		_, err := os.ReadFile(a.Path)
		require.NoError(t, err)
	}
}

func TestBrowserRenderer_LargeCanvasOverviewAndTiles(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	geo := smallGeometry()
	geo.Canvas = layout.Rect{W: 4000, H: 2500}
	r, err := b.Render(context.Background(), layout.NewRenderRequest("large-canvas", 1, geo, smallNodeTypes()))
	require.NoError(t, err)
	require.Len(t, r.Artifacts, 10)
	overview := r.Artifacts[0]
	assert.InDelta(t, .5, overview.Scale, 1e-9)
	for _, a := range r.Artifacts {
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		image, err := png.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err)
		assert.InDelta(t, a.Region.W*a.Scale, float64(image.Width), 2)
		assert.InDelta(t, a.Region.H*a.Scale, float64(image.Height), 2)
	}
}

func TestBrowserRenderer_RelaunchesAfterBrowserDies(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	_, err := b.Measure(context.Background(), measureReq("before"))
	require.NoError(t, err)
	browser := chromedp.FromContext(b.browserCtx).Browser
	process := browser.Process()
	require.NotNil(t, process)
	require.NoError(t, process.Kill())
	select {
	case <-browser.LostConnection:
	case <-time.After(5 * time.Second):
		t.Fatal("browser did not exit")
	}
	m, err := b.Measure(context.Background(), measureReq("after-relaunch"))
	require.NoError(t, err)
	assert.Contains(t, m.Items, "after-relaunch")
	assert.NotEqual(t, process.Pid, chromedp.FromContext(b.browserCtx).Browser.Process().Pid)
}

func TestBrowserRenderer_CloseCancelsInflightCalls(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, echoAssets()))
	_, err := b.Measure(context.Background(), measureReq("warmup"))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := b.Measure(context.Background(), measureReq("hang")); done <- err }()
	time.Sleep(150 * time.Millisecond)
	b.Close()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close left an active call")
	}
	_, err = b.Measure(context.Background(), measureReq("after"))
	assert.ErrorContains(t, err, "renderer is closed")
}

// Crash after a real overview exists, while readable tiles are still being
// captured. Neither a between-call kill nor caller cancellation proves this
// failure boundary or preservation of an earlier review of the same attempt.
func TestBrowserRenderer_RealSurfaceCrashMidCapturePreservesReviewedEvidence(t *testing.T) {
	cfg := testBrowserConfig(t, embeddedSurface(t))
	b := newTestRenderer(t, cfg)
	coordinator, err := layout.NewCoordinator(b, NewReadabilityReviewer(nil))
	require.NoError(t, err)
	job := layout.Job{Revision: "crash-capture", MaxAttempts: 1, Manifest: &workflow.Workflow{
		Name: "crash-capture",
		Nodes: []workflow.Node{
			{ID: "a", Type: workflow.NodeTypeTransform, Label: "Start", Entrypoint: true},
			{ID: "b", Type: workflow.NodeTypeTransform, Label: "Finish"},
		},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}},
	}}
	first := coordinator.Run(context.Background(), job, nil)
	require.Equal(t, layout.VerdictPassed, first.Verdict, "%+v; %+v", first.Failure, first.Findings)
	require.NotNil(t, first.Best.Review)
	original := map[string][]byte{}
	for _, a := range first.Best.Review.Evidence.Artifacts {
		raw, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		original[a.Path] = raw
	}
	originalDir := filepath.Dir(first.Best.Review.Evidence.Artifacts[0].Path)
	browser := chromedp.FromContext(b.browserCtx).Browser
	process := browser.Process()
	require.NotNil(t, process)
	// Many small tiles leave a generous observation window after the overview;
	// readiness is observed from a written PNG, never inferred from a sleep.
	tileWidth, tileHeight := b.cfg.TileWidth, b.cfg.TileHeight
	b.cfg.TileWidth, b.cfg.TileHeight = 64, 64
	require.Greater(t, len(PlanTiles(first.Best.Geometry.Canvas, first.Best.Geometry.Contract.PracticalZoom, 64, 64, b.cfg.Overlap)), 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan layout.Result, 1)
	go func() { done <- coordinator.Run(ctx, job, nil) }()
	var partialDir string
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
observe:
	for {
		select {
		case result := <-done:
			t.Fatalf("render ended before crash injection: %+v", result)
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("no partial capture appeared")
		case <-ticker.C:
			files, err := filepath.Glob(filepath.Join(cfg.ArtifactDir, job.Revision, "1", "capture-*", "overview.png"))
			require.NoError(t, err)
			for _, path := range files {
				if filepath.Dir(path) != originalDir {
					partialDir = filepath.Dir(path)
					break observe
				}
			}
		}
	}
	require.NoError(t, process.Kill())
	failed := <-done
	require.Equal(t, layout.VerdictFailed, failed.Verdict)
	require.NotNil(t, failed.Failure)
	require.Equal(t, layout.StageRendering, failed.Failure.Stage)
	// The persisted reason is the fixed crash category; the adapter detail
	// (which step, the process error) is logged only.
	require.Equal(t, "rendering failed: browser exited", failed.Failure.Reason)
	require.True(t, failed.Unreviewed)
	require.Nil(t, failed.Best.Review)
	_, err = os.Stat(partialDir)
	require.True(t, os.IsNotExist(err), "unpublished partial capture survived: %v", err)
	require.Zero(t, b.server.pending())
	b.cfg.TileWidth, b.cfg.TileHeight = tileWidth, tileHeight
	// A subsequent real run relaunches Chrome, produces new evidence and passes.
	recovered := coordinator.Run(context.Background(), job, nil)
	require.Equal(t, layout.VerdictPassed, recovered.Verdict, "%+v; %+v", recovered.Failure, recovered.Findings)
	require.NotEqual(t, process.Pid, chromedp.FromContext(b.browserCtx).Browser.Process().Pid)
	for _, a := range recovered.Best.Review.Evidence.Artifacts {
		_, reused := original[a.Path]
		require.False(t, reused, "recovery reused a published artifact path")
	}
	for path, want := range original {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, want, got, "crash or recovery changed reviewed evidence")
	}
}
