package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Delay the existing engine queue boundary AFTER Trigger has persisted its real
// immutable snapshot, but BEFORE any sample agent/script executor can start.
// Cancellation joins Trigger; releasing the boundary delegates to the real queue.
type surfaceQueueGate struct {
	engine.QueueManager
	entered chan uuid.UUID
	release chan struct{}
}

func (g *surfaceQueueGate) Enqueue(ctx context.Context, id, owner uuid.UUID) (bool, error) {
	g.entered <- id
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-g.release:
		return g.QueueManager.Enqueue(ctx, id, owner)
	}
}

func surfaceSnapshot(t *testing.T, ds *domainStack, wf *workflow.Workflow) uuid.UUID {
	t.Helper()
	g := &surfaceQueueGate{QueueManager: ds.queueMgr, entered: make(chan uuid.UUID, 1), release: make(chan struct{})}
	ds.eng.SetQueueManager(g)
	ctx, cancel := context.WithCancel(adminCtxForLocalUser(t, ds))
	done := make(chan struct{})
	vars := map[string]any{}
	for _, input := range wf.Inputs {
		if input.Required {
			vars[input.Name] = "visualization-only-never-executed"
		}
	}
	go func() {
		defer close(done)
		_, _, _ = ds.eng.Trigger(ctx, wf.Name, vars, "normal")
	}()
	t.Cleanup(func() { cancel(); <-done; ds.eng.SetQueueManager(ds.queueMgr) })
	select {
	case id := <-g.entered:
		inst, err := ds.eng.GetInstance(ctx, id)
		require.NoError(t, err)
		require.Equal(t, instance.StateQueued, inst.State)
		require.Equal(t, wf.SavedRevision(), snapshotRevision(t, ds, id))
		trace, err := ds.eng.GetTrace(ctx, id)
		require.NoError(t, err)
		require.Empty(t, trace, "sample executors must never run to obtain a graph")
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("Trigger did not reach the real queue boundary")
		return uuid.Nil
	}
}

func surfaceServer(t *testing.T, ds *domainStack) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(adapthttp.NewRouterFromDeps(ds.routerDeps(nil, dashboard.Handler(), nil, nil)))
	t.Cleanup(s.Close)
	return s
}

func surfaceBrowser(t *testing.T) context.Context {
	t.Helper()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(requireBrowserE2E(t)), chromedp.WindowSize(1440, 1000))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, stopAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, stopTab := chromedp.NewContext(alloc)
	ctx, stopDeadline := context.WithTimeout(ctx, 2*time.Minute)
	t.Cleanup(func() { stopDeadline(); stopTab(); stopAlloc() })
	return ctx
}

func surfaceView(t *testing.T, h http.Handler, path string) layout.View {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
	var view layout.View
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	return view
}

// This collector reads only actual DOM attributes, computed styles and browser
// bounds. It does not call the production converter/path builder or echo inputs.
const surfaceDOM = `((instanceID=null) => {
 const all = selector => [...document.querySelectorAll(selector)].filter(e=>!instanceID||e.dataset.layoutInstance===instanceID);
 const viewport = document.querySelector('.react-flow__viewport');
 const canvas = document.querySelector('[data-arranged-canvas]');
 const panel = [...document.querySelectorAll('[role="status"]')].map(e=>e.textContent).join(' ');
 if (!viewport) return {mode:canvas?.dataset.layoutMode, panel, nodes:[], edges:[]};
 const m = new DOMMatrix(getComputedStyle(viewport).transform);
 const origin = viewport.parentElement.getBoundingClientRect();
 const box = e => { const r=e.getBoundingClientRect(); return {x:(r.x-origin.x-m.e)/m.a,y:(r.y-origin.y-m.f)/m.d,w:r.width/m.a,h:r.height/m.d}; };
 const visible = e => { for(let a=e;a;a=a.parentElement) { const s=getComputedStyle(a); if(s.display==='none'||s.visibility!=='visible'||+s.opacity===0) return false; } return true; };
 const nodes = all('[data-arranged-node]').map(e=>{
   const label=e.querySelector('[data-node-text="label"]'), type=e.querySelector('[data-node-text="type"]');
   return {id:e.dataset.arrangedNode,box:box(e),text:label.textContent,typeCaption:type?.textContent??'',visible:visible(e)&&visible(label)&&(!type||visible(type))};
 });
 const edges = all('[data-edge-path]').map(e=>{
   const ref=e.getAttribute('marker-end'); const marker=document.getElementById(ref?.match(/#([^)]*)/)?.[1]);
   const label=all('[data-edge-label]').find(l=>l.dataset.edgeLabel===e.dataset.edgePath);
   return {ordinal:+e.dataset.edgePath,d:e.getAttribute('d'),kind:e.dataset.edgeKind,visible:visible(e),stroke:getComputedStyle(e).stroke,strokePx:parseFloat(getComputedStyle(e).strokeWidth),dash:getComputedStyle(e).strokeDasharray,marker:marker?{orient:marker.getAttribute('orient'),units:marker.getAttribute('markerUnits'),refX:+marker.getAttribute('refX'),refY:+marker.getAttribute('refY'),d:marker.querySelector('path')?.getAttribute('d'),fill:getComputedStyle(marker.querySelector('path')).fill,visible:visible(marker)&&visible(marker.querySelector('path'))}:null,label:label?{text:label.textContent,box:box(label),visible:visible(label)}:null};
 });
 return {mode:canvas?.dataset.layoutMode,panel,zoom:m.a,nodes,edges};
})()`

type surfaceDOMResult struct {
	Mode, Panel string
	Zoom        float64
	Nodes       []struct {
		ID, Text, TypeCaption string
		Box                   layout.Rect
		Visible               bool
	}
	Edges []struct {
		Ordinal               int
		D, Kind, Stroke, Dash string
		StrokePx              float64
		Visible               bool
		Marker                *struct {
			Orient, Units, D, Fill string
			RefX, RefY             float64
			Visible                bool
		}
		Label *struct {
			Text    string
			Box     layout.Rect
			Visible bool
		}
	}
}

func surfaceInspect(t *testing.T, ctx context.Context) surfaceDOMResult {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[surfaceDOMResult](surfaceDOM))
	require.NoError(t, err)
	return got
}

func surfaceReady(t *testing.T, ctx context.Context, url, heading string) {
	t.Helper()
	require.NoError(t, chromedp.Do(ctx, chromedp.Navigate(url)))
	expr := fmt.Sprintf(`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes(%q))`, heading)
	require.NoError(t, chromedp.Do(ctx, chromedp.Poll[chromedp.Void](expr, chromedp.WithPollingTimeout(30*time.Second))))
}

func surfaceRect(t *testing.T, want, got layout.Rect) {
	t.Helper()
	// Chrome uses fractional CSS layout units; persisted sizing rounds separately.
	for i, v := range []float64{want.X, want.Y, want.W, want.H} {
		require.InDelta(t, v, []float64{got.X, got.Y, got.W, got.H}[i], 0.1)
	}
}

// Independently derive the expected M/L route, including every persisted gap.
// surfacePath independently models dashboard routedPath.ts:buildRoutedPath,
// including the dash restarts a loop return gets before its gaps and across the
// crossings it passes over.
func surfacePath(geo layout.Geometry, edge layout.EdgeRoute) string {
	pattern := ""
	if edge.Kind != layout.EdgeForward {
		pattern = geo.Contract.LoopReturnDash
	}
	return surfaceRoutePath(geo, fmt.Sprintf("edge:%d", edge.Ordinal), edge.Points, pattern)
}

func surfaceRoutePath(geo layout.Geometry, key string, points []layout.Point, pattern string) string {
	xy := func(p layout.Point) string {
		f := func(n float64) string { return strconv.FormatFloat(math.Round(n*100)/100, 'f', -1, 64) }
		return f(p.X) + "," + f(p.Y)
	}
	half := geo.Contract.CrossingGapPx / 2
	on := 0.0
	if pattern != "" {
		on, _ = strconv.ParseFloat(strings.Fields(strings.ReplaceAll(pattern, ",", " "))[0], 64)
	}
	cmds := []string{"M " + xy(points[0])}
	fresh := true
	gaps, overs := []layout.Point{}, []layout.Point{}
	for _, c := range geo.GroupCrossings {
		if c.Under == key {
			gaps = append(gaps, c.Point)
		}
		if c.Over == key && on > 0 {
			overs = append(overs, c.Point)
		}
	}
	for _, c := range geo.Crossings {
		if fmt.Sprintf("edge:%d", c.Under) == key {
			gaps = append(gaps, c.Point)
		}
		if fmt.Sprintf("edge:%d", c.Over) == key && on > 0 {
			overs = append(overs, c.Point)
		}
	}
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		length := math.Hypot(b.X-a.X, b.Y-a.Y)
		if length == 0 {
			continue
		}
		at := func(d float64) layout.Point {
			return layout.Point{X: a.X + (b.X-a.X)*d/length, Y: a.Y + (b.Y-a.Y)*d/length}
		}
		take := func(points *[]layout.Point) []float64 {
			var out []float64
			for k := len(*points) - 1; k >= 0; k-- {
				p := (*points)[k]
				d := ((p.X-a.X)*(b.X-a.X) + (p.Y-a.Y)*(b.Y-a.Y)) / length
				projected := at(math.Max(0, math.Min(length, d)))
				if d >= -0.5 && d <= length+0.5 && math.Hypot(p.X-projected.X, p.Y-projected.Y) <= 0.5 {
					out = append(out, math.Max(0, math.Min(length, d)))
					*points = append((*points)[:k], (*points)[k+1:]...)
				}
			}
			return out
		}
		type event struct {
			at  float64
			gap bool
		}
		var events []event
		for _, cut := range take(&gaps) {
			events = append(events, event{cut, true})
			if on > 0 {
				events = append(events, event{math.Max(0, cut-half-on), false})
			}
		}
		for _, cut := range take(&overs) {
			for r := cut - half - 1; ; r += on {
				events = append(events, event{math.Max(0, r), false})
				if r+on >= cut+half+1 {
					break
				}
			}
		}
		from := func(e event) float64 {
			if e.gap {
				return e.at - half
			}
			return e.at
		}
		sort.SliceStable(events, func(i, j int) bool {
			if from(events[i]) != from(events[j]) {
				return from(events[i]) < from(events[j])
			}
			return events[i].gap && !events[j].gap
		})
		cursor := 0.0
		for _, e := range events {
			if !e.gap {
				if e.at >= length || e.at < cursor || (e.at == cursor && fresh) {
					continue
				}
				if e.at > cursor {
					cmds = append(cmds, "L "+xy(at(e.at)))
				}
				cmds = append(cmds, "M "+xy(at(e.at)))
				cursor, fresh = e.at, true
				continue
			}
			start := math.Max(cursor, e.at-half)
			end := math.Min(length, e.at+half)
			if end <= cursor {
				continue
			}
			if start > cursor {
				cmds = append(cmds, "L "+xy(at(start)))
			}
			cmds = append(cmds, "M "+xy(at(end)))
			cursor, fresh = end, true
		}
		if cursor < length {
			cmds = append(cmds, "L "+xy(b))
			fresh = false
		}
	}
	return strings.Join(cmds, " ")
}

func surfaceAssertGeometry(t *testing.T, ctx context.Context, view layout.View, heading string) {
	t.Helper()
	surfaceAssertGeometryScript(t, ctx, view, heading, surfaceDOM)
}

func surfaceAssertGeometryScript(t *testing.T, ctx context.Context, view layout.View, heading, script string) {
	t.Helper()
	require.NotNil(t, view.Geometry)
	g := *view.Geometry
	// React Flow mounts edges after measuring node handles and can remount on
	// a status refresh. Collect the complete DOM in the SAME browser task that
	// checks readiness, not in a second round trip that can see half a remount.
	ready := fmt.Sprintf(`(() => { const actual = %s; return actual.mode === 'arranged' && actual.nodes.length === %d && actual.edges.length === %d ? actual : false; })()`, script, len(g.Nodes), len(g.Edges))
	got, err := chromedp.Run(ctx, chromedp.Poll[surfaceDOMResult](ready, chromedp.WithPollingTimeout(30*time.Second)))
	require.NoError(t, err)
	require.Equal(t, "arranged", got.Mode)
	require.Contains(t, got.Panel, heading)
	if script == surfaceDOM {
		require.InDelta(t, g.Contract.PracticalZoom, got.Zoom, 0.001)
	} else {
		// Scoped live tiles share the all-active follow viewport, which can
		// animate its zoom without changing any inverse-transformed geometry.
		require.Positive(t, got.Zoom)
	}
	require.Len(t, got.Nodes, len(g.Nodes))
	for _, n := range g.Nodes {
		found := false
		for _, actual := range got.Nodes {
			if actual.ID != n.ID {
				continue
			}
			found = true
			surfaceRect(t, n.Box, actual.Box)
			require.Equal(t, n.Text, actual.Text)
			require.Equal(t, n.TypeCaption, actual.TypeCaption)
			require.True(t, actual.Visible)
		}
		require.True(t, found, "missing node %s", n.ID)
	}
	require.Len(t, got.Edges, len(g.Edges))
	for _, e := range g.Edges {
		found := false
		for _, actual := range got.Edges {
			if actual.Ordinal != e.Ordinal {
				continue
			}
			found = true
			require.Equal(t, string(e.Kind), actual.Kind)
			require.Equal(t, surfacePath(g, e), actual.D, "edge %d full persisted route", e.Ordinal)
			require.True(t, actual.Visible)
			require.Equal(t, g.Contract.StrokePx, actual.StrokePx)
			require.NotNil(t, actual.Marker)
			require.True(t, actual.Marker.Visible)
			require.Equal(t, "auto", actual.Marker.Orient)
			require.Equal(t, "userSpaceOnUse", actual.Marker.Units)
			require.Equal(t, g.Contract.ArrowPx, actual.Marker.RefX)
			require.Equal(t, g.Contract.ArrowPx/2, actual.Marker.RefY)
			require.Equal(t, fmt.Sprintf("M 0,0 L %v,%v L 0,%v Z", g.Contract.ArrowPx, g.Contract.ArrowPx/2, g.Contract.ArrowPx), actual.Marker.D)
			color := g.Contract.ForwardColor
			if e.Kind != layout.EdgeForward {
				color = g.Contract.LoopReturnColor
				require.NotEqual(t, "none", actual.Dash)
			} else {
				require.Equal(t, "none", actual.Dash)
			}
			var red, green, blue int
			_, err := fmt.Sscanf(color, "#%02x%02x%02x", &red, &green, &blue)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("rgb(%d, %d, %d)", red, green, blue), actual.Stroke)
			require.Equal(t, actual.Stroke, actual.Marker.Fill)
			if e.Text == nil {
				require.Nil(t, actual.Label)
			} else {
				require.NotNil(t, actual.Label)
				require.Equal(t, e.Text.Text, actual.Label.Text)
				surfaceRect(t, e.Text.Box, actual.Label.Box)
				require.True(t, actual.Label.Visible)
			}
		}
		require.True(t, found, "missing edge %d", e.Ordinal)
	}
	if script == surfaceDOM {
		t.Logf("DOM matched actual HTTP revision=%s generation=%d digest=%s", view.SavedRevision, view.ResultGeneration, layout.GeometryDigest(g))
	} else {
		t.Logf("DOM matched uniformly translated snapshot revision=%s generation=%d", view.SavedRevision, view.ResultGeneration)
	}
}

func surfaceAuthor(t *testing.T, h http.Handler, wf *workflow.Workflow) {
	t.Helper()
	raw, err := json.Marshal(wf)
	require.NoError(t, err)
	code, body := layoutE2EDo(t, h, http.MethodPost, "/api/v1/workflows/", string(raw))
	require.Equal(t, http.StatusCreated, code, "%v", body)
}

// Both actual acceptance samples must reach the workflow and snapshot routes;
// the complex sample below additionally exercises persisted crossing gaps.
func TestLayoutSurfaces_PRReviewWorkflowAndInstanceUseReviewedGeometry(t *testing.T) {
	surfaceWorkflowAndInstanceParity(t, "pr-review")
}

// feature-pipeline carries the persisted crossings, dashed loop returns and
// branch labels that pr-review lacks, so both routes must reproduce the
// gap-split, dash-restarted routes the reviewer actually passed (P3T14 Step 1).
func TestLayoutSurfaces_FeaturePipelineWorkflowAndInstanceUseReviewedGeometry(t *testing.T) {
	surfaceWorkflowAndInstanceParity(t, "feature-pipeline")
}

func surfaceWorkflowAndInstanceParity(t *testing.T, name string) {
	t.Helper()
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	s := surfaceServer(t, ds)
	wf := layoutE2EManifest(t, filepath.Join("..", "..", "samples", name, "workflow.json"))
	surfaceAuthor(t, s.Config.Handler, wf)
	saved := layoutE2EManifest(t, e.privatePath(name))
	id := surfaceSnapshot(t, ds, saved)
	rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), 5*time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "%s; findings %+v", rec.FailureReason, rec.Findings)
	result, err := ds.layout.store.Result(context.Background(), rec.Revision, rec.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	assertEvidenceOnDisk(t, e, result.Review.Evidence)
	ctx := surfaceBrowser(t)
	for _, route := range []string{"/workflows/" + name, "/instances/" + id.String()} {
		view := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
		require.Equal(t, rec.Revision, view.SavedRevision)
		require.Equal(t, rec.ResultGeneration, view.ResultGeneration)
		require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
		surfaceReady(t, ctx, s.URL+route, "Layout reviewed")
		surfaceAssertGeometry(t, ctx, view, "Layout reviewed")
	}
	if name == "feature-pipeline" {
		// Guard the fixture: without these the test would not exercise gaps or dashes.
		require.NotEmpty(t, result.Geometry.Crossings, "feature-pipeline must reach the routes with persisted crossings")
		loops := 0
		for _, edge := range result.Geometry.Edges {
			if edge.Kind != layout.EdgeForward {
				loops++
			}
		}
		require.Positive(t, loops, "feature-pipeline must reach the routes with dashed loop returns")
	}
}

func TestLayoutSurfaces_HistoricalRetryAfterSaveCacheLossAndRestart(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	ds, closeRoot := e.boot(t)
	s := surfaceServer(t, ds)
	const name = "historical-surface"
	surfaceAuthor(t, s.Config.Handler, layoutE2EWorkflow(name, "Original snapshot"))
	first := layoutE2ESavedRevision(t, e.privatePath(name))
	rec := layoutRenderWaitTerminal(t, ds, first, time.Minute)
	require.Equal(t, layout.StatePassed, rec.State)
	original := surfaceView(t, s.Config.Handler, "/api/v1/workflows/"+name+"/layout")
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), name, nil, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.eng.GetInstance(adminCtxForLocalUser(t, ds), id)
		return err == nil && inst.State == instance.StateCompleted
	}, 10*time.Second, 20*time.Millisecond)
	raw, err := json.Marshal(layoutE2EWorkflow(name, "New current definition"))
	require.NoError(t, err)
	code, body := layoutE2EDo(t, s.Config.Handler, http.MethodPut, "/api/v1/workflows/"+name, string(raw))
	require.Equal(t, http.StatusOK, code, "%v", body)
	second := layoutE2ESavedRevision(t, e.privatePath(name))
	require.NotEqual(t, first, second)
	newRow := layoutRenderWaitTerminal(t, ds, second, time.Minute)
	require.Equal(t, layout.StatePassed, newRow.State)
	ctx := adminCtxForLocalUser(t, ds)
	require.Eventually(t, func() bool {
		cache, ok, err := ds.layout.cache.ReadCache(ctx, newRow.Destination.Scope, name)
		return err == nil && ok && cache.Revision == second
	}, 10*time.Second, 20*time.Millisecond)
	// Real storage loss makes a formerly passed historical result unavailable;
	// Retry must invalidate it and run the immutable old snapshot, not R2.
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", first)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(e.privatePath(name)), "layout.json")))
	s.Close()
	closeRoot()
	ds, _ = e.boot(t)
	s = surfaceServer(t, ds)
	page := surfaceBrowser(t)
	route := "/instances/" + id.String()
	err = chromedp.Do(page, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null && [...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(30*time.Second)))
	require.NoError(t, err, "actual DOM: %+v", surfaceInspect(t, page))
	before := surfaceInspect(t, page)
	require.NotContains(t, before.Panel, "Layout reviewed")
	require.Contains(t, before.Panel, "attention")
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
	require.Eventually(t, func() bool {
		r, err := ds.layout.store.Get(context.Background(), first)
		return err == nil && r.JobGeneration > rec.JobGeneration && r.State == layout.StatePassed
	}, time.Minute, 100*time.Millisecond)
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`, chromedp.WithPollingTimeout(30*time.Second))))
	history := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
	require.Equal(t, first, history.SavedRevision)
	require.Greater(t, history.JobGeneration, original.JobGeneration)
	require.Equal(t, layout.GeometryDigest(*original.Geometry), layout.GeometryDigest(*history.Geometry))
	surfaceAssertGeometry(t, page, history, "Layout reviewed")
	current := surfaceView(t, s.Config.Handler, "/api/v1/workflows/"+name+"/layout")
	require.Equal(t, second, current.SavedRevision)
	require.Equal(t, newRow.JobGeneration, current.JobGeneration)
	surfaceReady(t, page, s.URL+"/workflows/"+name, "Layout reviewed")
	surfaceAssertGeometry(t, page, current, "Layout reviewed")
	require.Equal(t, second, layoutE2ESavedRevision(t, e.privatePath(name)))

	// The authorised immutable instance also repairs a missing row after a real
	// ownership move, without needing current workflow visibility or a journal.
	id2, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), name, nil, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.eng.GetInstance(adminCtxForLocalUser(t, ds), id2)
		return err == nil && inst.State == instance.StateCompleted
	}, 10*time.Second, 20*time.Millisecond)
	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(adminCtxForLocalUser(t, ds), name, owner))
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", second)
	require.NoError(t, err)
	_, err = ds.db.Exec("DELETE FROM layout_revisions WHERE revision = ?", second)
	require.NoError(t, err)
	route = "/instances/" + id2.String()
	require.NoError(t, chromedp.Do(page, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(30*time.Second))))
	require.NotContains(t, surfaceInspect(t, page).Panel, "Layout reviewed")
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
	row := layoutRenderWaitTerminal(t, ds, second, time.Minute)
	require.Equal(t, layout.StatePassed, row.State)
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`, chromedp.WithPollingTimeout(30*time.Second))))
	relocated := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
	movedCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	moved, err := ds.layout.query.WorkflowLayout(movedCtx, name)
	require.NoError(t, err)
	require.Equal(t, relocated.SavedRevision, moved.SavedRevision)
	require.Equal(t, layout.GeometryDigest(*relocated.Geometry), layout.GeometryDigest(*moved.Geometry))
	surfaceAssertGeometry(t, page, relocated, "Layout reviewed")
	require.NoDirExists(t, filepath.Dir(e.privatePath(name)), "old-locator cache publication must not recreate the folder")
	require.FileExists(t, filepath.Join(e.workspaceDir, "users", owner.String(), name, "workflow.json"))
}

// blockingReviewer runs the real readability review, then adds one blocking
// finding. Every bundled sample now passes review on the first attempt, so the
// failure display needs a defect the producer cannot repair.
type blockingReviewer struct{ layout.Reviewer }

const blockingReviewerEvidence = "injected blocking readability finding"

func (b blockingReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	rv, err := b.Reviewer.Review(ctx, req)
	if err == nil {
		rv.Findings = append(rv.Findings, layout.Finding{Criterion: layout.CriterionIndistinctBranch, Severity: layout.SeverityBlocking, Source: layout.SourceReview, Evidence: blockingReviewerEvidence})
	}
	return rv, err
}

func TestLayoutSurfaces_NeedsAttentionShowsFindingsNotReviewed(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, closeFn, err := old(cfg, logger)
		return renderer, blockingReviewer{reviewer}, closeFn, err
	}
	ds, _ := e.boot(t)
	s := surfaceServer(t, ds)
	const name = "feature-pipeline"
	wf := layoutE2EManifest(t, filepath.Join("..", "..", "samples", name, "workflow.json"))
	surfaceAuthor(t, s.Config.Handler, wf)
	rev := layoutE2ESavedRevision(t, e.privatePath(name))
	rec := layoutRenderWaitTerminal(t, ds, rev, 3*time.Minute)
	require.Equal(t, layout.StateNeedsAttention, rec.State)
	require.NotEmpty(t, rec.Findings)
	var injected bool
	for _, f := range rec.Findings {
		injected = injected || f.Evidence == blockingReviewerEvidence
	}
	require.True(t, injected, "the persisted record carries the reviewer's finding: %+v", rec.Findings)
	view := surfaceView(t, s.Config.Handler, "/api/v1/workflows/"+name+"/layout")
	require.NotNil(t, view.Geometry)
	page := surfaceBrowser(t)
	surfaceReady(t, page, s.URL+"/workflows/"+name, "Layout needs attention")
	surfaceAssertGeometry(t, page, view, "Layout needs attention")
	got := surfaceInspect(t, page)
	require.NotContains(t, got.Panel, "Layout reviewed")
	for _, finding := range view.Findings {
		require.Contains(t, got.Panel, finding.Evidence)
	}
}

func TestLayoutSurfaces_RenderFailureRetainsFallbackAndRetry(t *testing.T) {
	// The interactive browser exists; only the production job browser is missing.
	requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	s := surfaceServer(t, ds)
	const name = "render-failure-surface"
	surfaceAuthor(t, s.Config.Handler, layoutE2EWorkflow(name, "Retained failed render"))
	rev := layoutE2ESavedRevision(t, e.privatePath(name))
	rec := layoutE2EWaitTerminal(t, ds, rev)
	require.Equal(t, layout.StateFailed, rec.State)
	page := surfaceBrowser(t)
	err := chromedp.Do(page, chromedp.Navigate(s.URL+"/workflows/"+name), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null && [...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(30*time.Second)))
	require.NoError(t, err, "actual DOM: %+v", surfaceInspect(t, page))
	got := surfaceInspect(t, page)
	require.Contains(t, got.Panel, "failed")
	require.NotContains(t, got.Panel, "Layout reviewed")
	text, err := chromedp.Run(page, chromedp.Evaluate[string](`document.querySelector('[data-arranged-canvas]').textContent`))
	require.NoError(t, err)
	require.Contains(t, text, "Retained failed render")
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement').click()`)))
	require.Eventually(t, func() bool {
		r, err := ds.layout.store.Get(context.Background(), rev)
		return err == nil && r.JobGeneration > rec.JobGeneration && r.State == layout.StateFailed
	}, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, rev, layoutE2ESavedRevision(t, e.privatePath(name)))
}

// A boundary delay delegates to the real browser unchanged after release.
type surfaceRenderGate struct {
	layout.Renderer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *surfaceRenderGate) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-ctx.Done():
		return layout.Rendering{}, ctx.Err()
	case <-g.release:
		return g.Renderer.Render(ctx, req)
	}
}

func TestLayoutSurfaces_CompletedInstanceRefreshesWithoutWebSocket(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	old := layoutRenderReviewerFactory
	var gate *surfaceRenderGate
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, close, err := newLayoutRenderReviewer(cfg, logger)
		gate = &surfaceRenderGate{Renderer: renderer, entered: make(chan struct{}), release: make(chan struct{})}
		return gate, reviewer, close, err
	}
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	ds, _ := e.boot(t)
	h := adapthttp.NewRouterFromDeps(ds.routerDeps(nil, dashboard.Handler(), nil, nil))
	var polls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/instances/") && strings.HasSuffix(r.URL.Path, "/layout") {
			polls.Add(1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	const name = "completed-before-review"
	surfaceAuthor(t, s.Config.Handler, layoutE2EWorkflow(name, "Café 測試 ✅ é"))
	rev := layoutE2ESavedRevision(t, e.privatePath(name))
	select {
	case <-gate.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("production renderer not reached")
	}
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), name, nil, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		inst, err := ds.eng.GetInstance(adminCtxForLocalUser(t, ds), id)
		return err == nil && inst.State == instance.StateCompleted
	}, 10*time.Second, 20*time.Millisecond)
	ctx := surfaceBrowser(t)
	var executionEvents atomic.Int64
	_, err = chromedp.Call(ctx, network.Enable, network.EnableParams{})
	require.NoError(t, err)
	eventsCtx, stopEvents := context.WithCancel(ctx)
	frames := cdp.Events(eventsCtx, chromedp.FromContext(ctx).Target, network.WebSocketFrameReceived)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for frame, err := range frames {
			if err != nil {
				return
			}
			var payload struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(frame.Response.PayloadData), &payload) == nil && (strings.HasPrefix(payload.Type, "node.") || payload.Type == "instance.state_changed") {
				executionEvents.Add(1)
			}
		}
	}()
	defer func() { stopEvents(); <-eventsDone }()
	route := "/instances/" + id.String()
	require.NoError(t, chromedp.Do(ctx, chromedp.Navigate(s.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null`, chromedp.WithPollingTimeout(20*time.Second))))
	before := surfaceInspect(t, ctx)
	require.NotContains(t, before.Panel, "Layout reviewed")
	require.Eventually(t, func() bool { return polls.Load() >= 2 }, 10*time.Second, 50*time.Millisecond)
	// Unmount WHILE review is pending, not after terminal polling already stopped.
	require.NoError(t, chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](`window.__surfaceUnmountSentinel=true; document.querySelector('a[href="/settings"]').click()`), chromedp.Poll[chromedp.Void](`location.pathname === '/settings' && document.querySelector('[data-arranged-canvas]') === null && window.__surfaceUnmountSentinel`, chromedp.WithPollingTimeout(10*time.Second))))
	atUnmount := polls.Load()
	require.Never(t, func() bool { return polls.Load() != atUnmount }, 3*time.Second, 50*time.Millisecond)
	require.NoError(t, chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](`history.back()`), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null && window.__surfaceUnmountSentinel`, chromedp.WithPollingTimeout(20*time.Second))))
	eventsBeforeRelease := executionEvents.Load()
	close(gate.release)
	rec := layoutRenderWaitTerminal(t, ds, rev, time.Minute)
	require.Equal(t, layout.StatePassed, rec.State, "%s %+v", rec.FailureReason, rec.Findings)
	// No navigation and no execution event: polling must replace the fallback.
	err = chromedp.Do(ctx, chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`, chromedp.WithPollingTimeout(30*time.Second)))
	require.NoError(t, err, "actual DOM: %+v", surfaceInspect(t, ctx))
	view := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
	surfaceAssertGeometry(t, ctx, view, "Layout reviewed")
	require.Equal(t, eventsBeforeRelease, executionEvents.Load(), "review completion must refresh without execution WebSocket events")
}
