package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/dashboard"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Inspect actual DOM bounds and paint; no production converter is called.
const surfaceGroupDOM = `((instanceID=null) => {
 const all = selector => [...document.querySelectorAll(selector)].filter(e=>!instanceID||e.dataset.layoutInstance===instanceID);
 const viewport=document.querySelector('.react-flow__viewport');
 const m=new DOMMatrix(getComputedStyle(viewport).transform), origin=viewport.parentElement.getBoundingClientRect();
 const box=e=>{const r=e.getBoundingClientRect();return {x:(r.x-origin.x-m.e)/m.a,y:(r.y-origin.y-m.f)/m.d,w:r.width/m.a,h:r.height/m.d};};
 const visible=e=>{for(let p=e;p;p=p.parentElement){const s=getComputedStyle(p);if(s.display==='none'||s.visibility!=='visible'||+s.opacity===0)return false;}return true;};
 return {fonts:document.fonts.status==='loaded',elements:all('[data-arranged-group-element]').map(e=>({id:e.dataset.arrangedGroupElement,box:box(e),visible:visible(e),border:parseFloat(getComputedStyle(e).borderWidth),overlay:e.querySelector('[data-group-overlay]')?box(e.querySelector('[data-group-overlay]')):null,texts:[...e.querySelectorAll('[data-group-text]')].map(t=>({key:t.dataset.groupText,text:t.textContent,box:box(t),visible:visible(t)}))})),connectors:all('path[data-group-connector]').map(e=>({id:e.dataset.groupConnector,d:e.getAttribute('d'),visible:visible(e),strokePx:parseFloat(getComputedStyle(e).strokeWidth),dash:getComputedStyle(e).strokeDasharray,arrow:!!document.getElementById(e.getAttribute('marker-end')?.match(/#([^)]*)/)?.[1])}))};
})()`

func surfaceAssertGroups(t *testing.T, ctx context.Context, view layout.View) {
	t.Helper()
	surfaceAssertGroupsScript(t, ctx, view, surfaceGroupDOM)
}

func surfaceAssertGroupsScript(t *testing.T, ctx context.Context, view layout.View, script string) {
	t.Helper()
	require.NotNil(t, view.Geometry)
	g := *view.Geometry
	// Fonts are part of readiness: on a slow runner the overlay mounts while
	// document.fonts is still loading, and a one-shot check then races it.
	ready := fmt.Sprintf(`(() => { const a=%s; return a.fonts && a.elements.length===%d && a.connectors.length===%d ? a : false; })()`, script, len(g.GroupElements), len(g.GroupConnectors))
	got, err := chromedp.Run(ctx, chromedp.Poll[struct {
		Fonts    bool
		Elements []struct {
			ID      string
			Box     layout.Rect
			Visible bool
			Border  float64
			Overlay *layout.Rect
			Texts   []struct {
				Key, Text string
				Box       layout.Rect
				Visible   bool
			}
		}
		Connectors []struct {
			ID, D, Dash    string
			Visible, Arrow bool
			StrokePx       float64
		}
	}](ready, chromedp.WithPollingTimeout(10*time.Second)))
	require.NoError(t, err)
	require.True(t, got.Fonts)
	require.NotEmpty(t, g.GroupElements)
	require.Len(t, got.Elements, len(g.GroupElements))
	for _, e := range g.GroupElements {
		found := false
		for _, actual := range got.Elements {
			if e.ID != actual.ID {
				continue
			}
			found = true
			surfaceRect(t, e.Box, actual.Box)
			require.True(t, actual.Visible)
			require.Equal(t, g.Contract.NodeBorderPx, actual.Border)
			if e.OverlayBox == nil {
				require.Nil(t, actual.Overlay)
			} else {
				require.NotNil(t, actual.Overlay)
				surfaceRect(t, *e.OverlayBox, *actual.Overlay)
			}
			for _, text := range e.Texts {
				textFound := false
				for _, a := range actual.Texts {
					if a.Key != text.Key {
						continue
					}
					textFound = true
					require.Equal(t, text.Text, a.Text)
					require.True(t, a.Visible)
					surfaceRect(t, text.Box, a.Box)
				}
				require.True(t, textFound, "missing group text %s", text.Key)
			}
		}
		require.True(t, found, "missing group element %s", e.ID)
	}
	require.Len(t, got.Connectors, len(g.GroupConnectors))
	for _, c := range g.GroupConnectors {
		found := false
		for _, a := range got.Connectors {
			if c.ID != a.ID {
				continue
			}
			found = true
			require.True(t, a.Visible)
			require.True(t, a.Arrow)
			require.Equal(t, g.Contract.StrokePx, a.StrokePx)
			require.Equal(t, strings.Fields(g.Contract.GroupConnectorDash), strings.Fields(strings.NewReplacer(",", " ", "px", "").Replace(a.Dash)))
			require.Equal(t, surfaceRoutePath(g, c.ID, c.Points, g.Contract.GroupConnectorDash), a.D)
		}
		require.True(t, found, "missing synthetic path %s", c.ID)
	}
}

func TestLayoutGroupSurfaces_WorkflowAndInstanceParity(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, form := range []string{"branches", "workflows", "nested"} {
		t.Run(form, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			child := layoutE2EWorkflow("ownership-child", "Child")
			child.Inputs = []workflow.InputField{{Name: "message", Type: "string"}}
			require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
			s := surfaceServer(t, ds)
			wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, form == "nested")
			if form == "workflows" {
				wf = layoutOwnershipWorkflow(workflow.GroupFormWorkflows, false)
			}
			if form == "branches" {
				wf = layoutTypographyWorkflow()
			}
			surfaceAuthor(t, s.Config.Handler, wf)
			saved := layoutE2EManifest(t, env.privatePath(wf.Name))
			id := surfaceSnapshot(t, ds, saved)
			rec := waitGroupGeometryCheckpoint(t, ds, saved.SavedRevision())
			requireGroupReviewCheckpoint(t, rec)
			page := surfaceBrowser(t)
			for _, route := range []string{"/workflows/" + wf.Name, "/instances/" + id.String()} {
				view := surfaceView(t, s.Config.Handler, "/api/v1"+route+"/layout")
				require.Equal(t, rec.Revision, view.SavedRevision)
				require.Equal(t, rec.ResultGeneration, view.ResultGeneration)
				require.Equal(t, rec.JobGeneration, view.JobGeneration)
				if form == "branches" {
					require.NotEmpty(t, view.Geometry.GroupCrossings, "actual mixed gaps must reach both embedded routes")
					loops := 0
					for _, edge := range view.Geometry.Edges {
						if edge.Kind != layout.EdgeForward {
							loops++
						}
					}
					require.Positive(t, loops)
				}
				surfaceReady(t, page, s.URL+route, "Layout reviewed")
				surfaceAssertGeometry(t, page, view, "Layout reviewed")
				surfaceAssertGroups(t, page, view)
				// Click the actual group header through React Flow; the original
				// definition identity must reach the inspector, not a synthetic ID.
				require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-arranged-node="t2"]').dispatchEvent(new MouseEvent('click',{bubbles:true}))`)))
				require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-node="t2"]').closest('.react-flow__node').classList.contains('selected')`, chromedp.WithPollingTimeout(10*time.Second))))
				surfaceAssertGroups(t, page, view)
			}
		})
	}
}

// Scope existing inspectors to one independently reviewed tile and compare a
// uniformly translated copy of its persisted geometry, never a merged result.
func surfaceAssertTile(t *testing.T, page context.Context, view layout.View, id string) float64 {
	t.Helper()
	raw, err := json.Marshal(id)
	require.NoError(t, err)
	scoped := func(script string) string { return strings.TrimSuffix(script, "})()") + "})(" + string(raw) + ")" }
	script := scoped(surfaceDOM)
	ready := fmt.Sprintf(`(() => { const a=%s; return a.nodes.length===%d && a.edges.length===%d ? a : false; })()`, script, len(view.Geometry.Nodes), len(view.Geometry.Edges))
	got, err := chromedp.Run(page, chromedp.Poll[surfaceDOMResult](ready, chromedp.WithPollingTimeout(20*time.Second)))
	require.NoError(t, err)
	first := view.Geometry.Nodes[0]
	dy := 0.0
	found := false
	for _, n := range got.Nodes {
		if n.ID == first.ID {
			dy = n.Box.Y - first.Box.Y
			found = true
		}
	}
	require.True(t, found)
	require.GreaterOrEqual(t, dy, -0.1, "DOM coordinate conversion may differ by a fractional CSS unit")
	if math.Abs(dy) < 0.1 {
		dy = 0
	}
	b, err := json.Marshal(view)
	require.NoError(t, err)
	var translated layout.View
	require.NoError(t, json.Unmarshal(b, &translated))
	g := translated.Geometry
	g.Canvas.Y += dy
	for i := range g.Nodes {
		g.Nodes[i].Box.Y += dy
	}
	for i := range g.Edges {
		for j := range g.Edges[i].Points {
			g.Edges[i].Points[j].Y += dy
		}
		g.Edges[i].SourceAttach.Point.Y += dy
		g.Edges[i].TargetAttach.Point.Y += dy
		if g.Edges[i].Text != nil {
			g.Edges[i].Text.Box.Y += dy
		}
	}
	for i := range g.Crossings {
		g.Crossings[i].Point.Y += dy
	}
	for i := range g.GroupCrossings {
		g.GroupCrossings[i].Point.Y += dy
	}
	for i := range g.GroupElements {
		e := &g.GroupElements[i]
		e.Box.Y += dy
		if e.OverlayBox != nil {
			e.OverlayBox.Y += dy
		}
		for j := range e.Texts {
			e.Texts[j].Box.Y += dy
		}
	}
	for i := range g.GroupLanes {
		g.GroupLanes[i].Box.Y += dy
	}
	for i := range g.GroupConnectors {
		for j := range g.GroupConnectors[i].Points {
			g.GroupConnectors[i].Points[j].Y += dy
		}
		g.GroupConnectors[i].SourceAttach.Point.Y += dy
		g.GroupConnectors[i].TargetAttach.Point.Y += dy
	}
	surfaceAssertGeometryScript(t, page, translated, "Layout reviewed", script)
	surfaceAssertGroupsScript(t, page, translated, scoped(surfaceGroupDOM))
	t.Logf("tile instance=%s persisted digest=%s translationY=%g", id, layout.GeometryDigest(*view.Geometry), dy)
	return dy
}

func TestLayoutGroupSurfaces_RealGroupedChildTilesAndIndependentOutcome(t *testing.T) {
	browser := requireBrowserE2E(t)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	gates := newGroupProcessGates(t)
	child := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
	child.Name = "ownership-child"
	child.Inputs = []workflow.InputField{{Name: "key", Type: "string", Required: true}}
	for i := range child.Nodes {
		if child.Nodes[i].ID == "a" || child.Nodes[i].ID == "b" {
			child.Nodes[i].Type = workflow.NodeTypeToolCall
			child.Nodes[i].Config = workflow.NodeConfig{Command: "sh", Args: []string{"-c", groupGateScript, "sh", gates.dir, "{{key}}-" + child.Nodes[i].ID}}
		}
	}
	childRevisions := map[string]string{}
	for _, key := range []string{"first", "second"} {
		child.Name = "ownership-child-" + key
		child.ID = uuid.Nil
		child.Presentation = nil
		child.Nodes[1].Label = "Child group " + key
		require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
		childRevisions[key] = layoutE2ESavedRevision(t, env.privatePath(child.Name))
		requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, childRevisions[key]))
	}
	require.NotEqual(t, childRevisions["first"], childRevisions["second"])
	root := layoutOwnershipWorkflow(workflow.GroupFormWorkflows, false)
	for i := range root.Nodes[1].Config.Group.Tasks {
		task := &root.Nodes[1].Config.Group.Tasks[i]
		task.Workflow = "ownership-child-" + task.Key
		task.Inputs = map[string]any{"key": task.Key}
	}
	require.NoError(t, ds.workflowSvc.Create(ctx, root.Name, root))
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, layoutE2ESavedRevision(t, env.privatePath(root.Name))))
	server := surfaceServer(t, ds)
	id, _, err := ds.eng.Trigger(ctx, root.Name, nil, "normal")
	require.NoError(t, err)
	// Always release and join before root/database shutdown, including failures.
	t.Cleanup(func() {
		for _, key := range []string{"first-a", "first-b", "second-a", "second-b"} {
			gates.release(t, key)
		}
		if ds.db.Ping() == nil {
			waitForTerminalState(t, ds, id)
		}
	})
	for _, key := range []string{"first-a", "first-b", "second-a", "second-b"} {
		gates.reached(t, key)
	}
	run := groupRunOf(t, ds, id, "t2")
	first, second := groupRosterTask(t, run, "first").ChildInstanceID, groupRosterTask(t, run, "second").ChildInstanceID
	require.NotEqual(t, first, second)
	page := surfaceBrowser(t)
	surfaceReady(t, page, server.URL+"/instances/"+id.String(), "Layout reviewed")
	rootView := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+id.String()+"/layout")
	require.InDelta(t, 0, surfaceAssertTile(t, page, rootView, id.String()), 0.1)
	graphState, err := ds.graphState.GraphState(ctx, id)
	require.NoError(t, err)
	bottom, tiles := rootView.Geometry.Canvas.Bottom(), 0
	for _, context := range graphState.Contexts {
		if context.Kind != "workflow" {
			continue
		}
		tiles++
		childID := context.InstanceID.String()
		view := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+childID+"/layout")
		require.NotEmpty(t, view.Geometry.GroupElements)
		require.Equal(t, childRevisions[context.TaskKey], view.SavedRevision)
		dy := surfaceAssertTile(t, page, view, childID)
		require.InDelta(t, bottom+rootView.Geometry.Contract.GroupRowGapPx, dy, 0.1, "captured context order must feed non-overlapping translated tiles")
		bottom = dy + view.Geometry.Canvas.Bottom()
	}
	require.Equal(t, 2, tiles)
	// Follow is driven by all four actual running branch bodies. Real user
	// viewport input suspends following, and the actual button restores it.
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[title="Following the active steps"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`(() => { const e=document.querySelector('.react-flow__pane'),r=e.getBoundingClientRect();e.dispatchEvent(new WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:160,clientX:r.x+r.width/2,clientY:r.y+r.height/2})); })()`), chromedp.Poll[chromedp.Void](`document.querySelector('[title="Recenter and follow the active steps"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`document.querySelector('[title="Recenter and follow the active steps"]').click()`), chromedp.Poll[chromedp.Void](`document.querySelector('[title="Following the active steps"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`(() => {const active=[...document.querySelectorAll('[data-arranged-node][data-state="running"]')],r=document.querySelector('.react-flow').getBoundingClientRect();return active.length===4&&active.every(e=>{const b=e.getBoundingClientRect();return b.left>=r.left-1&&b.right<=r.right+1&&b.top>=r.top-1&&b.bottom<=r.bottom+1;});})()`, chromedp.WithPollingTimeout(10*time.Second))))
	railClear, err := chromedp.Run(page, chromedp.Evaluate[bool](`(() => {const a=document.querySelector('[aria-label="Child arrangements"]').getBoundingClientRect(),b=document.querySelector('.react-flow').getBoundingClientRect();return a.right<=b.left+.5||a.bottom<=b.top+.5;})()`))
	require.NoError(t, err)
	require.True(t, railClear, "real child titles/findings/Retry rail must remain outside the painted graph")
	gates.release(t, "first-a")
	waitForState(t, ds, id, instance.StateCompleted)
	// A completed root must continue observing its independently running loser.
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-canvas]').textContent.includes('non-blocking') && [...document.querySelectorAll('[data-group-text]')].some(e=>e.textContent==='winner: first') && [...document.querySelectorAll('[data-group-text]')].some(e=>e.textContent==='gathered as unfinished')`, chromedp.WithPollingTimeout(15*time.Second))))
	secondState, err := ds.eng.GetInstance(ctx, second)
	require.NoError(t, err)
	require.False(t, secondState.State.IsTerminal())
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-node][aria-label="continuing independently"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	// A body belongs to its executing branch, not its workflow tile or root.
	branchOwner := groupRosterTask(t, groupRunOf(t, ds, second, "t2"), "first").ChildInstanceID
	bodyClick := fmt.Sprintf(`[...document.querySelectorAll('[data-arranged-node="b"]')].find(e=>e.dataset.layoutInstance===%q).dispatchEvent(new MouseEvent('click',{bubbles:true}))`, second.String())
	ownerSink := fmt.Sprintf(`document.querySelector('[data-testid="trace-owner"]')?.textContent.includes(%q)`, branchOwner.String()[:8])
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](bodyClick), chromedp.Poll[chromedp.Void](ownerSink, chromedp.WithPollingTimeout(10*time.Second)), chromedp.Evaluate[chromedp.Void](`[...document.querySelectorAll('button')].find(e=>e.textContent==='Variables').click()`), chromedp.Poll[chromedp.Void](`[...document.querySelectorAll('tr')].some(r=>r.children[0]?.textContent==='key'&&r.children[1]?.textContent==='second')`, chromedp.WithPollingTimeout(10*time.Second))))
	// Selection must open the original grouped child, not the root definition.
	click := fmt.Sprintf(`[...document.querySelectorAll('[data-arranged-node="t2"]')].find(e=>e.dataset.layoutInstance===%q).dispatchEvent(new MouseEvent('click',{bubbles:true}))`, second.String())
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](click), chromedp.Poll[chromedp.Void](`document.querySelector('[data-testid="group-panel"]') !== null && document.querySelector('[data-testid="group-task-row-second"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	gates.release(t, "second-a")
	waitForState(t, ds, second, instance.StateCompleted)
	lateLoser := fmt.Sprintf(`(() => { const e=[...document.querySelectorAll('[data-arranged-group-element]')].find(e=>e.dataset.layoutInstance===%q && e.dataset.arrangedGroupElement===%q); const texts=[...(e?.querySelectorAll('[data-group-text]')??[])].map(e=>e.textContent);return texts.includes('completed')&&texts.includes('gathered as unfinished'); })()`, id.String(), layout.GroupElementID("t2", "task", "second"))
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](lateLoser, chromedp.WithPollingTimeout(15*time.Second))))
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-testid="group-panel"]').textContent.includes('success')`, chromedp.WithPollingTimeout(15*time.Second))))
	for _, childID := range []string{id.String(), first.String(), second.String()} {
		surfaceAssertTile(t, page, surfaceView(t, server.Config.Handler, "/api/v1/instances/"+childID+"/layout"), childID)
	}
	gates.release(t, "first-b")
	gates.release(t, "second-b")
	// The bounded processes are released before slower storage/restart controls.
	for _, childID := range []uuid.UUID{first, second} {
		childRun := groupRunOf(t, ds, childID, "t2")
		for _, task := range childRun.Tasks {
			waitForState(t, ds, task.ChildInstanceID, instance.StateCompleted)
		}
	}
	surfaceGroupHistoryControls(t, page, ds, env, root, id, first, second)
}

func TestLayoutGroupSurfaces_IncompatibleStoredGroupsRemainFallback(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, kind := range []string{"nil", "v1", "missing ownership"} {
		t.Run(kind, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
			useLayoutE2EBrowser(t, browser)
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
			require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
			saved := layoutE2EManifest(t, env.privatePath(wf.Name))
			id := surfaceSnapshot(t, ds, saved)
			rec := waitGroupGeometryCheckpoint(t, ds, saved.SavedRevision())
			requireGroupReviewCheckpoint(t, rec)
			stored, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			ds.layout.stop()
			if kind == "v1" {
				stored.Geometry.Format = layout.FormatV1
			}
			if kind == "missing ownership" {
				stored.Geometry.GroupElements[0].GroupNodeID = ""
			}
			raw, err := json.Marshal(stored.Geometry)
			require.NoError(t, err)
			if kind == "nil" {
				raw = []byte("null")
			}
			_, err = ds.db.Exec("UPDATE layout_results SET geometry=? WHERE revision=? AND generation=?", raw, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			err = os.Remove(filepath.Join(filepath.Dir(env.privatePath(wf.Name)), "layout.json"))
			require.True(t, err == nil || os.IsNotExist(err))
			server := surfaceServer(t, ds)
			page := surfaceBrowser(t)
			for _, route := range []string{"/workflows/" + wf.Name, "/instances/" + id.String()} {
				view := surfaceView(t, server.Config.Handler, "/api/v1"+route+"/layout")
				require.True(t, view.Unreviewed)
				require.True(t, view.RetryEligible)
				require.Nil(t, view.Geometry)
				require.NotNil(t, view.Groups)
				require.NoError(t, chromedp.Do(page, chromedp.Navigate(server.URL+route), chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-canvas][data-layout-mode="fallback"]')!==null && [...document.querySelectorAll('button')].some(e=>e.textContent==='Retry arrangement')`, chromedp.WithPollingTimeout(15*time.Second))))
				dom, err := chromedp.Run(page, chromedp.Evaluate[string](`document.querySelector('[data-arranged-canvas]').textContent`))
				require.NoError(t, err)
				require.Contains(t, dom, wf.Nodes[1].Label)
				require.NotContains(t, surfaceInspect(t, page).Panel, "Layout reviewed")
			}
		})
	}
}

func TestLayoutGroupSurfaces_ResolutionCalloutAnchorsReviewedFooter(t *testing.T) {
	browser := requireBrowserE2E(t)
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	// Reuse the existing subprocess transcript fixture, not a session/GraphState
	// fake. It changes no files: only the actual Git index can resolve this run.
	cli := filepath.Join(env.root, "resolver-cli")
	require.NoError(t, os.WriteFile(cli, []byte(fakeResolverScript), 0700))
	cfg, err := os.ReadFile(env.configPath)
	require.NoError(t, err)
	// Join the env's profile list; a pinned profile names a fixed model, here the
	// one fakeResolverScript reports executing.
	cfg = bytes.Replace(cfg, []byte("profiles:\n"), []byte("profiles:\n  - alias: resolver\n    kind: agent\n    model: \"claude-sonnet-4-5\"\n    allowedtools: [Read, Edit, Bash]\n"), 1)
	cfg = append(cfg, []byte("llm:\n  binarypath: "+yamlQuote(cli)+"\n")...)
	require.NoError(t, os.WriteFile(env.configPath, cfg, 0600))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(filesConflictManifest), &wf))
	require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, &wf))
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, layoutE2ESavedRevision(t, env.privatePath(wf.Name))))
	id, _, err := ds.eng.Trigger(ctx, wf.Name, nil, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ds.eng.Cancel(ctx, id) })
	run := waitForOpenResolution(t, ds, id)
	require.NotNil(t, run.Gather.ActiveSession)
	server := surfaceServer(t, ds)
	page := surfaceBrowser(t)
	surfaceReady(t, page, server.URL+"/instances/"+id.String(), "Layout reviewed")
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-arranged-node="group-1"]').dispatchEvent(new MouseEvent('click',{bubbles:true}))`)))
	open := `[...document.querySelectorAll('button')].find(e=>e.textContent.includes('Open resolution session'))`
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`!!(`+open+`)`, chromedp.WithPollingTimeout(15*time.Second)), chromedp.Evaluate[chromedp.Void](open+`.click()`)))
	footer := layout.GroupElementID("group-1", "footer", "")
	matches := fmt.Sprintf(`(() => { const e=[...document.querySelectorAll('[data-arranged-group-element]')].find(e=>e.dataset.arrangedGroupElement===%q),circle=document.querySelector('svg circle[r="4"]'),line=document.querySelector('svg line[marker-end="url(#callout-arrowhead)"]'); if(!e||!circle||!line)return false;const r=e.getBoundingClientRect();return Math.abs(+circle.getAttribute('cx')-r.right)<1&&Math.abs(+circle.getAttribute('cy')-(r.y+r.height/2))<1; })()`, footer)
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](matches, chromedp.WithPollingTimeout(15*time.Second))))
	view := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+id.String()+"/layout")
	surfaceAssertTile(t, page, view, id.String())
	resolveConflictInStaging(t, run, "resolved\n")
	_, err = ds.conversationHandler.EndSession(ctx, id)
	require.NoError(t, err)
	waitForState(t, ds, id, instance.StateCompleted)
}

func surfaceGroupHistoryControls(t *testing.T, page context.Context, ds *domainStack, env *layoutE2EEnv, root *workflow.Workflow, rootID, first, second uuid.UUID) {
	t.Helper()
	ctx := adminCtxForLocalUser(t, ds)
	baseline := map[uuid.UUID]layout.View{}
	for _, id := range []uuid.UUID{rootID, first, second} {
		var err error
		baseline[id], err = ds.layout.query.InstanceLayout(ctx, id)
		require.NoError(t, err)
		require.False(t, baseline[id].Unreviewed)
	}
	// Use a real failure-only HTTP boundary; all successful responses, sessions,
	// graph-state and layout bytes still come from the production router.
	handler := surfaceServer(t, ds).Config.Handler
	var mu sync.Mutex
	outage := false
	var requests []string
	secondPath := "/api/v1/instances/" + second.String() + "/layout"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		fail := outage && r.URL.Path == secondPath
		mu.Unlock()
		if fail {
			http.Error(w, "layout status storage outage", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	surfaceReady(t, page, server.URL+"/instances/"+rootID.String(), "Layout reviewed")
	for _, id := range []uuid.UUID{rootID, first, second} {
		surfaceAssertTile(t, page, baseline[id], id.String())
	}
	mu.Lock()
	outage = true
	mu.Unlock()
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`)))
	rail := fmt.Sprintf(`[...document.querySelectorAll('[data-layout-status-instance]')].find(e=>e.dataset.layoutStatusInstance===%q)`, second.String())
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](rail+`?.textContent.includes('Could not refresh arrangement status')`, chromedp.WithPollingTimeout(15*time.Second))))
	surfaceAssertTile(t, page, baseline[first], first.String())
	require.Equal(t, baseline[rootID], surfaceView(t, handler, "/api/v1/instances/"+rootID.String()+"/layout"))
	mu.Lock()
	outage = false
	mu.Unlock()
	// Remove only this child's retained result; the other child's distinct
	// revision and the reviewed root must not inherit its generation or error.
	_, err := ds.db.Exec("DELETE FROM layout_results WHERE revision=?", baseline[second].SavedRevision)
	require.NoError(t, err)
	for _, name := range []string{root.Name, "ownership-child-first", "ownership-child-second"} {
		require.NoError(t, os.Remove(layoutCacheFile(t, filepath.Dir(env.privatePath(name)))))
	}
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`), chromedp.Poll[chromedp.Void](rail+`?.textContent.includes('Retry arrangement')`, chromedp.WithPollingTimeout(15*time.Second))))
	click := rail + `.querySelector('button').click()`
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](click)))
	require.Eventually(t, func() bool {
		v, err := ds.layout.query.InstanceLayout(ctx, second)
		return err == nil && !v.Unreviewed && v.ResultGeneration > baseline[second].ResultGeneration
	}, groupBrowserTimeout, 50*time.Millisecond)
	updated, err := ds.layout.query.InstanceLayout(ctx, second)
	require.NoError(t, err)
	require.Equal(t, baseline[second].JobGeneration+1, updated.JobGeneration)
	require.Equal(t, layout.GeometryDigest(*baseline[second].Geometry), layout.GeometryDigest(*updated.Geometry))
	surfaceAssertTile(t, page, updated, second.String())
	surfaceAssertTile(t, page, baseline[first], first.String())
	baseline[second] = updated
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision=?", baseline[rootID].SavedRevision)
	require.NoError(t, err)
	rootRetry := `[...document.querySelectorAll('button')].find(e=>e.textContent==='Retry arrangement'&&!e.closest('[data-layout-status-instance]'))`
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`), chromedp.Poll[chromedp.Void](`!!(`+rootRetry+`)`, chromedp.WithPollingTimeout(15*time.Second))))
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](rootRetry+`.click()`)))
	require.Eventually(t, func() bool {
		v, err := ds.layout.query.InstanceLayout(ctx, rootID)
		return err == nil && !v.Unreviewed && v.ResultGeneration > baseline[rootID].ResultGeneration
	}, groupBrowserTimeout, 50*time.Millisecond)
	rootRetried, err := ds.layout.query.InstanceLayout(ctx, rootID)
	require.NoError(t, err)
	require.Equal(t, baseline[rootID].JobGeneration+1, rootRetried.JobGeneration)
	require.Equal(t, layout.GeometryDigest(*baseline[rootID].Geometry), layout.GeometryDigest(*rootRetried.Geometry))
	baseline[rootID] = rootRetried
	for _, id := range []uuid.UUID{rootID, first, second} {
		surfaceAssertTile(t, page, baseline[id], id.String())
	}
	mu.Lock()
	observedRetries := append([]string(nil), requests...)
	mu.Unlock()
	var retryPaths []string
	for _, path := range observedRetries {
		if strings.HasSuffix(path, "/layout/retry") {
			retryPaths = append(retryPaths, path)
		}
	}
	require.Equal(t, []string{secondPath + "/retry", "/api/v1/instances/" + rootID.String() + "/layout/retry"}, retryPaths)
	// A later authoring save moves only WorkflowReview; these three immutable
	// snapshots keep their own reviewed bytes after a real root restart.
	root.Nodes[1].Label = "External later save"
	require.NoError(t, ds.workflowSvc.Update(ctx, root.Name, root))
	currentRevision := layoutE2ESavedRevision(t, env.privatePath(root.Name))
	require.NotEqual(t, baseline[rootID].SavedRevision, currentRevision)
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, currentRevision))
	server.Close()
	ds.Close(context.Background())
	restarted, _ := env.boot(t)
	fresh := surfaceServer(t, restarted)
	for _, id := range []uuid.UUID{rootID, first, second} {
		v := surfaceView(t, fresh.Config.Handler, "/api/v1/instances/"+id.String()+"/layout")
		require.Equal(t, baseline[id].SavedRevision, v.SavedRevision)
		require.Equal(t, baseline[id].ResultGeneration, v.ResultGeneration)
		require.Equal(t, layout.GeometryDigest(*baseline[id].Geometry), layout.GeometryDigest(*v.Geometry))
	}
	surfaceReady(t, page, fresh.URL+"/instances/"+rootID.String(), "Layout reviewed")
	for _, id := range []uuid.UUID{rootID, first, second} {
		surfaceAssertTile(t, page, baseline[id], id.String())
	}
	current := surfaceView(t, fresh.Config.Handler, "/api/v1/workflows/"+root.Name+"/layout")
	require.Equal(t, currentRevision, current.SavedRevision)
	surfaceReady(t, page, fresh.URL+"/workflows/"+root.Name, "Layout reviewed")
	surfaceAssertGeometry(t, page, current, "Layout reviewed")
	surfaceAssertGroups(t, page, current)
	// Real org-session authorization now removes the second child's context.
	// No GraphState or successful HTTP response is manufactured for redaction.
	require.NoError(t, restarted.userRepo.UpdateRole(ctx, localDefaultUser, string(identity.RoleUser)))
	other := &instance.User{ID: uuid.New(), Username: "hidden-child-owner", Role: identity.RoleUser}
	require.NoError(t, restarted.userRepo.Create(ctx, other))
	deps := restarted.routerDeps(nil, dashboard.Handler(), nil, nil)
	deps.Auth = adapthttp.NewAuthMiddleware(identity.ModeOrg, "session", restarted.sessionRepo, restarted.tokenRepo, restarted.userRepo, identity.GroupConfig{DefaultGroup: "default"})
	t.Cleanup(deps.Auth.Stop)
	require.NoError(t, restarted.sessionRepo.Create(ctx, &identity.Session{ID: "group-surface-session", UserID: localDefaultUser, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}))
	orgHandler := adapthttp.NewRouterFromDeps(deps)
	mu.Lock()
	requests = nil
	mu.Unlock()
	org := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		orgHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(org.Close)
	_, err = chromedp.Call(page, network.SetCookie, network.SetCookieParams{Name: "session", Value: "group-surface-session", URL: org.URL})
	require.NoError(t, err)
	surfaceReady(t, page, org.URL+"/instances/"+rootID.String(), "Layout reviewed")
	surfaceAssertTile(t, page, baseline[second], second.String())
	_, err = restarted.db.Exec("UPDATE instances SET owner_id=?,visibility='private' WHERE id=?", other.ID.String(), second.String())
	require.NoError(t, err)
	// The already mounted aggregate's real poll must remove the context and
	// abort its layout hook, rather than relying on a navigation to clear it.
	// Completed graphs use useGraphState's 30-second inactive poll interval.
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-arranged-canvas]')?.textContent.includes('hidden dependency')`, chromedp.WithPollingTimeout(40*time.Second))))
	mu.Lock()
	requests = nil
	mu.Unlock()
	require.NoError(t, chromedp.Do(page, chromedp.Evaluate[chromedp.Void](`window.dispatchEvent(new Event('focus'))`)))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(requests, "/api/v1/instances/"+first.String()+"/layout")
	}, 5*time.Second, 10*time.Millisecond, "visible child must still refresh after removal of its hidden sibling")
	body, err := chromedp.Run(page, chromedp.Evaluate[string](`document.body.innerHTML`))
	require.NoError(t, err)
	require.NotContains(t, body, second.String())
	require.NotContains(t, body, "ownership-child-second", "the redacted card and removed rail cannot reveal the configured child's title")
	mu.Lock()
	observed := append([]string(nil), requests...)
	mu.Unlock()
	for _, path := range observed {
		require.NotContains(t, path, second.String(), "hidden child must not receive a layout, trace or link request")
	}
}

// Delay only the already measured group's completion; the real browser,
// coordinator and reviewer still produce the retained result after release.
type surfaceGroupMeasureGate struct {
	layout.Renderer
	entered chan<- struct{}
	release <-chan struct{}
}

func (g surfaceGroupMeasureGate) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	m, err := g.Renderer.Measure(ctx, req)
	if err != nil {
		return m, err
	}
	for _, item := range req.Items {
		if strings.HasPrefix(item.Key, `["text",`) {
			select {
			case g.entered <- struct{}{}:
			default:
			}
			select {
			case <-g.release:
				return m, nil
			case <-ctx.Done():
				return m, ctx.Err()
			}
		}
	}
	return m, nil
}

func TestLayoutGroupSurfaces_CompletedInstanceReceivesLateReview(t *testing.T) {
	browser := requireBrowserE2E(t)
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	previous := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = previous })
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
		return surfaceGroupMeasureGate{renderer, entered, release}, reviewer, closeFn, err
	}
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := env.boot(t)
	server := surfaceServer(t, ds)
	wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, false)
	surfaceAuthor(t, server.Config.Handler, wf)
	revision := layoutE2ESavedRevision(t, env.privatePath(wf.Name))
	select {
	case <-entered:
	case <-time.After(groupBrowserTimeout):
		t.Fatal("worker did not measure the saved group")
	}
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), wf.Name, nil, "normal")
	require.NoError(t, err)
	waitForState(t, ds, id, instance.StateCompleted)
	page := surfaceBrowser(t)
	require.NoError(t, chromedp.Do(page, chromedp.Navigate(server.URL+"/instances/"+id.String()), chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="fallback"]') !== null`, chromedp.WithPollingTimeout(10*time.Second))))
	pending := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+id.String()+"/layout")
	require.True(t, pending.Unreviewed)
	require.Nil(t, pending.Geometry)
	unblock()
	requireGroupReviewCheckpoint(t, waitGroupGeometryCheckpoint(t, ds, revision))
	require.NoError(t, chromedp.Do(page, chromedp.Poll[chromedp.Void](`document.querySelector('[data-layout-mode="arranged"]') !== null && [...document.querySelectorAll('[role="status"]')].some(e=>e.textContent.includes('Layout reviewed'))`, chromedp.WithPollingTimeout(15*time.Second))))
	view := surfaceView(t, server.Config.Handler, "/api/v1/instances/"+id.String()+"/layout")
	require.Equal(t, revision, view.SavedRevision)
	surfaceAssertTile(t, page, view, id.String())
}

func layoutOwnershipWorkflow(form string, nested bool) *workflow.Workflow {
	wf := layoutE2EWorkflow("group-ownership", "保存したグループ ✅")
	wf.Nodes[1].Type = workflow.NodeTypeParallelGroup
	wf.Nodes[1].Config = workflow.NodeConfig{Group: &workflow.GroupConfig{
		Form: form, CompletionRule: workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedFinishIndependently, GatherMode: workflow.GroupGatherResults,
		Tasks: []workflow.GroupTask{{Key: "second", Workflow: "ownership-child", Inputs: map[string]any{"message": "保存した入力 ✅"}}, {Key: "first", Workflow: "ownership-child"}},
	}}
	oops := wf.Nodes[2]
	oops.ID = "oops"
	wf.Nodes = append(wf.Nodes, oops)
	wf.Edges = append(wf.Edges, workflow.Edge{Source: "t2", Target: "oops", Output: workflow.GroupPortFailure})
	wf.Edges[1].Output = workflow.GroupPortSuccess
	if form == workflow.GroupFormBranches {
		for i, id := range []string{"a", "b"} {
			node := wf.Nodes[2]
			node.ID = id
			wf.Nodes = append(wf.Nodes, node)
			wf.Nodes[1].Config.Group.Tasks[i].Workflow = ""
			wf.Nodes[1].Config.Group.Tasks[i].Entry = id
			wf.Edges = append(wf.Edges, workflow.Edge{Source: "t2", Target: id, Output: workflow.GroupPortTask, Label: "Same task port"})
		}
	}
	if nested {
		inner := layoutOwnershipWorkflow(workflow.GroupFormWorkflows, false)
		wf.Nodes[4].Type = workflow.NodeTypeParallelGroup
		wf.Nodes[4].Config = inner.Nodes[1].Config
		for _, id := range []string{"inner-done", "inner-oops"} {
			node := wf.Nodes[2]
			node.ID = id
			wf.Nodes = append(wf.Nodes, node)
		}
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "inner-done", Output: workflow.GroupPortSuccess}, workflow.Edge{Source: "a", Target: "inner-oops", Output: workflow.GroupPortFailure})
	}
	return wf
}

// Ownership/input proof only: the worker is stopped so no ordinary-box result
// can accidentally stand in for the v2 reviewed-geometry acceptance gate.
func TestLayoutGroupSurfaces_RevisionBoundOwnershipBeforeGeometry(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{{"branches", "branches", false}, {"workflows", "workflows", false}, {"nested", "branches", true}} {
		t.Run(tc.name, func(t *testing.T) {
			env := newLayoutE2EEnv(t, layoutE2EOptions{})
			ds, _ := env.boot(t)
			ds.layout.stop()
			ctx := adminCtxForLocalUser(t, ds)
			router := newLayoutE2ERouter(t, ds)
			child := layoutE2EWorkflow("ownership-child", "Child")
			child.Inputs = []workflow.InputField{{Name: "message", Type: "string"}}
			require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
			wf := layoutOwnershipWorkflow(tc.form, tc.nested)
			require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
			saved := layoutE2EManifest(t, env.privatePath(wf.Name))
			want, findings := workflow.AnalyzeGroups(saved)
			require.Empty(t, findings)
			require.NotEmpty(t, want.Order)
			id := surfaceSnapshot(t, ds, saved)
			// Save a different config/order while the authorized instance retains
			// the original snapshot. Read neither group map from a moving lookup.
			changed := layoutOwnershipWorkflow(tc.form, tc.nested)
			cfg := changed.Nodes[1].Config.Group
			cfg.Tasks[0], cfg.Tasks[1] = cfg.Tasks[1], cfg.Tasks[0]
			cfg.CompletionRule = workflow.GroupRuleAllSucceed
			cfg.UnfinishedBehavior = workflow.GroupUnfinishedWaitForRemaining
			require.NoError(t, ds.workflowSvc.Update(ctx, wf.Name, changed))
			current := layoutE2EManifest(t, env.privatePath(wf.Name))
			require.NotEqual(t, saved.SavedRevision(), current.SavedRevision())
			currentGroups, findings := workflow.AnalyzeGroups(current)
			require.Empty(t, findings)
			before, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			for _, read := range []struct {
				path, revision string
				groups         *workflow.GroupGraph
			}{
				{"/api/v1/workflows/" + wf.Name + "/layout", current.SavedRevision(), currentGroups},
				{"/api/v1/instances/" + id.String() + "/layout", saved.SavedRevision(), want},
			} {
				status, body := layoutE2EDo(t, router, http.MethodGet, read.path, "")
				require.Equal(t, http.StatusOK, status, "%s: %v", read.path, body)
				assert.Equal(t, read.revision, body["savedRevision"])
				require.NotNil(t, body["groups"], "the real Service → Reader → authorized HTTP body must feed group ownership even while pending")
				got, err := json.Marshal(body["groups"])
				require.NoError(t, err)
				expected, err := json.Marshal(read.groups)
				require.NoError(t, err)
				assert.JSONEq(t, string(expected), string(got))
				assert.Equal(t, "pending", body["displayStatus"])
				assert.Equal(t, true, body["unreviewed"])
				assert.NotContains(t, body, "geometry", "ownership is not reviewed arrangement")
			}
			after, err := os.ReadFile(env.privatePath(wf.Name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "derived ownership is response data, never an authoring mutation")
		})
	}
}

// Record exactly what the live worker handed the shipped renderer, per saved
// revision. Deliberately omit group report transport after actual capture to
// keep the independent immutable-revision Retry control actionable.
type ownershipObservingRenderer struct {
	layout.Renderer
	mu   *sync.Mutex
	seen map[string][]*workflow.GroupGraph
}

func (r ownershipObservingRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	r.mu.Lock()
	r.seen[req.Revision] = append(r.seen[req.Revision], req.WorkflowGroups)
	r.mu.Unlock()
	out, err := r.Renderer.Render(ctx, req)
	if req.WorkflowGroups != nil {
		out.Report.GroupElements = nil
	}
	return out, err
}

// paired:7 — the worker and Reader derive ownership from the same saved
// revision independently. After R1 fails review and R2 reorders the
// tasks, the frozen instance view must pair R1's retained unreviewed best with
// the ownership R1's renderer received, and the workflow view must pair R2's
// with R2's; a Retry of R1 must feed the renderer the same R1 ownership again.
func TestLayoutGroupSurfaces_ReaderOwnershipMatchesRenderedRevision(t *testing.T) {
	browser := requireBrowserE2E(t)
	var mu sync.Mutex
	seen := map[string][]*workflow.GroupGraph{}
	rendered := func(revision string) []*workflow.GroupGraph {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen[revision])
	}
	previous := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = previous })
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
		if err != nil {
			return nil, nil, nil, err
		}
		return ownershipObservingRenderer{Renderer: renderer, mu: &mu, seen: seen}, reviewer, closeFn, nil
	}
	env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
	ds, _ := env.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	router := newLayoutE2ERouter(t, ds)
	child := layoutE2EWorkflow("ownership-child", "Child")
	child.Inputs = []workflow.InputField{{Name: "message", Type: "string"}}
	require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, child))
	wf := layoutOwnershipWorkflow(workflow.GroupFormBranches, true)
	require.NoError(t, ds.workflowSvc.Create(ctx, wf.Name, wf))
	r1 := layoutE2EManifest(t, env.privatePath(wf.Name))
	id := surfaceSnapshot(t, ds, r1)
	rec1 := layoutRenderWaitTerminal(t, ds, r1.SavedRevision(), time.Minute)
	require.Equal(t, layout.StateNeedsAttention, rec1.State)
	require.Equal(t, "no_refinement", rec1.FailureReason)
	require.Contains(t, rec1.Findings[0].Evidence, "group element/connector/crossing coverage")
	require.Positive(t, rec1.ResultGeneration, "complete v2 geometry is retained, but missing group report transport is never reviewed")
	stored1, err := ds.layout.store.Result(ctx, r1.SavedRevision(), rec1.ResultGeneration)
	require.NoError(t, err)

	changed := layoutOwnershipWorkflow(workflow.GroupFormBranches, true)
	cfg := changed.Nodes[1].Config.Group
	cfg.Tasks[0], cfg.Tasks[1] = cfg.Tasks[1], cfg.Tasks[0]
	require.NoError(t, ds.workflowSvc.Update(ctx, wf.Name, changed))
	r2 := layoutE2EManifest(t, env.privatePath(wf.Name))
	require.NotEqual(t, r1.SavedRevision(), r2.SavedRevision())
	rec2 := layoutRenderWaitTerminal(t, ds, r2.SavedRevision(), time.Minute)
	require.Equal(t, layout.StateNeedsAttention, rec2.State)
	require.Equal(t, "no_refinement", rec2.FailureReason)

	jsonOf := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return string(b)
	}
	sent1, sent2 := rendered(r1.SavedRevision()), rendered(r2.SavedRevision())
	require.Len(t, sent1, 1, "missing group report transport blocks the first actual capture")
	require.Len(t, sent2, 1)
	require.NotNil(t, sent1[0])
	require.NotNil(t, sent2[0])
	require.NotEqual(t, jsonOf(sent1[0]), jsonOf(sent2[0]), "the reordered save must change the ownership the renderer receives")

	for _, read := range []struct {
		path, revision string
		sent           *workflow.GroupGraph
		other          *workflow.GroupGraph
	}{
		{"/api/v1/instances/" + id.String() + "/layout", r1.SavedRevision(), sent1[0], sent2[0]},
		{"/api/v1/workflows/" + wf.Name + "/layout", r2.SavedRevision(), sent2[0], sent1[0]},
	} {
		status, body := layoutE2EDo(t, router, http.MethodGet, read.path, "")
		require.Equal(t, http.StatusOK, status, "%s: %v", read.path, body)
		assert.Equal(t, read.revision, body["savedRevision"], read.path)
		assert.JSONEq(t, jsonOf(read.sent), jsonOf(body["groups"]), "%s must pair its geometry with the ownership its own renderer received", read.path)
		assert.NotEqual(t, jsonOf(read.other), jsonOf(body["groups"]), read.path)
		assert.Equal(t, string(layout.DisplayNeedsAttention), body["displayStatus"], read.path)
		assert.Equal(t, "no_refinement", body["failureReason"], read.path)
		assert.Equal(t, true, body["unreviewed"], "%s: a failed group best is never labelled reviewed", read.path)
		assert.Equal(t, true, body["retryEligible"], read.path)
		assert.NotContains(t, body, "evidence", read.path)
	}
	inst, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, inst.Geometry)
	assert.Equal(t, layout.GeometryDigest(stored1.Geometry), layout.GeometryDigest(*inst.Geometry), "the instance view shows R1's own retained best, not R2's")

	ack, err := ds.layout.query.RetryInstance(ctx, id, r1.SavedRevision())
	require.NoError(t, err)
	assert.Equal(t, rec1.JobGeneration+1, ack.JobGeneration)
	require.Eventually(t, func() bool {
		rec, err := ds.layout.store.Get(ctx, r1.SavedRevision())
		return err == nil && rec.JobGeneration == ack.JobGeneration && rec.State.Terminal()
	}, time.Minute, 20*time.Millisecond)
	sent1 = rendered(r1.SavedRevision())
	require.Len(t, sent1, 2)
	assert.JSONEq(t, jsonOf(sent1[0]), jsonOf(sent1[1]), "Retry re-renders R1 from its immutable row, never the moved workflow file")
	inst, err = ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.JSONEq(t, jsonOf(sent1[1]), jsonOf(inst.Groups))
	assert.Equal(t, layout.DisplayNeedsAttention, inst.DisplayStatus)
	assert.True(t, inst.Unreviewed)
}
