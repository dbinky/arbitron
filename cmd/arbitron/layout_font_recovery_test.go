package main

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// Only the job server loses the subset; interactive routes keep their real assets.
type fontOutageAssets struct {
	fs.FS
	missing  *atomic.Bool
	rejected *atomic.Int64
}

func (a fontOutageAssets) Open(name string) (fs.File, error) {
	if a.missing.Load() && strings.Contains(name, "inter-latin-600-normal-") && (strings.HasSuffix(name, ".woff2") || strings.HasSuffix(name, ".woff")) { // the face and its CSS .woff fallback
		a.rejected.Add(1)
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return a.FS.Open(name)
}

// Arm the outage at the capture boundary, after real measurement has succeeded.
// All requests/results still pass unchanged through the production browser.
type fontCaptureOutage struct {
	layout.Renderer
	armed, missing *atomic.Bool
}

func (r fontCaptureOutage) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	if r.armed.Load() {
		r.missing.Store(true)
	}
	return r.Renderer.Render(ctx, req)
}

func TestLayoutSurfaces_ServerFontFailureRetainsSavedContentAndRetry(t *testing.T) {
	surfaceServerFontFailure(t, func(name string) *workflow.Workflow { return layoutE2EWorkflow(name, "Café 測試 ✅ é") })
}

func TestLayoutGroupSurfaces_ServerFontFailureRetainsSavedContentAndRetry(t *testing.T) {
	surfaceServerFontFailure(t, func(name string) *workflow.Workflow {
		wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
		wf.Name = name
		return wf
	})
}

func surfaceServerFontFailure(t *testing.T, manifest func(string) *workflow.Workflow) {
	t.Helper()
	browser := requireBrowserE2E(t)
	for _, stage := range []string{"measure", "capture"} {
		t.Run(stage, func(t *testing.T) {
			var missing, capture atomic.Bool
			var rejected, retryPosts atomic.Int64
			missing.Store(stage == "measure")
			capture.Store(stage == "capture")
			originalAssets := layoutRenderAssets
			assets := fontOutageAssets{originalAssets(), &missing, &rejected}
			layoutRenderAssets = func() fs.FS { return assets }
			t.Cleanup(func() { layoutRenderAssets = originalAssets })
			originalFactory := layoutRenderReviewerFactory
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
				return fontCaptureOutage{renderer, &capture, &missing}, reviewer, closeFn, err
			}
			t.Cleanup(func() { layoutRenderReviewerFactory = originalFactory })
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			useLayoutE2EBrowser(t, browser)
			ds, _ := e.boot(t)
			h := surfaceServer(t, ds).Config.Handler
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/layout/retry") {
					retryPosts.Add(1)
				}
				h.ServeHTTP(w, r)
			}))
			t.Cleanup(s.Close)
			name := "server-font-" + stage
			wf := manifest(name)
			surfaceAuthor(t, h, wf)
			saved := layoutE2EManifest(t, e.privatePath(name))
			before, err := os.ReadFile(e.privatePath(name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
			require.Equal(t, layout.StateFailed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			// Fixed category only: the missing-subset detail is logged, never persisted.
			require.NotContains(t, rec.FailureReason, "font asset unavailable")
			require.Positive(t, rejected.Load(), "the real job server must request the missing subset")
			require.Equal(t, layout.StageRendering, rec.FailureStage)
			if stage == "measure" {
				require.Equal(t, "measuring text failed", rec.FailureReason)
			} else {
				require.Equal(t, "rendering failed", rec.FailureReason)
			}
			id := surfaceSnapshot(t, ds, saved)
			page := surfaceBrowser(t)
			routes := []string{"/workflows/" + name, "/instances/" + id.String()}
			for _, route := range routes {
				view := surfaceView(t, h, "/api/v1"+route+"/layout")
				require.Equal(t, layout.DisplayFailed, view.DisplayStatus)
				require.True(t, view.RetryEligible)
				require.True(t, view.Unreviewed)
				require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('.react-flow__viewport') !== null && [...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(30*time.Second))))
				got := surfaceInspect(t, page)
				require.Contains(t, got.Panel, "failed")
				require.NotContains(t, got.Panel, "Layout reviewed")
				text, err := chromedp.Run(page, chromedp.Evaluate[string](`document.querySelector('[data-arranged-canvas]').textContent`))
				require.NoError(t, err)
				require.Contains(t, text, wf.Nodes[0].Label)
				require.NotContains(t, text, "Reload reviewed fonts", "server failure must use arrangement Retry, not client reload")
			}
			capture.Store(false)
			missing.Store(false)
			// Exercise workflow retry for measurement, snapshot retry for capture.
			if stage == "measure" {
				require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+routes[0]), chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(30*time.Second))))
			}
			require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
			require.Eventually(t, func() bool {
				r, err := ds.layout.store.Get(context.Background(), rec.Revision)
				return err == nil && r.JobGeneration == rec.JobGeneration+1 && r.State == layout.StatePassed
			}, time.Minute, 100*time.Millisecond)
			require.EqualValues(t, 1, retryPosts.Load())
			for _, route := range routes {
				view := surfaceView(t, h, "/api/v1"+route+"/layout")
				require.Equal(t, rec.Revision, view.SavedRevision)
				require.Equal(t, rec.JobGeneration+1, view.JobGeneration)
				surfaceReady(t, page, s.URL+route, "Layout reviewed")
				surfaceAssertGeometry(t, page, view, "Layout reviewed")
				if len(view.Geometry.GroupElements) > 0 {
					surfaceAssertGroups(t, page, view)
				}
			}
			after, err := os.ReadFile(e.privatePath(name))
			require.NoError(t, err)
			require.Equal(t, before, after, "server asset failure/retry must not rewrite saved content")
		})
	}
}
