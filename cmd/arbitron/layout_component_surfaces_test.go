package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

func TestLayoutSurfaces_MissingBundledSubsetRetainsFallback(t *testing.T) {
	surfaceClientFontFailure(t, layoutE2EWorkflow("missing-font-subset", "Café 測試 ✅ é"))
}

func TestLayoutGroupSurfaces_MissingBundledSubsetRetainsFallback(t *testing.T) {
	surfaceClientFontFailure(t, layoutOwnershipWorkflow(workflow.GroupFormBranches, false))
}

func surfaceClientFontFailure(t *testing.T, wf *workflow.Workflow) {
	t.Helper()
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	h := surfaceServer(t, ds).Config.Handler
	var missing atomic.Bool
	var rejected, retryPosts atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/layout/retry") {
			retryPosts.Add(1)
		}
		if missing.Load() && strings.Contains(r.URL.Path, "/inter-latin-600-normal-") && (strings.HasSuffix(r.URL.Path, ".woff2") || strings.HasSuffix(r.URL.Path, ".woff")) { // the face and its CSS .woff fallback
			rejected.Add(1)
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	name := wf.Name
	surfaceAuthor(t, h, wf)
	rev := layoutE2ESavedRevision(t, e.privatePath(name))
	rec := layoutRenderWaitTerminal(t, ds, rev, time.Minute)
	require.Equal(t, layout.StatePassed, rec.State)
	// Terminal review precedes asynchronous cache publication. Capture the immutable
	// baseline only after that real producer settles, before any client outage.
	require.Eventually(t, func() bool {
		settled, err := ds.layout.store.Get(context.Background(), rev)
		if err != nil || settled.CacheGeneration != settled.ResultGeneration {
			return false
		}
		rec = settled
		return true
	}, 5*time.Second, 20*time.Millisecond, "the real worker must publish this reviewed generation")
	result, err := ds.layout.store.Result(context.Background(), rev, rec.ResultGeneration)
	require.NoError(t, err)
	id := surfaceSnapshot(t, ds, layoutE2EManifest(t, e.privatePath(name)))
	for _, route := range []string{"/workflows/" + name, "/instances/" + id.String()} {
		t.Run(route, func(t *testing.T) {
			// Each route starts a new browser/font cache after the real server verdict.
			missing.Store(true)
			rejected.Store(0)
			page := surfaceBrowser(t)
			require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('font asset unavailable'))`, chromedp.WithPollingTimeout(30*time.Second))))
			require.Positive(t, rejected.Load(), "the actual saved labels must request the removed bundled subset")
			got := surfaceInspect(t, page)
			require.NotContains(t, got.Panel, "Layout reviewed")
			require.Empty(t, got.Nodes)
			require.Empty(t, got.Edges)
			fallback, err := chromedp.Run(page, chromedp.Evaluate[string](`document.querySelector('[data-arranged-canvas]').textContent`))
			require.NoError(t, err)
			require.Contains(t, fallback, wf.Nodes[0].Label)
			// The passed row stays non-retryable; the client's own action starts a fresh
			// page/font epoch (G8), not a fake ready flag.
			retry, err := chromedp.Run(page, chromedp.Evaluate[bool](`[...document.querySelectorAll('button')].some(b=>b.textContent.includes('Retry arrangement'))`))
			require.NoError(t, err)
			require.False(t, retry, "a valid passed result must not offer server Retry")
			missing.Store(false)
			require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.__beforeFontReload = true`)))
			clickCtx, cancelClick := context.WithTimeout(page, 30*time.Second)
			defer cancelClick()
			require.NoError(t, chromedp.Do(clickCtx, chromedp.Click(chromedp.Search(`//button[normalize-space()='Reload reviewed fonts']`))), "fallback for a passed row must offer the font reload action")
			// Evaluations fail while the old execution context is torn down; keep asking.
			require.Eventually(t, func() bool {
				ok, err := chromedp.Run(page, chromedp.Evaluate[bool](`window.__beforeFontReload === undefined && document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`))
				return err == nil && ok
			}, 30*time.Second, 100*time.Millisecond, "the action must start a fresh page/font epoch that draws the reviewed layout")
			view := surfaceView(t, h, "/api/v1"+route+"/layout")
			require.Equal(t, rev, view.SavedRevision)
			require.Equal(t, rec.JobGeneration, view.JobGeneration)
			require.Equal(t, rec.ResultGeneration, view.ResultGeneration)
			require.NotNil(t, view.Geometry)
			require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
			surfaceAssertGeometry(t, page, view, "Layout reviewed")
			if len(view.Geometry.GroupElements) > 0 {
				surfaceAssertGroups(t, page, view)
			}
			require.Zero(t, retryPosts.Load(), "client font recovery must never POST an arrangement retry")
			after, err := ds.layout.store.Get(context.Background(), rev)
			require.NoError(t, err)
			require.Equal(t, rec, after, "client recovery must preserve the persisted server verdict/job")
			afterResult, err := ds.layout.store.Result(context.Background(), rev, rec.ResultGeneration)
			require.NoError(t, err)
			require.Equal(t, result, afterResult, "client recovery must not replace reviewed geometry or evidence")
		})
	}
}

// A real script holds the engine at a running node until its supporting file is
// released. No instance state, trace, HTTP response or graph component is faked.
func TestLayoutSurfaces_ComponentRuntimeStylesPreserveReviewedGeometry(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	s := surfaceServer(t, ds)
	const name = "component-runtime"
	wf := &workflow.Workflow{
		Name: name,
		Nodes: []workflow.Node{
			{ID: "hold", Type: workflow.NodeTypeScript, Entrypoint: true, Label: "Café 測試 ✅ é", Config: workflow.NodeConfig{ScriptFile: "hold.sh", TimeoutSeconds: 180}},
			{ID: "done", Type: workflow.NodeTypeTransform, Label: "Done", Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "done", Operation: "set", Value: true}}}},
		},
		Edges: []workflow.Edge{{Source: "hold", Target: "done", Label: "continue"}},
	}
	surfaceAuthor(t, s.Config.Handler, wf)
	owner := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowRepo.WriteFile(owner, workflow.Private(localDefaultUser), name, "hold.sh", []byte("while [ ! -f release ]; do sleep 0.1; done\nprintf '{}'\n")))
	rev := layoutE2ESavedRevision(t, e.privatePath(name))
	rec := layoutRenderWaitTerminal(t, ds, rev, time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "%s %+v", rec.FailureReason, rec.Findings)
	id, _, err := ds.eng.Trigger(owner, name, nil, "normal")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ds.workflowRepo.WriteFile(context.Background(), workflow.Private(localDefaultUser), name, "release", []byte("released"))
	})
	require.Eventually(t, func() bool {
		inst, err := ds.eng.GetInstance(owner, id)
		return err == nil && inst.State == instance.StateRunning && inst.CurrentNodeID != nil && *inst.CurrentNodeID == "hold"
	}, 10*time.Second, 20*time.Millisecond)
	page := surfaceBrowser(t)
	for _, route := range []string{"/workflows/" + name, "/instances/" + id.String()} {
		view := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
		surfaceReady(t, page, s.URL+route, "Layout reviewed")
		surfaceAssertGeometry(t, page, view, "Layout reviewed")
		if route != "/workflows/"+name {
			require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-node="hold"][data-state="running"]') !== null && document.querySelector('[data-edge-overlay="0"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
		}
		// Select through the route's actual React Flow click handler.
		require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-arranged-node="hold"]').dispatchEvent(new MouseEvent('click',{bubbles:true}))`), chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-node="hold"]').className.includes('selected')`, chromedp.WithPollingTimeout(10*time.Second))))
		for _, phase := range []float64{0, 0.25, 0.5, 0.75, 1} {
			// Seek live CSS animations, including both pulse extrema and overlay
			// phases. Unlike render.html this is the interactive production SPA.
			js := fmt.Sprintf(`(() => {const animations=document.getAnimations(); for(const a of animations){a.pause();a.currentTime=a.effect.getComputedTiming().duration * %v;} return animations.length;})()`, phase)
			animations, err := chromedp.Run(page, chromedp.Evaluate[int](js))
			require.NoError(t, err)
			if route != "/workflows/"+name {
				require.GreaterOrEqual(t, animations, 2, "real running-node pulse and edge overlay must be present")
			}
			surfaceAssertGeometry(t, page, view, "Layout reviewed")
			opacity, err := chromedp.Run(page, chromedp.Evaluate[string](`getComputedStyle(document.querySelector('[data-arranged-node="hold"]')).opacity`))
			require.NoError(t, err)
			require.Equal(t, "1", opacity)
			readable, err := chromedp.Run(page, chromedp.Evaluate[bool](`(() => {
 const visible = e => { for(let p=e;p;p=p.parentElement){ const s=getComputedStyle(p); if(s.display==='none'||s.visibility!=='visible'||+s.opacity!==1) return false; } return true; };
 const texts = [...document.querySelectorAll('[data-node-text]')];
 const fit = texts.every(e=>{const n=e.closest('[data-arranged-node]').getBoundingClientRect(),range=document.createRange();range.selectNodeContents(e);return visible(e)&&[...range.getClientRects()].every(r=>r.left>=n.left&&r.top>=n.top&&r.right<=n.right&&r.bottom<=n.bottom);});
 const overlays = [...document.querySelectorAll('[data-edge-overlay]')];
 return texts.length>0 && fit && overlays.every(e=>{const base=document.querySelector('[data-edge-path="'+e.dataset.edgeOverlay+'"]'),s=getComputedStyle(e);return e.getAttribute('d')===base.getAttribute('d')&&!e.hasAttribute('marker-end')&&+s.strokeOpacity>0&&+s.strokeOpacity<1&&parseFloat(s.strokeWidth)<=parseFloat(getComputedStyle(base).strokeWidth);});
})()`))
			require.NoError(t, err)
			require.True(t, readable, "text glyphs and the non-occluding overlay must remain readable at phase %v", phase)
		}
	}
	// Release and join real execution before closing the root/database.
	require.NoError(t, ds.workflowRepo.WriteFile(owner, workflow.Private(localDefaultUser), name, "release", []byte("released")))
	require.Eventually(t, func() bool {
		inst, err := ds.eng.GetInstance(owner, id)
		return err == nil && inst.State == instance.StateCompleted
	}, 10*time.Second, 20*time.Millisecond)
}
