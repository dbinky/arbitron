package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// Faults apply only at the HTTP boundary, after the real authorised handler has
// produced its saved-result JSON. Successful requests, authoring, review and
// retries use the root's real collaborators. This is client rejection evidence,
// not the complex-sample release gate owned by P3T7/P3T14.
func TestLayoutSurfaces_ClientValidationAndAPIReachDOM(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	h := adapthttp.NewRouterFromDeps(ds.routerDeps(nil, dashboard.Handler(), nil, nil))
	var fault atomic.Int64
	var layoutReads atomic.Int64
	faults := []struct {
		name, issue string
		change      func(map[string]any)
	}{
		{"same-version padding", "renderer contract.nodePaddingX", func(v map[string]any) {
			c := v["geometry"].(map[string]any)["contract"].(map[string]any)
			c["nodePaddingX"] = c["nodePaddingX"].(float64) + 1
		}},
		{"full node text", "text", func(v map[string]any) {
			v["geometry"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["text"] = "wrong saved text"
		}},
		{"attachment", "edge 0 has an invalid route", func(v map[string]any) {
			a := v["geometry"].(map[string]any)["edges"].([]any)[0].(map[string]any)["sourceAttach"].(map[string]any)
			a["nodeId"] = "not-the-source"
		}},
		{"empty geometry", "geometry has 0 nodes", func(v map[string]any) {
			g := v["geometry"].(map[string]any)
			g["nodes"], g["edges"] = []any{}, []any{}
		}},
		{"missing reviewed geometry", "missing reviewed geometry", func(v map[string]any) { delete(v, "geometry") }},
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isLayout := r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/layout")
		if isLayout {
			layoutReads.Add(1)
		}
		// Instance GET also carries an initial LayoutView; corrupt that same seam
		// so a valid embedded value cannot mask a rejected polling response.
		isInstance := r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/instances/") && strings.Count(r.URL.Path, "/") == 4
		f := int(fault.Load())
		if f == 0 || (!isLayout && !isInstance) {
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			w.Header()[key] = values
		}
		if rec.Code != http.StatusOK {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		view := body
		if isInstance {
			view, _ = body["layout"].(map[string]any)
		}
		if view != nil {
			faults[f-1].change(view)
		}
		w.WriteHeader(rec.Code)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(s.Close)
	const name = "client-validation"
	surfaceAuthor(t, h, layoutE2EWorkflow(name, "Saved client seam"))
	saved := layoutE2EManifest(t, e.privatePath(name))
	id := surfaceSnapshot(t, ds, saved)
	rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 3*time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "%+v", rec.Findings)

	for _, route := range []string{"/workflows/" + name, "/instances/" + id.String()} {
		t.Run(route, func(t *testing.T) {
			page := surfaceBrowser(t)
			fault.Store(0)
			view := surfaceView(t, h, "/api/v1"+route+"/layout")
			require.Equal(t, saved.SavedRevision(), view.SavedRevision)
			beforeReads := layoutReads.Load()
			surfaceReady(t, page, s.URL+route, "Layout reviewed")
			surfaceAssertGeometry(t, page, view, "Layout reviewed")
			require.Greater(t, layoutReads.Load(), beforeReads, "the real API client must fetch the saved result")
			for i, f := range faults {
				t.Run(f.name, func(t *testing.T) {
					fault.Store(int64(i + 1))
					require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+route)))
					expr := fmt.Sprintf(`document.querySelector('[data-layout-mode="fallback"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes(%q))`, f.issue)
					require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](expr, chromedp.WithPollingTimeout(10*time.Second))), "DOM: %+v", surfaceInspect(t, page))
					got := surfaceInspect(t, page)
					require.NotContains(t, got.Panel, "Layout reviewed")
					require.Contains(t, got.Panel, "Layout needs attention")
					// Assert the retained graph sink, not just a rejected-validator hop.
					positions, err := chromedp.Run(page, chromedp.Evaluate[map[string]layout.Point](`Object.fromEntries([...document.querySelectorAll('.react-flow__node')].map(e=>{const m=new DOMMatrix(getComputedStyle(e).transform);return [e.dataset.id,{x:m.e,y:m.f}]}))`))
					require.NoError(t, err)
					require.Len(t, positions, len(saved.Nodes))
					for _, n := range saved.Nodes {
						require.Equal(t, layout.Point{X: n.Position.X, Y: n.Position.Y}, positions[n.ID])
					}
					text, err := chromedp.Run(page, chromedp.Evaluate[string](`document.querySelector('[data-arranged-canvas]').textContent`))
					require.NoError(t, err)
					for _, n := range saved.Nodes {
						require.Contains(t, text, n.Label)
					}
					require.NotContains(t, text, "wrong saved text")
				})
			}
			fault.Store(0)
			// Actual result loss exposes Retry through the production reader. Both
			// API retry methods must post this saved revision and reach SQLite/DOM.
			before, err := ds.layout.store.Get(context.Background(), saved.SavedRevision())
			require.NoError(t, err)
			_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", saved.SavedRevision())
			require.NoError(t, err)
			require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null && [...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(10*time.Second))))
			require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
			require.Eventually(t, func() bool {
				r, err := ds.layout.store.Get(context.Background(), saved.SavedRevision())
				return err == nil && r.JobGeneration > before.JobGeneration && r.State == layout.StatePassed
			}, time.Minute, 50*time.Millisecond)
			require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`, chromedp.WithPollingTimeout(10*time.Second))))
			retried := surfaceView(t, h, "/api/v1"+route+"/layout")
			require.Equal(t, saved.SavedRevision(), retried.SavedRevision)
			require.Greater(t, retried.JobGeneration, before.JobGeneration)
			surfaceAssertGeometry(t, page, retried, "Layout reviewed")
			require.Equal(t, saved.SavedRevision(), layoutE2ESavedRevision(t, e.privatePath(name)))
		})
	}
}
